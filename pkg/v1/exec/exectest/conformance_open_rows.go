package exectest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The wire cases about how a session ends and what becomes of its stream: case 9 (every kind of end is typed, a session has one, and
// nothing follows it; and the end of a session that ended while no stream was attached, which is the tombstone's), case 14 (a retired
// connection ends its sessions), case 25 (every stream ends with exactly one of Ended or Detached, and an Ended never goes to a stream
// that left), case 28 (remote_may_survive per kind of end) and the pair test P5 (ending twice, after the exit and during the start give
// the same end). Each runs scripts of its own on the fake of the declared target, over the real transport, and holds what the host
// reads to what the script did: the oracle is the script and the declaration, never what the plugin answered.
//
// A session outlives its stream, so a session ends in one of two places: on the stream that is attached when it ends, as an Ended frame
// (the attached half of case 9 and case 14), or, when no stream is attached, nowhere on a stream at all: its end is the tombstone's,
// which the host reads with ListSessions (the detached half of case 9, in conformance_open_rows_tombstone.go). The streams of one session,
// and what each of them is told as it leaves, are case 25's (conformance_open_rows_streams.go).

// ---------------------------------------------------------------------------------------------------------------
// one session that ends one way
// ---------------------------------------------------------------------------------------------------------------

// endScenario is a session of the cases that are about its end: the function it runs, what the host does to it, and the end the host
// must be told.
type endScenario struct {
	name string
	// steps are the function of the session.
	steps []Step
	// input is what the host sends once the session is open, in frames of two bytes: the frames are not the bytes.
	input []byte
	// drive does what the case does to the session once it is open and has its input; nil leaves it to end by itself. A session that
	// writes "up" first is running once the host has read it.
	drive func(r *wireRig, s *wireSession)
	// fresh says that the scenario ends the connection or the plugin, so it runs on a rig of its own.
	fresh bool
	// end, started and output are what the host must be told: the end, whether the start was observed, and exactly the bytes that
	// the function wrote. A scenario with no end is not held to them: the case that runs it reads the Ended itself.
	end     *execv2.End
	started bool
	output  string
	// reports says that the function calls Proc.Started: the host then has a Started frame even if the function writes nothing.
	reports bool
	// mods change the start request: a selection, for the cases about the choices of a target.
	mods []func(*execv2.Start)
	// detached is how the same scenario runs when the host leaves the session before it ends, so that no stream is attached when it does
	// and the end is read from the tombstone; nil is a scenario that only runs with the stream attached. detachedOnly is a scenario
	// that cannot run attached: the lease that runs out ends a session that nobody holds, and no Ended frame can carry it.
	detached     *detachment
	detachedOnly bool
}

// afterUp reads what a running session writes first, "up", which says that its function runs, and then does what ends the session.
func afterUp(end func(r *wireRig, s *wireSession)) func(*wireRig, *wireSession) {
	return func(r *wireRig, s *wireSession) {
		s.readBytes(len("up"))
		end(r, s)
	}
}

// closeWith is what ends a session that the host closes: one Close frame for each mode, in order. A stream that has ended already
// refuses the frame, which is no failure of the host.
func closeWith(modes ...execv2.CloseMode) func(*wireRig, *wireSession) {
	return func(_ *wireRig, s *wireSession) {
		for _, mode := range modes {
			_ = s.closeSession(mode)
		}
	}
}

// shutdown stops the plugin as the controller's Shutdown does: it ends every session, and returns once each attached stream has its
// Ended handed over.
func (r *wireRig) shutdown() {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), caseBound)
	defer cancel()
	if err := r.server.plugin.Shutdown(ctx); err != nil {
		r.t.Errorf("shut the plugin down: %v", err)
	}
}

// playEnd opens the session of a scenario, sends it its input, does what the scenario does to it, reads it to its Ended and holds the
// Ended to what the scenario expects: the end, the counts, the completeness, the bytes that were written and the one Started that
// precedes them. It returns the Ended.
func (r *wireRig) playEnd(sc endScenario) *execv2.Ended {
	r.t.Helper()
	_, ended := r.playSession(sc)
	return ended
}

// playSession is playEnd that also returns the session, whose Opened and Started the case may read.
func (r *wireRig) playSession(sc endScenario) (*wireSession, *execv2.Ended) {
	r.t.Helper()
	script := r.newID("script")
	r.script(script, sc.steps...)
	s := r.mustOpen(r.newID("end"), script, sc.mods...)
	audit := s.audit(nil)
	audit.keep = true
	for i := 0; i < len(sc.input); i += 2 {
		s.mustInput(sc.input[i:min(i+2, len(sc.input))])
	}
	if sc.drive != nil {
		sc.drive(r, s)
	}
	ended := s.finish()
	if sc.end == nil {
		return s, ended
	}
	s.checkEnded(sc.name, ended, wantEnded{end: sc.end, started: sc.started, bytesIn: uint64(len(sc.input))})
	if got := string(audit.data); got != sc.output {
		r.t.Errorf("%s: the output is %q, want exactly what the function wrote, %q", sc.name, got, sc.output)
	}
	if want := boolIndex(sc.output != "" || sc.reports); s.grammar.started != want {
		r.t.Errorf("%s: %d Started frames for a function that wrote %d bytes and reported its start: %v, want %d: one precedes the first output, one follows a report, and none comes without either",
			sc.name, s.grammar.started, len(sc.output), sc.reports, want)
	}
	return s, ended
}

