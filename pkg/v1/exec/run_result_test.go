package exec

import (
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/internal/session"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// How a run ends is decided in one place: the End the engine concluded, and what the call read of the output, become one
// RunResult. This table gives every arm of an End, and every cause that ends a run, the outcome the contract says, and pins
// the account of each stream and its completeness.

const (
	outcomeExited    = execv2.Outcome_OUTCOME_EXITED
	outcomeSignaled  = execv2.Outcome_OUTCOME_SIGNALED
	outcomeTimedOut  = execv2.Outcome_OUTCOME_TIMED_OUT
	outcomeCancelled = execv2.Outcome_OUTCOME_CANCELLED
	outcomeLost      = execv2.Outcome_OUTCOME_LOST
	outcomeFailed    = execv2.Outcome_OUTCOME_FAILED
)

const (
	completeAll     = execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE
	completeTrunc   = execv2.OutputCompleteness_OUTPUT_COMPLETENESS_TRUNCATED
	completeUnknown = execv2.OutputCompleteness_OUTPUT_COMPLETENESS_UNSPECIFIED
)

func endExited(code int64, source execv2.ExitSource, survive *bool) *execv2.End {
	return &execv2.End{Kind: &execv2.End_Exited{Exited: &execv2.Exited{ExitCode: code, Source: source}}, RemoteMaySurvive: survive}
}

func endSignaled(name string, number int32, source execv2.ExitSource, survive *bool) *execv2.End {
	return &execv2.End{
		Kind:             &execv2.End_Signaled{Signaled: &execv2.Signaled{Signal: name, SignalNumber: number, Source: source}},
		RemoteMaySurvive: survive,
	}
}

func endClosed(by execv2.ClosedBy, survive *bool) *execv2.End {
	return &execv2.End{Kind: &execv2.End_Closed{Closed: &execv2.Closed{By: by}}, RemoteMaySurvive: survive}
}

func endFailed(f *execv2.Failed) *execv2.End { return &execv2.End{Kind: &execv2.End_Failed{Failed: f}} }

func endLost(cause execv2.LostCause, survive *bool) *execv2.End {
	return &execv2.End{Kind: &execv2.End_Lost{Lost: &execv2.Lost{Cause: cause}}, RemoteMaySurvive: survive}
}

// goodOutput is the account of a stream that was read to its end and nothing was dropped.
func goodOutput(data string, valid bool) runOutput {
	n := uint64(len(data))
	return runOutput{Data: []byte(data), Account: outputAccount{Total: n, Kept: n, Head: len(data), ValidUTF8: valid}}
}

// baseInput is the input of a run that read its output to the end: what the cases of the table change.
func baseInput(end *execv2.End, cause session.Cause, mods ...func(*runResultInput)) runResultInput {
	in := runResultInput{
		RequestID: "req-1",
		Result: commandResult{
			End: end, Cause: cause, StartObserved: true, Duration: 1500 * time.Millisecond, Output: completeAll,
		},
		Target:  &execv2.TargetFacts{Provider: "fake", Type: "thing", Id: "thing-1", DisplayName: "Fake"},
		Program: &execv2.Program{Argv: []string{"prog", "-x"}, Name: "prog"},
		Stdout:  goodOutput("out", true),
		Stderr:  goodOutput("err!", true),
		Drained: true,
	}
	for _, mod := range mods {
		mod(&in)
	}
	return in
}

// wantResult is the RunResult that baseInput gives when nothing but the outcome differs.
func wantResult(outcome execv2.Outcome, stdoutDone, stderrDone execv2.OutputCompleteness, mods ...func(*execv2.RunResult)) *execv2.RunResult {
	want := &execv2.RunResult{
		RequestId:     "req-1",
		Outcome:       outcome,
		Stdout:        []byte("out"),
		Stderr:        []byte("err!"),
		StdoutAccount: &execv2.OutputAccount{TotalBytes: 3, KeptBytes: 3, HeadBytes: 3, ValidUtf8: true, Completeness: stdoutDone},
		StderrAccount: &execv2.OutputAccount{TotalBytes: 4, KeptBytes: 4, HeadBytes: 4, ValidUtf8: true, Completeness: stderrDone},
		DurationMs:    1500,
		StartObserved: true,
		Target:        &execv2.TargetFacts{Provider: "fake", Type: "thing", Id: "thing-1", DisplayName: "Fake"},
		Program:       &execv2.Program{Argv: []string{"prog", "-x"}, Name: "prog"},
	}
	for _, mod := range mods {
		mod(want)
	}
	return want
}

func withCode(code int64, source execv2.ExitSource) func(*execv2.RunResult) {
	return func(r *execv2.RunResult) { r.ExitCode, r.ExitSource = &code, source }
}

func withSource(source execv2.ExitSource) func(*execv2.RunResult) {
	return func(r *execv2.RunResult) { r.ExitSource = source }
}

func withSignal(name string, number int32, source execv2.ExitSource) func(*execv2.RunResult) {
	return func(r *execv2.RunResult) { r.Signal, r.SignalNumber, r.ExitSource = name, number, source }
}

func withSurvive(survive *bool) func(*execv2.RunResult) {
	return func(r *execv2.RunResult) { r.RemoteMaySurvive = survive }
}

func withFailure(f *execv2.Failed) func(*execv2.RunResult) {
	return func(r *execv2.RunResult) { r.Failed = f }
}

func withoutFacts(r *execv2.RunResult) { r.StartObserved, r.Target, r.Program = false, nil, nil }

func TestEveryEndOfARunIsTheOutcomeTheContractSays(t *testing.T) {
	const (
		process = execv2.ExitSource_EXIT_SOURCE_PROCESS
		folded  = execv2.ExitSource_EXIT_SOURCE_SIGNAL_FOLDED
		wire    = execv2.ExitSource_EXIT_SOURCE_TRANSPORT
		host    = execv2.ExitSource_EXIT_SOURCE_HOST
	)
	retired := &execv2.Failed{
		Class: execv2.ErrorClass_ERROR_CLASS_CONNECTION, Reason: execv2.ErrorReason_ERROR_REASON_CONNECTION_RETIRED,
		Stage: execv2.FailureStage_FAILURE_STAGE_RUN,
	}
	unavailable := &execv2.Failed{Class: execv2.ErrorClass_ERROR_CLASS_UNAVAILABLE, Stage: execv2.FailureStage_FAILURE_STAGE_RUN}
	internal := &execv2.Failed{Class: execv2.ErrorClass_ERROR_CLASS_INTERNAL, Stage: execv2.FailureStage_FAILURE_STAGE_RUN}
	unobserved := func(r *runResultInput) { r.Result.StartObserved = false }
	engineSays := func(c execv2.OutputCompleteness) func(*runResultInput) {
		return func(r *runResultInput) { r.Result.Output = c }
	}

	for _, tc := range []struct {
		name string
		in   runResultInput
		want *execv2.RunResult
	}{
		{
			"exited 0, the process's own status",
			baseInput(endExited(0, process, new(false)), session.CauseFinished),
			wantResult(outcomeExited, completeAll, completeAll, withCode(0, process), withSurvive(new(false))),
		},
		{
			"exited with a code that a signal folded into",
			baseInput(endExited(137, folded, new(false)), session.CauseFinished),
			wantResult(outcomeExited, completeAll, completeAll, withCode(137, folded), withSurvive(new(false))),
		},
		{
			"exited by the transport's word: its survival stays unknown and completeness is not claimed",
			baseInput(endExited(5, wire, nil), session.CauseFinished, engineSays(completeUnknown)),
			wantResult(outcomeExited, completeUnknown, completeUnknown, withCode(5, wire)),
		},
		{
			"a negative 32-bit reading of a Windows status is carried exactly",
			baseInput(endExited(-1073741819, process, new(false)), session.CauseFinished),
			wantResult(outcomeExited, completeAll, completeAll, withCode(-1073741819, process), withSurvive(new(false))),
		},
		{
			"a DWORD status is carried exactly",
			baseInput(endExited(3221225477, process, new(false)), session.CauseFinished),
			wantResult(outcomeExited, completeAll, completeAll, withCode(3221225477, process), withSurvive(new(false))),
		},
		{
			"signaled",
			baseInput(endSignaled("SIGKILL", 9, process, new(false)), session.CauseFinished),
			wantResult(outcomeSignaled, completeAll, completeAll, withSignal("SIGKILL", 9, process), withSurvive(new(false))),
		},
		{
			"signaled by the transport's word",
			baseInput(endSignaled("SIGTERM", 15, wire, nil), session.CauseFinished, engineSays(completeUnknown)),
			wantResult(outcomeSignaled, completeUnknown, completeUnknown, withSignal("SIGTERM", 15, wire)),
		},
		{
			"signaled with a number alone",
			baseInput(endSignaled("", 9, process, new(false)), session.CauseFinished),
			wantResult(outcomeSignaled, completeAll, completeAll, withSignal("", 9, process), withSurvive(new(false))),
		},
		{
			"timed out: the code, if there were one, would be the aftermath of the SDK's kill",
			baseInput(endClosed(execv2.ClosedBy_CLOSED_BY_RUN_TIMEOUT, new(false)), session.CauseTimeout, engineSays(completeUnknown)),
			wantResult(outcomeTimedOut, completeUnknown, completeUnknown, withSource(host), withSurvive(new(false))),
		},
		{
			"timed out on a target that cannot promise the process stopped",
			baseInput(endClosed(execv2.ClosedBy_CLOSED_BY_RUN_TIMEOUT, new(true)), session.CauseTimeout, engineSays(completeUnknown)),
			wantResult(outcomeTimedOut, completeUnknown, completeUnknown, withSource(host), withSurvive(new(true))),
		},
		{
			"timed out on a target that did not say what ending does: survival unknown",
			baseInput(endClosed(execv2.ClosedBy_CLOSED_BY_RUN_TIMEOUT, nil), session.CauseTimeout, engineSays(completeUnknown)),
			wantResult(outcomeTimedOut, completeUnknown, completeUnknown, withSource(host)),
		},
		{
			"cancelled",
			baseInput(endClosed(execv2.ClosedBy_CLOSED_BY_CANCELLED, new(false)), session.CauseCancelled, engineSays(completeUnknown)),
			wantResult(outcomeCancelled, completeUnknown, completeUnknown, withSource(host), withSurvive(new(false))),
		},
		{
			"closed by a request is a cancel",
			baseInput(endClosed(execv2.ClosedBy_CLOSED_BY_REQUEST, new(true)), session.CauseRequested, engineSays(completeUnknown)),
			wantResult(outcomeCancelled, completeUnknown, completeUnknown, withSource(host), withSurvive(new(true))),
		},
		{
			"the plugin shut down: a failure of the plugin, after the start",
			baseInput(endClosed(execv2.ClosedBy_CLOSED_BY_PLUGIN_SHUTDOWN, new(false)), session.CauseShutdown, engineSays(completeUnknown)),
			wantResult(outcomeFailed, completeUnknown, completeUnknown, withFailure(unavailable), withSurvive(new(false))),
		},
		{
			"the plugin shut down before the start was observed: the failure is at the start, and no start is claimed",
			baseInput(endClosed(execv2.ClosedBy_CLOSED_BY_PLUGIN_SHUTDOWN, nil), session.CauseShutdown, engineSays(completeUnknown), unobserved),
			wantResult(outcomeFailed, completeUnknown, completeUnknown, withoutFacts,
				withFailure(&execv2.Failed{Class: execv2.ErrorClass_ERROR_CLASS_UNAVAILABLE, Stage: execv2.FailureStage_FAILURE_STAGE_START})),
		},
		{
			"the connection was retired",
			baseInput(endClosed(execv2.ClosedBy_CLOSED_BY_CONNECTION_RETIRED, new(true)), session.CauseRetired, engineSays(completeUnknown)),
			wantResult(outcomeFailed, completeUnknown, completeUnknown, withFailure(retired), withSurvive(new(true))),
		},
		{
			"a detach timeout is no end of a run: a failure of the SDK",
			baseInput(endClosed(execv2.ClosedBy_CLOSED_BY_DETACH_TIMEOUT, nil), session.CauseExpired, engineSays(completeUnknown)),
			wantResult(outcomeFailed, completeUnknown, completeUnknown, withFailure(internal)),
		},
		{
			"an idle timeout is no end of a run either",
			baseInput(endClosed(execv2.ClosedBy_CLOSED_BY_IDLE_TIMEOUT, nil), session.CauseExpired, engineSays(completeUnknown)),
			wantResult(outcomeFailed, completeUnknown, completeUnknown, withFailure(internal)),
		},
		{
			"closed by nobody the SDK knows",
			baseInput(endClosed(execv2.ClosedBy_CLOSED_BY_UNSPECIFIED, nil), session.CauseExpired, engineSays(completeUnknown)),
			wantResult(outcomeFailed, completeUnknown, completeUnknown, withFailure(internal)),
		},
		{
			"failed after the start: the failure is copied as the engine staged it",
			baseInput(endFailed(&execv2.Failed{
				Class: execv2.ErrorClass_ERROR_CLASS_FORBIDDEN, Reason: execv2.ErrorReason_ERROR_REASON_TARGET_DENIED,
				Facts: &execv2.ErrorFacts{Program: "sh", RetryAfterMs: 250}, Stage: execv2.FailureStage_FAILURE_STAGE_RUN, PublicMessage: "denied",
			}), session.CauseFinished, engineSays(completeUnknown)),
			wantResult(outcomeFailed, completeUnknown, completeUnknown, withFailure(&execv2.Failed{
				Class: execv2.ErrorClass_ERROR_CLASS_FORBIDDEN, Reason: execv2.ErrorReason_ERROR_REASON_TARGET_DENIED,
				Facts: &execv2.ErrorFacts{Program: "sh", RetryAfterMs: 250}, Stage: execv2.FailureStage_FAILURE_STAGE_RUN, PublicMessage: "denied",
			})),
		},
		{
			"failed before the start was observed",
			baseInput(endFailed(&execv2.Failed{Class: execv2.ErrorClass_ERROR_CLASS_INTERNAL, Stage: execv2.FailureStage_FAILURE_STAGE_START}),
				session.CauseFinished, engineSays(completeUnknown), unobserved),
			wantResult(outcomeFailed, completeUnknown, completeUnknown, withoutFacts,
				withFailure(&execv2.Failed{Class: execv2.ErrorClass_ERROR_CLASS_INTERNAL, Stage: execv2.FailureStage_FAILURE_STAGE_START})),
		},
		{
			"failed on the stream between the plugin and the target",
			baseInput(endFailed(&execv2.Failed{Class: execv2.ErrorClass_ERROR_CLASS_CONNECTION, Stage: execv2.FailureStage_FAILURE_STAGE_STREAM}),
				session.CauseFinished, engineSays(completeUnknown)),
			wantResult(outcomeFailed, completeUnknown, completeUnknown,
				withFailure(&execv2.Failed{Class: execv2.ErrorClass_ERROR_CLASS_CONNECTION, Stage: execv2.FailureStage_FAILURE_STAGE_STREAM})),
		},
		{
			"lost: the process may be running, always",
			baseInput(endLost(execv2.LostCause_LOST_CAUSE_NO_STATUS, new(true)), session.CauseFinished, engineSays(completeUnknown)),
			wantResult(outcomeLost, completeUnknown, completeUnknown, withSurvive(new(true))),
		},
		{
			"lost says the process may be running even when the end did not",
			baseInput(endLost(execv2.LostCause_LOST_CAUSE_LINK_DEAD, nil), session.CauseFinished, engineSays(completeUnknown)),
			wantResult(outcomeLost, completeUnknown, completeUnknown, withSurvive(new(true))),
		},
		{
			"lost before the start was observed: no start, no facts",
			baseInput(endLost(execv2.LostCause_LOST_CAUSE_NO_STATUS, new(true)), session.CauseFinished, engineSays(completeUnknown), unobserved),
			wantResult(outcomeLost, completeUnknown, completeUnknown, withoutFacts, withSurvive(new(true))),
		},
		{
			"an end with no arm is a failure of the SDK, never an exit and never an unspecified outcome",
			baseInput(&execv2.End{}, session.CauseFinished, engineSays(completeUnknown)),
			wantResult(outcomeFailed, completeUnknown, completeUnknown, withFailure(internal)),
		},
		{
			"no end at all",
			baseInput(nil, session.CauseFinished, engineSays(completeUnknown)),
			wantResult(outcomeFailed, completeUnknown, completeUnknown, withFailure(internal)),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := buildRunResult(tc.in, true)
			if !proto.Equal(got, tc.want) {
				t.Errorf("result:\n%s\nwant:\n%s", prototext.Format(got), prototext.Format(tc.want))
			}
		})
	}
}

