// Package proxy contains the gateway's main handler: it validates a chat
// completion request, picks an upstream, optionally serves it from cache,
// forwards it (with retries, a circuit breaker and an optional fallback
// route), relays the response (including streaming) and records tokens,
// latency and estimated cost.
package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"llmgate/internal/budget"
	"llmgate/internal/cache"
	"llmgate/internal/httpx"
	"llmgate/internal/metrics"
	"llmgate/internal/pricing"
	"llmgate/internal/reqid"
	"llmgate/internal/resilience"
	"llmgate/internal/stats"
)

const (
	chatPath             = "/v1/chat/completions"
	maxUpstreamBody      = 16 << 20 // refuse to buffer more than 16 MiB from an upstream
	statusClientGoneAway = 499      // nginx convention: client closed the connection
)

// errUpstreamStatus marks "the upstream answered, but with a 5xx".
var errUpstreamStatus = errors.New("upstream returned a server error")

// Route describes one upstream inference server.
type Route struct {
	Name    string // "ollama" or "mock" (used as a metric label)
	BaseURL string // e.g. http://ollama:11434
	// Model is the real model name sent upstream when the client asks for an
	// alias ("local", "auto" or nothing). It is also what a fallback request
	// uses, because the model the client named may not exist on that route.
	Model string
	// KnownModels is an allowlist used for the `model` metric label. Clients
	// can send any string as "model"; using it raw as a label would let a
	// caller create unlimited time series and blow up Prometheus memory.
	KnownModels []string
}

func (r Route) modelLabel(model string) string {
	for _, m := range r.KnownModels {
		if m == model {
			return model
		}
	}
	return "other"
}

// routeState is a Route plus its own circuit breaker: one sick upstream must
// not stop traffic to a healthy one.
type routeState struct {
	route   Route
	breaker *resilience.Breaker
}

// target is one concrete thing to try: a route and the exact request body for it.
type target struct {
	rs    *routeState
	body  []byte
	model string // model name sent to this route
}

// Options bundles the dependencies of a Gateway.
type Options struct {
	Routes          []Route
	DefaultRoute    string
	Cache           cache.Cache
	CacheTTL        time.Duration
	Metrics         *metrics.Metrics
	Stats           *stats.Stats
	Pricing         *pricing.Table
	UpstreamTimeout time.Duration
	MaxBodyBytes    int64
	Logger          *slog.Logger

	// Resilience. The zero values mean: no retries, breaker disabled, no fallback.
	Retries         int
	RetryBackoff    time.Duration
	BreakerFailures int
	BreakerCooldown time.Duration
	FallbackRoute   string

	// Token budget accounting. Leave Budget nil to disable.
	Budget    budget.Store
	BudgetKey func(*http.Request) string
}

// Gateway implements the HTTP handlers.
type Gateway struct {
	routes          map[string]*routeState
	defaultRoute    string
	fallback        string
	cache           cache.Cache
	cacheTTL        time.Duration
	metrics         *metrics.Metrics
	stats           *stats.Stats
	pricing         *pricing.Table
	client          *http.Client
	maxBody         int64
	log             *slog.Logger
	maxRetries      int
	retryBackoff    time.Duration
	breakerCooldown time.Duration
	budget          budget.Store
	budgetKey       func(*http.Request) string
}

