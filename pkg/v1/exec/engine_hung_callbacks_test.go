package exec_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The author's Options callback runs in the start and its Classify function at the end of a command: author code that the
// grace of the function does not cover. A callback that ignores its context and never returns must not hold a session, a
// place, the connection's lease or the engine's Close for ever (the independent review of the engine, SHOULD 2): the engine
// gives each a deadline, then the grace, and then gives up on it, as it does on a function.

// hungOptions is an Options callback that enters, tells the test, and then ignores its context until the gate opens.
func hungOptions(gate *exectest.Gate, entered chan<- struct{}) exec.OptionsFunc {
	return func(*types.PluginContext, resourcev2.ResourceReference) ([]exec.Option, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-gate.Opened()
		return nil, nil
	}
}

// hungOptionsTarget is a target whose Options hangs, and whose function counts the times it ran.
func hungOptionsTarget(gate *exectest.Gate, calls *atomic.Int32, entered chan<- struct{}, maxSessions int) exec.Target {
	target := engTarget(func(ctx context.Context, _ *exec.Proc) (exec.Exit, error) {
		calls.Add(1)
		<-ctx.Done()
		return exec.Exit{}, ctx.Err()
	}, func(i *exec.TargetInfo) { i.Session.MaxSessions = maxSessions })
	target.Options = hungOptions(gate, entered)
	return target
}

func TestAnOptionsCallbackThatIgnoresItsContextIsGivenUpOnAtTheDeadline(t *testing.T) {
	gate := exectest.NewGate()
	t.Cleanup(gate.Open)
	var calls atomic.Int32
	rig := newEngineRig(t, engineRigConfig{optionsDeadline: 30 * time.Millisecond, grace: 20 * time.Millisecond},
		hungOptionsTarget(gate, &calls, nil, 4))

	_, err := rig.open("s1", withoutTTY)
	if refusal := refusalOf(t, err); refusal.Class != exec.ErrorClassTimeout {
		t.Fatalf("a start whose Options never returns: %v, want TIMEOUT", refusal.Class)
	}
	if calls.Load() != 0 {
		t.Fatal("the function ran for a start that was refused")
	}
	if live := rig.eng.Live(); len(live) != 0 {
		t.Fatalf("a refused start left %v", live)
	}
	if got := rig.eng.Abandoned(); got != 1 {
		t.Errorf("the engine counts %d abandoned callbacks, want 1", got)
	}
	wantMetric(t, rig, metricViolations, map[string]string{"reason": "options_hung"}, 1)

	gate.Open()
	eventually(t, "the callback to return and stop being counted", func() bool { return rig.eng.Abandoned() == 0 })
}

// Each hung callback keeps its place until it returns, as an abandoned function does: a host that retries a start whose
// Options hangs is refused, typed, after as many as the target serves, and is not given a goroutine each.
func TestHungOptionsCallbacksAreBoundedByTheTargetsSessionLimit(t *testing.T) {
	gate := exectest.NewGate()
	t.Cleanup(gate.Open)
	var calls atomic.Int32
	rig := newEngineRig(t, engineRigConfig{optionsDeadline: 20 * time.Millisecond, grace: 10 * time.Millisecond},
		hungOptionsTarget(gate, &calls, nil, 2))
	for i := range 2 {
		_, err := rig.open("s"+string(rune('a'+i)), withoutTTY)
		if refusal := refusalOf(t, err); refusal.Class != exec.ErrorClassTimeout {
			t.Fatalf("start %d: %v, want TIMEOUT", i, refusal.Class)
		}
	}
	_, err := rig.open("sc", withoutTTY)
	if refusal := refusalOf(t, err); refusal.Class != exec.ErrorClassUnavailable {
		t.Fatalf("a start while both places are held by hung callbacks: %v, want UNAVAILABLE at once", refusal.Class)
	}
	if got := rig.eng.Abandoned(); got != 2 {
		t.Errorf("the engine counts %d abandoned callbacks, want 2", got)
	}

	gate.Open() // the callbacks return, and Options now answers at once
	eventually(t, "the places to be free", func() bool {
		cmd, err := rig.open("sd", withoutTTY)
		if err != nil {
			return false
		}
		cmd.Close(true)
		<-cmd.Done()
		return true
	})
}

// The engine's Close does not wait for a callback that ignores its context longer than the grace.
func TestAnEngineClosesWhileAnOptionsCallbackHangs(t *testing.T) {
	gate := exectest.NewGate()
	t.Cleanup(gate.Open)
	var calls atomic.Int32
	entered := make(chan struct{}, 1)
	rig := newEngineRig(t, engineRigConfig{grace: 20 * time.Millisecond}, hungOptionsTarget(gate, &calls, entered, 4))

	opened := make(chan error, 1)
	go func() {
		_, err := rig.open("s1", withoutTTY)
		opened <- err
	}()
	awaitChannel2(t, entered, "the callback to be entered")
	ctx, cancel := context.WithTimeout(context.Background(), engineWait)
	defer cancel()
	if err := rig.eng.Close(ctx); err != nil {
		t.Fatalf("Close: %v: it must not wait for a callback that ignores its context", err)
	}
	select {
	case err := <-opened:
		if refusal := refusalOf(t, err); refusal.Class != exec.ErrorClassUnavailable {
			t.Errorf("the start that the shutdown found in its Options: %v, want UNAVAILABLE", refusal.Class)
		}
	case <-time.After(engineWait):
		t.Fatal("the start never returned")
	}
	if calls.Load() != 0 {
		t.Error("the function ran for a start that the shutdown refused")
	}
}

