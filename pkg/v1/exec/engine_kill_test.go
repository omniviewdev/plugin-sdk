package exec_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// A kill does not wait for the grace, and it does wait a moment (killWait, 50 ms by default) for the function to return. A
// function that honors its cancelled context returns within it: it is not abandoned, and its place is free when the end is
// delivered, so that a host that kills a session and starts another at once is not refused (the independent review of the
// engine, N4: 400 of 400 such starts were refused). One that does not is given up on at the end of the wait, whatever the grace.

// windingDownTarget is a target whose function honors its cancelled context and takes its time to return: it tells the test
// that it saw the cancellation, and returns when the test sends it a value.
func windingDownTarget(maxSessions int, saw chan<- struct{}, let <-chan struct{}) exec.Target {
	return engTarget(func(ctx context.Context, _ *exec.Proc) (exec.Exit, error) {
		<-ctx.Done()
		saw <- struct{}{}
		<-let
		return exec.Exit{}, ctx.Err()
	}, func(i *exec.TargetInfo) { i.Session.MaxSessions = maxSessions })
}

// awaitDoneWithin is awaitDone with a shorter bound, for a test whose failure is a wait that would otherwise last the whole of
// engineWait: it decides nothing, it ends a hang sooner.
func awaitDoneWithin(t testing.TB, cmd exechook.Command, bound time.Duration) exechook.Result {
	t.Helper()
	select {
	case <-cmd.Done():
	case <-time.After(bound):
		t.Fatalf("command %q did not end within %v", cmd.ID(), bound)
	}
	return cmd.Result()
}

// A target of one place, the kill, a function that winds down after its context was cancelled: the host reads the end and
// starts again at once, round after round, and no start is refused and no function is given up on. The kill waits for the
// function for an hour here, so that whatever the scheduler does the function returns first.
func TestAKillOfAFunctionThatReturnsWithinTheKillWaitIsNotAbandonedAndFreesItsPlace(t *testing.T) {
	ends := map[string]func(cmd exechook.Command){
		"a kill":                           func(cmd exechook.Command) { cmd.Close(true) },
		"a graceful close and then a kill": func(cmd exechook.Command) { cmd.Close(false); cmd.Close(true) },
	}
	for name, end := range ends {
		t.Run(name, func(t *testing.T) {
			saw, let := make(chan struct{}, 1), make(chan struct{}, 1)
			rig := newEngineRig(t, engineRigConfig{grace: time.Hour, killWait: time.Hour}, windingDownTarget(1, saw, let))
			for round := range 25 {
				cmd, err := rig.open(fmt.Sprintf("k%d", round), withoutTTY)
				if err != nil {
					t.Fatalf("round %d: a start at the place a kill had freed: %v", round, err)
				}
				end(cmd)
				awaitChannel(t, saw, "the function to see that its context was cancelled")
				let <- struct{}{}
				res := awaitDone(t, cmd)
				if res.End.GetClosed().GetBy() != execv2.ClosedBy_CLOSED_BY_REQUEST {
					t.Fatalf("round %d: the end is %v, want Closed by REQUEST", round, res.End)
				}
				if res.Abandoned || len(res.Violations) != 0 {
					t.Fatalf("round %d: abandoned %v violations %v: a function that returned within the kill wait is not given up on", round, res.Abandoned, res.Violations)
				}
				awaitChannel(t, cmd.Gone(), "the command to be removed")
			}
			if got := rig.eng.Abandoned(); got != 0 {
				t.Errorf("the engine counts %d abandoned functions for functions that all returned", got)
			}
			if v, ok := metricValue(rig, metricAbandoned, map[string]string{}); ok && v != 0 {
				t.Errorf("the abandoned gauge is %v: a function that returned within the kill wait is never counted", v)
			}
		})
	}
}

