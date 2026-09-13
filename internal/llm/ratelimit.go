package llm

import (
	"context"
	"sync"
	"time"
)

// Limiter spaces calls to stay inside a requests-per-minute quota. Stage 5
// fires one call per persona concurrently, so a single shared limiter is what
// keeps that burst from tripping the provider's rate limit.
type Limiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

// NewLimiter returns a limiter permitting rpm requests per minute. rpm <= 0
// disables limiting.
func NewLimiter(rpm int) *Limiter {
	if rpm <= 0 {
		return &Limiter{}
	}
	return &Limiter{interval: time.Minute / time.Duration(rpm)}
}

// Wait blocks until the caller may issue a request, or until ctx is done. An
// already-cancelled ctx returns immediately without reserving a slot — stage 5
// fires calls concurrently, so a cancelled caller must not delay the others by
// consuming a slot it will never use.
func (l *Limiter) Wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.interval == 0 {
		return nil
	}

	l.mu.Lock()
	now := time.Now()
	slot := l.next
	if slot.Before(now) {
		slot = now
	}
	l.next = slot.Add(l.interval)
	l.mu.Unlock()

	delay := time.Until(slot)
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
