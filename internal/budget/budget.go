// Package budget enforces a token budget per API key per time window
// (for example "100,000 tokens per 24 hours").
//
// The request-rate limiter answers "how fast"; the budget answers "how much".
// Tokens are only known after the model answers, so the flow is:
//
//  1. before the request: if the key has already used >= its budget, reject;
//  2. after the response: add the tokens that were actually used.
//
// That means one request can overshoot the budget slightly. This is a common
// and deliberate trade-off: the alternative is guessing token counts up front.
package budget

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Store keeps per-key token usage for the current window.
type Store interface {
	// Used returns tokens used so far in the current window and how long until
	// the window resets (0 if the key has no usage yet).
	Used(ctx context.Context, key string) (used int64, resetIn time.Duration, err error)
	// Add records tokens. The first Add of a window starts the window clock.
	Add(ctx context.Context, key string, tokens int64) error
}

// ---------- in-memory (tests / no-Redis mode) ----------

type Memory struct {
	mu      sync.Mutex
	now     func() time.Time
	window  time.Duration
	entries map[string]*entry
}

type entry struct {
	used    int64
	resetAt time.Time
}

func NewMemory(window time.Duration) *Memory { return newMemoryWithClock(window, time.Now) }

func newMemoryWithClock(window time.Duration, now func() time.Time) *Memory {
	return &Memory{now: now, window: window, entries: map[string]*entry{}}
}

func (m *Memory) Used(_ context.Context, key string) (int64, time.Duration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if !ok || !m.now().Before(e.resetAt) {
		return 0, 0, nil
	}
	return e.used, e.resetAt.Sub(m.now()), nil
}

func (m *Memory) Add(_ context.Context, key string, tokens int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	e, ok := m.entries[key]
	if !ok || !now.Before(e.resetAt) {
		e = &entry{resetAt: now.Add(m.window)}
		m.entries[key] = e
	}
	e.used += tokens
	return nil
}

// ---------- Redis ----------

// Both operations run as Lua scripts so they are atomic inside Redis.
//
// add:  INCRBY, and start the expiry clock if the key has none yet.
// used: GET plus the remaining time-to-live in milliseconds.
const addLua = `
local v = redis.call('INCRBY', KEYS[1], ARGV[1])
if redis.call('PTTL', KEYS[1]) < 0 then
  redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return v
`

const usedLua = `
local v = redis.call('GET', KEYS[1])
if not v then return {0, 0} end
return {tonumber(v), redis.call('PTTL', KEYS[1])}
`

var (
	addScript  = redis.NewScript(addLua)
	usedScript = redis.NewScript(usedLua)
)

type Redis struct {
	rdb    redis.Scripter
	window time.Duration
}

func NewRedis(rdb redis.Scripter, window time.Duration) *Redis {
	return &Redis{rdb: rdb, window: window}
}

func redisKey(key string) []string { return []string{"llmgate:budget:" + key} }

func (r *Redis) Used(ctx context.Context, key string) (int64, time.Duration, error) {
	raw, err := usedScript.Run(ctx, r.rdb, redisKey(key)).Slice()
	if err != nil {
		return 0, 0, fmt.Errorf("budget used script: %w", err)
	}
	if len(raw) != 2 {
		return 0, 0, fmt.Errorf("budget used script returned %d values, want 2", len(raw))
	}
	used, ok1 := raw[0].(int64)
	ttlMs, ok2 := raw[1].(int64)
	if !ok1 || !ok2 {
		return 0, 0, fmt.Errorf("unexpected budget script result types: %T %T", raw[0], raw[1])
	}
	if ttlMs < 0 {
		ttlMs = 0
	}
	return used, time.Duration(ttlMs) * time.Millisecond, nil
}

func (r *Redis) Add(ctx context.Context, key string, tokens int64) error {
	if err := addScript.Run(ctx, r.rdb, redisKey(key), tokens, r.window.Milliseconds()).Err(); err != nil {
		return fmt.Errorf("budget add script: %w", err)
	}
	return nil
}
