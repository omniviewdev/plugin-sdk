// Package producers counts the goroutines that keep something alive, and decides its end
// under the lock that admits them.
//
// A sync.WaitGroup cannot do that: a member must not be added from outside while Wait may be
// returning, and the owners of these goroutines are joined from outside by nature. A log
// session takes a source from an options update while its own goroutine is finishing; a
// session manager takes a session from a caller while it is closing. A WaitGroup in that
// place is a data race under -race and, in a plugin, "sync: WaitGroup is reused before
// previous Wait has returned": a panic that ends the process. A Group makes the end of the
// owner a state instead, so that a producer that races it is refused, or waited for, and
// never lost.
package producers

import (
	"context"
	"sync"
)

// Group tracks the producers of one owner: the goroutines that go on feeding it after its own
// work is over. Its owner waits for them, so that it can publish its end when the last one
// has returned.
//
// It does what a sync.WaitGroup does, without the one thing a WaitGroup forbids: a member
// cannot be added from outside while Wait may be returning, and a producer is outside by
// nature. Here the end of the group is a state, decided under the same lock that admits
// members, so a producer either joins the group or is told it is over.
//
// A group passes through three states:
//
//   - open: the owner's own work is still running. Start always succeeds.
//   - closing: the owner has called Wait, because its own work is over, and the producers that
//     remain are still running. Start still succeeds: a running producer keeps the owner alive
//     (a session's watcher may add a source, and an update may restart one).
//   - ended: Wait has returned, because the last producer returned. Start fails for good, and
//     the owner's end is, or has been, published.
//
// An owner that has to stop admitting at the moment it begins to shut down, and not when the
// last producer has gone, keeps its own flag under the lock that admits, and checks it
// before Start (a manager's Close does). The group guarantees the rest: that a producer
// admitted before the end is waited for, and that one that comes after it is refused.
//
// The zero value is an open group.
type Group struct {
	mu      sync.Mutex
	running int           // producers started and not yet done
	waiting bool          // the owner called Wait: the group is closing, or ended
	ended   bool          // waiting, and no producer is left
	idle    chan struct{} // closed when the group ends; made by Wait when it must block
}

// Start registers n producers, each of which must call Done when it returns. It reports
// whether it did: it does not once the group has ended, and then registers none.
func (g *Group) Start(n int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ended {
		return false
	}
	g.running += n
	return true
}

// Done reports that a producer registered with Start has returned. It panics when no producer
// is registered: a Done without a Start is a bug in the owner.
func (g *Group) Done() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.running--
	if g.running < 0 {
		panic("producers: Done without a matching Start")
	}
	if g.running == 0 && g.waiting {
		g.end()
	}
}

// Wait is called by the owner when its own work is over. It returns once no producer is left,
// and from then on the group has ended. It may be called again, and by many goroutines.
func (g *Group) Wait() {
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

// WaitContext is Wait that gives up when ctx ends. It returns nil once the group has ended,
// that is, once no producer is left, and ctx.Err() if ctx ended first. When the group has
// ended by the time WaitContext looks at ctx, or ends at the same moment as ctx, the answer is
// nil: the end the caller waited for is the one it is told of. A group that has ended, or that
// has no producer to wait for, is reported ended whatever ctx says, as Wait would.
//
// Giving up leaves the group exactly as Wait leaves it while it blocks: closing, its
// producers still running and still admitted, and ended by the last of them. It may be called
// again, by the owner or by another, and by many goroutines at once, with Wait or with other
// contexts. It follows Wait step for step until the point where Wait blocks, so the two are to
// be kept in step.
func (g *Group) WaitContext(ctx context.Context) error {
	g.mu.Lock()
	g.waiting = true
	if g.ended {
		g.mu.Unlock()
		return nil
	}
	if g.running == 0 {
		g.end()
		g.mu.Unlock()
		return nil
	}
	if g.idle == nil {
		g.idle = make(chan struct{})
	}
	idle := g.idle
	g.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		select {
		case <-idle:
			return nil // both are ready: the group's end is the answer
		default:
			return ctx.Err()
		}
	}
}

// end moves the group to its final state. The caller holds g.mu and has checked
// that the group has not ended.
func (g *Group) end() {
	g.ended = true
	if g.idle != nil {
		close(g.idle)
	}
}

// Freeze holds the group's lock until the returned function is called: every Start, Done,
// Wait and WaitContext blocks meanwhile. It is a seam for tests that have to hold a
// producer's reservation where it happens, to force an interleaving of the code that
// reserves it; nothing else should call it.
func (g *Group) Freeze() (thaw func()) {
	g.mu.Lock()
	return g.mu.Unlock
}
