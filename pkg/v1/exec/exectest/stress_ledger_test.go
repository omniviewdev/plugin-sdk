package exectest

import "testing"

// The ledger says which session a call that is addressed to an id was answered by. An id is used again once its tombstone is gone, and a start
// of it may be admitted by the engine before the ledger hears of it, so that a call made meanwhile meets a session that the harness knows nothing
// of: what the harness knows of the first session (its terminal, its end, its output) says nothing of that answer, and it must not be held to it.

// admitted puts a session under an id as a worker's start does: the start is under way, the engine admits it, the ledger records it, the
// start has returned.
func admitted(l *ledger, id string) *sessionRec {
	rec := &sessionRec{id: id}
	l.beginStart(id)
	l.admit(rec)
	l.endStart(id)
	return rec
}

func TestTheLedgerKnowsThatACallMeetsTheSessionItWasMeantForOnlyIfNoStartOfTheIdOverlappedIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		// before is what happens before the call that is addressed to the id "x" begins, and meanwhile what happens while it is made. The call is
		// meant for the session that held "x" when it began, and the ledger says whether the answer is that session's.
		before, meanwhile func(l *ledger)
		want              bool
	}{
		{"nothing happens", func(*ledger) {}, func(*ledger) {}, true},
		{"another id is started and admitted", func(*ledger) {}, func(l *ledger) { admitted(l, "y") }, true},
		{"the id is admitted again during the call", func(*ledger) {}, func(l *ledger) { admitted(l, "x") }, false},
		{"a start of the id is refused during the call", func(*ledger) {}, func(l *ledger) { l.beginStart("x"); l.endStart("x") }, true},
		{"a start of the id is under way when the call begins", func(l *ledger) { l.beginStart("x") }, func(l *ledger) { l.endStart("x") }, false},
		{"a start of the id begins during the call and has not returned when it ends", func(*ledger) {}, func(l *ledger) { l.beginStart("x") }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := &ledger{}
			rec := admitted(l, "x")
			tc.before(l)
			was := l.stamp("x")
			tc.meanwhile(l)
			if got := l.unchanged(was); got != tc.want {
				t.Fatalf("unchanged is %v, want %v", got, tc.want)
			}
			if got, want := l.holds(rec, was), tc.want && l.isLatest(rec); got != want {
				t.Fatalf("holds is %v, want %v", got, want)
			}
		})
	}
}

// A call that was made for a session that another has since replaced under its id is not meant for it, whatever overlapped it.
func TestTheLedgerDoesNotHoldASessionThatAnotherReplacedUnderItsId(t *testing.T) {
	l := &ledger{}
	old := admitted(l, "x")
	newer := admitted(l, "x")
	was := l.stamp("x")
	if l.holds(old, was) {
		t.Error("the ledger holds the session that was replaced")
	}
	if !l.holds(newer, was) {
		t.Error("the ledger does not hold the newest session of the id")
	}
}

// The calls on goroutines of their own that are addressed to an id are counted by the id, so that a start of it by the worker finds them
// whichever of its sessions they were made for.
func TestTheLedgerCountsTheCallsInFlightByIdAndNotBySession(t *testing.T) {
	l := &ledger{}
	l.flightOf("x").Add(1)
	if got := l.flightOf("x").Load(); got != 1 {
		t.Fatalf("%d calls in flight for x, want 1: the count is the id's, whoever asks", got)
	}
	if got := l.flightOf("y").Load(); got != 0 {
		t.Fatalf("%d calls in flight for y, want 0", got)
	}
}
