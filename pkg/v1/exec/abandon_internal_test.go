package exec

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The engine's account of a call of author code, and of what a target has of the unary Options RPC in flight, one rule at a
// time and without a goroutine: the decisions that the engine's tests reach only through a race.

// timerLog is the timers of an engine, recorded: the durations the engine armed, in order, and how many it stopped.
type timerLog struct {
	armed   []time.Duration
	stopped int
}

func (l *timerLog) install(e *engine) {
	e.newTimer = func(d time.Duration) (<-chan time.Time, func()) {
		l.armed = append(l.armed, d)
		return make(chan time.Time), func() { l.stopped++ }
	}
}

// A call that returned before the engine gave up is never given up on, and holds nothing.
func TestAnAuthorCallThatReturnedBeforeTheEngineGaveUpIsNeverGivenUpOn(t *testing.T) {
	e := newEngine(engineConfig{})
	held := 0
	call := &authorCall{e: e, hold: func() func() { held++; return func() {} }}
	abandoned, release := call.markReturned()
	if abandoned || release != nil {
		t.Fatalf("markReturned of a call that was never given up on: abandoned %v release %v", abandoned, release != nil)
	}
	if call.giveUp() {
		t.Error("the engine gave up on a call that had returned")
	}
	if held != 0 || e.tel.abandonedCount() != 0 {
		t.Errorf("a call that returned holds %d places and is counted %d times as abandoned, want none", held, e.tel.abandonedCount())
	}
}

// A call that was given up on keeps its place and is counted until it returns; when it returns, the place is released once, the
// count goes down, and the goroutine's last act runs.
func TestAnAuthorCallThatWasGivenUpOnKeepsItsPlaceUntilItReturnsAndThenFreesItOnce(t *testing.T) {
	e := newEngine(engineConfig{})
	held, released, done := 0, 0, 0
	call := &authorCall{e: e,
		hold: func() func() { held++; return func() { released++ } },
		done: func() { done++ },
	}
	if !call.giveUp() {
		t.Fatal("the engine could not give up on a call that was running")
	}
	if call.giveUp() {
		t.Error("the engine gave up on a call twice")
	}
	if held != 1 || e.tel.abandonedCount() != 1 || released != 0 {
		t.Fatalf("a call given up on: held %d, counted %d, released %d, want 1, 1, 0", held, e.tel.abandonedCount(), released)
	}
	abandoned, release := call.markReturned()
	if !abandoned || release == nil {
		t.Fatalf("markReturned of a call that was given up on: abandoned %v release %v", abandoned, release != nil)
	}
	if released != 0 || done != 0 {
		t.Errorf("the place was released (%d) or the call finished (%d) before the goroutine said it settled", released, done)
	}
	call.settle(abandoned, release)
	if released != 1 || done != 1 || e.tel.abandonedCount() != 0 {
		t.Errorf("a call given up on that returned: released %d, done %d, counted %d, want 1, 1, 0", released, done, e.tel.abandonedCount())
	}
}

// A call that was not given up on and returned releases nothing and is not counted, and its goroutine's last act still runs.
func TestAnAuthorCallThatReturnedInTimeSettlesWithoutReleasingOrCounting(t *testing.T) {
	e := newEngine(engineConfig{})
	released, done := 0, 0
	call := &authorCall{e: e, hold: func() func() { return func() { released++ } }, done: func() { done++ }}
	abandoned, release := call.markReturned()
	call.settle(abandoned, release)
	if released != 0 || done != 1 || e.tel.abandonedCount() != 0 {
		t.Errorf("released %d, done %d, counted %d, want 0, 1, 0", released, done, e.tel.abandonedCount())
	}
}

func flightOf(e *engine, target *registeredTarget) optionsFlight {
	e.flightMu.Lock()
	defer e.flightMu.Unlock()
	if f := e.flights[target]; f != nil {
		return *f
	}
	return optionsFlight{}
}

