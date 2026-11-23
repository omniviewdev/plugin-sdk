package exec

import (
	"fmt"
	"time"
)

// The wire bounds of the exec contract. They are part of the contract text (proto/exec/v2/exec.proto) and apply to
// every plugin; the limits a target declares (SessionLimits, RunLimits) only tighten them.
const (
	// maxSessionIDBytes is the longest session id, in bytes of printable ASCII.
	maxSessionIDBytes = 128
	// maxRequestIDBytes is the longest run request id, in bytes of printable ASCII.
	maxRequestIDBytes = 128

	maxArgvElements     = 1024
	maxArgvElementBytes = 32 << 10
	defaultArgvBytes    = 64 << 10
	maxArgvBytesCap     = 256 << 10
	maxEnvEntries       = 256
	maxEnvNameBytes     = 256
	maxEnvValueBytes    = 32 << 10
	defaultEnvBytes     = 64 << 10
	maxEnvBytesCap      = 256 << 10
	maxCwdBytes         = 4096
	maxLabelEntries     = 16
	maxLabelKeyBytes    = 64
	maxLabelValueBytes  = 256
	maxSelectionEntries = 8
	maxSelectionValue   = 256
	maxFactBytes        = 256
	maxTermBytes        = 64
	maxDisplayName      = 128
	maxChoiceName       = 256
	maxCodeBytes        = 40
	maxPublicMessage    = 512
	maxCommandLine      = 4 << 10
	maxTargets          = 256
	maxDefaultArgv      = 64
	maxOptions          = 8
	maxChoices          = 256
	maxKittyKeyboard    = 16
	maxSignals          = 16

	minReplayBytes        = 1 << 10
	maxInputFrameCap      = 1 << 20
	maxReplayBytesCap     = 64 << 20
	maxSessionsCap        = 4096
	maxDimensionCap       = 4096
	maxDetachTimeoutCap   = 24 * time.Hour
	maxStdinBytesCap      = 1 << 20
	maxOutputBytesCap     = 1 << 20
	maxRunTimeoutCap      = time.Hour
	maxConcurrentRunsCap  = 1024
	maxPostExitDrainCap   = time.Minute
	defaultMaxSessions    = 32
	defaultMaxInputFrame  = 64 << 10
	defaultReplayBytes    = 1 << 20
	defaultMaxReplayBytes = 4 << 20
	defaultDetachTimeout  = 5 * time.Minute
	defaultMaxDetach      = 30 * time.Minute
	defaultMaxDimension   = 1024
)

// SessionLimits are the bounds an interactive session is served under. The zero value means the target serves no
// interactive sessions (a target that serves only runs, or only Options). A value with any field set declares
// sessions and takes the SDK's default for each field left at zero; DefaultSessionLimits is the all-default value.
// Describe and each session's facts report the effective values, never zero.
type SessionLimits struct {
	// MaxSessions is the most sessions the plugin holds at once for this target kind (default 32).
	MaxSessions int
	// MaxInputFrame is the largest input frame, in bytes (default 64 KiB, at most 1 MiB).
	MaxInputFrame int
	// ReplayBytes is the replay ring a session gets by default, in bytes (default 1 MiB).
	ReplayBytes int
	// MaxReplayBytes is the largest replay ring a host may ask for, in bytes (default 4 MiB, at most 64 MiB).
	MaxReplayBytes int
	// DetachTimeout is the lease of a detached session by default (default 5 minutes).
	DetachTimeout time.Duration
	// MaxDetachTimeout is the longest lease a host may ask for (default 30 minutes, at most 24 hours).
	MaxDetachTimeout time.Duration
	// MaxDimension is the largest value of either dimension of a Size (default 1024, at least 80 because the SDK
	// gives a terminal of 80 x 24 to a Start that gives no size, at most 4096).
	MaxDimension int
}

// DefaultSessionLimits returns the SDK's default session limits: the value to use when a target serves interactive
// sessions and has no reason to differ.
func DefaultSessionLimits() SessionLimits {
	return SessionLimits{
		MaxSessions:      defaultMaxSessions,
		MaxInputFrame:    defaultMaxInputFrame,
		ReplayBytes:      defaultReplayBytes,
		MaxReplayBytes:   defaultMaxReplayBytes,
		DetachTimeout:    defaultDetachTimeout,
		MaxDetachTimeout: defaultMaxDetach,
		MaxDimension:     defaultMaxDimension,
	}
}

// IsZero reports whether the limits declare no interactive sessions.
func (l SessionLimits) IsZero() bool { return l == SessionLimits{} }

// defaultPair fills a default value and its maximum so that the default never exceeds the maximum: an author that
// sets only one of the two gets a default for the other that fits.
func defaultPair[T int | time.Duration](value, maximum, defaultValue, defaultMaximum T) (T, T) {
	switch {
	case value == 0 && maximum == 0:
		return defaultValue, defaultMaximum
	case value == 0:
		return min(defaultValue, maximum), maximum
	case maximum == 0:
		return value, max(defaultMaximum, value)
	default:
		return value, maximum
	}
}

