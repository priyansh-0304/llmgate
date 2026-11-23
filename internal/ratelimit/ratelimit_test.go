package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestMemoryTokenBucket(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := newMemoryWithClock(2, 1, func() time.Time { return now }) // burst 2, 1 token/sec
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		res, err := l.Allow(ctx, "a")
		if err != nil || !res.Allowed {
			t.Fatalf("request %d should be allowed (res=%+v err=%v)", i+1, res, err)
		}
	}

	res, _ := l.Allow(ctx, "a")
	if res.Allowed {
		t.Fatal("third immediate request should be rejected")
	}
	if res.RetryAfter < 900*time.Millisecond || res.RetryAfter > 1100*time.Millisecond {
		t.Errorf("RetryAfter = %v, want about 1s", res.RetryAfter)
	}

	// Another client has its own bucket.
	if res, _ := l.Allow(ctx, "b"); !res.Allowed {
		t.Error("a different key must not be affected")
	}

	// One second later exactly one token has refilled.
	now = now.Add(time.Second)
	if res, _ := l.Allow(ctx, "a"); !res.Allowed {
		t.Error("request after refill should be allowed")
	}
	if res, _ := l.Allow(ctx, "a"); res.Allowed {
		t.Error("only one token should have refilled")
	}
}
