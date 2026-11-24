package exectest

import (
	"context"
)

// The canary is the flow of a session that outlives its stream, played once by every worker at the start of every round on a session of
// its own that the dice never touch, with every answer awaited: the dice reach each op, but only by chance each flow, and a flow that has to
// work is not a thing to leave to chance. An echo is fed over a stream, a second host takes the session over, the frames of the stream that
// left are sent (and must do nothing: the session still echoes), the host detaches and the output written while nobody was attached is in
// the replay of the attach that follows, a stream breaks and the session lives on, and a session that ends with no stream attached is
// ended by EndSession and told to no stream. Every wait is for an event that has to happen, and ends when the engine is closing: a
// Close that lands in the middle explains anything.

// canary plays the flow. The session it starts is the worker's afterwards, for the dice to dispose of.
func (w *stressWorker) canary() {
	r := w.r
	rng := w.rng
	before := len(w.pool)
	w.calm = true
	w.start(behaviourNamed("echo"), false, "", false)
	w.calm = false
	if len(w.pool) == before {
		return // refused: the engine is closing
	}
	c := w.pool[len(w.pool)-1]
	w.pool = w.pool[:before]
	defer func() { w.pool = append(w.pool, c) }()
	s := c.sess
	if s == nil {
		return
	}
	fail := func(format string, args ...any) {
		if !r.closing.Load() {
			r.found.add("%s: canary: "+format, append([]any{c.id}, args...)...)
		}
	}
	feed := func(what string, send func(context.Context, []byte) (int, error), n int) bool {
		ctx, cancel := r.opCtx()
		defer cancel()
		took, err := send(ctx, patternBytes(c.sent, n))
		c.sent += uint64(max(took, 0))
		if err != nil || took != n {
			fail("%s: %d bytes of input: took %d and returned %v", what, n, took, err)
			return false
		}
		return true
	}
	echoed := func(win *window, what string) bool {
		if r.waitFor(c, func() bool { return win.nextA.Load() >= c.sent }) {
			return true
		}
		fail("%s: window %d is at offset %d and %d bytes were sent to an echo: the echo never came", what, win.n, win.nextA.Load(), c.sent)
		return false
	}
	idle := func(win *window, what string) bool {
		if r.waitFor(c, func() bool { return win.state.Load() == winIdle }) {
			return true
		}
		fail("%s: window %d is still %s: its stream was never told that it left", what, win.n, windowStateName(win.state.Load()))
		return false
	}
	live := func(win *window) *stressStream {
		ss := win.cur.Load()
		if ss == nil {
			fail("window %d has no stream after it attached", win.n)
		}
		return ss
	}

	s.mu.Lock()
	first := s.windows[0]
	s.mu.Unlock()
	ss0 := first.cur.Load()
	if ss0 == nil || !feed("the first input", ss0.s.Write, 40) || !echoed(first, "the first input") {
		return
	}

	// A second host takes the session over: the stream that was the session's is told so, and is one that left.
	second := r.attachOp(c, rng, true, false)
	if second == nil {
		fail("the attach of a second host was refused")
		return
	}
	if !idle(first, "after another stream took the session over") || !echoed(second, "after taking the session over") {
		return
	}
	// What a stream that left sends takes no effect: the session goes on, and is the second host's.
	for kind := range staleKinds {
		r.staleFrame(c, ss0, kind, rng)
	}
	ss1 := live(second)
	if ss1 == nil || !feed("input after the frames of a stream that left", ss1.s.Write, 40) || !echoed(second, "after the frames of a stream that left") {
		return
	}

	// EndSession is for a session that no stream is attached to: with one attached it is CONFLICT, and the session lives (the host closes it on its stream).
	if err := r.endSessionOp(c, false, r.cfg.CloseBound); err == nil || refusalName(err) != "CONFLICT" {
		fail("EndSession of a session with a stream attached answered %v, want CONFLICT", err)
		return
	}

	// The host detaches: the session goes on with no stream, and what is written meanwhile is in the replay of the attach that follows.
	r.detachOp(c, rng, 0, true)
	if !idle(second, "after Detach") || !feed("input while no stream is attached", c.cmd.Write, 30) {
		return
	}
	third := r.attachOp(c, rng, false, false)
	if third == nil {
		fail("the attach after a Detach was refused")
		return
	}
	if !echoed(third, "after the attach that follows a Detach") {
		return
	}

	// A stream that breaks is an implicit detach: the session lives for its lease, and the host comes back to it.
	r.leaveOp(c, rng)
	if !idle(third, "after its stream broke") {
		return
	}
	fourth := r.attachOp(c, rng, false, false)
	if fourth == nil {
		fail("the attach after a stream broke was refused")
		return
	}
	ss4 := live(fourth)
	if ss4 == nil || !feed("input after the stream was released", ss4.s.Write, 20) || !echoed(fourth, "after the attach that follows a broken stream") {
		return
	}

	// The session ends while no stream is attached: EndSession answers once it has ended, no stream is told Ended, and the tombstone is
	// listed, and read by the windows that were away.
	r.detachOp(c, rng, 0, true)
	if !idle(fourth, "before EndSession") {
		return
	}
	s.tombstoneOnly.Store(true)
	if err := r.endSessionOp(c, false, r.cfg.CloseBound); err != nil {
		fail("EndSession of a session no stream is attached to: %v", err)
	}
	if !r.waitFor(c, func() bool {
		select {
		case <-c.cmd.Done():
			return true
		default:
			return false
		}
	}) {
		fail("the session did not end after EndSession")
		return
	}
	r.listOp()
	r.attachOp(c, rng, false, false) // refused: the tombstone of a session is not a session to attach to (attachOp says if it was not)
	r.tally.add("canary/played")
}

// behaviourNamed is the behaviour of the stress with the name.
func behaviourNamed(name string) *stressBehaviour {
	for _, b := range stressBehaviours {
		if b.name == name {
			return b
		}
	}
	panic("exectest: the stress has no behaviour " + name)
}
