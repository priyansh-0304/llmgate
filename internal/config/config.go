// Package config loads gateway settings from environment variables.
//
// Why environment variables? In Kubernetes, a Secret or ConfigMap is injected
// into the container as env vars, so the Go code never needs to know where the
// values came from and no secret is ever baked into an image or a frontend bundle.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds every tunable setting. A struct is Go's way of grouping fields
// (similar to a class with only data).
type Config struct {
	ListenAddr string // e.g. ":8080"

	// Redis. Leave RedisAddr empty to use in-memory fallbacks (handy for a
	// first `go run` with nothing else installed).
	RedisAddr     string
	RedisPassword string // comes from a Kubernetes Secret

	// GatewayAPIKey protects /v1/*. Empty means "no auth" (local dev only).
	GatewayAPIKey string // comes from a Kubernetes Secret

	DefaultRoute string // "ollama" or "mock"
	OllamaURL    string
	OllamaModel  string
	MockURL      string

	RateCapacity     float64 // token-bucket size (burst)
	RateRefillPerSec float64 // sustained requests per second per client
	RateFailOpen     bool    // if Redis is down: true = allow requests, false = reject
	DemoEndpoint     bool    // POST /api/demo: lets the dashboard's "Send test request" button work (local demos only)

	CacheTTL        time.Duration
	UpstreamTimeout time.Duration // time to wait for the upstream's response headers
	MaxBodyBytes    int64

	// Resilience.
	MaxRetries      int           // extra attempts per route after a transient failure
	RetryBackoff    time.Duration // base delay before the first retry (doubles each time)
	BreakerFailures int           // consecutive failures that open a route's circuit; 0 disables
	BreakerCooldown time.Duration // how long an open circuit stays open before a trial request
	FallbackRoute   string        // "" (none), "ollama" or "mock": used when the chosen route fails

	// Token budget per API key (or IP). 0 disables.
	TokenBudget       int64
	TokenBudgetWindow time.Duration

	PricingFile string // JSON file with reference list prices
}

// Load reads the environment and returns a Config. Go functions commonly return
// (value, error); callers must check the error.
func Load() (Config, error) {
	l := &loader{}
	cfg := Config{
		ListenAddr:       l.str("LISTEN_ADDR", ":8080"),
		RedisAddr:        l.str("REDIS_ADDR", ""),
		RedisPassword:    l.str("REDIS_PASSWORD", ""),
		GatewayAPIKey:    l.str("GATEWAY_API_KEY", ""),
		DefaultRoute:     l.str("DEFAULT_ROUTE", "ollama"),
		OllamaURL:        l.str("OLLAMA_URL", "http://localhost:11434"),
		OllamaModel:      l.str("OLLAMA_MODEL", "qwen2.5:0.5b"),
		MockURL:          l.str("MOCK_URL", "http://localhost:9000"),
		RateCapacity:     l.float("RATE_CAPACITY", 20),
		RateRefillPerSec: l.float("RATE_REFILL_PER_SEC", 10),
		RateFailOpen:     l.boolean("RATE_FAIL_OPEN", true),
		DemoEndpoint:     l.boolean("DEMO_ENDPOINT", false),
		CacheTTL:         l.duration("CACHE_TTL", 10*time.Minute),
		UpstreamTimeout:  l.duration("UPSTREAM_TIMEOUT", 120*time.Second),
		MaxBodyBytes:     int64(l.float("MAX_BODY_BYTES", 1<<20)),
		PricingFile:      l.str("PRICING_FILE", "data/pricing.seed.json"),

		MaxRetries:        int(l.integer("MAX_RETRIES", 1)),
		RetryBackoff:      l.duration("RETRY_BACKOFF", 100*time.Millisecond),
		BreakerFailures:   int(l.integer("BREAKER_FAILURES", 5)),
		BreakerCooldown:   l.duration("BREAKER_COOLDOWN", 30*time.Second),
		FallbackRoute:     l.str("FALLBACK_ROUTE", ""),
		TokenBudget:       l.integer("TOKEN_BUDGET", 0),
		TokenBudgetWindow: l.duration("TOKEN_BUDGET_WINDOW", 24*time.Hour),
	}
	if l.err != nil {
		return Config{}, l.err
	}
	if cfg.DefaultRoute != "ollama" && cfg.DefaultRoute != "mock" {
		return Config{}, fmt.Errorf("DEFAULT_ROUTE must be \"ollama\" or \"mock\", got %q", cfg.DefaultRoute)
	}
	if cfg.RateCapacity < 1 || cfg.RateRefillPerSec <= 0 {
		return Config{}, fmt.Errorf("RATE_CAPACITY must be >= 1 and RATE_REFILL_PER_SEC must be > 0")
	}
	if r := cfg.FallbackRoute; r != "" && r != "ollama" && r != "mock" {
		return Config{}, fmt.Errorf("FALLBACK_ROUTE must be empty, \"ollama\" or \"mock\", got %q", r)
	}
	if cfg.MaxRetries < 0 || cfg.MaxRetries > 5 {
		return Config{}, fmt.Errorf("MAX_RETRIES must be between 0 and 5, got %d", cfg.MaxRetries)
	}
	if cfg.BreakerFailures < 0 || cfg.TokenBudget < 0 {
		return Config{}, fmt.Errorf("BREAKER_FAILURES and TOKEN_BUDGET must not be negative")
	}
	if cfg.TokenBudget > 0 && cfg.TokenBudgetWindow <= 0 {
		return Config{}, fmt.Errorf("TOKEN_BUDGET_WINDOW must be positive when TOKEN_BUDGET is set")
	}
	return cfg, nil
}

// loader remembers the first parse error so Load() stays readable.
type loader struct{ err error }

func (l *loader) fail(key string, err error) {
	if l.err == nil {
		l.err = fmt.Errorf("invalid %s: %w", key, err)
	}
}

func (l *loader) str(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func (l *loader) float(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		l.fail(key, err)
		return def
	}
	return f
}

func (l *loader) integer(key string, def int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		l.fail(key, err)
		return def
	}
	return n
}

func (l *loader) boolean(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.fail(key, err)
		return def
	}
	return b
}

func (l *loader) duration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		l.fail(key, err)
		return def
	}
	return d
}
