package exec

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/session"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// Author code that can be stopped only by returning is the one thing the engine cannot take back: the function of a command
// that ignores its cancelled context, an Options callback that never returns, a Classify that hangs. The engine gives each a
// bound (the grace, the kill wait, a deadline), and when the bound passes it gives up on the call: the command ends and the start
// or the call is refused without it, loudly. The goroutine is still there, still holding what it holds, so giving up is not free,
// and a host that can start another command the moment the last was abandoned could make the plugin leak a goroutine, a queue and
// a client for each, without end.
//
// So an abandoned call keeps its place until it returns. For a command that is its place among the target's sessions
// (internal/session: Entry.Retain): the target's declared maximum is then a bound on the commands that run and the calls that
// were abandoned together, a start that the abandoned calls alone make too many is refused UNAVAILABLE / TARGET_WEDGED and
// counted, and what a stuck target can leak is its declared maximum, not the number of times a host asked. For the callback of a
// unary Options call, which has no session, it is the place it holds among the Options callbacks of its target that are in
// flight (optionsFlight), capped for the same reason (defaultOptionsInFlight).
//
// A function that never returns keeps its place until the plugin restarts: the target is then dead for that kind of call, and
// nothing a host does changes it. That is why the refusal has a reason of its own, and why the engine says so in the log at the
// moment it gives up on a function, and not only when a host asks again.

// The states of an authorCall.
const (
	callRunning = iota
	callReturned
	callAbandoned
)

// authorCall is the engine's account of one call of author code: it is running, it returned, or the engine gave up on it
// and it has not returned yet. The two decisions, to give up and to return, are made under one lock, so that exactly one of
// them is first and the other knows it.
type authorCall struct {
	e *engine
	// hold keeps the place of the call when the engine gives up on it, and returns the release of that place, which the call's
	// own goroutine runs when it returns. done, when set, is the last thing that goroutine does, whether the engine gave up on
	// it or not.
	hold func() (release func())
	done func()

	mu      sync.Mutex
	state   int
	release func() // the place of the call that was kept when it was given up on
}

// newAuthorCall returns the account of a call of the author code of a command: its place is its entry's.
func (e *engine) newAuthorCall(entry *session.Entry[*command]) *authorCall {
	return &authorCall{e: e, hold: entry.Retain}
}

// markReturned is what the author's goroutine does when it returns, before it reports anything: it settles which of the two
// came first. When the engine had given up already, it returns the release of the place that was kept for it, which the
// goroutine hands to settle once it has told the engine what it returned.
func (a *authorCall) markReturned() (abandoned bool, release func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	abandoned, release = a.state == callAbandoned, a.release
	a.state = callReturned
	return abandoned, release
}

// settle is the author's goroutine's last act: a call that was given up on has returned, so its place is free and it is no
// longer counted.
func (a *authorCall) settle(abandoned bool, release func()) {
	if abandoned {
		if release != nil {
			release()
		}
		a.e.tel.abandonedChanged(-1)
	}
	if a.done != nil {
		a.done()
	}
}

// giveUp is the engine's decision to stop waiting. It reports true when the call was still running: the engine gave up on it,
// its place is kept and the call is counted. It reports false when the call had returned first.
func (a *authorCall) giveUp() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != callRunning {
		return false
	}
	a.state = callAbandoned
	a.release = a.hold()
	a.e.tel.abandonedChanged(1)
	return true
}

// callBounded runs fn, author code that has no way to be stopped but to return, on a goroutine of its own, and waits for its
// answer until ctx is done and then for at most grace more (the shutdown grace when the engine is closing). It returns the
// answer, or false when the engine gave up on the call: the goroutine goes on, the call keeps its place until it returns, and the
// caller refuses what it was doing. fn must not panic: the author code inside it recovers its own panics, because a goroutine
// that nobody waits for cannot be recovered from outside.
func callBounded[T any](call *authorCall, ctx context.Context, grace time.Duration, fn func() T) (answer T, ok bool) {
	answered := make(chan T, 1)
	go func() {
		v := fn()
		abandoned, release := call.markReturned()
		if hook := call.e.afterCallReturned; hook != nil {
			hook()
		}
		answered <- v // buffered: the caller may be gone
		call.settle(abandoned, release)
	}()
	select {
	case v := <-answered:
		return v, true
	case <-ctx.Done():
	}
	wait := call.e.newGraceWait(grace, false)
	defer wait.stop()
	closingC := call.e.closingC
	for expired := false; !expired; {
		select {
		case v := <-answered:
			return v, true
		case <-closingC:
			closingC = nil
			wait.closing()
		case <-wait.grace:
			expired = true
		case <-wait.shutdown:
			expired = true
		}
	}
	gaveUp := call.giveUp()
	if hook := call.e.afterAbandonDecision; hook != nil {
		hook(gaveUp)
	}
	if gaveUp {
		var zero T
		return zero, false
	}
	return <-answered, true // it returned while the engine decided: its answer stands
}