// The completeness of a stream: TRUNCATED when it dropped a byte, whatever else happened; COMPLETE only when nothing was dropped,
// the run ended with a status that the target relayed (the engine says so), and the output was read to its end in time;
// UNSPECIFIED otherwise.
func TestCompletenessIsTruncatedWhenAnythingWasDroppedAndCompleteOnlyForAStatusReadToItsEnd(t *testing.T) {
	dropped := func(o runOutput) runOutput {
		o.Account.Total += 10
		o.Account.Dropped = 10
		return o
	}
	exited := endExited(0, execv2.ExitSource_EXIT_SOURCE_PROCESS, new(false))
	timedOut := endClosed(execv2.ClosedBy_CLOSED_BY_RUN_TIMEOUT, new(false))
	for _, tc := range []struct {
		name           string
		end            *execv2.End
		cause          session.Cause
		mods           []func(*runResultInput)
		stdout, stderr execv2.OutputCompleteness
	}{
		{"a status, nothing dropped, read to its end", exited, session.CauseFinished, nil, completeAll, completeAll},
		{"a status and the drain ran out", exited, session.CauseFinished,
			[]func(*runResultInput){func(r *runResultInput) { r.Drained = false }}, completeUnknown, completeUnknown},
		{"a status the engine cannot vouch for (late writes, an abandoned function, the transport's word)", exited, session.CauseFinished,
			[]func(*runResultInput){engineSaysOutput(completeUnknown)}, completeUnknown, completeUnknown},
		{"a status and the engine's queue dropped bytes that this stream does not account for", exited, session.CauseFinished,
			[]func(*runResultInput){engineSaysOutput(completeTrunc)}, completeUnknown, completeUnknown},
		{"stdout dropped bytes: it is truncated and stderr is not", exited, session.CauseFinished,
			[]func(*runResultInput){func(r *runResultInput) { r.Stdout = dropped(r.Stdout) }}, completeTrunc, completeAll},
		{"stderr dropped bytes: it is truncated and stdout is not", exited, session.CauseFinished,
			[]func(*runResultInput){func(r *runResultInput) { r.Stderr = dropped(r.Stderr) }}, completeAll, completeTrunc},
		{"dropped bytes take precedence over a drain that ran out", exited, session.CauseFinished,
			[]func(*runResultInput){func(r *runResultInput) { r.Stdout = dropped(r.Stdout); r.Drained = false }}, completeTrunc, completeUnknown},
		{"dropped bytes take precedence over an abnormal end", timedOut, session.CauseTimeout,
			[]func(*runResultInput){engineSaysOutput(completeUnknown), func(r *runResultInput) { r.Stdout = dropped(r.Stdout) }}, completeTrunc, completeUnknown},
		{"a timeout with nothing dropped is not complete", timedOut, session.CauseTimeout,
			[]func(*runResultInput){engineSaysOutput(completeAll)}, completeUnknown, completeUnknown},
		{"a lost end with nothing dropped is not complete", endLost(execv2.LostCause_LOST_CAUSE_NO_STATUS, new(true)), session.CauseFinished,
			[]func(*runResultInput){engineSaysOutput(completeAll)}, completeUnknown, completeUnknown},
		{"a failure with nothing dropped is not complete", endFailed(&execv2.Failed{Class: execv2.ErrorClass_ERROR_CLASS_INTERNAL}), session.CauseFinished,
			[]func(*runResultInput){engineSaysOutput(completeAll)}, completeUnknown, completeUnknown},
		{"a lost end with dropped bytes is truncated", endLost(execv2.LostCause_LOST_CAUSE_NO_STATUS, new(true)), session.CauseFinished,
			[]func(*runResultInput){engineSaysOutput(completeUnknown), func(r *runResultInput) { r.Stderr = dropped(r.Stderr) }}, completeUnknown, completeTrunc},
		{"a failure with dropped bytes is truncated", endFailed(&execv2.Failed{Class: execv2.ErrorClass_ERROR_CLASS_INTERNAL}), session.CauseFinished,
			[]func(*runResultInput){engineSaysOutput(completeUnknown), func(r *runResultInput) { r.Stdout = dropped(r.Stdout) }}, completeTrunc, completeUnknown},
		{"a signal read to its end is complete", endSignaled("SIGKILL", 9, execv2.ExitSource_EXIT_SOURCE_PROCESS, new(false)), session.CauseFinished,
			nil, completeAll, completeAll},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := buildRunResult(baseInput(tc.end, tc.cause, tc.mods...), true)
			if got.GetStdoutAccount().GetCompleteness() != tc.stdout || got.GetStderrAccount().GetCompleteness() != tc.stderr {
				t.Errorf("completeness stdout %v stderr %v, want %v and %v", got.GetStdoutAccount().GetCompleteness(), got.GetStderrAccount().GetCompleteness(), tc.stdout, tc.stderr)
			}
		})
	}
}

