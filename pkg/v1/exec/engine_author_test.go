package exec_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// What the engine does with an author's function that breaks the contract: each way is either loud (a line in the log, a
// count in the diagnostics, a violation on the result) or contained (nothing else is harmed and nothing waits for ever), and
// most are both. One test for each of the ways the plan lists, then the ones the engine adds.

// hasViolation reports whether the violations hold one of the kind.
func hasViolation(violations []string, kind string) bool {
	return slices.ContainsFunc(violations, func(v string) bool { return strings.HasPrefix(v, kind+":") })
}

// logged returns the records of the rig's log whose message is msg, with their fields as a map.
func logged(rig *engineRig, msg string) []map[string]any {
	var found []map[string]any
	for _, record := range rig.logs.Records() {
		if record.Message == msg {
			found = append(found, record.ToMap())
		}
	}
	return found
}

// A function that returns while its process is still running, leaving goroutines that keep the Proc: the session ends at
// the return, what was written before it is delivered, and everything the goroutines do afterwards is refused, counted and
// said once in the log.
func TestAFunctionThatReturnsBeforeItsProcessEndsEndsTheSessionAtTheReturnAndRefusesWhatFollows(t *testing.T) {
	gate, late := exectest.NewGate(), exectest.NewLateWrites()
	target := exectest.NewFakeTarget(exectest.FakeScript(
		exectest.WriteOut("before"),
		exectest.OutliveWith(gate, "after", late),
		exectest.EndWith(exec.ExitCode(0)),
	))
	target.Info.Capabilities.LinkHealth = true
	rig := newEngineRig(t, engineRigConfig{}, target)
	cmd := rig.mustOpen("s1", withoutTTY)
	res := awaitDone(t, cmd)
	assertEnd(t, res.End, exited(0, byProcess, new(false)))

	// The goroutine writes after the end.
	gate.Open()
	awaitChannel(t, late.Done(), "the late goroutine to use the stdio")
	if n, err := late.Stdout(); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("a late write to stdout: %d, %v, want 0, io.ErrClosedPipe", n, err)
	}
	if n, err := late.Stderr(); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("a late write to stderr: %d, %v, want 0, io.ErrClosedPipe", n, err)
	}
	if err := late.Stdin(); !errors.Is(err, io.EOF) {
		t.Errorf("a late read of stdin: %v, want io.EOF, so that a goroutine blocked on stdin returns", err)
	}

	// What was written before the return is delivered, and nothing after it; the end did not change.
	out := drain(t, cmd)
	if string(out.data) != "before" {
		t.Errorf("the output is %q, want only what was written before the function returned", out.data)
	}
	assertEnd(t, cmd.Result().End, exited(0, byProcess, new(false)))

	// Loud: the log says it once, the violations name it, the late calls of Started and Link are refused.
	if !hasViolation(cmd.Violations(), "late_write") || !hasViolation(cmd.Violations(), "late_call") {
		t.Errorf("violations %v, want late_write and late_call", cmd.Violations())
	}
	if got := logged(rig, "an exec function broke the contract"); len(got) < 2 {
		t.Errorf("the log has %d violation lines, want one for the late write and one for the late call", len(got))
	}
}

