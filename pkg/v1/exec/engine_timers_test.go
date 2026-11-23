package exec_test

import (
	"sync"
	"testing"
	"time"
)

// fakeTimers are the timers of the engine's waits for author code, made by hand. The engine arms one for the grace of a
// graceful end, one for a kill's wait, one for the shutdown grace, and a test that wants to know which of them it armed, and
// when each runs out, supplies fakeTimers.New as exec.EngineSeams.NewTimer: it reads the timers in the order they were armed
// (next) and runs one out (fire). Nothing decides by the clock: a test that has to show that the engine did not give up at a
// moment has no timer of that moment to fire.
type fakeTimers struct {
	armed chan *fakeTimer
}

// fakeTimer is one timer the engine armed.
type fakeTimer struct {
	d time.Duration

	mu      sync.Mutex
	c       chan time.Time
	stopped bool
}

func newFakeTimers() *fakeTimers { return &fakeTimers{armed: make(chan *fakeTimer, 4096)} }

// New is the timer seam of the engine: a timer of d that runs out when the test fires it.
func (f *fakeTimers) New(d time.Duration) (<-chan time.Time, func()) {
	timer := &fakeTimer{d: d, c: make(chan time.Time, 1)}
	select {
	case f.armed <- timer:
	default:
		panic("fakeTimers: more than 4096 timers armed and not read: a test bug")
	}
	return timer.c, func() {
		timer.mu.Lock()
		timer.stopped = true
		timer.mu.Unlock()
	}
}

// next waits for the engine to arm a timer that the test has not read yet, and returns it. The wait ends a hang and decides
// nothing.
func (f *fakeTimers) next(t testing.TB) *fakeTimer {
	t.Helper()
	select {
	case timer := <-f.armed:
		return timer
	case <-time.After(engineWait):
		t.Fatalf("the engine armed no timer within %v", engineWait)
		return nil
	}
}

// nextOf waits for a timer of d, reading the timers armed before it, and returns it.
func (f *fakeTimers) nextOf(t testing.TB, d time.Duration) *fakeTimer {
	t.Helper()
	for {
		timer := f.next(t)
		if timer.d == d {
			return timer
		}
	}
}

// pending returns the durations of the timers that were armed and have not been read.
func (f *fakeTimers) pending() []time.Duration {
	var out []time.Duration
	for {
		select {
		case timer := <-f.armed:
			out = append(out, timer.d)
		default:
			return out
		}
	}
}

// fire runs the timer out, as a real one does when its time has passed. A timer that was stopped does not run out.
func (ft *fakeTimer) fire() {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if ft.stopped {
		return
	}
	select {
	case ft.c <- time.Time{}:
	default:
	}
}

// fakeClock is a clock a test moves by hand.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}
