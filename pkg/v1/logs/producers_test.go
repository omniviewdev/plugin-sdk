package logs

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// settle is long enough for a goroutine that can make progress to have done so.
const settle = 30 * time.Millisecond

// waitInBackground runs g.wait in a goroutine and returns a channel closed when it returns.
func waitInBackground(g *producerGroup) <-chan struct{} {
	returned := make(chan struct{})
	go func() {
		g.wait()
		close(returned)
	}()
	return returned
}

func isClosed(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

func awaitClosed(t *testing.T, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(lifecycleWait):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// Every interleaving of start, done and wait that decides whether an update may
// join a session, forced one at a time.

func TestProducerGroupIsOpenUntilTheOrchestratorWaits(t *testing.T) {
	var g producerGroup
	if !g.start(1) || !g.start(2) {
		t.Fatal("an open group refused a producer")
	}
	g.done()
	g.done()
	g.done()
	if !g.start(1) {
		t.Fatal("a group that has not been waited for refused a producer after its producers returned")
	}
}

func TestProducerGroupWaitReturnsOnlyWhenTheLastProducerReturns(t *testing.T) {
	var g producerGroup
	g.start(2)
	returned := waitInBackground(&g)

	time.Sleep(settle)
	if isClosed(returned) {
		t.Fatal("wait returned with two producers running")
	}
	g.done()
	time.Sleep(settle)
	if isClosed(returned) {
		t.Fatal("wait returned with one producer running")
	}
	g.done()
	awaitClosed(t, returned, "wait to return after the last producer")
}

func TestProducerGroupWaitWithNoProducerReturnsAtOnce(t *testing.T) {
	var g producerGroup
	awaitClosed(t, waitInBackground(&g), "wait on an empty group")
}

// A closing group, one the orchestrator waits for while a producer still runs, admits
// producers: a watcher may add a source and an update may restart one, because the
// running producer keeps the session alive.
func TestProducerGroupAdmitsWhileClosingAndAProducerRuns(t *testing.T) {
	var g producerGroup
	g.start(1)
	returned := waitInBackground(&g)
	time.Sleep(settle)

	if !g.start(1) {
		t.Fatal("a closing group with a running producer refused a producer")
	}
	g.done() // the first producer returns; the second is still running
	time.Sleep(settle)
	if isClosed(returned) {
		t.Fatal("wait returned while the producer admitted during the wait was still running")
	}
	g.done()
	awaitClosed(t, returned, "wait to return after the admitted producer")
}

// The last producer returning while the orchestrator waits ends the group: nothing
// is admitted after that, and the end is decided under the lock that admits.
func TestProducerGroupRefusesOnceTheLastProducerReturnedWhileClosing(t *testing.T) {
	var g producerGroup
	g.start(1)
	returned := waitInBackground(&g)
	time.Sleep(settle)
	g.done()
	awaitClosed(t, returned, "wait to return")

	if g.start(1) {
		t.Fatal("an ended group admitted a producer")
	}
	if g.running != 0 {
		t.Fatalf("a refused start left %d producers registered", g.running)
	}
}

// When the last producer returned before the orchestrator began to wait, the wait
// ends the group at once.
func TestProducerGroupRefusesAfterAWaitThatFoundNoProducer(t *testing.T) {
	var g producerGroup
	g.start(1)
	g.done()
	awaitClosed(t, waitInBackground(&g), "wait after the last producer returned")

	if g.start(1) {
		t.Fatal("an ended group admitted a producer")
	}
}

func TestProducerGroupStartIsAllOrNothing(t *testing.T) {
	var g producerGroup
	awaitClosed(t, waitInBackground(&g), "wait on an empty group")
	if g.start(3) {
		t.Fatal("an ended group admitted three producers")
	}
	if g.running != 0 {
		t.Fatalf("a refused start registered %d of its 3 producers", g.running)
	}

	var open producerGroup
	if !open.start(3) || open.running != 3 {
		t.Fatalf("an open group registered %d of 3 producers", open.running)
	}
}

func TestProducerGroupWaitMayBeCalledAgainAndByManyAtOnce(t *testing.T) {
	var g producerGroup
	g.start(1)
	first, second := waitInBackground(&g), waitInBackground(&g)
	time.Sleep(settle)
	if isClosed(first) || isClosed(second) {
		t.Fatal("a wait returned with a producer running")
	}
	g.done()
	awaitClosed(t, first, "the first wait")
	awaitClosed(t, second, "the second wait")
	awaitClosed(t, waitInBackground(&g), "a wait on an ended group")
}

func TestProducerGroupPanicsOnAProducerThatWasNeverStarted(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("done without start did not panic")
		}
	}()
	var g producerGroup
	g.done()
}

// Producers start and finish from many goroutines while the orchestrator waits at an
// arbitrary moment. Whatever the interleaving: when wait returns no producer is
// running, and no producer is admitted afterwards. Run under -race.
func TestProducerGroupUnderContention(t *testing.T) {
	const workers = 4
	for round := range 20 {
		var (
			g      producerGroup
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
					if !g.start(1) {
						continue
					}
					if wasEnded {
						t.Error("a producer was admitted after wait had returned")
					}
					active.Add(1)
					active.Add(-1)
					g.done()
				}
			})
		}
		time.Sleep(time.Duration(round%7) * 10 * time.Microsecond) // the wait lands at a different moment each round
		g.wait()
		ended.Store(true)
		if n := active.Load(); n != 0 {
			t.Errorf("wait returned with %d producers running", n)
		}
		time.Sleep(50 * time.Microsecond)
		close(stop)
		wg.Wait()
		if g.running != 0 {
			t.Fatalf("%d producers left registered", g.running)
		}
	}
}