// A function that ignores its cancelled context is given the grace and then given up on: the session ends without it, the
// engine does not wait for it for ever, the plugin's shutdown is not held up, and it is counted until it returns.
func TestAFunctionThatIgnoresItsCancelledContextIsGivenUpOnAfterTheGrace(t *testing.T) {
	const grace = 300 * time.Millisecond
	stuck := exectest.NewGate()
	target := exectest.NewFakeTarget(exectest.FakeScript(exectest.IgnoreCancel(stuck), exectest.EndWith(exec.ExitCode(0))))
	rig := newEngineRig(t, engineRigConfig{grace: grace}, target)
	t.Cleanup(stuck.Open) // the goroutine ends with the test; runs before the engine's own cleanup
	cmd := rig.mustOpen("s1")

	asked := time.Now()
	cmd.Close(false)
	res := awaitDone(t, cmd)
	waited := time.Since(asked)

	assertEnd(t, res.End, closedBy(execv2.ClosedBy_CLOSED_BY_REQUEST, new(false)))
	if waited < grace {
		t.Errorf("the end came after %v, before the grace of %v was over: a function was given up on that had time left", waited, grace)
	}
	if !res.Abandoned || !hasViolation(res.Violations, "ignored_cancel") {
		t.Errorf("abandoned %v violations %v, want the function reported as ignoring its context", res.Abandoned, res.Violations)
	}
	if got := rig.eng.Abandoned(); got != 1 {
		t.Errorf("the engine counts %d abandoned functions, want 1", got)
	}
	if errs := logged(rig, "an exec function broke the contract"); len(errs) != 1 || errs[0]["violation"] != "ignored_cancel" || errs[0]["level"] != "error" {
		t.Errorf("the log %v, want one error line naming ignored_cancel", errs)
	}

	// The stuck function ends when the test lets it; it is no longer abandoned, and the output it left is refused.
	stuck.Open()
	eventually(t, "the abandoned function to return and be forgotten", func() bool { return rig.eng.Abandoned() == 0 })
}

// A kill does not wait for the function: the end is immediate, whatever the grace, and the function is not blamed for not
// having returned.
func TestAKillEndsTheSessionWithoutWaitingForTheFunction(t *testing.T) {
	stuck := exectest.NewGate()
	target := exectest.NewFakeTarget(exectest.FakeScript(exectest.IgnoreCancel(stuck), exectest.EndWith(exec.ExitCode(0))))
	rig := newEngineRig(t, engineRigConfig{grace: time.Hour}, target) // a wait for the grace would hang the test
	t.Cleanup(stuck.Open)
	cmd := rig.mustOpen("s1")
	cmd.Close(true)
	res := awaitDone(t, cmd)
	assertEnd(t, res.End, closedBy(execv2.ClosedBy_CLOSED_BY_REQUEST, new(false)))
	if !res.Abandoned || hasViolation(res.Violations, "ignored_cancel") {
		t.Errorf("abandoned %v violations %v: a kill gives up on the function without calling that its fault", res.Abandoned, res.Violations)
	}
	stuck.Open()
	eventually(t, "the abandoned function to return", func() bool { return rig.eng.Abandoned() == 0 })
}

// A kill that arrives while a graceful end is waiting for the function ends the wait.
func TestAKillAfterAGracefulCloseEndsTheWait(t *testing.T) {
	stuck := exectest.NewGate()
	target := exectest.NewFakeTarget(exectest.FakeScript(exectest.IgnoreCancel(stuck), exectest.EndWith(exec.ExitCode(0))))
	rig := newEngineRig(t, engineRigConfig{grace: time.Hour}, target)
	t.Cleanup(stuck.Open)
	cmd := rig.mustOpen("s1")
	cmd.Close(false)
	cmd.Close(true)
	res := awaitDone(t, cmd)
	assertEnd(t, res.End, closedBy(execv2.ClosedBy_CLOSED_BY_REQUEST, new(false)))
	if !res.Abandoned {
		t.Error("the function was not given up on")
	}
}

// The shutdown of the plugin is not held up by a function that ignores its context: it returns within the grace.
func TestTheEnginesCloseIsNotHeldUpByAFunctionThatIgnoresItsContext(t *testing.T) {
	stuck := exectest.NewGate()
	target := exectest.NewFakeTarget(exectest.FakeScript(exectest.IgnoreCancel(stuck), exectest.EndWith(exec.ExitCode(0))))
	rig := newEngineRig(t, engineRigConfig{grace: 100 * time.Millisecond}, target)
	t.Cleanup(stuck.Open)
	cmd := rig.mustOpen("s1")
	if err := rig.eng.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	res := awaitDone(t, cmd)
	assertEnd(t, res.End, closedBy(execv2.ClosedBy_CLOSED_BY_PLUGIN_SHUTDOWN, new(false)))
	if live := rig.eng.Live(); len(live) != 0 {
		t.Fatalf("a closed engine holds %v", live)
	}
}

