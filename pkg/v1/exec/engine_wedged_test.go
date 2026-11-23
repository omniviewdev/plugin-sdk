package exec_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"google.golang.org/grpc/status"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// Functions that never return keep their places until the plugin restarts, and a target whose places they all hold is dead for
// that kind of call. Nothing a host can do helps, so the refusal says so with a reason of its own (UNAVAILABLE / TARGET_WEDGED:
// restart the plugin) that a host can tell from a shutdown (UNAVAILABLE, no reason: the plugin is stopping) and from a target
// that is full of live work (LIMIT). The author hears of it when the function is given up on, not at the first start that is
// refused for it (the independent review of the engine, SHOULD 3).

const (
	msgHeldRefusal = "exec start refused: the target's places are held by functions that were abandoned"
	msgViolation   = "an exec function broke the contract"
)

func TestAStartRefusedBecauseAbandonedFunctionsHoldItsPlacesIsUnavailableAndWedged(t *testing.T) {
	gate := exectest.NewGate()
	rig := newEngineRig(t, engineRigConfig{killWait: time.Millisecond}, stuckTarget(gate, 1))
	t.Cleanup(gate.Open)
	openAndAbandon(t, rig, "a")
	_, err := rig.open("b", withoutTTY)
	if refusal := refusalOf(t, err); refusal.Class != exec.ErrorClassUnavailable || refusal.Reason != exec.ErrorReasonTargetWedged {
		t.Errorf("a start into a target whose one place is held by an abandoned function: %v/%v, want UNAVAILABLE/TARGET_WEDGED", refusal.Class, refusal.Reason)
	}
	wantMetric(t, rig, metricAbandonedRefused, map[string]string{}, 1)
}

// A plugin that is shutting down is not wedged: its refusal is UNAVAILABLE with no reason, which a host reads as "this plugin
// is going away", where TARGET_WEDGED says "this target will not come back without a restart".
func TestAStartRefusedBecauseThePluginIsStoppingIsUnavailableAndNotWedged(t *testing.T) {
	rig := newEngineRig(t, engineRigConfig{}, engTarget(blockUntilCancelled))
	if err := rig.eng.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err := rig.open("a", withoutTTY)
	if refusal := refusalOf(t, err); refusal.Class != exec.ErrorClassUnavailable || refusal.Reason != exec.ErrorReasonUnspecified {
		t.Errorf("a start after the engine closed: %v/%v, want UNAVAILABLE with no reason", refusal.Class, refusal.Reason)
	}
}

// What makes a target wedged is the places that abandoned functions hold, together with the live sessions: a target of two
// places with one live session and one abandoned function has one place free for nothing a retry will change, and is wedged;
// a target whose two places are live sessions is full, and is a LIMIT whatever else it holds.
func TestHeldPlacesAndLiveSessionsTogetherAreWedgedAndLiveSessionsAloneAreALimit(t *testing.T) {
	gate := exectest.NewGate()
	rig := newEngineRig(t, engineRigConfig{killWait: time.Millisecond}, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		if p.Request.Labels["kind"] == "stuck" {
			<-gate.Opened()
			return exec.Exit{}, ctx.Err()
		}
		<-ctx.Done()
		return exec.Exit{}, ctx.Err()
	}, func(i *exec.TargetInfo) { i.Session.MaxSessions = 2 }))
	t.Cleanup(gate.Open)

	stuck := rig.mustOpen("stuck", withKind("stuck", false))
	stuck.Close(true)
	if res := awaitDone(t, stuck); !res.Abandoned {
		t.Fatalf("the stuck function was not given up on: %+v", res)
	}
	live := rig.mustOpen("live", withKind("live", false)) // one live session and one held place: the target is full
	_, err := rig.open("third", withKind("live", false))
	if refusal := refusalOf(t, err); refusal.Class != exec.ErrorClassUnavailable || refusal.Reason != exec.ErrorReasonTargetWedged {
		t.Errorf("one live session and one held place: %v/%v, want UNAVAILABLE/TARGET_WEDGED", refusal.Class, refusal.Reason)
	}

	gate.Open()
	eventually(t, "the abandoned function to return", func() bool { return rig.eng.Abandoned() == 0 })
	other := rig.mustOpen("other", withKind("live", false)) // the held place is free: two live sessions
	_, err = rig.open("fourth", withKind("live", false))
	if refusal := refusalOf(t, err); refusal.Class != exec.ErrorClassLimit || refusal.Reason != exec.ErrorReasonTooManySessions {
		t.Errorf("two live sessions: %v/%v, want LIMIT/TOO_MANY_SESSIONS", refusal.Class, refusal.Reason)
	}
	live.Close(true)
	other.Close(true)
}

