package exectest

import (
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The wire cases about how a session ends, its limits, its errors, its exit, its link and its capabilities drive a plugin over a
// transport, so the tests of the suite hold them to two things, as the bodies of the bounded run are: each body passes on every shape of
// declaration that an author may write, and fails when the plugin is wrong. The wrongness is a faulty plugin, the real one served as it
// is with one fault added at the edge where the host reads it, given to the body through the serve seam of the wire environment, and
// the real SDK with one line of it changed (the mutants that the tests of this file were run against are listed in the change that
// added them).

// openRowBody is the wire body of a case about the interactive session, by what the tests call it.
type openRowBody struct {
	name string
	body func(t checkT, target exec.Target, env *wireEnv)
}

func openRowBodies() []openRowBody {
	return []openRowBody{
		{"case 09", caseWireEnds},
		{"case 12", caseWireLimits},
		{"case 13", caseWireErrorsOpen},
		{"case 14", caseWireRetired},
		{"case 16", caseWireLostOpen},
		{"case 17", caseWireExits},
		{"case 23", caseWireLink},
		{"case 25", caseWireOneEndPerStream},
		{"case 26", caseWireNarrowing},
		{"case 28", caseWireSurvival},
		{"pair P5", casePairWireEnding},
	}
}

func openRowBodyNamed(t *testing.T, name string) func(checkT, exec.Target, *wireEnv) {
	t.Helper()
	for _, b := range openRowBodies() {
		if b.name == name {
			return b.body
		}
	}
	t.Fatalf("no body %q", name)
	return nil
}

// openShapes are the shapes of declaration that the bodies of the interactive session are run against: the ones of the suite, and a kind
// that reports the health of its link, which is what the link case of the wire has something to say about.
func openShapes() []shape {
	return append(shapes(), shape{"a target that reports the health of its link", func() exec.TargetInfo {
		info := NewFakeTarget().Info
		info.Provider, info.Type = "link", "hosts"
		info.Capabilities.LinkHealth = true
		return info
	}})
}

func TestEveryOpenRowBodyPassesOnEveryShapeAnAuthorDeclaresAndNeverCallsTheirExec(t *testing.T) {
	var calls atomic.Int64
	for _, s := range openShapes() {
		info := s.info()
		for _, b := range openRowBodies() {
			t.Run(s.name+"/"+b.name, func(t *testing.T) {
				rec := &recordT{}
				rec.run(func(r checkT) { b.body(r, declared(t, s, &calls), &wireEnv{resource: "thing-1"}) })
				if info.Session.IsZero() {
					if len(rec.skips) != 1 || len(rec.failed()) != 0 {
						t.Errorf("a target that serves no sessions: %d skips and failures %q, want one skip and no failure", len(rec.skips), rec.failed())
					}
					return
				}
				if len(rec.skips) != 0 {
					t.Errorf("the body skipped: %q: a target that declares sessions has something to run", rec.skips)
				}
				if failures := rec.failed(); len(failures) != 0 {
					t.Errorf("the real plugin fails the body for a target declared as %s: %q", s.name, failures)
				}
			})
		}
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("the author's Exec was called %d times: the cases run a fake built from what the author declared", got)
	}
}

// The rows about the interactive session are written, including the halves that need a session that outlives its stream: case 9 (every kind
// of end, and the end of a session that ended while no stream was attached, which is the tombstone's), 12 (limits), 13 and 16 (their Open
// part), 14 (a retired connection), 17 (the exit), 23 (the link), 25 (every stream ends with exactly one of Ended or Detached, and an Ended
// never goes to a stream that left), 26 (capabilities narrow per session), 28 (remote_may_survive) and the pair P5. Each drives
// FEATURE_SESSION (13 and 16 FEATURE_RUN as well, for their Run part), and 9 and 25 FEATURE_SESSION_MANAGEMENT as well: their wire bodies
// call ListSessions, ReadOutput, EndSession, Detach and an attach, which a build that does not list it does not serve. None has a step
// left, so a build that lists the feature runs them and does not fail them for being unwritten (see conclude).
func TestTheRowsOfTheInteractiveSessionAreWrittenAndNothingIsLeftOfThem(t *testing.T) {
	session, run, management := execv2.Feature_FEATURE_SESSION, execv2.Feature_FEATURE_RUN, execv2.Feature_FEATURE_SESSION_MANAGEMENT
	none := execv2.Feature_FEATURE_UNSPECIFIED
	byName := func(name string) engineCase {
		for _, c := range engineCases() {
			if c.name == name {
				return c
			}
		}
		t.Fatalf("the skeleton has no case %q", name)
		return engineCase{}
	}
	for _, want := range []struct {
		c        engineCase
		requires execv2.Feature // what the row records of the feature its body needs beyond the session; the row has no step, so it makes no build list it
		needs    []execv2.Feature
	}{
		{caseNumbered(t, 9), management, []execv2.Feature{session, management}},
		{caseNumbered(t, 12), none, []execv2.Feature{session}},
		{caseNumbered(t, 13), none, []execv2.Feature{session, run}},
		{caseNumbered(t, 14), none, []execv2.Feature{session}},
		{caseNumbered(t, 16), none, []execv2.Feature{session, run}},
		{caseNumbered(t, 17), none, []execv2.Feature{session}},
		{caseNumbered(t, 23), none, []execv2.Feature{session}},
		{caseNumbered(t, 25), management, []execv2.Feature{session, management}},
		{caseNumbered(t, 26), none, []execv2.Feature{session}},
		{caseNumbered(t, 28), none, []execv2.Feature{session}},
		{byName(pairEndingName), none, []execv2.Feature{session}},
	} {
		c := want.c
		if c.wire == nil {
			t.Errorf("case %d (%q) has no wire body", c.number, c.name)
		}
		if c.step != "" || c.requires != want.requires {
			t.Errorf("case %d (%q) is filled by step %q and records the feature %s, want no step and %s: nothing is left of it", c.number, c.name, c.step, c.requires, want.requires)
		}
		if got := c.wireFeatures(); !slices.Equal(got, want.needs) {
			t.Errorf("case %d (%q) needs %v for its wire body, want %v", c.number, c.name, got, want.needs)
		}
		// A build that lists FEATURE_SESSION_MANAGEMENT runs a row that is complete, and fails neither it nor skips it.
		var done concluded
		c.conclude(&done, []execv2.Feature{session, management})
		if len(done.fatal) != 0 || len(done.skip) != 0 {
			t.Errorf("case %d (%q) in a build that lists %s: %d failures and %d skips, want neither: it is complete", c.number, c.name, management, len(done.fatal), len(done.skip))
		}
	}
	// The rows that the wire bodies complete are the ones with a body in process as well: the wire part was all that was left of each.
	for _, number := range []int{14, 17, 28} {
		if c := caseNumbered(t, number); c.inProcess == nil || c.wire == nil || c.step != "" {
			t.Errorf("case %d runs in process and over the wire and has nothing left: in process %v, over the wire %v, step %q", number, c.inProcess != nil, c.wire != nil, c.step)
		}
	}
	// Case 9 has a body in process too, for what a command that was never a session can say; the tombstone and the streams that left are the wire's.
	if c := caseNumbered(t, 9); c.inProcess == nil || c.wire == nil {
		t.Errorf("case 9 runs in process and over the wire: in process %v, over the wire %v", c.inProcess != nil, c.wire != nil)
	}
}

// closedAtKill is what the host read of a session that was closed: the Kill that its Started carried, and what its Ended said of the
// process.
type closedAtKill struct {
	kill    execv2.KillBehavior
	survive *bool
}

// closeRecorder is a fault that changes nothing and records, for each stream, the Kill of its Started and the survival of its closed Ended.
type closeRecorder struct {
	mu   sync.Mutex
	seen []closedAtKill
}

func (r *closeRecorder) client() faultyOpenClient {
	return faultyOpenClient{perStream: func() func(*execv2.ServerFrame) []*execv2.ServerFrame {
		var kill execv2.KillBehavior
		return func(f *execv2.ServerFrame) []*execv2.ServerFrame {
			if started := f.GetStarted(); started != nil {
				kill = started.GetFacts().GetCapabilities().GetKill()
			}
			if end := f.GetEnded().GetEnd(); end.GetClosed() != nil {
				r.mu.Lock()
				r.seen = append(r.seen, closedAtKill{kill: kill, survive: end.RemoteMaySurvive})
				r.mu.Unlock()
			}
			return []*execv2.ServerFrame{f}
		}
	}}
}

// Case 28 closes a session of each Kill that the kind allows, not only the kind's own, and tells a Close and a kill from the host the same:
// a kind that cannot promise that ending a session ends the process (CLOSES_STREAM_ONLY, a Kubernetes exec) has remote_may_survive present
// and true, whoever closed it; one that terminates it, false; BEST_EFFORT, true. A session cannot claim a Kill above its kind, so each kind
// has the values up to its own.
func TestCaseTwentyEightClosesASessionOfEveryKillTheKindAllowsAndReadsItsSurvival(t *testing.T) {
	shapesOf := openShapes()
	for _, tc := range []struct {
		shape int
		want  map[execv2.KillBehavior]*bool // the survival the host must read of a closed session, by the Kill of its Started
	}{
		{0, map[execv2.KillBehavior]*bool{
			execv2.KillBehavior_KILL_BEHAVIOR_CLOSES_STREAM_ONLY: new(true),
			execv2.KillBehavior_KILL_BEHAVIOR_BEST_EFFORT:        new(true),
			execv2.KillBehavior_KILL_BEHAVIOR_TERMINATES:         new(false),
		}},
		{2, map[execv2.KillBehavior]*bool{execv2.KillBehavior_KILL_BEHAVIOR_CLOSES_STREAM_ONLY: new(true)}},
		{3, map[execv2.KillBehavior]*bool{
			execv2.KillBehavior_KILL_BEHAVIOR_CLOSES_STREAM_ONLY: new(true),
			execv2.KillBehavior_KILL_BEHAVIOR_BEST_EFFORT:        new(true),
		}},
	} {
		t.Run(shapesOf[tc.shape].name, func(t *testing.T) {
			var calls atomic.Int64
			var recorder closeRecorder
			rec := &recordT{}
			env := &wireEnv{resource: "thing-1", serve: serveFaultyOpen(recorder.client(), nil)}
			rec.run(func(r checkT) { caseWireSurvival(r, declared(t, shapesOf[tc.shape], &calls), env) })
			if failures := rec.failed(); len(failures) != 0 || len(rec.skips) != 0 {
				t.Fatalf("the body fails or skips on the real plugin: %q %q", failures, rec.skips)
			}
			got := map[execv2.KillBehavior][]*bool{}
			recorder.mu.Lock()
			for _, c := range recorder.seen {
				got[c.kill] = append(got[c.kill], c.survive)
			}
			recorder.mu.Unlock()
			for kill, survive := range tc.want {
				closes := got[kill]
				if len(closes) < 2 {
					t.Errorf("%d sessions of the Kill %s were closed, want at least two: a graceful close and a kill", len(closes), kill)
				}
				for _, read := range closes {
					if (read == nil) != (survive == nil) || read != nil && *read != *survive {
						t.Errorf("a session of the Kill %s was closed and the host read remote_may_survive as %s, want %s", kill, survivalText(read), survivalText(survive))
					}
				}
			}
			for kill := range got {
				if _, allowed := tc.want[kill]; !allowed {
					t.Errorf("a session of the Kill %s was closed, which a kind of this declaration cannot claim", kill)
				}
			}
		})
	}
}

// Case 17 sends every source that an exit can have: the process's own status (a code that is not 0 as well), the code a target folded from
// a death by signal, the transport's word, with the code 0 as well, which is a legitimate end and not a vestige (client-go's "ended
// normally" relays no status of the process), and a break of the stream, which is Lost and has no code.
func TestCaseSeventeenSendsEverySourceOfAnExitAndABreak(t *testing.T) {
	caps := NewFakeTarget().Info.Capabilities
	var processNonZero, folded, transportZero, transportNonZero, lost int
	for _, ec := range exitCases(caps) {
		switch exited := ec.end.GetExited(); {
		case exited.GetSource() == execv2.ExitSource_EXIT_SOURCE_PROCESS && exited.GetExitCode() != 0:
			processNonZero++
		case exited.GetSource() == execv2.ExitSource_EXIT_SOURCE_SIGNAL_FOLDED:
			folded++
		case exited.GetSource() == execv2.ExitSource_EXIT_SOURCE_TRANSPORT && exited.GetExitCode() == 0:
			transportZero++
		case exited.GetSource() == execv2.ExitSource_EXIT_SOURCE_TRANSPORT:
			transportNonZero++
		case ec.end.GetLost() != nil:
			lost++
		}
	}
	if processNonZero == 0 || folded == 0 || transportZero == 0 || transportNonZero == 0 || lost == 0 {
		t.Errorf("the exits of case 17: %d non-zero statuses of the process, %d folded, %d transport 0, %d transport not 0, %d breaks: want each of them", processNonZero, folded, transportZero, transportNonZero, lost)
	}
	// A kind that reports no status relays only the transport's word, whatever its function claims.
	for _, ec := range exitCases(shapes()[3].info().Capabilities) {
		if exited := ec.end.GetExited(); exited != nil && exited.GetSource() != execv2.ExitSource_EXIT_SOURCE_TRANSPORT {
			t.Errorf("%s: for a kind that declares ExitReport NONE the code %d is %s, want TRANSPORT", ec.name, exited.GetExitCode(), exited.GetSource())
		}
	}
}
