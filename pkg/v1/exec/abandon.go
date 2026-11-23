package exec

import (
	"context"
	"sync"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/session"
)

// Author code that can be stopped only by returning is the one thing the engine cannot take back: the function of a command
// that ignores its cancelled context, an Options callback that never returns, a Classify that hangs. The engine gives each a
// bound (the grace, a deadline), and when the bound passes it gives up on the call: the command ends and the start is refused
// without it, loudly. The goroutine is still there, still holding what it holds, so giving up is not free, and a host that can
// start another command the moment the last was abandoned could make the plugin leak a goroutine, a queue and a client for
// each, without end.
//
// So an abandoned call keeps its command's place among the target's sessions until it returns (internal/session: Entry.Retain).
// The target's declared maximum is then a bound on the commands that run and the calls that were abandoned together, a start
// that the abandoned calls alone make too many is refused UNAVAILABLE and counted, and what a stuck target can leak is its
// declared maximum, not the number of times a host asked.

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
	e     *engine
	entry *session.Entry[*command]

	mu      sync.Mutex
	state   int
	release func() // the place of the entry that was kept when the call was given up on
}

func (e *engine) newAuthorCall(entry *session.Entry[*command]) *authorCall {
	return &authorCall{e: e, entry: entry}
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
	if !abandoned {
		return
	}
	if release != nil {
		release()
	}
	a.e.tel.abandonedChanged(-1)
}

// giveUp is the engine's decision to stop waiting. It reports true when the call was still running: the engine gave up on it,
// its entry's place is kept and the call is counted. It reports false when the call had returned first.
func (a *authorCall) giveUp() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != callRunning {
		return false
	}
	a.state = callAbandoned
	a.release = a.entry.Retain()
	a.e.tel.abandonedChanged(1)
	return true
}

// callBounded runs fn, author code that has no way to be stopped but to return, on a goroutine of its own, and waits for its
// answer until ctx is done and then for at most grace more. It returns the answer, or false when the engine gave up on the
// call: the goroutine goes on, its entry keeps its place until it returns, and the caller refuses what it was doing. fn must
// not panic: the author code inside it recovers its own panics, because a goroutine that nobody waits for cannot be
// recovered from outside.
func callBounded[T any](c *command, ctx context.Context, grace time.Duration, fn func() T) (answer T, ok bool) {
	call := c.e.newAuthorCall(c.entry)
	answered := make(chan T, 1)
	go func() {
		v := fn()
		abandoned, release := call.markReturned()
		answered <- v // buffered: the caller may be gone
		call.settle(abandoned, release)
	}()
	select {
	case v := <-answered:
		return v, true
	case <-ctx.Done():
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case v := <-answered:
		return v, true
	case <-timer.C:
	}
	if call.giveUp() {
		var zero T
		return zero, false
	}
	return <-answered, true // it returned while the engine decided: its answer stands
}