// endScenarios are the ways a session ends, for the declared kind: a status of the process (an exit with and without output, one that
// follows the input the host sent), a code that only the transport relayed, a stream that ended with no status, a link that died, a
// failure before and after the start, a close by the host, graceful and kill, a signal where the kind reports signals, and, on a rig of
// their own, a retired connection and the plugin's shutdown; and, for a session that has no stream, the lease that runs out, for the Kill
// of the kind and for each narrower Kill that a session of it may report in Started. Each also runs with the host gone: the host leaves
// the session, and it ends with nobody attached.
func endScenarios(caps exec.Capabilities) []endScenario {
	exit0, end0 := processExit(caps, 0)
	exit3, end3 := processExit(caps, 3)
	closed := func(by execv2.ClosedBy) *execv2.End { return endClosed(by, caps.Kill) }
	running := []Step{WriteOut("up"), Block()}
	graceful, kill, unnamed := execv2.CloseMode_CLOSE_MODE_GRACEFUL, execv2.CloseMode_CLOSE_MODE_KILL, execv2.CloseMode_CLOSE_MODE_UNSPECIFIED
	// With the host gone, a function that ends by itself is held until it has left, and a function that runs is ended by what ends it.
	itself := &detachment{}
	then := func(end func(*wireRig, *wireSession)) *detachment { return &detachment{runs: true, end: end} }
	scenarios := []endScenario{
		{name: "exit 0", steps: []Step{EndWith(exit0)}, end: end0, started: true, detached: itself},
		{name: "exit n with output", steps: []Step{WriteOut("out"), EndWith(exit3)}, end: end3, started: true, output: "out", detached: itself},
		{name: "input and output counted exactly", steps: []Step{WaitInput(5), WriteOut("hello"), EndWith(exit0)}, input: []byte("12345"), end: end0, started: true, output: "hello", detached: itself},
		{name: "a code only the transport relayed", steps: []Step{EndWith(exec.ExitTransport(0))}, end: endExited(0, execv2.ExitSource_EXIT_SOURCE_TRANSPORT, nil), started: true, detached: itself},
		{name: "a stream that ended with no status", steps: []Step{EndWith(exec.ExitLost(exec.LostNoStatus))}, end: endLost(execv2.LostCause_LOST_CAUSE_NO_STATUS), detached: itself},
		{name: "a link that died after output", steps: []Step{WriteOut("tail"), EndWith(exec.ExitLost(exec.LostLinkDead))},
			end: endLost(execv2.LostCause_LOST_CAUSE_LINK_DEAD), started: true, output: "tail", detached: itself},
		{name: "a failure at the start", steps: []Step{Fail(&exec.Error{Class: exec.ErrorClassNotFound, Reason: exec.ErrorReasonNoSuchExecutable})},
			end: endFailed(execv2.ErrorClass_ERROR_CLASS_NOT_FOUND, execv2.ErrorReason_ERROR_REASON_NO_SUCH_EXECUTABLE, execv2.FailureStage_FAILURE_STAGE_START), detached: itself},
		{name: "a failure in the middle of the run", steps: []Step{WriteOut("x"), Fail(&exec.Error{Class: exec.ErrorClassUnavailable, Reason: exec.ErrorReasonNotRunning})},
			end: endFailed(execv2.ErrorClass_ERROR_CLASS_UNAVAILABLE, execv2.ErrorReason_ERROR_REASON_NOT_RUNNING, execv2.FailureStage_FAILURE_STAGE_RUN), started: true, output: "x", detached: itself},
		{name: "a graceful close by the host", steps: running, drive: afterUp(closeWith(graceful)),
			end: closed(execv2.ClosedBy_CLOSED_BY_REQUEST), started: true, output: "up", detached: then(endsSession(graceful))},
		{name: "a kill by the host", steps: running, drive: afterUp(closeWith(kill)),
			end: closed(execv2.ClosedBy_CLOSED_BY_REQUEST), started: true, output: "up", detached: then(endsSession(kill))},
		{name: "a close that names no mode is graceful", steps: running, drive: afterUp(closeWith(unnamed)),
			end: closed(execv2.ClosedBy_CLOSED_BY_REQUEST), started: true, output: "up", detached: then(endsSession(unnamed))},
		{name: "a close by the host of a function that is slow to wind down", steps: []Step{WriteOut("up"), windsDown(50 * time.Millisecond)}, drive: afterUp(closeWith(graceful)),
			end: closed(execv2.ClosedBy_CLOSED_BY_REQUEST), started: true, output: "up", detached: then(endsSession(graceful))},
		{name: "a retired connection", fresh: true, steps: running, drive: afterUp(retires),
			end: closed(execv2.ClosedBy_CLOSED_BY_CONNECTION_RETIRED), started: true, output: "up", detached: then(retires)},
		{name: "the plugin's shutdown", fresh: true, steps: running, drive: afterUp(shutsDown),
			end: closed(execv2.ClosedBy_CLOSED_BY_PLUGIN_SHUTDOWN), started: true, output: "up", detached: then(shutsDown)},
		// The lease ends a session that nobody holds, so it has no stream to carry an Ended: its end is only the tombstone's.
		{name: "a lease that ran out", steps: running, end: closed(execv2.ClosedBy_CLOSED_BY_DETACH_TIMEOUT), started: true, output: "up",
			detachedOnly: true, detached: &detachment{runs: true, leaseMs: 1}},
	}
	// A session that ran out of lease says whether the process may survive as its own Kill says, which the session may have narrowed in
	// Started: a kind that terminates a session's process on a close, and a session of it that reports it only closes its stream.
	for _, k := range movesKill {
		if slices.Index(movesKill, k) > slices.Index(movesKill, caps.Kill) {
			continue
		}
		narrowed := cloneCapabilities(caps)
		narrowed.Kill = k
		scenarios = append(scenarios, endScenario{
			name:  fmt.Sprintf("a lease that ran out, on a session of the Kill %s", k),
			steps: []Step{ReportStarted(exec.SessionFacts{Capabilities: &narrowed}), WriteOut("up"), Block()},
			end:   endClosed(execv2.ClosedBy_CLOSED_BY_DETACH_TIMEOUT, k), started: true, output: "up", reports: true,
			detachedOnly: true, detached: &detachment{runs: true, leaseMs: 1},
		})
	}
	if reportsSignals(caps) {
		scenarios = append(scenarios, endScenario{name: "a signal", steps: []Step{EndWith(exec.ExitSignal("SIGKILL", 9))},
			end: endSignaled("SIGKILL", 9, new(false)), started: true, detached: itself})
	}
	return scenarios
}