// A panic in the function is the end of its session, Failed with ErrorClassInternal, and never the plugin's: the panic value
// and the stack are in the plugin's log, and nowhere else.
func TestAPanicInTheFunctionFailsItsSessionAndNothingElse(t *testing.T) {
	canary := "CANARY-panic-value-5d3f"
	rig := newEngineRig(t, engineRigConfig{}, exectest.NewFakeTarget(exectest.FakeScript(
		exectest.WriteOut("partial"),
		exectest.Panic(canary),
	)))
	other := rig.mustOpen("other") // a neighbour that keeps running while the other one panics
	other.Close(false)
	awaitDone(t, other)

	cmd := rig.mustOpen("s1")
	res := awaitDone(t, cmd)
	assertEnd(t, res.End, failed(execv2.ErrorClass_ERROR_CLASS_INTERNAL, execv2.ErrorReason_ERROR_REASON_UNSPECIFIED, atRun, nil))
	if !hasViolation(res.Violations, "panic") {
		t.Errorf("violations %v, want panic", res.Violations)
	}
	if engContainsCanary(canary, prototext.Format(res.End), strings.Join(res.Violations, " ")) {
		t.Error("the panic value reached the end or the violations, which cross to the host")
	}
	found := false
	for _, record := range rig.logs.Records() {
		if record.Message == "an exec function panicked" {
			found = true
			values := record.ToMap()
			if !engContainsCanary(canary, values["panic"].(string)) || !strings.Contains(values["stack"].(string), "exectest.") {
				t.Errorf("the log line lacks the panic value or the stack: %v", values)
			}
		}
	}
	if !found {
		t.Error("the panic is not in the plugin's log")
	}
	if got := drain(t, cmd).data; string(got) != "partial" {
		t.Errorf("the output %q: what was written before the panic is delivered", got)
	}
}

// A function that declares Resize and never reads Proc.Resize holds nothing: resizes are accepted at once, only the
// newest is kept, and the command ends as any other. The log says so once, after the wait.
func TestAFunctionThatNeverReadsItsSizesHoldsNothingAndIsToldOnce(t *testing.T) {
	rig := newEngineRig(t, engineRigConfig{resizeUnread: time.Millisecond}, engTarget(blockUntilCancelled))
	cmd := rig.mustOpen("s1")
	for seq := uint32(1); seq <= 50000; seq++ {
		if got := cmd.Resize(seq, 100, uint16(30+seq%40)); got != exechook.ResizeAccepted {
			t.Fatalf("resize %d: %v, want accepted at once", seq, got)
		}
	}
	eventually(t, "the engine to say that the sizes are not read", func() bool { return hasViolation(cmd.Violations(), "resize_unread") })
	if n := len(logged(rig, "an exec function broke the contract")); n != 1 {
		t.Errorf("%d violation lines, want one for the whole session", n)
	}
	cmd.Close(false)
	res := awaitDone(t, cmd)
	assertEnd(t, res.End, closedBy(execv2.ClosedBy_CLOSED_BY_REQUEST, new(false)))
	if _, _, _, ok := cmd.AppliedResize(); ok {
		t.Error("a size is reported as taken by a function that never read one")
	}
}