// The other side: a function that does not return is given up on when the kill wait runs out and not when the grace does. The
// grace is an hour here, so a kill that waited for it would not end within the bound of this test.
func TestAKillOfAFunctionThatDoesNotReturnGivesUpOnItAtTheKillWaitAndNeverAtTheGrace(t *testing.T) {
	gate := exectest.NewGate()
	rig := newEngineRig(t, engineRigConfig{grace: time.Hour, killWait: time.Millisecond}, stuckTarget(gate, 1))
	t.Cleanup(gate.Open)
	cmd := rig.mustOpen("a", withoutTTY)
	cmd.Close(true)
	res := awaitDoneWithin(t, cmd, 10*time.Second)
	if !res.Abandoned || !hasViolation(res.Violations, "did_not_return") || hasViolation(res.Violations, "ignored_cancel") {
		t.Errorf("abandoned %v violations %v, want the function given up on and the kill's own word, did_not_return", res.Abandoned, res.Violations)
	}
	if got := rig.eng.Abandoned(); got != 1 {
		t.Errorf("the engine counts %d abandoned functions, want 1", got)
	}
	wantMetric(t, rig, metricAbandoned, map[string]string{}, 1)
	wantMetric(t, rig, metricViolations, map[string]string{"reason": "did_not_return"}, 1)
	if _, err := rig.open("b", withoutTTY); err == nil {
		t.Error("a start was admitted into the one place that the abandoned function holds")
	}
	gate.Open()
	eventually(t, "the abandoned function to return", func() bool { return rig.eng.Abandoned() == 0 })
	eventually(t, "the place to be free", func() bool {
		next, err := rig.open("c", withoutTTY)
		if err != nil {
			return false
		}
		next.Close(true)
		<-next.Done()
		return true
	})
}

// Each kind of end waits for its own bound. The engine's timers are replaced by ones this test fires, so that the bound is the
// one that was armed (its duration is read) and the end is decided by firing it: no clock decides anything. The three bounds
// have different durations so that a timer of the wrong one is seen.
func TestEachKindOfEndWaitsForTheBoundThatAppliesToItAndNamesItsViolation(t *testing.T) {
	const grace, killWait, shutdownGrace = 7 * time.Second, 3 * time.Second, 2 * time.Second
	setup := func(t *testing.T) (rig *engineRig, timers *fakeTimers, cmd exechook.Command) {
		t.Helper()
		gate := exectest.NewGate()
		rig = newEngineRig(t, engineRigConfig{grace: grace, killWait: killWait, shutdownGrace: shutdownGrace}, stuckTarget(gate, 4))
		t.Cleanup(gate.Open) // before the engine closes: a stuck function is let go, since no timer would end the wait
		timers = newFakeTimers()
		exec.SetEngineSeams(rig.eng, exec.EngineSeams{NewTimer: timers.New})
		return rig, timers, rig.mustOpen("a", withoutTTY)
	}
	wantAbandoned := func(t *testing.T, cmd exechook.Command, word, notWord string) {
		t.Helper()
		res := awaitDone(t, cmd)
		if !res.Abandoned || !hasViolation(res.Violations, word) || hasViolation(res.Violations, notWord) {
			t.Errorf("abandoned %v violations %v, want the function given up on and %s", res.Abandoned, res.Violations, word)
		}
	}
	wantTimer := func(t *testing.T, timers *fakeTimers, want time.Duration) *fakeTimer {
		t.Helper()
		timer := timers.next(t)
		if timer.d != want {
			t.Fatalf("the engine armed a timer of %v, want %v", timer.d, want)
		}
		return timer
	}

	t.Run("a graceful close waits for the grace", func(t *testing.T) {
		_, timers, cmd := setup(t)
		cmd.Close(false)
		wantTimer(t, timers, grace).fire()
		wantAbandoned(t, cmd, "ignored_cancel", "did_not_return")
		if extra := timers.pending(); len(extra) != 0 {
			t.Errorf("the engine armed %v besides the grace", extra)
		}
	})
	t.Run("a kill waits for the kill wait and never arms the grace", func(t *testing.T) {
		_, timers, cmd := setup(t)
		cmd.Close(true)
		wantTimer(t, timers, killWait).fire()
		wantAbandoned(t, cmd, "did_not_return", "ignored_cancel")
		if extra := timers.pending(); len(extra) != 0 {
			t.Errorf("the engine armed %v besides the kill wait: a kill does not wait for the grace", extra)
		}
	})
	t.Run("a kill during the grace arms the kill wait and leaves the grace running", func(t *testing.T) {
		for _, which := range []string{"the kill wait runs out first", "the grace runs out first"} {
			t.Run(which, func(t *testing.T) {
				_, timers, cmd := setup(t)
				cmd.Close(false)
				graceTimer := wantTimer(t, timers, grace)
				cmd.Close(true)
				killTimer := wantTimer(t, timers, killWait)
				if which == "the kill wait runs out first" {
					killTimer.fire()
					wantAbandoned(t, cmd, "did_not_return", "ignored_cancel")
				} else {
					graceTimer.fire() // the grace is not cut short by a kill: when it runs out first it decides
					wantAbandoned(t, cmd, "ignored_cancel", "did_not_return")
				}
			})
		}
	})
	t.Run("the engine's close gives each function the shutdown grace and not the grace", func(t *testing.T) {
		rig, timers, cmd := setup(t)
		closed := make(chan error, 1)
		go func() { closed <- rig.eng.Close(context.Background()) }()
		wantTimer(t, timers, shutdownGrace).fire()
		wantAbandoned(t, cmd, "ignored_cancel", "did_not_return")
		if res := cmd.Result(); res.End.GetClosed().GetBy() != execv2.ClosedBy_CLOSED_BY_PLUGIN_SHUTDOWN {
			t.Errorf("the end is %v, want Closed by PLUGIN_SHUTDOWN", res.End)
		}
		if err := <-closed; err != nil {
			t.Errorf("Close: %v", err)
		}
		if extra := timers.pending(); len(extra) != 0 {
			t.Errorf("the engine armed %v besides the shutdown grace: the close grace does not apply to a plugin that is stopping", extra)
		}
	})
	t.Run("a graceful close in progress when the engine closes has the shutdown grace from then", func(t *testing.T) {
		rig, timers, cmd := setup(t)
		cmd.Close(false)
		wantTimer(t, timers, grace)
		go func() { _ = rig.eng.Close(context.Background()) }()
		wantTimer(t, timers, shutdownGrace).fire()
		wantAbandoned(t, cmd, "ignored_cancel", "did_not_return")
		if res := cmd.Result(); res.End.GetClosed().GetBy() != execv2.ClosedBy_CLOSED_BY_REQUEST {
			t.Errorf("the end is %v: the session was closed by its host before the plugin stopped, and the first end stands", res.End)
		}
	})
}

