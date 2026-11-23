package proxy_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"llmgate/internal/budget"
	"llmgate/internal/cache"
	"llmgate/internal/metrics"
	"llmgate/internal/pricing"
	"llmgate/internal/proxy"
	"llmgate/internal/reqid"
	"llmgate/internal/stats"
)

const detBody = `{"model":"local","temperature":0,"messages":[{"role":"user","content":"hello"}]}`

// twoRoutes starts two fake upstreams ("primary" is the default route, "backup"
// is only used as a fallback) and returns a gateway plus their call counters.
func twoRoutes(t *testing.T, primary, backup http.HandlerFunc, mutate func(*proxy.Options)) (gw *proxy.Gateway, primaryCalls, backupCalls *atomic.Int32) {
	t.Helper()
	primaryCalls, backupCalls = new(atomic.Int32), new(atomic.Int32)
	ps := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		primary(w, r)
	}))
	bs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupCalls.Add(1)
		backup(w, r)
	}))
	t.Cleanup(ps.Close)
	t.Cleanup(bs.Close)

	opts := proxy.Options{
		Routes: []proxy.Route{
			{Name: "primary", BaseURL: ps.URL, Model: "big-model", KnownModels: []string{"big-model"}},
			{Name: "backup", BaseURL: bs.URL, Model: "small-model", KnownModels: []string{"small-model"}},
		},
		DefaultRoute:    "primary",
		Cache:           cache.NewMemory(),
		CacheTTL:        time.Minute,
		Metrics:         metrics.New(),
		Stats:           stats.New(),
		Pricing:         &pricing.Table{References: []pricing.Reference{{ID: "ref", InputPerMTok: 1, OutputPerMTok: 2}}},
		UpstreamTimeout: 5 * time.Second,
		MaxBodyBytes:    1 << 20,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if mutate != nil {
		mutate(&opts)
	}
	return proxy.New(opts), primaryCalls, backupCalls
}

func fail500(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	fmt.Fprint(w, `{"error":{"message":"boom"}}`)
}

func TestRetrySucceedsOnSecondAttempt(t *testing.T) {
	var n atomic.Int32
	gw, primaryCalls, _ := twoRoutes(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			fail500(w, r)
			return
		}
		jsonUpstream(w, r)
	}, jsonUpstream, func(o *proxy.Options) { o.Retries = 1 })

	rec := post(gw, detBody)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 after one retry", rec.Code)
	}
	if primaryCalls.Load() != 2 {
		t.Errorf("primary called %d times, want 2 (fail then succeed)", primaryCalls.Load())
	}
	if rec.Header().Get("X-Gateway-Fallback") != "" {
		t.Error("a successful retry must not be reported as a fallback")
	}
}