// runEnds plays every scenario on the rig, and on a rig of its own for the ones that end the connection or the plugin.
func runEnds(t checkT, env *wireEnv, target exec.Target, scenarios []endScenario, play func(r *wireRig, sc endScenario)) {
	t.Helper()
	shared := newWireRig(t, env, target)
	for _, sc := range scenarios {
		rig := shared
		if sc.fresh {
			rig = newWireRig(t, env, target)
		}
		play(rig, sc)
	}
}

// ---------------------------------------------------------------------------------------------------------------
// case 9: every kind of end is typed, a session has one, and nothing follows it
// ---------------------------------------------------------------------------------------------------------------

// caseWireEnds: a session has exactly one end, and it is read in the one place the session's stream says it goes. The end is one of the
// five ways the contract names, each typed as the declaration says. With a stream attached when the session ends, the Ended is the last
// frame of that stream, which then ends with status OK, and the tombstone says the same end. With no stream attached (the host detached,
// or its stream broke, and then the session ended) there is no Ended frame anywhere, and the end is the tombstone's, which the host reads
// with ListSessions (ended_with) and its output with ReadOutput. The kinds are an exit with and without output, one that follows the input
// the host sent, a code that only the transport relayed, a stream that ended with no status, a link that died, a failure before the start
// was observed and one after, a close by the host (graceful, kill, and one that names no mode; for a session with no stream, EndSession),
// a signal for a kind that reports signals, a retired connection, the plugin's shutdown and, for a session with no stream only, a lease
// that ran out. The counts are exact (bytes_out is what the function wrote, bytes_in what the host sent, last_offset is bytes_out), the
// completeness is the one the end allows, start_observed is the contract's, remote_may_survive is what the declared Kill makes of the end
// whoever ended the session, and the bytes are exactly the function's.
func caseWireEnds(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	if target.Info.Session.IsZero() {
		t.Skipf(noSessions)
	}
	scenarios := endScenarios(target.Info.Capabilities)
	runEnds(t, env, target, scenarios, func(r *wireRig, sc endScenario) {
		if !sc.detachedOnly {
			s, ended := r.playSession(sc)
			r.wantTombstoneIs(sc, s, ended)
		}
	})
	for _, how := range []leaving{leavesByDetach, leavesByBreak} {
		runEnds(t, env, target, scenarios, func(r *wireRig, sc endScenario) {
			if sc.detached != nil {
				r.playEndDetached(sc, how)
			}
		})
	}
}

// ---------------------------------------------------------------------------------------------------------------
// case 28: remote_may_survive per kind of end
// ---------------------------------------------------------------------------------------------------------------

// survivalScenario is an end, what remote_may_survive says of it, and whether the start was observed, which the host reads with it: a
// command that ended with a code that only the transport relayed has started (the transport said so), and a host that read it as unstarted
// would read the absent survival as "never ran" and not as "may".
type survivalScenario struct {
	endScenario
	arm string
	// by is who closed the session, for a closed end.
	by      execv2.ClosedBy
	want    *bool
	started bool
}

