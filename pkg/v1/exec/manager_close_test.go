package exec_test

// Manager.Close and the starts of sessions, one interleaving at a time.
//
// A start passes four places on its way to a session: the check that the manager is open,
// the handler (native work: a terminal, a process), admission (the session is registered and
// counted), and the launch of the session's goroutines. Close can arrive between any two of
// them. The tests below stop a start at the places where it matters, with the seams a Manager
// offers (its handler, its logger), run Close there, and check what each side gets.

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
)

var bothKinds = []startKind{startLegacy, startResource}

// gate stops goroutines at a place until the test lets them go.
type gate struct {
	reached chan struct{}
	open    chan struct{}
	once    sync.Once
	opened  sync.Once
}

func newGate(t testing.TB) *gate {
	g := &gate{reached: make(chan struct{}), open: make(chan struct{})}
	t.Cleanup(g.release) // a test that fails while a goroutine waits must not leave it there
	return g
}

// hold is called at the place: it reports that a goroutine arrived and waits for release.
func (g *gate) hold() {
	g.once.Do(func() { close(g.reached) })
	<-g.open
}

func (g *gate) release() { g.opened.Do(func() { close(g.open) }) }

// A start after Close is refused, with the refusal, and before it does any native work.
func TestAStartAfterCloseIsRefusedBeforeAnyNativeWork(t *testing.T) {
	for _, kind := range bothKinds {
		t.Run(kind.String(), func(t *testing.T) {
			r := newRig(t)
			r.closeManager()

			var handlerCalls atomic.Int64
			hook := func(exec.SessionOptions) { handlerCalls.Add(1) }
			r.inHandler.Store(&hook)

			session, err := r.start(kind, "late", nil)
			if !isRefusal(err) || session != nil {
				t.Fatalf("a start after Close returned (%+v, %v); want the refusal CONNECTION_ERROR", session, err)
			}
			if calls, terminals := handlerCalls.Load(), r.terms.created.Load(); calls != 0 || terminals != 0 {
				t.Errorf("the refused start did native work: %d handler calls, %d terminals opened", calls, terminals)
			}
			r.assertDrained(t)
		})
	}
}

// Close may arrive after a start has done its native work and before it is admitted: the start
// is refused when it gets there, and what it opened is closed, because nothing else will.
func TestAStartThatLosesTheRaceWithCloseIsRefusedAndLeavesNothingBehind(t *testing.T) {
	for _, kind := range bothKinds {
		t.Run(kind.String(), func(t *testing.T) {
			r := newRig(t)
			handler := newGate(t)
			hook := func(exec.SessionOptions) { handler.hold() }
			r.inHandler.Store(&hook)

			started := r.startAsync(kind, "loser")
			awaitClosed(t, handler.reached, "the start to reach its handler")
			r.closeManager() // the start is not admitted: Close has nothing to wait for, and returns
			handler.release()

			result := <-started
			if !isRefusal(result.err) || result.session != nil {
				t.Fatalf("a start that was between its handler and its admission when Close ran returned (%+v, %v); want the refusal CONNECTION_ERROR",
					result.session, result.err)
			}
			r.assertDrained(t) // its terminal is closed, and the process its handler started has exited
			if r.terms.created.Load() != 1 || r.procs.started.Load() != 1 {
				t.Errorf("the start was meant to have opened a terminal and started a process: %d, %d", r.terms.created.Load(), r.procs.started.Load())
			}
		})
	}
}

// Close may arrive after a session is admitted and before its goroutines exist. Close cancels
// it, and must not return until it is gone: a session that is registered and counted is
// Close's to wait for, whether or not the start that admitted it has finished.
func TestCloseWaitsForASessionItAdmittedThatIsNotRunningYet(t *testing.T) {
	for _, kind := range bothKinds {
		t.Run(kind.String(), func(t *testing.T) {
			r := newRig(t)
			launch := newGate(t)
			hook := func(msg string) {
				if msg == "session created" { // logged after admission, before the goroutines start
					launch.hold()
				}
			}
			r.onLog.Store(&hook)

			started := r.startAsync(kind, "admitted")
			awaitClosed(t, launch.reached, "the start to admit its session")
			closed := r.closeAsync()
			assertStillBlocked(t, closed, "Close returned while a session it had to cancel was admitted and not running yet")
			launch.release()

			if result := <-started; result.err != nil {
				t.Fatalf("a start admitted before Close began failed: %v", result.err)
			}
			awaitClosed(t, closed, "Close to return once the session it cancelled has ended")
			r.assertDrained(t)
			if admitted := r.admitted.Load(); admitted != 1 {
				t.Errorf("admitted %d sessions, want 1", admitted)
			}
		})
	}
}

