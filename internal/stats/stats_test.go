package stats

import (
	"math"
	"testing"
	"time"
)

func TestSnapshotTotalsAndThroughput(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	s := newWithClock(func() time.Time { return now }) // fake clock

	s.RecordRequest(10, 20, 100*time.Millisecond, false)
	now = now.Add(time.Second)
	s.RecordRequest(5, 5, 300*time.Millisecond, true)
	s.RecordRateLimited()

	snap := s.Snapshot(3)

	if snap.TotalRequests != 3 {
		t.Errorf("TotalRequests = %d, want 3 (2 served + 1 rate limited)", snap.TotalRequests)
	}
	if snap.CacheHits != 1 || snap.RateLimited != 1 {
		t.Errorf("CacheHits=%d RateLimited=%d, want 1 and 1", snap.CacheHits, snap.RateLimited)
	}
	if snap.PromptTokens != 15 || snap.CompletionTokens != 25 {
		t.Errorf("tokens = %d/%d, want 15/25", snap.PromptTokens, snap.CompletionTokens)
	}
	if math.Abs(snap.AvgLatencyMs-200) > 0.001 {
		t.Errorf("AvgLatencyMs = %f, want 200", snap.AvgLatencyMs)
	}
	want := []uint64{0, 30, 10} // seconds 999999, 1000000, 1000001
	if len(snap.Throughput) != len(want) {
		t.Fatalf("throughput has %d points, want %d", len(snap.Throughput), len(want))
	}
	for i, w := range want {
		if snap.Throughput[i].Tokens != w {
			t.Errorf("throughput[%d] = %d, want %d", i, snap.Throughput[i].Tokens, w)
		}
	}
}

func TestOldSlotsAreNotReused(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	s := newWithClock(func() time.Time { return now })
	s.RecordRequest(1, 1, time.Millisecond, false)

	// Jump exactly one full window ahead: same ring index, different second.
	now = now.Add(windowSeconds * time.Second)
	snap := s.Snapshot(1)
	if snap.Throughput[0].Tokens != 0 {
		t.Errorf("stale slot leaked %d tokens into a new second", snap.Throughput[0].Tokens)
	}
}
