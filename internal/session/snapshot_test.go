package session

import (
	"context"
	"testing"
)

func TestSnapshotIsSortedACopyAndShowsTheCauseUntilTheEntryIsRemoved(t *testing.T) {
	l := newLane(t)
	gate, inEnd := make(chan struct{}), make(chan struct{})
	l.endHook = func(e *Entry[*state]) {
		if e.ID() == "b" {
			close(inEnd)
			<-gate
		}
	}
	entries := make(map[string]*Entry[*state])
	for _, spec := range []Spec{{ID: "c", Group: "y"}, {ID: "a", Group: "x"}, {ID: "b", Group: "x"}, {ID: "10"}} {
		e, err := l.admitWith(context.Background(), spec)
		if err != nil {
			t.Fatal(err)
		}
		entries[spec.ID] = e
	}

	infos := l.reg.Snapshot()
	want := []Info{{ID: "10"}, {ID: "a", Group: "x"}, {ID: "b", Group: "x"}, {ID: "c", Group: "y"}}
	if !equalInfos(infos, want) {
		t.Fatalf("Snapshot = %v, want %v, sorted by id", infos, want)
	}

	// It is a copy: nothing done to it, and nothing the registry does later, shows in it.
	infos[0].ID = "zzz"
	if got := l.reg.Snapshot(); !equalInfos(got, want) {
		t.Fatalf("Snapshot after the caller changed the one it was given = %v, want %v", got, want)
	}
	before := l.reg.Snapshot()
	entries["a"].End(CauseRequested)
	l.awaitEnd(entries["a"])
	l.awaitRemoved("a")
	if !equalInfos(before, want) {
		t.Fatalf("a Snapshot changed when the registry did: %v", before)
	}

	// The cause shows from End until the entry is removed, which is after its end function.
	entries["b"].End(CauseRetired)
	awaitClosed(t, inEnd, "the end function of b to start")
	got := l.reg.Snapshot()
	want = []Info{{ID: "10"}, {ID: "b", Group: "x", Cause: CauseRetired}, {ID: "c", Group: "y"}}
	if !equalInfos(got, want) {
		t.Fatalf("Snapshot while b's end function runs = %v, want %v", got, want)
	}
	close(gate)
	l.awaitRemoved("b")
	if got := l.reg.Snapshot(); len(got) != 2 {
		t.Fatalf("Snapshot after b was removed = %v, want two entries", got)
	}
}

func TestSnapshotOfARegistryWithNoEntriesIsEmpty(t *testing.T) {
	r := New(Config[int]{})
	if got := r.Snapshot(); len(got) != 0 {
		t.Fatalf("Snapshot of an empty registry = %v", got)
	}
	if n := r.Len(); n != 0 {
		t.Fatalf("Len of an empty registry = %d", n)
	}
}

func equalInfos(a, b []Info) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