// Two starts of one ID meet at admission, where the loser is refused for the ID: the refusal
// must leave nothing registered, or Close would wait for a session that does not exist.
func TestAStartThatLosesItsIDAtAdmissionLeavesNothingToWaitFor(t *testing.T) {
	r := newRig(t)
	var arrived sync.WaitGroup
	arrived.Add(2)
	both := make(chan struct{})
	hook := func(exec.SessionOptions) {
		arrived.Done()
		<-both
	}
	r.inHandler.Store(&hook)

	first, second := r.startAsync(startLegacy, "same"), r.startAsync(startLegacy, "same")
	bothArrived := make(chan struct{})
	go func() { arrived.Wait(); close(bothArrived) }()
	awaitClosed(t, bothArrived, "both starts to reach their handler, past the check that the ID is free")
	close(both)

	results := []startResult{<-first, <-second}
	var admitted, taken int
	for _, result := range results {
		switch {
		case result.err == nil:
			admitted++
		case errors.Is(result.err, exec.ErrSessionExists):
			taken++
		default:
			t.Errorf("a start of an ID that was being started failed with %v", result.err)
		}
	}
	if admitted != 1 || taken != 1 {
		t.Fatalf("two starts of one ID: %d admitted, %d refused for the ID; want one each", admitted, taken)
	}

	closed := r.closeAsync()
	awaitClosed(t, closed, "Close to return: the refused start left a session for it to wait for")
	r.assertDrained(t)
}

// Every Close waits for the sessions, not only the first: a second caller that found the
// manager already closing and returned at once would report a manager that was still running.
func TestEveryConcurrentCloseWaitsForTheSessions(t *testing.T) {
	r := newRig(t)
	held := make(chan struct{})
	var unhold sync.Once
	release := func() { unhold.Do(func() { close(held) }) }
	t.Cleanup(release)        // runs before the manager's own Close, which would wait on a held terminal
	r.terms.gate.Store(&held) // the sessions cannot end while their terminals cannot close
	for i := range 3 {
		if _, err := r.start(bothKinds[i%2], string(rune('a'+i)), nil); err != nil {
			t.Fatal(err)
		}
	}

	closes := make([]<-chan struct{}, 4)
	for i := range closes {
		closes[i] = r.closeAsync()
	}
	for _, c := range closes {
		assertStillBlocked(t, c, "a Close returned while sessions were still running")
	}
	release()
	for _, c := range closes {
		awaitClosed(t, c, "every Close to return once the sessions have ended")
	}
	r.assertDrained(t)

	r.closeManager() // and a Close after it is a no-op that returns
}

// A start can reach admission while Close is still waiting for the sessions it cancelled. The
// group is closing then, not ended, and would take it: it is the manager's own flag, set before
// Close waits, that refuses it. Without that, the session would be admitted after Close had
// cancelled the sessions, nobody would cancel it, and Close would wait for it for ever.
func TestAStartThatReachesAdmissionWhileCloseIsWaitingIsRefused(t *testing.T) {
	for _, kind := range bothKinds {
		t.Run(kind.String(), func(t *testing.T) {
			r := newRig(t)
			held := make(chan struct{})
			var unhold sync.Once
			release := func() { unhold.Do(func() { close(held) }) }
			t.Cleanup(release)
			r.terms.gate.Store(&held) // the first session cannot end: Close has to wait for it
			if _, err := r.start(startLegacy, "running", nil); err != nil {
				t.Fatal(err)
			}
			r.terms.gate.Store(nil) // the terminals of later starts close at once

			handler := newGate(t)
			hook := func(opts exec.SessionOptions) {
				if opts.ID == "late" {
					handler.hold()
				}
			}
			r.inHandler.Store(&hook)
			started := r.startAsync(kind, "late") // passes the check that the manager is open
			awaitClosed(t, handler.reached, "the start to reach its handler")

			closed := r.closeAsync() // sets closed, cancels "running", and waits for it
			assertStillBlocked(t, closed, "Close returned while a session could not end")
			handler.release() // the start reaches admission while Close is waiting

			result := <-started
			if !isRefusal(result.err) || result.session != nil {
				t.Errorf("a start that reached admission while Close was waiting returned (%+v, %v); want the refusal CONNECTION_ERROR",
					result.session, result.err)
			}
			release()
			awaitClosed(t, closed, "Close to return once the session it waited for has ended")
			r.assertDrained(t)
		})
	}
}