func engineSaysOutput(c execv2.OutputCompleteness) func(*runResultInput) {
	return func(r *runResultInput) { r.Result.Output = c }
}

// The account of each stream is its own: every number of it reaches the wire, and the streams are not swapped.
func TestEachStreamReportsItsOwnAccount(t *testing.T) {
	in := baseInput(endExited(0, execv2.ExitSource_EXIT_SOURCE_PROCESS, new(false)), session.CauseFinished, func(r *runResultInput) {
		r.Stdout = runOutput{Data: []byte("AB"), Account: outputAccount{Total: 20, Kept: 2, Dropped: 18, Head: 1, Tail: 1, ValidUTF8: true}}
		r.Stderr = runOutput{Data: []byte("\xFFxyz"), Account: outputAccount{Total: 4, Kept: 4, Dropped: 0, Head: 4, Tail: 0, ValidUTF8: false}}
	})
	got := buildRunResult(in, true)
	wantStdout := &execv2.OutputAccount{
		TotalBytes: 20, KeptBytes: 2, DroppedBytes: 18, Truncated: true, HeadBytes: 1, TailBytes: 1, ValidUtf8: true, Completeness: completeTrunc,
	}
	wantStderr := &execv2.OutputAccount{TotalBytes: 4, KeptBytes: 4, HeadBytes: 4, ValidUtf8: false, Completeness: completeAll}
	if !proto.Equal(got.GetStdoutAccount(), wantStdout) {
		t.Errorf("stdout account:\n%s\nwant:\n%s", prototext.Format(got.GetStdoutAccount()), prototext.Format(wantStdout))
	}
	if !proto.Equal(got.GetStderrAccount(), wantStderr) {
		t.Errorf("stderr account:\n%s\nwant:\n%s", prototext.Format(got.GetStderrAccount()), prototext.Format(wantStderr))
	}
	if string(got.GetStdout()) != "AB" || string(got.GetStderr()) != "\xFFxyz" {
		t.Errorf("payloads stdout %q stderr %q, want AB and the raw bytes of stderr", got.GetStdout(), got.GetStderr())
	}
}

