package session

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"testing"

	"github.com/omniviewdev/plugin-sdk/internal/lifecycletest"
)

// Admit against Close: the registry decides both under one lock, so an Admit is either before
// Close, and Close ends it, or after, and it is refused. There is no third order.

func TestAnAdmitAfterCloseIsRefusedAndRegistersAndStartsNothing(t *testing.T) {
	l := newLane(t)
	if err := l.reg.Close(context.Background()); err != nil {
		t.Fatalf("Close of an empty registry: %v", err)
	}

	// A parent that can be cancelled: an entry that was admitted would start a watcher for it.
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	baseline := lifecycletest.TakeBaseline()
	inits := 0
	for i := range 1000 {
		e, err := l.reg.Admit(parent, Spec{ID: fmt.Sprintf("late-%d", i)}, func(context.Context) (*state, error) {
			inits++ // init runs under the registry's lock
			return &state{over: make(chan struct{})}, nil
		})
		if !errors.Is(err, ErrClosed) || e != nil {
			t.Fatalf("Admit after Close returned (%v, %v), want (nil, %v)", e, err, ErrClosed)
		}
	}
	if inits != 0 {
		t.Fatalf("init ran %d times for an Admit that was refused", inits)
	}
	if n := l.reg.Len(); n != 0 {
		t.Fatalf("a refused Admit registered %d entries", n)
	}
	if infos := l.reg.Snapshot(); len(infos) != 0 {
		t.Fatalf("a refused Admit left %v in the registry", infos)
	}
	if err := baseline.Check(mustHappen / 50); err != nil {
		t.Fatalf("a refused Admit started a goroutine: %v", err)
	}
	if n := len(l.events()); n != 0 {
		t.Fatalf("a refused Admit emitted %d ends", n)
	}
}

