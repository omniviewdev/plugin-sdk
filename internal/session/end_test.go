package session

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/omniviewdev/plugin-sdk/internal/lifecycletest"
)

// The frames of the goroutines the tests below wait for: a producer, which is a closure in Go,
// and the finisher.
const (
	producerFrame = "(*Entry[...]).Go.func1"
	finisherFrame = "(*Registry[...]).finish("
)

// producer starts a producer of e that runs until release is closed, and returns once it runs.
// It records "producer returned" when it returns, and counts itself in the state while it runs, so
// that the end function can check that no producer is left.
func (l *lane) producer(e *Entry[*state], release <-chan struct{}) {
	l.t.Helper()
	st := e.Value()
	started := make(chan struct{})
	if !e.Go(func() {
		st.active.Add(1)
		defer st.active.Add(-1)
		close(started)
		<-release
		l.event("producer returned")
	}) {
		l.t.Fatalf("Go was refused by %s, which has not ended", e.ID())
	}
	awaitClosed(l.t, started, "the producer to run")
}

// End against the last producer. The entry's end is decided by two things, that End was called
// and that its last producer returned, and the order they come in is not the caller's to
// choose: the last producer returns before End, End is called and the last producer returns
// later, or both at once. Done closes once, after both; the end function runs after Done and
// once.

func TestTheLastProducerReturnsBeforeEnd(t *testing.T) {
	l := newLane(t)
	e := l.admit("a")
	release := make(chan struct{})
	l.producer(e, release)
	close(release)
	// The producer goroutine is gone, so its Done was called and End finds no producer.
	awaitGoroutines(t, 0, producerFrame)

	if !e.End(CauseRequested) {
		t.Fatal("End did not win")
	}
	l.awaitEnd(e)
	assertOrder(t, l, "producer returned", "end:a")
	select {
	case <-e.Done():
	default:
		t.Fatal("Done is not closed after the end function ran")
	}
}

func TestEndIsCalledAndTheLastProducerReturnsLater(t *testing.T) {
	l := newLane(t)
	e := l.admit("a")
	release := make(chan struct{})
	l.producer(e, release)

	if !e.End(CauseRequested) {
		t.Fatal("End did not win")
	}
	// The finisher waits for the producer: it is parked, so neither Done nor the end function
	// can have happened.
	awaitBlocked(t, 1, finisherFrame, groupWaitFrame)
	select {
	case <-e.Done():
		t.Fatal("Done is closed with a producer running")
	default:
	}
	if n := l.endCount("a"); n != 0 {
		t.Fatalf("the end function ran %d times with a producer running", n)
	}
	close(release)
	l.awaitEnd(e)
	assertOrder(t, l, "producer returned", "end:a")
}

// The crossing: End has recorded the cause and not yet started the finisher when the last
// producer returns, and then End goes on. The finisher starts after the producer's Done, finds
// nothing to wait for, and the entry ends once.
func TestTheLastProducerReturnsBetweenEndRecordingAndTheFinisherStarting(t *testing.T) {
	l := newLane(t)
	e := l.admit("a")
	release := make(chan struct{})
	l.producer(e, release)
	reached, releaseEnd := l.pauses.arm(atEndRecorded, "a")
	defer releaseEnd()

	var c crew
	c.run(func() { e.End(CauseRequested) })
	awaitClosed(t, reached, "End to record its cause")
	close(release)
	awaitGoroutines(t, 0, producerFrame) // the last producer's Done ran, with nobody waiting for it yet
	releaseEnd()
	c.wait(t)

	l.awaitEnd(e)
	assertOrder(t, l, "producer returned", "end:a")
	if n := l.endCount("a"); n != 1 {
		t.Fatalf("the end function ran %d times, want once", n)
	}
}

