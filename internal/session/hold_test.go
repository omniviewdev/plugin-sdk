package session

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// A retained place stays taken after the entry that owned it is removed: an abandoned goroutine of a lane is still there,
// still using what the place stood for, and the lane must not admit another session to stand in its place.

func TestARetainedPlaceStaysTakenAfterItsEntryIsRemovedUntilReleased(t *testing.T) {
	l := newLane(t)
	spec := func(id string) Spec { return Spec{ID: id, Group: "g", Limit: 2} }
	a, err := l.admitWith(context.Background(), spec("a"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := l.admitWith(context.Background(), spec("b"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.admitWith(context.Background(), spec("c")); !errors.Is(err, ErrLimit) {
		t.Fatalf("two entries in a group of two: %v, want ErrLimit", err)
	}
	release := a.Retain()
	if got := l.reg.Held("g"); got != 1 {
		t.Fatalf("Held = %d after Retain, want 1", got)
	}
	// The place did not change hands: the group is as full as before, and now the hold is part of why.
	if _, err := l.admitWith(context.Background(), spec("c")); !errors.Is(err, ErrHeld) {
		t.Fatalf("a group of one entry and one hold, limit two: %v, want ErrHeld", err)
	}
	// Another group is not touched.
	if other, err := l.admitWith(context.Background(), Spec{ID: "other", Group: "h", Limit: 1}); err != nil {
		t.Fatalf("a hold of one group refused an admit into another: %v", err)
	} else {
		other.End(CauseRequested)
	}
	// The retained entry ends and is removed: its id is free, its place is not.
	a.End(CauseRequested)
	l.awaitRemoved("a")
	if again, err := l.admitWith(context.Background(), Spec{ID: "a", Group: "other-group"}); err != nil {
		t.Fatalf("the id of a removed entry is not free: %v", err)
	} else {
		again.End(CauseRequested)
	}
	if _, err := l.admitWith(context.Background(), spec("c")); !errors.Is(err, ErrHeld) {
		t.Fatalf("a removed entry's place that is retained: %v, want ErrHeld", err)
	}
	// The other entry ends: the hold alone is left, and it leaves room for one.
	b.End(CauseRequested)
	l.awaitRemoved("b")
	c, err := l.admitWith(context.Background(), spec("c"))
	if err != nil {
		t.Fatalf("one hold in a group of two: %v, want room for one entry", err)
	}
	c.End(CauseRequested)
	l.awaitRemoved("c")
	// Released, the place is free; releasing again, or retaining again, changes nothing.
	release()
	release()
	if again := a.Retain(); again == nil {
		t.Fatal("Retain returned no release the second time")
	}
	if got := l.reg.Held("g"); got != 0 {
		t.Fatalf("Held = %d after release, want 0: a second Retain of the same entry is the same hold, released", got)
	}
	l.reg.mu.Lock()
	groups, held := len(l.reg.groups), len(l.reg.held)
	l.reg.mu.Unlock()
	if groups != 0 || held != 0 {
		t.Fatalf("the registry keeps %d group counters and %d hold counters at rest: the maps must not grow", groups, held)
	}
}

// A refusal for a full group is ErrLimit when the live entries fill it, and ErrHeld when the holds are part of what fills it: a
// lane that tells its host "too many sessions" must be able to say whether that is true.
func TestAnAdmitIsRefusedForTheHoldsWhenTheHoldsArePartOfWhatFillsTheGroup(t *testing.T) {
	l := newLane(t)
	entries := make([]*Entry[*state], 3)
	for i := range entries {
		e, err := l.admitWith(context.Background(), Spec{ID: fmt.Sprintf("e%d", i), Group: "g", Limit: 3})
		if err != nil {
			t.Fatal(err)
		}
		entries[i] = e
	}
	if _, err := l.admitWith(context.Background(), Spec{ID: "z", Group: "g", Limit: 3}); !errors.Is(err, ErrLimit) {
		t.Fatalf("a group of three entries, limit three: %v, want ErrLimit", err)
	}
	// A retained entry is a hold, not an entry: with it, two entries and a hold fill three places.
	release := entries[0].Retain()
	defer release()
	if _, err := l.admitWith(context.Background(), Spec{ID: "z", Group: "g", Limit: 3}); !errors.Is(err, ErrHeld) {
		t.Fatalf("two entries and a hold, limit three: %v, want ErrHeld", err)
	}
	// An entry that is not retained ends: its place is free, and one more is admitted; then the hold and two entries fill it.
	entries[1].End(CauseRequested)
	l.awaitRemoved("e1")
	x, err := l.admitWith(context.Background(), Spec{ID: "x", Group: "g", Limit: 3})
	if err != nil {
		t.Fatalf("an entry ended and its place is free: %v", err)
	}
	if _, err := l.admitWith(context.Background(), Spec{ID: "y", Group: "g", Limit: 3}); !errors.Is(err, ErrHeld) {
		t.Fatalf("two entries and a hold again: %v, want ErrHeld", err)
	}
	x.End(CauseRequested)
}

// A hold is not an entry: it does not keep the registry from closing, and Close does not wait for it.
func TestAHoldDoesNotHoldUpCloseAndLeavesNoCounterBehind(t *testing.T) {
	l := newLane(t)
	e, err := l.admitWith(context.Background(), Spec{ID: "a", Group: "g", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	release := e.Retain()
	ctx, cancel := context.WithTimeout(context.Background(), mustHappen)
	defer cancel()
	if err := l.reg.Close(ctx); err != nil {
		t.Fatalf("Close with a hold outstanding: %v: it waits for entries, not for holds", err)
	}
	if got := l.reg.Held("g"); got != 1 {
		t.Fatalf("Held = %d after Close, want 1: the hold outlives the entry and the registry's close", got)
	}
	// Retaining an entry that was removed already takes a new hold: the place it had is gone, and the fact is true all the same.
	late := e.Retain()
	if got := l.reg.Held("g"); got != 1 {
		t.Fatalf("Held = %d, want 1: the entry was retained before", got)
	}
	release()
	late()
	if got := l.reg.Held("g"); got != 0 {
		t.Fatalf("Held = %d after the release, want 0", got)
	}
	l.reg.mu.Lock()
	groups, held := len(l.reg.groups), len(l.reg.held)
	l.reg.mu.Unlock()
	if groups != 0 || held != 0 {
		t.Fatalf("the registry keeps %d group counters and %d hold counters at rest: the maps must not grow", groups, held)
	}
}

// Retaining after the entry was removed is a new hold.
func TestRetainingAnEntryThatWasRemovedTakesANewHold(t *testing.T) {
	l := newLane(t)
	e, err := l.admitWith(context.Background(), Spec{ID: "a", Group: "g", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	e.End(CauseRequested)
	l.awaitRemoved("a")
	release := e.Retain()
	if got := l.reg.Held("g"); got != 1 {
		t.Fatalf("Held = %d, want 1", got)
	}
	x, err := l.admitWith(context.Background(), Spec{ID: "x", Group: "g", Limit: 2})
	if err != nil {
		t.Fatalf("one hold in a group of two: %v", err)
	}
	if _, err := l.admitWith(context.Background(), Spec{ID: "y", Group: "g", Limit: 2}); !errors.Is(err, ErrHeld) {
		t.Fatalf("a hold and an entry in a group of two: %v, want ErrHeld", err)
	}
	x.End(CauseRequested)
	release()
}

// Retains and admits racing. The rule is decided under the registry's lock, so it is checked there: inside init, which runs
// under it, an admit that is about to succeed finds the entries and the holds of its group below the limit. The end state is
// exact.
func TestRetainsAndAdmitsRacingNeverAdmitIntoAFullGroup(t *testing.T) {
	const (
		limit   = 8
		workers = 16
		rounds  = 400
	)
	l := newLane(t)
	var found findings
	var crewOf crew
	for w := range workers {
		crewOf.run(func() {
			for i := range rounds {
				e, err := l.admitInit(context.Background(), Spec{ID: fmt.Sprintf("w%d-%d", w, i), Group: "g", Limit: limit}, func() {
					// Under the registry's lock: the entry is not registered yet.
					if n := l.reg.groups["g"] + l.reg.held["g"]; n >= limit {
						found.add("an entry was admitted into a group that held %d entries and holds of a limit of %d", n, limit)
					}
				})
				switch {
				case err == nil:
					var release func()
					if (w+i)%3 == 0 {
						release = e.Retain()
					}
					e.End(CauseRequested)
					if release != nil {
						release()
					}
				case !errors.Is(err, ErrLimit) && !errors.Is(err, ErrHeld):
					found.add("an admit failed with %v", err)
				}
			}
		})
	}
	crewOf.wait(t)
	found.report(t)
	l.cleanup()
	if got := l.reg.Held("g"); got != 0 {
		t.Errorf("Held = %d at rest, want 0", got)
	}
	if got := l.reg.Len(); got != 0 {
		t.Errorf("%d entries at rest, want 0", got)
	}
	l.reg.mu.Lock()
	groups, held := len(l.reg.groups), len(l.reg.held)
	l.reg.mu.Unlock()
	if groups != 0 || held != 0 {
		t.Errorf("the registry keeps %d group counters and %d hold counters at rest, want none", groups, held)
	}
}
