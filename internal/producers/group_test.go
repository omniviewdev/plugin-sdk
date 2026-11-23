package producers

import (
	"context"
	"errors"
	"math/rand/v2"
	"runtime"
	"strings"
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

// WaitContext is Wait with a way out. The tests below order the end of the group, the end of
// the context and the blocking itself without a timer. That a goroutine is blocked inside Wait
// or WaitContext is established by the runtime's own record of it (blockedIn), not by waiting
// and seeing nothing happen. A WaitContext that has to block asks its context for Done once,
// after it has released the group's lock and decided to block, so a context that runs a
// function when it is asked (hookCtx) puts an action at exactly that crossing.

func TestGroupWaitContextReturnsNilWhenTheGroupEndsFirst(t *testing.T) {
	var g Group
	g.Start(1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := waitContextInBackground(&g, ctx)
	awaitBlocked(t, "(*Group).WaitContext(", 1)

	g.Done() // the last producer returns while the context is live
	if err := awaitResult(t, result, "WaitContext after the last producer returned"); err != nil {
		t.Fatalf("WaitContext returned %v for a group that ended", err)
	}
	if g.Start(1) {
		t.Fatal("a group WaitContext reported ended admitted a producer")
	}
}

func TestGroupWaitContextReturnsTheContextErrorWhenTheContextEndsFirst(t *testing.T) {
	var g Group
	g.Start(1)
	ctx, cancel := context.WithCancel(context.Background())
	result := waitContextInBackground(&g, ctx)
	awaitBlocked(t, "(*Group).WaitContext(", 1)

	cancel() // the context ends while the producer still runs
	if err := awaitResult(t, result, "WaitContext after its context ended"); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitContext returned %v, want %v", err, context.Canceled)
	}
	assertClosing(t, &g, 1)

	// Giving up left the group as Wait leaves it: closing, its producer running, still admitting,
	// and ended by its last producer.
	if !g.Start(1) {
		t.Fatal("a closing group with a running producer refused a producer after WaitContext gave up")
	}
	later := waitInBackground(&g)
	awaitBlocked(t, "(*Group).Wait(", 1)
	g.Done()
	awaitBlocked(t, "(*Group).Wait(", 1) // a producer is left: the Wait is still blocked, not woken by this Done
	g.Done()
	awaitClosed(t, later, "a later Wait to return after the last producer")
	if g.Start(1) {
		t.Fatal("an ended group admitted a producer")
	}
}

// A context that is done when WaitContext looks at it gives up at once on a group that still
// has a producer, and leaves it closing: the producer's Done ends it.
func TestGroupWaitContextWithADoneContextOnARunningGroupGivesUpAndLeavesItClosing(t *testing.T) {
	var g Group
	g.Start(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := waitContext(t, &g, ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitContext returned %v, want %v", err, context.Canceled)
	}
	assertClosing(t, &g, 1)

	g.Done()
	if waiting, ended, running := stateOf(&g); !waiting || !ended || running != 0 {
		t.Fatalf("after the last Done: waiting=%v ended=%v running=%d, want an ended group", waiting, ended, running)
	}
	awaitClosed(t, waitInBackground(&g), "a Wait on the group the last Done ended")
}

// The end of the group is the answer whatever the context says: a group that has ended is
// reported ended even when the context is done.
func TestGroupWaitContextReportsAnEndedGroupWhateverItsContext(t *testing.T) {
	done, cancel := context.WithCancel(context.Background())
	cancel()
	for name, ctx := range map[string]context.Context{"done": done, "never done": context.Background()} {
		t.Run("context "+name, func(t *testing.T) {
			var g Group
			g.Start(1)
			g.Done()
			awaitClosed(t, waitInBackground(&g), "Wait on a group whose producer returned")
			if err := waitContext(t, &g, ctx); err != nil {
				t.Fatalf("WaitContext on an ended group returned %v", err)
			}
		})
	}
}

// A group with nothing to wait for ends at once, as it does for Wait, and the context is not
// consulted.
func TestGroupWaitContextOnAnEmptyGroupEndsItLikeWait(t *testing.T) {
	done, cancel := context.WithCancel(context.Background())
	cancel()
	for name, ctx := range map[string]context.Context{"done": done, "live": context.Background()} {
		t.Run("context "+name, func(t *testing.T) {
			var g Group
			if err := waitContext(t, &g, ctx); err != nil {
				t.Fatalf("WaitContext on a group with no producer returned %v", err)
			}
			if g.Start(1) {
				t.Fatal("a group WaitContext ended admitted a producer")
			}
		})
	}
}

// The last producer returns and the context ends while WaitContext has decided to block and
// has not yet looked: both are ready when it does, and the group's end is the answer. A
// choice between the two at random would show here as an error in about half of the rounds.
func TestGroupWaitContextReportsTheEndWhenTheContextEndsAtTheSameMoment(t *testing.T) {
	for round := range 200 {
		var g Group
		g.Start(1)
		ctx, cancel := context.WithCancel(context.Background())
		both := newHookCtx(ctx, func() {
			g.Done()
			cancel()
		})
		if err := waitContext(t, &g, both); err != nil {
			t.Fatalf("round %d: WaitContext returned %v with its group ended", round, err)
		}
	}
}

// The crossing between deciding to block and blocking, forced from either side: a group that
// ends there is not waited for in vain, and a context that ends there is not missed.
func TestGroupWaitContextAtTheCrossingBetweenDecidingToBlockAndBlocking(t *testing.T) {
	t.Run("the group ends", func(t *testing.T) {
		var g Group
		g.Start(1)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := waitContext(t, &g, newHookCtx(ctx, g.Done)); err != nil {
			t.Fatalf("WaitContext returned %v for a group that ended at the crossing", err)
		}
	})
	t.Run("the context ends", func(t *testing.T) {
		var g Group
		g.Start(1)
		ctx, cancel := context.WithCancel(context.Background())
		err := waitContext(t, &g, newHookCtx(ctx, cancel))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("WaitContext returned %v, want %v for a context that ended at the crossing", err, context.Canceled)
		}
		assertClosing(t, &g, 1)
	})
}

// WaitContext and Wait may be called by many goroutines at once, and each context decides for
// its own caller: one that gives up leaves the others waiting.
func TestGroupWaitContextAndWaitMayBeCalledByManyAtOnce(t *testing.T) {
	var g Group
	g.Start(1)
	quitCtx, quit := context.WithCancel(context.Background())
	defer quit()
	stayCtx, stop := context.WithCancel(context.Background())
	defer stop()
	givesUp := waitContextInBackground(&g, quitCtx)
	stays := waitContextInBackground(&g, stayCtx)
	plain := waitInBackground(&g)
	awaitBlocked(t, "(*Group).WaitContext(", 2)
	awaitBlocked(t, "(*Group).Wait(", 1)

	quit()
	if err := awaitResult(t, givesUp, "the WaitContext whose context ended"); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitContext returned %v, want %v", err, context.Canceled)
	}
	awaitBlocked(t, "(*Group).WaitContext(", 1) // the other one is still waiting
	awaitBlocked(t, "(*Group).Wait(", 1)

	g.Done()
	if err := awaitResult(t, stays, "the WaitContext that kept waiting"); err != nil {
		t.Fatalf("WaitContext returned %v for a group that ended", err)
	}
	awaitClosed(t, plain, "the Wait")
	if err := waitContext(t, &g, context.Background()); err != nil {
		t.Fatalf("WaitContext on the ended group returned %v", err)
	}
}