// The producer is the one that ends the session, as a run whose work is over does: End does not
// wait for its producers, or this would never return.
func TestEndCalledByTheLastProducerDoesNotWaitForIt(t *testing.T) {
	l := newLane(t)
	e := l.admit("a")
	st := e.Value()
	returned := make(chan bool, 1)
	if !e.Go(func() {
		st.active.Add(1)
		defer st.active.Add(-1)
		returned <- e.End(CauseFinished)
		l.event("producer returned")
	}) {
		t.Fatal("Go was refused")
	}
	if first := awaitValue(t, returned, "End to return to the producer that called it"); !first {
		t.Fatal("the producer's End did not win")
	}
	l.awaitEnd(e)
	assertOrder(t, l, "producer returned", "end:a")
	if got := e.Cause(); got != CauseFinished {
		t.Fatalf("cause %v, want %v", got, CauseFinished)
	}
}

// End and the last producer, released together and at random points of their work, over many
// entries. Run under -race.
func TestEndAndTheLastProducerReturnConcurrently(t *testing.T) {
	l := newLane(t)
	const rounds = 400
	for round := range rounds {
		e := l.admit(fmt.Sprintf("a%d", round))
		st := e.Value()
		var c crew
		start := make(chan struct{})
		producing := make(chan struct{})
		if !e.Go(func() {
			st.active.Add(1)
			defer st.active.Add(-1)
			close(producing)
			<-start
			shuffle(rngFor(1, uint64(round)), 30)
			l.event("producer returned " + e.ID())
		}) {
			t.Fatal("Go was refused")
		}
		awaitClosed(t, producing, "the producer")
		c.run(func() {
			<-start
			shuffle(rngFor(2, uint64(round)), 30)
			e.End(CauseRequested)
		})
		close(start)
		c.wait(t)
		l.awaitEnd(e)
		assertOrder(t, l, "producer returned "+e.ID(), "end:"+e.ID())
	}
}

// assertOrder fails unless first happened, and then second.
func assertOrder(t *testing.T, l *lane, first, second string) {
	t.Helper()
	i, j := l.indexOf(first), l.indexOf(second)
	if i < 0 || j < 0 || i > j {
		t.Fatalf("events %v: want %q before %q", l.events(), first, second)
	}
}

// Go against End.

// Go is refused once End has returned, whether the entry's last producer has returned or not:
// a closing group would admit a producer while one runs, and an entry that has been ended must
// not.
func TestGoAfterEndIsRefusedAndStartsNothing(t *testing.T) {
	l := newLane(t)
	gate, inEnd := make(chan struct{}), make(chan struct{})
	l.endHook = func(e *Entry[*state]) {
		if e.ID() == "a" {
			close(inEnd)
			<-gate
		}
	}
	e := l.admit("a")
	release := make(chan struct{})
	l.producer(e, release)
	e.End(CauseRequested)

	var ran atomic.Bool
	refuses := func(when string) {
		t.Helper()
		baseline := lifecycletest.TakeBaseline()
		for range 1000 {
			if e.Go(func() { ran.Store(true) }) {
				t.Fatalf("Go was accepted %s", when)
			}
		}
		if err := baseline.Check(mustHappen / 50); err != nil {
			t.Fatalf("a Go that was refused %s started a goroutine: %v", when, err)
		}
		if ran.Load() {
			t.Fatalf("a producer that Go refused %s ran", when)
		}
	}

	awaitBlocked(t, 1, finisherFrame, groupWaitFrame)
	refuses("while the entry's last producer still ran")
	close(release)
	awaitClosed(t, inEnd, "the end function to start")
	refuses("while the end function ran")
	close(gate)
	l.awaitRemoved("a")
	refuses("after the entry was removed")
}

// Go and End, with the crossing between Go's decision and its registration forced in each way.
func TestGoRacingEndWhenTheEntryEndsFirstIsRefusedWithoutAGoroutine(t *testing.T) {
	l := newLane(t)
	e := l.admit("a")
	reached, release := l.pauses.arm(atGoChecked, "a")
	defer release()

	var (
		c   crew
		ran atomic.Bool
		ok  bool
	)
	c.run(func() { ok = e.Go(func() { ran.Store(true) }) })
	awaitClosed(t, reached, "Go to pass its check")

	// The entry ends completely, and is removed, while Go is between its check and its
	// registration of the producer.
	e.End(CauseRequested)
	l.awaitEnd(e)
	l.awaitRemoved("a")
	baseline := lifecycletest.TakeBaseline()
	release()
	c.wait(t)
	if ok {
		t.Fatal("Go reported a producer started on an entry whose end function had already run")
	}
	if err := baseline.Check(mustHappen / 50); err != nil {
		t.Fatalf("a refused Go started a goroutine: %v", err)
	}
	if ran.Load() {
		t.Fatal("the producer ran after the entry's end function")
	}
	if n := l.endCount("a"); n != 1 {
		t.Fatalf("the end function ran %d times, want once", n)
	}
}