func TestACloseAfterAnAdmitEndsItWithShutdownAndWaitsForItsEnd(t *testing.T) {
	l := newLane(t)
	gate, inEnd := make(chan struct{}), make(chan struct{})
	l.endHook = func(*Entry[*state]) {
		close(inEnd)
		<-gate
	}
	e := l.admit("a")

	closed := closeInBackground(l.reg, context.Background())
	awaitClosed(t, inEnd, "Close to end the entry")
	if got := e.Cause(); got != CauseShutdown {
		t.Fatalf("Close ended the entry with %v, want %v", got, CauseShutdown)
	}
	// The end function is running and holds the entry: Close is parked waiting for it, and the
	// registry still has the entry.
	awaitBlocked(t, 1, closeFrame)
	if n := l.reg.Len(); n != 1 {
		t.Fatalf("the registry holds %d entries while the end function runs, want 1", n)
	}
	if infos := l.reg.Snapshot(); len(infos) != 1 || infos[0].Cause != CauseShutdown {
		t.Fatalf("Snapshot while the end function runs: %v, want the entry with cause %v", infos, CauseShutdown)
	}

	close(gate)
	if err := awaitValue(t, closed, "Close after the end function returned"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if n := l.reg.Len(); n != 0 {
		t.Fatalf("Close returned with %d entries left", n)
	}
	if n := l.endCount("a"); n != 1 {
		t.Fatalf("the end function ran %d times, want once", n)
	}
}

// An Admit and a Close that are both blocked on the admission lock, released together: whichever
// the lock lets in first, the entry is never admitted and forgotten. The order the lock hands
// out is made to come out both ways, by which goroutine is parked first.
func TestAnAdmitAndACloseBlockedOnTheAdmissionLockAreOrderedByIt(t *testing.T) {
	var admitted, refused int
	for round := range 200 {
		l := newLane(t)
		thaw := l.freeze()
		var (
			c        crew
			e        *Entry[*state]
			admitErr error
			closeErr error
		)
		startAdmit := func() {
			c.run(func() { e, admitErr = l.admitWith(context.Background(), Spec{ID: "x"}) })
			awaitBlocked(t, 1, admitFrame)
		}
		startClose := func() {
			c.run(func() { closeErr = l.reg.Close(context.Background()) })
			awaitBlocked(t, 1, closeFrame)
		}
		if rngFor(7, uint64(round)).IntN(2) == 0 {
			startAdmit()
			startClose()
		} else {
			startClose()
			startAdmit()
		}
		thaw()
		c.wait(t)

		if closeErr != nil {
			t.Fatalf("round %d: Close: %v", round, closeErr)
		}
		if n := l.reg.Len(); n != 0 {
			t.Fatalf("round %d: Close returned with %d entries left", round, n)
		}
		switch {
		case admitErr == nil:
			admitted++
			if got := e.Cause(); got != CauseShutdown {
				t.Fatalf("round %d: the entry an Admit got before Close ended with %v, want %v", round, got, CauseShutdown)
			}
			if n := l.endCount("x"); n != 1 {
				t.Fatalf("round %d: the admitted entry's end ran %d times, want once", round, n)
			}
		case errors.Is(admitErr, ErrClosed):
			refused++
			if n := l.endCount("x"); n != 0 {
				t.Fatalf("round %d: an Admit that was refused had its end run %d times", round, n)
			}
		default:
			t.Fatalf("round %d: Admit: %v", round, admitErr)
		}
		if e2, err := l.admitWith(context.Background(), Spec{ID: "y"}); !errors.Is(err, ErrClosed) || e2 != nil {
			t.Fatalf("round %d: Admit after Close returned (%v, %v), want %v", round, e2, err, ErrClosed)
		}
	}
	if admitted == 0 || refused == 0 {
		t.Fatalf("only one order came out in 200 rounds (admitted %d, refused %d): the crossing was not forced both ways", admitted, refused)
	}
	t.Logf("the Admit won %d rounds and the Close won %d", admitted, refused)
}

// An Admit refused for several reasons says the most final one: a closed registry, before a
// live id, before a full group.
func TestAnAdmitThatIsRefusedForSeveralReasonsReportsTheClosedRegistryFirst(t *testing.T) {
	l := newLane(t)
	gate, inEnd := make(chan struct{}), make(chan struct{})
	l.endHook = func(*Entry[*state]) {
		close(inEnd)
		<-gate
	}
	if _, err := l.admitWith(context.Background(), Spec{ID: "a", Group: "g", Limit: 1}); err != nil {
		t.Fatal(err)
	}
	closed := closeInBackground(l.reg, context.Background())
	awaitClosed(t, inEnd, "Close to end the entry")

	// The entry is live and the group is full, and the registry is closed.
	if _, err := l.admitWith(context.Background(), Spec{ID: "a", Group: "g", Limit: 1}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Admit of a live id in a full group of a closed registry: %v, want %v", err, ErrClosed)
	}
	close(gate)
	awaitValue(t, closed, "Close")
}

// Admit against Admit.

func TestConcurrentAdmitsOfOneIDAdmitExactlyOne(t *testing.T) {
	const callers = 12
	for round := range 100 {
		l := newLane(t)
		var c crew
		start := make(chan struct{})
		results := make([]error, callers)
		for i := range callers {
			c.run(func() {
				<-start
				shuffle(rngFor(uint64(round), uint64(i)), 20)
				_, results[i] = l.admitWith(context.Background(), Spec{ID: "same"})
			})
		}
		close(start)
		c.wait(t)

		wins := 0
		for i, err := range results {
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ErrExists):
			default:
				t.Fatalf("round %d: caller %d: %v", round, i, err)
			}
		}
		if wins != 1 {
			t.Fatalf("round %d: %d of %d Admits of one id were admitted, want exactly one", round, wins, callers)
		}
		if n := l.reg.Len(); n != 1 {
			t.Fatalf("round %d: the registry holds %d entries, want 1", round, n)
		}
	}
}