func New(o Options) *Gateway {
	routes := make(map[string]*routeState, len(o.Routes))
	for _, r := range o.Routes {
		name := r.Name
		rs := &routeState{route: r}
		// The breaker calls this whenever its state changes, so the gauge
		// always shows the current state and the counter shows flapping.
		rs.breaker = resilience.NewBreaker(o.BreakerFailures, o.BreakerCooldown, func(s resilience.State) {
			o.Metrics.CircuitState.WithLabelValues(name).Set(float64(s))
			o.Metrics.CircuitTransitions.WithLabelValues(name, s.String()).Inc()
		})
		o.Metrics.CircuitState.WithLabelValues(name).Set(float64(resilience.Closed))
		routes[name] = rs
	}
	// Note: no http.Client.Timeout. That timeout includes reading the whole
	// body, which would cut off long streaming generations. We bound the wait
	// for response headers instead; the request context handles disconnects.
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		ResponseHeaderTimeout: o.UpstreamTimeout,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
	}
	return &Gateway{
		routes:          routes,
		defaultRoute:    o.DefaultRoute,
		fallback:        o.FallbackRoute,
		cache:           o.Cache,
		cacheTTL:        o.CacheTTL,
		metrics:         o.Metrics,
		stats:           o.Stats,
		pricing:         o.Pricing,
		client:          &http.Client{Transport: transport},
		maxBody:         o.MaxBodyBytes,
		log:             o.Logger,
		maxRetries:      max(0, o.Retries),
		retryBackoff:    o.RetryBackoff,
		breakerCooldown: o.BreakerCooldown,
		budget:          o.Budget,
		budgetKey:       o.BudgetKey,
	}
}

// pickRoute chooses an upstream. Order: explicit X-LLM-Route header, then
// "mock*" model names, then the configured default.
func (g *Gateway) pickRoute(model, override string) (*routeState, bool) {
	if override != "" {
		rs, ok := g.routes[override]
		return rs, ok
	}
	if strings.HasPrefix(model, "mock") {
		rs, ok := g.routes["mock"]
		return rs, ok
	}
	rs, ok := g.routes[g.defaultRoute]
	return rs, ok
}

// buildTargets prepares the ordered list of things to try: the chosen route
// first, then the fallback route (if configured and different).
func (g *Gateway) buildTargets(primary *routeState, req map[string]any, model string, stream bool) ([]target, error) {
	candidates := []*routeState{primary}
	if fb, ok := g.routes[g.fallback]; ok && fb != primary {
		candidates = append(candidates, fb)
	}
	alias := model == "" || model == "local" || model == "auto"

	targets := make([]target, 0, len(candidates))
	for i, rs := range candidates {
		upstreamModel := model
		// A fallback always uses its own default model: the model the client
		// named belongs to the route that just failed.
		if rs.route.Model != "" && (alias || i > 0) {
			upstreamModel = rs.route.Model
		}
		clone := maps.Clone(req) // shallow copy: we only replace top-level keys
		clone["model"] = upstreamModel
		if stream {
			// Ask the upstream to put token usage in the final stream chunk.
			clone["stream_options"] = map[string]any{"include_usage": true}
		}
		body, err := json.Marshal(clone)
		if err != nil {
			return nil, err
		}
		targets = append(targets, target{rs: rs, body: body, model: upstreamModel})
	}
	return targets, nil
}