func TestGoRacingEndWhenTheEntryIsStillWaitingForAProducerIsWaitedFor(t *testing.T) {
	l := newLane(t)
	e := l.admit("a")
	st := e.Value()
	first := make(chan struct{})
	l.producer(e, first)
	reached, release := l.pauses.arm(atGoChecked, "a")
	defer release()

	var (
		c        crew
		ok       bool
		started  = make(chan struct{})
		second   = make(chan struct{})
		secondOK atomic.Bool
	)
	c.run(func() {
		ok = e.Go(func() {
			st.active.Add(1)
			defer st.active.Add(-1)
			close(started)
			<-second
			l.event("second producer returned")
			secondOK.Store(true)
		})
	})
	awaitClosed(t, reached, "Go to pass its check")
	e.End(CauseRequested)                             // the finisher waits for the first producer
	awaitBlocked(t, 1, finisherFrame, groupWaitFrame) // the group is closing, and admits while one runs
	release()
	awaitClosed(t, started, "the producer that raced End")
	c.wait(t)
	if !ok {
		t.Fatal("Go was refused while the entry was waiting for a producer: a producer that races End must be admitted and waited for")
	}

	close(first)
	awaitGoroutines(t, 1, producerFrame) // the first producer returned, the second runs
	awaitBlocked(t, 1, finisherFrame, groupWaitFrame)
	select {
	case <-e.Done():
		t.Fatal("Done closed with the producer that raced End still running")
	default:
	}
	close(second)
	l.awaitEnd(e)
	assertOrder(t, l, "second producer returned", "end:a")
	if !secondOK.Load() {
		t.Fatal("the end function ran before the second producer finished")
	}
}

// Go and End from different goroutines at random points: whichever comes first, an accepted
// producer ran before the end function and a refused one never ran.
func TestGoAndEndRaceAtRandomPoints(t *testing.T) {
	l := newLane(t)
	const rounds = 500
	for round := range rounds {
		id := fmt.Sprintf("a%d", round)
		e := l.admit(id)
		st := e.Value()
		var (
			c   crew
			ok  bool
			ran atomic.Bool
		)
		start := make(chan struct{})
		c.run(func() {
			<-start
			shuffle(rngFor(3, uint64(round)), 30)
			ok = e.Go(func() {
				st.active.Add(1)
				defer st.active.Add(-1)
				ran.Store(true)
				l.event("producer ran " + id)
			})
		})
		c.run(func() {
			<-start
			shuffle(rngFor(4, uint64(round)), 30)
			e.End(CauseRequested)
		})
		close(start)
		c.wait(t)
		l.awaitEnd(e)

		switch {
		case ok:
			// Admitted, so the end waited for it: it ran, and before the end function did.
			assertOrder(t, l, "producer ran "+id, "end:"+id)
		case ran.Load():
			t.Fatalf("round %d: Go reported refused, and the producer ran", round)
		}
	}
}

func TestGoFromAProducerIsAcceptedBeforeEndAndRefusedAfter(t *testing.T) {
	l := newLane(t)
	e := l.admit("a")
	st := e.Value()
	type results struct{ before, after bool }
	got := make(chan results, 1)
	inner := make(chan struct{})
	if !e.Go(func() {
		st.active.Add(1)
		defer st.active.Add(-1)
		var r results
		r.before = e.Go(func() {
			st.active.Add(1)
			defer st.active.Add(-1)
			close(inner)
		})
		<-e.Ended()
		r.after = e.Go(func() { l.event("a producer started after End") })
		got <- r
	}) {
		t.Fatal("Go was refused")
	}
	awaitClosed(t, inner, "the producer a producer started")
	e.End(CauseRequested)
	r := awaitValue(t, got, "the producer's results")
	if !r.before {
		t.Fatal("Go from a producer before End was refused")
	}
	if r.after {
		t.Fatal("Go from a producer after End was accepted")
	}
	l.awaitEnd(e)
	if l.indexOf("a producer started after End") >= 0 {
		t.Fatal("a producer that Go refused ran")
	}
}

