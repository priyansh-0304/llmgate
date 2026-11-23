// Package metrics defines every Prometheus metric the gateway exports.
//
// Prometheus works by *scraping*: it calls GET /metrics every few seconds and
// stores the numbers. Counters only go up; histograms record distributions
// (that is how Grafana computes p95/p99 latency).
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type Metrics struct {
	Registry *prometheus.Registry

	// --- LLM-level metrics (recorded by the proxy handler) ---

	// LLMRequests counts completed chat requests.
	// Labels: route (ollama|mock), model, status (HTTP code), cache (hit|miss|bypass).
	LLMRequests *prometheus.CounterVec
	// Tokens is llm_tokens_total. Labels: route, model, type (prompt|completion), cache.
	Tokens *prometheus.CounterVec
	// Latency is llm_latency_seconds. Labels: route, model, cache.
	Latency *prometheus.HistogramVec
	// EstimatedCost is what the same tokens WOULD have cost on a commercial
	// API (one series per reference model). It is an estimate, not a bill.
	EstimatedCost *prometheus.CounterVec
	// UsageEstimated counts responses where the upstream sent no token usage
	// and the gateway fell back to a rough chars/4 estimate.
	UsageEstimated *prometheus.CounterVec
	CacheEvents    *prometheus.CounterVec // label: result (hit|miss|bypass)

	// --- Gateway-level metrics ---
	RateLimited       prometheus.Counter
	RateLimiterErrors prometheus.Counter
	HTTPRequests      *prometheus.CounterVec   // method, path, code
	HTTPDuration      *prometheus.HistogramVec // method, path
	InFlight          prometheus.Gauge

	// --- Resilience metrics ---

	// UpstreamAttempts counts every try against an upstream.
	// Labels: route, outcome (success|error|timeout|http_5xx|circuit_open).
	UpstreamAttempts *prometheus.CounterVec
	Retries          *prometheus.CounterVec // label: route
	Fallbacks        *prometheus.CounterVec // labels: from, to
	// CircuitState: 0 = closed (healthy), 1 = open (blocking), 2 = half-open (testing).
	CircuitState       *prometheus.GaugeVec
	CircuitTransitions *prometheus.CounterVec // labels: route, to

	// --- Token budget metrics ---
	BudgetRejected prometheus.Counter
	BudgetErrors   prometheus.Counter
}

// New creates a private registry (instead of Go's global default) so tests can
// create as many independent Metrics as they like.
func New() *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	f := promauto.With(reg)

	// LLM calls can take many seconds on a CPU, so buckets go well past 1s.
	llmBuckets := []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120}

	return &Metrics{
		Registry: reg,
		LLMRequests: f.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_requests_total",
			Help: "Chat completion requests handled, by route, model, status and cache result.",
		}, []string{"route", "model", "status", "cache"}),
		Tokens: f.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_tokens_total",
			Help: "Tokens processed, by type (prompt|completion).",
		}, []string{"route", "model", "type", "cache"}),
		Latency: f.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "llm_latency_seconds",
			Help:    "End-to-end latency of chat completion requests.",
			Buckets: llmBuckets,
		}, []string{"route", "model", "cache"}),
		EstimatedCost: f.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_estimated_cost_usd_total",
			Help: "ESTIMATED USD cost if the same tokens were billed at a reference commercial list price.",
		}, []string{"reference"}),
		UsageEstimated: f.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_usage_estimated_total",
			Help: "Responses whose token usage was estimated by the gateway because upstream sent none.",
		}, []string{"route"}),
		CacheEvents: f.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_cache_events_total",
			Help: "Response cache lookups by result.",
		}, []string{"result"}),
		RateLimited: f.NewCounter(prometheus.CounterOpts{
			Name: "llm_rate_limited_total",
			Help: "Requests rejected with 429 by the gateway rate limiter.",
		}),
		RateLimiterErrors: f.NewCounter(prometheus.CounterOpts{
			Name: "llm_rate_limiter_errors_total",
			Help: "Rate limiter backend (Redis) errors.",
		}),
		HTTPRequests: f.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_http_requests_total",
			Help: "All HTTP requests seen by the gateway.",
		}, []string{"method", "path", "code"}),
		HTTPDuration: f.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "gateway_http_request_duration_seconds",
			Help:    "HTTP request duration as seen by the gateway.",
			Buckets: llmBuckets,
		}, []string{"method", "path"}),
		InFlight: f.NewGauge(prometheus.GaugeOpts{
			Name: "gateway_http_in_flight_requests",
			Help: "Requests currently being served.",
		}),
		UpstreamAttempts: f.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_upstream_attempts_total",
			Help: "Attempts against an upstream, by outcome.",
		}, []string{"route", "outcome"}),
		Retries: f.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_retries_total",
			Help: "Retries of a failed upstream attempt.",
		}, []string{"route"}),
		Fallbacks: f.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_fallbacks_total",
			Help: "Requests moved to the fallback route because the first route failed.",
		}, []string{"from", "to"}),
		CircuitState: f.NewGaugeVec(prometheus.GaugeOpts{
			Name: "llm_circuit_state",
			Help: "Circuit breaker state per route: 0 closed, 1 open, 2 half-open.",
		}, []string{"route"}),
		CircuitTransitions: f.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_circuit_transitions_total",
			Help: "Circuit breaker state changes, by new state.",
		}, []string{"route", "to"}),
		BudgetRejected: f.NewCounter(prometheus.CounterOpts{
			Name: "llm_budget_rejected_total",
			Help: "Requests rejected with 429 because the API key's token budget is used up.",
		}),
		BudgetErrors: f.NewCounter(prometheus.CounterOpts{
			Name: "llm_budget_errors_total",
			Help: "Token budget backend (Redis) errors.",
		}),
	}
}