// What a target has in flight is the callbacks that have not returned, the ones given up on included, and the cap is on that
// number. The refusal over the cap is UNAVAILABLE, with the reason TARGET_WEDGED only while a callback in flight was given up on
// and has not returned; a callback that returns, given up on or not, makes room.
func TestTheOptionsFlightOfATargetCountsCallbacksUntilTheyReturnAndSaysWhenOneWasGivenUpOn(t *testing.T) {
	e := newEngine(engineConfig{optionsInFlight: 2})
	target, other := &registeredTarget{}, &registeredTarget{}
	ctx := context.Background()
	first, err := e.enterOptions(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.enterOptions(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if got := flightOf(e, target); got != (optionsFlight{running: 2}) {
		t.Fatalf("two callbacks in flight: %+v", got)
	}

	refusal := func() *Error {
		t.Helper()
		_, err := e.enterOptions(ctx, target)
		var typed *Error
		if !errors.As(err, &typed) {
			t.Fatalf("a call over the cap: %v, want an *Error", err)
		}
		return typed
	}
	if got := refusal(); got.Class != ErrorClassUnavailable || got.Reason != ErrorReasonUnspecified {
		t.Errorf("a call over the cap, every callback inside its bound: %v/%v, want UNAVAILABLE with no reason", got.Class, got.Reason)
	}
	if got := flightOf(e, target); got != (optionsFlight{running: 2}) {
		t.Errorf("a refused call changed what is in flight: %+v", got)
	}
	if _, err := e.enterOptions(ctx, other); err != nil {
		t.Errorf("another target was refused for this one's callbacks: %v", err)
	}

	if !first.giveUp() {
		t.Fatal("the engine could not give up on a running callback")
	}
	if got := flightOf(e, target); got != (optionsFlight{running: 2, abandoned: 1}) {
		t.Fatalf("one callback given up on: %+v, want it still in flight and abandoned", got)
	}
	if got := refusal(); got.Class != ErrorClassUnavailable || got.Reason != ErrorReasonTargetWedged {
		t.Errorf("a call over the cap while a callback was given up on: %v/%v, want UNAVAILABLE/TARGET_WEDGED", got.Class, got.Reason)
	}

	// The callback that was given up on returns: its place is free and it is no longer abandoned.
	abandoned, release := first.markReturned()
	first.settle(abandoned, release)
	if got := flightOf(e, target); got != (optionsFlight{running: 1}) {
		t.Fatalf("the abandoned callback returned: %+v, want one callback running and none abandoned", got)
	}
	// One that was never given up on returns: its place is free.
	abandoned, release = second.markReturned()
	second.settle(abandoned, release)
	if got := flightOf(e, target); got != (optionsFlight{}) {
		t.Fatalf("both callbacks returned: %+v, want none", got)
	}
	if _, err := e.enterOptions(ctx, target); err != nil {
		t.Errorf("a call once the callbacks returned: %v", err)
	}
}

// The bounds that apply to a function that was told to stop are armed when what sets them happens, and only those: a graceful
// end has the grace, a command already killed has the kill wait and no grace, an engine already closing has the shutdown grace
// and no grace, and a kill or a close that comes later adds its bound without taking the grace's away.
func TestAGraceWaitArmsTheBoundsThatApplyWhenWhatSetsThemHappens(t *testing.T) {
	const grace, killWait, shutdown = 7 * time.Second, 3 * time.Second, 2 * time.Second
	newTestEngine := func() (*engine, *timerLog) {
		e := newEngine(engineConfig{closeGrace: grace, killWait: killWait, shutdownGrace: shutdown})
		log := &timerLog{}
		log.install(e)
		return e, log
	}
	same := func(got, want []time.Duration) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	t.Run("a graceful end arms the grace", func(t *testing.T) {
		e, log := newTestEngine()
		w := e.newGraceWait(grace, false)
		if !same(log.armed, []time.Duration{grace}) || w.grace == nil || w.kill != nil || w.shutdown != nil {
			t.Errorf("armed %v grace %v kill %v shutdown %v", log.armed, w.grace != nil, w.kill != nil, w.shutdown != nil)
		}
		w.killed()
		w.closing()
		if !same(log.armed, []time.Duration{grace, killWait, shutdown}) || w.kill == nil || w.shutdown == nil {
			t.Errorf("after a kill and a close: armed %v, want the grace, the kill wait and the shutdown grace", log.armed)
		}
		w.stop()
		if log.stopped != 3 {
			t.Errorf("stop released %d timers, want 3", log.stopped)
		}
	})
	t.Run("a command that was killed has no grace", func(t *testing.T) {
		e, log := newTestEngine()
		w := e.newGraceWait(grace, true)
		if len(log.armed) != 0 || w.grace != nil {
			t.Errorf("armed %v for an end that was a kill already", log.armed)
		}
		w.killed()
		if !same(log.armed, []time.Duration{killWait}) {
			t.Errorf("the kill armed %v, want the kill wait", log.armed)
		}
	})
	t.Run("an engine that is closing has no grace", func(t *testing.T) {
		e, log := newTestEngine()
		e.closeOnce.Do(func() { close(e.closingC) })
		w := e.newGraceWait(grace, false)
		if len(log.armed) != 0 || w.grace != nil {
			t.Errorf("armed %v by an engine that was closing already", log.armed)
		}
		w.closing()
		if !same(log.armed, []time.Duration{shutdown}) {
			t.Errorf("the close armed %v, want the shutdown grace", log.armed)
		}
	})
}