// Go from many goroutines at once on one entry: every producer is accepted, runs, and has
// returned when the end function runs.
func TestConcurrentGosOnOneEntryAreAllAcceptedAndAllWaitedFor(t *testing.T) {
	const callers = 32
	l := newLane(t)
	e := l.admit("a")
	st := e.Value()
	var (
		c       crew
		ran     atomic.Int32
		refused atomic.Int32
	)
	start := make(chan struct{})
	for i := range callers {
		c.run(func() {
			<-start
			shuffle(rngFor(5, uint64(i)), 20)
			if !e.Go(func() {
				st.active.Add(1)
				defer st.active.Add(-1)
				shuffle(rngFor(6, uint64(i)), 20)
				ran.Add(1)
			}) {
				refused.Add(1)
			}
		})
	}
	close(start)
	c.wait(t)
	if n := refused.Load(); n != 0 {
		t.Fatalf("%d of %d concurrent Go calls on a running entry were refused", n, callers)
	}
	e.End(CauseRequested)
	l.awaitEnd(e)
	if n := ran.Load(); n != callers {
		t.Fatalf("%d of %d producers had run when the end function did", n, callers)
	}
}

// End against End.

func TestConcurrentEndsHaveOneWinnerAndOneCause(t *testing.T) {
	const callers = 8
	causes := []Cause{CauseFinished, CauseRequested, CauseShutdown, CauseRetired, CauseExpired, CauseTimeout, CauseCancelled, CauseFinished}
	for round := range 100 {
		l := newLane(t)
		e := l.admit("a")
		var (
			c       crew
			found   findings
			winners atomic.Int32
			winner  atomic.Uint32
		)
		start := make(chan struct{})
		for i := range callers {
			c.run(func() {
				<-start
				shuffle(rngFor(uint64(round), uint64(i)), 20)
				if e.End(causes[i]) {
					winner.Store(uint32(causes[i]))
					winners.Add(1)
				}
				// However it came out, once End has returned the context is cancelled, Ended is
				// closed and the cause is recorded.
				select {
				case <-e.Ended():
				default:
					found.add("round %d: End returned with Ended not closed", round)
				}
				if e.Context().Err() == nil {
					found.add("round %d: End returned with the context not cancelled", round)
				}
				if e.Cause() == CauseNone {
					found.add("round %d: End returned with no cause recorded", round)
				}
			})
		}
		close(start)
		c.wait(t)
		found.report(t)
		if n := winners.Load(); n != 1 {
			t.Fatalf("round %d: %d of %d concurrent Ends reported first, want exactly one", round, n, callers)
		}
		if got := e.Cause(); got != Cause(winner.Load()) {
			t.Fatalf("round %d: the cause is %v, but the End that won passed %v", round, got, Cause(winner.Load()))
		}
		l.awaitEnd(e)
		if n := l.endCount("a"); n != 1 {
			t.Fatalf("round %d: the end function ran %d times, want once", round, n)
		}
	}
}

func TestAnEndAfterTheFirstChangesNothing(t *testing.T) {
	l := newLane(t)
	e := l.admit("a")
	if e.Cause() != CauseNone {
		t.Fatalf("a running entry has cause %v", e.Cause())
	}
	if !e.End(CauseRequested) {
		t.Fatal("the first End did not win")
	}
	for _, c := range []Cause{CauseRequested, CauseTimeout, CauseShutdown} {
		if e.End(c) {
			t.Fatalf("a later End(%v) reported first", c)
		}
	}
	if got := e.Cause(); got != CauseRequested {
		t.Fatalf("cause %v, want %v: a later End changed it", got, CauseRequested)
	}
	l.awaitEnd(e)
	if e.End(CauseFinished) {
		t.Fatal("an End after the end function ran reported first")
	}
	if got := e.Cause(); got != CauseRequested {
		t.Fatalf("cause %v after the end function ran, want %v", got, CauseRequested)
	}
}

