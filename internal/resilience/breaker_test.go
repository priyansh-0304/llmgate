package resilience

import (
	"context"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func newTestBreaker(threshold int, cooldown time.Duration) (*Breaker, *fakeClock, *[]State) {
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	var changes []State
	b := newBreakerWithClock(threshold, cooldown, func(s State) { changes = append(changes, s) }, clock.now)
	return b, clock, &changes
}

func TestOpensAfterConsecutiveFailures(t *testing.T) {
	b, _, changes := newTestBreaker(3, time.Minute)

	for i := 0; i < 2; i++ {
		if !b.Allow() {
			t.Fatalf("request %d should be allowed while closed", i+1)
		}
		b.Failure()
	}
	if b.State() != Closed {
		t.Fatalf("state after 2 failures = %v, want closed", b.State())
	}
	b.Allow()
	b.Failure() // third in a row
	if b.State() != Open {
		t.Fatalf("state after 3 failures = %v, want open", b.State())
	}
	if b.Allow() {
		t.Error("open breaker must refuse requests")
	}
	if len(*changes) != 1 || (*changes)[0] != Open {
		t.Errorf("onChange calls = %v, want [open]", *changes)
	}
}

func TestSuccessResetsFailureCount(t *testing.T) {
	b, _, _ := newTestBreaker(2, time.Minute)
	b.Allow()
	b.Failure()
	b.Allow()
	b.Success() // breaks the streak
	b.Allow()
	b.Failure()
	if b.State() != Closed {
		t.Errorf("failures were not consecutive; state = %v, want closed", b.State())
	}
}

func TestHalfOpenAllowsOneProbeThenCloses(t *testing.T) {
	b, clock, _ := newTestBreaker(1, 30*time.Second)
	b.Allow()
	b.Failure() // opens
	if b.Allow() {
		t.Fatal("must refuse during cooldown")
	}

	clock.t = clock.t.Add(31 * time.Second)
	if !b.Allow() {
		t.Fatal("first request after cooldown should be the probe")
	}
	if b.State() != HalfOpen {
		t.Errorf("state = %v, want half-open", b.State())
	}
	if b.Allow() {
		t.Error("only one probe may be in flight")
	}
	b.Success()
	if b.State() != Closed {
		t.Errorf("successful probe should close the breaker, got %v", b.State())
	}
	if !b.Allow() {
		t.Error("closed breaker should allow requests")
	}
}

func TestFailedProbeReopensAndRestartsCooldown(t *testing.T) {
	b, clock, _ := newTestBreaker(1, 30*time.Second)
	b.Allow()
	b.Failure()
	clock.t = clock.t.Add(31 * time.Second)
	b.Allow() // probe
	b.Failure()
	if b.State() != Open {
		t.Fatalf("state = %v, want open", b.State())
	}
	clock.t = clock.t.Add(10 * time.Second)
	if b.Allow() {
		t.Error("cooldown should have restarted from the failed probe")
	}
}

func TestReleaseFreesTheProbeWithoutChangingState(t *testing.T) {
	b, clock, _ := newTestBreaker(1, time.Second)
	b.Allow()
	b.Failure()
	clock.t = clock.t.Add(2 * time.Second)
	b.Allow() // probe taken
	b.Release()
	if b.State() != HalfOpen {
		t.Errorf("state = %v, want half-open", b.State())
	}
	if !b.Allow() {
		t.Error("after Release another probe must be possible")
	}
}

func TestLateSuccessWhileOpenIsIgnored(t *testing.T) {
	b, _, _ := newTestBreaker(1, time.Minute)
	b.Allow()
	b.Failure()
	b.Success() // answer to a request that was already in flight
	if b.State() != Open {
		t.Errorf("state = %v, want still open", b.State())
	}
}

func TestZeroThresholdDisablesBreaker(t *testing.T) {
	b, _, _ := newTestBreaker(0, time.Minute)
	for i := 0; i < 100; i++ {
		if !b.Allow() {
			t.Fatal("disabled breaker must always allow")
		}
		b.Failure()
	}
	if b.State() != Closed {
		t.Errorf("state = %v, want closed", b.State())
	}
}

func TestBackoff(t *testing.T) {
	zero := func() float64 { return 0 } // lowest jitter: exactly half
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 50 * time.Millisecond},    // base 100ms -> half
		{2, 100 * time.Millisecond},   // 200ms -> half
		{3, 200 * time.Millisecond},   // 400ms -> half
		{20, 1000 * time.Millisecond}, // capped at 2s -> half
	}
	for _, c := range cases {
		if got := Backoff(100*time.Millisecond, c.attempt, zero); got != c.want {
			t.Errorf("Backoff(attempt=%d) = %v, want %v", c.attempt, got, c.want)
		}
	}
	almostOne := func() float64 { return 0.999999 }
	if got := Backoff(100*time.Millisecond, 1, almostOne); got < 99*time.Millisecond || got > 100*time.Millisecond {
		t.Errorf("max jitter = %v, want just under 100ms", got)
	}
	if Backoff(0, 3, zero) != 0 {
		t.Error("zero base must mean no delay")
	}
}

func TestSleepCtx(t *testing.T) {
	if !SleepCtx(context.Background(), time.Millisecond) {
		t.Error("short sleep should complete")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if SleepCtx(ctx, time.Hour) {
		t.Error("cancelled context must end the sleep early")
	}
}
