// Package resilience holds the small building blocks that keep the gateway
// useful when an upstream model misbehaves: a circuit breaker, retry backoff
// and a context-aware sleep.
package resilience

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrCircuitOpen is returned when the breaker refuses to even try an upstream.
var ErrCircuitOpen = errors.New("circuit breaker is open")

// State of a circuit breaker.
type State int

// The numeric values are exported as the llm_circuit_state metric, so do not
// reorder them: 0 = healthy, 1 = blocking traffic, 2 = testing recovery.
const (
	Closed   State = iota // normal: requests flow
	Open                  // too many failures: requests are refused immediately
	HalfOpen              // cooldown over: exactly one trial request is allowed
)

func (s State) String() string {
	switch s {
	case Closed:
		return "closed"
	case Open:
		return "open"
	case HalfOpen:
		return "half-open"
	}
	return "unknown"
}

// Breaker is a circuit breaker, like the one in your house's fuse box.
//
//	Closed ──(N failures in a row)──► Open ──(cooldown passes)──► HalfOpen
//	  ▲                                ▲                              │
//	  └──────(trial succeeds)──────────┼──────────────────────────────┤
//	                                   └──────(trial fails)───────────┘
//
// Why bother? If a model is down, every request would wait for a timeout and
// pile up. Failing fast (and moving to a fallback) is faster and kinder to a
// struggling upstream.
type Breaker struct {
	mu        sync.Mutex
	now       func() time.Time
	threshold int // consecutive failures that open the breaker; <= 0 disables it
	cooldown  time.Duration
	onChange  func(State) // called while the lock is held: keep it quick

	state    State
	failures int
	openedAt time.Time
	probing  bool // true while the single HalfOpen trial request is in flight
}

// NewBreaker creates a breaker. onChange may be nil.
func NewBreaker(threshold int, cooldown time.Duration, onChange func(State)) *Breaker {
	return newBreakerWithClock(threshold, cooldown, onChange, time.Now)
}

func newBreakerWithClock(threshold int, cooldown time.Duration, onChange func(State), now func() time.Time) *Breaker {
	return &Breaker{now: now, threshold: threshold, cooldown: cooldown, onChange: onChange}
}

// Allow reports whether a request may be sent. Every Allow() that returns true
// MUST be followed by exactly one of Success, Failure or Release.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.threshold <= 0 {
		return true
	}
	switch b.state {
	case Open:
		if b.now().Sub(b.openedAt) < b.cooldown {
			return false
		}
		b.set(HalfOpen)
		b.probing = true
		return true
	case HalfOpen:
		if b.probing {
			return false // someone else is already testing; everyone else waits
		}
		b.probing = true
		return true
	}
	return true
}

// Success records a request that worked.
func (b *Breaker) Success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case HalfOpen:
		b.failures = 0
		b.probing = false
		b.set(Closed)
	case Closed:
		b.failures = 0
	case Open:
		// A late answer from a request sent before the breaker opened.
		// It tells us nothing about recovery, so ignore it.
	}
}

// Failure records a request that failed because of the upstream (not the client).
func (b *Breaker) Failure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.threshold <= 0 {
		return
	}
	switch b.state {
	case HalfOpen:
		b.probing = false
		b.open()
	case Closed:
		b.failures++
		if b.failures >= b.threshold {
			b.open()
		}
	}
}

// Release gives back an Allow() when the request ended for a reason that says
// nothing about upstream health (for example the client hung up).
func (b *Breaker) Release() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probing = false
}

// State returns the current state (for metrics and the dashboard).
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

func (b *Breaker) open() {
	b.openedAt = b.now()
	b.failures = 0
	b.set(Open)
}

func (b *Breaker) set(s State) {
	if b.state == s {
		return
	}
	b.state = s
	if b.onChange != nil {
		b.onChange(s)
	}
}

// Backoff returns how long to wait before retry number `attempt` (1, 2, ...).
// It doubles each time, is capped at 2 s, and applies "equal jitter" (half
// fixed, half random) so many clients do not retry in lockstep.
// rnd must return a value in [0, 1).
func Backoff(base time.Duration, attempt int, rnd func() float64) time.Duration {
	if base <= 0 || attempt < 1 {
		return 0
	}
	const maxDelay = 2 * time.Second
	d := base
	for i := 1; i < attempt && d < maxDelay; i++ {
		d *= 2
	}
	if d > maxDelay {
		d = maxDelay
	}
	half := d / 2
	return half + time.Duration(rnd()*float64(half))
}

// SleepCtx waits for d or until ctx is cancelled. It returns false if the
// context ended first.
func SleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
