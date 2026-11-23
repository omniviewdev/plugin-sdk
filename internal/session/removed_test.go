package session

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// Removed closes when the registry has removed the entry, which is after its end function
// returned, and from the moment it does the id and the place in the group are free.

func TestRemovedClosesOnlyAfterTheEndFunctionReturned(t *testing.T) {
	l := newLane(t)
	var (
		once  sync.Once
		gate  = make(chan struct{})
		inEnd = make(chan struct{})
	)
	l.endHook = func(*Entry[*state]) {
		once.Do(func() {
			close(inEnd)
			<-gate
		})
	}
	e := l.admit("a")
	select {
	case <-e.Removed():
		t.Fatal("Removed is closed for a running entry")
	default:
	}
	e.End(CauseRequested)
	awaitClosed(t, inEnd, "the end function to start")
	select {
	case <-e.Removed():
		t.Fatal("Removed is closed while the end function runs")
	default:
	}
	close(gate)
	awaitClosed(t, e.Removed(), "Removed")
}

// The id is released before Removed closes, not after: the finisher is held on the lock it
// takes to release the id, and Removed must wait for it.
func TestRemovedDoesNotCloseBeforeTheIDIsReleased(t *testing.T) {
	l := newLane(t)
	spec := Spec{ID: "a", Group: "g", Limit: 1}
	e, err := l.admitWith(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	thaw := l.freeze()
	e.End(CauseRequested)
	l.awaitEnd(e)                                  // the end function ran: it does not need the registry's lock
	awaitBlocked(t, 1, "(*Registry[...]).remove(") // the finisher waits for the lock that releases the id
	select {
	case <-e.Removed():
		thaw()
		t.Fatal("Removed is closed while the id is still held")
	default:
	}
	thaw()
	awaitClosed(t, e.Removed(), "Removed")
	// From the moment Removed closes, with no waiting, the id and the slot are free.
	if _, err := l.admitWith(context.Background(), spec); err != nil {
		t.Fatalf("Admit of the id and group right after Removed closed: %v", err)
	}
}

// An id that is admitted again as soon as its entry is removed is never refused: Removed is the
// signal that a retry may be made.
func TestAnIDIsAdmittedAgainTheMomentItsEntryIsRemoved(t *testing.T) {
	l := newLane(t)
	for cycle := range 300 {
		e, err := l.admitWith(context.Background(), Spec{ID: "again", Group: "g", Limit: 1})
		if err != nil {
			t.Fatalf("cycle %d: Admit: %v", cycle, err)
		}
		e.End(CauseFinished)
		awaitClosed(t, e.Removed(), fmt.Sprintf("Removed in cycle %d", cycle))
	}
	if n := l.endCount("again"); n != 300 {
		t.Fatalf("the end function ran %d times for 300 admissions", n)
	}
}

// Close returns after the registry removed every entry: Removed is closed for all of them.
func TestRemovedIsClosedForEveryEntryWhenCloseReturns(t *testing.T) {
	l := newLane(t)
	var entries []*Entry[*state]
	for i := range 20 {
		entries = append(entries, l.admit(fmt.Sprintf("a%d", i)))
	}
	if err := awaitValue(t, closeInBackground(l.reg, context.Background()), "Close"); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		select {
		case <-e.Removed():
		default:
			t.Fatalf("Close returned before %s was removed", e.ID())
		}
	}
}

// An end function that panics still has its entry removed, and Removed closed.
func TestRemovedClosesWhenTheEndFunctionPanics(t *testing.T) {
	r := New(Config[struct{}]{OnEnd: func(*Entry[struct{}]) { panic("boom") }})
	panicked := make(chan any, 1)
	r.seams.spawn = func(run func()) {
		go func() {
			defer func() { panicked <- recover() }()
			run()
		}()
	}
	e, err := r.Admit(context.Background(), Spec{ID: "a"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.End(CauseRequested)
	awaitValue(t, panicked, "the finisher")
	select {
	case <-e.Removed():
	default:
		t.Fatal("Removed is not closed after the end function panicked")
	}
}