// withDefaults returns l with every zero field replaced by its default.
func (l SessionLimits) withDefaults() SessionLimits {
	d := DefaultSessionLimits()
	if l.MaxSessions == 0 {
		l.MaxSessions = d.MaxSessions
	}
	if l.MaxInputFrame == 0 {
		l.MaxInputFrame = d.MaxInputFrame
	}
	l.ReplayBytes, l.MaxReplayBytes = defaultPair(l.ReplayBytes, l.MaxReplayBytes, d.ReplayBytes, d.MaxReplayBytes)
	l.DetachTimeout, l.MaxDetachTimeout = defaultPair(l.DetachTimeout, l.MaxDetachTimeout, d.DetachTimeout, d.MaxDetachTimeout)
	if l.MaxDimension == 0 {
		l.MaxDimension = d.MaxDimension
	}
	return l
}

// limitCheck is one bound of a limits value: the field, its value and the range the contract allows. A duration
// holds its nanoseconds and reports whole milliseconds on the wire: it is checked as given, never truncated first.
type limitCheck struct {
	name                  string
	value, floor, ceiling int64
	duration              bool
}

// durationCheck is the bound of a duration limit: at least a millisecond, at most ceiling, whole milliseconds.
func durationCheck(name string, d, ceiling time.Duration) limitCheck {
	return limitCheck{name: name, value: int64(d), floor: int64(time.Millisecond), ceiling: int64(ceiling), duration: true}
}

// checkLimits verifies every bound. With allowUnknown a zero field passes: a producer that is not this SDK may
// leave a limit unstated, which means unknown and never unlimited; without it every field must be set.
func checkLimits(kind string, checks []limitCheck, allowUnknown bool) error {
	for _, c := range checks {
		if c.value == 0 && allowUnknown {
			continue
		}
		if c.value < c.floor || c.value > c.ceiling {
			if c.duration {
				return fmt.Errorf("%s limit %s is %s, outside [%s, %s]", kind, c.name, time.Duration(c.value), time.Duration(c.floor), time.Duration(c.ceiling))
			}
			return fmt.Errorf("%s limit %s is %d, outside [%d, %d]", kind, c.name, c.value, c.floor, c.ceiling)
		}
		if c.duration && c.value%int64(time.Millisecond) != 0 {
			return fmt.Errorf("%s limit %s is %s, not a whole number of milliseconds", kind, c.name, time.Duration(c.value))
		}
	}
	return nil
}

// validate checks limits: every field within its ceiling and the fields consistent with each other. With
// allowUnknown a zero field is accepted as unknown (limits decoded from another producer); without it the limits
// must be effective (default-filled), every field positive.
func (l SessionLimits) validate(allowUnknown bool) error {
	err := checkLimits("session", []limitCheck{
		{name: "MaxSessions", value: int64(l.MaxSessions), floor: 1, ceiling: maxSessionsCap},
		{name: "MaxInputFrame", value: int64(l.MaxInputFrame), floor: 1, ceiling: maxInputFrameCap},
		{name: "ReplayBytes", value: int64(l.ReplayBytes), floor: minReplayBytes, ceiling: maxReplayBytesCap},
		{name: "MaxReplayBytes", value: int64(l.MaxReplayBytes), floor: minReplayBytes, ceiling: maxReplayBytesCap},
		durationCheck("DetachTimeout", l.DetachTimeout, maxDetachTimeoutCap),
		durationCheck("MaxDetachTimeout", l.MaxDetachTimeout, maxDetachTimeoutCap),
		{name: "MaxDimension", value: int64(l.MaxDimension), floor: 1, ceiling: maxDimensionCap},
	}, allowUnknown)
	if err != nil {
		return err
	}
	// The SDK gives a terminal of defaultStartCols x defaultStartRows to a Start that asks for one without a size:
	// a target that declares sessions must allow it. A producer that is not this SDK is not held to this.
	if !allowUnknown && l.MaxDimension < int(defaultStartCols) {
		return fmt.Errorf("session limit MaxDimension is %d, below %d: the default terminal of %d x %d would exceed it",
			l.MaxDimension, defaultStartCols, defaultStartCols, defaultStartRows)
	}
	if l.MaxReplayBytes != 0 && l.ReplayBytes > l.MaxReplayBytes {
		return fmt.Errorf("session limit ReplayBytes %d exceeds MaxReplayBytes %d", l.ReplayBytes, l.MaxReplayBytes)
	}
	if l.MaxDetachTimeout != 0 && l.DetachTimeout > l.MaxDetachTimeout {
		return fmt.Errorf("session limit DetachTimeout %s exceeds MaxDetachTimeout %s", l.DetachTimeout, l.MaxDetachTimeout)
	}
	return nil
}

