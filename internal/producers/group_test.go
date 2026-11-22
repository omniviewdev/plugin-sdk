package producers

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// settle is long enough for a goroutine that can make progress to have done so.
const settle = 30 * time.Millisecond

// mustHappen bounds the waits for something that has to happen.
const mustHappen = 5 * time.Second

// waitInBackground runs g.Wait in a goroutine and returns a channel closed when it returns.
func waitInBackground(g *Group) <-chan struct{} {
	returned := make(chan struct{})
	go func() {
		g.Wait()
		close(returned)
	}()
	return returned
}

func awaitClosed(t *testing.T, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(mustHappen):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// Every interleaving of Start, Done and Wait that decides whether a producer may join an
// owner, forced one at a time.

func TestGroupIsOpenUntilTheOwnerWaits(t *testing.T) {
	var g Group
	if !g.Start(1) || !g.Start(2) {
		t.Fatal("an open group refused a producer")
	}
	g.Done()
	g.Done()
	g.Done()
	if !g.Start(1) {
		t.Fatal("a group that has not been waited for refused a producer after its producers returned")
	}
}

func TestGroupWaitReturnsOnlyWhenTheLastProducerReturns(t *testing.T) {
	var g Group
	g.Start(2)
	returned := waitInBackground(&g)

	awaitWaiting(t, &g)
	assertStillWaiting(t, returned, "Wait returned with two producers running")
	g.Done()
	assertStillWaiting(t, returned, "Wait returned with one producer running")
	g.Done()
	awaitClosed(t, returned, "Wait to return after the last producer")
}

func TestGroupWaitWithNoProducerReturnsAtOnce(t *testing.T) {
	var g Group
	awaitClosed(t, waitInBackground(&g), "Wait on an empty group")
}

// A closing group, one the owner waits for while a producer still runs, admits producers: a
// watcher may add a source and an update may restart one, because the running producer keeps
// the session alive.
func TestGroupAdmitsWhileClosingAndAProducerRuns(t *testing.T) {
	var g Group
	g.Start(1)
	returned := waitInBackground(&g)
	awaitWaiting(t, &g) // the group is closing: Wait has begun and a producer runs

	if !g.Start(1) {
		t.Fatal("a closing group with a running producer refused a producer")
	}
	g.Done() // the first producer returns; the second is still running
	assertStillWaiting(t, returned, "Wait returned while the producer admitted during the wait was still running")
	g.Done()
	awaitClosed(t, returned, "Wait to return after the admitted producer")
}

// The last producer returning while the owner waits ends the group: nothing is admitted after
// that, and the end is decided under the lock that admits.
func TestGroupRefusesOnceTheLastProducerReturnedWhileClosing(t *testing.T) {
	var g Group
	g.Start(1)
	returned := waitInBackground(&g)
	awaitWaiting(t, &g) // the last producer returns while the group is closing, not before
	g.Done()
	awaitClosed(t, returned, "Wait to return")

	if g.Start(1) {
		t.Fatal("an ended group admitted a producer")
	}
	if g.running != 0 {
		t.Fatalf("a refused Start left %d producers registered", g.running)
	}
}

// When the last producer returned before the owner began to wait, the wait ends the group at
// once.
func TestGroupRefusesAfterAWaitThatFoundNoProducer(t *testing.T) {
	var g Group
	g.Start(1)
	g.Done()
	awaitClosed(t, waitInBackground(&g), "Wait after the last producer returned")

	if g.Start(1) {
		t.Fatal("an ended group admitted a producer")
	}
}

func TestGroupStartIsAllOrNothing(t *testing.T) {
	var g Group
	awaitClosed(t, waitInBackground(&g), "Wait on an empty group")
	if g.Start(3) {
		t.Fatal("an ended group admitted three producers")
	}
	if g.running != 0 {
		t.Fatalf("a refused Start registered %d of its 3 producers", g.running)
	}

	var open Group
	if !open.Start(3) || open.running != 3 {
		t.Fatalf("an open group registered %d of 3 producers", open.running)
	}
}

func TestGroupWaitMayBeCalledAgainAndByManyAtOnce(t *testing.T) {
	var g Group
	g.Start(1)
	first, second := waitInBackground(&g), waitInBackground(&g)
	awaitWaiting(t, &g)
	assertStillWaiting(t, first, "a Wait returned with a producer running")
	assertStillWaiting(t, second, "a Wait returned with a producer running")
	g.Done()
	awaitClosed(t, first, "the first Wait")
	awaitClosed(t, second, "the second Wait")
	awaitClosed(t, waitInBackground(&g), "a Wait on an ended group")
}

func TestGroupPanicsOnAProducerThatWasNeverStarted(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Done without Start did not panic")
		}
	}()
	var g Group
	g.Done()
}

// Freeze holds every Start, Done and Wait, and lets them go when it is released.
func TestGroupFreezeHoldsEveryCallUntilItIsReleased(t *testing.T) {
	var g Group
	thaw := g.Freeze()

	started := make(chan bool, 1)
	go func() { started <- g.Start(1) }()
	select {
	case <-started:
		t.Fatal("Start returned while the group was frozen")
	case <-time.After(settle):
	}
	thaw()
	select {
	case ok := <-started:
		if !ok {
			t.Fatal("Start was refused by an open group once it was thawed")
		}
	case <-time.After(mustHappen):
		t.Fatal("Start did not return after the group was thawed")
	}
}

// Producers start and finish from many goroutines while the owner waits at an arbitrary
// moment. Whatever the interleaving: when Wait returns no producer is running, and no producer
// is admitted afterwards. Run under -race.
func TestGroupUnderContention(t *testing.T) {
	const workers = 4
	for round := range 20 {
		var (
			g      Group
			active atomic.Int64
			ended  atomic.Bool
			wg     sync.WaitGroup
			stop   = make(chan struct{})
		)
		for range workers {
			wg.Go(func() {
				for {
					select {
					case <-stop:
						return
					default:
					}
					wasEnded := ended.Load()
					if !g.Start(1) {
						continue
					}
					if wasEnded {
						t.Error("a producer was admitted after Wait had returned")
					}
					active.Add(1)
					active.Add(-1)
					g.Done()
				}
			})
		}
		time.Sleep(time.Duration(round%7) * 10 * time.Microsecond) // the Wait lands at a different moment each round
		g.Wait()
		ended.Store(true)
		if n := active.Load(); n != 0 {
			t.Errorf("Wait returned with %d producers running", n)
		}
		time.Sleep(50 * time.Microsecond)
		close(stop)
		wg.Wait()
		if g.running != 0 {
			t.Fatalf("%d producers left registered", g.running)
		}
	}
}

// awaitWaiting returns once Wait has marked the group closing, which it does under the lock
// before it blocks: from then on a test that starts or finishes a producer does so to a
// closing group, not to a group the waiting goroutine has not reached yet.
func awaitWaiting(t *testing.T, g *Group) {
	t.Helper()
	deadline := time.Now().Add(mustHappen)
	for {
		g.mu.Lock()
		waiting := g.waiting
		g.mu.Unlock()
		if waiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for Wait to start")
		}
		time.Sleep(time.Millisecond)
	}
}

// assertStillWaiting fails when returned is closed. A wait that has not returned cannot be
// proved by waiting, so this gives a returning Wait a moment to show itself: it can miss a
// Wait that returns late, and it never fails a correct one.
func assertStillWaiting(t *testing.T, returned <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-returned:
		t.Fatal(msg)
	case <-time.After(settle):
	}
}
