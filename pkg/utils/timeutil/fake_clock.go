package timeutil

import (
	"context"
	"sync"
	"time"
)

// fakeTimer tracks a pending After() call.
type fakeTimer struct {
	deadline time.Duration
	ch       chan time.Time
}

// FakeClock implements Clock for deterministic testing.
// Use Advance() to manually progress time and fire pending timers,
// or use NewInstantFakeClock() for tests that just need zero-delay After().
type FakeClock struct {
	mu      sync.Mutex
	now     time.Time
	timers  []*fakeTimer
	changed chan struct{}
}

// NewFakeClock creates a FakeClock anchored to a fixed time.
// After() calls register timers that fire when Advance() is called.
func NewFakeClock() *FakeClock {
	return &FakeClock{
		now:     time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		changed: make(chan struct{}),
	}
}

// NewInstantFakeClock creates a FakeClock where After() returns immediately.
// Useful for tests that just need to eliminate delays without controlling timing.
func NewInstantFakeClock() *FakeClock {
	c := NewFakeClock()
	c.now = time.Time{} // sentinel: instant mode
	return c
}

// Now returns the fake clock's current time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.now.IsZero() {
		return time.Now() // instant mode falls through to real time for Now()
	}
	return c.now
}

// After returns a channel that fires when Advance() progresses past the duration.
// In instant mode (NewInstantFakeClock), the channel fires immediately.
func (c *FakeClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)

	c.mu.Lock()
	if c.now.IsZero() {
		// Instant mode: fire immediately
		c.mu.Unlock()
		ch <- time.Now()
		return ch
	}

	c.timers = append(c.timers, &fakeTimer{deadline: d, ch: ch})
	old := c.changed
	c.changed = make(chan struct{})
	c.mu.Unlock()
	close(old)
	return ch
}

// Advance progresses time by d, firing any timers that expire.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var remaining []*fakeTimer
	for _, t := range c.timers {
		t.deadline -= d
		if t.deadline <= 0 {
			t.ch <- c.now
		} else {
			remaining = append(remaining, t)
		}
	}
	c.timers = remaining
	old := c.changed
	c.changed = make(chan struct{})
	c.mu.Unlock()
	close(old)
}

// PendingTimers returns the number of timers waiting to fire.
func (c *FakeClock) PendingTimers() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

// WaitForTimers blocks until at least n timers are pending, or ctx is cancelled.
func (c *FakeClock) WaitForTimers(ctx context.Context, n int) error {
	for {
		c.mu.Lock()
		count := len(c.timers)
		ch := c.changed
		c.mu.Unlock()
		if count >= n {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}