// caseWireSurvival: remote_may_survive says whether the process may still run, per kind of end, as the host reads it: true for a lost
// stream; false for the status of the process itself, an exit or a signal, and for a code the target folded from a death by signal;
// absent for a code that only the transport relayed (which has a start that was observed: client-go's "ended normally" is
// Exited{0, TRANSPORT}) and for a failure; and for a Closed end it follows the Kill of the session, whoever closed it (the host, with a
// graceful close or a kill, a retirement, the plugin's shutdown): false only for TERMINATES, present and true for BEST_EFFORT and for
// CLOSES_STREAM_ONLY, absent for a kind that declared nothing. The Kill of the session is the kind's, or a narrower one that the session
// reports in Started, so the case closes a session of each Kill that the kind allows, not only the kind's own: a target that cannot
// promise that ending a session ends the process (a Kubernetes exec closes its stream and the shell may keep running) is told so in
// every one of them. Absent is a value of its own, and it crosses the wire as absent.
func caseWireSurvival(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	info := target.Info
	if info.Session.IsZero() {
		t.Skipf(noSessions)
	}
	caps := info.Capabilities
	running := []Step{WriteOut("up"), Block()}
	graceful, kill := execv2.CloseMode_CLOSE_MODE_GRACEFUL, execv2.CloseMode_CLOSE_MODE_KILL
	request := execv2.ClosedBy_CLOSED_BY_REQUEST
	scenarios := []survivalScenario{
		{endScenario: endScenario{name: "a lost stream", steps: []Step{EndWith(exec.ExitLost(exec.LostNoStatus))}}, arm: "lost", want: new(true)},
		{endScenario: endScenario{name: "a link that gave up", steps: []Step{EndWith(exec.ExitLost(exec.LostLinkDead))}}, arm: "lost", want: new(true)},
		{endScenario: endScenario{name: "a lost stream after output", steps: []Step{WriteOut("tail"), EndWith(exec.ExitLost(exec.LostNoStatus))}}, arm: "lost", want: new(true), started: true},
		{endScenario: endScenario{name: "a code only the transport relayed", steps: []Step{EndWith(exec.ExitTransport(0))}}, arm: "exited", started: true},
		{endScenario: endScenario{name: "a code only the transport relayed, after output", steps: []Step{WriteOut("out"), EndWith(exec.ExitTransport(3))}}, arm: "exited", started: true},
		{endScenario: endScenario{name: "a failure", steps: []Step{Fail(&exec.Error{Class: exec.ErrorClassUnavailable, Reason: exec.ErrorReasonNotRunning})}}, arm: "failed"},
		{endScenario: endScenario{name: "a failure the SDK classifies", steps: []Step{Fail(errors.New("a failure of the target"))}}, arm: "failed"},
		{endScenario: endScenario{name: "a close by the host", steps: running, drive: afterUp(closeWith(graceful))}, arm: "closed", by: request, want: closeSurvival(caps.Kill), started: true},
		{endScenario: endScenario{name: "a kill by the host", steps: running, drive: afterUp(closeWith(kill))}, arm: "closed", by: request, want: closeSurvival(caps.Kill), started: true},
		{endScenario: endScenario{name: "a retired connection", fresh: true, steps: running, drive: afterUp(func(r *wireRig, _ *wireSession) { r.conns.Retire() })},
			arm: "closed", by: execv2.ClosedBy_CLOSED_BY_CONNECTION_RETIRED, want: closeSurvival(caps.Kill), started: true},
		{endScenario: endScenario{name: "the plugin's shutdown", fresh: true, steps: running, drive: afterUp(func(r *wireRig, _ *wireSession) { r.shutdown() })},
			arm: "closed", by: execv2.ClosedBy_CLOSED_BY_PLUGIN_SHUTDOWN, want: closeSurvival(caps.Kill), started: true},
	}
	// A session closed under each Kill that the kind allows: the kind's own, and every one below it, which the session reports in Started.
	// What a session claims above its kind is served the kind's (case 26), so a kind that declared nothing has none below it.
	for _, k := range movesKill {
		if slices.Index(movesKill, k) > slices.Index(movesKill, caps.Kill) {
			continue
		}
		narrowed := cloneCapabilities(caps)
		narrowed.Kill = k
		for _, ending := range []struct {
			how  string
			mode execv2.CloseMode
		}{{"a graceful close", graceful}, {"a kill", kill}} {
			scenarios = append(scenarios, survivalScenario{
				endScenario: endScenario{
					name:  fmt.Sprintf("a session of the Kill %s, ended by %s of the host", k, ending.how),
					steps: []Step{ReportStarted(exec.SessionFacts{Capabilities: &narrowed}), WriteOut("up"), Block()}, drive: afterUp(closeWith(ending.mode)), reports: true, output: "up",
				},
				arm: "closed", by: request, want: closeSurvival(k), started: true,
			})
		}
	}
	if caps.ExitReport != exec.ExitReportNone {
		scenarios = append(scenarios,
			survivalScenario{endScenario: endScenario{name: "the process's own exit", steps: []Step{EndWith(exec.ExitCode(0))}}, arm: "exited", want: new(false), started: true},
			survivalScenario{endScenario: endScenario{name: "the process's own exit, not 0", steps: []Step{EndWith(exec.ExitCode(3))}}, arm: "exited", want: new(false), started: true},
			survivalScenario{endScenario: endScenario{name: "a code folded from a death by signal", steps: []Step{EndWith(exec.ExitFolded(137))}}, arm: "exited", want: new(false), started: true})
	} else {
		t.Logf("no process-status scenario: the target declares ExitReport NONE, so any code it returns is the transport's word")
	}
	if reportsSignals(caps) {
		scenarios = append(scenarios, survivalScenario{endScenario: endScenario{name: "a signal", steps: []Step{EndWith(exec.ExitSignal("SIGTERM", 15))}}, arm: "signaled", want: new(false), started: true})
	}
	byName := map[string]survivalScenario{}
	plain := make([]endScenario, len(scenarios))
	for i, sc := range scenarios {
		plain[i] = sc.endScenario
		byName[sc.name] = sc
	}
	runEnds(t, env, target, plain, func(r *wireRig, sc endScenario) {
		want := byName[sc.name]
		ended := r.playEnd(sc)
		end := ended.GetEnd()
		if arm, _ := endArm(end); arm != want.arm {
			t.Errorf("%s: the end is %s, want %s", sc.name, armText(end), want.arm)
		}
		if want.by != execv2.ClosedBy_CLOSED_BY_UNSPECIFIED && end.GetClosed().GetBy() != want.by {
			t.Errorf("%s: the session is closed by %s, want %s", sc.name, end.GetClosed().GetBy(), want.by)
		}
		if got := end.RemoteMaySurvive; (got == nil) != (want.want == nil) || (got != nil && *got != *want.want) {
			t.Errorf("%s: remote_may_survive is %s, want %s (the declared Kill is %s, ExitReport %s)",
				sc.name, survivalText(got), survivalText(want.want), caps.Kill, caps.ExitReport)
		}
		if ended.GetStartObserved() != want.started {
			t.Errorf("%s: start_observed is %v, want %v: a host reads the survival of a command that started differently from one that never did", sc.name, ended.GetStartObserved(), want.started)
		}
	})
}