// ChatCompletions handles POST /v1/chat/completions.
func (g *Gateway) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	reqID := reqid.From(r.Context())

	// 1. Read and parse the request body (bounded).
	r.Body = http.MaxBytesReader(w, r.Body, g.maxBody)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "request body too large or unreadable")
		return
	}
	// Decoding into map[string]any keeps every field the client sent, so we
	// can forward unknown OpenAI parameters untouched. Re-encoding sorts the
	// keys, which also makes the cache key independent of field order.
	var req map[string]any
	if err := json.Unmarshal(raw, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request_error", "body must be a JSON object")
		return
	}
	model, _ := req["model"].(string) // "comma ok": zero value if missing/not a string
	stream, _ := req["stream"].(bool)
	temp, hasTemp := req["temperature"].(float64)

	// 2. Route, and build the request for every route we might try.
	primaryRS, ok := g.pickRoute(model, r.Header.Get("X-LLM-Route"))
	if !ok {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request_error", "unknown route")
		return
	}
	targets, err := g.buildTargets(primaryRS, req, model, stream)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "could not encode request")
		return
	}
	primary := targets[0]
	primaryName := primary.rs.route.Name
	primaryLabel := primary.rs.route.modelLabel(primary.model)
	w.Header().Set("X-Gateway-Route", primaryName)

	// 3. Cache. Only deterministic (temperature == 0), non-streaming requests
	// are cached: otherwise "the same prompt" legitimately has different answers.
	cacheable := !stream && hasTemp && temp == 0
	cacheKey := ""
	if cacheable {
		cacheKey = hashKey(primaryName, primary.body)
		if hit, found := g.cache.Get(r.Context(), cacheKey); found {
			g.metrics.CacheEvents.WithLabelValues("hit").Inc()
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Cache", "HIT")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(hit)
			g.record(r, primaryName, primaryLabel, http.StatusOK, true, usageFromBody(hit, req), time.Since(start))
			return
		}
		g.metrics.CacheEvents.WithLabelValues("miss").Inc()
	} else {
		g.metrics.CacheEvents.WithLabelValues("bypass").Inc()
	}
	cacheState := "BYPASS"
	if cacheable {
		cacheState = "MISS"
	}

	// 4. Forward: try each route in order. Within a route, callRoute retries
	// transient failures and consults that route's circuit breaker.
	var (
		resp    *http.Response
		served  target
		lastErr error
	)
	for i, t := range targets {
		hasNext := i < len(targets)-1
		res, err := g.callRoute(r.Context(), t, hasNext, reqID)
		if err == nil {
			resp, served = res, t
			break
		}
		if r.Context().Err() != nil { // the client gave up; nothing to send back
			g.record(r, primaryName, primaryLabel, statusClientGoneAway, false, tokenUsage{}, time.Since(start))
			return
		}
		lastErr = err
		if hasNext {
			next := targets[i+1].rs.route.Name
			g.metrics.Fallbacks.WithLabelValues(t.rs.route.Name, next).Inc()
			g.log.Warn("falling back to another route", "from", t.rs.route.Name, "to", next, "err", err, "request_id", reqID)
		}
	}
	if resp == nil {
		status, errType, msg := http.StatusBadGateway, "upstream_error", "upstream unreachable"
		if errors.Is(lastErr, resilience.ErrCircuitOpen) {
			status, errType, msg = http.StatusServiceUnavailable, "upstream_unavailable", "upstream temporarily unavailable (circuit open)"
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(g.breakerCooldown.Seconds()))))
		}
		g.log.Warn("all upstreams failed", "route", primaryName, "err", lastErr, "request_id", reqID)
		g.record(r, primaryName, primaryLabel, status, false, tokenUsage{}, time.Since(start))
		httpx.WriteError(w, status, errType, msg)
		return
	}
	defer resp.Body.Close() // always release the connection

	routeName := served.rs.route.Name
	label := served.rs.route.modelLabel(served.model)
	usedFallback := served.rs != primary.rs
	w.Header().Set("X-Gateway-Route", routeName)
	if usedFallback {
		w.Header().Set("X-Gateway-Fallback", "true")
	}

	// 5a. Streaming (Server-Sent Events): relay chunk by chunk.
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		usage := g.relayStream(w, resp, req)
		g.record(r, routeName, label, resp.StatusCode, false, usage, time.Since(start))
		return
	}

	// 5b. Normal JSON response.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamBody))
	if err != nil {
		g.record(r, routeName, label, http.StatusBadGateway, false, tokenUsage{}, time.Since(start))
		httpx.WriteError(w, http.StatusBadGateway, "upstream_error", "failed reading upstream response")
		return
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		w.Header().Set("Retry-After", ra)
	}
	w.Header().Set("X-Cache", cacheState)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(data)

	var usage tokenUsage
	if resp.StatusCode == http.StatusOK {
		usage = usageFromBody(data, req)
		// Never cache a fallback's answer under the primary route's key: the
		// next identical request would be served the wrong model's reply.
		if cacheable && !usedFallback {
			g.cache.Set(r.Context(), cacheKey, data, g.cacheTTL)
		}
	}
	g.record(r, routeName, label, resp.StatusCode, false, usage, time.Since(start))
}

