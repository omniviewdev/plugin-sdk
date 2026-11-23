package exec_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

const (
	byProcess = execv2.ExitSource_EXIT_SOURCE_PROCESS
	byFolded  = execv2.ExitSource_EXIT_SOURCE_SIGNAL_FOLDED
	byWire    = execv2.ExitSource_EXIT_SOURCE_TRANSPORT

	atStart  = execv2.FailureStage_FAILURE_STAGE_START
	atRun    = execv2.FailureStage_FAILURE_STAGE_RUN
	atStream = execv2.FailureStage_FAILURE_STAGE_STREAM

	complete    = execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE
	truncated   = execv2.OutputCompleteness_OUTPUT_COMPLETENESS_TRUNCATED
	unspecified = execv2.OutputCompleteness_OUTPUT_COMPLETENESS_UNSPECIFIED
)

// ---------------------------------------------------------------------------------------------------------------
// what a function returns becomes the one typed end (conformance case 9 and 28 at the engine level)
// ---------------------------------------------------------------------------------------------------------------

func TestWhatTheFunctionReturnsBecomesTheTypedEndOfItsSession(t *testing.T) {
	writeThen := func(out string, exit exec.Exit, err error) exec.ExecFunc {
		return func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
			if out != "" {
				_, _ = p.Stdout.Write([]byte(out))
			}
			return exit, err
		}
	}
	for _, tc := range []struct {
		name     string
		fn       exec.ExecFunc
		want     *execv2.End
		started  bool
		output   execv2.OutputCompleteness
		bytesOut uint64
	}{
		{"exit 0", writeThen("", exec.ExitCode(0), nil), exited(0, byProcess, new(false)), true, complete, 0},
		{"exit n with output", writeThen("hello", exec.ExitCode(3), nil), exited(3, byProcess, new(false)), true, complete, 5},
		{"a signal", writeThen("", exec.ExitSignal("SIGKILL", 9), nil), signaled("SIGKILL", 9, byProcess, new(false)), true, complete, 0},
		{"a code the target folded from a signal", writeThen("", exec.ExitFolded(137), nil), exited(137, byFolded, new(false)), true, complete, 0},
		{"a code only the transport relayed", writeThen("", exec.ExitTransport(0), nil), exited(0, byWire, nil), true, unspecified, 0},
		{"a stream that ended with no status", writeThen("", exec.ExitLost(exec.LostNoStatus), nil), lost(execv2.LostCause_LOST_CAUSE_NO_STATUS), false, unspecified, 0},
		{"a link that died", writeThen("tail", exec.ExitLost(exec.LostLinkDead), nil), lost(execv2.LostCause_LOST_CAUSE_LINK_DEAD), true, unspecified, 4},
		{"a failure at start", writeThen("", exec.Exit{}, errors.New("cannot reach the target")),
			failed(execv2.ErrorClass_ERROR_CLASS_INTERNAL, execv2.ErrorReason_ERROR_REASON_UNSPECIFIED, atStart, nil), false, unspecified, 0},
		{"a typed failure with its facts", writeThen("", exec.Exit{}, &exec.Error{
			Class: exec.ClassNotFound, Reason: exec.ReasonNoSuchExecutable, Facts: exec.ErrorFacts{Program: "bash"},
		}), failed(execv2.ErrorClass_ERROR_CLASS_NOT_FOUND, execv2.ErrorReason_ERROR_REASON_NO_SUCH_EXECUTABLE, atStart,
			&execv2.ErrorFacts{Program: "bash"}), false, unspecified, 0},
		{"a failure after the start was observed", writeThen("x", exec.Exit{}, errors.New("boom")),
			failed(execv2.ErrorClass_ERROR_CLASS_INTERNAL, execv2.ErrorReason_ERROR_REASON_UNSPECIFIED, atRun, nil), true, unspecified, 1},
		{"a connection that failed after the start", writeThen("x", exec.Exit{}, &exec.Error{Class: exec.ClassConnection}),
			failed(execv2.ErrorClass_ERROR_CLASS_CONNECTION, execv2.ErrorReason_ERROR_REASON_UNSPECIFIED, atStream, nil), true, unspecified, 1},
		{"a status the SDK classifies: a gRPC permission error", writeThen("", exec.Exit{}, status.Error(codes.PermissionDenied, "no")),
			failed(execv2.ErrorClass_ERROR_CLASS_FORBIDDEN, execv2.ErrorReason_ERROR_REASON_UNSPECIFIED, atStart, nil), false, unspecified, 0},
		{"an error the SDK classifies: a network timeout", writeThen("", exec.Exit{}, &net.DNSError{IsTimeout: true}),
			failed(execv2.ErrorClass_ERROR_CLASS_TIMEOUT, execv2.ErrorReason_ERROR_REASON_UNSPECIFIED, atStart, nil), false, unspecified, 0},
		{"an empty Exit is never an exit 0", writeThen("", exec.Exit{}, nil),
			failed(execv2.ErrorClass_ERROR_CLASS_INTERNAL, execv2.ErrorReason_ERROR_REASON_UNSPECIFIED, atRun, nil), false, unspecified, 0},
		{"a panic", func(context.Context, *exec.Proc) (exec.Exit, error) { panic("CANARY-panic") },
			failed(execv2.ErrorClass_ERROR_CLASS_INTERNAL, execv2.ErrorReason_ERROR_REASON_UNSPECIFIED, atStart, nil), false, unspecified, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newEngineRig(t, engineRigConfig{}, engTarget(tc.fn))
			cmd := rig.mustOpen("s1")
			res := awaitDone(t, cmd)
			assertEnd(t, res.End, tc.want)
			if res.Cause != "finished" || res.StartObserved != tc.started || res.Output != tc.output || res.BytesOut != tc.bytesOut {
				t.Errorf("cause %q started %v output %v bytes out %d, want finished, %v, %v, %d",
					res.Cause, res.StartObserved, res.Output, res.BytesOut, tc.started, tc.output, tc.bytesOut)
			}
			if res.DroppedBytes != 0 || res.LateWrites != 0 || res.Abandoned {
				t.Errorf("dropped %d late %d abandoned %v: a function that behaved lost nothing", res.DroppedBytes, res.LateWrites, res.Abandoned)
			}
			// Nothing follows the end: the output closes, and reading it gives io.EOF.
			drain(t, cmd)
		})
	}
}

