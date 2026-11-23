package exectest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync/atomic"

	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The wire cases about what a session says when it goes wrong: case 13 (an error crosses the wire as its class, its reason and its facts,
// and what is local to the plugin never does), case 16 (a stream that ended with no status is Lost, never an exit 0) and case 17 (the source
// and the report of an exit round-trip, and a signal from a kind that cannot report one is a contract violation). They share the rule of the
// contract that a refusal, before the command was handed to the function, is a status with no frame, and everything after is data: the
// Ended.

// readToEnd reads the stream to its end and returns every frame that it carried, with the Ended: the Ended must be the last frame, there
// must be one, and the stream must then end with status OK. A case that scans the whole stream for what it must not carry reads it with this.
func (s *wireSession) readToEnd() ([]*execv2.ServerFrame, *execv2.Ended) {
	t := s.r.t
	t.Helper()
	var frames []*execv2.ServerFrame
	for {
		frame, err := s.recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("session %q: the stream broke after %d frames: %v", s.id, len(frames), err)
		}
		frames = append(frames, frame)
	}
	var ended *execv2.Ended
	for i, frame := range frames {
		switch e := frame.GetEnded(); {
		case e == nil:
		case ended != nil:
			t.Errorf("session %q: a second Ended, at frame %d: a session has at most one", s.id, i)
		case i != len(frames)-1:
			t.Errorf("session %q: Ended is frame %d of %d: nothing follows Ended", s.id, i+1, len(frames))
			ended = e
		default:
			ended = e
		}
	}
	if ended == nil {
		t.Fatalf("session %q: the stream ended with status OK and no Ended after %d frames", s.id, len(frames))
	}
	s.ended = ended
	for _, problem := range s.grammar.problems {
		t.Errorf("session %q: %s", s.id, problem)
	}
	if s.out != nil {
		for _, problem := range s.out.problems {
			t.Errorf("session %q: %s", s.id, problem)
		}
	}
	return frames, ended
}

// ---------------------------------------------------------------------------------------------------------------
// case 13: class, reason and facts round-trip, and what is local never crosses
// ---------------------------------------------------------------------------------------------------------------

// caseWireErrors is case 13 for a target that declares sessions, runs or both: the part for each of them that the target serves.
func caseWireErrors(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	sessions, runs := !target.Info.Session.IsZero(), !target.Info.Run.IsZero()
	if !sessions && !runs {
		t.Skipf("the target declares neither sessions nor runs: no command runs in it")
	}
	if runs {
		caseWireErrorsRun(t, target, env)
	}
	if sessions {
		caseWireErrorsOpen(t, target, env)
	}
}

// caseWireErrorsOpen: the class, the reason and the facts of an error cross the wire as they were given, when the error refuses an Open
// (a status, with no frame before it) and when it ends a session (the failure of an Ended), and what is local never crosses: the cause of an
// error and the value of a panic, which carry a canary, appear in no frame, no status and no header, a fact that breaks its bound is dropped
// and never cut, and a pairing of class and reason that the contract does not allow is an error of the author, served as INTERNAL.
func caseWireErrorsOpen(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	if target.Info.Session.IsZero() {
		t.Skipf(noSessions)
	}
	rig := newOptionsRig(t, env, target, failureOptions)
	rig.checkOpenRefusalsRoundTrip()
	rig.checkOpenFailuresRoundTrip()
}

// checkOpenRefusalsRoundTrip: an error of the Options callback, which runs before the function is called, refuses the Open: a status with
// the class, the reason and the facts of the error and its message for a class that is not INTERNAL, and no frame before it.
func (r *wireRig) checkOpenRefusalsRoundTrip() {
	r.t.Helper()
	r.script("never", Block())
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
		req := r.openRequest(r.newID("refused"), "never", func(st *execv2.Start) { st.Target = target })

		conn, err := r.client.Open(r.request(), req)
		var refusal *exec.Error
		switch {
		case err == nil:
			_ = conn.Close()
			r.t.Errorf("%s: the Open was admitted, want a refusal", tc.thing)
		case !errors.As(err, &refusal):
			r.t.Errorf("%s: the Open was refused with %v, want a refusal of the exec lane", tc.thing, err)
		case refusal.Class != tc.class || refusal.Reason != tc.reason || refusal.Facts != tc.facts || refusal.Message != tc.message:
			r.t.Errorf("%s: the refusal is %s/%s with facts %+v and message %q, want %s/%s with facts %+v and message %q",
				tc.thing, refusal.Class, refusal.Reason, refusal.Facts, refusal.Message, tc.class, tc.reason, tc.facts, tc.message)
		}
		if refusal != nil && (refusal.Err != nil || strings.Contains(err.Error(), runCanary)) {
			r.t.Errorf("%s: the error that reached the host carries a local cause: %v", tc.thing, err)
		}
		r.scanOpenStatus(tc.thing, req)
		if got := r.started.Load(); got != before {
			r.t.Errorf("%s: the function was called for an Open that was refused", tc.thing)
		}
	}
}