// RunEnded is the result without the payloads: the accounts, the outcome and the flags are all there, the bytes are not.
func TestTheResultOfAStreamCarriesNoPayload(t *testing.T) {
	in := baseInput(endExited(2, execv2.ExitSource_EXIT_SOURCE_PROCESS, new(false)), session.CauseFinished)
	full, ended := buildRunResult(in, true), buildRunResult(in, false)
	if len(ended.GetStdout()) != 0 || len(ended.GetStderr()) != 0 {
		t.Errorf("the result of a stream carries stdout %q and stderr %q", ended.GetStdout(), ended.GetStderr())
	}
	full.Stdout, full.Stderr = nil, nil
	if !proto.Equal(full, ended) {
		t.Errorf("the result of a stream:\n%s\nis not the full result without its payloads:\n%s", prototext.Format(ended), prototext.Format(full))
	}
	if ended.GetStdoutAccount().GetKeptBytes() != 3 || ended.GetStderrAccount().GetKeptBytes() != 4 {
		t.Errorf("the accounts are gone from the result of a stream: %v %v", ended.GetStdoutAccount(), ended.GetStderrAccount())
	}
}

// The request id is the host's own, echoed, and empty when the host gave none; the duration is whole milliseconds.
func TestTheResultEchoesTheRequestIDAndReportsWholeMilliseconds(t *testing.T) {
	in := baseInput(endExited(0, execv2.ExitSource_EXIT_SOURCE_PROCESS, new(false)), session.CauseFinished, func(r *runResultInput) {
		r.RequestID = ""
		r.Result.Duration = 2999 * time.Microsecond
	})
	got := buildRunResult(in, true)
	if got.GetRequestId() != "" || got.GetDurationMs() != 2 {
		t.Errorf("request id %q duration %d ms, want an empty id and 2 ms", got.GetRequestId(), got.GetDurationMs())
	}
	in.RequestID = "audit-7"
	if got := buildRunResult(in, true); got.GetRequestId() != "audit-7" {
		t.Errorf("request id %q, want the host's", got.GetRequestId())
	}
}

