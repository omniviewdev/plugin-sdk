package exec_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/lifecycletest"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// A function that ignores its cancelled context cannot be stopped: the engine gives up on it after the grace, and what it
// holds stays held until it returns. These tests pin the bound on how many of them a target can leak: an abandoned function
// keeps its session's place, so a host that starts sessions as fast as it can end them is refused, typed, instead of being
// given a new goroutine, queue and client for each.

const metricAbandonedRefused = "omniview.plugin.self.sdk.exec.abandoned.refused"

// stuckTarget is a target of the fake's routing whose function ignores its context until the test opens the gate, and that
// serves at most maxSessions sessions.
func stuckTarget(gate *exectest.Gate, maxSessions int, mods ...func(*exec.TargetInfo)) exec.Target {
	return engTarget(func(ctx context.Context, _ *exec.Proc) (exec.Exit, error) {
		<-gate.Opened()
		return exec.Exit{}, ctx.Err()
	}, append([]func(*exec.TargetInfo){func(i *exec.TargetInfo) { i.Session.MaxSessions = maxSessions }}, mods...)...)
}

// openAndAbandon opens a session on a target whose function ignores its context and ends it with a kill, which does not wait
// for the function: the session is over and its function is abandoned.
func openAndAbandon(t testing.TB, rig *engineRig, id string, mods ...func(*execv2.Start)) {
	t.Helper()
	cmd := rig.mustOpen(id, append([]func(*execv2.Start){withoutTTY}, mods...)...)
	cmd.Close(true)
	if res := awaitDone(t, cmd); !res.Abandoned {
		t.Fatalf("session %q ended without its function being given up on: %+v", id, res)
	}
}

// 3,002 kills of a target that serves 32 sessions: before the bound, 3,002 goroutines of 6.3 KiB each and none refused (the
// independent review of the engine, SHOULD 1). Now 32 are abandoned, held, counted, and every start after them is refused.
func TestAbandonedFunctionsAreBoundedByTheTargetsSessionLimit(t *testing.T) {
	const limit, attempts = 32, 3002
	gate := exectest.NewGate()
	t.Cleanup(gate.Open)
	rig := newEngineRig(t, engineRigConfig{grace: time.Millisecond}, stuckTarget(gate, limit))
	baseline := lifecycletest.TakeBaseline()

	admitted, refused := 0, 0
	for i := range attempts {
		cmd, err := rig.open(fmt.Sprintf("s%d", i), withoutTTY)
		if err != nil {
			refusal := refusalOf(t, err)
			if refusal.Class != exec.ErrorClassUnavailable {
				t.Fatalf("attempt %d: refused %v/%v, want UNAVAILABLE: the target's places are held by functions it cannot stop", i, refusal.Class, refusal.Reason)
			}
			refused++
			continue
		}
		admitted++
		cmd.Close(true)
		if res := awaitDone(t, cmd); !res.Abandoned {
			t.Fatalf("attempt %d: the function was not abandoned: %+v", i, res)
		}
	}
	if admitted != limit || refused != attempts-limit {
		t.Fatalf("admitted %d and refused %d of %d starts, want %d and %d: each abandoned function keeps its place", admitted, refused, attempts, limit, attempts-limit)
	}
	if got := rig.eng.Abandoned(); got != limit {
		t.Errorf("the engine counts %d abandoned functions, want %d", got, limit)
	}
	wantMetric(t, rig, metricAbandoned, map[string]string{}, limit)
	wantMetric(t, rig, metricAbandonedRefused, map[string]string{}, attempts-limit)
	if live := rig.eng.Live(); len(live) != 0 {
		t.Errorf("sessions that ended are still held: %v", live)
	}

	// They return: the places are free, the count is zero, nothing is left.
	gate.Open()
	eventually(t, "the abandoned functions to return", func() bool { return rig.eng.Abandoned() == 0 })
	cmd, err := rig.open("after", withoutTTY)
	if err != nil {
		t.Fatalf("a start after every abandoned function returned: %v", err)
	}
	cmd.Close(true)
	awaitDone(t, cmd)
	if err := baseline.Check(10 * time.Second); err != nil {
		t.Errorf("goroutines after the abandoned functions returned: %v", err)
	}
}

// The place of an abandoned function is free the moment it returns, and not before: a session started at that moment is
// admitted.
func TestAnAbandonedFunctionFreesItsPlaceWhenItReturns(t *testing.T) {
	gate := exectest.NewGate()
	t.Cleanup(gate.Open)
	rig := newEngineRig(t, engineRigConfig{grace: time.Millisecond}, stuckTarget(gate, 1))
	openAndAbandon(t, rig, "a")
	_, err := rig.open("b", withoutTTY)
	if refusal := refusalOf(t, err); refusal.Class != exec.ErrorClassUnavailable {
		t.Fatalf("a start while the one place is held by an abandoned function: %v, want UNAVAILABLE", refusal.Class)
	}
	gate.Open()
	eventually(t, "the place to be free", func() bool {
		cmd, err := rig.open("c", withoutTTY)
		if err != nil {
			return false
		}
		cmd.Close(true)
		<-cmd.Done()
		return true
	})
}

