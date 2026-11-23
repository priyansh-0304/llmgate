package middleware

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"llmgate/internal/budget"
	"llmgate/internal/metrics"
	"llmgate/internal/stats"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

func TestTokenBudgetRejectsWhenUsedUp(t *testing.T) {
	store := budget.NewMemory(time.Hour)
	h := TokenBudget(store, 100, metrics.New(), stats.New(), true, quietLogger())(okHandler())

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	_ = store.Add(context.Background(), ClientKey(req), 100) // exactly the whole budget

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("want a Retry-After header telling the caller when the window resets")
	}
	if got := rec.Header().Get("X-Budget-Remaining-Tokens"); got != "0" {
		t.Errorf("X-Budget-Remaining-Tokens = %q, want 0", got)
	}
	if !contains(rec.Body.String(), "insufficient_quota") {
		t.Errorf("body %q should use OpenAI's insufficient_quota error type", rec.Body.String())
	}
}

func TestTokenBudgetAllowsWithRemainingTokens(t *testing.T) {
	store := budget.NewMemory(time.Hour)
	h := TokenBudget(store, 100, metrics.New(), stats.New(), true, quietLogger())(okHandler())

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	_ = store.Add(context.Background(), ClientKey(req), 40)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("X-Budget-Remaining-Tokens"); got != "60" {
		t.Errorf("X-Budget-Remaining-Tokens = %q, want 60", got)
	}
}

func TestTokenBudgetDisabledWhenLimitIsZero(t *testing.T) {
	store := budget.NewMemory(time.Hour)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	_ = store.Add(context.Background(), ClientKey(req), 1_000_000)

	h := TokenBudget(store, 0, metrics.New(), stats.New(), true, quietLogger())(okHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (budget disabled)", rec.Code)
	}
}

type brokenStore struct{}

func (brokenStore) Used(context.Context, string) (int64, time.Duration, error) {
	return 0, 0, errors.New("redis down")
}
func (brokenStore) Add(context.Context, string, int64) error { return errors.New("redis down") }

func TestTokenBudgetFailOpenAndClosed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)

	open := TokenBudget(brokenStore{}, 100, metrics.New(), stats.New(), true, quietLogger())(okHandler())
	rec := httptest.NewRecorder()
	open.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("fail-open: status = %d, want 200", rec.Code)
	}

	closed := TokenBudget(brokenStore{}, 100, metrics.New(), stats.New(), false, quietLogger())(okHandler())
	rec = httptest.NewRecorder()
	closed.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("fail-closed: status = %d, want 503", rec.Code)
	}
}

func TestClientKeyNeverContainsTheRawAPIKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer sk-super-secret")
	if key := ClientKey(req); contains(key, "secret") || !contains(key, "key:") {
		t.Errorf("ClientKey = %q: must be a hash, not the raw key", key)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