// waitContext runs g.WaitContext in a goroutine, so that a WaitContext that does not return
// fails the test instead of hanging it, and returns its result.
func waitContext(t *testing.T, g *Group, ctx context.Context) error {
	t.Helper()
	return awaitResult(t, waitContextInBackground(g, ctx), "WaitContext to return")
}

// Freeze holds WaitContext as it holds Wait.
func TestGroupFreezeHoldsWaitContextToo(t *testing.T) {
	var g Group
	thaw := g.Freeze()
	result := waitContextInBackground(&g, context.Background())
	awaitBlocked(t, "(*Group).WaitContext(", 1) // parked on the group's lock
	select {
	case err := <-result:
		t.Fatalf("WaitContext returned %v while the group was frozen", err)
	default:
	}
	thaw()
	if err := awaitResult(t, result, "WaitContext after the group was thawed"); err != nil {
		t.Fatalf("WaitContext on an empty group returned %v", err)
	}
}

// Producers start and finish from many goroutines while the owner waits with a context that
// ends at an arbitrary moment. Whatever the interleaving: a WaitContext that reports nil
// returns an ended group with nothing running, one that gives up reports its context's error,
// and either way the last producer's Done leaves the group ended. Run under -race.
func TestGroupWaitContextUnderContention(t *testing.T) {
	var ended, gaveUp int
	for round := range 60 {
		rng := rand.New(rand.NewPCG(uint64(round), 1))
		workers := 1 + rng.IntN(4)
		var g Group
		ctx, cancel := context.WithCancel(context.Background())
		stop := make(chan struct{})
		exited := make(chan struct{}, workers)
		for w := range workers {
			hold := rand.New(rand.NewPCG(uint64(round), uint64(w)+2)).IntN(8)
			go func() {
				defer func() { exited <- struct{}{} }()
				for {
					select {
					case <-stop:
						return
					default:
					}
					if g.Start(1) {
						for range hold {
							runtime.Gosched() // a producer that stays a while keeps the group closing
						}
						g.Done()
					}
				}
			}()
		}
		turns := rng.IntN(300)
		cancelled := make(chan struct{})
		go func() {
			defer close(cancelled)
			for range turns {
				runtime.Gosched()
			}
			cancel()
		}()

		err := g.WaitContext(ctx)
		switch {
		case err == nil:
			ended++
			if waiting, isEnded, running := stateOf(&g); !waiting || !isEnded || running != 0 {
				t.Errorf("round %d: WaitContext returned nil with waiting=%v ended=%v running=%d", round, waiting, isEnded, running)
			}
		case errors.Is(err, context.Canceled):
			gaveUp++
		default:
			t.Errorf("round %d: WaitContext returned %v", round, err)
		}

		close(stop)
		for range workers {
			awaitClosed(t, exited, "a producer goroutine to stop")
		}
		awaitClosed(t, cancelled, "the goroutine that ends the context")
		cancel()
		if waiting, isEnded, running := stateOf(&g); !waiting || !isEnded || running != 0 {
			t.Fatalf("round %d: after every producer returned: waiting=%v ended=%v running=%d, want an ended group", round, waiting, isEnded, running)
		}
	}
	t.Logf("WaitContext reported the end %d times and gave up %d times", ended, gaveUp)
}