// Whatever Kill and ExitReport a target declares, an end that the SDK made says whether the process may survive by
// the Kill declaration, and only a terminating target says it is gone (conformance case 28).
func TestAClosedEndSaysTheProcessIsGoneOnlyWhenTheTargetTerminatesIt(t *testing.T) {
	for _, tc := range []struct {
		kill exec.KillBehavior
		want *bool
	}{
		{exec.KillTerminates, new(false)},
		{exec.KillBestEffort, new(true)},
		{exec.KillClosesStreamOnly, new(true)},
	} {
		t.Run(tc.kill.String(), func(t *testing.T) {
			rig := newEngineRig(t, engineRigConfig{}, engTarget(blockUntilCancelled, func(i *exec.TargetInfo) { i.Capabilities.Kill = tc.kill }))
			cmd := rig.mustOpen("s1")
			cmd.Close(false)
			assertEnd(t, awaitDone(t, cmd).End, closedBy(execv2.ClosedBy_CLOSED_BY_REQUEST, tc.want))
		})
	}
}

// A target whose kind reports no status cannot relay a process's own: the code it returns is the transport's word, the
// survival is unknown, and the author is told in the log (an exit 0 from a connection the target cannot vouch for is not
// the process's exit 0).
func TestAProcessStatusFromATargetThatReportsNoneIsTheTransportsWord(t *testing.T) {
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
		return exec.ExitCode(0), nil
	}, func(i *exec.TargetInfo) { i.Capabilities.ExitReport = exec.ExitReportNone }))
	cmd := rig.mustOpen("s1")
	res := awaitDone(t, cmd)
	assertEnd(t, res.End, exited(0, byWire, nil))
	if res.Output != unspecified {
		t.Errorf("output %v: a status that is not the process's is no proof of complete output", res.Output)
	}
	if len(res.Violations) != 1 {
		t.Errorf("violations %v, want the one that says a status was claimed from a target that reports none", res.Violations)
	}
}

