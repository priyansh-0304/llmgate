// Package cache stores full chat-completion responses so identical
// deterministic requests don't hit the model twice.
package cache

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cache is best-effort: a failed lookup is treated as a miss, never an error
// the client sees.
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, bool)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration)
}

// ---------- Redis ----------

type Redis struct {
	rdb *redis.Client
	log *slog.Logger
}

func NewRedis(rdb *redis.Client, log *slog.Logger) *Redis { return &Redis{rdb: rdb, log: log} }

func (c *Redis) Get(ctx context.Context, key string) ([]byte, bool) {
	val, err := c.rdb.Get(ctx, "llmgate:cache:"+key).Bytes()
	if err == redis.Nil { // redis.Nil means "key not found"; that's normal
		return nil, false
	}
	if err != nil {
		c.log.Warn("cache get failed", "err", err)
		return nil, false
	}
	return val, true
}

func (c *Redis) Set(ctx context.Context, key string, value []byte, ttl time.Duration) {
	if err := c.rdb.Set(ctx, "llmgate:cache:"+key, value, ttl).Err(); err != nil {
		c.log.Warn("cache set failed", "err", err)
	}
}

// ---------- In-memory (tests / no-Redis mode) ----------

type Memory struct {
	mu      sync.Mutex
	entries map[string]memEntry
}

type memEntry struct {
	value   []byte
	expires time.Time
}

func NewMemory() *Memory { return &Memory{entries: map[string]memEntry{}} }

func (m *Memory) Get(_ context.Context, key string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if !ok || time.Now().After(e.expires) {
		delete(m.entries, key)
		return nil, false
	}
	return e.value, true
}

func (m *Memory) Set(_ context.Context, key string, value []byte, ttl time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[key] = memEntry{value: value, expires: time.Now().Add(ttl)}
}