// A connection's retirement waits for every lease holder: the engine holds the lease for a command's whole life, and for
// its Options too, so a callback that ignores the cancellation of the lease must not keep the connection from stopping.
func TestARetirementDoesNotWaitForAnOptionsCallbackThatHangs(t *testing.T) {
	gate := exectest.NewGate()
	t.Cleanup(gate.Open)
	var calls atomic.Int32
	entered := make(chan struct{}, 1)
	rig := newEngineRig(t, engineRigConfig{grace: 20 * time.Millisecond}, hungOptionsTarget(gate, &calls, entered, 4))

	opened := make(chan error, 1)
	go func() {
		_, err := rig.open("s1", withoutTTY)
		opened <- err
	}()
	awaitChannel2(t, entered, "the callback to be entered")
	retired := make(chan struct{})
	go func() {
		rig.conns.Retire()
		close(retired)
	}()
	awaitChannel(t, retired, "the retirement to complete while the callback hangs")
	select {
	case err := <-opened:
		if refusal := refusalOf(t, err); refusal.Class != exec.ErrorClassConnection || refusal.Reason != exec.ErrorReasonConnectionRetired {
			t.Errorf("the start that the retirement found in its Options: %v/%v, want CONNECTION/CONNECTION_RETIRED", refusal.Class, refusal.Reason)
		}
	case <-time.After(engineWait):
		t.Fatal("the start never returned")
	}
}

// A callback that honors its context returns at the deadline by itself: nothing is abandoned and no place is held.
func TestAnOptionsCallbackThatHonorsItsContextIsNotAbandoned(t *testing.T) {
	var first atomic.Bool
	target := engTarget(blockUntilCancelled, func(i *exec.TargetInfo) { i.Session.MaxSessions = 1 })
	target.Options = func(pc *types.PluginContext, _ resourcev2.ResourceReference) ([]exec.Option, error) {
		if first.CompareAndSwap(false, true) {
			<-pc.Context.Done() // the deadline of the engine ends it
			return nil, pc.Context.Err()
		}
		return nil, nil
	}
	rig := newEngineRig(t, engineRigConfig{optionsDeadline: 20 * time.Millisecond, grace: time.Second}, target)
	_, err := rig.open("s1", withoutTTY)
	if refusal := refusalOf(t, err); refusal.Class != exec.ErrorClassTimeout {
		t.Fatalf("a callback that returned the deadline's error: %v, want TIMEOUT", refusal.Class)
	}
	if got := rig.eng.Abandoned(); got != 0 {
		t.Errorf("the engine counts %d abandoned callbacks for one that returned, want 0", got)
	}
	cmd, err := rig.open("s2", withoutTTY) // the one place is free
	if err != nil {
		t.Fatalf("the place of a callback that returned is held: %v", err)
	}
	cmd.Close(true)
	awaitDone(t, cmd)
}

// A callback given up on that panics when it returns is the author's bug and not the plugin's end.
func TestAnOptionsCallbackThatPanicsAfterTheEngineGaveUpOnItIsContained(t *testing.T) {
	gate := exectest.NewGate()
	t.Cleanup(gate.Open)
	target := engTarget(blockUntilCancelled)
	target.Options = func(*types.PluginContext, resourcev2.ResourceReference) ([]exec.Option, error) {
		<-gate.Opened()
		panic("a callback that panics after it was given up on")
	}
	rig := newEngineRig(t, engineRigConfig{optionsDeadline: 20 * time.Millisecond, grace: 10 * time.Millisecond}, target)
	if _, err := rig.open("s1", withoutTTY); err == nil {
		t.Fatal("a start whose Options hangs was admitted")
	}
	gate.Open()
	eventually(t, "the callback to panic and be accounted for", func() bool { return rig.eng.Abandoned() == 0 })
}

// A Classify that never returns does not keep a session from ending: the end is the SDK's classification of the error, and
// the author is told in the log and the counters.
func TestAClassifyThatHangsIsGivenUpOnAndTheSessionStillEnds(t *testing.T) {
	gate := exectest.NewGate()
	t.Cleanup(gate.Open)
	entered := make(chan struct{}, 1)
	target := engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
		return exec.Exit{}, errors.New("the target failed")
	})
	target.Classify = func(error) (*exec.Error, bool) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-gate.Opened()
		return nil, false
	}
	rig := newEngineRig(t, engineRigConfig{classifyDeadline: 30 * time.Millisecond, grace: 20 * time.Millisecond}, target)
	cmd := rig.mustOpen("s1", withoutTTY)
	res := awaitDone(t, cmd)
	failed := res.End.GetFailed()
	if failed == nil || failed.GetClass() != execv2.ErrorClass_ERROR_CLASS_INTERNAL {
		t.Fatalf("end %v, want Failed{INTERNAL}: the SDK's own classification of an error nobody could classify", res.End)
	}
	<-entered
	if !hasViolation(res.Violations, "classify_hung") {
		t.Errorf("violations %v, want classify_hung", res.Violations)
	}
	if got := rig.eng.Abandoned(); got != 1 {
		t.Errorf("the engine counts %d abandoned functions, want 1: the Classify that never returned", got)
	}
	gate.Open()
	eventually(t, "the Classify to return and stop being counted", func() bool { return rig.eng.Abandoned() == 0 })
}

// awaitChannel2 waits for a value on c.
func awaitChannel2[T any](t testing.TB, c <-chan T, what string) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(engineWait):
		t.Fatalf("timed out waiting for %s", what)
	}
}
