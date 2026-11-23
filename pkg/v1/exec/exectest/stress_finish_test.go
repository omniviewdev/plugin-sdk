package exectest

import (
	"context"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/internal/lifecycletest"
)

// What the stress says of the end of a session is held to what happened, and an attach that a session's end refuses is one of the things that
// happen: the workers go on attaching to a session after it ended, and each attach takes a window, is refused and gives it back, at any
// moment, including the moment at which the check of the finished session looks at the window. These tests run the check by hand with an
// attach made at each of those moments, which the hooks of the stress place exactly, so that nothing depends on how the goroutines run.

// newBareRound is a round over the real engine, as execute builds it before the workers start, for a test that drives the parts of a round by hand. The
// engine is closed and the round retired when the test ends.
func newBareRound(t *testing.T, hooks stressHooks) *stressRound {
	t.Helper()
	if hooks.engine == nil {
		hooks.engine = realEngine
	}
	cfg := lifecycletest.Config{Workers: 1, Duration: time.Second, Seed: 1}.WithDefaults()
	r := newStressRound(t, hooks, cfg, 1, time.Second)
	if err := r.build(); err != nil {
		t.Fatalf("build the round: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cfg.CloseBound)
		defer cancel()
		r.closing.Store(true)
		_ = r.eng.Close(ctx)
		r.gates.open()
		r.stopReaders()
		_ = r.readers.WaitContext(ctx)
		r.conns.Retire()
	})
	return r
}

// finishedSession is a session of the round that has ended, with one window that holds no stream.
type finishedSession struct {
	c *stressCommand
	s *sessionState
	w *window
}

func newFinishedSession(r *stressRound) finishedSession {
	s := &sessionState{rec: &sessionRec{id: "s1"}}
	c := &stressCommand{id: "s1", key: "s1#1", b: behaviourNamed("echo"), sess: s}
	w := s.newWindow(c, 0, false)
	w.state.Store(winIdle)
	return finishedSession{c: c, s: s, w: w}
}

func (r *stressRound) violations() []string { return r.found.list() }

// An attach that a session's end refuses takes the window at the moment the check looks at it, and gives it back, and takes it again: the
// check waits for each, and then completes the window. It never reports a window that an attach was holding for a moment as a window that
// holds a stream. (The window's first look is the one that a second attach spoiled: the check read it idle, and the attach took it before the
// check did anything with what it read.)
func TestTheCheckOfAFinishedSessionWaitsForTheAttachesThatItsEndRefuses(t *testing.T) {
	var calls int
	var f finishedSession
	r := newBareRound(t, stressHooks{finishLooked: func(w *window) {
		calls++
		switch calls {
		case 1:
			if !w.state.CompareAndSwap(winIdle, winBusy) { // an attach is made after the end: it takes the window
				t.Errorf("the check had seen the window idle and an attach could not take it")
			}
		case 2:
			w.state.Store(winIdle) // it was refused: it gives the window back
		case 3:
			w.state.CompareAndSwap(winIdle, winBusy) // and another attach takes it, as the check is about to take it itself
		case 4:
			w.state.Store(winIdle)
		}
	}})
	f = newFinishedSession(r)

	r.finishWindow(f.c, f.w, exechook.Result{})
	if v := r.violations(); len(v) != 0 {
		t.Fatalf("the check of a finished session said %q of a window that an attach held for a moment, which is no stream", v)
	}
	if got := f.w.state.Load(); got != winEnded {
		t.Fatalf("the window is %s after the check, want ended: nothing takes it again", windowStateName(got))
	}
	if calls < 5 {
		t.Fatalf("the check looked at the window %d times, want it to wait through both attaches and look at least five times", calls)
	}
}

// The check of a finished session owns the window it completes from the tombstone, as an attach does the window it makes a stream for:
// an attach that comes meanwhile cannot take it, and so never reads or writes the transcript that the check is writing.
func TestTheCheckOfAFinishedSessionOwnsTheWindowItCompletes(t *testing.T) {
	var f finishedSession
	var got *window
	rng := rand.New(rand.NewPCG(1, 2))
	r := newBareRound(t, stressHooks{finishCompleting: func(w *window) {
		got = f.s.takeWindow(f.c, rng, false) // an attach after the end, while the check is completing the window
	}})
	f = newFinishedSession(r)

	r.finishWindow(f.c, f.w, exechook.Result{})
	if got == nil {
		t.Fatal("the check did not reach the completion of the window")
	}
	if got == f.w {
		t.Fatal("an attach took the window that the check was completing from the tombstone: two goroutines write one transcript")
	}
	if v := r.violations(); len(v) != 0 {
		t.Fatalf("the check said %q of a session that ended with no stream", v)
	}
}

