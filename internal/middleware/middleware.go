// Package middleware holds the HTTP middleware chain around the gateway.
//
// A middleware is just a function that takes an http.Handler and returns a new
// http.Handler that does something before and/or after calling the original.
// Chaining them gives you an onion:
// RequestID( Recover( Instrument( Auth( RateLimit( TokenBudget( handler )))))).
package middleware

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"math"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"llmgate/internal/budget"
	"llmgate/internal/httpx"
	"llmgate/internal/metrics"
	"llmgate/internal/ratelimit"
	"llmgate/internal/reqid"
	"llmgate/internal/stats"
)

type Middleware func(http.Handler) http.Handler

// Chain wraps h so that the FIRST middleware listed is the OUTERMOST one.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// ---------- status capture ----------

// statusRecorder remembers the status code a handler wrote, because the
// standard ResponseWriter doesn't let you read it back.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status, s.wrote = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.status, s.wrote = http.StatusOK, true
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.NewResponseController reach the real writer, which is how
// the proxy flushes streaming (SSE) responses through this wrapper.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// ---------- Recover ----------

// Recover turns a panic inside a handler into a 500 instead of killing the process.
func Recover(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("panic in handler", "panic", rec, "stack", string(debug.Stack()), "request_id", reqid.From(r.Context()))
					httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "internal error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// ---------- Instrument (Prometheus) ----------

// Instrument records request count, duration and in-flight gauge for every route.
func Instrument(m *metrics.Metrics) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			m.InFlight.Inc()
			defer m.InFlight.Dec()

			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			path := pathLabel(r.URL.Path)
			m.HTTPRequests.WithLabelValues(r.Method, path, strconv.Itoa(rec.status)).Inc()
			m.HTTPDuration.WithLabelValues(r.Method, path).Observe(time.Since(start).Seconds())
		})
	}
}

// pathLabel keeps Prometheus label cardinality bounded: a scanner hitting
// /random-1, /random-2... must not create a new time series each time.
func pathLabel(p string) string {
	switch p {
	case "/v1/chat/completions", "/healthz", "/metrics", "/api/stats":
		return p
	}
	return "other"
}

// ---------- Auth ----------

// Auth requires "Authorization: Bearer <key>". An empty key disables auth.
func Auth(key string) Middleware {
	return func(next http.Handler) http.Handler {
		if key == "" {
			return next
		}
		want := []byte(key)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			// ConstantTimeCompare avoids leaking the key through response timing.
			if subtle.ConstantTimeCompare([]byte(got), want) != 1 {
				httpx.WriteError(w, http.StatusUnauthorized, "authentication_error", "invalid or missing API key")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ---------- Rate limit ----------

// RateLimit applies the limiter once per request, keyed by API key (hashed) or
// client IP. If the limiter backend errors and failOpen is true, the request
// is allowed (a Redis outage shouldn't take down the gateway).
func RateLimit(l ratelimit.Limiter, m *metrics.Metrics, st *stats.Stats, failOpen bool, log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			res, err := l.Allow(r.Context(), ClientKey(r))
			if err != nil {
				m.RateLimiterErrors.Inc()
				log.Warn("rate limiter error", "err", err, "fail_open", failOpen, "request_id", reqid.From(r.Context()))
				if !failOpen {
					httpx.WriteError(w, http.StatusServiceUnavailable, "rate_limiter_unavailable", "rate limiter unavailable")
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(int(math.Floor(res.Remaining))))
			if !res.Allowed {
				m.RateLimited.Inc()
				st.RecordRateLimited()
				secs := int(math.Ceil(res.RetryAfter.Seconds()))
				if secs < 1 {
					secs = 1
				}
				w.Header().Set("Retry-After", strconv.Itoa(secs))
				httpx.WriteError(w, http.StatusTooManyRequests, "rate_limit_error", "rate limit exceeded, retry later")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ---------- Token budget ----------

// TokenBudget rejects callers whose token budget for the current window is
// used up (HTTP 429, error type "insufficient_quota", like OpenAI's). The
// tokens themselves are added after each response by the proxy handler.
// limit <= 0 disables the check.
func TokenBudget(s budget.Store, limit int64, m *metrics.Metrics, st *stats.Stats, failOpen bool, log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		if s == nil || limit <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			used, resetIn, err := s.Used(r.Context(), ClientKey(r))
			if err != nil {
				m.BudgetErrors.Inc()
				log.Warn("token budget error", "err", err, "fail_open", failOpen, "request_id", reqid.From(r.Context()))
				if !failOpen {
					httpx.WriteError(w, http.StatusServiceUnavailable, "budget_unavailable", "token budget store unavailable")
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			remaining := limit - used
			if remaining < 0 {
				remaining = 0
			}
			w.Header().Set("X-Budget-Remaining-Tokens", strconv.FormatInt(remaining, 10))
			if used >= limit {
				m.BudgetRejected.Inc()
				st.RecordRateLimited() // shown with the other 429s on the dashboard
				secs := int(math.Ceil(resetIn.Seconds()))
				if secs < 1 {
					secs = 1
				}
				w.Header().Set("Retry-After", strconv.Itoa(secs))
				httpx.WriteError(w, http.StatusTooManyRequests, "insufficient_quota", "token budget used up for this API key; it resets later")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ClientKey identifies the caller. Behind a reverse proxy you would use the
// X-Forwarded-For header instead of RemoteAddr.
func ClientKey(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		sum := sha256.Sum256([]byte(auth)) // never use the raw key as a Redis key
		return "key:" + hex.EncodeToString(sum[:8])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return "ip:" + host
}