// A function that returns an exit 0 for a connection it lost: the SDK cannot see the connection, but it can hold the
// function to what it says about itself. The ends it can contain are contained, and the log says which.
func TestAnExitZeroForALostConnectionIsContainedWhereTheSDKHasEvidence(t *testing.T) {
	linkHealth := func(i *exec.TargetInfo) { i.Capabilities.LinkHealth = true }
	t.Run("the function reported the link stalled", func(t *testing.T) {
		rig := newEngineRig(t, engineRigConfig{}, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
			p.Link(exec.LinkStalled)
			return exec.ExitCode(0), nil
		}, linkHealth))
		res := awaitDone(t, rig.mustOpen("s1"))
		assertEnd(t, res.End, exited(0, byWire, nil))
		if !hasViolation(res.Violations, "stalled_status") || res.Output != unspecified {
			t.Errorf("violations %v output %v, want the stalled status named and no claim of complete output", res.Violations, res.Output)
		}
	})
	t.Run("the function reported the link stalled and then good", func(t *testing.T) {
		rig := newEngineRig(t, engineRigConfig{}, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
			p.Link(exec.LinkStalled)
			p.Link(exec.LinkGood)
			return exec.ExitCode(0), nil
		}, linkHealth))
		assertEnd(t, awaitDone(t, rig.mustOpen("s1")).End, exited(0, byProcess, new(false)))
	})
	t.Run("a target that reports no status", func(t *testing.T) {
		rig := newEngineRig(t, engineRigConfig{}, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
			return exec.ExitCode(0), nil
		}, func(i *exec.TargetInfo) { i.Capabilities.ExitReport = exec.ExitReportNone }))
		assertEnd(t, awaitDone(t, rig.mustOpen("s1")).End, exited(0, byWire, nil))
	})
	t.Run("a signal is the same", func(t *testing.T) {
		rig := newEngineRig(t, engineRigConfig{}, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
			p.Link(exec.LinkStalled)
			return exec.ExitSignal("SIGTERM", 15), nil
		}, linkHealth))
		assertEnd(t, awaitDone(t, rig.mustOpen("s1")).End, signaled("SIGTERM", 15, byWire, nil))
	})
	t.Run("the SDK ended the session", func(t *testing.T) {
		rig := newEngineRig(t, engineRigConfig{}, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			p.Link(exec.LinkStalled)
			<-ctx.Done()
			return exec.ExitCode(0), nil
		}, linkHealth))
		cmd := rig.mustOpen("s1")
		eventually(t, "the link state", func() bool { _, ok := cmd.Link(); return ok })
		cmd.Close(false)
		assertEnd(t, awaitDone(t, cmd).End, closedBy(execv2.ClosedBy_CLOSED_BY_REQUEST, new(false)))
	})
}

func TestTheLinkStateTheFunctionReportsIsKeptAndSignalledOnAChangeOnly(t *testing.T) {
	step, done := exectest.NewGate(), exectest.NewGate()
	reports := []exec.LinkState{exec.LinkGood, exec.LinkGood, exec.LinkDegraded, exec.LinkDegraded, exec.LinkStalled}
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		for _, state := range reports {
			p.Link(state)
			select {
			case <-step.Opened():
			case <-ctx.Done():
			}
		}
		<-done.Opened()
		return exec.ExitCode(0), nil
	}, func(i *exec.TargetInfo) { i.Capabilities.LinkHealth = true }))
	cmd := rig.mustOpen("s1")
	eventually(t, "the first state", func() bool { s, ok := cmd.Link(); return ok && s == execv2.LinkState_LINK_STATE_GOOD })
	<-cmd.LinkChanged()
	step.Open()
	eventually(t, "the last state", func() bool { s, ok := cmd.Link(); return ok && s == execv2.LinkState_LINK_STATE_STALLED })
	// Good, good, degraded, degraded, stalled is three changes: one value is waiting for the consumer, not five.
	<-cmd.LinkChanged()
	select {
	case <-cmd.LinkChanged():
		t.Error("a second change signal is waiting: the signal is one slot, coalesced")
	default:
	}
	done.Open()
	awaitDone(t, cmd)
}

func TestALinkReportFromATargetThatDidNotDeclareItOrWithNoStateIsRefusedAndSaidOnce(t *testing.T) {
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		p.Link(exec.LinkGood)
		p.Link(exec.LinkStalled)
		return exec.ExitCode(0), nil
	}))
	cmd := rig.mustOpen("s1")
	res := awaitDone(t, cmd)
	if _, ok := cmd.Link(); ok || !hasViolation(res.Violations, "link_undeclared") {
		t.Errorf("link kept %v, violations %v: a target that declared no link health has none", ok, res.Violations)
	}
	rig = newEngineRig(t, engineRigConfig{}, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		p.Link(exec.LinkUnspecified)
		p.Link(exec.LinkState(99))
		return exec.ExitCode(0), nil
	}, func(i *exec.TargetInfo) { i.Capabilities.LinkHealth = true }))
	cmd = rig.mustOpen("s1")
	res = awaitDone(t, cmd)
	if _, ok := cmd.Link(); ok || !hasViolation(res.Violations, "link_invalid") {
		t.Errorf("link kept %v, violations %v: a state that is none of the three is not kept", ok, res.Violations)
	}
}