// A window that still holds a stream once every stream of the session returned is a violation, and the message says whose window and what it
// holds. So is a window that an attach never gave back.
func TestTheCheckOfAFinishedSessionReportsAWindowThatStillHoldsAStreamOrAnAttach(t *testing.T) {
	for _, tc := range []struct {
		name   string
		state  int32
		phrase []string
	}{
		{"a stream that never returned", winStreaming, []string{"s1: window 0", "still has a stream", "every stream of the session returned"}},
		{"an attach that never gave the window back", winBusy, []string{"s1: window 0", "still held by an attach", "after the session ended"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newBareRound(t, stressHooks{})
			r.cfg.CloseBound = time.Microsecond // the wait for an attach to give the window back ends at once
			f := newFinishedSession(r)
			f.w.state.Store(tc.state)

			r.finishWindow(f.c, f.w, exechook.Result{})
			v := r.violations()
			if len(v) != 1 {
				t.Fatalf("the check said %q, want one violation", v)
			}
			for _, phrase := range tc.phrase {
				if !strings.Contains(v[0], phrase) {
					t.Errorf("the check said %q, want it to say %q", v[0], phrase)
				}
			}
		})
	}
}

// A window whose stream was told Ended has seen the end of the session: there is nothing to complete and nothing to wait for.
func TestTheCheckOfAFinishedSessionLeavesAWindowThatSawTheEndAlone(t *testing.T) {
	completing := 0
	r := newBareRound(t, stressHooks{finishCompleting: func(*window) { completing++ }})
	r.cfg.CloseBound = time.Microsecond // a wait for this window would end at once, and say so
	f := newFinishedSession(r)
	f.w.state.Store(winEnded)

	r.finishWindow(f.c, f.w, exechook.Result{})
	if v := r.violations(); len(v) != 0 || completing != 0 || f.w.state.Load() != winEnded {
		t.Fatalf("the check said %q and completed %d windows, and the window is %s: want nothing said, nothing completed, the window left ended", v, completing, windowStateName(f.w.state.Load()))
	}
}

// A violation of the stress is said with what a rerun needs: the seed, the workers and the duration of the run, and what a rerun cannot promise (the
// choices of a round are its seed's, how its goroutines interleave is not), so that a failure that shows up on a busy machine is not a mystery.
func TestAFailureOfTheStressSaysHowToRunItAgain(t *testing.T) {
	rec := stressWithFault(t, faultLive)
	if !rec.said("round 0", "found", "violations", "StressConfig{Seed: 7, Workers: 4, Duration: 100ms}", "go test -race -count", "how its goroutines interleave is not") {
		t.Fatalf("the stress said %q of an engine that leaves a ghost behind, want a line that says how to run the round again", rec.failed())
	}
}

// The stress costs what it runs for, and the conformance of every plugin author runs it three times: two seconds by default, one under -short, and
// what the environment says when it says (the hammer's own default then reads it).
func TestTheStressRunsForTwoSecondsByDefaultAndForWhatTheEnvironmentSays(t *testing.T) {
	for _, tc := range []struct {
		name    string
		env     time.Duration
		short   bool
		want    time.Duration
		comment string
	}{
		{"nothing says", 0, false, 2 * time.Second, "two rounds"},
		{"go test -short", 0, true, time.Second, "one"},
		{"the environment says", 30 * time.Second, false, 0, "left to the hammer's default, which reads the environment"},
		{"the environment says under -short", 30 * time.Second, true, 0, "the environment wins"},
	} {
		if got := stressDefaultDuration(tc.env, tc.short); got != tc.want {
			t.Errorf("%s: the default is %v, want %v (%s)", tc.name, got, tc.want, tc.comment)
		}
	}
}