// callRoute sends the request to ONE route. It retries transient failures,
// asks the route's circuit breaker for permission before every attempt, and
// reports every outcome back to the breaker.
//
// It returns either a response the caller must handle (and close), or an error
// meaning "this route cannot serve the request right now".
func (g *Gateway) callRoute(ctx context.Context, t target, hasFallback bool, reqID string) (*http.Response, error) {
	name := t.rs.route.Name
	breaker := t.rs.breaker
	lastErr := resilience.ErrCircuitOpen // what we report if the breaker never lets us try

	for attempt := 0; attempt <= g.maxRetries; attempt++ {
		if attempt > 0 {
			g.metrics.Retries.WithLabelValues(name).Inc()
			if !resilience.SleepCtx(ctx, resilience.Backoff(g.retryBackoff, attempt, rand.Float64)) {
				return nil, ctx.Err()
			}
		}
		if !breaker.Allow() {
			g.metrics.UpstreamAttempts.WithLabelValues(name, "circuit_open").Inc()
			return nil, lastErr
		}

		resp, err := g.send(ctx, t, reqID)
		if err != nil {
			if ctx.Err() != nil { // client hung up: says nothing about upstream health
				breaker.Release()
				return nil, ctx.Err()
			}
			breaker.Failure()
			lastErr = err
			if isTimeout(err) {
				// Retrying a model that already took the full timeout would just
				// double the wait. Count it, then let the caller try the fallback.
				g.metrics.UpstreamAttempts.WithLabelValues(name, "timeout").Inc()
				return nil, err
			}
			g.metrics.UpstreamAttempts.WithLabelValues(name, "error").Inc()
			continue
		}

		if resp.StatusCode >= 500 {
			breaker.Failure()
			g.metrics.UpstreamAttempts.WithLabelValues(name, "http_5xx").Inc()
			if attempt < g.maxRetries || hasFallback {
				// Another attempt or route is available: throw this answer away.
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
				resp.Body.Close()
				lastErr = fmt.Errorf("%w (HTTP %d)", errUpstreamStatus, resp.StatusCode)
				continue
			}
			return resp, nil // last chance: pass the upstream's error through to the client
		}

		// 2xx, 3xx and 4xx all mean the upstream is alive. A 4xx is the client's fault.
		breaker.Success()
		g.metrics.UpstreamAttempts.WithLabelValues(name, "success").Inc()
		return resp, nil
	}
	return nil, lastErr
}

// send performs one HTTP call to a route.
func (g *Gateway) send(ctx context.Context, t target, reqID string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.rs.route.BaseURL+chatPath, bytes.NewReader(t.body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if reqID != "" {
		req.Header.Set(reqid.Header, reqID) // lets you follow one request into the upstream's logs
	}
	// Client credentials are deliberately NOT forwarded: the gateway's own key
	// must never leak to an upstream.
	return g.client.Do(req)
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// relayStream copies an SSE response to the client line by line, flushing at
// each event boundary, while watching for the usage block.
func (g *Gateway) relayStream(w http.ResponseWriter, resp *http.Response, req map[string]any) tokenUsage {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Cache", "BYPASS")
	h.Set("X-Accel-Buffering", "no") // tell nginx not to buffer
	w.WriteHeader(resp.StatusCode)
	rc := http.NewResponseController(w)

	var (
		usage     tokenUsage
		haveUsage bool
		text      strings.Builder
	)
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if _, werr := w.Write(line); werr != nil {
				break // client went away
			}
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) == 0 {
				_ = rc.Flush() // blank line = end of one SSE event
			} else if payload, isData := bytes.CutPrefix(trimmed, []byte("data:")); isData {
				payload = bytes.TrimSpace(payload)
				if !bytes.Equal(payload, []byte("[DONE]")) {
					var chunk struct {
						Choices []struct {
							Delta struct {
								Content string `json:"content"`
							} `json:"delta"`
						} `json:"choices"`
						Usage *usageJSON `json:"usage"`
					}
					if json.Unmarshal(payload, &chunk) == nil {
						for _, c := range chunk.Choices {
							text.WriteString(c.Delta.Content)
						}
						if chunk.Usage != nil {
							usage = tokenUsage{Prompt: chunk.Usage.PromptTokens, Completion: chunk.Usage.CompletionTokens}
							haveUsage = true
						}
					}
				}
			}
		}
		if err != nil {
			break // io.EOF at the normal end of the stream
		}
	}
	_ = rc.Flush()

	if !haveUsage && resp.StatusCode == http.StatusOK {
		usage = tokenUsage{
			Prompt:     estimateTokens(promptText(req)),
			Completion: estimateTokens(text.String()),
			Estimated:  true,
		}
	}
	return usage
}

