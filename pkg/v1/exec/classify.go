package exec

import (
	"fmt"
	"runtime/debug"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/session"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// This file decides how a command ended: what the author's function returned (an Exit, an error or a panic) and what
// the SDK did to it (a cancel, from whom) become the one typed End of the session. The rules, in the order they apply:
//
//  1. The SDK ended it first (a close, a shutdown, a retired connection, a timeout, a caller that gave up): the end is
//     Closed, whoever asked, whatever the function returned afterwards. What a function returns after its context was
//     cancelled is the aftermath of the cancel and never the command's status. Whether the process may still run
//     follows the session's Kill capability.
//  2. The function panicked: Failed, ClassInternal. The panic value never leaves the plugin.
//  3. The function returned an error: Failed, classified by the author's Target.Classify and the SDK's own rules for
//     context, resource-lane, gRPC and network errors. An Exit returned beside the error is a contract violation and is
//     not used: an error is never turned into an exit 0.
//  4. The function returned an Exit: Exit.end, which gives Exited, Signaled, Lost or, for a shape that breaks the
//     contract, Failed. A process status returned while the function's own last Link report said STALLED is taken as
//     the transport's word and no longer as the process's (see downgradeStalledStatus).
//
// Whatever the rule, the end is typed with a closed class, reason and facts, and carries no text the author wrote
// except Error.Message, which the contract lets through for the classes it names (see Error).

// violation is one way an author's function broke the contract. The engine logs it, counts it in the diagnostics and
// keeps it with the result of the command so that a test (and the conformance suite) can see it. It is never sent to a
// host.
type violation struct {
	kind   string // one of the violation* words of telemetry.go
	detail string // local text: it may name a field; it never quotes what the author or the host wrote
}

func (v violation) String() string { return v.kind + ": " + v.detail }

// functionOutcome is what an author's function did when it returned.
type functionOutcome struct {
	exit       Exit
	err        error
	panicked   bool
	panicValue any    // local only
	stack      []byte // local only
}

// endInputs is everything the end of a command is decided from.
type endInputs struct {
	outcome  functionOutcome // what the function returned; zero when it did not return
	cause    session.Cause   // why the session ended, first caller wins
	caps     Capabilities    // the session's effective capabilities at the end
	started  bool            // the start was observed: a report, the first output or a status
	link     LinkState       // the last Link report, LinkUnspecified when there was none
	classify ClassifyFunc    // the target's own classification, may be nil
}

// endDecision is the end and what is known about it locally.
type endDecision struct {
	end        *execv2.End
	violations []violation
	// failure is the classified error of a Failed end, with the local cause in Err, for the engine's log.
	failure *Error
	// processStatus is true when the end carries a status the process or the target relayed (Exited, Signaled).
	processStatus bool
}

// decideEnd applies the rules above.
func decideEnd(in endInputs) endDecision {
	if in.cause != session.CauseFinished {
		return endDecision{end: closedEnd(in.cause, in.caps.Kill)}
	}
	out := in.outcome
	switch {
	case out.panicked:
		failure := &Error{Class: ClassInternal, Err: fmt.Errorf("the function panicked: %v", out.panicValue)}
		return endDecision{
			end:        failedEnd(failure, failureStage(failure, in.started)),
			violations: []violation{{violationPanic, "the function panicked; the panic value and stack are in the plugin's log"}},
			failure:    failure,
		}
	case out.err != nil:
		var violations []violation
		if !isZeroExit(out.exit) {
			violations = append(violations, violation{violationExitAndError,
				"the function returned an error and an Exit: the error is used, and an error is never turned into an exit"})
		}
		failure, classifyPanic := classifyErrorSafely(in.classify, out.err)
		if classifyPanic != nil {
			violations = append(violations, violation{violationClassifyPanic, "Target.Classify panicked: the error is classified by the SDK alone"})
		}
		return endDecision{end: failedEnd(failure, failureStage(failure, in.started)), violations: violations, failure: failure}
	}
	end, exitViolation := out.exit.end(in.caps)
	decision := endDecision{end: end}
	if exitViolation != nil {
		decision.violations = append(decision.violations, violation{violationExit, exitViolation.what})
	}
	if failed := end.GetFailed(); failed != nil {
		decision.failure = &Error{Class: classFromWire(failed.GetClass()), Reason: reasonFromWire(failed.GetReason())}
		return decision
	}
	if end.GetLost() != nil {
		return decision // the stream ended with no status: there is none to vouch for
	}
	decision.processStatus = true
	if downgradeStalledStatus(end, in.link) {
		decision.violations = append(decision.violations, violation{violationStalledStatus,
			"the function returned a status while its last Link report was STALLED: the status is reported as the transport's"})
	}
	return decision
}

// isZeroExit reports whether e is the empty Exit.
func isZeroExit(e Exit) bool {
	return e.Code == nil && e.Signal == "" && e.SignalNumber == 0 && e.Source == SourceUnspecified && e.Lost == LostUnspecified
}

// failureStage says how far a failed command got. A start that was not observed is START; once it was, a failure of the
// connection to the target is STREAM and any other is RUN.
func failureStage(failure *Error, started bool) execv2.FailureStage {
	switch {
	case !started:
		return execv2.FailureStage_FAILURE_STAGE_START
	case failure.Class == ClassConnection:
		return execv2.FailureStage_FAILURE_STAGE_STREAM
	}
	return execv2.FailureStage_FAILURE_STAGE_RUN
}

// failedEnd is the End for an error classified as failure, at stage. remote_may_survive stays absent: the SDK does not
// know whether the command still runs.
func failedEnd(failure *Error, stage execv2.FailureStage) *execv2.End {
	n := failure.normalized()
	return &execv2.End{Kind: &execv2.End_Failed{Failed: &execv2.Failed{
		Class:         n.Class.toProto(),
		Reason:        n.Reason.toProto(),
		Facts:         errorFactsToProto(n.Facts),
		Stage:         stage,
		PublicMessage: n.Message,
	}}}
}

// errorFactsToProto returns the wire facts, or nil when there is none to say.
func errorFactsToProto(f ErrorFacts) *execv2.ErrorFacts {
	if f == (ErrorFacts{}) {
		return nil
	}
	return &execv2.ErrorFacts{
		Program:        f.Program,
		SelectionName:  f.SelectionName,
		SelectionValue: f.SelectionValue,
		RetryAfterMs:   uint32(f.RetryAfter / time.Millisecond), //nolint:gosec // normalized() bounds it to what a uint32 holds
	}
}

// closedEnd is the End of a session that the SDK ended. The process may still run unless ending a session of this kind
// terminates it.
func closedEnd(cause session.Cause, kill KillBehavior) *execv2.End {
	return &execv2.End{
		Kind:             &execv2.End_Closed{Closed: &execv2.Closed{By: closedBy(cause)}},
		RemoteMaySurvive: survivalOfClose(kill),
	}
}

// closedBy maps a cause of the lifecycle to the wire's ClosedBy. A cause that is no end the SDK made (CauseFinished,
// CauseNone) is a bug of the engine and reads as ClosedBy UNSPECIFIED, "closed", which a consumer treats as closed.
func closedBy(cause session.Cause) execv2.ClosedBy {
	switch cause {
	case session.CauseRequested:
		return execv2.ClosedBy_CLOSED_BY_REQUEST
	case session.CauseShutdown:
		return execv2.ClosedBy_CLOSED_BY_PLUGIN_SHUTDOWN
	case session.CauseRetired:
		return execv2.ClosedBy_CLOSED_BY_CONNECTION_RETIRED
	case session.CauseExpired:
		return execv2.ClosedBy_CLOSED_BY_DETACH_TIMEOUT
	case session.CauseTimeout:
		return execv2.ClosedBy_CLOSED_BY_RUN_TIMEOUT
	case session.CauseCancelled:
		return execv2.ClosedBy_CLOSED_BY_CANCELLED
	case session.CauseNone, session.CauseFinished:
	}
	return execv2.ClosedBy_CLOSED_BY_UNSPECIFIED
}

// survivalOfClose is the remote_may_survive of a Closed end. False only when ending a session of this kind terminates the
// process; a target that can only close its stream, or cannot promise, leaves the process possibly running (true); a kind
// that did not declare what ending does leaves the fact unknown (absent, which a consumer reads as "may").
func survivalOfClose(kill KillBehavior) *bool {
	switch kill {
	case KillTerminates:
		return new(false)
	case KillBestEffort, KillClosesStreamOnly:
		return new(true)
	case KillUnspecified:
	}
	return nil
}

// downgradeStalledStatus handles a process status that arrives while the function's own last Link report said the link to
// the target was stalled. The SDK cannot tell a real status from an exit 0 that a function made up for a connection that
// died, and the function's own report is the only evidence it has: the status keeps its code and is read as the
// transport's (EXIT_SOURCE_TRANSPORT, survival unknown), which claims nothing about the process. It reports whether it
// changed the end.
func downgradeStalledStatus(end *execv2.End, link LinkState) bool {
	if link != LinkStalled {
		return false
	}
	switch kind := end.GetKind().(type) {
	case *execv2.End_Exited:
		if s := kind.Exited.GetSource(); s == execv2.ExitSource_EXIT_SOURCE_PROCESS || s == execv2.ExitSource_EXIT_SOURCE_SIGNAL_FOLDED {
			kind.Exited.Source = execv2.ExitSource_EXIT_SOURCE_TRANSPORT
			end.RemoteMaySurvive = nil
			return true
		}
	case *execv2.End_Signaled:
		if s := kind.Signaled.GetSource(); s == execv2.ExitSource_EXIT_SOURCE_PROCESS || s == execv2.ExitSource_EXIT_SOURCE_SIGNAL_FOLDED {
			kind.Signaled.Source = execv2.ExitSource_EXIT_SOURCE_TRANSPORT
			end.RemoteMaySurvive = nil
			return true
		}
	}
	return false
}

// classifyErrorSafely is classifyError with the author's Classify held to the rule that a callback must not take the
// plugin down: a panic in it is returned, and the error is classified by the SDK's own rules alone.
func classifyErrorSafely(classify ClassifyFunc, err error) (failure *Error, panicked any) {
	defer func() {
		if recovered := recover(); recovered != nil {
			panicked = recovered
			failure = classifyError(nil, err)
		}
	}()
	return classifyError(classify, err), nil
}

// outputCompleteness is the most the engine can say about the output of a command that has ended, from what it counted:
// COMPLETE when nothing was overwritten or refused and the end is a status the target relayed (so the function read its
// streams to the end), TRUNCATED when output was dropped, and UNSPECIFIED otherwise. A consumer that did not deliver
// every byte it was given says less.
func outputCompleteness(d endDecision, dropped, late uint64, abandoned bool) execv2.OutputCompleteness {
	switch {
	case dropped > 0:
		return execv2.OutputCompleteness_OUTPUT_COMPLETENESS_TRUNCATED
	case late > 0 || abandoned || !d.processStatus:
		return execv2.OutputCompleteness_OUTPUT_COMPLETENESS_UNSPECIFIED
	}
	if sourceOf(d.end) == execv2.ExitSource_EXIT_SOURCE_TRANSPORT {
		return execv2.OutputCompleteness_OUTPUT_COMPLETENESS_UNSPECIFIED
	}
	return execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE
}

// sourceOf returns the source of an exit or a signal end, and UNSPECIFIED for any other.
func sourceOf(end *execv2.End) execv2.ExitSource {
	switch kind := end.GetKind().(type) {
	case *execv2.End_Exited:
		return kind.Exited.GetSource()
	case *execv2.End_Signaled:
		return kind.Signaled.GetSource()
	}
	return execv2.ExitSource_EXIT_SOURCE_UNSPECIFIED
}

// panicOutcome is the outcome of a function that panicked with value.
func panicOutcome(value any) functionOutcome {
	return functionOutcome{panicked: true, panicValue: value, stack: debug.Stack()}
}

// classFromWire and reasonFromWire read the Go enums of a wire value, for the local failure of a decision.
func classFromWire(c execv2.ErrorClass) ErrorClass { return enumFromProto(errorClassPairs, c) }

func reasonFromWire(r execv2.ErrorReason) ErrorReason { return enumFromProto(errorReasonPairs, r) }