// ---------------------------------------------------------------------------------------------------------------
// pair P5: ending twice, after the exit and during the start give the same end
// ---------------------------------------------------------------------------------------------------------------

// sameEnded says how the Ended of a session differs from the one of the same session ended another way in anything but the clock
// (the end, whether the start was observed, the counts, the completeness and the mode reset), or returns "" when it does not.
func sameEnded(first, now *execv2.Ended) string {
	switch {
	case !proto.Equal(first.GetEnd(), now.GetEnd()):
		return fmt.Sprintf("the end is %s, it was %s", endText(now.GetEnd()), endText(first.GetEnd()))
	case first.GetStartObserved() != now.GetStartObserved():
		return fmt.Sprintf("start_observed is %v, it was %v", now.GetStartObserved(), first.GetStartObserved())
	case first.GetBytesOut() != now.GetBytesOut() || first.GetBytesIn() != now.GetBytesIn() || first.GetLastOffset() != now.GetLastOffset() || first.GetDroppedBytes() != now.GetDroppedBytes():
		return fmt.Sprintf("the counts are out %d in %d last_offset %d dropped %d, they were out %d in %d last_offset %d dropped %d",
			now.GetBytesOut(), now.GetBytesIn(), now.GetLastOffset(), now.GetDroppedBytes(), first.GetBytesOut(), first.GetBytesIn(), first.GetLastOffset(), first.GetDroppedBytes())
	case first.GetOutput() != now.GetOutput():
		return fmt.Sprintf("the output is %s, it was %s", now.GetOutput(), first.GetOutput())
	case (first.ModeReset == nil) != (now.ModeReset == nil) || !bytes.Equal(first.GetModeReset(), now.GetModeReset()):
		return fmt.Sprintf("mode_reset is %q, it was %q", now.GetModeReset(), first.GetModeReset())
	}
	return ""
}

// concurrently is what a host does that sends its Close frames from as many goroutines at once: it makes no call that ends a test on
// them, and waits for every frame to be taken, within the bound of a case.
func concurrently(modes ...execv2.CloseMode) func(*wireRig, *wireSession) {
	return func(r *wireRig, s *wireSession) {
		sent := make(chan struct{}, len(modes))
		for _, mode := range modes {
			go func() {
				defer func() { sent <- struct{}{} }()
				_ = s.conn.Send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Close{Close: &execv2.Close{Mode: mode}}})
			}()
		}
		for range modes {
			select {
			case <-sent:
			case <-time.After(caseBound):
				r.t.Fatalf("session %q: the plugin took no Close frame within %v", s.id, caseBound)
			}
		}
	}
}

