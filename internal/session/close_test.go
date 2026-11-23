package session

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// Close against Close: any number of callers, at once and one after another, all return, and
// the entries end once.

func TestManyConcurrentClosesAllReturnOnceTheEntriesEnded(t *testing.T) {
	const entries, callers = 5, 8
	l := newLane(t)
	gate, inEnd := make(chan struct{}), make(chan string, entries)
	l.endHook = func(e *Entry[*state]) {
		inEnd <- e.ID()
		<-gate
	}
	for i := range entries {
		l.admit(fmt.Sprintf("a%d", i))
	}

	results := make([]<-chan error, callers)
	for i := range callers {
		results[i] = closeInBackground(l.reg, context.Background())
	}
	awaitBlocked(t, callers, closeFrame) // every caller waits for the entries
	for range entries {
		awaitValue(t, inEnd, "an end function to start")
	}
	if n := l.reg.Len(); n != entries {
		t.Fatalf("the registry holds %d entries while their end functions run, want %d", n, entries)
	}

	close(gate)
	for i, result := range results {
		if err := awaitValue(t, result, fmt.Sprintf("Close #%d", i)); err != nil {
			t.Fatalf("Close #%d: %v", i, err)
		}
	}
	if n := l.reg.Len(); n != 0 {
		t.Fatalf("Close returned with %d entries left", n)
	}
	for i := range entries {
		if n := l.endCount(fmt.Sprintf("a%d", i)); n != 1 {
			t.Fatalf("the end function ran %d times for entry %d, want once", n, i)
		}
	}
	// And again, after everything is gone: it returns at once.
	if err := awaitValue(t, closeInBackground(l.reg, context.Background()), "a Close of a closed registry"); err != nil {
		t.Fatalf("Close of a closed registry: %v", err)
	}
}

func TestCloseGivesUpWhenItsContextEndsAndALaterCloseWaitsAgain(t *testing.T) {
	l := newLane(t)
	gate, inEnd := make(chan struct{}), make(chan struct{})
	l.endHook = func(*Entry[*state]) {
		close(inEnd)
		<-gate
	}
	l.admit("a")

	ctx, cancel := context.WithCancel(context.Background())
	closed := closeInBackground(l.reg, ctx)
	awaitClosed(t, inEnd, "the end function to start")
	awaitBlocked(t, 1, closeFrame)
	cancel() // the context ends before the entry does
	if err := awaitValue(t, closed, "Close after its context ended"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close returned %v, want %v", err, context.Canceled)
	}

	// The registry stays closed, and still holds the entry the end function has not released.
	if _, err := l.admitWith(context.Background(), Spec{ID: "b"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Admit after a Close that gave up: %v, want %v", err, ErrClosed)
	}
	if n := l.reg.Len(); n != 1 {
		t.Fatalf("the registry holds %d entries after a Close that gave up, want 1", n)
	}

	later := closeInBackground(l.reg, context.Background())
	awaitBlocked(t, 1, closeFrame)
	close(gate)
	if err := awaitValue(t, later, "a later Close"); err != nil {
		t.Fatalf("a later Close: %v", err)
	}
	if n := l.reg.Len(); n != 0 {
		t.Fatalf("the later Close returned with %d entries left", n)
	}
	if n := l.endCount("a"); n != 1 {
		t.Fatalf("the end function ran %d times, want once", n)
	}
}

// Several Closes with their own contexts: the ones whose context ends give up, the others keep
// waiting, and all of them return once the entries have.
func TestClosesWithDifferentContextsGiveUpIndependently(t *testing.T) {
	l := newLane(t)
	gate, inEnd := make(chan struct{}), make(chan struct{})
	l.endHook = func(*Entry[*state]) {
		close(inEnd)
		<-gate
	}
	l.admit("a")

	giveUp, cancel := context.WithCancel(context.Background())
	quitting := []<-chan error{closeInBackground(l.reg, giveUp), closeInBackground(l.reg, giveUp)}
	staying := []<-chan error{closeInBackground(l.reg, context.Background()), closeInBackground(l.reg, context.Background())}
	awaitClosed(t, inEnd, "the end function to start")
	awaitBlocked(t, 4, closeFrame)

	cancel()
	for i, result := range quitting {
		if err := awaitValue(t, result, fmt.Sprintf("the Close that gives up (#%d)", i)); !errors.Is(err, context.Canceled) {
			t.Fatalf("a Close whose context ended returned %v, want %v", err, context.Canceled)
		}
	}
	awaitBlocked(t, 2, closeFrame) // the others are still waiting

	close(gate)
	for i, result := range staying {
		if err := awaitValue(t, result, fmt.Sprintf("the Close that stays (#%d)", i)); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
}

// With nothing to wait for, Close has nothing to give up: it returns nil whatever its context.
func TestCloseOfARegistryWithNoEntriesReturnsNilWhateverItsContext(t *testing.T) {
	done, cancel := context.WithCancel(context.Background())
	cancel()
	for name, ctx := range map[string]context.Context{"done": done, "live": context.Background()} {
		t.Run("context "+name, func(t *testing.T) {
			l := newLane(t)
			if err := awaitValue(t, closeInBackground(l.reg, ctx), "Close"); err != nil {
				t.Fatalf("Close of an empty registry: %v", err)
			}
		})
	}
}

// A Close with an entry that is already ending ends nothing again: the cause it recorded stays,
// and the end function still runs once.
func TestCloseKeepsTheCauseOfAnEntryThatIsAlreadyEnding(t *testing.T) {
	l := newLane(t)
	e := l.admit("a")
	release := make(chan struct{})
	l.producer(e, release)
	e.End(CauseRetired)

	closed := closeInBackground(l.reg, context.Background())
	awaitBlocked(t, 1, closeFrame)
	close(release)
	if err := awaitValue(t, closed, "Close"); err != nil {
		t.Fatal(err)
	}
	if got := e.Cause(); got != CauseRetired {
		t.Fatalf("cause %v, want %v: Close must not change the cause of an entry that is ending", got, CauseRetired)
	}
}
