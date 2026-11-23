package exec_test

import (
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
)

// The gaps the independent review of the exec lane found in what the suite proved of the engine (the review of #90, SHOULD 4): each
// test below is green on the code and red against the fault it names. The engine's timings run on the clock of a synctest bubble, so
// that what a test needs to wait for (a deadline, a grace, a second of a log limit) passes without the test waiting, and what it
// asks is answered once every goroutine of the engine has done what it can without the clock.

// An Options callback that honours its context, but slowly, is waited for: the engine cancels it at the deadline and gives it the
// grace to return, and only a callback that has not returned by then is given up on (M06: with no grace for the callback, one that
// returned 200 ms after its cancellation was counted as hung, abandoned and made a violation).
func TestAnOptionsCallbackThatHonoursItsContextSlowlyIsWaitedForAndNotAbandoned(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		target := engTarget(blockUntilCancelled)
		target.Options = func(ctx *types.PluginContext, _ resourcev2.ResourceReference) ([]exec.Option, error) {
			<-ctx.Context.Done()
			time.Sleep(200 * time.Millisecond) // its cleanup: slow, and well inside the grace
			return nil, ctx.Context.Err()
		}
		rig := newEngineRig(t, engineRigConfig{optionsDeadline: 100 * time.Millisecond, grace: 2 * time.Second}, target)

		_, err := rig.open("s1", withoutTTY)
		if refusal := refusalOf(t, err); refusal.Class != exec.ErrorClassTimeout {
			t.Fatalf("a start whose Options ran past its deadline: %v, want TIMEOUT", refusal.Class)
		}
		if got := rig.eng.Abandoned(); got != 0 {
			t.Errorf("the engine counts %d abandoned callbacks, want none: the callback returned inside the grace", got)
		}
		if got, present := metricValue(rig, metricViolations, map[string]string{"reason": "options_hung"}); present && got != 0 {
			t.Errorf("%d violations options_hung for a callback that returned inside its grace", int(got))
		}
	})
}

// The refusal of a start for a target whose places are held by abandoned functions is logged at most once a second for each target,
// however many starts a host retries, and every refusal is counted (M08b: the limit removed, so that a host that retries without
// end writes a line for each retry).
func TestTheRefusalForHeldPlacesIsLoggedAtMostOncePerSecondForEachTarget(t *testing.T) {
	const message = "exec start refused: the target's places are held by functions that were abandoned"
	synctest.Test(t, func(t *testing.T) {
		gate := exectest.NewGate()
		t.Cleanup(gate.Open)
		rig := newEngineRig(t, engineRigConfig{grace: time.Millisecond}, stuckTarget(gate, 2))
		openAndAbandon(t, rig, "a")
		openAndAbandon(t, rig, "b")

		const refusals = 500
		for i := range refusals {
			_, err := rig.open(fmt.Sprintf("x%d", i), withoutTTY)
			if refusal := refusalOf(t, err); refusal.Class != exec.ErrorClassUnavailable {
				t.Fatalf("refusal %d: %v, want UNAVAILABLE: both places are held by functions that were abandoned", i, refusal.Class)
			}
		}
		if n := len(logged(rig, message)); n != 1 {
			t.Errorf("%d log lines for %d refusals inside one second, want 1", n, refusals)
		}
		wantMetric(t, rig, metricAbandonedRefused, map[string]string{}, refusals)

		// A second later the next refusal is said again; the one before it was only counted.
		time.Sleep(time.Second + time.Millisecond)
		if _, err := rig.open("y", withoutTTY); err == nil {
			t.Fatal("a start was admitted into a place held by an abandoned function")
		}
		if n := len(logged(rig, message)); n != 2 {
			t.Errorf("%d log lines after a second more, want 2: the limit is one line a second for each target", n)
		}
		wantMetric(t, rig, metricAbandonedRefused, map[string]string{}, refusals+1)
	})
}