// record updates Prometheus and the dashboard stats for one finished request.
func (g *Gateway) record(r *http.Request, route, model string, status int, cacheHit bool, u tokenUsage, latency time.Duration) {
	cacheLabel := "miss"
	if cacheHit {
		cacheLabel = "hit"
	}
	g.metrics.LLMRequests.WithLabelValues(route, model, strconv.Itoa(status), cacheLabel).Inc()
	g.metrics.Latency.WithLabelValues(route, model, cacheLabel).Observe(latency.Seconds())

	if status == http.StatusOK {
		g.metrics.Tokens.WithLabelValues(route, model, "prompt", cacheLabel).Add(float64(u.Prompt))
		g.metrics.Tokens.WithLabelValues(route, model, "completion", cacheLabel).Add(float64(u.Completion))
		for _, ref := range g.pricing.References {
			g.metrics.EstimatedCost.WithLabelValues(ref.ID).Add(ref.Cost(int64(u.Prompt), int64(u.Completion)))
		}
		if u.Estimated {
			g.metrics.UsageEstimated.WithLabelValues(route).Inc()
		}
	}
	g.stats.RecordRequest(u.Prompt, u.Completion, latency, cacheHit)
	g.chargeBudget(r, status, cacheHit, u)
}

// chargeBudget adds the tokens a successful, model-generated response used to
// the caller's budget. Cache hits are free: no model work happened.
func (g *Gateway) chargeBudget(r *http.Request, status int, cacheHit bool, u tokenUsage) {
	if g.budget == nil || g.budgetKey == nil || status != http.StatusOK || cacheHit {
		return
	}
	tokens := int64(u.Prompt + u.Completion)
	if tokens <= 0 {
		return
	}
	// WithoutCancel: still record usage if the client hung up mid-stream.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), time.Second)
	defer cancel()
	if err := g.budget.Add(ctx, g.budgetKey(r), tokens); err != nil {
		g.metrics.BudgetErrors.Inc()
		g.log.Warn("could not record token usage", "err", err, "request_id", reqid.From(r.Context()))
	}
}

func hashKey(route string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(route))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// ---------- token usage ----------

type usageJSON struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type tokenUsage struct {
	Prompt     int
	Completion int
	Estimated  bool // true when we guessed because upstream sent no usage
}

// usageFromBody reads the OpenAI-style "usage" block. If it is missing, fall
// back to a rough estimate of ~4 characters per token and flag it.
func usageFromBody(body []byte, req map[string]any) tokenUsage {
	var resp struct {
		Usage   *usageJSON `json:"usage"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err == nil && resp.Usage != nil {
		return tokenUsage{Prompt: resp.Usage.PromptTokens, Completion: resp.Usage.CompletionTokens}
	}
	var out strings.Builder
	for _, c := range resp.Choices {
		out.WriteString(c.Message.Content)
	}
	return tokenUsage{
		Prompt:     estimateTokens(promptText(req)),
		Completion: estimateTokens(out.String()),
		Estimated:  true,
	}
}

func estimateTokens(s string) int { return (utf8.RuneCountInString(s) + 3) / 4 }

// promptText concatenates the text of all messages in the request.
func promptText(req map[string]any) string {
	msgs, _ := req["messages"].([]any)
	var b strings.Builder
	for _, m := range msgs {
		if mm, ok := m.(map[string]any); ok {
			if s, ok := mm["content"].(string); ok {
				b.WriteString(s)
			}
		}
	}
	return b.String()
}