func TestConcurrentAdmitsOfDifferentIDsAreAllAdmitted(t *testing.T) {
	const callers = 12
	for round := range 100 {
		l := newLane(t)
		var c crew
		start := make(chan struct{})
		results := make([]error, callers)
		for i := range callers {
			c.run(func() {
				<-start
				shuffle(rngFor(uint64(round), uint64(i)), 20)
				_, results[i] = l.admitWith(context.Background(), Spec{ID: fmt.Sprintf("id-%02d", i)})
			})
		}
		close(start)
		c.wait(t)

		for i, err := range results {
			if err != nil {
				t.Fatalf("round %d: Admit of a distinct id (caller %d): %v", round, i, err)
			}
		}
		infos := l.reg.Snapshot()
		if len(infos) != callers {
			t.Fatalf("round %d: the registry holds %d entries, want %d", round, len(infos), callers)
		}
		for i, info := range infos {
			if want := fmt.Sprintf("id-%02d", i); info.ID != want {
				t.Fatalf("round %d: Snapshot[%d] is %q, want %q", round, i, info.ID, want)
			}
		}
	}
}

func TestTheLimitOfAGroupHoldsWhenManyAdmitAtOnce(t *testing.T) {
	const callers, limit = 16, 3
	for round := range 50 {
		l := newLane(t)
		var c crew
		start := make(chan struct{})
		results := make([]error, callers)
		for i := range callers {
			c.run(func() {
				<-start
				shuffle(rngFor(uint64(round), uint64(i)), 20)
				_, results[i] = l.admitWith(context.Background(), Spec{ID: fmt.Sprintf("s-%02d", i), Group: "g", Limit: limit})
			})
		}
		close(start)
		c.wait(t)

		wins := 0
		for i, err := range results {
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ErrLimit):
			default:
				t.Fatalf("round %d: caller %d: %v", round, i, err)
			}
		}
		if wins != limit {
			t.Fatalf("round %d: %d Admits of a group limited to %d were admitted", round, wins, limit)
		}
		// The refused ones registered nothing.
		if n := l.reg.Len(); n != limit {
			t.Fatalf("round %d: the registry holds %d entries, want %d", round, n, limit)
		}
		if got := l.reg.groups["g"]; got != limit {
			t.Fatalf("round %d: the group counts %d entries, want %d", round, got, limit)
		}
	}
}

// The limit holds while entries of the group end and are admitted again from many goroutines:
// every Admit that passes checks it under the lock that counts, and none ever sees the group
// full and admits.
func TestTheLimitOfAGroupHoldsWhileItsEntriesEndAndAreAdmittedAgain(t *testing.T) {
	const workers, limit, cycles = 8, 4, 300
	l := newLane(t)
	var (
		c     crew
		found findings
	)
	for w := range workers {
		c.run(func() {
			rng := rngFor(11, uint64(w))
			for n := range cycles {
				spec := Spec{ID: fmt.Sprintf("w%d-%d", w, n), Group: "g", Limit: limit}
				e, err := l.admitInit(context.Background(), spec, func() {
					// Under the lock: the group must have room for this entry.
					if got := l.reg.groups["g"]; got >= limit {
						found.add("init ran with %d live entries in a group limited to %d", got, limit)
					}
				})
				if err != nil {
					if !errors.Is(err, ErrLimit) {
						found.add("Admit: %v", err)
					}
					shuffle(rng, 10)
					continue
				}
				shuffle(rng, 10)
				e.End(CauseFinished)
			}
		})
	}
	// A reader takes snapshots all the while: no snapshot has more than the limit.
	stop, read := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(read)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if n := countGroup(l.reg.Snapshot(), "g"); n > limit {
				found.add("a snapshot holds %d entries of a group limited to %d", n, limit)
			}
			runtime.Gosched()
		}
	}()
	c.wait(t)
	close(stop)
	awaitClosed(t, read, "the snapshot reader")
	found.report(t)
}

func countGroup(infos []Info, group string) int {
	n := 0
	for _, info := range infos {
		if info.Group == group {
			n++
		}
	}
	return n
}

