// Package timeutil provides shared time abstractions for deterministic testing.
package timeutil

import "time"

// Clock abstracts time operations so production code uses real time
// and tests can inject controllable fakes.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// RealClock delegates to the standard time package.
type RealClock struct{}

// Now returns the current wall-clock time, including the standard library's monotonic reading.
func (RealClock) Now() time.Time                         { return time.Now() }

// After returns a channel that receives once after d; nonpositive durations fire immediately.
func (RealClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
