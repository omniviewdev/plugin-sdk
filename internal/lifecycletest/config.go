// Package lifecycletest is the lifecycle hammer that every lane's manager runs.
//
// A lane's manager creates sessions, updates them, ends them and is closed, from many
// goroutines at once, and its end is correct only if it is correct under every interleaving of
// those. A test of one interleaving at a time does not show that. The hammer runs all of them
// at once for as long as it is told, then looks at what is left: every session that was
// admitted has been ended exactly once and nothing was emitted for it afterwards, the manager
// holds nothing, Close returned within its bound, and the goroutines are back where they were.
//
// The hammer knows nothing of a lane's wire or of its sessions. The lane gives it a [Lane]
// whose sessions report what they emit to a [Recorder]; a lane with operations of its own
// reuses [Recorder], [GoroutineBaseline] and [Config] to run its own stress with the same
// end-state checks.
//
// The package is test support: it is imported by tests only.
package lifecycletest

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// envDuration is the environment variable that sets how long a hammer runs.
const envDuration = "OMNIVIEW_LIFECYCLE_HAMMER"

// DurationFromEnv returns the duration set in the environment variable
// OMNIVIEW_LIFECYCLE_HAMMER, read with [time.ParseDuration]. It returns zero and no error when
// the variable is unset or empty, and an error when its value is not a positive duration.
func DurationFromEnv() (time.Duration, error) {
	value := os.Getenv(envDuration)
	if value == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", envDuration, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s: %q is not a positive duration", envDuration, value)
	}
	return d, nil
}

// DefaultDuration is how long a hammer runs when its [Config] does not say: the value of
// OMNIVIEW_LIFECYCLE_HAMMER when that is a valid duration (CI sets it to 30s for the long
// run), otherwise one second under go test -short and five seconds without it. It never
// fails: an invalid value of the variable is reported by [Hammer], which fails the test, and
// DefaultDuration falls back to the built-in value. It must be called from a test, after the
// testing flags are parsed.
func DefaultDuration() time.Duration {
	if d, err := DurationFromEnv(); err == nil && d > 0 {
		return d
	}
	if testing.Short() {
		return time.Second
	}
	return 5 * time.Second
}

// Config tunes a hammer run. The zero value of every field means its default; see
// [Config.WithDefaults].
type Config struct {
	// Workers is the number of goroutines that create, update and end sessions. The default
	// is 8.
	Workers int
	// Duration is how long the hammer runs, in rounds of a fresh lane each. The default is
	// [DefaultDuration].
	Duration time.Duration
	// Seed seeds every goroutine's random source, so that the choices a run makes are
	// reproducible; how they interleave is not. The default is 1.
	Seed uint64
	// CloseBound is how long Close may take once it is called. The default is 10 seconds.
	CloseBound time.Duration
	// Settle is how long the goroutines get to return to their baseline once the lane is
	// closed. The default is 2 seconds.
	Settle time.Duration
}

// WithDefaults returns c with every field that is zero (or negative) set to its default.
func (c Config) WithDefaults() Config {
	if c.Workers <= 0 {
		c.Workers = 8
	}
	if c.Duration <= 0 {
		c.Duration = DefaultDuration()
	}
	if c.Seed == 0 {
		c.Seed = 1
	}
	if c.CloseBound <= 0 {
		c.CloseBound = 10 * time.Second
	}
	if c.Settle <= 0 {
		c.Settle = 2 * time.Second
	}
	return c
}
