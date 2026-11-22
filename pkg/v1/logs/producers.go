package logs

import "sync"

// producerGroup tracks the goroutines that feed a session after its initial
// fan-out: the watcher of dynamic sources, the sources it adds, and the sources an
// options update restarts. The orchestrating goroutine waits for them before it
// publishes the session's end.
//
// It does what a sync.WaitGroup does, without the one thing a WaitGroup forbids: a
// member cannot be added from outside while Wait may be returning, and an options
// update is outside by nature. Here the end of the group is a state, decided under
// the same lock that admits members, so an update either joins the group or is told
// the session is over.
//
// A session passes through three states as far as its producers go:
//
//   - open: the orchestrating goroutine is still running the session's initial sources.
//     start always succeeds.
//   - closing: the orchestrating goroutine has called wait, because its own work is over,
//     and the producers that remain are still running. start still succeeds: a running
//     producer keeps the session alive, the watcher may add a source, and an update may
//     restart one.
//   - ended: wait has returned, because the last producer returned. start fails for good,
//     and the session's end is, or has been, published: STREAM_ENDED is the last thing
//     any producer of the session causes to be sent.
//
// The zero value is an open group.
type producerGroup struct {
	mu      sync.Mutex
	running int           // producers started and not yet done
	waiting bool          // the orchestrator called wait: the group is closing, or ended
	ended   bool          // waiting, and no producer is left
	idle    chan struct{} // closed when the group ends; made by wait when it must block
}

// start registers n producers, each of which must call done when it returns. It
// reports whether it did: it does not once the group has ended, and then registers none.
func (g *producerGroup) start(n int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ended {
		return false
	}
	g.running += n
	return true
}

// done reports that a producer registered with start has returned.
func (g *producerGroup) done() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.running--
	if g.running < 0 {
		panic("logs: producer finished that was never started")
	}
	if g.running == 0 && g.waiting {
		g.end()
	}
}

// wait is called by the orchestrating goroutine when its own work is over. It
// returns once no producer is left, and from then on the group has ended.
func (g *producerGroup) wait() {
	g.mu.Lock()
	g.waiting = true
	if g.ended {
		g.mu.Unlock()
		return
	}
	if g.running == 0 {
		g.end()
		g.mu.Unlock()
		return
	}
	if g.idle == nil {
		g.idle = make(chan struct{})
	}
	idle := g.idle
	g.mu.Unlock()
	<-idle
}

// end moves the group to its final state. The caller holds g.mu and has checked
// that the group has not ended.
func (g *producerGroup) end() {
	g.ended = true
	if g.idle != nil {
		close(g.idle)
	}
}