// ---------------------------------------------------------------------------------------------------------------
// Started: at most once, before the first output, only what the wire carries
// ---------------------------------------------------------------------------------------------------------------

func TestTheFactsTheFunctionReportsInStartedAreBoundedBeforeTheHostSeesThem(t *testing.T) {
	granted := true
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		p.Started(exec.SessionFacts{
			Program:   exec.Program{Argv: []string{"bash", "-l"}, Name: "bash", Path: "/bin/bash"},
			PID:       4242,
			TTY:       &granted,
			Size:      exec.Size{Cols: 90, Rows: 30},
			Term:      "xterm-256color",
			Transport: "k8s-ws-v5",
		})
		return exec.ExitCode(0), nil
	}))
	cmd := rig.mustOpen("s1")
	awaitDone(t, cmd)
	facts, ok := cmd.Started()
	if !ok {
		t.Fatal("a function that reported Started has none")
	}
	if !slices.Equal(facts.Argv, []string{"bash", "-l"}) || facts.Name != "bash" || facts.Path != "/bin/bash" || facts.PID != 4242 ||
		facts.TTY == nil || !*facts.TTY || facts.Cols != 90 || facts.Rows != 30 || facts.Term != "xterm-256color" || facts.Transport != "k8s-ws-v5" {
		t.Errorf("facts %+v, want what the function reported", facts)
	}
	if facts.Capabilities == nil {
		t.Error("the Started the host receives carries the session's capabilities")
	}
	if cmd.Result().StartObserved != true {
		t.Error("a reported Started is an observed start")
	}

	// What the wire cannot carry is dropped, never cut, and said in the log.
	rig = newEngineRig(t, engineRigConfig{}, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		p.Started(exec.SessionFacts{
			Program:   exec.Program{Argv: []string{"bash\x00"}, Name: strings.Repeat("n", 300), Path: "/bin/\x07bash"},
			Size:      exec.Size{Cols: 5000, Rows: 30}, // above the target's limit of 1024
			Term:      strings.Repeat("t", 65),
			Transport: "Not A Closed Code",
		})
		return exec.ExitCode(0), nil
	}))
	cmd = rig.mustOpen("s1")
	res := awaitDone(t, cmd)
	facts, _ = cmd.Started()
	if len(facts.Argv) != 0 || facts.Name != "" || facts.Path != "" || facts.Cols != 0 || facts.Rows != 0 || facts.Term != "" || facts.Transport != "" {
		t.Errorf("facts %+v: every fact that broke its bound is dropped", facts)
	}
	if !hasViolation(res.Violations, "started_facts") {
		t.Errorf("violations %v, want started_facts", res.Violations)
	}
}

func TestStartedAfterTheFirstOutputOrASecondTimeIsRefusedAndTheHostGetsOne(t *testing.T) {
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		p.Started(exec.SessionFacts{PID: 1})
		p.Started(exec.SessionFacts{PID: 2}) // a second one
		_, _ = p.Stdout.Write([]byte("x"))
		p.Started(exec.SessionFacts{PID: 3}) // after the first output
		return exec.ExitCode(0), nil
	}))
	cmd := rig.mustOpen("s1")
	res := awaitDone(t, cmd)
	facts, _ := cmd.Started()
	if facts.PID != 1 {
		t.Errorf("the host's Started has pid %d, want the first report's", facts.PID)
	}
	if !hasViolation(res.Violations, "started_late") {
		t.Errorf("violations %v, want started_late", res.Violations)
	}
}

// A function that writes before it reports gets the Started the SDK makes, and the host never sees output ahead of it.
func TestTheFirstOutputMakesTheStartedAndItIsRecordedBeforeTheOutputCanBeRead(t *testing.T) {
	for range 200 {
		rig := newEngineRig(t, engineRigConfig{}, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
			_, _ = p.Stdout.Write([]byte("hello"))
			return exec.ExitCode(0), nil
		}))
		cmd := rig.mustOpen("s1")
		buf := make([]byte, 16)
		chunk, err := cmd.Read(context.Background(), buf)
		if err != nil || chunk.N != 5 {
			t.Fatalf("read: %+v, %v", chunk, err)
		}
		if _, ok := cmd.Started(); !ok {
			t.Fatal("output was read before the Started that precedes it was recorded")
		}
		facts, _ := cmd.Started()
		if !slices.Equal(facts.Argv, []string{"/bin/sh"}) || facts.Capabilities == nil {
			t.Fatalf("the SDK's Started %+v lacks the requested program or the capabilities", facts)
		}
		awaitDone(t, cmd)
	}
}