func TestClientErrorsAreNotRetried(t *testing.T) {
	gw, primaryCalls, _ := twoRoutes(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"bad"}}`)
	}, jsonUpstream, func(o *proxy.Options) { o.Retries = 3; o.FallbackRoute = "backup" })

	rec := post(gw, detBody)
	if rec.Code != 400 {
		t.Errorf("status = %d, want the upstream's 400 passed through", rec.Code)
	}
	if primaryCalls.Load() != 1 {
		t.Errorf("primary called %d times, want 1 (4xx is the caller's fault: no retry)", primaryCalls.Load())
	}
}

func TestFallbackServesWhenPrimaryFails(t *testing.T) {
	gw, primaryCalls, backupCalls := twoRoutes(t, fail500, jsonUpstream, func(o *proxy.Options) {
		o.FallbackRoute = "backup"
	})

	rec := post(gw, detBody)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 from the fallback", rec.Code)
	}
	if rec.Header().Get("X-Gateway-Route") != "backup" || rec.Header().Get("X-Gateway-Fallback") != "true" {
		t.Errorf("headers route=%q fallback=%q, want backup / true",
			rec.Header().Get("X-Gateway-Route"), rec.Header().Get("X-Gateway-Fallback"))
	}

	// The fallback's answer must NOT be cached under the primary route's key.
	post(gw, detBody)
	if primaryCalls.Load() != 2 || backupCalls.Load() != 2 {
		t.Errorf("calls primary=%d backup=%d, want 2 and 2 (nothing cached from a fallback)",
			primaryCalls.Load(), backupCalls.Load())
	}
}

func TestFallbackUsesItsOwnModelName(t *testing.T) {
	var backupSaw string
	gw, _, _ := twoRoutes(t, fail500, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		backupSaw, _ = req["model"].(string)
		jsonUpstream(w, r)
	}, func(o *proxy.Options) { o.FallbackRoute = "backup" })

	// The client explicitly names a model that only exists on the primary.
	post(gw, `{"model":"big-model","messages":[]}`)
	if backupSaw != "small-model" {
		t.Errorf("fallback received model %q, want its own default %q", backupSaw, "small-model")
	}
}

func TestCircuitOpensThenFailsFast(t *testing.T) {
	gw, primaryCalls, _ := twoRoutes(t, fail500, jsonUpstream, func(o *proxy.Options) {
		o.BreakerFailures = 2
		o.BreakerCooldown = time.Hour
	})

	for i := 0; i < 2; i++ {
		if rec := post(gw, detBody); rec.Code != 500 {
			t.Fatalf("request %d: status %d, want the upstream's 500", i+1, rec.Code)
		}
	}
	rec := post(gw, detBody)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("with the circuit open: status %d, want 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("503 from an open circuit should carry Retry-After")
	}
	if primaryCalls.Load() != 2 {
		t.Errorf("primary called %d times, want 2: the open circuit must not call it", primaryCalls.Load())
	}

	// The dashboard endpoint reports the open circuit.
	srec := httptest.NewRecorder()
	gw.Stats(srec, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
	var body struct {
		Routes []struct{ Name, Circuit string }
	}
	if err := json.Unmarshal(srec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, r := range body.Routes {
		if r.Name == "primary" && r.Circuit != "open" {
			t.Errorf("primary circuit = %q in /api/stats, want open", r.Circuit)
		}
		if r.Name == "backup" && r.Circuit != "closed" {
			t.Errorf("backup circuit = %q in /api/stats, want closed", r.Circuit)
		}
	}
}

func TestOpenCircuitSkipsStraightToFallback(t *testing.T) {
	gw, primaryCalls, backupCalls := twoRoutes(t, fail500, jsonUpstream, func(o *proxy.Options) {
		o.BreakerFailures = 1
		o.BreakerCooldown = time.Hour
		o.FallbackRoute = "backup"
	})
	post(gw, detBody) // primary fails once -> circuit opens, fallback answers
	post(gw, detBody) // circuit open: primary must be skipped entirely
	if primaryCalls.Load() != 1 {
		t.Errorf("primary called %d times, want 1", primaryCalls.Load())
	}
	if backupCalls.Load() != 2 {
		t.Errorf("backup called %d times, want 2", backupCalls.Load())
	}
}

func TestUnreachableUpstreamGives502(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close() // nothing listens there any more

	gw := proxy.New(proxy.Options{
		Routes:       []proxy.Route{{Name: "primary", BaseURL: url}},
		DefaultRoute: "primary",
		Cache:        cache.NewMemory(), CacheTTL: time.Minute,
		Metrics: metrics.New(), Stats: stats.New(),
		Pricing:         &pricing.Table{},
		UpstreamTimeout: time.Second, MaxBodyBytes: 1 << 20,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	rec := post(gw, detBody)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "upstream_error") {
		t.Errorf("body %q should be an OpenAI-style upstream_error", rec.Body.String())
	}
}

func TestBudgetChargesModelTokensButNotCacheHits(t *testing.T) {
	store := budget.NewMemory(time.Hour)
	gw, _, _ := twoRoutes(t, jsonUpstream, jsonUpstream, func(o *proxy.Options) {
		o.Budget = store
		o.BudgetKey = func(*http.Request) string { return "caller" }
	})

	post(gw, detBody) // upstream reports 10 + 20 tokens
	if used, _, _ := store.Used(context.Background(), "caller"); used != 30 {
		t.Fatalf("used after first request = %d, want 30", used)
	}
	post(gw, detBody) // identical + temperature 0 -> cache hit, no model work
	if used, _, _ := store.Used(context.Background(), "caller"); used != 30 {
		t.Errorf("used after a cache hit = %d, want still 30", used)
	}
	post(gw, `{"model":"local","messages":[]}`) // no temperature: not cacheable, charged again
	if used, _, _ := store.Used(context.Background(), "caller"); used != 60 {
		t.Errorf("used after a third request = %d, want 60", used)
	}
}

func TestRequestIDIsForwardedUpstream(t *testing.T) {
	var seen string
	gw, _, _ := twoRoutes(t, func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("X-Request-ID")
		jsonUpstream(w, r)
	}, jsonUpstream, nil)

	h := reqid.Middleware(http.HandlerFunc(gw.ChatCompletions))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(detBody))
	req.Header.Set("X-Request-ID", "trace-abc-123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if seen != "trace-abc-123" {
		t.Errorf("upstream saw X-Request-ID %q, want trace-abc-123", seen)
	}
	if rec.Header().Get("X-Request-ID") != "trace-abc-123" {
		t.Errorf("response X-Request-ID = %q", rec.Header().Get("X-Request-ID"))
	}
}
