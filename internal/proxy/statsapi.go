package proxy

import (
	"encoding/json"
	"net/http"
	"sort"
)

type refCost struct {
	ID               string  `json:"id"`
	Provider         string  `json:"provider"`
	Model            string  `json:"model"`
	InputPerMTok     float64 `json:"input_per_mtok_usd"`
	OutputPerMTok    float64 `json:"output_per_mtok_usd"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
}

// routeHealth is what the dashboard shows for each upstream.
type routeHealth struct {
	Name     string `json:"name"`
	Circuit  string `json:"circuit"` // closed | open | half-open
	Fallback bool   `json:"is_fallback"`
}

type statsResponse struct {
	// Estimated is always true: every dollar figure here is a list-price
	// estimate, never a real invoice. The UI shows an "estimated" badge.
	Estimated        bool          `json:"estimated"`
	PricingAsOf      string        `json:"pricing_as_of"`
	Disclaimer       string        `json:"disclaimer"`
	DefaultReference string        `json:"default_reference"`
	TotalRequests    uint64        `json:"total_requests"`
	CacheHits        uint64        `json:"cache_hits"`
	RateLimited      uint64        `json:"rate_limited"`
	PromptTokens     uint64        `json:"prompt_tokens"`
	CompletionTokens uint64        `json:"completion_tokens"`
	AvgLatencyMs     float64       `json:"avg_latency_ms"`
	UptimeSeconds    float64       `json:"uptime_seconds"`
	References       []refCost     `json:"references"`
	Routes           []routeHealth `json:"routes"`
	Throughput       any           `json:"throughput"`
}

// routeHealth lists every route with its circuit breaker state, sorted by name.
func (g *Gateway) routeHealth() []routeHealth {
	names := make([]string, 0, len(g.routes))
	for name := range g.routes {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]routeHealth, 0, len(names))
	for _, name := range names {
		out = append(out, routeHealth{
			Name:     name,
			Circuit:  g.routes[name].breaker.State().String(),
			Fallback: name == g.fallback,
		})
	}
	return out
}

// Stats handles GET /api/stats for the dashboard.
func (g *Gateway) Stats(w http.ResponseWriter, _ *http.Request) {
	snap := g.stats.Snapshot(60)
	refs := make([]refCost, 0, len(g.pricing.References))
	for _, ref := range g.pricing.References {
		refs = append(refs, refCost{
			ID: ref.ID, Provider: ref.Provider, Model: ref.Model,
			InputPerMTok: ref.InputPerMTok, OutputPerMTok: ref.OutputPerMTok,
			EstimatedCostUSD: ref.Cost(int64(snap.PromptTokens), int64(snap.CompletionTokens)),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(statsResponse{
		Estimated:        true,
		PricingAsOf:      g.pricing.AsOf,
		Disclaimer:       g.pricing.Disclaimer,
		DefaultReference: g.pricing.DefaultReference,
		TotalRequests:    snap.TotalRequests,
		CacheHits:        snap.CacheHits,
		RateLimited:      snap.RateLimited,
		PromptTokens:     snap.PromptTokens,
		CompletionTokens: snap.CompletionTokens,
		AvgLatencyMs:     snap.AvgLatencyMs,
		UptimeSeconds:    snap.UptimeSeconds,
		References:       refs,
		Routes:           g.routeHealth(),
		Throughput:       snap.Throughput,
	})
}
