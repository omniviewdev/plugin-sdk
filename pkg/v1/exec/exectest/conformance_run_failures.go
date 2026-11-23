package exectest

import (
	"context"
	"errors"
	"strings"
	"time"

	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The cases that are about how a run goes wrong: what an error carries across the wire, a stream that ends with no status, and a
// failure after the command was handed to the target. They share one rule of the contract: a refusal, before the command was handed
// to the target, is a status, and nothing ran; everything after is a result.

// The facts and the message that the cases' errors carry: what a host words a refusal from.
var (
	denyFacts   = exec.ErrorFacts{Program: "kubectl", SelectionName: "container", SelectionValue: "app", RetryAfter: 1500 * time.Millisecond}
	wireFacts   = &execv2.ErrorFacts{Program: "kubectl", SelectionName: "container", SelectionValue: "app", RetryAfterMs: 1500}
	denyMessage = "exec is not allowed in this pod"
)

// failureOptions is the Options callback of the fake of the cases that are about errors: the thing that the request addresses chooses
// what it does. Its errors carry the canary in their local cause and in a panic.
func failureOptions(_ *types.PluginContext, ref resourcev2.ResourceReference) ([]exec.Option, error) {
	switch ref.Address.ID {
	case denyThing:
		return nil, &exec.Error{Class: exec.ErrorClassForbidden, Reason: exec.ErrorReasonTargetDenied, Facts: denyFacts, Message: denyMessage, Err: errors.New(runCanary)}
	case panicThing:
		panic(runCanary)
	case plainThing:
		return nil, errors.New(runCanary)
	}
	return nil, nil
}

// scanFor holds every message of a result and of a stream, as it is on the wire, to the rule that the canary is on none of them.
func scanFor(what string, res *execv2.RunResult, stream *streamCall) []string {
	var found []string
	if raw, err := proto.Marshal(res); err == nil {
		found = append(found, canaryProblem(what+": the result", raw)...)
	}
	if stream != nil {
		for _, event := range stream.events {
			if raw, err := proto.Marshal(event); err == nil {
				found = append(found, canaryProblem(what+": "+eventName(event), raw)...)
			}
		}
	}
	return found
}

// ---------------------------------------------------------------------------------------------------------------
// case 13, the Run part: class, reason and facts round-trip, and a canary in Error.Err appears nowhere
// ---------------------------------------------------------------------------------------------------------------

// caseWireErrorsRun: the class, the reason and the facts of an error cross the wire as they were given, both when the error refuses
// a run (a status) and when it ends one (the failure of a result), and what is local never crosses: the cause of an error and the
// value of a panic, which carry a canary, appear in no result, no event and no status, and a fact that breaks its bound is dropped
// and never cut. A pairing of class and reason that the contract does not allow is an error of the author, served as INTERNAL.
func caseWireErrorsRun(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	if target.Info.Run.IsZero() {
		t.Skipf(noRuns)
	}
	rig := newRunRig(t, env, target, failureOptions)
	rig.checkRefusalsRoundTrip()
	rig.checkFailuresRoundTrip()
}

// checkRefusalsRoundTrip: an error of the Options callback, which runs before the command is handed over, refuses the run: a status
// with the class, the reason and the facts of the error, and its message for a class that is not INTERNAL.
func (r *runRig) checkRefusalsRoundTrip() {
	r.register("never", runScript{})
	for _, tc := range []struct {
		thing   string
		class   exec.ErrorClass
		reason  exec.ErrorReason
		facts   exec.ErrorFacts
		message string
	}{
		{denyThing, exec.ErrorClassForbidden, exec.ErrorReasonTargetDenied, denyFacts, denyMessage},
		{panicThing, exec.ErrorClassInternal, exec.ErrorReasonUnspecified, exec.ErrorFacts{}, ""},
		{plainThing, exec.ErrorClassInternal, exec.ErrorReasonUnspecified, exec.ErrorFacts{}, ""},
	} {
		before := r.started.Load()
		target, err := wireTarget(r.conns, r.info.Type, tc.thing)
		if err != nil {
			r.t.Fatalf("%v", err)
		}
		req := r.runRequest("", "never", func(req *execv2.RunRequest) { req.Target = target })

		call := r.run(req)
		var refusal *exec.Error
		switch {
		case call.res != nil || !errors.As(call.err, &refusal):
			r.t.Errorf("%s: Run answered %v and %v, want a refusal of the exec lane", tc.thing, call.res.GetOutcome(), call.err)
		case refusal.Class != tc.class || refusal.Reason != tc.reason || refusal.Facts != tc.facts || refusal.Message != tc.message:
			r.t.Errorf("%s: the refusal is %s/%s with facts %+v and message %q, want %s/%s with facts %+v and message %q",
				tc.thing, refusal.Class, refusal.Reason, refusal.Facts, refusal.Message, tc.class, tc.reason, tc.facts, tc.message)
		}
		if refusal != nil && (refusal.Err != nil || strings.Contains(call.err.Error(), runCanary)) {
			r.t.Errorf("%s: the error that reached the host carries a local cause: %v", tc.thing, call.err)
		}
		if r.lists(execv2.Feature_FEATURE_RUN_STREAM) {
			stream := r.callStream(req, nil)
			var streamRefusal *exec.Error
			if len(stream.events) != 0 || !errors.As(stream.err, &streamRefusal) || streamRefusal.Class != tc.class || streamRefusal.Reason != tc.reason || streamRefusal.Facts != tc.facts {
				r.t.Errorf("%s (RunStream): %d events and %v, want no event and the same refusal", tc.thing, len(stream.events), stream.err)
			}
		}
		r.scanStatuses(tc.thing, req)
		if got := r.started.Load(); got != before {
			r.t.Errorf("%s: the function was called for a run that was refused", tc.thing)
		}
	}
}

// scanStatuses asks for the run with the generated client, which gives the status as it is on the wire, and holds it to the rules of a
// status: the text is the fixed one, and the canary is nowhere in it.
func (r *runRig) scanStatuses(what string, req *execv2.RunRequest) {
	r.t.Helper()
	ctx, err := resourceplugin.OutgoingSessionContext(r.request())
	if err != nil {
		r.t.Fatalf("the call's context: %v", err)
	}
	ctx, cancel := context.WithTimeout(ctx, caseBound)
	defer cancel()
	check := func(kind string, err error) {
		st, ok := status.FromError(err)
		if !ok || err == nil {
			r.t.Errorf("%s (%s): the answer is %v, want a status that is an error", what, kind, err)
			return
		}
		raw, marshalErr := proto.Marshal(st.Proto())
		if marshalErr != nil {
			r.t.Errorf("%s (%s): marshal the status: %v", what, kind, marshalErr)
			return
		}
		for _, problem := range canaryProblem(what+" ("+kind+"): the status", raw) {
			r.t.Errorf("%s", problem)
		}
		if st.Message() != "exec operation failed" {
			r.t.Errorf("%s (%s): the text of the status is %q, want the fixed text: nothing the plugin wrote crosses in it", what, kind, st.Message())
		}
	}
	_, err = r.raw.Run(ctx, req)
	check("Run", err)
	if r.lists(execv2.Feature_FEATURE_RUN_STREAM) {
		stream, err := r.raw.RunStream(ctx, req)
		if err == nil {
			_, err = stream.Recv()
		}
		check("RunStream", err)
	}
}

// failure is a command that fails, and the failure its result must carry.
type failure struct {
	name   string
	script runScript
	failed *execv2.Failed
}

// checkFailuresRoundTrip: an error that the function returns ends the run with a result of the outcome FAILED that carries the class,
// the reason and the facts it was given, at the stage the run had reached, and the message of a class that is not INTERNAL. A fact
// that breaks its bound is dropped, not cut; a pairing of class and reason that the contract does not allow is INTERNAL; an error that
// is no exec error and a panic are INTERNAL too. The canary in the local cause and in the panic is in no result and no event.
func (r *runRig) checkFailuresRoundTrip() {
	out := []runWrite{{n: 11, chunk: 4}, {stderr: true, n: 5}}
	typed := func() *exec.Error {
		return &exec.Error{
			Class: exec.ErrorClassNotFound, Reason: exec.ErrorReasonNoSuchExecutable, Facts: denyFacts, Message: "kubectl is not in the container", Err: errors.New(runCanary),
		}
	}
	notFound := func(stage execv2.FailureStage) *execv2.Failed {
		return &execv2.Failed{
			Class: execv2.ErrorClass_ERROR_CLASS_NOT_FOUND, Reason: execv2.ErrorReason_ERROR_REASON_NO_SUCH_EXECUTABLE, Facts: wireFacts, Stage: stage,
			PublicMessage: "kubectl is not in the container",
		}
	}
	internal := func(stage execv2.FailureStage) *execv2.Failed {
		return &execv2.Failed{Class: execv2.ErrorClass_ERROR_CLASS_INTERNAL, Stage: stage}
	}
	start, run := execv2.FailureStage_FAILURE_STAGE_START, execv2.FailureStage_FAILURE_STAGE_RUN
	failures := []failure{
		{"an error with facts, before anything was written", runScript{err: typed()}, notFound(start)},
		{"an error with facts, after output", runScript{writes: out, err: typed()}, notFound(run)},
		{"a fact over its bound is dropped and one within it is kept, and a message with a control character is dropped", runScript{err: &exec.Error{
			Class: exec.ErrorClassNotFound, Reason: exec.ErrorReasonNoSuchExecutable,
			Facts: exec.ErrorFacts{Program: strings.Repeat("x", 300), SelectionName: "container"}, Message: "bad\x00message", Err: errors.New(runCanary),
		}}, &execv2.Failed{
			Class: execv2.ErrorClass_ERROR_CLASS_NOT_FOUND, Reason: execv2.ErrorReason_ERROR_REASON_NO_SUCH_EXECUTABLE,
			Facts: &execv2.ErrorFacts{SelectionName: "container"}, Stage: start,
		}},
		{"a pairing of class and reason the contract does not allow", runScript{err: &exec.Error{
			Class: exec.ErrorClassForbidden, Reason: exec.ErrorReasonNoSuchExecutable, Facts: denyFacts, Message: "no", Err: errors.New(runCanary),
		}}, internal(start)},
		{"an error that is no exec error", runScript{writes: out, err: errors.New(runCanary)}, internal(run)},
		{"a panic", runScript{writes: out, panics: true}, internal(run)},
	}
	for _, f := range failures {
		res := r.scenario(f.name, f.script, endFailedWith(f.failed))
		for _, problem := range scanFor(f.name, res, r.lastStream) {
			r.t.Errorf("%s", problem)
		}
	}
}

// ---------------------------------------------------------------------------------------------------------------
// case 16, the Run part: a stream that ends with no status ends a run LOST, never Exited 0
// ---------------------------------------------------------------------------------------------------------------

// caseWireLostRun: a command whose stream to the target ended with no terminal status is a result of the outcome LOST: it may be
// running, so remote_may_survive is true; there is no exit code, which is never 0; the output it had written is kept, with an
// account that cannot say the output is complete; and a run that is lost before its start was observed has no target and no
// program, and a RunStream of it is RunEnded alone. It is a result and never an error, and a host never retries it.
func caseWireLostRun(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	if target.Info.Run.IsZero() {
		t.Skipf(noRuns)
	}
	rig := newRunRig(t, env, target, nil)
	out := []runWrite{{n: 13, chunk: 5}, {stderr: true, n: 7}}
	rig.scenario("a stream that ended with no status, and no output", runScript{exit: exec.ExitLost(exec.LostNoStatus)}, endLost(execv2.LostCause_LOST_CAUSE_NO_STATUS))
	rig.scenario("a stream that ended with no status after output", runScript{writes: out, exit: exec.ExitLost(exec.LostNoStatus)}, endLost(execv2.LostCause_LOST_CAUSE_NO_STATUS))
	rig.scenario("a link that gave up after output", runScript{report: true, writes: out, exit: exec.ExitLost(exec.LostLinkDead)}, endLost(execv2.LostCause_LOST_CAUSE_LINK_DEAD))
	rig.scenario("lost after the cap elided output: truncated takes precedence", runScript{
		writes: []runWrite{{n: 100, chunk: 9}, {stderr: true, n: 3}}, exit: exec.ExitLost(exec.LostNoStatus),
	}, endLost(execv2.LostCause_LOST_CAUSE_NO_STATUS), func(req *execv2.RunRequest) {
		req.Output = &execv2.OutputLimits{MaxStdoutBytes: 10, Policy: execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD_TAIL}
	})
}

// ---------------------------------------------------------------------------------------------------------------
// case 27: a run that fails after the command was handed to the target
// ---------------------------------------------------------------------------------------------------------------

// caseWireRunFailure: once the command was handed to the target, how it ended is part of the result, never a status: a failure of the
// function, of its connection, of the plugin or the connection that the run belongs to is a result of the outcome FAILED with the class,
// reason and stage of the failure, the output read before it (partial, with its account and a completeness that does not claim more
// than the SDK knows), whether the start was observed, and whether the process may survive, which a failure leaves unsaid. A refusal,
// before the hand-off, is a status: the same error that a function returns is a result, and an Options callback's is a status.
func caseWireRunFailure(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	if target.Info.Run.IsZero() {
		t.Skipf(noRuns)
	}
	rig := newRunRig(t, env, target, failureOptions)
	rig.checkFailureResults()
	rig.checkRefusedBeforeTheHandOff()
	rig.checkEndedByThePlugin("the connection the run belongs to is retired", endClosed(execv2.ClosedBy_CLOSED_BY_CONNECTION_RETIRED, target.Info.Capabilities.Kill),
		func(rig *runRig) { rig.conns.Retire() })
	rig2 := newRunRig(t, env, target, failureOptions)
	rig2.checkEndedByThePlugin("the plugin shuts down", endClosed(execv2.ClosedBy_CLOSED_BY_PLUGIN_SHUTDOWN, target.Info.Capabilities.Kill), func(rig *runRig) {
		ctx, cancel := context.WithTimeout(context.Background(), caseBound)
		defer cancel()
		if err := rig.server.plugin.Shutdown(ctx); err != nil {
			rig.t.Errorf("shut the plugin down: %v", err)
		}
	})
}

// checkFailureResults: the failures that the function causes are results.
func (r *runRig) checkFailureResults() {
	out := []runWrite{{n: 17, chunk: 5}, {stderr: true, n: 9, chunk: 4}, {n: 3}}
	unavailable := func() *exec.Error {
		return &exec.Error{Class: exec.ErrorClassUnavailable, Reason: exec.ErrorReasonNotRunning, Err: errors.New(runCanary)}
	}
	failed := func(class execv2.ErrorClass, reason execv2.ErrorReason, stage execv2.FailureStage) *execv2.Failed {
		return &execv2.Failed{Class: class, Reason: reason, Stage: stage}
	}
	unspecified := execv2.ErrorReason_ERROR_REASON_UNSPECIFIED
	start, run, stream := execv2.FailureStage_FAILURE_STAGE_START, execv2.FailureStage_FAILURE_STAGE_RUN, execv2.FailureStage_FAILURE_STAGE_STREAM
	for _, f := range []failure{
		{"before it writes anything: the start is not observed", runScript{err: unavailable()},
			failed(execv2.ErrorClass_ERROR_CLASS_UNAVAILABLE, execv2.ErrorReason_ERROR_REASON_NOT_RUNNING, start)},
		{"after partial output on both streams", runScript{writes: out, err: unavailable()},
			failed(execv2.ErrorClass_ERROR_CLASS_UNAVAILABLE, execv2.ErrorReason_ERROR_REASON_NOT_RUNNING, run)},
		{"after it reported its start, before any output", runScript{report: true, err: unavailable()},
			failed(execv2.ErrorClass_ERROR_CLASS_UNAVAILABLE, execv2.ErrorReason_ERROR_REASON_NOT_RUNNING, run)},
		{"its connection to the target fails after output: the stream failed", runScript{writes: out, err: &exec.Error{Class: exec.ErrorClassConnection, Err: errors.New(runCanary)}},
			failed(execv2.ErrorClass_ERROR_CLASS_CONNECTION, unspecified, stream)},
		{"an error that is no exec error", runScript{writes: out, err: errors.New(runCanary)}, failed(execv2.ErrorClass_ERROR_CLASS_INTERNAL, unspecified, run)},
		{"a panic after output", runScript{writes: out, panics: true}, failed(execv2.ErrorClass_ERROR_CLASS_INTERNAL, unspecified, run)},
	} {
		r.scenario(f.name, f.script, endFailedWith(f.failed))
	}
	// A failure after the cap elided output is still a failure with the output it read, and the truncation is what the account says.
	truncated := runScript{writes: []runWrite{{n: 300, chunk: 17}, {stderr: true, n: 4}}, err: unavailable()}
	r.scenario("after the cap elided output: truncated takes precedence over unspecified", truncated,
		endFailedWith(failed(execv2.ErrorClass_ERROR_CLASS_UNAVAILABLE, execv2.ErrorReason_ERROR_REASON_NOT_RUNNING, run)), func(req *execv2.RunRequest) {
			req.Output = &execv2.OutputLimits{MaxStdoutBytes: 10, MaxStderrBytes: 2, Policy: execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD_TAIL}
		})
}

// checkRefusedBeforeTheHandOff: the error that the function of a run returns is a result of the outcome FAILED, and the same error
// that the Options callback of the start returns, before the command is handed over, is a status with no result, and the function
// never runs.
func (r *runRig) checkRefusedBeforeTheHandOff() {
	r.register("never", runScript{})
	for _, tc := range []struct {
		name string
		mod  func(*execv2.RunRequest)
		want string
	}{
		{"an Options callback that refuses", nil, "FORBIDDEN/TARGET_DENIED"},
		{"a request that breaks a bound", func(req *execv2.RunRequest) { req.Argv = nil }, "INVALID_REQUEST"},
	} {
		before := r.started.Load()
		target, err := wireTarget(r.conns, r.info.Type, denyThing)
		if err != nil {
			r.t.Fatalf("%v", err)
		}
		req := r.runRequest("", "never", func(req *execv2.RunRequest) {
			if tc.mod == nil {
				req.Target = target
				return
			}
			tc.mod(req)
		})
		call := r.run(req)
		if call.res != nil || call.err == nil || refusalName(call.err) != tc.want {
			r.t.Errorf("%s: Run answered %v and %v, want a status: the refusal %s and no result", tc.name, call.res.GetOutcome(), call.err, tc.want)
		}
		if got := r.started.Load(); got != before {
			r.t.Errorf("%s: the function was called for a run that was refused: nothing ran", tc.name)
		}
	}
	// The same error from the function, once the command was handed over, is a result.
	r.scenario("the error of the Options callback, returned by the function", runScript{err: &exec.Error{
		Class: exec.ErrorClassForbidden, Reason: exec.ErrorReasonTargetDenied, Facts: denyFacts, Message: denyMessage,
	}}, endFailedWith(&execv2.Failed{
		Class: execv2.ErrorClass_ERROR_CLASS_FORBIDDEN, Reason: execv2.ErrorReason_ERROR_REASON_TARGET_DENIED, Facts: wireFacts,
		Stage: execv2.FailureStage_FAILURE_STAGE_START, PublicMessage: denyMessage,
	}))
}

// checkEndedByThePlugin: a run that the plugin ends itself (it shuts down, or the connection the run belongs to is retired) is a result
// too, after the hand-over: the outcome FAILED, UNAVAILABLE for a shutdown and CONNECTION for a retirement, with the output it had
// written. The process may survive as the kind's kill behavior says, which the end the SDK made records. cause is what ends the run,
// and want the end the SDK makes of it.
func (r *runRig) checkEndedByThePlugin(name string, want *execv2.End, cause func(*runRig)) {
	r.t.Helper()
	up := make(chan struct{}, 2)
	script := r.register(name, runScript{writes: []runWrite{{n: 5}, {stderr: true, n: 2}}, block: true, written: func() { up <- struct{}{} }})
	req := r.runRequest("", name)
	done := make(chan *runCall, 1)
	go func() { done <- r.run(req) }()
	select {
	case <-up:
	case call := <-done:
		r.t.Errorf("%s: the run ended before the function wrote: %v", name, call.err)
		return
	case <-time.After(caseBound):
		r.t.Errorf("%s: the function did not start within %v", name, caseBound)
		return
	}
	cause(r)
	select {
	case call := <-done:
		r.expect(name, call, r.wantFor(req, script, want))
	case <-time.After(caseBound):
		r.t.Errorf("%s: the run did not end within %v", name, caseBound)
	}
}