// RunLimits are the bounds a bounded run is served under. The zero value means the target serves no runs. A value
// with any field set declares runs and takes the SDK's default for each field left at zero; DefaultRunLimits is the
// all-default value. Describe reports the effective values, never zero.
type RunLimits struct {
	// DefaultTimeout is the timeout a run gets when the request gives none (default 30 seconds).
	DefaultTimeout time.Duration
	// MaxTimeout is the longest timeout a request may ask for (default 5 minutes, at most 1 hour).
	MaxTimeout time.Duration
	// DefaultOutputBytes is the per-stream output cap a run gets when the request gives none (default 256 KiB).
	DefaultOutputBytes int
	// MaxOutputBytes is the largest per-stream output cap a request may ask for (default 1 MiB, at most 1 MiB).
	MaxOutputBytes int
	// MaxStdinBytes is the largest stdin a request may carry (default 64 KiB, at most 1 MiB).
	MaxStdinBytes int
	// MaxConcurrent is the most runs in flight at once for this target kind (default 8).
	MaxConcurrent int
	// MaxArgvBytes is the largest total size of a request's argv (default 64 KiB, at most 256 KiB).
	MaxArgvBytes int
	// MaxEnvBytes is the largest total size of a request's env names and values (default 64 KiB, at most 256 KiB).
	MaxEnvBytes int
	// PostExitDrain is how long after the command's exit the SDK keeps reading output (default 1 second).
	PostExitDrain time.Duration
}

// DefaultRunLimits returns the SDK's default run limits.
func DefaultRunLimits() RunLimits {
	return RunLimits{
		DefaultTimeout:     30 * time.Second,
		MaxTimeout:         5 * time.Minute,
		DefaultOutputBytes: 256 << 10,
		MaxOutputBytes:     maxOutputBytesCap,
		MaxStdinBytes:      64 << 10,
		MaxConcurrent:      8,
		MaxArgvBytes:       defaultArgvBytes,
		MaxEnvBytes:        defaultEnvBytes,
		PostExitDrain:      time.Second,
	}
}

// IsZero reports whether the limits declare no runs.
func (l RunLimits) IsZero() bool { return l == RunLimits{} }

// withDefaults returns l with every zero field replaced by its default.
func (l RunLimits) withDefaults() RunLimits {
	d := DefaultRunLimits()
	l.DefaultTimeout, l.MaxTimeout = defaultPair(l.DefaultTimeout, l.MaxTimeout, d.DefaultTimeout, d.MaxTimeout)
	l.DefaultOutputBytes, l.MaxOutputBytes = defaultPair(l.DefaultOutputBytes, l.MaxOutputBytes, d.DefaultOutputBytes, d.MaxOutputBytes)
	if l.MaxStdinBytes == 0 {
		l.MaxStdinBytes = d.MaxStdinBytes
	}
	if l.MaxConcurrent == 0 {
		l.MaxConcurrent = d.MaxConcurrent
	}
	if l.MaxArgvBytes == 0 {
		l.MaxArgvBytes = d.MaxArgvBytes
	}
	if l.MaxEnvBytes == 0 {
		l.MaxEnvBytes = d.MaxEnvBytes
	}
	if l.PostExitDrain == 0 {
		l.PostExitDrain = d.PostExitDrain
	}
	return l
}

// validate checks limits as SessionLimits.validate does.
func (l RunLimits) validate(allowUnknown bool) error {
	err := checkLimits("run", []limitCheck{
		durationCheck("DefaultTimeout", l.DefaultTimeout, maxRunTimeoutCap),
		durationCheck("MaxTimeout", l.MaxTimeout, maxRunTimeoutCap),
		{name: "DefaultOutputBytes", value: int64(l.DefaultOutputBytes), floor: 1, ceiling: maxOutputBytesCap},
		{name: "MaxOutputBytes", value: int64(l.MaxOutputBytes), floor: 1, ceiling: maxOutputBytesCap},
		{name: "MaxStdinBytes", value: int64(l.MaxStdinBytes), floor: 1, ceiling: maxStdinBytesCap},
		{name: "MaxConcurrent", value: int64(l.MaxConcurrent), floor: 1, ceiling: maxConcurrentRunsCap},
		{name: "MaxArgvBytes", value: int64(l.MaxArgvBytes), floor: 1, ceiling: maxArgvBytesCap},
		{name: "MaxEnvBytes", value: int64(l.MaxEnvBytes), floor: 1, ceiling: maxEnvBytesCap},
		durationCheck("PostExitDrain", l.PostExitDrain, maxPostExitDrainCap),
	}, allowUnknown)
	if err != nil {
		return err
	}
	if l.MaxTimeout != 0 && l.DefaultTimeout > l.MaxTimeout {
		return fmt.Errorf("run limit DefaultTimeout %s exceeds MaxTimeout %s", l.DefaultTimeout, l.MaxTimeout)
	}
	if l.MaxOutputBytes != 0 && l.DefaultOutputBytes > l.MaxOutputBytes {
		return fmt.Errorf("run limit DefaultOutputBytes %d exceeds MaxOutputBytes %d", l.DefaultOutputBytes, l.MaxOutputBytes)
	}
	return nil
}
