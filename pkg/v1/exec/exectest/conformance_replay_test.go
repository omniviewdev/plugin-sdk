package exectest

import (
	"sync/atomic"
	"testing"
)

// The wire cases of the sessions that outlive their stream (10, 11, 21 and 22, and the pair P2) drive a plugin over a transport, so the tests of
// the suite hold them as they hold the other wire bodies: each body passes on every shape of declaration that an author may write, and none calls
// the author's Exec. A shape that matters here is the target that serves one session at a time: a body that opens a second session before it has
// ended the first is refused LIMIT / TOO_MANY_SESSIONS by it, and an author whose kind serves one session at a time would see the conformance fail
// for the suite's own sake.

// replayRowBodies are the wire bodies of the cases about a session that outlives its stream, by what the tests call them.
func replayRowBodies() []openRowBody {
	return []openRowBody{
		{"case 10", caseWireDetachReattach},
		{"case 11", caseWireLease},
		{"case 21", caseWireAtomicAttach},
		{"case 22", caseWireDetach},
		{"pair P2", casePairReplay},
	}
}

func TestEveryReplayRowBodyPassesOnEveryShapeAnAuthorDeclaresAndNeverCallsTheirExec(t *testing.T) {
	var calls atomic.Int64
	for _, s := range openShapes() {
		info := s.info()
		for _, b := range replayRowBodies() {
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

// The target of one session at a time is in the table above, and it is the declaration that makes a body hold two sessions at once visible: the
// table has to keep it, or a body that opens a second session before it has ended the first passes every other shape and fails the one an author
// of such a kind has.
func TestTheShapesOfTheReplayRowsHoldATargetThatServesOneSessionAtATime(t *testing.T) {
	for _, s := range openShapes() {
		if limits := s.info().Session; limits.MaxSessions == 1 {
			return
		}
	}
	t.Fatalf("no shape of the table declares MaxSessions 1: a body that holds two sessions at once is never refused")
}
