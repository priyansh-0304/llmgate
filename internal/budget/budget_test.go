package budget

import (
	"context"
	"testing"
	"time"
)

func TestMemoryBudgetWindow(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	m := newMemoryWithClock(time.Hour, func() time.Time { return now })
	ctx := context.Background()

	if used, _, _ := m.Used(ctx, "k"); used != 0 {
		t.Fatalf("fresh key used = %d, want 0", used)
	}

	_ = m.Add(ctx, "k", 100)
	now = now.Add(10 * time.Minute)
	_ = m.Add(ctx, "k", 50)

	used, resetIn, _ := m.Used(ctx, "k")
	if used != 150 {
		t.Errorf("used = %d, want 150", used)
	}
	if resetIn != 50*time.Minute { // window started at the FIRST add, not the last
		t.Errorf("resetIn = %v, want 50m", resetIn)
	}

	// A different key has its own budget.
	if used, _, _ := m.Used(ctx, "other"); used != 0 {
		t.Errorf("other key used = %d, want 0", used)
	}

	// After the window passes, usage starts again from zero.
	now = now.Add(51 * time.Minute)
	if used, _, _ := m.Used(ctx, "k"); used != 0 {
		t.Errorf("used after window = %d, want 0", used)
	}
	_ = m.Add(ctx, "k", 7)
	if used, _, _ := m.Used(ctx, "k"); used != 7 {
		t.Errorf("used in new window = %d, want 7", used)
	}
}
