package exec_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The end of a command whose stream a client library ended, as a host reads it over the wire. A Kubernetes Success is "the
// transport reported a normal end", which the API server also sends for a command that did not exit 0 (after a kubelet or a
// containerd restart): it is the transport's word, never the process's own status, and what the process did is not known.

// kinds names the frames of a stream in order.
func kinds(frames []*execv2.ServerFrame) []string {
	out := make([]string, len(frames))
	for i, f := range frames {
		out[i] = frameKind(f)
	}
	return out
}

// streamEnds is the three ends of a stream that a client library ended, written as an author writes them with
// ExitOfStreamError: a normal end, a status, and a stream that broke.
func streamEnds(streamErr error, broke bool) exec.ExecFunc {
	return func(context.Context, *exec.Proc) (exec.Exit, error) {
		if broke {
			return exec.ExitLost(exec.LostNoStatus), nil
		}
		exit, ok := exec.ExitOfStreamError(streamErr)
		if !ok {
			return exec.Exit{}, streamErr
		}
		return exit, nil
	}
}

// A quiet command whose stream ended normally ends Exited{0, TRANSPORT}: its start was observed (a status was seen), nothing says
// the process is gone, nothing says the output is complete, and the host receives no Started frame, because the command never
// wrote and never reported (the proto's rule for Started). A status of the process, and a stream that broke, are told apart from it.
func TestAQuietCommandThatEndedNormallyIsExitedZeroFromTheTransportOverTheWire(t *testing.T) {
	for _, tc := range []struct {
		name         string
		fn           exec.ExecFunc
		wantExit     *execv2.Exited // nil: not an Exited end
		wantSurvive  *bool
		wantObserved bool
		wantOutput   execv2.OutputCompleteness
		wantLost     bool
	}{
		{
			name:         "the stream ended normally",
			fn:           streamEnds(nil, false),
			wantExit:     &execv2.Exited{ExitCode: 0, Source: execv2.ExitSource_EXIT_SOURCE_TRANSPORT},
			wantSurvive:  nil,
			wantObserved: true,
			wantOutput:   execv2.OutputCompleteness_OUTPUT_COMPLETENESS_UNSPECIFIED,
		},
		{
			name:         "the stream relayed a status",
			fn:           streamEnds(exectest.CodeExitError{Code: 3}, false),
			wantExit:     &execv2.Exited{ExitCode: 3, Source: execv2.ExitSource_EXIT_SOURCE_PROCESS},
			wantSurvive:  new(false),
			wantObserved: true,
			wantOutput:   execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE,
		},
		{
			name:         "the stream broke",
			fn:           streamEnds(nil, true),
			wantLost:     true,
			wantSurvive:  new(true),
			wantObserved: false,
			wantOutput:   execv2.OutputCompleteness_OUTPUT_COMPLETENESS_UNSPECIFIED,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newWireRig(t, engTarget(tc.fn))
			frames, ended := readUntilEnded(t, rig.mustOpen("s1", withoutTTY))

			if got, want := kinds(frames), []string{"ended"}; !slices.Equal(got, want) {
				t.Errorf("the frames after Opened are %v, want %v: a command that never wrote and never reported has no Started", got, want)
			}
			end := ended.GetEnd()
			switch {
			case tc.wantLost:
				if end.GetLost().GetCause() != execv2.LostCause_LOST_CAUSE_NO_STATUS {
					t.Errorf("the end is %v, want Lost{NO_STATUS}", end)
				}
			default:
				exited := end.GetExited()
				if exited == nil || exited.GetExitCode() != tc.wantExit.GetExitCode() || exited.GetSource() != tc.wantExit.GetSource() {
					t.Errorf("the end is %v, want Exited{%d, %v}", end, tc.wantExit.GetExitCode(), tc.wantExit.GetSource())
				}
			}
			if got := end.RemoteMaySurvive; (got == nil) != (tc.wantSurvive == nil) || (got != nil && *got != *tc.wantSurvive) {
				t.Errorf("remote_may_survive is %v, want %v (absent is unknown, which a consumer reads as may survive)", got, tc.wantSurvive)
			}
			if ended.GetStartObserved() != tc.wantObserved {
				t.Errorf("start_observed is %v, want %v", ended.GetStartObserved(), tc.wantObserved)
			}
			if ended.GetOutput() != tc.wantOutput {
				t.Errorf("output is %v, want %v", ended.GetOutput(), tc.wantOutput)
			}
		})
	}
}

