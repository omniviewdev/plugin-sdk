package exec

import (
	"strings"
	"testing"
	"time"
)

func TestDefaultLimitsAreEffectiveAndValid(t *testing.T) {
	s := DefaultSessionLimits()
	if s.IsZero() || s.withDefaults() != s {
		t.Errorf("DefaultSessionLimits must be non-zero and a fixed point of withDefaults: %+v", s)
	}
	if err := s.validate(false); err != nil {
		t.Errorf("DefaultSessionLimits is not valid: %v", err)
	}
	r := DefaultRunLimits()
	if r.IsZero() || r.withDefaults() != r {
		t.Errorf("DefaultRunLimits must be non-zero and a fixed point of withDefaults: %+v", r)
	}
	if err := r.validate(false); err != nil {
		t.Errorf("DefaultRunLimits is not valid: %v", err)
	}
	if !(SessionLimits{}).IsZero() || !(RunLimits{}).IsZero() {
		t.Error("the zero limits must mean no sessions and no runs")
	}
}

func TestSessionLimitsDefaultsFillWhatIsLeftZeroAndKeepWhatIsSet(t *testing.T) {
	d := DefaultSessionLimits()
	tests := []struct {
		name string
		in   SessionLimits
		want func(d SessionLimits) SessionLimits
	}{
		{"one field set", SessionLimits{MaxSessions: 4}, func(d SessionLimits) SessionLimits { d.MaxSessions = 4; return d }},
		{
			"a ring larger than the default maximum raises the maximum",
			SessionLimits{ReplayBytes: 8 << 20},
			func(d SessionLimits) SessionLimits { d.ReplayBytes, d.MaxReplayBytes = 8<<20, 8<<20; return d },
		},
		{
			"a maximum below the default ring lowers the ring",
			SessionLimits{MaxReplayBytes: 100 << 10},
			func(d SessionLimits) SessionLimits { d.ReplayBytes, d.MaxReplayBytes = 100<<10, 100<<10; return d },
		},
		{
			"a lease above the default maximum raises the maximum",
			SessionLimits{DetachTimeout: time.Hour},
			func(d SessionLimits) SessionLimits {
				d.DetachTimeout, d.MaxDetachTimeout = time.Hour, time.Hour
				return d
			},
		},
		{
			"a maximum lease below the default lowers the lease",
			SessionLimits{MaxDetachTimeout: time.Minute},
			func(d SessionLimits) SessionLimits {
				d.DetachTimeout, d.MaxDetachTimeout = time.Minute, time.Minute
				return d
			},
		},
		{
			"both set are kept as they are",
			SessionLimits{DetachTimeout: time.Minute, MaxDetachTimeout: time.Hour, ReplayBytes: 2 << 10, MaxReplayBytes: 3 << 10},
			func(d SessionLimits) SessionLimits {
				d.DetachTimeout, d.MaxDetachTimeout, d.ReplayBytes, d.MaxReplayBytes = time.Minute, time.Hour, 2<<10, 3<<10
				return d
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in.withDefaults()
			if want := tc.want(d); got != want {
				t.Errorf("withDefaults = %+v, want %+v", got, want)
			}
			if err := got.validate(false); err != nil {
				t.Errorf("the result is not valid: %v", err)
			}
		})
	}
}

func TestRunLimitsDefaultsFillWhatIsLeftZeroAndKeepWhatIsSet(t *testing.T) {
	d := DefaultRunLimits()
	tests := []struct {
		name string
		in   RunLimits
		want func(d RunLimits) RunLimits
	}{
		{
			"the plan's example",
			RunLimits{DefaultTimeout: 30 * time.Second, MaxTimeout: 5 * time.Minute},
			func(d RunLimits) RunLimits { return d },
		},
		{"one field set", RunLimits{MaxConcurrent: 2}, func(d RunLimits) RunLimits { d.MaxConcurrent = 2; return d }},
		{
			"a maximum timeout below the default lowers the default",
			RunLimits{MaxTimeout: 10 * time.Second},
			func(d RunLimits) RunLimits { d.DefaultTimeout, d.MaxTimeout = 10*time.Second, 10*time.Second; return d },
		},
		{
			"a default timeout above the default maximum raises the maximum",
			RunLimits{DefaultTimeout: 10 * time.Minute},
			func(d RunLimits) RunLimits { d.DefaultTimeout, d.MaxTimeout = 10*time.Minute, 10*time.Minute; return d },
		},
		{
			"a maximum output below the default lowers the default",
			RunLimits{MaxOutputBytes: 100},
			func(d RunLimits) RunLimits { d.DefaultOutputBytes, d.MaxOutputBytes = 100, 100; return d },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in.withDefaults()
			if want := tc.want(d); got != want {
				t.Errorf("withDefaults = %+v, want %+v", got, want)
			}
			if err := got.validate(false); err != nil {
				t.Errorf("the result is not valid: %v", err)
			}
		})
	}
}