func TestAStartedThatWidensTheCapabilitiesIsClampedAndSaid(t *testing.T) {
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		wide := p.Capabilities
		wide.Kill = exec.KillTerminates
		wide.Stall = exec.StallPropagates
		wide.Signals = append(wide.Signals, exec.SignalHup)
		p.Started(exec.SessionFacts{Capabilities: &wide})
		return exec.ExitCode(0), nil
	}, func(i *exec.TargetInfo) {
		i.Capabilities.Kill = exec.KillBestEffort
		i.Capabilities.Stall = exec.StallLossy
	}))
	cmd := rig.mustOpen("s1")
	res := awaitDone(t, cmd)
	facts, _ := cmd.Started()
	if facts.Capabilities.GetKill() != execv2.KillBehavior_KILL_BEHAVIOR_BEST_EFFORT || facts.Capabilities.GetStall() != execv2.StallBehavior_STALL_BEHAVIOR_LOSSY {
		t.Errorf("the Started claims %v and %v: a session never exceeds what it was opened with", facts.Capabilities.GetKill(), facts.Capabilities.GetStall())
	}
	if !hasViolation(res.Violations, "capabilities_wide") {
		t.Errorf("violations %v, want capabilities_wide", res.Violations)
	}
}

// ---------------------------------------------------------------------------------------------------------------
// errors: classes and facts cross, the author's text never does (the canary, conformance case 13 at the engine level)
// ---------------------------------------------------------------------------------------------------------------

func TestACanaryInAnErrorNeverLeavesTheEngineInTheEndTheResultOrTheMeasurements(t *testing.T) {
	canary := "CANARY-local-cause-3c9a"
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		_, _ = p.Stdout.Write([]byte("out"))
		return exec.Exit{}, fmt.Errorf("wrapped: %w", &exec.Error{
			Class: exec.ErrorClassForbidden, Reason: exec.ErrorReasonTargetDenied, Message: "you may not exec in this pod",
			Err: errors.New(canary), Facts: exec.ErrorFacts{SelectionName: "container", SelectionValue: "app"},
		})
	}))
	cmd := rig.mustOpen("s1")
	res := awaitDone(t, cmd)

	// The class, reason and facts cross, with the public message the contract lets through; the local cause does not.
	failedEnd := res.End.GetFailed()
	if failedEnd.GetClass() != execv2.ErrorClass_ERROR_CLASS_FORBIDDEN || failedEnd.GetReason() != execv2.ErrorReason_ERROR_REASON_TARGET_DENIED ||
		failedEnd.GetPublicMessage() != "you may not exec in this pod" || failedEnd.GetFacts().GetSelectionName() != "container" {
		t.Errorf("the failed end %s", prototext.Format(res.End))
	}
	crossing := prototext.Format(res.End) + strings.Join(res.Violations, "\n") + res.Cause
	if engContainsCanary(canary, crossing) {
		t.Fatal("the local cause of an error reached what crosses to the host")
	}
	if wire, err := proto.Marshal(res.End); err != nil || bytes.Contains(wire, []byte(canary)) {
		t.Fatalf("the canary is in the wire form of the end: %v", err)
	}
	// The measurements go to a backend: never an error text.
	for _, metric := range snapshotMetrics(rig) {
		if engContainsCanary(canary, metric) {
			t.Errorf("the canary is in the diagnostics: %s", metric)
		}
	}
	// It is in the plugin's own log, which is where a local cause belongs.
	local := false
	for _, record := range rig.logs.Records() {
		for _, field := range record.Fields {
			if s, ok := field.Value.(string); ok && strings.Contains(s, canary) {
				local = true
			}
		}
	}
	if !local {
		t.Error("the local cause is nowhere in the plugin's log: an operator could not find why it failed")
	}
}