func TestEndRecordsTheCauseCancelsTheContextAndClosesEndedBeforeItReturns(t *testing.T) {
	l := newLane(t)
	e := l.admit("a")
	if err := e.Context().Err(); err != nil {
		t.Fatalf("the context of a running entry is done: %v", err)
	}
	select {
	case <-e.Ended():
		t.Fatal("Ended is closed for a running entry")
	case <-e.Done():
		t.Fatal("Done is closed for a running entry")
	default:
	}
	e.End(CauseExpired)
	if got := e.Cause(); got != CauseExpired {
		t.Fatalf("cause %v, want %v", got, CauseExpired)
	}
	if err := e.Context().Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("the context's error is %v after End, want %v", err, context.Canceled)
	}
	select {
	case <-e.Ended():
	default:
		t.Fatal("Ended is not closed after End returned")
	}
}

func TestEndWithCauseNoneIsAProgrammerError(t *testing.T) {
	l := newLane(t)
	e := l.admit("a")
	if got := recovered(func() { e.End(CauseNone) }); got == nil {
		t.Fatal("End(CauseNone) did not panic")
	}
	if got := recovered(func() { e.Watch(context.Background(), CauseNone) }); got == nil {
		t.Fatal("Watch(ctx, CauseNone) did not panic")
	}
	// Neither ended the entry.
	if got := e.Cause(); got != CauseNone {
		t.Fatalf("a refused End left cause %v", got)
	}
	if !e.Go(func() {}) {
		t.Fatal("Go was refused by an entry that is still running")
	}
}

// End against Close. The first to record its cause wins; the other waits behind it and changes
// nothing. The seam holds the winner after it has recorded, so that the loser arrives while it
// is held.
func TestEndAgainstCloseTheFirstToRecordWins(t *testing.T) {
	t.Run("End first", func(t *testing.T) {
		l := newLane(t)
		e := l.admit("a")
		reached, release := l.pauses.arm(atEndRecorded, "a")
		defer release()

		var (
			c     crew
			first bool
		)
		c.run(func() { first = e.End(CauseRequested) })
		awaitClosed(t, reached, "End to record its cause")
		closed := closeInBackground(l.reg, context.Background())
		awaitBlocked(t, 1, closeFrame, endFrame, "(*Mutex).lockSlow") // Close's End waits behind the first
		release()
		c.wait(t)
		if err := awaitValue(t, closed, "Close"); err != nil {
			t.Fatal(err)
		}
		if !first {
			t.Fatal("the End that recorded first did not report first")
		}
		if got := e.Cause(); got != CauseRequested {
			t.Fatalf("cause %v, want %v", got, CauseRequested)
		}
		if n := l.endCount("a"); n != 1 {
			t.Fatalf("the end function ran %d times, want once", n)
		}
	})
	t.Run("Close first", func(t *testing.T) {
		l := newLane(t)
		e := l.admit("a")
		reached, release := l.pauses.arm(atEndRecorded, "a")
		defer release()

		closed := closeInBackground(l.reg, context.Background())
		awaitClosed(t, reached, "Close's End to record its cause")
		var (
			c     crew
			first bool
		)
		c.run(func() { first = e.End(CauseRequested) })
		awaitBlocked(t, 1, endFrame, "(*Mutex).lockSlow") // the lane's End waits behind Close's
		release()
		c.wait(t)
		if err := awaitValue(t, closed, "Close"); err != nil {
			t.Fatal(err)
		}
		if first {
			t.Fatal("an End that came after Close's reported first")
		}
		if got := e.Cause(); got != CauseShutdown {
			t.Fatalf("cause %v, want %v", got, CauseShutdown)
		}
		if n := l.endCount("a"); n != 1 {
			t.Fatalf("the end function ran %d times, want once", n)
		}
	})
}