// An entry that has ended still holds its id and its place in its group until its end function
// has returned: the registry releases them after, not when End is called.
func TestTheIDAndTheLimitSlotAreHeldUntilTheEndFunctionReturned(t *testing.T) {
	l := newLane(t)
	gate, inEnd := make(chan struct{}), make(chan struct{})
	l.endHook = func(e *Entry[*state]) {
		if e.ID() == "a" {
			close(inEnd)
			<-gate
		}
	}
	spec := Spec{ID: "a", Group: "g", Limit: 1}
	a, err := l.admitWith(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if !a.End(CauseRequested) {
		t.Fatal("the first End did not win")
	}
	awaitClosed(t, inEnd, "the end function to start")

	// During the end function: the id is reserved, the slot is taken, the entry is still there.
	if _, err := l.admitWith(context.Background(), spec); !errors.Is(err, ErrExists) {
		t.Fatalf("Admit of the id during its end function: %v, want %v", err, ErrExists)
	}
	if _, err := l.admitWith(context.Background(), Spec{ID: "b", Group: "g", Limit: 1}); !errors.Is(err, ErrLimit) {
		t.Fatalf("Admit into the full group during an end function: %v, want %v", err, ErrLimit)
	}
	if infos := l.reg.Snapshot(); len(infos) != 1 || infos[0].ID != "a" || infos[0].Cause != CauseRequested {
		t.Fatalf("Snapshot during the end function: %v, want the ended entry", infos)
	}

	close(gate)
	l.awaitRemoved("a")
	// Afterwards both are free: the slot, and the id.
	if _, err := l.admitWith(context.Background(), Spec{ID: "b", Group: "g", Limit: 1}); err != nil {
		t.Fatalf("Admit into the group after the entry was removed: %v", err)
	}
}

func TestAnIDCanBeAdmittedAgainOnceItsEntryIsRemoved(t *testing.T) {
	l := newLane(t)
	for range 50 {
		e := l.admit("again")
		e.End(CauseFinished)
		l.awaitEnd(e)
		l.awaitRemoved("again")
	}
	if n := l.endCount("again"); n != 50 {
		t.Fatalf("the end function ran %d times for 50 admissions", n)
	}
}

func TestAnAdmitRefusedByInitRegistersNothing(t *testing.T) {
	l := newLane(t)
	boom := errors.New("boom")
	var seen context.Context
	spec := Spec{ID: "a", Group: "g", Limit: 1}
	e, err := l.reg.Admit(context.Background(), spec, func(ctx context.Context) (*state, error) {
		seen = ctx
		return nil, boom
	})
	if !errors.Is(err, boom) || e != nil {
		t.Fatalf("Admit with a failing init returned (%v, %v), want (nil, %v)", e, err, boom)
	}
	if seen.Err() == nil {
		t.Fatal("the context init was given was not cancelled after it failed")
	}
	if n := l.reg.Len(); n != 0 {
		t.Fatalf("a refused Admit registered %d entries", n)
	}
	// The slot and the id are free.
	if _, err := l.admitWith(context.Background(), spec); err != nil {
		t.Fatalf("Admit after one that init refused: %v", err)
	}
}

// init runs under the registry's lock: a panic in it must not leave the registry locked, or
// holding the half-admitted entry.
func TestAPanicInInitLeavesTheRegistryUsable(t *testing.T) {
	l := newLane(t)
	got := recovered(func() {
		_, _ = l.reg.Admit(context.Background(), Spec{ID: "a", Group: "g", Limit: 1}, func(context.Context) (*state, error) {
			panic("init panicked")
		})
	})
	if got != "init panicked" {
		t.Fatalf("Admit let init's panic %v through, want it unchanged", got)
	}
	if n := l.reg.Len(); n != 0 {
		t.Fatalf("a panicking init left %d entries", n)
	}
	l.admit("a") // the lock is free, and so are the id and the slot
}

func TestAdmitWithoutAnInitAdmitsAnEntryWithoutState(t *testing.T) {
	r := New(Config[int]{})
	e, err := r.Admit(context.Background(), Spec{ID: "a"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if e.Value() != 0 {
		t.Fatalf("an entry admitted without init holds %d", e.Value())
	}
	ctx, cancel := context.WithTimeout(context.Background(), mustHappen)
	defer cancel()
	if err := r.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestAdmitPanicsOnAnEmptyID(t *testing.T) {
	r := New(Config[int]{})
	if got := recovered(func() { _, _ = r.Admit(context.Background(), Spec{}, nil) }); got == nil {
		t.Fatal("Admit with an empty id did not panic")
	}
	if n := r.Len(); n != 0 {
		t.Fatalf("a panicking Admit registered %d entries", n)
	}
}

func TestAnEntryHoldsTheStateInitBuiltAndItsID(t *testing.T) {
	r := New(Config[*string]{})
	text := "state"
	e, err := r.Admit(context.Background(), Spec{ID: "a", Group: "g"}, func(context.Context) (*string, error) { return &text, nil })
	if err != nil {
		t.Fatal(err)
	}
	if e.ID() != "a" || e.Value() != &text {
		t.Fatalf("entry holds (%q, %p), want (%q, %p)", e.ID(), e.Value(), "a", &text)
	}
	if infos := r.Snapshot(); len(infos) != 1 || infos[0] != (Info{ID: "a", Group: "g"}) {
		t.Fatalf("Snapshot: %v", infos)
	}
	e.End(CauseFinished)
	ctx, cancel := context.WithTimeout(context.Background(), mustHappen)
	defer cancel()
	if err := r.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

// New accepts no end function: an entry that ends is removed, and nothing is announced.
func TestARegistryWithoutAnEndFunctionStillRemovesWhatEnds(t *testing.T) {
	r := New(Config[struct{}]{})
	e, err := r.Admit(context.Background(), Spec{ID: "a"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.End(CauseFinished)
	awaitClosed(t, e.Done(), "Done")
	if !poll(mustHappen, func() bool { return r.Len() == 0 }) {
		t.Fatal("the entry that ended was not removed")
	}
}

// The parent context.

func TestACancellableParentEndingEndsTheEntryWithCauseCancelled(t *testing.T) {
	l := newLane(t)
	parent, cancel := context.WithCancel(context.Background())
	e, err := l.admitWith(parent, Spec{ID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if e.Cause() != CauseNone {
		t.Fatalf("a running entry has cause %v", e.Cause())
	}
	cancel()
	l.awaitEnd(e)
	if got := e.Cause(); got != CauseCancelled {
		t.Fatalf("the entry ended with %v when its parent ended, want %v", got, CauseCancelled)
	}
}

// The entry's context is cancelled only by End, so a context that is done always means End
// was called and the cause is recorded. The parent's cancellation reaches the entry through the
// watcher's End, not around it: held there, with the cause recorded, the context is not done.
func TestTheEntryContextIsNeverDoneBeforeEndRecordsTheCause(t *testing.T) {
	l := newLane(t)
	parent, cancel := context.WithCancel(context.Background())
	e, err := l.admitWith(parent, Spec{ID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	reached, release := l.pauses.arm(atEndRecorded, "a")
	defer release()

	cancel()
	awaitClosed(t, reached, "the watcher's End to record the cause")
	if got := e.Cause(); got != CauseCancelled {
		t.Fatalf("the cause is %v while the watcher's End is held after recording it, want %v", got, CauseCancelled)
	}
	if err := e.Context().Err(); err != nil {
		t.Fatalf("the entry's context is done (%v) before End cancelled it: its parent's cancellation went around End", err)
	}
	select {
	case <-e.Ended():
		t.Fatal("Ended is closed before End finished")
	default:
	}
	release()
	l.awaitEnd(e)
	if e.Context().Err() == nil {
		t.Fatal("the entry's context is not done after the entry ended")
	}
}

func TestTheEntryContextCarriesTheValuesOfItsParent(t *testing.T) {
	type key struct{}
	l := newLane(t)
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, "value"))
	defer cancel()
	e, err := l.admitWith(parent, Spec{ID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.Context().Value(key{}); got != "value" {
		t.Fatalf("the entry's context carries %v for the parent's value, want %q", got, "value")
	}
}

// An entry whose parent can never be cancelled costs no goroutine. One whose parent can costs
// exactly one, its watcher, which ends with the entry.
func TestAParentThatCanNeverBeCancelledStartsNoGoroutine(t *testing.T) {
	l := newLane(t)
	cancellable, cancel := context.WithCancel(context.Background())
	defer cancel()
	type key struct{}
	parents := map[string]context.Context{
		"background":     context.Background(),
		"todo":           context.TODO(),
		"with value":     context.WithValue(context.Background(), key{}, 1),
		"without cancel": context.WithoutCancel(cancellable),
	}
	baseline := lifecycletest.TakeBaseline()
	var entries []*Entry[*state]
	for name, parent := range parents {
		for i := range 25 {
			e, err := l.admitWith(parent, Spec{ID: fmt.Sprintf("%s-%d", name, i)})
			if err != nil {
				t.Fatal(err)
			}
			entries = append(entries, e)
		}
	}
	if err := baseline.Check(mustHappen / 50); err != nil {
		t.Fatalf("admitting entries whose parent can never be cancelled started goroutines: %v", err)
	}
	for _, e := range entries {
		e.End(CauseFinished)
	}
	for _, e := range entries {
		l.awaitEnd(e)
	}

	// The control: a parent that can be cancelled does cost a watcher each, and the baseline
	// check sees them.
	var watched []*Entry[*state]
	for i := range 25 {
		e, err := l.admitWith(cancellable, Spec{ID: fmt.Sprintf("watched-%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		watched = append(watched, e)
	}
	if err := baseline.Check(mustHappen / 50); err == nil {
		t.Fatal("25 entries with a cancellable parent started no goroutine: the check cannot see a watcher")
	}
	for _, e := range watched {
		e.End(CauseFinished)
	}
	for _, e := range watched {
		l.awaitEnd(e)
	}
	if err := baseline.Check(mustHappen); err != nil {
		t.Fatalf("the watchers outlived their entries: %v", err)
	}
}

// The parent ending and an explicit End race: the first one wins, in either order, and the
// entry ends once.
func TestTheParentEndingRacingAnExplicitEndTheFirstToRecordWins(t *testing.T) {
	t.Run("End first", func(t *testing.T) {
		l := newLane(t)
		parent, cancel := context.WithCancel(context.Background())
		e, err := l.admitWith(parent, Spec{ID: "a"})
		if err != nil {
			t.Fatal(err)
		}
		reached, release := l.pauses.arm(atEndRecorded, "a")
		defer release()

		var c crew
		c.run(func() { e.End(CauseRequested) })
		awaitClosed(t, reached, "End to record its cause")
		cancel()                                          // the parent ends while End is between recording and finishing
		awaitBlocked(t, 1, endFrame, "(*Mutex).lockSlow") // the watcher's End waits behind the first
		release()
		c.wait(t)
		l.awaitEnd(e)
		if got := e.Cause(); got != CauseRequested {
			t.Fatalf("cause %v, want %v: the End that recorded first must win", got, CauseRequested)
		}
	})
	t.Run("parent first", func(t *testing.T) {
		l := newLane(t)
		parent, cancel := context.WithCancel(context.Background())
		e, err := l.admitWith(parent, Spec{ID: "a"})
		if err != nil {
			t.Fatal(err)
		}
		reached, release := l.pauses.arm(atEndRecorded, "a")
		defer release()

		cancel() // the watcher's End records CauseCancelled and is held there
		awaitClosed(t, reached, "the watcher's End to record its cause")
		var (
			c     crew
			first bool
		)
		c.run(func() { first = e.End(CauseRequested) })
		awaitBlocked(t, 1, endFrame, "(*Mutex).lockSlow") // held behind the first End
		release()
		c.wait(t)
		l.awaitEnd(e)
		if first {
			t.Fatal("an explicit End reported first after the parent's cancellation recorded its cause")
		}
		if got := e.Cause(); got != CauseCancelled {
			t.Fatalf("cause %v, want %v: the End that recorded first must win", got, CauseCancelled)
		}
	})
}