// casePairWireEnding: a session ended once, ended twice (gracefully or with a kill, in either order, back to back or from two
// senders at once), ended during its start and ended after its function returned has one final end, and the same one as the end that
// was not repeated: ending again changes nothing. The Ended of each is held to the one of the session that was ended once.
func casePairWireEnding(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	info := target.Info
	if info.Session.IsZero() {
		t.Skipf(noSessions)
	}
	caps := info.Capabilities
	rig := newWireRig(t, env, target)
	graceful, kill := execv2.CloseMode_CLOSE_MODE_GRACEFUL, execv2.CloseMode_CLOSE_MODE_KILL
	wantClosed := endClosed(execv2.ClosedBy_CLOSED_BY_REQUEST, caps.Kill)
	running := []Step{WriteOut("up"), Block()}

	// Ended once is the end that every other way must give.
	once := rig.playEnd(endScenario{name: "ended once", steps: running, drive: afterUp(closeWith(graceful)), end: wantClosed, started: true, output: "up"})
	for _, tc := range []struct {
		name  string
		drive func(*wireRig, *wireSession)
	}{
		{"gracefully twice", closeWith(graceful, graceful)},
		{"with a kill twice", closeWith(kill, kill)},
		{"gracefully and then with a kill", closeWith(graceful, kill)},
		{"with a kill and then gracefully", closeWith(kill, graceful)},
		{"three times", closeWith(graceful, kill, graceful)},
		{"gracefully and with a kill from two senders at once", concurrently(graceful, kill)},
		{"twice from two senders at once", concurrently(graceful, graceful)},
	} {
		name := "ended " + tc.name
		got := rig.playEnd(endScenario{name: name, steps: running, drive: afterUp(tc.drive), end: wantClosed, started: true, output: "up"})
		if diff := sameEnded(once, got); diff != "" {
			t.Errorf("%s: %s: ending again changes nothing", name, diff)
		}
	}

	// Ended during the start: the function has begun and has written and reported nothing. The start was not observed, and nothing else
	// differs from the end of a session that had started.
	var during *execv2.Ended
	for _, tc := range []struct {
		name  string
		drive func(*wireRig, *wireSession)
	}{
		{"once", closeWith(graceful)}, {"with a kill", closeWith(kill)}, {"twice", closeWith(graceful, graceful)}, {"with a kill and then gracefully", closeWith(kill, graceful)},
		{"from two senders at once", concurrently(graceful, kill)},
	} {
		begun := NewGate()
		t.Cleanup(begun.Open)
		name := "ended during the start " + tc.name
		sc := endScenario{
			name: name, steps: []Step{opensGate(begun), Block()}, end: wantClosed,
			drive: func(r *wireRig, s *wireSession) {
				select {
				case <-begun.Opened():
				case <-time.After(caseBound):
					r.t.Fatalf("%s: the function did not begin within %v", name, caseBound)
				}
				tc.drive(r, s)
			},
		}
		got := rig.playEnd(sc)
		if during == nil {
			during = got
			if !proto.Equal(got.GetEnd(), once.GetEnd()) || got.GetStartObserved() || got.GetBytesOut() != 0 {
				t.Errorf("%s: the end is %s with start_observed %v and %d bytes out, want the end of a session ended once, with the start not observed and no bytes",
					name, endText(got.GetEnd()), got.GetStartObserved(), got.GetBytesOut())
			}
		} else if diff := sameEnded(during, got); diff != "" {
			t.Errorf("%s: %s: ending again changes nothing", name, diff)
		}
	}
	// Ended before the function began: the end is the same, however far the function got.
	raw := rig.playEnd(endScenario{name: "ended as soon as it opened", steps: []Step{Block()}, drive: closeWith(graceful), end: wantClosed})
	if raw.GetStartObserved() || raw.GetBytesOut() != 0 {
		t.Errorf("ended as soon as it opened: start_observed %v and %d bytes out for a function that wrote and reported nothing", raw.GetStartObserved(), raw.GetBytesOut())
	}

	// Ended after its function returned. The function has returned and its end is decided when the context it was given ends, which the
	// SDK does once the end is recorded; the host does not read, so the transport holds the end back, and it ends the session after that.
	exit7, end7 := processExit(caps, 7)
	total := 200 << 10
	for _, ending := range [][]execv2.CloseMode{nil, {graceful}, {kill}, {kill, graceful}, {graceful, kill}} {
		recorded := make(chan struct{})
		name := rig.newID("after-exit")
		rig.scriptFunc(name, func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			for offset := 0; offset < total; {
				n := min(32<<10, total-offset)
				if _, err := p.Stdout.Write(patternBytes(uint64(offset), n)); err != nil {
					return exec.Exit{}, err
				}
				offset += n
			}
			go func() { <-ctx.Done(); close(recorded) }()
			return exit7, nil
		})
		label := fmt.Sprintf("ended after its exit, by %d Close frames", len(ending))
		s := rig.mustOpenOn(rig.slow(), rig.newID("after-exit"), name)
		audit := s.audit(patternByte)
		select {
		case <-recorded:
		case <-time.After(caseBound):
			t.Fatalf("%s: the function did not return and have its end recorded within %v", label, caseBound)
		}
		closeWith(ending...)(rig, s)
		ended := s.finish()
		s.checkEnded(label, ended, wantEnded{end: end7, started: true})
		lossy := caps.Stall == exec.StallLossy
		if audit.read+audit.dropped != uint64(total) || (!lossy && audit.dropped != 0) {
			t.Errorf("%s: the host read %d bytes and was told %d dropped of the %d the function wrote: a Close after the exit loses no output", label, audit.read, audit.dropped, total)
		}
	}
}

// ---------------------------------------------------------------------------------------------------------------
// case 25: every stream ends with exactly one of Ended or Detached, and an Ended never goes to a stream that left
// ---------------------------------------------------------------------------------------------------------------

