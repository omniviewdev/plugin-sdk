package exec

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/status"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The timings and bounds that the engine adds for a function that does not return are written in docs/exec.md and in the doc
// comments of the constants. A change of one is a change of what every author and every host is told, so each is pinned here as
// a number and the change is made in all the places together.

func TestTheKillWaitIs50msTheShutdownGraceOneSecondAndTheOptionsCapThirtyTwo(t *testing.T) {
	if defaultKillWait != 50*time.Millisecond {
		t.Errorf("defaultKillWait = %v, the contract says 50ms", defaultKillWait)
	}
	if defaultShutdownGrace != time.Second {
		t.Errorf("defaultShutdownGrace = %v, the contract says 1s: the host stops a plugin two seconds after it asks", defaultShutdownGrace)
	}
	if defaultOptionsInFlight != 32 {
		t.Errorf("defaultOptionsInFlight = %d, the contract says 32", defaultOptionsInFlight)
	}
}

// An engine built with no timings uses the defaults: a zero is "the default", never "no wait".
func TestAnEngineWithNoTimingsUsesTheDefaultKillWaitShutdownGraceAndOptionsCap(t *testing.T) {
	cfg := newEngine(engineConfig{}).cfg
	if cfg.killWait != defaultKillWait {
		t.Errorf("killWait = %v, want the default %v", cfg.killWait, defaultKillWait)
	}
	if cfg.shutdownGrace != defaultShutdownGrace {
		t.Errorf("shutdownGrace = %v, want the default %v", cfg.shutdownGrace, defaultShutdownGrace)
	}
	if cfg.optionsInFlight != defaultOptionsInFlight {
		t.Errorf("optionsInFlight = %d, want the default %d", cfg.optionsInFlight, defaultOptionsInFlight)
	}
}

// The in-process engine takes its settings from exechook.EngineConfig, and a zero field leaves the engine's own.
func TestConfigureTakesTheKillWaitTheShutdownGraceAndTheOptionsCapAndAZeroLeavesTheDefault(t *testing.T) {
	e := newEngine(engineConfig{})
	e.configure(exechook.EngineConfig{KillWait: 7 * time.Millisecond, ShutdownGrace: 9 * time.Millisecond, OptionsInFlight: 3})
	if e.cfg.killWait != 7*time.Millisecond || e.cfg.shutdownGrace != 9*time.Millisecond || e.cfg.optionsInFlight != 3 {
		t.Errorf("configured: killWait %v shutdownGrace %v optionsInFlight %d, want 7ms 9ms 3", e.cfg.killWait, e.cfg.shutdownGrace, e.cfg.optionsInFlight)
	}
	e = newEngine(engineConfig{})
	e.configure(exechook.EngineConfig{})
	if e.cfg.killWait != defaultKillWait || e.cfg.shutdownGrace != defaultShutdownGrace || e.cfg.optionsInFlight != defaultOptionsInFlight {
		t.Errorf("a zero config changed the defaults: killWait %v shutdownGrace %v optionsInFlight %d", e.cfg.killWait, e.cfg.shutdownGrace, e.cfg.optionsInFlight)
	}
}

// The line a host could provoke without end is written the first time for a key, and after that once a second for each key:
// exactly a second later is a second later, and a clock that went back writes nothing.
func TestALogLimitWritesTheFirstLineAndThenAtMostOneASecondForEachKey(t *testing.T) {
	var limit logLimit
	t0 := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	steps := []struct {
		key  string
		at   time.Duration
		want bool
	}{
		{"a", 0, true},
		{"a", 0, false},
		{"a", 999*time.Millisecond + 999*time.Microsecond, false},
		{"b", 500 * time.Millisecond, true}, // another key has its own budget
		{"a", time.Second, true},            // exactly a second later
		{"a", time.Second + time.Millisecond, false},
		{"a", -time.Hour, false}, // a clock that went back
		{"a", 2 * time.Second, true},
	}
	for i, step := range steps {
		if got := limit.allow(step.key, t0.Add(step.at)); got != step.want {
			t.Errorf("step %d: allow(%q, +%v) = %v, want %v", i, step.key, step.at, got, step.want)
		}
	}
	// A key that was never written is allowed at the zero time, which a clock that has not been set reads.
	var fresh logLimit
	if !fresh.allow("a", time.Time{}) {
		t.Error("the first line for a key was not allowed at the zero time")
	}
}

// wedgedErrorInfo returns the exec-domain ErrorInfo of a status, or nil.
func wedgedErrorInfo(st *status.Status) *errdetails.ErrorInfo {
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.GetDomain() == errorDomain {
			return info
		}
	}
	return nil
}

// TARGET_WEDGED is UNAVAILABLE and nothing else: paired with any other class it is a producer's bug, which the SDK replaces with
// INTERNAL before anything is sent, and the host reads it back as the Go value it names. Its name on the wire is its own.
func TestTargetWedgedIsOnlyEverPairedWithUnavailableAndRoundTripsThroughAStatus(t *testing.T) {
	if got := ErrorReasonTargetWedged.String(); got != "TARGET_WEDGED" {
		t.Errorf("the reason is spelled %q", got)
	}
	if got := ErrorReasonTargetWedged.toProto(); got != execv2.ErrorReason_ERROR_REASON_TARGET_WEDGED {
		t.Errorf("the reason is written as %v", got)
	}
	for _, p := range errorClassPairs {
		want := p.g == ErrorClassUnavailable
		if got := validPair(p.g, ErrorReasonTargetWedged); got != want {
			t.Errorf("validPair(%s, TARGET_WEDGED) = %v, want %v", p.g, got, want)
		}
	}
	wedged := &Error{Class: ErrorClassUnavailable, Reason: ErrorReasonTargetWedged, Err: errors.New("a cause that stays local")}
	st, ok := status.FromError(wedged.status())
	if !ok {
		t.Fatal("not a status")
	}
	info := wedgedErrorInfo(st)
	if info == nil || info.GetReason() != "UNAVAILABLE" || info.GetMetadata()[metadataReason] != "TARGET_WEDGED" {
		t.Fatalf("the status says %v, want UNAVAILABLE with the reason TARGET_WEDGED", info)
	}
	got, ok := errorFromStatus(context.Background(), wedged.status()).(*Error)
	if !ok || got.Class != ErrorClassUnavailable || got.Reason != ErrorReasonTargetWedged {
		t.Errorf("the host reads %+v back, want UNAVAILABLE / TARGET_WEDGED", got)
	}
	misused := &Error{Class: ErrorClassLimit, Reason: ErrorReasonTargetWedged}
	if n := misused.normalized(); n.Class != ErrorClassInternal || n.Reason != ErrorReasonUnspecified {
		t.Errorf("LIMIT / TARGET_WEDGED normalizes to %v / %v, want INTERNAL with no reason", n.Class, n.Reason)
	}
}