// graceWait is the time that author code which was told to stop has to return. It ends at the first of the bounds that apply:
// the grace of an ordinary end; the kill wait, counted from a kill and only for a command that was killed; and the shutdown
// grace, counted from the moment the engine began to close, which replaces the grace for a plugin that is stopping. A bound is
// armed when what sets it happens, so a kill or a close that comes while the grace runs cuts the wait short and never makes it
// longer. A channel that has not been armed is nil, which a select never takes.
type graceWait struct {
	e        *engine
	grace    <-chan time.Time // the grace of an ordinary end
	kill     <-chan time.Time // the kill wait
	shutdown <-chan time.Time // the shutdown grace
	stops    []func()
}

// newGraceWait starts the wait of an end with the grace. A command that was killed already has no grace, only the kill wait,
// and an engine that is closing already has the shutdown grace and not the grace: the caller arms those with killed and
// closing when it sees the kill and the close, which it does at once for the ones that happened before.
func (e *engine) newGraceWait(grace time.Duration, killed bool) *graceWait {
	w := &graceWait{e: e}
	if !killed && !e.closing() {
		w.grace = w.arm(grace)
	}
	return w
}

func (w *graceWait) arm(d time.Duration) <-chan time.Time {
	c, stop := w.e.timer(d)
	w.stops = append(w.stops, stop)
	return c
}

// killed is the kill: the function has the kill wait from now.
func (w *graceWait) killed() { w.kill = w.arm(w.e.cfg.killWait) }

// closing is the engine's close: the function has the shutdown grace from now.
func (w *graceWait) closing() { w.shutdown = w.arm(w.e.cfg.shutdownGrace) }

// stop releases the timers.
func (w *graceWait) stop() {
	for _, stop := range w.stops {
		stop()
	}
}

// timer returns a timer of d: the channel that it runs out on, and its stop. A test replaces it (EngineSeams.NewTimer).
func (e *engine) timer(d time.Duration) (c <-chan time.Time, stop func()) {
	if e.newTimer != nil {
		return e.newTimer(d)
	}
	t := time.NewTimer(d)
	return t.C, func() { t.Stop() }
}

// closing reports whether the engine has begun to close.
func (e *engine) closing() bool {
	select {
	case <-e.closingC:
		return true
	default:
		return false
	}
}

// clock is the time that the log limits count in.
func (e *engine) clock() time.Time {
	if e.now != nil {
		return e.now()
	}
	return time.Now()
}

// logLimit decides when a line that a host could provoke without end is written: the first time for a key, and after that at
// most once a second. A host that retries a refused start in a loop, or that kills and starts again as fast as it can, would
// write a line each time; the counters say how often it happened.
type logLimit struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// allow reports whether the line of key may be written at now, and counts it as written when it may.
func (l *logLimit) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if last, written := l.last[key]; written && now.Sub(last) < time.Second {
		return false
	}
	if l.last == nil {
		l.last = map[string]time.Time{}
	}
	l.last[key] = now
	return true
}

// optionsFlight is what one target has of the unary Options RPC in flight: the callbacks that have not returned, and, of those,
// the ones the engine gave up on. Both are guarded by the engine's flightMu.
type optionsFlight struct {
	running, abandoned int
}

var (
	// errOptionsInFlight is the local cause of a call that the cap refused, and errOptionsHung of one whose callback was given
	// up on. They are logged and never sent.
	errOptionsInFlight = errors.New("the target has as many Options callbacks in flight as the SDK allows")
	errOptionsHung     = errors.New("the options callback did not return within its deadline and the grace after its context ended")
)