// ---------------------------------------------------------------------------------------------------------------
// the SDK ends the session: Closed, whoever asked, and the function's return is the aftermath
// ---------------------------------------------------------------------------------------------------------------

func TestTheAftermathOfACancelIsNeverTheCommandsStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   exec.ExecFunc
	}{
		{"an exit 0", func(ctx context.Context, _ *exec.Proc) (exec.Exit, error) { <-ctx.Done(); return exec.ExitCode(0), nil }},
		{"a code", func(ctx context.Context, _ *exec.Proc) (exec.Exit, error) {
			<-ctx.Done()
			return exec.ExitCode(137), nil
		}},
		{"a status and an error", func(ctx context.Context, _ *exec.Proc) (exec.Exit, error) {
			<-ctx.Done()
			return exec.ExitCode(0), ctx.Err()
		}},
		{"the context's error", blockUntilCancelled},
		{"a lost stream", func(ctx context.Context, _ *exec.Proc) (exec.Exit, error) {
			<-ctx.Done()
			return exec.ExitLost(exec.LostNoStatus), nil
		}},
		{"a panic", func(ctx context.Context, _ *exec.Proc) (exec.Exit, error) { <-ctx.Done(); panic("after the cancel") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newEngineRig(t, engineRigConfig{}, engTarget(tc.fn))
			cmd := rig.mustOpen("s1")
			cmd.Close(false)
			res := awaitDone(t, cmd)
			assertEnd(t, res.End, closedBy(execv2.ClosedBy_CLOSED_BY_REQUEST, new(false)))
			if res.Cause != "requested" || res.Abandoned {
				t.Errorf("cause %q abandoned %v", res.Cause, res.Abandoned)
			}
		})
	}
}

func TestShuttingTheEngineDownEndsEveryCommandWithTheShutdownCause(t *testing.T) {
	rig := newEngineRig(t, engineRigConfig{}, engTarget(blockUntilCancelled))
	var cmds []exechook.Command
	for i := range 5 {
		cmds = append(cmds, rig.mustOpen(fmt.Sprintf("s%d", i)))
	}
	if err := rig.eng.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	for _, cmd := range cmds {
		res := awaitDone(t, cmd)
		if res.Cause != "shutdown" {
			t.Errorf("command %s ended with cause %q, want shutdown", cmd.ID(), res.Cause)
		}
		assertEnd(t, res.End, closedBy(execv2.ClosedBy_CLOSED_BY_PLUGIN_SHUTDOWN, new(false)))
	}
}

// ---------------------------------------------------------------------------------------------------------------
// the connection lease: held for the life of the command, so a retired connection ends it (conformance case 14)
// ---------------------------------------------------------------------------------------------------------------

func TestARetiredConnectionEndsItsSessionsWithTheRetiredCause(t *testing.T) {
	rig := newEngineRig(t, engineRigConfig{}, engTarget(blockUntilCancelled))
	a, b := rig.mustOpen("a"), rig.mustOpen("b")
	rig.conns.Retire()
	for _, cmd := range []exechook.Command{a, b} {
		res := awaitDone(t, cmd)
		if res.Cause != "retired" {
			t.Errorf("session %s ended with cause %q, want retired", cmd.ID(), res.Cause)
		}
		assertEnd(t, res.End, closedBy(execv2.ClosedBy_CLOSED_BY_CONNECTION_RETIRED, new(false)))
	}
	if _, err := rig.open("c"); err == nil {
		t.Fatal("a session was opened on a retired connection")
	}
}

