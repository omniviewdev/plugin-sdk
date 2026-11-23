package exec_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
)

// A function that narrows its session to a lossy stall (Started with a narrower Stall) is never held by a reader afterwards:
// the policy of the queue follows the effective capabilities, not only the ones the session was opened with.
func TestAFunctionThatNarrowsItsSessionToLossyIsNeverHeldByAReaderAfterwards(t *testing.T) {
	const total = 2 << 20
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		narrowed := p.Capabilities
		narrowed.Stall = exec.StallLossy
		p.Started(exec.SessionFacts{Capabilities: &narrowed})
		if _, err := p.Stdout.Write(make([]byte, total)); err != nil { // nobody reads
			return exec.Exit{}, err
		}
		return exec.ExitCode(0), nil
	}, func(i *exec.TargetInfo) { i.Capabilities.Stall = exec.StallPropagates }))
	cmd := rig.mustOpen("s1", withoutTTY)
	res := awaitDone(t, cmd) // a blocking queue would hold the function here until the grace
	assertEnd(t, res.End, exited(0, byProcess, new(false)))
	if res.DroppedBytes == 0 || res.Abandoned {
		t.Errorf("dropped %d abandoned %v, want the overflow counted and the function never held", res.DroppedBytes, res.Abandoned)
	}
}

// A call of Started or Link after the session has ended, while the function still runs inside its grace, changes nothing and
// is the author's bug, as it is after the function returned.
func TestStartedAndLinkAfterTheSessionEndedAreRefusedWhileTheFunctionStillRuns(t *testing.T) {
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		<-ctx.Done() // the end records its cause before it cancels: the session has ended when this returns
		p.Started(exec.SessionFacts{PID: 42})
		p.Link(exec.LinkGood)
		return exec.Exit{}, ctx.Err()
	}, func(i *exec.TargetInfo) { i.Capabilities.LinkHealth = true }))
	cmd := rig.mustOpen("s1", withoutTTY)
	cmd.Close(false)
	res := awaitDone(t, cmd)
	if !hasViolation(cmd.Violations(), "late_call") {
		t.Errorf("violations %v, want late_call for the calls after the end", cmd.Violations())
	}
	if started, ok := cmd.Started(); ok || res.StartObserved {
		t.Errorf("a Started that came after the end was taken: %v (observed %v)", started, res.StartObserved)
	}
}

// A context that was cancelled before the Open began admits nothing that runs: the function is not called and nothing is
// left behind.
func TestAnOpenWhoseContextWasAlreadyCancelledNeverRunsTheFunctionAndLeavesNothing(t *testing.T) {
	var calls atomic.Int32
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
		calls.Add(1)
		return exec.ExitCode(0), nil
	}))
	for i := range 200 {
		ctx, cancel := context.WithCancel(rig.request())
		cancel()
		if cmd, err := rig.eng.Open(ctx, rig.openRequest(fmt.Sprintf("s%d", i))); err == nil {
			t.Fatalf("open %d with a cancelled context admitted %v", i, cmd.ID())
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("the function ran %d times for an Open that was cancelled before it began", calls.Load())
	}
	if live := rig.eng.Live(); len(live) != 0 {
		t.Fatalf("cancelled opens left %v", live)
	}
}

// A Classify that panics on an error of Options is contained: the start is refused INTERNAL, the plugin is not taken down,
// and the violation is counted.
func TestAClassifierThatPanicsOnAnOptionsErrorIsContainedAndCounted(t *testing.T) {
	var calls atomic.Int32
	target := engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
		calls.Add(1)
		return exec.ExitCode(0), nil
	})
	target.Options = func(*types.PluginContext, resourcev2.ResourceReference) ([]exec.Option, error) {
		return nil, errors.New("backend down")
	}
	target.Classify = func(error) (*exec.Error, bool) { panic("classifier bug") }
	rig := newEngineRig(t, engineRigConfig{}, target)
	_, err := rig.open("s1")
	if refusal := refusalOf(t, err); refusal.Class != exec.ErrorClassInternal {
		t.Fatalf("refused with %v, want INTERNAL", refusal.Class)
	}
	if calls.Load() != 0 {
		t.Fatal("the function ran for a refused start")
	}
	wantMetric(t, rig, metricViolations, map[string]string{"reason": "classify_panic"}, 1)
}

// The raw cause of a failure is the target's own text and may carry a secret: it is in the plugin's log at Error and at no
// other level, whether or not the target classified the failure.
func TestTheRawCauseOfAFailureIsLoggedOnlyAtError(t *testing.T) {
	canary := "CANARY-failure-cause-1c7f"
	fail := func(context.Context, *exec.Proc) (exec.Exit, error) { return exec.Exit{}, errors.New(canary) }
	classified := engTarget(fail)
	classified.Classify = func(err error) (*exec.Error, bool) {
		return exec.NewError(exec.ErrorClassNotFound, exec.ErrorReasonTargetGone, err), true
	}
	for name, target := range map[string]exec.Target{"a failure of the target": engTarget(fail), "a classified failure": classified} {
		rig := newEngineRig(t, engineRigConfig{}, target)
		awaitDone(t, rig.mustOpen("s1"))
		seen := false
		for _, rec := range rig.logs.Records() {
			m := rec.ToMap()
			if strings.Contains(fmt.Sprint(m), canary) {
				seen = true
				if m["level"] != "error" {
					t.Errorf("%s: the raw cause is in a %v record: %v", name, m["level"], m)
				}
			}
		}
		if !seen {
			t.Errorf("%s: the raw cause is not in the plugin's log at all", name)
		}
	}
}

// A kill that races the function's own return never takes a function that returned for one that was abandoned: when the
// function ended the session, its status is the end and the function is not abandoned. This is a stress of the order of the
// two decisions, not a forced interleaving: the kill and the return are released together.
func TestAKillRacingTheFunctionsOwnReturnNeverCallsAReturnedFunctionAbandoned(t *testing.T) {
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
		return exec.ExitCode(0), nil
	}, func(i *exec.TargetInfo) { i.Capabilities.Kill = exec.KillTerminates }))
	const rounds = 400
	for i := range rounds {
		cmd := rig.mustOpen(fmt.Sprintf("s%d", i), withoutTTY)
		start := exectest.NewGate()
		done := make(chan struct{})
		go func() {
			<-start.Opened()
			cmd.Close(true)
			close(done)
		}()
		start.Open()
		res := awaitDone(t, cmd)
		<-done
		if res.Cause == "finished" && (res.Abandoned || res.End.GetExited() == nil) {
			t.Fatalf("round %d: the function ended the session and returned, yet abandoned=%v end=%v", i, res.Abandoned, res.End)
		}
	}
	// A kill that reaches the supervisor before the function has returned abandons it, by design: such a function returns a
	// moment later and is no longer counted. Every function here returns, so the count falls to zero.
	eventually(t, "the functions that were abandoned in the race to return and stop being counted", func() bool {
		return rig.eng.Abandoned() == 0
	})
}