// caseWireOneEndPerStream: every stream that can still receive frames ends with exactly one of Ended or Detached, once, last, and then with
// status OK. Many sessions at once, each of its own kind of end, each on a stream of its own: every stream ends with its session's Ended,
// and the Ended is the one of the session of the stream (its end, its counts and its bytes are the ones its own function and its own host
// made), so an Ended that went to the wrong stream, or twice, or not at all, is seen, and no stream is told Detached, for no host left. A
// request that is refused is a status with no frame, and nothing follows it. Then the streams of one session, which outlives each of them
// (checkStreamsOfOneSession): a stream that detaches ends with Detached{REQUESTED} carrying the lease that was applied, a stream that another
// took the session from ends with Detached{SUPERSEDED} and no lease, the stream that holds the session when it ends is told Ended, and a
// session has at most one Ended whatever the order in which its hosts attach, detach, close and end it, and none when it ends while no stream
// is attached: its end is the tombstone's.
func caseWireOneEndPerStream(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	info := target.Info
	if info.Session.IsZero() {
		t.Skipf(noSessions)
	}
	caps := info.Capabilities
	rig := newWireRig(t, env, target)
	n := min(effectiveLimit(info), 12)
	if n < 2 {
		t.Logf("the target serves one session at a time: the sessions run one after the other")
	}
	type opened struct {
		s     *wireSession
		input []byte
		want  endScenario
	}
	var sessions []opened
	for round := range 2 {
		sessions = sessions[:0]
		for i := range n {
			exit, end := processExit(caps, int64(i))
			sc := endScenario{
				name:  fmt.Sprintf("session %d of round %d", i, round),
				steps: []Step{WaitInput(i + 1), WriteOut(strings.Repeat(string(rune('a'+i)), i+1))},
				input: bytes.Repeat([]byte{byte('0' + i%10)}, i+1), end: end, started: true, output: strings.Repeat(string(rune('a'+i)), i+1),
			}
			switch i % 4 {
			case 0:
				sc.steps = append(sc.steps, EndWith(exit))
			case 1:
				sc.steps = append(sc.steps, Block())
				sc.end = endClosed(execv2.ClosedBy_CLOSED_BY_REQUEST, caps.Kill)
			case 2:
				sc.steps = append(sc.steps, EndWith(exec.ExitLost(exec.LostNoStatus)))
				sc.end = endLost(execv2.LostCause_LOST_CAUSE_NO_STATUS)
			case 3:
				sc.steps = append(sc.steps, Fail(&exec.Error{Class: exec.ErrorClassUnavailable, Reason: exec.ErrorReasonNotRunning}))
				sc.end = endFailed(execv2.ErrorClass_ERROR_CLASS_UNAVAILABLE, execv2.ErrorReason_ERROR_REASON_NOT_RUNNING, execv2.FailureStage_FAILURE_STAGE_RUN)
			}
			script := rig.newID("script")
			rig.script(script, sc.steps...)
			s := rig.mustOpen(rig.newID("one-end"), script)
			audit := s.audit(nil)
			audit.keep = true
			sessions = append(sessions, opened{s: s, input: sc.input, want: sc})
			// The session that waits to be closed is closed once it has its input and has written: the host does not wait for the others.
		}
		// Every host sends its input, and the sessions run at once; the streams are then read one after the other, each to its end.
		for _, o := range sessions {
			for i := 0; i < len(o.input); i += 3 {
				o.s.mustInput(o.input[i:min(i+3, len(o.input))])
			}
		}
		for i, o := range sessions {
			if i%4 == 1 {
				o.s.readBytes(len(o.want.output))
				_ = o.s.closeSession(execv2.CloseMode_CLOSE_MODE_GRACEFUL)
			}
		}
		for _, o := range sessions {
			ended := o.s.finish()
			o.s.checkEnded(o.want.name, ended, wantEnded{end: o.want.end, started: true, bytesIn: uint64(len(o.input))})
			if got := string(o.s.out.data); got != o.want.output {
				t.Errorf("%s: the output is %q, want %q: the stream carries the output of its own session only", o.want.name, got, o.want.output)
			}
			if o.s.grammar.started != 1 {
				t.Errorf("%s: %d Started frames, want one", o.want.name, o.s.grammar.started)
			}
		}
		rig.await("every function of the round to return", func() bool { return rig.started.Load() == rig.returned.Load() })
	}

	// A request that is refused gives no frame at all: it is a status, and no Ended or Detached follows because none came before.
	ran := rig.started.Load()
	refused := rig.openRequest("", "unused")
	if frame, err := rig.rawFirst(refused); frame != nil || err == nil {
		t.Errorf("an Open that was refused gave %s and %v: a refusal is a status and no frame", frameName(frame), err)
	}
	if got := rig.started.Load(); got != ran {
		t.Errorf("the function ran %d times for a request that was refused", got-ran)
	}

	// The streams of one session: each ends with exactly one of Ended or Detached, and an Ended never goes to one that left.
	rig.checkStreamsOfOneSession()
}

// ---------------------------------------------------------------------------------------------------------------
// case 14: a retired connection ends its sessions
// ---------------------------------------------------------------------------------------------------------------

// retireOutcome is what one host goroutine saw of a session that it opened while the connection was retired.
type retireOutcome struct {
	id     string
	tty    bool
	opened bool
	// refusal is why the Open was refused, when it was.
	refusal error
	frames  []*execv2.ServerFrame
	// end is how the stream ended: io.EOF for a clean end.
	end error
}

// drainSession opens a session and reads its stream to its end, and says when the function has written. It makes no call that ends a
// test: it runs on a goroutine that is not the test's.
func drainSession(ctx context.Context, client exec.Client, req *execv2.OpenRequest, running chan<- struct{}) retireOutcome {
	out := retireOutcome{id: req.GetSessionId(), tty: req.GetStart().GetTty()}
	conn, err := client.Open(ctx, req)
	if err != nil {
		out.refusal = err
		return out
	}
	defer func() { _ = conn.Close() }()
	out.opened = true
	guard := time.AfterFunc(caseBound, func() { _ = conn.Close() })
	defer guard.Stop()
	signalled := false
	for {
		frame, err := conn.Recv()
		if err != nil {
			out.end = err
			return out
		}
		out.frames = append(out.frames, frame)
		if !signalled && frame.GetOutput() != nil {
			signalled = true
			select {
			case running <- struct{}{}:
			default:
			}
		}
	}
}

// isTypedRefusal says that err is a refusal that the host can read: one of the exec lane's, or the resource lane's for the connection.
func isTypedRefusal(err error) bool {
	var execErr *exec.Error
	var resourceErr *resourcev2.ResourceOperationError
	return errors.As(err, &execErr) || errors.As(err, &resourceErr)
}