// A function that is given up on is logged at error at once, and not only when a start is refused for it: a host that never
// retries must still be told. The line is written the first time and at most once a second for each target after that, because
// a host that kills and starts again as fast as it can would otherwise write a line each; every function is counted all the
// same, in the violations and in the result of its command. A close whose grace ran out and a kill whose wait did share the
// budget of their target. The clock of the limit is the test's.
func TestAnAbandonedFunctionIsLoggedAtErrorWhenItIsGivenUpOnAtMostOnceASecondForEachTarget(t *testing.T) {
	gate := exectest.NewGate()
	other := stuckTarget(gate, 8, func(i *exec.TargetInfo) { i.Type = "other" })
	rig := newEngineRig(t, engineRigConfig{grace: time.Millisecond, killWait: time.Millisecond}, stuckTarget(gate, 8), other)
	t.Cleanup(gate.Open)
	clock := newFakeClock()
	exec.SetEngineSeams(rig.eng, exec.EngineSeams{Now: clock.Now})
	otherThing := func(s *execv2.Start) { s.Target = rig.conns.WireTarget(rig.t, "other", "other-1") }

	kill := func(cmd exechook.Command) { cmd.Close(true) }
	closeGracefully := func(cmd exechook.Command) { cmd.Close(false) }
	abandon := func(id string, end func(exechook.Command), mods ...func(*execv2.Start)) {
		t.Helper()
		cmd := rig.mustOpen(id, append([]func(*execv2.Start){withoutTTY}, mods...)...)
		end(cmd)
		if res := awaitDone(t, cmd); !res.Abandoned {
			t.Fatalf("session %q ended without its function being given up on: %+v", id, res)
		}
	}
	lines := func() []map[string]any { return logged(rig, msgViolation) }

	abandon("a1", kill)
	got := lines()
	if len(got) != 1 || got[0]["level"] != "error" || got[0]["violation"] != "did_not_return" || got[0]["type"] != "thing" {
		t.Fatalf("after one kill that abandoned a function, and no start refused: %v, want one error line naming did_not_return and the target", got)
	}
	abandon("a2", kill)
	abandon("a3", closeGracefully)
	if n := len(lines()); n != 1 {
		t.Errorf("%d lines for three abandoned functions of one target in one second, want 1", n)
	}
	clock.advance(999 * time.Millisecond)
	abandon("a4", kill)
	if n := len(lines()); n != 1 {
		t.Errorf("%d lines 999 ms after the first, want 1", n)
	}
	clock.advance(time.Millisecond)
	abandon("a5", closeGracefully)
	got = lines()
	if len(got) != 2 || got[1]["level"] != "error" || got[1]["violation"] != "ignored_cancel" {
		t.Errorf("a second after the first: %v, want a second error line, naming ignored_cancel", got)
	}
	abandon("b1", kill, otherThing)
	got = lines()
	if len(got) != 3 || got[2]["type"] != "other" || got[2]["level"] != "error" {
		t.Errorf("an abandoned function of another target: %v, want a line of its own for it", got)
	}

	wantMetric(t, rig, metricViolations, map[string]string{"reason": "did_not_return"}, 4)
	wantMetric(t, rig, metricViolations, map[string]string{"reason": "ignored_cancel"}, 2)
	wantMetric(t, rig, metricAbandoned, map[string]string{}, 6)
}

