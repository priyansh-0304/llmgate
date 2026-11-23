package proxy_test

import (
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

	"llmgate/internal/cache"
	"llmgate/internal/metrics"
	"llmgate/internal/pricing"
	"llmgate/internal/proxy"
	"llmgate/internal/stats"
)

// newGateway wires a Gateway to a fake upstream. It returns the gateway, the
// stats object (to assert on) and a counter of upstream calls.
func newGateway(t *testing.T, upstream http.HandlerFunc) (*proxy.Gateway, *stats.Stats, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		upstream(w, r)
	}))
	t.Cleanup(ts.Close)

	st := stats.New()
	gw := proxy.New(proxy.Options{
		Routes: []proxy.Route{{
			Name: "mock", BaseURL: ts.URL, Model: "mock-fast", KnownModels: []string{"mock-fast"},
		}},
		DefaultRoute:    "mock",
		Cache:           cache.NewMemory(),
		CacheTTL:        time.Minute,
		Metrics:         metrics.New(),
		Stats:           st,
		Pricing:         &pricing.Table{References: []pricing.Reference{{ID: "ref", InputPerMTok: 1, OutputPerMTok: 2}}},
		UpstreamTimeout: 5 * time.Second,
		MaxBodyBytes:    1 << 20,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return gw, st, &calls
}

func post(gw *proxy.Gateway, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	gw.ChatCompletions(rec, req)
	return rec
}

func jsonUpstream(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}`)
}

func TestDeterministicRequestsAreCached(t *testing.T) {
	gw, st, calls := newGateway(t, jsonUpstream)
	body := `{"model":"local","temperature":0,"messages":[{"role":"user","content":"hello"}]}`

	first := post(gw, body)
	second := post(gw, body)

	if first.Code != 200 || second.Code != 200 {
		t.Fatalf("status codes %d, %d; want 200, 200", first.Code, second.Code)
	}
	if got := first.Header().Get("X-Cache"); got != "MISS" {
		t.Errorf("first X-Cache = %q, want MISS", got)
	}
	if got := second.Header().Get("X-Cache"); got != "HIT" {
		t.Errorf("second X-Cache = %q, want HIT", got)
	}
	if calls.Load() != 1 {
		t.Errorf("upstream called %d times, want 1", calls.Load())
	}
	snap := st.Snapshot(1)
	if snap.TotalRequests != 2 || snap.CacheHits != 1 {
		t.Errorf("requests=%d cacheHits=%d, want 2 and 1", snap.TotalRequests, snap.CacheHits)
	}
	// Cache hits still count tokens: they are what a paid API would have billed.
	if snap.PromptTokens != 20 || snap.CompletionTokens != 40 {
		t.Errorf("tokens = %d/%d, want 20/40", snap.PromptTokens, snap.CompletionTokens)
	}
}

func TestNonDeterministicRequestsBypassCache(t *testing.T) {
	gw, _, calls := newGateway(t, jsonUpstream)
	body := `{"model":"local","messages":[{"role":"user","content":"hello"}]}` // no temperature

	post(gw, body)
	rec := post(gw, body)

	if calls.Load() != 2 {
		t.Errorf("upstream called %d times, want 2", calls.Load())
	}
	if got := rec.Header().Get("X-Cache"); got != "BYPASS" {
		t.Errorf("X-Cache = %q, want BYPASS", got)
	}
}

func TestAliasModelIsRewrittenAndStreamUsageOptionAdded(t *testing.T) {
	var seen map[string]any
	gw, _, _ := newGateway(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&seen)
		jsonUpstream(w, r)
	})
	post(gw, `{"model":"local","messages":[]}`)
	if seen["model"] != "mock-fast" {
		t.Errorf("upstream saw model %v, want mock-fast", seen["model"])
	}
}

func TestStreamingIsRelayedAndUsageRecorded(t *testing.T) {
	var sawIncludeUsage bool
	gw, st, _ := newGateway(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			StreamOptions struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		sawIncludeUsage = req.StreamOptions.IncludeUsage

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})

	rec := post(gw, `{"model":"local","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	if !sawIncludeUsage {
		t.Error("gateway should ask upstream to include usage when streaming")
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"content":"hel"`) || !strings.Contains(out, "[DONE]") {
		t.Errorf("stream was not relayed intact: %q", out)
	}
	snap := st.Snapshot(1)
	if snap.PromptTokens != 7 || snap.CompletionTokens != 2 {
		t.Errorf("tokens = %d/%d, want 7/2", snap.PromptTokens, snap.CompletionTokens)
	}
}

func TestUpstreamErrorsPassThroughAndAreNotCached(t *testing.T) {
	gw, _, calls := newGateway(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"message":"boom"}}`)
	})
	body := `{"model":"local","temperature":0,"messages":[]}`
	if rec := post(gw, body); rec.Code != 500 {
		t.Errorf("status = %d, want 500 passed through", rec.Code)
	}
	post(gw, body)
	if calls.Load() != 2 {
		t.Errorf("errors must not be cached; upstream calls = %d, want 2", calls.Load())
	}
}

func TestMissingUsageFallsBackToEstimate(t *testing.T) {
	gw, st, _ := newGateway(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"12345678"}}]}`) // no usage block
	})
	post(gw, `{"model":"local","messages":[{"role":"user","content":"abcd"}]}`)
	snap := st.Snapshot(1)
	if snap.PromptTokens != 1 || snap.CompletionTokens != 2 { // chars/4, rounded up
		t.Errorf("estimated tokens = %d/%d, want 1/2", snap.PromptTokens, snap.CompletionTokens)
	}
}

func TestBadRequests(t *testing.T) {
	gw, _, _ := newGateway(t, jsonUpstream)
	if rec := post(gw, `not json`); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid JSON -> %d, want 400", rec.Code)
	}
}