// scanOpenStatus asks for the Open with the generated client, which gives the status as it is on the wire, and holds it to the rules of a
// status: no frame came before it, the text is the fixed one, and the canary is nowhere in the status, its details, its header or its trailer.
func (r *wireRig) scanOpenStatus(what string, req *execv2.OpenRequest) {
	r.t.Helper()
	ctx, err := resourceplugin.OutgoingSessionContext(r.request())
	if err != nil {
		r.t.Fatalf("the call's context: %v", err)
	}
	ctx, cancel := context.WithTimeout(ctx, caseBound)
	defer cancel()
	stream, err := r.raw.Open(ctx)
	if err != nil {
		r.t.Errorf("%s: open the raw stream: %v", what, err)
		return
	}
	if err := stream.Send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Open{Open: req}}); err != nil {
		r.t.Errorf("%s: send the open frame: %v", what, err)
		return
	}
	frame, err := stream.Recv()
	st, ok := status.FromError(err)
	if frame != nil || err == nil || !ok {
		r.t.Errorf("%s (Open): the answer is %s and %v, want a status that is an error and no frame", what, frameName(frame), err)
		return
	}
	raw, marshalErr := proto.Marshal(st.Proto())
	if marshalErr != nil {
		r.t.Errorf("%s (Open): marshal the status: %v", what, marshalErr)
		return
	}
	for _, problem := range canaryProblem(what+" (Open): the status", raw) {
		r.t.Errorf("%s", problem)
	}
	if st.Message() != "exec operation failed" {
		r.t.Errorf("%s (Open): the text of the status is %q, want the fixed text: nothing the plugin wrote crosses in it", what, st.Message())
	}
	header, _ := stream.Header()
	for name, md := range map[string]metadata.MD{"header": header, "trailer": stream.Trailer()} {
		for key, values := range md {
			if strings.Contains(key, runCanary) || strings.Contains(strings.Join(values, "\x00"), runCanary) {
				r.t.Errorf("%s (Open): the %s %q carries the canary: nothing local to the plugin crosses in the metadata", what, name, key)
			}
		}
	}
}

// openFailure is a function that fails, and the failure the Ended must carry.
type openFailure struct {
	name   string
	steps  []Step
	output string
	failed *execv2.Failed
}

