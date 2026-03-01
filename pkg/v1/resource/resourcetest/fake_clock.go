package resourcetest

import (
	"context"
	"sync"
	"time"
)

// fakeTimer tracks a pending After() call.
type fakeTimer struct {
	deadline time.Duration // remaining time until fire
	ch       chan time.Time
}

// FakeClock implements resource.Clock for deterministic testing.
// Use Advance() to manually progress time and fire pending timers.
type FakeClock struct {
	mu      sync.Mutex
	timers  []*fakeTimer
	changed chan struct{} // broadcast channel, replaced on each change
}

// NewFakeClock creates a new FakeClock.
func NewFakeClock() *FakeClock {
	return &FakeClock{
		changed: make(chan struct{}),
	}
}

// After returns a channel that fires when Advance() progresses past the duration.
func (c *FakeClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	c.mu.Lock()
	c.timers = append(c.timers, &fakeTimer{deadline: d, ch: ch})
	// Broadcast that a timer was added.
	old := c.changed
	c.changed = make(chan struct{})
	c.mu.Unlock()
	close(old)
	return ch
}

// Advance progresses time by d, firing any timers that expire.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	var remaining []*fakeTimer
	for _, t := range c.timers {
		t.deadline -= d
		if t.deadline <= 0 {
			t.ch <- time.Now()
		} else {
			remaining = append(remaining, t)
		}
	}
	c.timers = remaining
	// Broadcast that timers changed.
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