// retiredProblems holds what a host saw of a session opened while the connection was retired to the contract: a refusal is typed and
// no frame came with it; an admitted session ends with one Ended, last, that says Closed by CONNECTION_RETIRED with exact counts, and
// then with status OK.
func retiredProblems(o retireOutcome, caps exec.Capabilities) []string {
	var found []string
	add := func(format string, args ...any) {
		found = append(found, fmt.Sprintf("session %q: "+format, append([]any{o.id}, args...)...))
	}
	if !o.opened {
		if !isTypedRefusal(o.refusal) {
			add("the Open was refused with %v, which is no refusal of the exec lane or of the connection", o.refusal)
		}
		return found
	}
	if !errors.Is(o.end, io.EOF) {
		add("the stream ended with %v, want a clean end after Ended", o.end)
	}
	var grammar streamGrammar
	var delivered uint64
	var ended *execv2.Ended
	for _, frame := range o.frames {
		grammar.take(frame)
		delivered += uint64(len(frame.GetOutput().GetData()))
		if e := frame.GetEnded(); e != nil {
			ended = e
		}
	}
	found = append(found, grammar.problems...)
	if ended == nil {
		return append(found, fmt.Sprintf("session %q: the stream had no Ended after %d frames", o.id, len(o.frames)))
	}
	if last := o.frames[len(o.frames)-1]; last.GetEnded() == nil {
		add("the last frame is %s, want Ended", frameName(last))
	}
	want := wantEnded{end: endClosed(execv2.ClosedBy_CLOSED_BY_CONNECTION_RETIRED, caps.Kill), started: delivered > 0, bytesOut: delivered}
	for _, problem := range endedProblems(ended, o.tty, want, nil) {
		add("%s", problem)
	}
	return found
}

// caseWireRetired: retiring the connection ends every session that runs through it, whatever stage its start had reached. Each stream
// is told Closed by CONNECTION_RETIRED, in an Ended that is its last frame, and every function has returned by the time the retirement
// is over; an Open that the retirement came before is refused with a typed status and no frame, and so is one that arrives after it; and
// a retirement that comes again changes nothing.
func caseWireRetired(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	info := target.Info
	if info.Session.IsZero() {
		t.Skipf(noSessions)
	}
	openers := min(effectiveLimit(info), 4)

	// Retired while every session runs, with the host reading each stream.
	rig := newWireRig(t, env, target)
	rig.script("hold", WriteOut("up"), Block())
	var running []*wireSession
	for range openers {
		running = append(running, rig.mustOpen(rig.newID("retire"), "hold"))
	}
	for _, s := range running {
		s.readBytes(len("up"))
	}
	rig.conns.Retire()
	for i, s := range running {
		ended := s.finish()
		name := fmt.Sprintf("session %d of %d", i+1, openers)
		s.checkEnded(name, ended, wantEnded{end: endClosed(execv2.ClosedBy_CLOSED_BY_CONNECTION_RETIRED, info.Capabilities.Kill), started: true, bytesOut: uint64(len("up"))})
	}
	if started, returned := rig.started.Load(), rig.returned.Load(); started != int64(openers) || returned != started {
		t.Errorf("%d functions ran and %d had returned when the retirement and every Ended were done: the function must have returned", started, returned)
	}
	// An Open on the retired connection is refused, with no frame, and the function never runs.
	before := rig.started.Load()
	if frame, err := rig.rawFirst(rig.openRequest(rig.newID("after"), "hold")); frame != nil || err == nil {
		t.Errorf("an Open on a retired connection gave %s and %v: a refusal is a status and no frame", frameName(frame), err)
	}
	if s, err := rig.open(rig.newID("after"), "hold"); err == nil {
		_ = s.closeSession(execv2.CloseMode_CLOSE_MODE_KILL)
		t.Errorf("a session was opened on a retired connection")
	} else if !isTypedRefusal(err) {
		t.Errorf("an Open on a retired connection was refused with %v, which is no typed refusal", err)
	}
	if got := rig.started.Load(); got != before {
		t.Errorf("the function ran %d times for an Open on a retired connection", got-before)
	}
	rig.conns.Retire() // again: nothing is left to end, and it returns

	// Retired at every stage of a start: some of the Opens are before the retirement, some are in it, some come after it. Each either
	// is refused with a typed status or is a session that the retirement ends, and no function is left running.
	for after := range openers + 1 {
		rig := newWireRig(t, env, target)
		rig.script("hold", WriteOut("up"), Block())
		requests := make([]*execv2.OpenRequest, openers)
		for i := range requests {
			requests[i] = rig.openRequest(rig.newID("stage"), "hold")
		}
		up := make(chan struct{}, openers)
		outcomes := make(chan retireOutcome, openers)
		ctx := rig.request()
		for _, req := range requests {
			go func() { outcomes <- drainSession(ctx, rig.client, req, up) }()
		}
		for wrote := range after {
			select {
			case <-up:
			case <-time.After(caseBound):
				t.Fatalf("retired after %d of %d sessions ran: only %d had written within %v", after, openers, wrote, caseBound)
			}
		}
		rig.conns.Retire()
		admitted := 0
		for range openers {
			select {
			case o := <-outcomes:
				if o.opened {
					admitted++
				}
				for _, problem := range retiredProblems(o, info.Capabilities) {
					t.Errorf("retired after %d of %d sessions ran: %s", after, openers, problem)
				}
			case <-time.After(caseBound):
				t.Fatalf("retired after %d of %d sessions ran: a session was neither refused nor ended within %v", after, openers, caseBound)
			}
		}
		if started, returned := rig.started.Load(), rig.returned.Load(); returned != started || started > int64(admitted) {
			t.Errorf("retired after %d of %d sessions ran: %d functions ran for %d admitted sessions and %d had returned: every function returns, and none runs for a refusal",
				after, openers, started, admitted, returned)
		}
	}
}
