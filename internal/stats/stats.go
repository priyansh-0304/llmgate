// Package stats keeps a small in-memory summary for the dashboard.
//
// Prometheus is the durable source of truth. This package exists so the React
// dashboard can fetch one small JSON document (GET /api/stats) instead of
// speaking PromQL. It resets when the gateway restarts.
package stats

import (
	"sync"
	"time"
)

const windowSeconds = 120 // how many seconds of per-second history we keep

type slot struct {
	sec    int64
	tokens uint64
}

// Stats is safe for concurrent use: every method takes the mutex first.
// (HTTP handlers run in many goroutines at once, so shared data needs a lock.)
type Stats struct {
	mu  sync.Mutex
	now func() time.Time

	startedAt        time.Time
	requests         uint64
	cacheHits        uint64
	rateLimited      uint64
	promptTokens     uint64
	completionTokens uint64
	servedCount      uint64
	latencySumSec    float64
	ring             [windowSeconds]slot // ring buffer: index = unix second % window
}

func New() *Stats { return newWithClock(time.Now) }

func newWithClock(now func() time.Time) *Stats {
	return &Stats{now: now, startedAt: now()}
}

// RecordRequest is called once per request that reached the handler.
func (s *Stats) RecordRequest(promptTokens, completionTokens int, latency time.Duration, cacheHit bool) {
	if promptTokens < 0 {
		promptTokens = 0
	}
	if completionTokens < 0 {
		completionTokens = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock() // runs when the function returns, even on panic

	s.requests++
	s.servedCount++
	if cacheHit {
		s.cacheHits++
	}
	s.promptTokens += uint64(promptTokens)
	s.completionTokens += uint64(completionTokens)
	s.latencySumSec += latency.Seconds()

	sec := s.now().Unix()
	i := sec % windowSeconds
	if s.ring[i].sec != sec { // slot holds an older second: reset it
		s.ring[i] = slot{sec: sec}
	}
	s.ring[i].tokens += uint64(promptTokens + completionTokens)
}

// RecordRateLimited counts a request rejected before reaching the handler.
func (s *Stats) RecordRateLimited() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests++
	s.rateLimited++
}

type Point struct {
	T      int64  `json:"t"` // unix seconds
	Tokens uint64 `json:"tokens"`
}

type Snapshot struct {
	UptimeSeconds    float64 `json:"uptime_seconds"`
	TotalRequests    uint64  `json:"total_requests"`
	CacheHits        uint64  `json:"cache_hits"`
	RateLimited      uint64  `json:"rate_limited"`
	PromptTokens     uint64  `json:"prompt_tokens"`
	CompletionTokens uint64  `json:"completion_tokens"`
	AvgLatencyMs     float64 `json:"avg_latency_ms"`
	Throughput       []Point `json:"throughput"` // tokens per second, oldest first
}

// Snapshot copies the current numbers. points is how many seconds of
// throughput history to return (capped at the window size).
func (s *Stats) Snapshot(points int) Snapshot {
	if points < 1 {
		points = 1
	}
	if points > windowSeconds {
		points = windowSeconds
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	snap := Snapshot{
		UptimeSeconds:    now.Sub(s.startedAt).Seconds(),
		TotalRequests:    s.requests,
		CacheHits:        s.cacheHits,
		RateLimited:      s.rateLimited,
		PromptTokens:     s.promptTokens,
		CompletionTokens: s.completionTokens,
		Throughput:       make([]Point, 0, points),
	}
	if s.servedCount > 0 {
		snap.AvgLatencyMs = s.latencySumSec / float64(s.servedCount) * 1000
	}
	end := now.Unix()
	for sec := end - int64(points) + 1; sec <= end; sec++ {
		p := Point{T: sec}
		if sl := s.ring[sec%windowSeconds]; sl.sec == sec {
			p.Tokens = sl.tokens
		}
		snap.Throughput = append(snap.Throughput, p)
	}
	return snap
}