func TestSessionLimitsValidateBoundsEveryField(t *testing.T) {
	d := DefaultSessionLimits()
	tests := []struct {
		name   string
		change func(l *SessionLimits)
		want   string // substring of the error; empty means valid
	}{
		{"the default", func(*SessionLimits) {}, ""},
		{"no sessions allowed", func(l *SessionLimits) { l.MaxSessions = 0 }, "MaxSessions"},
		{"negative sessions", func(l *SessionLimits) { l.MaxSessions = -1 }, "MaxSessions"},
		{"too many sessions", func(l *SessionLimits) { l.MaxSessions = maxSessionsCap + 1 }, "MaxSessions"},
		{"the most sessions", func(l *SessionLimits) { l.MaxSessions = maxSessionsCap }, ""},
		{"an input frame of zero", func(l *SessionLimits) { l.MaxInputFrame = 0 }, "MaxInputFrame"},
		{"an input frame over its ceiling", func(l *SessionLimits) { l.MaxInputFrame = maxInputFrameCap + 1 }, "MaxInputFrame"},
		{"the largest input frame", func(l *SessionLimits) { l.MaxInputFrame = maxInputFrameCap }, ""},
		{"a ring below its floor", func(l *SessionLimits) { l.ReplayBytes = minReplayBytes - 1 }, "ReplayBytes"},
		{"a ring at its floor", func(l *SessionLimits) { l.ReplayBytes = minReplayBytes }, ""},
		{"a maximum ring over its ceiling", func(l *SessionLimits) { l.MaxReplayBytes = maxReplayBytesCap + 1 }, "MaxReplayBytes"},
		{"a ring larger than its maximum", func(l *SessionLimits) { l.ReplayBytes = l.MaxReplayBytes + 1 }, "exceeds MaxReplayBytes"},
		{"a lease of less than a millisecond", func(l *SessionLimits) { l.DetachTimeout = time.Microsecond }, "DetachTimeout"},
		{"a lease over its ceiling", func(l *SessionLimits) { l.MaxDetachTimeout = maxDetachTimeoutCap + time.Millisecond }, "MaxDetachTimeout"},
		{"a lease longer than its maximum", func(l *SessionLimits) { l.DetachTimeout = l.MaxDetachTimeout + time.Millisecond }, "exceeds MaxDetachTimeout"},
		{"a dimension of zero", func(l *SessionLimits) { l.MaxDimension = 0 }, "MaxDimension"},
		{"a dimension over its ceiling", func(l *SessionLimits) { l.MaxDimension = maxDimensionCap + 1 }, "MaxDimension"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := d
			tc.change(&l)
			err := l.validate(false)
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestRunLimitsValidateBoundsEveryField(t *testing.T) {
	d := DefaultRunLimits()
	tests := []struct {
		name   string
		change func(l *RunLimits)
		want   string
	}{
		{"the default", func(*RunLimits) {}, ""},
		{"a timeout of zero", func(l *RunLimits) { l.DefaultTimeout = 0 }, "DefaultTimeout"},
		{"a timeout over an hour", func(l *RunLimits) { l.MaxTimeout = maxRunTimeoutCap + time.Millisecond }, "MaxTimeout"},
		{"a default timeout over its maximum", func(l *RunLimits) { l.DefaultTimeout = l.MaxTimeout + time.Millisecond }, "exceeds MaxTimeout"},
		{"an output cap of zero", func(l *RunLimits) { l.DefaultOutputBytes = 0 }, "DefaultOutputBytes"},
		{"an output cap over 1 MiB", func(l *RunLimits) { l.MaxOutputBytes = maxOutputBytesCap + 1 }, "MaxOutputBytes"},
		{"a default output over its maximum", func(l *RunLimits) { l.MaxOutputBytes, l.DefaultOutputBytes = 1000, 2000 }, "exceeds MaxOutputBytes"},
		{"stdin over 1 MiB", func(l *RunLimits) { l.MaxStdinBytes = maxStdinBytesCap + 1 }, "MaxStdinBytes"},
		{"no concurrency", func(l *RunLimits) { l.MaxConcurrent = 0 }, "MaxConcurrent"},
		{"too much concurrency", func(l *RunLimits) { l.MaxConcurrent = maxConcurrentRunsCap + 1 }, "MaxConcurrent"},
		{"argv over its ceiling", func(l *RunLimits) { l.MaxArgvBytes = maxArgvBytesCap + 1 }, "MaxArgvBytes"},
		{"env over its ceiling", func(l *RunLimits) { l.MaxEnvBytes = maxEnvBytesCap + 1 }, "MaxEnvBytes"},
		{"a drain of zero", func(l *RunLimits) { l.PostExitDrain = 0 }, "PostExitDrain"},
		{"a drain over a minute", func(l *RunLimits) { l.PostExitDrain = maxPostExitDrainCap + time.Millisecond }, "PostExitDrain"},
		{"a negative value", func(l *RunLimits) { l.MaxStdinBytes = -5 }, "MaxStdinBytes"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := d
			tc.change(&l)
			err := l.validate(false)
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestLimitsFromAnotherProducerMayLeaveAFieldUnstatedButNeverOutOfRange(t *testing.T) {
	// Zero means unknown, never unlimited: accepted when decoding, refused when registering.
	partial := SessionLimits{MaxSessions: 3}
	if err := partial.validate(true); err != nil {
		t.Errorf("a producer that states only MaxSessions: %v", err)
	}
	if err := partial.validate(false); err == nil {
		t.Error("an author's effective limits must state every field")
	}
	if err := (SessionLimits{MaxSessions: maxSessionsCap + 1}).validate(true); err == nil {
		t.Error("a stated limit above its ceiling was accepted")
	}
	if err := (SessionLimits{ReplayBytes: 2 << 20, MaxReplayBytes: 1 << 20}).validate(true); err == nil {
		t.Error("a ring above its stated maximum was accepted")
	}
	if err := (SessionLimits{ReplayBytes: 2 << 20}).validate(true); err != nil {
		t.Errorf("a ring with an unstated maximum: %v", err)
	}
	if err := (RunLimits{DefaultTimeout: time.Minute}).validate(true); err != nil {
		t.Errorf("a timeout with an unstated maximum: %v", err)
	}
	if err := (RunLimits{DefaultTimeout: time.Minute, MaxTimeout: time.Second}).validate(true); err == nil {
		t.Error("a default timeout above its stated maximum was accepted")
	}
}

func TestDurationLimitsAreValidatedAsGivenAndReportedWhole(t *testing.T) {
	sessionTests := []struct {
		name   string
		change func(l *SessionLimits)
		want   string // substring of the error; empty means valid
	}{
		{"the exact ceiling of a lease", func(l *SessionLimits) { l.MaxDetachTimeout = maxDetachTimeoutCap }, ""},
		{"a nanosecond over the ceiling of a maximum lease", func(l *SessionLimits) { l.MaxDetachTimeout = maxDetachTimeoutCap + time.Nanosecond }, "MaxDetachTimeout"},
		{"a nanosecond over the ceiling of a lease", func(l *SessionLimits) {
			l.MaxDetachTimeout = maxDetachTimeoutCap
			l.DetachTimeout = maxDetachTimeoutCap + time.Nanosecond
		}, "DetachTimeout"},
		{"a lease with a sub-millisecond part", func(l *SessionLimits) { l.DetachTimeout = time.Minute + time.Microsecond }, "DetachTimeout"},
		{"a maximum lease with a sub-millisecond part", func(l *SessionLimits) { l.MaxDetachTimeout = time.Hour + time.Nanosecond }, "MaxDetachTimeout"},
		{"a lease of whole milliseconds", func(l *SessionLimits) { l.DetachTimeout = 1500 * time.Millisecond }, ""},
	}
	for _, tc := range sessionTests {
		t.Run("session/"+tc.name, func(t *testing.T) {
			l := DefaultSessionLimits()
			tc.change(&l)
			requireLimitError(t, l.validate(false), tc.want)
		})
	}
	runTests := []struct {
		name   string
		change func(l *RunLimits)
		want   string
	}{
		{"the exact ceiling of a maximum timeout", func(l *RunLimits) { l.MaxTimeout = maxRunTimeoutCap }, ""},
		{"a nanosecond over the ceiling of a maximum timeout", func(l *RunLimits) { l.MaxTimeout = maxRunTimeoutCap + time.Nanosecond }, "MaxTimeout"},
		{"a nanosecond over the ceiling of a default timeout", func(l *RunLimits) {
			l.MaxTimeout = maxRunTimeoutCap
			l.DefaultTimeout = maxRunTimeoutCap + time.Nanosecond
		}, "DefaultTimeout"},
		{"a default timeout with a sub-millisecond part", func(l *RunLimits) { l.DefaultTimeout = time.Second + time.Nanosecond }, "DefaultTimeout"},
		{"the exact ceiling of the drain", func(l *RunLimits) { l.PostExitDrain = maxPostExitDrainCap }, ""},
		{"a nanosecond over the ceiling of the drain", func(l *RunLimits) { l.PostExitDrain = maxPostExitDrainCap + time.Nanosecond }, "PostExitDrain"},
		{"a drain with a sub-millisecond part", func(l *RunLimits) { l.PostExitDrain = time.Second + time.Microsecond }, "PostExitDrain"},
		{"a drain below a millisecond", func(l *RunLimits) { l.PostExitDrain = time.Microsecond }, "PostExitDrain"},
	}
	for _, tc := range runTests {
		t.Run("run/"+tc.name, func(t *testing.T) {
			l := DefaultRunLimits()
			tc.change(&l)
			requireLimitError(t, l.validate(false), tc.want)
		})
	}
	// Limits decoded from another producer are whole milliseconds by construction and read the same way.
	if err := (SessionLimits{MaxDetachTimeout: maxDetachTimeoutCap + time.Nanosecond}).validate(true); err == nil {
		t.Error("an unknown-tolerant validation must still refuse a value over its ceiling")
	}
}

func requireLimitError(t *testing.T, err error, want string) {
	t.Helper()
	switch {
	case want == "" && err != nil:
		t.Errorf("unexpected error: %v", err)
	case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
		t.Errorf("error = %v, want one containing %q", err, want)
	}
}

// The SDK gives a terminal of 80 x 24 to a Start that asks for one without a size, so a target that declares
// sessions must allow at least that much.
func TestSessionLimitsMustAllowTheDefaultTerminal(t *testing.T) {
	for _, tc := range []struct {
		name      string
		dimension int
		want      string
	}{
		{"below the default width", int(defaultStartCols) - 1, "MaxDimension"},
		{"one", 1, "MaxDimension"},
		{"the default width", int(defaultStartCols), ""},
		{"above it", 200, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := DefaultSessionLimits()
			l.MaxDimension = tc.dimension
			err := l.validate(false)
			requireLimitError(t, err, tc.want)
			if tc.want != "" && err != nil && !strings.Contains(err.Error(), "default terminal") {
				t.Errorf("error = %v, want it to name the default terminal", err)
			}
		})
	}
	if int(defaultStartRows) > int(defaultStartCols) {
		t.Fatal("the width check must cover the default height")
	}
	// A producer that is not this SDK states its own limits: only registration holds a target to the default size.
	if err := (SessionLimits{MaxDimension: 10}).validate(true); err != nil {
		t.Errorf("decoded limits were held to the SDK's default terminal: %v", err)
	}
}
