// Package ratelimit implements a token-bucket rate limiter.
//
// Token bucket in one paragraph: each client has a bucket that holds up to
// `capacity` tokens and refills at `refill` tokens/second. Every request takes
// one token; if the bucket is empty the request is rejected (HTTP 429). That
// allows short bursts (up to capacity) but caps the sustained rate.
package ratelimit

import (
	"context"
	"math"
	"sync"
	"time"
)

// Result describes the outcome of one Allow call.
type Result struct {
	Allowed    bool
	Remaining  float64       // tokens left after this request
	RetryAfter time.Duration // when to try again (only meaningful if !Allowed)
}

// Limiter is an *interface*: any type with this Allow method satisfies it
// (Go has no "implements" keyword). The gateway depends only on this
// interface, so tests can use Memory while production uses Redis.
type Limiter interface {
	Allow(ctx context.Context, key string) (Result, error)
}

// Memory is a single-process limiter. Used for tests and for running the
// gateway with no Redis at all. Note: it never evicts idle buckets, which is
// fine for a laptop but not for a long-lived public service.
type Memory struct {
	mu       sync.Mutex
	now      func() time.Time
	capacity float64
	refill   float64
	buckets  map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func NewMemory(capacity, refillPerSec float64) *Memory {
	return newMemoryWithClock(capacity, refillPerSec, time.Now)
}

func newMemoryWithClock(capacity, refillPerSec float64, now func() time.Time) *Memory {
	return &Memory{now: now, capacity: capacity, refill: refillPerSec, buckets: map[string]*bucket{}}
}

func (m *Memory) Allow(_ context.Context, key string) (Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	b, ok := m.buckets[key]
	if !ok {
		b = &bucket{tokens: m.capacity, last: now}
		m.buckets[key] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	b.tokens = math.Min(m.capacity, b.tokens+elapsed*m.refill)
	b.last = now

	if b.tokens >= 1 {
		b.tokens--
		return Result{Allowed: true, Remaining: b.tokens}, nil
	}
	wait := (1 - b.tokens) / m.refill
	return Result{Allowed: false, Remaining: b.tokens, RetryAfter: time.Duration(wait * float64(time.Second))}, nil
}
