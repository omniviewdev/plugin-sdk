package exec

import (
	"google.golang.org/protobuf/proto"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// How a bounded run ends is decided here, in one place: the End the engine concluded for the command, and what the call read of
// its output, become the one RunResult of the run. Once the command was handed to the target every end is a result and none is a
// status, however it ended; the table of docs/exec.md is this file:
//
//	End of the command                      Outcome       exit_source  remote_may_survive
//	Exited                                  EXITED        the End's    the End's
//	Signaled                                SIGNALED      the End's    the End's
//	Closed by RUN_TIMEOUT                   TIMED_OUT     HOST         the End's
//	Closed by CANCELLED or REQUEST          CANCELLED     HOST         the End's
//	Closed by PLUGIN_SHUTDOWN               FAILED        none         the End's  failed: UNAVAILABLE
//	Closed by CONNECTION_RETIRED            FAILED        none         the End's  failed: CONNECTION, CONNECTION_RETIRED
//	Closed by anything else                 FAILED        none         the End's  failed: INTERNAL
//	Failed                                  FAILED        none         the End's  failed: the End's, copied
//	Lost                                    LOST          none         true
//
// A failure that the SDK makes of a close (PLUGIN_SHUTDOWN, CONNECTION_RETIRED and the closes nothing names) is at the stage RUN
// once the start was observed and at START before it; a failure of the End keeps the stage the engine gave it. The exit code is
// present only for Exited, and the signal only for Signaled. The target and the program are reported only for a start that was
// observed: a start is never made up.

// runOutput is what a run read of one stream: the bytes it keeps and their account.
type runOutput struct {
	Data    []byte
	Account outputAccount
}

// runResultInput is everything the RunResult of a run that has ended is built from.
type runResultInput struct {
	// RequestID is the host's own id for the run, echoed; empty when the host gave none.
	RequestID string
	// Result is the end the engine concluded for the command.
	Result commandResult
	// Target and Program are what the SDK knows of the target and of what ran: they are reported when the start was observed.
	Target  *execv2.TargetFacts
	Program *execv2.Program
	// Stdout and Stderr are what the call read of each stream.
	Stdout, Stderr runOutput
	// Drained is true when the output was read to its end within the post-exit drain.
	Drained bool
}

// buildRunResult returns the RunResult of a run that has ended. A result with no payload (withPayload false) is the RunEnded
// of a stream: stdout and stderr were delivered as events, and what is left is the outcome, the exit, the accounts, the failure
// and the flags.
func buildRunResult(in runResultInput, withPayload bool) *execv2.RunResult {
	res := &execv2.RunResult{
		RequestId:     in.RequestID,
		StartObserved: in.Result.StartObserved,
		DurationMs:    uint64(max(in.Result.Duration.Milliseconds(), 0)), //nolint:gosec // not negative
	}
	endRun(res, in.Result.End, in.Result.StartObserved)
	if in.Result.StartObserved {
		res.Target, res.Program = in.Target, in.Program
	}
	res.StdoutAccount = accountToProto(in.Stdout.Account, streamCompleteness(in.Stdout.Account, res.Outcome, in.Result.Output, in.Drained))
	res.StderrAccount = accountToProto(in.Stderr.Account, streamCompleteness(in.Stderr.Account, res.Outcome, in.Result.Output, in.Drained))
	if withPayload {
		res.Stdout, res.Stderr = in.Stdout.Data, in.Stderr.Data
	}
	return res
}

// endRun sets the outcome, the exit, the failure and the survival of res from the End of the command. started says whether the
// start was observed.
func endRun(res *execv2.RunResult, end *execv2.End, started bool) {
	if end != nil && end.RemoteMaySurvive != nil {
		res.RemoteMaySurvive = new(*end.RemoteMaySurvive)
	}
	switch kind := end.GetKind().(type) {
	case *execv2.End_Exited:
		res.Outcome = execv2.Outcome_OUTCOME_EXITED
		res.ExitCode = new(kind.Exited.GetExitCode())
		res.ExitSource = kind.Exited.GetSource()
	case *execv2.End_Signaled:
		res.Outcome = execv2.Outcome_OUTCOME_SIGNALED
		res.Signal, res.SignalNumber = kind.Signaled.GetSignal(), kind.Signaled.GetSignalNumber()
		res.ExitSource = kind.Signaled.GetSource()
	case *execv2.End_Closed:
		closedRun(res, kind.Closed.GetBy(), started)
	case *execv2.End_Failed:
		res.Outcome = execv2.Outcome_OUTCOME_FAILED
		res.Failed = proto.Clone(kind.Failed).(*execv2.Failed)
	case *execv2.End_Lost:
		res.Outcome = execv2.Outcome_OUTCOME_LOST
		res.RemoteMaySurvive = new(true) // a stream that ended with no status: the process may be running, always
	default:
		// An end that no arm names is an end this SDK cannot account for, never an exit and never an unspecified outcome.
		res.Outcome = execv2.Outcome_OUTCOME_FAILED
		res.Failed = runFailure(execv2.ErrorClass_ERROR_CLASS_INTERNAL, execv2.ErrorReason_ERROR_REASON_UNSPECIFIED, started)
	}
}

// closedRun is the outcome of a command that the SDK ended. A timeout and a cancel are the outcomes of a run; every other close
// ends a run as a failure of the plugin or of the connection, because nothing else ends a run.
func closedRun(res *execv2.RunResult, by execv2.ClosedBy, started bool) {
	switch by {
	case execv2.ClosedBy_CLOSED_BY_RUN_TIMEOUT:
		res.Outcome, res.ExitSource = execv2.Outcome_OUTCOME_TIMED_OUT, execv2.ExitSource_EXIT_SOURCE_HOST
	case execv2.ClosedBy_CLOSED_BY_CANCELLED, execv2.ClosedBy_CLOSED_BY_REQUEST:
		res.Outcome, res.ExitSource = execv2.Outcome_OUTCOME_CANCELLED, execv2.ExitSource_EXIT_SOURCE_HOST
	case execv2.ClosedBy_CLOSED_BY_PLUGIN_SHUTDOWN:
		res.Outcome = execv2.Outcome_OUTCOME_FAILED
		res.Failed = runFailure(execv2.ErrorClass_ERROR_CLASS_UNAVAILABLE, execv2.ErrorReason_ERROR_REASON_UNSPECIFIED, started)
	case execv2.ClosedBy_CLOSED_BY_CONNECTION_RETIRED:
		res.Outcome = execv2.Outcome_OUTCOME_FAILED
		res.Failed = runFailure(execv2.ErrorClass_ERROR_CLASS_CONNECTION, execv2.ErrorReason_ERROR_REASON_CONNECTION_RETIRED, started)
	case execv2.ClosedBy_CLOSED_BY_UNSPECIFIED, execv2.ClosedBy_CLOSED_BY_DETACH_TIMEOUT, execv2.ClosedBy_CLOSED_BY_IDLE_TIMEOUT:
		res.Outcome = execv2.Outcome_OUTCOME_FAILED
		res.Failed = runFailure(execv2.ErrorClass_ERROR_CLASS_INTERNAL, execv2.ErrorReason_ERROR_REASON_UNSPECIFIED, started)
	default:
		res.Outcome = execv2.Outcome_OUTCOME_FAILED // a value of a newer contract: read as a close nobody names
		res.Failed = runFailure(execv2.ErrorClass_ERROR_CLASS_INTERNAL, execv2.ErrorReason_ERROR_REASON_UNSPECIFIED, started)
	}
}

// runFailure is the failure the SDK makes of a close that is no end of a run: RUN once the start was observed, START before it.
func runFailure(class execv2.ErrorClass, reason execv2.ErrorReason, started bool) *execv2.Failed {
	stage := execv2.FailureStage_FAILURE_STAGE_START
	if started {
		stage = execv2.FailureStage_FAILURE_STAGE_RUN
	}
	return &execv2.Failed{Class: class, Reason: reason, Stage: stage}
}

// accountToProto is the OutputAccount of one stream with the completeness the run says of it.
func accountToProto(a outputAccount, completeness execv2.OutputCompleteness) *execv2.OutputAccount {
	return &execv2.OutputAccount{
		TotalBytes:   a.Total,
		KeptBytes:    a.Kept,
		DroppedBytes: a.Dropped,
		Truncated:    a.Truncated(),
		HeadBytes:    uint32(a.Head), //nolint:gosec // at most the cap of the stream, which is at most 1 MiB
		TailBytes:    uint32(a.Tail), //nolint:gosec // idem
		ValidUtf8:    a.ValidUTF8,
		Completeness: completeness,
	}
}

// streamCompleteness is what a run can claim of one stream. TRUNCATED when a byte of it was dropped, whatever else happened.
// COMPLETE only when nothing was dropped, the command ended with a status the target relayed, the engine vouches for the output
// (nothing was overwritten, refused after the end or left to a function that was given up on), and the output was read to its
// end within the post-exit drain. UNSPECIFIED for the rest: an abnormal end, a drain that ran out, a target that cannot tell.
func streamCompleteness(a outputAccount, outcome execv2.Outcome, engine execv2.OutputCompleteness, drained bool) execv2.OutputCompleteness {
	switch {
	case a.Dropped > 0:
		return execv2.OutputCompleteness_OUTPUT_COMPLETENESS_TRUNCATED
	case !drained, engine != execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE:
		return execv2.OutputCompleteness_OUTPUT_COMPLETENESS_UNSPECIFIED
	case outcome == execv2.Outcome_OUTCOME_EXITED, outcome == execv2.Outcome_OUTCOME_SIGNALED:
		return execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE
	}
	return execv2.OutputCompleteness_OUTPUT_COMPLETENESS_UNSPECIFIED
}