// enterOptions admits one call of the Options RPC to run the callback of t, and returns the account of that callback: it is in
// flight until it returns, and when the engine gives up on it, it is in flight and abandoned. A call that would be one more than
// the target's cap is refused UNAVAILABLE and counted: with TARGET_WEDGED when a callback in flight was given up on and has not
// returned (nothing a retry does changes that), and with no reason when every one is still inside its bound (the target is busy).
func (e *engine) enterOptions(ctx context.Context, t *registeredTarget) (*authorCall, error) {
	e.flightMu.Lock()
	f := e.flights[t]
	if f == nil {
		if e.flights == nil {
			e.flights = map[*registeredTarget]*optionsFlight{}
		}
		f = &optionsFlight{}
		e.flights[t] = f
	}
	if f.running >= e.cfg.optionsInFlight {
		inFlight, abandoned := f.running, f.abandoned
		e.flightMu.Unlock()
		e.optionsRefused(ctx, t, inFlight, abandoned)
		if abandoned > 0 {
			return nil, NewError(ErrorClassUnavailable, ErrorReasonTargetWedged, errOptionsInFlight)
		}
		return nil, &Error{Class: ErrorClassUnavailable, Err: errOptionsInFlight}
	}
	f.running++
	e.flightMu.Unlock()
	return &authorCall{
		e: e,
		hold: func() func() {
			e.flightMu.Lock()
			f.abandoned++
			e.flightMu.Unlock()
			return func() {
				e.flightMu.Lock()
				f.abandoned--
				e.flightMu.Unlock()
			}
		},
		done: func() {
			e.flightMu.Lock()
			f.running--
			e.flightMu.Unlock()
		},
	}, nil
}

// optionsCall runs the author's Options callback for one call of the Options RPC, inside the connection's lease that bound
// stands for. The callback is bounded as the one of a start is (boundedOptions): its context ends at the engine's deadline and
// when the caller's or the lease's ends, and it then has the grace to return. A callback that does not is given up on, which
// ends this function: the caller of this function releases the lease, the call is refused, and the callback keeps its place in
// flight until it returns. The refusal is the caller's own cancellation or timeout when the caller has gone, and TIMEOUT
// otherwise; the RPC turns a TIMEOUT into CONNECTION_RETIRED when the lease was cancelled while the caller still waited.
func (e *engine) optionsCall(caller context.Context, t *registeredTarget, bound *types.PluginContext, run func(*types.PluginContext) optionsAnswer) ([]Option, error) {
	call, err := e.enterOptions(caller, t)
	if err != nil {
		return nil, err
	}
	answer, ok := e.boundedOptions(call, bound, run)
	if !ok {
		e.optionsHung(caller, t)
		if err := caller.Err(); err != nil {
			return nil, contextError(err)
		}
		return nil, &Error{Class: ErrorClassTimeout, Err: errOptionsHung}
	}
	return answer.options, answer.err
}

// optionsHung counts the Options callback of a call that was given up on, and says so in the plugin's log: the first time, and
// at most once a second for each target after that.
func (e *engine) optionsHung(ctx context.Context, t *registeredTarget) {
	e.tel.violation(violationOptionsHung)
	if !e.logs.allow("options_hung\x00"+t.info.Provider+"\x00"+t.info.Type, e.clock()) {
		return
	}
	e.log.Warnw(ctx, "an exec function broke the contract", "violation", violationOptionsHung,
		"detail", "the options callback did not return within its deadline and the grace after its context ended: the call is refused without it",
		"provider", t.info.Provider, "type", t.info.Type, "kind", "options")
}

// optionsRefused counts a call that the cap on the Options callbacks of a target refused, and says so in the log: the first time
// and at most once a second for each target after that.
func (e *engine) optionsRefused(ctx context.Context, t *registeredTarget, inFlight, abandoned int) {
	e.tel.optionsRefused()
	if !e.logs.allow("options_refused\x00"+t.info.Provider+"\x00"+t.info.Type, e.clock()) {
		return
	}
	e.log.Warnw(ctx, "exec options refused: the target has too many Options callbacks in flight",
		"provider", t.info.Provider, "type", t.info.Type, "in_flight", inFlight, "abandoned", abandoned)
}
