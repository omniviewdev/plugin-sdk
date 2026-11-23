package session

import (
	"context"
	"testing"

	"github.com/omniviewdev/plugin-sdk/internal/lifecycletest"
)

// watcherFrame is the frame of a watcher goroutine: the closure Watch starts with Go.
const watcherFrame = "(*Entry[...]).Watch.func1"

func TestWatchEndsTheEntryWithTheGivenCauseWhenItsContextIsDone(t *testing.T) {
	l := newLane(t)
	e := l.admit("a")
	ctx, cancel := context.WithCancel(context.Background())
	e.Watch(ctx, CauseExpired)

	// The watcher waits: parked, it has not ended the entry.
	awaitBlocked(t, 1, watcherFrame)
	if got := e.Cause(); got != CauseNone {
		t.Fatalf("the entry has cause %v while the context it is watching is live", got)
	}
	cancel()
	l.awaitEnd(e)
	if got := e.Cause(); got != CauseExpired {
		t.Fatalf("cause %v, want %v", got, CauseExpired)
	}
}

func TestAWatcherStopsWhenTheEntryEndsFirst(t *testing.T) {
	l := newLane(t)
	e := l.admit("a")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	baseline := lifecycletest.TakeBaseline()
	e.Watch(ctx, CauseExpired)
	awaitBlocked(t, 1, watcherFrame)

	e.End(CauseRequested)
	// The end function runs once the watcher, a producer of the entry, has returned.
	l.awaitEnd(e)
	awaitGoroutines(t, 0, watcherFrame)
	if err := baseline.Check(mustHappen); err != nil {
		t.Fatalf("the watcher outlived its entry: %v", err)
	}
	cancel() // too late to change anything
	if got := e.Cause(); got != CauseRequested {
		t.Fatalf("cause %v, want %v", got, CauseRequested)
	}
}

func TestWatchOnAContextThatCannotBeDoneStartsNothing(t *testing.T) {
	l := newLane(t)
	e := l.admit("a")
	cancellable, cancel := context.WithCancel(context.Background())
	defer cancel()
	type key struct{}
	baseline := lifecycletest.TakeBaseline()
	for range 100 {
		e.Watch(context.Background(), CauseExpired)
		e.Watch(context.TODO(), CauseExpired)
		e.Watch(context.WithValue(context.Background(), key{}, 1), CauseExpired)
		e.Watch(context.WithoutCancel(cancellable), CauseExpired)
	}
	if err := baseline.Check(mustHappen / 50); err != nil {
		t.Fatalf("Watch on a context that can never be done started a goroutine: %v", err)
	}
	if got := e.Cause(); got != CauseNone {
		t.Fatalf("the entry has cause %v", got)
	}
}

func TestWatchOnAnEntryThatIsEndingDoesNothing(t *testing.T) {
	l := newLane(t)
	e := l.admit("a")
	release := make(chan struct{})
	l.producer(e, release) // keeps the entry ending, not done, when Watch is called
	e.End(CauseRequested)

	ctx, cancel := context.WithCancel(context.Background())
	baseline := lifecycletest.TakeBaseline()
	for range 100 {
		e.Watch(ctx, CauseExpired)
	}
	if err := baseline.Check(mustHappen / 50); err != nil {
		t.Fatalf("Watch on an entry that is ending started a goroutine: %v", err)
	}
	close(release)
	l.awaitEnd(e)
	cancel()
	if got := e.Cause(); got != CauseRequested {
		t.Fatalf("cause %v, want %v", got, CauseRequested)
	}
}

// Two watchers on one entry: the first to end it wins, and the other changes nothing. The seam
// holds the first after it has recorded its cause, so that the second arrives while it is held.
func TestTwoWatchersTheFirstToRecordWins(t *testing.T) {
	for _, tc := range []struct {
		name           string
		first, second  Cause
		cancelFirstCtx bool
	}{
		{"the first watcher's context ends first", CauseExpired, CauseTimeout, true},
		{"the second watcher's context ends first", CauseTimeout, CauseExpired, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newLane(t)
			e := l.admit("a")
			ctxA, cancelA := context.WithCancel(context.Background())
			ctxB, cancelB := context.WithCancel(context.Background())
			defer cancelA()
			defer cancelB()
			e.Watch(ctxA, CauseExpired)
			e.Watch(ctxB, CauseTimeout)
			awaitBlocked(t, 2, watcherFrame)

			reached, release := l.pauses.arm(atEndRecorded, "a")
			defer release()
			winner, loser := cancelA, cancelB
			want := CauseExpired
			if !tc.cancelFirstCtx {
				winner, loser = cancelB, cancelA
				want = CauseTimeout
			}
			winner()
			awaitClosed(t, reached, "the first watcher's End to record its cause")
			loser()
			awaitBlocked(t, 1, watcherFrame, endFrame, "(*Mutex).lockSlow") // the second waits behind it
			release()
			l.awaitEnd(e)
			if got := e.Cause(); got != want {
				t.Fatalf("cause %v, want %v: the watcher that recorded first must win", got, want)
			}
		})
	}
}

// Two watchers whose contexts end together: either may win, and the entry ends once with one of
// the two causes.
func TestTwoWatchersWhoseContextsEndTogetherEndTheEntryOnce(t *testing.T) {
	for round := range 200 {
		l := newLane(t)
		e := l.admit("a")
		shared, cancel := context.WithCancel(context.Background())
		e.Watch(shared, CauseExpired)
		e.Watch(shared, CauseTimeout)
		cancel()
		l.awaitEnd(e)
		if got := e.Cause(); got != CauseExpired && got != CauseTimeout {
			t.Fatalf("round %d: cause %v, want one of the watchers'", round, got)
		}
		if n := l.endCount("a"); n != 1 {
			t.Fatalf("round %d: the end function ran %d times, want once", round, n)
		}
	}
}