// The retirement ends a session whose function ignores its cancelled context too: the end is typed at the retirement, the
// function is given up on after the grace, and the retirement is not held for ever by it.
func TestARetiredConnectionEndsASessionWhoseFunctionIgnoresItsContextAfterTheGrace(t *testing.T) {
	stuck := exectest.NewGate()
	rig := newEngineRig(t, engineRigConfig{grace: 20 * time.Millisecond}, exectest.NewFakeTarget(exectest.FakeScript(exectest.IgnoreCancel(stuck))))
	t.Cleanup(stuck.Open)
	cmd := rig.mustOpen("s1")
	rig.conns.Retire()
	res := awaitDone(t, cmd)
	if res.Cause != "retired" || !res.Abandoned {
		t.Errorf("cause %q, abandoned %v, want retired and the function given up on", res.Cause, res.Abandoned)
	}
	assertEnd(t, res.End, closedBy(execv2.ClosedBy_CLOSED_BY_CONNECTION_RETIRED, new(false)))
}

// The lease is held for as long as the command runs: the connection's retirement waits for the function to return, as it
// waits for every other holder of a lease, and the command ends only when the function has.
func TestTheConnectionsRetirementWaitsForTheFunctionToReturn(t *testing.T) {
	cancelled, release := exectest.NewGate(), exectest.NewGate()
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(ctx context.Context, _ *exec.Proc) (exec.Exit, error) {
		<-ctx.Done()
		cancelled.Open()
		<-release.Opened() // the function takes its time to tear down
		return exec.Exit{}, ctx.Err()
	}))
	cmd := rig.mustOpen("s1")
	retired := make(chan struct{})
	go func() {
		defer close(retired)
		rig.conns.Retire()
	}()
	awaitChannel(t, cancelled.Opened(), "the function to see its context end")
	select {
	case <-retired:
		t.Fatal("the connection was retired while a command still held its lease")
	case <-cmd.Done():
		t.Fatal("the command ended before its function returned")
	default:
	}
	release.Open()
	awaitChannel(t, retired, "the retirement to finish")
	res := awaitDone(t, cmd)
	assertEnd(t, res.End, closedBy(execv2.ClosedBy_CLOSED_BY_CONNECTION_RETIRED, new(false)))
}

// A function that returns the moment its context ends, with that context's error, races the retirement that ended it. The
// end is Closed with the retired cause every time: the error a function returns because it was cancelled is no failure,
// and the SDK decides who ended the session first by the cancel that came first.
func TestAFunctionThatReturnsBecauseTheConnectionWasRetiredEndsClosedNeverFailed(t *testing.T) {
	for i := range 150 {
		rig := newEngineRig(t, engineRigConfig{}, engTarget(blockUntilCancelled))
		cmd := rig.mustOpen("s1")
		rig.conns.Retire()
		res := awaitDone(t, cmd)
		if res.Cause != "retired" {
			t.Fatalf("round %d: cause %q, want retired", i, res.Cause)
		}
		assertEnd(t, res.End, closedBy(execv2.ClosedBy_CLOSED_BY_CONNECTION_RETIRED, new(false)))
		if t.Failed() {
			return
		}
	}
}

func TestAStartOnARetiredConnectionIsRefusedByTheResourceLayer(t *testing.T) {
	rig := newEngineRig(t, engineRigConfig{}, engTarget(blockUntilCancelled))
	rig.conns.Retire()
	_, err := rig.open("s1")
	if err == nil {
		t.Fatal("a session was opened on a retired connection")
	}
	if live := rig.eng.Live(); len(live) != 0 {
		t.Fatalf("a refused start left %v", live)
	}
}

// A function that ends by itself while the connection is retired at the same moment ends the session once, with whichever
// end came first, and never both.
func TestARetirementRacingTheFunctionsOwnExitEndsTheSessionOnce(t *testing.T) {
	for range 100 {
		rig := newEngineRig(t, engineRigConfig{}, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
			return exec.ExitCode(0), nil
		}))
		cmd := rig.mustOpen("s1")
		rig.conns.Retire()
		res := awaitDone(t, cmd)
		switch res.Cause {
		case "finished":
			assertEnd(t, res.End, exited(0, byProcess, new(false)))
		case "retired":
			assertEnd(t, res.End, closedBy(execv2.ClosedBy_CLOSED_BY_CONNECTION_RETIRED, new(false)))
		default:
			t.Fatalf("cause %q", res.Cause)
		}
		if t.Failed() {
			return
		}
	}
}