func TestAClassifyThatPanicsDoesNotTakeThePluginDownAndTheErrorIsClassifiedByTheSDK(t *testing.T) {
	target := engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) { return exec.Exit{}, context.DeadlineExceeded })
	target.Classify = func(error) (*exec.Error, bool) { panic("classify") }
	rig := newEngineRig(t, engineRigConfig{}, target)
	res := awaitDone(t, rig.mustOpen("s1"))
	assertEnd(t, res.End, failed(execv2.ErrorClass_ERROR_CLASS_TIMEOUT, execv2.ErrorReason_ERROR_REASON_UNSPECIFIED, atStart, nil))
	if !hasViolation(res.Violations, "classify_panic") {
		t.Errorf("violations %v, want classify_panic", res.Violations)
	}
}

func TestAnErrorTheFunctionReturnsWithAnExitIsAFailureNeverAnExit(t *testing.T) {
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
		return exec.ExitCode(0), errors.New("the transport broke after the status")
	}))
	res := awaitDone(t, rig.mustOpen("s1"))
	assertEnd(t, res.End, failed(execv2.ErrorClass_ERROR_CLASS_INTERNAL, execv2.ErrorReason_ERROR_REASON_UNSPECIFIED, atStart, nil))
	if !hasViolation(res.Violations, "exit_and_error") {
		t.Errorf("violations %v, want exit_and_error", res.Violations)
	}
}

// The Classify the target registered is applied to what the function returns, and its answer is what crosses.
func TestTheTargetsClassifyMapsItsOwnErrorsAndTheSDKTheRest(t *testing.T) {
	target := engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
		return exec.Exit{}, errors.New("executable file not found in $PATH: \"nosuch\"")
	})
	target.Classify = func(err error) (*exec.Error, bool) {
		if strings.Contains(err.Error(), "not found in $PATH") {
			return &exec.Error{Class: exec.ErrorClassNotFound, Reason: exec.ErrorReasonNoSuchExecutable, Facts: exec.ErrorFacts{Program: "nosuch"}, Err: err}, true
		}
		return nil, false
	}
	rig := newEngineRig(t, engineRigConfig{}, target)
	res := awaitDone(t, rig.mustOpen("s1"))
	assertEnd(t, res.End, failed(execv2.ErrorClass_ERROR_CLASS_NOT_FOUND, execv2.ErrorReason_ERROR_REASON_NO_SUCH_EXECUTABLE, atStart, &execv2.ErrorFacts{Program: "nosuch"}))
}

// The function's own logs are not the engine's: the engine's lines carry the session, the target and the kind of command,
// and never an argument, an environment value or a resource name.
func TestTheEnginesLogLinesCarryTheSessionAndTheTargetAndNoArgumentsOrEnvironment(t *testing.T) {
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
		return exec.ExitCode(0), nil
	}))
	secret := "CANARY-argv-secret-8e22"
	cmd := rig.mustOpen("s-log", func(s *execv2.Start) {
		s.Argv = []string{"tool", "--token=" + secret}
		s.Env = []*execv2.NameValue{{Name: "PASSWORD", Value: secret}}
		s.Tty = false
	})
	awaitDone(t, cmd)
	eventually(t, "the end to be logged", func() bool { return len(logged(rig, "exec command ended")) == 1 })
	started := logged(rig, "exec command started")
	if len(started) != 1 {
		t.Fatalf("%d start lines, want 1", len(started))
	}
	for _, line := range started {
		if line["session"] != "s-log" || line["provider"] != "fake" || line["type"] != "thing" || line["kind"] != "session" {
			t.Errorf("the start line %v lacks the session, the provider, the type or the kind", line)
		}
	}
	for _, record := range rig.logs.Records() {
		for _, field := range record.Fields {
			if s, ok := field.Value.(string); ok && strings.Contains(s, secret) {
				t.Errorf("the log line %q carries an argument or an environment value: %v", record.Message, field)
			}
		}
	}
}