// checkOpenFailuresRoundTrip: an error that the function returns ends the session with an Ended whose end is a failure that carries the class,
// the reason and the facts it was given, at the stage the session had reached (START until the start was observed, STREAM for a failure of the
// connection to the target after it, RUN otherwise), and the message of a class that is not INTERNAL. A fact that breaks its bound is dropped,
// not cut; a pairing of class and reason that the contract does not allow is INTERNAL, and so are an error that is no exec error and a panic.
// The canary in the local cause and in the panic is in no frame of the stream.
func (r *wireRig) checkOpenFailuresRoundTrip() {
	r.t.Helper()
	const out = "partial output"
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
	start, run, stream := execv2.FailureStage_FAILURE_STAGE_START, execv2.FailureStage_FAILURE_STAGE_RUN, execv2.FailureStage_FAILURE_STAGE_STREAM
	for _, f := range []openFailure{
		{"an error with facts, before anything was written", []Step{Fail(typed())}, "", notFound(start)},
		{"an error with facts, after output", []Step{WriteOut(out), Fail(typed())}, out, notFound(run)},
		{"a fact over its bound is dropped and one within it is kept, and a message with a control character is dropped", []Step{Fail(&exec.Error{
			Class: exec.ErrorClassNotFound, Reason: exec.ErrorReasonNoSuchExecutable,
			Facts: exec.ErrorFacts{Program: strings.Repeat("x", 300), SelectionName: "container"}, Message: "bad\x00message", Err: errors.New(runCanary),
		})}, "", &execv2.Failed{
			Class: execv2.ErrorClass_ERROR_CLASS_NOT_FOUND, Reason: execv2.ErrorReason_ERROR_REASON_NO_SUCH_EXECUTABLE,
			Facts: &execv2.ErrorFacts{SelectionName: "container"}, Stage: start,
		}},
		{"a pairing of class and reason the contract does not allow", []Step{Fail(&exec.Error{
			Class: exec.ErrorClassForbidden, Reason: exec.ErrorReasonNoSuchExecutable, Facts: denyFacts, Message: "no", Err: errors.New(runCanary),
		})}, "", internal(start)},
		{"a failure of the connection to the target after output: the stream failed", []Step{WriteOut(out), Fail(&exec.Error{Class: exec.ErrorClassConnection, Err: errors.New(runCanary)})}, out,
			&execv2.Failed{Class: execv2.ErrorClass_ERROR_CLASS_CONNECTION, Stage: stream}},
		{"an error that is no exec error, after output", []Step{WriteOut(out), Fail(errors.New(runCanary))}, out, internal(run)},
		{"an error that is no exec error, before anything was written", []Step{Fail(errors.New(runCanary))}, "", internal(start)},
		{"a panic after output", []Step{WriteOut(out), Panic(runCanary)}, out, internal(run)},
		{"a panic before anything was written", []Step{Panic(runCanary)}, "", internal(start)},
	} {
		script := r.newID("script")
		r.script(script, f.steps...)
		s := r.mustOpen(r.newID("failure"), script)
		audit := s.audit(nil)
		audit.keep = true
		frames, ended := s.readToEnd()
		s.checkEnded(f.name, ended, wantEnded{end: endFailedWith(f.failed), started: f.output != ""})
		if got := string(audit.data); got != f.output {
			r.t.Errorf("%s: the output is %q, want what was written before the failure, %q", f.name, got, f.output)
		}
		for i, frame := range frames {
			if raw, err := proto.Marshal(frame); err == nil {
				for _, problem := range canaryProblem(fmt.Sprintf("%s: frame %d (%s)", f.name, i+1, frameName(frame)), raw) {
					r.t.Errorf("%s", problem)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------------------------------------------
// case 16: a stream that ended with no status is Lost, never an exit 0
// ---------------------------------------------------------------------------------------------------------------

// caseWireLost is case 16 for a target that declares sessions, runs or both: the part for each of them that the target serves.
func caseWireLost(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	sessions, runs := !target.Info.Session.IsZero(), !target.Info.Run.IsZero()
	if !sessions && !runs {
		t.Skipf("the target declares neither sessions nor runs: no command runs in it")
	}
	if runs {
		caseWireLostRun(t, target, env)
	}
	if sessions {
		caseWireLostOpen(t, target, env)
	}
}

// caseWireLostOpen: a function whose stream to the target ended with no terminal status ends the session Lost: the process may be running,
// so remote_may_survive is true; there is no exit code, which is never 0; the output it had written is intact and its start is observed when
// it wrote, and the completeness cannot say the output is complete (UNSPECIFIED, and TRUNCATED when output was dropped, which takes precedence).
// It is an Ended, and never a status. An Exit that says nothing, or both a loss and a code, is a failure of the target and never an exit 0.
func caseWireLostOpen(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	info := target.Info
	if info.Session.IsZero() {
		t.Skipf(noSessions)
	}
	caps := info.Capabilities
	rig := newWireRig(t, env, target)
	noStatus, linkDead := endLost(execv2.LostCause_LOST_CAUSE_NO_STATUS), endLost(execv2.LostCause_LOST_CAUSE_LINK_DEAD)
	lost, lostLink := exec.ExitLost(exec.LostNoStatus), exec.ExitLost(exec.LostLinkDead)
	both := []Step{WriteOut("tail")}
	output := "tail"
	if !rig.tty() && caps.SeparateStderr {
		both = append(both, WriteErr("!"))
		output = "tail!"
	}
	internal := endFailed(execv2.ErrorClass_ERROR_CLASS_INTERNAL, execv2.ErrorReason_ERROR_REASON_UNSPECIFIED, execv2.FailureStage_FAILURE_STAGE_RUN)
	for _, sc := range []endScenario{
		{name: "a stream that ended with no status, and no output", steps: []Step{EndWith(lost)}, end: noStatus},
		{name: "a stream that ended with no status after output", steps: append(slices.Clone(both), EndWith(lost)), end: noStatus, started: true, output: output},
		{name: "a link that gave up after the function reported its start", steps: []Step{ReportStarted(exec.SessionFacts{Transport: "exectest"}), EndWith(lostLink)},
			end: linkDead, started: true, reports: true},
		{name: "a link that gave up after output", steps: append(slices.Clone(both), EndWith(lostLink)), end: linkDead, started: true, output: output},
		{name: "an Exit that says nothing is a failure of the target", steps: []Step{EndWith(exec.Exit{})}, end: internal},
		{name: "a loss with a code is a failure of the target", steps: []Step{EndWith(exec.Exit{Code: new(int64), Lost: exec.LostNoStatus})}, end: internal},
	} {
		ended := rig.playEnd(sc)
		if arm, _ := endArm(ended.GetEnd()); arm == "exited" {
			t.Errorf("%s: the end is an exit: a stream that ended with no status is never an exit, and an exit 0 least of all", sc.name)
		}
	}
	rig.checkLostAfterDrops()
}

// checkLostAfterDrops: for a target that stalls lossily, output that the ring dropped before the stream ended is counted, and it takes
// precedence over what a lost end cannot prove: the output is TRUNCATED. A target whose stalled reader slows the process drops nothing,
// and has nothing to check here.
func (r *wireRig) checkLostAfterDrops() {
	r.t.Helper()
	if r.info.Capabilities.Stall != exec.StallLossy {
		return
	}
	ring := int(r.described().GetSession().GetReplayBytes())
	total := ring + 1<<20
	if ring > 8<<20 {
		r.t.Logf("the declared replay ring is %d bytes, too large to overflow cheaply: the flood of %d bytes does not", ring, 4<<20)
		total = 4 << 20
	}
	var accepted atomic.Int64
	r.script("flood-lost", floodPattern(total, &accepted), EndWith(exec.ExitLost(exec.LostNoStatus)))
	s := r.mustOpenOn(r.slow(), r.newID("flood-lost"), "flood-lost")
	audit := s.audit(patternByte)
	r.await(fmt.Sprintf("a function that loses its stream to write all %d bytes while its host reads nothing", total), func() bool { return accepted.Load() == int64(total) })
	ended := s.finish()
	s.checkEnded("lost after the ring dropped output", ended, wantEnded{end: endLost(execv2.LostCause_LOST_CAUSE_NO_STATUS), started: true})
	if audit.dropped == 0 && total > ring+(1<<20)-1 {
		r.t.Errorf("a function that wrote %d bytes into a ring of %d while its host read nothing lost none before its stream ended: the output is not bounded", total, ring)
	}
	if audit.dropped > 0 && ended.GetOutput() != execv2.OutputCompleteness_OUTPUT_COMPLETENESS_TRUNCATED {
		r.t.Errorf("lost after %d bytes were dropped: the output is %s, want TRUNCATED: it takes precedence over what a lost end cannot prove", audit.dropped, ended.GetOutput())
	}
}

// ---------------------------------------------------------------------------------------------------------------
// case 17: exit source and report round-trip, and a signal from a kind that cannot report one is a violation
// ---------------------------------------------------------------------------------------------------------------

// exitCase is what a function returns, and what the host must be told of it for the declared kind.
type exitCase struct {
	name string
	exit exec.Exit
	end  *execv2.End
	// output is what the function writes before it returns: the start is observed then, whatever the end is.
	output string
	// failed says that the end is a failure of the target: the function broke the contract of Exit. lost says that it is a break of the
	// stream, which has no status.
	failed, lost bool
}

// signaledEnd is a Signaled end with the source and the survival that the contract gives it.
func signaledEnd(name string, number int32, source execv2.ExitSource, survive *bool) *execv2.End {
	return &execv2.End{
		Kind:             &execv2.End_Signaled{Signaled: &execv2.Signaled{Signal: name, SignalNumber: number, Source: source}},
		RemoteMaySurvive: survive,
	}
}

// exitCases are the Exits that a function may return and what each becomes on the kind the target declared: an exit code with each source,
// a code that does not fit 32 bits and one that is negative, a signal in each shape for a kind that reports signals, a signal from one that does
// not, and the shapes that break the contract of Exit.
func exitCases(caps exec.Capabilities) []exitCase {
	process := caps.ExitReport != exec.ExitReportNone
	transport := execv2.ExitSource_EXIT_SOURCE_TRANSPORT
	// The status of the process: the kind says whose it is. A kind that reports none can only relay the transport's word, and the
	// survival is then not known.
	status := func(code int64, source execv2.ExitSource) *execv2.End {
		if !process {
			return endExited(code, transport, nil)
		}
		return endExited(code, source, new(false))
	}
	internal := endFailed(execv2.ErrorClass_ERROR_CLASS_INTERNAL, execv2.ErrorReason_ERROR_REASON_UNSPECIFIED, execv2.FailureStage_FAILURE_STAGE_RUN)
	cases := []exitCase{
		{name: "the process's own status 0", exit: exec.ExitCode(0), end: status(0, execv2.ExitSource_EXIT_SOURCE_PROCESS)},
		{name: "the process's own status 255", exit: exec.ExitCode(255), end: status(255, execv2.ExitSource_EXIT_SOURCE_PROCESS)},
		{name: "a Windows status above 32 bits: carried exactly", exit: exec.ExitCode(uint32(0xC0000005)), end: status(3221225477, execv2.ExitSource_EXIT_SOURCE_PROCESS)},
		{name: "the same Windows status as a negative 32-bit reading: it stays negative", exit: exec.ExitCode(int32(-1073741819)), end: status(-1073741819, execv2.ExitSource_EXIT_SOURCE_PROCESS)},
		{name: "a code the target folded from a death by signal", exit: exec.ExitFolded(137), end: status(137, execv2.ExitSource_EXIT_SOURCE_SIGNAL_FOLDED)},
		{name: "a code the transport relayed", exit: exec.ExitTransport(5), end: endExited(5, transport, nil)},
		{name: "a code the transport relayed, 0: the stream ended normally and relayed no status of the process", exit: exec.ExitTransport(0), end: endExited(0, transport, nil)},
		{name: "a code the transport relayed, 0, after output", output: "out", exit: exec.ExitTransport(0), end: endExited(0, transport, nil)},
		{name: "a break of the stream with no status is Lost, never an exit 0", exit: exec.ExitLost(exec.LostNoStatus), end: endLost(execv2.LostCause_LOST_CAUSE_NO_STATUS), lost: true},
		{name: "a break of the stream after output", output: "out", exit: exec.ExitLost(exec.LostNoStatus), end: endLost(execv2.LostCause_LOST_CAUSE_NO_STATUS), lost: true},
		{name: "a link that gave up is Lost", exit: exec.ExitLost(exec.LostLinkDead), end: endLost(execv2.LostCause_LOST_CAUSE_LINK_DEAD), lost: true},
		{name: "a status the author claimed for the SDK's own source", exit: exec.Exit{Code: new(int64(3)), Source: exec.SourceHost}, end: endExited(3, transport, nil)},
		{name: "an Exit that says nothing", exit: exec.Exit{}, end: internal, failed: true},
		{name: "an Exit with both a code and a signal", exit: exec.Exit{Code: new(int64(1)), Signal: "SIGKILL", SignalNumber: 9}, end: internal, failed: true},
	}
	if reportsSignals(caps) {
		byProcess := execv2.ExitSource_EXIT_SOURCE_PROCESS
		cases = append(cases,
			exitCase{name: "a signal", exit: exec.ExitSignal("SIGKILL", 9), end: signaledEnd("SIGKILL", 9, byProcess, new(false))},
			exitCase{name: "a signal that has a name and no number", exit: exec.ExitSignal("SIGTERM", 0), end: signaledEnd("SIGTERM", 0, byProcess, new(false))},
			exitCase{name: "a signal that has a number and no name", exit: exec.ExitSignal("", 15), end: signaledEnd("", 15, byProcess, new(false))},
			exitCase{name: "a signal whose name is no text the wire carries: the name is dropped and the number kept", exit: exec.ExitSignal("SIG\x00KILL", 9), end: signaledEnd("", 9, byProcess, new(false))},
			exitCase{name: "a signal whose number is no signal number: the number is dropped and the name kept", exit: exec.ExitSignal("SIGHUP", 99), end: signaledEnd("SIGHUP", 0, byProcess, new(false))},
			exitCase{name: "a signal with neither a usable name nor a usable number", exit: exec.ExitSignal("SIG\x00KILL", 65), end: internal, failed: true},
			exitCase{name: "a signal with a folded source contradicts itself: it is read as the transport's word", exit: exec.Exit{Signal: "SIGKILL", SignalNumber: 9, Source: exec.SourceSignalFolded},
				end: signaledEnd("SIGKILL", 9, transport, nil)})
	} else {
		cases = append(cases,
			exitCase{name: "a signal from a kind that does not report signals is a failure of the target: it is never folded into a code", exit: exec.ExitSignal("SIGKILL", 9), end: internal, failed: true},
			exitCase{name: "a signal from the transport, from a kind that does not report signals", exit: exec.Exit{Signal: "SIGKILL", SignalNumber: 9, Source: exec.SourceTransport}, end: internal, failed: true})
	}
	return cases
}

// caseWireExits: the exit of a function reaches the host as what the kind declared it can report. An exit code arrives exactly as given (a
// Windows status above 32 bits, and one given as a negative 32-bit number, are carried whole) from the source that the kind can vouch for: the
// process's own (a code that is not 0 from a status is PROCESS), for a kind that reports a status; the transport's, for one that does not. A
// code the target folded from a death by signal says so (SIGNAL_FOLDED) and a code the transport relayed says that: Exited{0, TRANSPORT} is a
// legitimate end (client-go's "the stream ended normally" relays no status of the process, and says so), its start is observed and the survival
// is not known; a break of the stream is Lost, with no code; a signal arrives as one only for a kind that reports signals, with a name or a
// number that the wire can carry, and from any other kind it is a failure of the target (INTERNAL), never a code. An Exit that says nothing or
// says two things is a failure and never an exit 0. The completeness follows the source: COMPLETE for a status of the process, UNSPECIFIED for
// the transport's word and for a break.
func caseWireExits(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	info := target.Info
	if info.Session.IsZero() {
		t.Skipf(noSessions)
	}
	rig := newWireRig(t, env, target)
	for _, ec := range exitCases(info.Capabilities) {
		steps := []Step{EndWith(ec.exit)}
		if ec.output != "" {
			steps = []Step{WriteOut(ec.output), EndWith(ec.exit)}
		}
		// The start is observed when the command wrote, and when it ended with a status (of the process or of the transport); a failure of the
		// target and a break that came before anything was written have none.
		started := ec.output != "" || !ec.failed && !ec.lost
		ended := rig.playEnd(endScenario{name: ec.name, steps: steps, end: ec.end, started: started, output: ec.output})
		if ec.failed && ended.GetEnd().GetFailed() == nil {
			t.Errorf("%s: the end is %s, want a failure of the target", ec.name, armText(ended.GetEnd()))
		}
		if ec.lost && ended.GetEnd().GetLost() == nil {
			t.Errorf("%s: the end is %s, want Lost: a stream that ended with no status is never an exit", ec.name, armText(ended.GetEnd()))
		}
	}
}

// caseExitViolations: in process, an Exit that the kind cannot give is a contract violation of the function: the engine records it (the
// violation "exit_shape", which it logs and counts) and reports the safe end. A signal from a kind that does not report signals, a status of
// the process from a kind that reports none, an Exit that says nothing and a status that claims the SDK's own source are violations; a status
// that the kind can give, and a signal from a kind that reports them, are not.
func caseExitViolations(t checkT, target exec.Target, env *inProcessEnv) {
	t.Helper()
	info := target.Info
	if !servesCommands(info) {
		t.Skipf("the target declares neither sessions nor runs: no command runs in it")
	}
	caps := info.Capabilities
	process := caps.ExitReport != exec.ExitReportNone
	type scenario struct {
		name      string
		exit      exec.Exit
		violation bool
	}
	scenarios := []scenario{
		{"the process's own status", exec.ExitCode(0), !process},
		{"a code folded from a death by signal", exec.ExitFolded(137), !process},
		{"a code the transport relayed", exec.ExitTransport(1), false},
		{"a stream that ended with no status", exec.ExitLost(exec.LostNoStatus), false},
		{"an Exit that says nothing", exec.Exit{}, true},
		{"a status that claims the SDK's own source", exec.Exit{Code: new(int64(2)), Source: exec.SourceHost}, true},
		{"a signal", exec.ExitSignal("SIGKILL", 9), !reportsSignals(caps)},
	}
	for _, sc := range scenarios {
		rig := newCaseRig(t, env, target, EndWith(sc.exit))
		res := rig.await(rig.mustStart(nil))
		var found []string
		for _, v := range res.Violations {
			if strings.HasPrefix(v, "exit_shape: ") {
				found = append(found, v)
			}
		}
		switch {
		case sc.violation && len(found) != 1:
			t.Errorf("%s from a kind that declares ExitReport %s: %d exit violations %q, want one: the author broke the contract of Exit and the engine says so", sc.name, caps.ExitReport, len(found), res.Violations)
		case !sc.violation && len(res.Violations) != 0:
			t.Errorf("%s from a kind that declares ExitReport %s: violations %q, want none: it is an Exit the kind can give", sc.name, caps.ExitReport, res.Violations)
		}
		rig.settle()
	}
}
