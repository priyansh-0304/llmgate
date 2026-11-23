package ratelimit

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// The bucket logic runs *inside* Redis as a Lua script. Redis executes a script
// atomically, so two gateway replicas can never both spend the last token
// (a read-then-write from Go would race).
//
// KEYS[1] = bucket key; ARGV = capacity, refill/sec, cost.
// Returns {allowed(0|1), remaining(string), retry_after_ms}.
// Remaining is returned as a string because Redis truncates Lua floats to ints.
const tokenBucketLua = `
local capacity = tonumber(ARGV[1])
local refill   = tonumber(ARGV[2])
local cost     = tonumber(ARGV[3])

local t   = redis.call('TIME')
local now = t[1] * 1000 + math.floor(t[2] / 1000) -- milliseconds

local data   = redis.call('HMGET', KEYS[1], 'tokens', 'ts')
local tokens = tonumber(data[1])
local ts     = tonumber(data[2])
if tokens == nil then
  tokens = capacity
  ts = now
end

tokens = math.min(capacity, tokens + (math.max(0, now - ts) / 1000) * refill)

local allowed = 0
local retry_ms = 0
if tokens >= cost then
  tokens = tokens - cost
  allowed = 1
else
  retry_ms = math.ceil((cost - tokens) / refill * 1000)
end

redis.call('HSET', KEYS[1], 'tokens', tostring(tokens), 'ts', now)
-- Idle buckets expire after twice the time needed to refill completely.
redis.call('PEXPIRE', KEYS[1], math.ceil(capacity / refill * 2000))

return {allowed, tostring(tokens), retry_ms}
`

var tokenBucketScript = redis.NewScript(tokenBucketLua)

// Redis is a Limiter backed by a shared Redis server.
type Redis struct {
	rdb      redis.Scripter
	capacity float64
	refill   float64
}

// NewRedis accepts any redis.Scripter (the real client or a test double).
func NewRedis(rdb redis.Scripter, capacity, refillPerSec float64) *Redis {
	return &Redis{rdb: rdb, capacity: capacity, refill: refillPerSec}
}

func (r *Redis) Allow(ctx context.Context, key string) (Result, error) {
	// Run uses EVALSHA and transparently falls back to EVAL if the script
	// isn't cached on the server yet.
	raw, err := tokenBucketScript.Run(ctx, r.rdb, []string{"llmgate:rl:" + key}, r.capacity, r.refill, 1).Slice()
	if err != nil {
		return Result{}, fmt.Errorf("token bucket script: %w", err)
	}
	if len(raw) != 3 {
		return Result{}, fmt.Errorf("token bucket script returned %d values, want 3", len(raw))
	}
	allowed, ok1 := raw[0].(int64)
	remainingStr, ok2 := raw[1].(string)
	retryMs, ok3 := raw[2].(int64)
	if !ok1 || !ok2 || !ok3 {
		return Result{}, fmt.Errorf("unexpected script result types: %T %T %T", raw[0], raw[1], raw[2])
	}
	remaining, err := strconv.ParseFloat(remainingStr, 64)
	if err != nil {
		return Result{}, fmt.Errorf("parse remaining tokens: %w", err)
	}
	return Result{
		Allowed:    allowed == 1,
		Remaining:  remaining,
		RetryAfter: time.Duration(retryMs) * time.Millisecond,
	}, nil
}