// Starts of one ID admit exactly one session, whatever the number of callers, and the
// starts of different IDs admit every one.
func TestConcurrentStartsOfOneIDAdmitExactlyOne(t *testing.T) {
	for _, kind := range bothKinds {
		t.Run(kind.String(), func(t *testing.T) {
			r := newRig(t)
			const callers = 16
			begin := make(chan struct{})
			results := make(chan startResult, callers)
			for range callers {
				go func() {
					<-begin
					session, err := r.start(kind, "contested", nil)
					results <- startResult{session, err}
				}()
			}
			close(begin)
			admitted := 0
			for range callers {
				result := <-results
				switch {
				case result.err == nil:
					admitted++
				case errors.Is(result.err, exec.ErrSessionExists):
				default:
					t.Errorf("a start of a contested ID failed with %v", result.err)
				}
			}
			if sessions, _ := r.m.ListSessions(nil); admitted != 1 || len(sessions) != 1 {
				t.Errorf("%d starts of one ID admitted %d sessions, %d are registered; want exactly one", callers, admitted, len(sessions))
			}
			r.closeManager()
			r.assertDrained(t)
		})
	}
}

func TestConcurrentStartsOfDifferentIDsAdmitEvery(t *testing.T) {
	for _, kind := range bothKinds {
		t.Run(kind.String(), func(t *testing.T) {
			r := newRig(t)
			const callers = 16
			begin := make(chan struct{})
			var wg sync.WaitGroup
			for i := range callers {
				wg.Go(func() {
					<-begin
					if _, err := r.start(kind, "id-"+string(rune('a'+i)), nil); err != nil {
						t.Errorf("a start of its own ID failed with %v", err)
					}
				})
			}
			close(begin)
			wg.Wait()
			if sessions, _ := r.m.ListSessions(nil); len(sessions) != callers {
				t.Errorf("%d registered sessions, want %d", len(sessions), callers)
			}
			r.closeManager()
			r.assertDrained(t)
		})
	}
}

// The Close signal is the last output of a session. Output that the reader is still delivering
// when the session ends is delivered first, and nothing follows the signal: a consumer that
// releases what it holds for a session when it sees the signal must not be handed more.
func TestOutputNeverFollowsTheCloseSignal(t *testing.T) {
	r := newRig(t)
	const id = "slow-consumer"
	if _, err := r.start(startLegacy, id, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.m.AttachSession(nil, id); err != nil {
		t.Fatal(err)
	}
	consumer := newGate(t)
	r.sink.holdData.Store(consumer) // the consumer is slow to take the session's output

	if _, err := r.procs.get(id).terminal.InjectOutput([]byte("last words")); err != nil {
		t.Fatal(err)
	}
	awaitClosed(t, consumer.reached, "the reader to hand the output to the consumer")

	closed := make(chan error, 1)
	go func() { closed <- r.m.CloseSession(nil, id) }() // the session ends while its output is in flight
	select {
	case <-time.After(settle):
	case err := <-closed:
		t.Fatalf("CloseSession returned (%v) while the session's output was still being delivered", err)
	}
	if signals := r.sink.closeSignals.Load(); signals != 0 {
		t.Errorf("the Close signal was delivered while the session's last output was still being delivered; that output would now follow it")
	}

	consumer.release()
	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("CloseSession: %v", err)
		}
	case <-time.After(mustHappen):
		t.Fatal("CloseSession did not return once the output was delivered")
	}
	r.assertDrained(t) // includes: no output after the session's Close signal
}