// The refusal that a held place makes is counted every time and written to the log the first time and at most once a second for
// each target after that. The clock of the limit is the test's.
func TestTheRefusalForHeldPlacesIsLoggedOnceASecondForEachTargetAndEveryRefusalIsCounted(t *testing.T) {
	gate := exectest.NewGate()
	other := stuckTarget(gate, 1, func(i *exec.TargetInfo) { i.Type = "other" })
	rig := newEngineRig(t, engineRigConfig{killWait: time.Millisecond}, stuckTarget(gate, 1), other)
	t.Cleanup(gate.Open)
	clock := newFakeClock()
	exec.SetEngineSeams(rig.eng, exec.EngineSeams{Now: clock.Now})
	otherThing := func(s *execv2.Start) { s.Target = rig.conns.WireTarget(rig.t, "other", "other-1") }
	openAndAbandon(t, rig, "a")
	openAndAbandon(t, rig, "b", otherThing)

	refused := 0
	refuse := func(mods ...func(*execv2.Start)) {
		t.Helper()
		refused++
		_, err := rig.open(fmt.Sprintf("r%d", refused), append([]func(*execv2.Start){withoutTTY}, mods...)...)
		if refusal := refusalOf(t, err); refusal.Reason != exec.ErrorReasonTargetWedged {
			t.Fatalf("refusal %d: %v/%v, want TARGET_WEDGED", refused, refusal.Class, refusal.Reason)
		}
	}
	for range 50 {
		refuse()
	}
	if n := len(logged(rig, msgHeldRefusal)); n != 1 {
		t.Errorf("%d lines for 50 refusals in one second, want 1", n)
	}
	clock.advance(999 * time.Millisecond)
	refuse()
	if n := len(logged(rig, msgHeldRefusal)); n != 1 {
		t.Errorf("%d lines 999 ms after the first, want 1", n)
	}
	clock.advance(time.Millisecond)
	refuse()
	if n := len(logged(rig, msgHeldRefusal)); n != 2 {
		t.Errorf("%d lines a second after the first, want 2", n)
	}
	refuse(otherThing)
	if n := len(logged(rig, msgHeldRefusal)); n != 3 {
		t.Errorf("%d lines after a refusal of another target, want a line of its own for it: 3", n)
	}
	wantMetric(t, rig, metricAbandonedRefused, map[string]string{}, float64(refused))
}

// Over the wire: the reason crosses in the status as its own word and decodes to the Go value, a kill of a function that never
// returns still delivers the session's Ended (the end does not wait for it), and Describe still lists the target, with nothing
// marked: the doc says what a host reads from the refusal.
func TestAWedgedTargetIsRefusedWithItsOwnReasonOverTheWireAndDescribeStillListsIt(t *testing.T) {
	gate := exectest.NewGate()
	rig := newWireRig(t, stuckTarget(gate, 1))
	t.Cleanup(gate.Open)
	exec.ConfigurePluginEngine(rig.server.Plugin, exechook.EngineConfig{KillWait: time.Millisecond})

	conn := rig.mustOpen("a", withoutTTY)
	sendClose(t, conn, execv2.CloseMode_CLOSE_MODE_KILL)
	_, ended := readUntilEnded(t, conn)
	if by := ended.GetEnd().GetClosed().GetBy(); by != execv2.ClosedBy_CLOSED_BY_REQUEST {
		t.Fatalf("the session ended %v, want Closed by REQUEST", ended.GetEnd())
	}

	_, err := rig.open(context.Background(), "b", withoutTTY)
	var typed *exec.Error
	if !errors.As(err, &typed) || typed.Class != exec.ErrorClassUnavailable || typed.Reason != exec.ErrorReasonTargetWedged {
		t.Fatalf("an Open into the wedged target: %v, want UNAVAILABLE/TARGET_WEDGED", err)
	}
	_, rawErr := rig.rawOpen(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Open{Open: rig.openRequest("c", withoutTTY)}})
	st, ok := status.FromError(rawErr)
	if !ok {
		t.Fatalf("the raw Open answered %v, want a status", rawErr)
	}
	info := execErrorInfo(st)
	if info == nil || info.GetReason() != "UNAVAILABLE" || info.GetMetadata()["reason"] != "TARGET_WEDGED" {
		t.Errorf("the status says %v, want UNAVAILABLE with the reason TARGET_WEDGED", info)
	}

	described, err := rig.client.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if targets := described.GetTargets(); len(targets) != 1 || targets[0].GetProvider() != "fake" || targets[0].GetType() != "thing" {
		t.Errorf("Describe lists %v, want the wedged target, still", targets)
	}
}