// The same end of a bounded run: an agent's run of a command in a pod returns "ended normally", and says nothing of the process.
func TestARunWhoseStreamEndedNormallyIsExitedZeroFromTheTransportOverTheWire(t *testing.T) {
	rig := newRunSrvRig(t, engTarget(streamEnds(nil, false)))
	res := rig.mustRun(rig.request())
	if res.GetOutcome() != execv2.Outcome_OUTCOME_EXITED || res.ExitCode == nil || res.GetExitCode() != 0 ||
		res.GetExitSource() != execv2.ExitSource_EXIT_SOURCE_TRANSPORT {
		t.Errorf("the run ended %v with code %v from %v, want EXITED, 0, TRANSPORT", res.GetOutcome(), res.ExitCode, res.GetExitSource())
	}
	if res.RemoteMaySurvive != nil {
		t.Errorf("remote_may_survive is %v, want absent: only the transport said the stream ended", *res.RemoteMaySurvive)
	}
	if !res.GetStartObserved() {
		t.Error("start_observed is false: a status was seen")
	}
	if got := res.GetStdoutAccount().GetCompleteness(); got != execv2.OutputCompleteness_OUTPUT_COMPLETENESS_UNSPECIFIED {
		t.Errorf("stdout completeness is %v, want UNSPECIFIED: the transport's word is no proof that the output is complete", got)
	}
}

// A command that reports its start when the executor first reads the stdin it was given (client-go does that once its streams exist,
// which is the one observation of "the stream is up" that it offers) has a Started frame though it is quiet, and a host sees it
// before the command has done anything else.
func TestAQuietCommandThatReportsItsStartOnTheFirstReadOfItsStdinHasAStartedFrame(t *testing.T) {
	facts := exec.SessionFacts{Transport: "k8s-ws-v5"}
	rig := newWireRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		stdin := exec.OnFirstRead(p.Stdin, func() { p.Started(facts) }) // what the executor reads once its streams exist
		if _, err := stdin.Read(make([]byte, 1)); err != nil {
			return exec.Exit{}, err
		}
		exit, _ := exec.ExitOfStreamError(nil)
		return exit, nil
	}))
	conn := rig.mustOpen("s1", withoutTTY)

	// Opened came with Open; the Started arrives before the host has sent anything: the trigger is the read, not a byte.
	frame, err := recvFrame(t, conn)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if got := frameKind(frame); got != "started" {
		t.Fatalf("the first frame after Opened is %s, want started", got)
	}
	started := frame.GetStarted().GetFacts()
	if started.GetTransport() != "k8s-ws-v5" || !slices.Equal(started.GetProgram().GetArgv(), []string{"/bin/sh"}) {
		t.Errorf("Started carries transport %q and argv %v, want the reported transport and the program that was asked", started.GetTransport(), started.GetProgram().GetArgv())
	}

	sendInput(t, conn, "x")
	rest, ended := readUntilEnded(t, conn)
	if want := []string{"ended"}; !slices.Equal(kinds(rest), want) {
		t.Errorf("the frames after the input are %v, want %v: the one Started is not repeated", kinds(rest), want)
	}
	if exited := ended.GetEnd().GetExited(); exited.GetSource() != execv2.ExitSource_EXIT_SOURCE_TRANSPORT || !ended.GetStartObserved() {
		t.Errorf("Ended is %v with start_observed %v, want Exited from the transport with the start observed", ended.GetEnd(), ended.GetStartObserved())
	}
}

// Where a command calls Started decides the stage of a failure: a refusal that comes before the stream exists (a 403, a container
// that is not running) is a failure at START, and one that comes after the command reported its start is a failure at RUN, even
// for a command that never ran. The first read of the stdin is the honest trigger: it keeps a refusal of the upgrade a START
// failure and makes a failure of the command itself (the runtime cannot find the executable) a RUN failure.
func TestACommandThatReportsItsStartOnTheFirstReadFailsAtStartWhenTheStreamNeverExisted(t *testing.T) {
	refused := errors.New("the API refused the command")
	for _, tc := range []struct {
		name  string
		reads bool
		want  execv2.FailureStage
	}{
		{"the executor never read its stdin: the stream never existed", false, execv2.FailureStage_FAILURE_STAGE_START},
		{"the executor read its stdin: the stream existed", true, execv2.FailureStage_FAILURE_STAGE_RUN},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newWireRig(t, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
				stdin := exec.OnFirstRead(p.Stdin, func() { p.Started(exec.SessionFacts{}) })
				if tc.reads {
					if _, err := stdin.Read(make([]byte, 1)); err != nil {
						return exec.Exit{}, err
					}
				}
				return exec.Exit{}, refused
			}))
			conn := rig.mustOpen("s1", withoutTTY)
			if tc.reads {
				sendInput(t, conn, "x")
			}
			frames, ended := readUntilEnded(t, conn)
			if failed := ended.GetEnd().GetFailed(); failed == nil || failed.GetStage() != tc.want {
				t.Fatalf("the end is %v, want Failed at %v", ended.GetEnd(), tc.want)
			}
			if started := slices.Contains(kinds(frames), "started"); started != tc.reads {
				t.Errorf("the frames are %v: a Started is expected exactly when the executor read its stdin (%v)", kinds(frames), tc.reads)
			}
		})
	}
}