// The facts of the target and the program are what the SDK observed: with no observed start, none are reported, whatever the
// caller holds.
func TestTheTargetAndTheProgramAreReportedOnlyForAStartThatWasObserved(t *testing.T) {
	in := baseInput(endExited(0, execv2.ExitSource_EXIT_SOURCE_PROCESS, new(false)), session.CauseFinished)
	got := buildRunResult(in, true)
	if got.GetTarget().GetId() != "thing-1" || got.GetProgram().GetName() != "prog" || !got.GetStartObserved() {
		t.Errorf("an observed start: target %v program %v observed %v", got.GetTarget(), got.GetProgram(), got.GetStartObserved())
	}
	in.Result.StartObserved = false
	got = buildRunResult(in, true)
	if got.GetTarget() != nil || got.GetProgram() != nil || got.GetStartObserved() {
		t.Errorf("a start that was not observed: target %v program %v observed %v, want none of them", got.GetTarget(), got.GetProgram(), got.GetStartObserved())
	}
}

// What the engine says about a failed end is copied, not shared: a change of the result does not reach the end the engine holds.
func TestAFailureIsCopiedFromTheEnd(t *testing.T) {
	failed := &execv2.Failed{Class: execv2.ErrorClass_ERROR_CLASS_FORBIDDEN, Stage: execv2.FailureStage_FAILURE_STAGE_RUN}
	in := baseInput(endFailed(failed), session.CauseFinished)
	got := buildRunResult(in, true)
	if got.Failed == nil {
		t.Fatal("the result of a failed end carries no failure")
	}
	got.Failed.Class = execv2.ErrorClass_ERROR_CLASS_INTERNAL
	if failed.GetClass() != execv2.ErrorClass_ERROR_CLASS_FORBIDDEN {
		t.Error("the result shares its failure with the end of the command")
	}
}
