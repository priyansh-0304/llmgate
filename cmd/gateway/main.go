// Command gateway is the LLM gateway: a reverse proxy in front of local
// inference servers, with rate limiting, caching, token accounting and metrics.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"

	"llmgate/internal/budget"
	"llmgate/internal/cache"
	"llmgate/internal/config"
	"llmgate/internal/metrics"
	"llmgate/internal/middleware"
	"llmgate/internal/pricing"
	"llmgate/internal/proxy"
	"llmgate/internal/ratelimit"
	"llmgate/internal/reqid"
	"llmgate/internal/stats"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// run() returns an error instead of calling os.Exit deep inside, so that
	// deferred cleanups run. main only decides the exit code.
	if err := run(log); err != nil {
		log.Error("gateway stopped with error", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	table, err := pricing.Load(cfg.PricingFile)
	if err != nil {
		return err
	}

	m := metrics.New()
	st := stats.New()

	// Pick Redis-backed or in-memory implementations of the three interfaces.
	var (
		limiter     ratelimit.Limiter
		respCache   cache.Cache
		tokenBudget budget.Store
	)
	if cfg.RedisAddr == "" {
		log.Warn("REDIS_ADDR not set: using in-memory rate limiter, cache and token budget (single process only)")
		limiter = ratelimit.NewMemory(cfg.RateCapacity, cfg.RateRefillPerSec)
		respCache = cache.NewMemory()
		tokenBudget = budget.NewMemory(cfg.TokenBudgetWindow)
	} else {
		rdb := redis.NewClient(&redis.Options{
			Addr:        cfg.RedisAddr,
			Password:    cfg.RedisPassword,
			DialTimeout: 2 * time.Second,
			ReadTimeout: 500 * time.Millisecond, // keep Redis from stalling requests
		})
		defer rdb.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := rdb.Ping(ctx).Err(); err != nil {
			// Not fatal: in Kubernetes the gateway may start before Redis is ready.
			log.Warn("redis not reachable yet", "addr", cfg.RedisAddr, "err", err)
		}
		cancel()
		limiter = ratelimit.NewRedis(rdb, cfg.RateCapacity, cfg.RateRefillPerSec)
		respCache = cache.NewRedis(rdb, log)
		tokenBudget = budget.NewRedis(rdb, cfg.TokenBudgetWindow)
	}

	gw := proxy.New(proxy.Options{
		Routes: []proxy.Route{
			{
				Name:        "ollama",
				BaseURL:     cfg.OllamaURL,
				Model:       cfg.OllamaModel,
				KnownModels: []string{cfg.OllamaModel},
			},
			{
				Name:        "mock",
				BaseURL:     cfg.MockURL,
				Model:       "mock-fast",
				KnownModels: []string{"mock-fast", "mock-slow", "mock-flaky", "mock-down"},
			},
		},
		DefaultRoute:    cfg.DefaultRoute,
		Cache:           respCache,
		CacheTTL:        cfg.CacheTTL,
		Metrics:         m,
		Stats:           st,
		Pricing:         table,
		UpstreamTimeout: cfg.UpstreamTimeout,
		MaxBodyBytes:    cfg.MaxBodyBytes,
		Logger:          log,

		Retries:         cfg.MaxRetries,
		RetryBackoff:    cfg.RetryBackoff,
		BreakerFailures: cfg.BreakerFailures,
		BreakerCooldown: cfg.BreakerCooldown,
		FallbackRoute:   cfg.FallbackRoute,

		Budget:    budgetIfEnabled(tokenBudget, cfg.TokenBudget),
		BudgetKey: middleware.ClientKey,
	})

	// Only the LLM endpoint is authenticated, rate limited and budgeted.
	chat := middleware.Chain(
		http.HandlerFunc(gw.ChatCompletions),
		middleware.Auth(cfg.GatewayAPIKey),
		middleware.RateLimit(limiter, m, st, cfg.RateFailOpen, log),
		middleware.TokenBudget(tokenBudget, cfg.TokenBudget, m, st, cfg.RateFailOpen, log),
	)

	// Go 1.22 ServeMux understands "METHOD /path" patterns.
	mux := http.NewServeMux()
	mux.Handle("POST /v1/chat/completions", chat)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("GET /metrics", promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /api/stats", gw.Stats)
	if cfg.DemoEndpoint {
		// Off by default. It lets the dashboard send a fixed, tiny request using the
		// gateway's own key, so the key never reaches the browser.
		mux.Handle("POST /api/demo", demoHandler(chat, cfg.GatewayAPIKey))
		log.Warn("DEMO_ENDPOINT is on: POST /api/demo sends test requests without a client key; use for local demos only")
	}

	// RequestID is outermost so every later layer (and every log line) can use it.
	root := middleware.Chain(mux,
		reqid.Middleware,
		middleware.Recover(log),
		middleware.Instrument(m),
	)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           root,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		// No WriteTimeout: it would abort long streaming responses.
	}

	// Graceful shutdown: on SIGINT/SIGTERM (Ctrl+C, or `kubectl delete pod`)
	// stop accepting new requests and let in-flight ones finish.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { // a goroutine: runs concurrently with the code below
		log.Info("gateway listening", "addr", cfg.ListenAddr, "default_route", cfg.DefaultRoute,
			"fallback_route", cfg.FallbackRoute, "retries", cfg.MaxRetries,
			"breaker_failures", cfg.BreakerFailures, "token_budget", cfg.TokenBudget,
			"auth", cfg.GatewayAPIKey != "", "redis", cfg.RedisAddr != "")
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
	return nil
}

// budgetIfEnabled returns nil when no budget is configured, so the proxy skips
// token accounting entirely (and never touches Redis for it).
func budgetIfEnabled(s budget.Store, limit int64) budget.Store {
	if limit <= 0 {
		return nil
	}
	return s
}