// A function that returns just as a wait runs out is not blamed for it: the engine decides, at the same compare-and-swap that
// the function's own return makes, whether it gave up, and records the violation only when it did. The order that a real run
// reaches only by chance is forced: the function is held right after it returned, the wait runs out (this test fires it), and
// the engine's decision is read before the function is let go.
func TestAFunctionThatReturnedWhenAWaitRanOutIsNotBlamedForIt(t *testing.T) {
	const grace, killWait = 7 * time.Second, 3 * time.Second
	for _, tc := range []struct {
		name  string
		end   func(cmd exechook.Command) // nil: the function's own end starts the wait
		bound time.Duration
	}{
		{"the kill wait", func(cmd exechook.Command) { cmd.Close(true) }, killWait},
		{"the grace", nil, grace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			functionEnded, release := exectest.NewGate(), exectest.NewGate()
			decided := make(chan bool, 1)
			timers := newFakeTimers()
			rig := newEngineRig(t, engineRigConfig{grace: grace, killWait: killWait}, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
				return exec.ExitCode(0), nil
			}))
			exec.SetEngineSeams(rig.eng, exec.EngineSeams{
				FunctionEnded:  func() { functionEnded.Open(); <-release.Opened() },
				AbandonDecided: func(abandoned bool) { decided <- abandoned },
				NewTimer:       timers.New,
			})
			t.Cleanup(release.Open)
			cmd := rig.mustOpen("s1", withoutTTY)
			awaitChannel(t, functionEnded.Opened(), "the function to end its session")
			if tc.end != nil {
				tc.end(cmd)
			}
			timers.nextOf(t, tc.bound).fire()
			select {
			case abandoned := <-decided:
				if abandoned {
					t.Fatal("the engine gave up on a function that had already returned")
				}
			case <-time.After(engineWait):
				t.Fatal("the engine never decided about the function")
			}
			release.Open()
			res := awaitDone(t, cmd)
			if res.Abandoned || len(res.Violations) != 0 {
				t.Errorf("abandoned %v violations %v: the function returned, and is not blamed for the wait that ran out as it did", res.Abandoned, res.Violations)
			}
			if v, ok := metricValue(rig, metricAbandoned, map[string]string{}); ok && v != 0 {
				t.Errorf("the abandoned gauge is %v for a function that returned", v)
			}
			if v, ok := metricValue(rig, metricViolations, map[string]string{"reason": "did_not_return"}); ok && v != 0 {
				t.Errorf("did_not_return is counted %v times for a function that returned", v)
			}
			if v, ok := metricValue(rig, metricViolations, map[string]string{"reason": "ignored_cancel"}); ok && v != 0 {
				t.Errorf("ignored_cancel is counted %v times for a function that returned", v)
			}
		})
	}
}