// hookCtx runs onDone, once, when its Done is first asked for.
type hookCtx struct {
	context.Context
	once   *sync.Once
	onDone func()
}

func newHookCtx(parent context.Context, onDone func()) hookCtx {
	return hookCtx{Context: parent, once: new(sync.Once), onDone: onDone}
}

func (c hookCtx) Done() <-chan struct{} {
	c.once.Do(c.onDone)
	return c.Context.Done()
}

// waitContextInBackground runs g.WaitContext in a goroutine and returns the channel its
// result arrives on.
func waitContextInBackground(g *Group, ctx context.Context) <-chan error {
	result := make(chan error, 1)
	go func() { result <- g.WaitContext(ctx) }()
	return result
}

func awaitResult(t *testing.T, result <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(mustHappen):
		t.Fatalf("timed out waiting for %s", what)
		return nil
	}
}

// stateOf reads the group's state under its lock.
func stateOf(g *Group) (waiting, ended bool, running int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.waiting, g.ended, g.running
}

// assertClosing fails unless the group is closing with n producers running: the state Wait
// leaves a group in while it blocks, and the one a WaitContext that gives up leaves.
func assertClosing(t *testing.T, g *Group, n int) {
	t.Helper()
	if waiting, ended, running := stateOf(g); !waiting || ended || running != n {
		t.Fatalf("group: waiting=%v ended=%v running=%d, want closing with %d running", waiting, ended, running, n)
	}
}

// awaitBlocked returns once exactly n goroutines are blocked with frame in their stack, and
// fails when that does not happen in time. A goroutine the runtime has parked has not returned
// from the function it is parked in, which no amount of waiting can show.
func awaitBlocked(t *testing.T, frame string, n int) {
	t.Helper()
	if !poll(mustHappen, func() bool { return blockedIn(frame) == n }) {
		t.Fatalf("timed out waiting for %d goroutines blocked in %s; %d are", n, frame, blockedIn(frame))
	}
}

// poll calls cond until it reports true, pausing a little longer between the calls, and
// reports whether that happened before the limit. It watches a state; it never decides by
// waiting.
func poll(limit time.Duration, cond func() bool) bool {
	end := time.Now().Add(limit)
	pause := 10 * time.Microsecond
	for !cond() {
		if time.Now().After(end) {
			return false
		}
		runtime.Gosched()
		timer := time.NewTimer(pause)
		<-timer.C
		if pause < 2*time.Millisecond {
			pause *= 2
		}
	}
	return true
}

// blockedIn counts the goroutines that the runtime has parked (in a select, on a channel, on a
// lock) and that have frame in their stack.
func blockedIn(frame string) int {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	count := 0
	for _, goroutine := range strings.Split(string(buf), "\n\n") {
		head, _, _ := strings.Cut(goroutine, "\n") // goroutine 7 [select]:
		open := strings.IndexByte(head, '[')
		if open < 0 || !strings.Contains(goroutine, frame) {
			continue
		}
		switch state := head[open+1:]; {
		case strings.HasPrefix(state, "running"), strings.HasPrefix(state, "runnable"), strings.HasPrefix(state, "syscall"):
		default:
			count++
		}
	}
	return count
}
