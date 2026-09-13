package llm

import (
	"context"
	"testing"
	"time"
)

func TestLimiterSpacesCalls(t *testing.T) {
	l := NewLimiter(600) // 600/min = one per 100ms
	ctx := context.Background()

	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := l.Wait(ctx); err != nil {
			t.Fatalf("Wait: %v", err)
		}
	}
	// First call is free; two more cost ~100ms each.
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Errorf("3 calls took %v, want at least 150ms", elapsed)
	}
}

func TestLimiterUnlimitedWhenRPMZero(t *testing.T) {
	l := NewLimiter(0)
	start := time.Now()
	for i := 0; i < 50; i++ {
		if err := l.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("unlimited limiter took %v", elapsed)
	}
}

func TestLimiterHonorsContextCancellation(t *testing.T) {
	l := NewLimiter(1) // one per minute
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_ = l.Wait(ctx) // first is free
	if err := l.Wait(ctx); err == nil {
		t.Error("want a context error on the second call")
	}
}

// A cancelled caller must not consume the slot it will never use — a bug here
// would show up as an unrelated, still-active caller waiting a full interval
// longer than it should for a burst like stage 5's concurrent persona calls.
func TestLimiterCancelledContextDoesNotConsumeSlot(t *testing.T) {
	l := NewLimiter(600) // one per 100ms
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the first call

	start := time.Now()
	if err := l.Wait(ctx); err == nil {
		t.Fatal("want a context error for an already-cancelled context")
	}
	if elapsed := time.Since(start); elapsed > 20*time.Millisecond {
		t.Errorf("Wait on a cancelled context took %v, want near-instant", elapsed)
	}

	// If the cancelled call above had reserved the first slot, this fresh
	// caller would now have to wait ~100ms for the next one instead of
	// getting the free first slot itself.
	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("second Wait took %v, want near-instant — the cancelled call must not have consumed a slot", elapsed)
	}
}
