package session

import (
	"context"
	"testing"
)

// Get finds a live entry by its id, one that has ended included until it is removed, and never one that was removed or never
// existed.
func TestGetFindsALiveEntryUntilItIsRemoved(t *testing.T) {
	l := newLane(t)
	gate, inEnd := make(chan struct{}), make(chan struct{})
	l.endHook = func(e *Entry[*state]) {
		if e.ID() == "a" {
			close(inEnd)
			<-gate
		}
	}
	a, err := l.admitWith(context.Background(), Spec{ID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := l.reg.Get("a"); !ok || got != a {
		t.Fatalf("Get(a) = %v, %v, want the entry that was admitted", got, ok)
	}
	if got, ok := l.reg.Get("nothing"); ok || got != nil {
		t.Fatalf("Get of an id that was never admitted = %v, %v", got, ok)
	}

	// An entry that has ended is found, with its cause, until its end function has returned and it is removed.
	a.End(CauseRequested)
	awaitClosed(t, inEnd, "the end function of a to start")
	got, ok := l.reg.Get("a")
	if !ok || got != a || got.Cause() != CauseRequested {
		t.Fatalf("Get(a) while its end function runs = %v, %v, want the entry with its cause", got, ok)
	}
	close(gate)
	l.awaitRemoved("a")
	if got, ok := l.reg.Get("a"); ok || got != nil {
		t.Fatalf("Get(a) after it was removed = %v, %v", got, ok)
	}
}

// Entries returns the live entries sorted by id, as a copy: it does not change as the registry does.
func TestEntriesAreTheLiveEntriesSortedByIdAndACopy(t *testing.T) {
	l := newLane(t)
	want := map[string]*Entry[*state]{}
	for _, id := range []string{"c", "a", "10", "b"} {
		want[id] = l.admit(id)
	}
	entries := l.reg.Entries()
	var ids []string
	for _, e := range entries {
		ids = append(ids, e.ID())
		if want[e.ID()] != e {
			t.Errorf("Entries holds %v for %q, want the entry that was admitted", e, e.ID())
		}
	}
	if got := ids; len(got) != 4 || got[0] != "10" || got[1] != "a" || got[2] != "b" || got[3] != "c" {
		t.Fatalf("Entries are %v, want 10, a, b, c", got)
	}
	want["a"].End(CauseRequested)
	l.awaitRemoved("a")
	if len(entries) != 4 {
		t.Fatalf("the slice Entries returned changed when the registry did: %d entries", len(entries))
	}
	if got := l.reg.Entries(); len(got) != 3 {
		t.Fatalf("Entries after a was removed: %d, want 3", len(got))
	}
	if got := New(Config[int]{}).Entries(); len(got) != 0 {
		t.Fatalf("Entries of an empty registry: %v", got)
	}
}