// ---------------------------------------------------------------------------------------------------------------
// the same operation against itself (pair P5): the final end is the same however it is asked for
// ---------------------------------------------------------------------------------------------------------------

func TestEndingTwiceAfterTheExitAndDuringTheStartGiveTheSameFinalEnd(t *testing.T) {
	t.Run("twice", func(t *testing.T) {
		rig := newEngineRig(t, engineRigConfig{}, engTarget(blockUntilCancelled))
		once := rig.mustOpen("once")
		once.Close(false)
		want := awaitDone(t, once)
		for _, kills := range [][]bool{{false, false}, {true, true}, {false, true}, {true, false}} {
			cmd := rig.mustOpen(fmt.Sprintf("twice-%v-%v", kills[0], kills[1]))
			cmd.Close(kills[0])
			cmd.Close(kills[1])
			got := awaitDone(t, cmd)
			assertEnd(t, got.End, want.End)
			if got.Cause != want.Cause || got.StartObserved != want.StartObserved || got.BytesOut != want.BytesOut {
				t.Errorf("ending twice (%v) gave %+v, want the same as ending once: %+v", kills, got, want)
			}
		}
	})
	t.Run("after the exit", func(t *testing.T) {
		rig := newEngineRig(t, engineRigConfig{}, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
			_, _ = p.Stdout.Write([]byte("done"))
			return exec.ExitCode(7), nil
		}))
		cmd := rig.mustOpen("s1")
		before := awaitDone(t, cmd)
		cmd.Close(false)
		cmd.Close(true)
		after := cmd.Result()
		assertEnd(t, after.End, exited(7, byProcess, new(false)))
		if !proto.Equal(before.End, after.End) || before.Cause != after.Cause || before.BytesOut != after.BytesOut {
			t.Errorf("ending a command after its exit changed its end: %+v then %+v", before, after)
		}
	})
	t.Run("during the start", func(t *testing.T) {
		for _, kills := range [][]bool{{false}, {true}, {false, false}, {true, true}, {false, true}} {
			started, release := exectest.NewGate(), exectest.NewGate()
			rig := newEngineRig(t, engineRigConfig{}, engTarget(func(ctx context.Context, _ *exec.Proc) (exec.Exit, error) {
				started.Open() // the function has begun and has reported nothing and written nothing
				select {
				case <-ctx.Done():
				case <-release.Opened():
				}
				return exec.ExitCode(0), nil
			}))
			cmd := rig.mustOpen("s1")
			awaitChannel(t, started.Opened(), "the function to begin")
			for _, kill := range kills {
				cmd.Close(kill)
			}
			res := awaitDone(t, cmd)
			release.Open()
			assertEnd(t, res.End, closedBy(execv2.ClosedBy_CLOSED_BY_REQUEST, new(false)))
			if res.StartObserved || res.Cause != "requested" {
				t.Errorf("ended during the start (%v): started %v cause %q, want an unobserved start and cause requested", kills, res.StartObserved, res.Cause)
			}
		}
	})
}

// Ending a command and its function returning at the same moment is one end: the first of the two stands, and both are
// valid. Run many times, the end is always one of them and never a mix.
func TestACloseRacingTheFunctionsExitEndsTheSessionOnce(t *testing.T) {
	var closedFirst, exitedFirst atomic.Int32
	for range 300 {
		rig := newEngineRig(t, engineRigConfig{}, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
			return exec.ExitCode(5), nil
		}))
		cmd := rig.mustOpen("s1")
		cmd.Close(false)
		res := awaitDone(t, cmd)
		switch res.Cause {
		case "finished":
			exitedFirst.Add(1)
			assertEnd(t, res.End, exited(5, byProcess, new(false)))
		case "requested":
			closedFirst.Add(1)
			assertEnd(t, res.End, closedBy(execv2.ClosedBy_CLOSED_BY_REQUEST, new(false)))
		default:
			t.Fatalf("cause %q", res.Cause)
		}
		if t.Failed() {
			return
		}
	}
	t.Logf("the exit came first %d times, the close %d times", exitedFirst.Load(), closedFirst.Load())
}