// A graceful close whose grace runs out abandons the function the same way a kill does, and holds the place the same way.
func TestAFunctionGivenUpOnAfterTheGraceHoldsItsPlaceToo(t *testing.T) {
	gate := exectest.NewGate()
	t.Cleanup(gate.Open)
	rig := newEngineRig(t, engineRigConfig{grace: 5 * time.Millisecond}, stuckTarget(gate, 1))
	cmd := rig.mustOpen("a", withoutTTY)
	cmd.Close(false)
	if res := awaitDone(t, cmd); !res.Abandoned || !hasViolation(res.Violations, "ignored_cancel") {
		t.Fatalf("result %+v, want the function given up on after the grace", res)
	}
	if _, err := rig.open("b", withoutTTY); err == nil {
		t.Fatal("a start was admitted into the place of a function that was given up on")
	}
}

// Each target bounds its own: the abandoned functions of one target do not refuse the starts of another.
func TestEachTargetBoundsItsOwnAbandonedFunctions(t *testing.T) {
	gate := exectest.NewGate()
	t.Cleanup(gate.Open)
	other := stuckTarget(gate, 1, func(i *exec.TargetInfo) { i.Type = "other" })
	rig := newEngineRig(t, engineRigConfig{grace: time.Millisecond}, stuckTarget(gate, 1), other)
	otherThing := func(s *execv2.Start) { s.Target = rig.conns.WireTarget(rig.t, "other", "other-1") }
	openAndAbandon(t, rig, "a")
	if _, err := rig.open("b", withoutTTY); err == nil {
		t.Fatal("a target whose one place is held admitted another session")
	}
	openAndAbandon(t, rig, "c", otherThing) // the other target is untouched
	if _, err := rig.open("d", withoutTTY, otherThing); err == nil {
		t.Fatal("the other target admitted a session while its own place is held")
	}
}

// A kill racing a function that returns by itself leaves no place held: whichever of the two decides, once every function
// has returned the target admits as many sessions as it declares.
func TestAKillRacingTheFunctionsReturnLeavesNoPlaceHeld(t *testing.T) {
	const limit = 4
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
		return exec.ExitCode(0), nil
	}, func(i *exec.TargetInfo) { i.Session.MaxSessions = limit; i.Capabilities.Kill = exec.KillTerminates }))
	for round := range 300 {
		cmd := rig.mustOpen(fmt.Sprintf("r%d", round), withoutTTY)
		cmd.Close(true)
		awaitDone(t, cmd)
	}
	eventually(t, "every function to be accounted for", func() bool { return rig.eng.Abandoned() == 0 })
	// Every place is free: the target admits all of them at once.
	var open []string
	for i := range limit {
		id := fmt.Sprintf("full%d", i)
		if _, err := rig.open(id, withoutTTY); err != nil {
			t.Fatalf("session %d of %d after the race: %v: a place was left held", i+1, limit, err)
		}
		open = append(open, id)
	}
}

// The refusal says what is true: a target whose places are all held by live sessions is LIMIT, and one whose places are held
// by functions that were abandoned is UNAVAILABLE; a host that shows "too many sessions" must not say it for the second.
func TestALimitIsALimitAndAnAbandonedPlaceIsUnavailable(t *testing.T) {
	gate := exectest.NewGate()
	t.Cleanup(gate.Open)
	live := newEngineRig(t, engineRigConfig{}, engTarget(blockUntilCancelled, func(i *exec.TargetInfo) { i.Session.MaxSessions = 1 }))
	live.mustOpen("a", withoutTTY)
	_, err := live.open("b", withoutTTY)
	if refusal := refusalOf(t, err); refusal.Class != exec.ErrorClassLimit || refusal.Reason != exec.ErrorReasonTooManySessions {
		t.Errorf("a full target of live sessions: %v/%v, want LIMIT/TOO_MANY_SESSIONS", refusal.Class, refusal.Reason)
	}
	stuck := newEngineRig(t, engineRigConfig{grace: time.Millisecond}, stuckTarget(gate, 1))
	openAndAbandon(t, stuck, "a")
	_, err = stuck.open("b", withoutTTY)
	if refusal := refusalOf(t, err); refusal.Class != exec.ErrorClassUnavailable || refusal.Reason != exec.ErrorReasonUnspecified {
		t.Errorf("a target whose place is held by an abandoned function: %v/%v, want UNAVAILABLE with no reason of its own", refusal.Class, refusal.Reason)
	}
	if logged(stuck, "exec start refused: the target's places are held by functions that were abandoned") == nil {
		t.Error("the plugin's log does not say why the start was refused")
	}
}
