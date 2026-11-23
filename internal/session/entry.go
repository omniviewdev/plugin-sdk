package session

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/omniviewdev/plugin-sdk/internal/producers"
)

// Entry is one session of a [Registry]: its id, the state its lane built for it, its context,
// and its lifecycle. See the package documentation for the states it passes through. Its
// methods are safe for concurrent use.
type Entry[S any] struct {
	reg    *Registry[S]
	id     string
	group  string
	value  S
	ctx    context.Context // cancelled by End and by nothing else
	cancel context.CancelFunc

	// end is run by the first End, and holds every later End until the first has finished, so
	// that once any End has returned the context is cancelled and Ended is closed.
	end sync.Once
	// cause is the Cause, CauseNone until End records it. Go reads it to refuse producers once
	// End has begun, and Snapshot reads it.
	cause atomic.Uint32
	ended chan struct{} // closed by the first End
	done  chan struct{} // closed by the finisher: End was called and the last producer returned
	gone  chan struct{} // closed once the registry has removed the entry: its id and its place in the group are free

	// prod holds the entry's producers. The finisher waits for it, and so decides the end of
	// the entry under the lock that admits producers: a Go that races the finisher is waited for
	// or refused, never lost.
	prod producers.Group

	// retained and releaseRetain are guarded by the registry's lock: see Retain.
	retained      bool
	releaseRetain func()
}

// ID returns the id the entry was admitted with.
func (e *Entry[S]) ID() string { return e.id }

// Value returns the state that the init function of Admit built.
func (e *Entry[S]) Value() S { return e.value }

// Context returns the entry's context. It carries the values of the parent context the entry
// was admitted with, and is cancelled by [Entry.End], which is also what Close and the
// cancellation of the parent call: so it is done only once End was called and the cause
// recorded.
func (e *Entry[S]) Context() context.Context { return e.ctx }

// Cause returns why the entry ended, and [CauseNone] until End has been called.
func (e *Entry[S]) Cause() Cause { return Cause(e.cause.Load()) }

// Ended returns a channel that is closed by the first End, once its cause is recorded and the
// entry's context is cancelled.
func (e *Entry[S]) Ended() <-chan struct{} { return e.ended }

// Done returns a channel that is closed once End has been called and the last producer has
// returned: the moment the end function runs.
func (e *Entry[S]) Done() <-chan struct{} { return e.done }

// Removed returns a channel that is closed once the registry has removed the entry, which is
// after its end function returned. From then on its id is free and it no longer counts toward
// its group's limit: an Admit of the same id, or of another into the same full group, is not
// refused for this entry. A lane that refuses a start after admitting it waits for it, so that
// a retry of the same id is not refused for the entry that has just ended.
func (e *Entry[S]) Removed() <-chan struct{} { return e.gone }

// Retain keeps the entry's place in its group after the entry is removed, until the returned release is called. The place
// becomes a hold at that moment, without the group's count changing, and the entry's removal then does not free it. It is for
// work that outlives its entry and still uses what the place stood for: a goroutine that was told to stop and has not, and
// that the lane gave up waiting for. The entry ends, its end is emitted and its id is free; the place is not, so that a lane
// asked to start sessions as fast as it can end them is not made to leak a goroutine each. The group's limit is then a bound
// on the live entries and the holds together, and an Admit that the holds alone make full is refused with [ErrHeld].
//
// A hold is not an entry: [Registry.Close] does not wait for it. Retain may be called more than once (every call returns the
// same release), by a producer of the entry or by anyone before the entry is removed; after that the place is already gone
// and the hold is a new one, which takes the group over its limit if others have filled the place since. release may be
// called more than once and from any goroutine; only the first call counts.
func (e *Entry[S]) Retain() (release func()) { return e.reg.retain(e) }

// Go starts fn as one producer of the entry, in a goroutine that the entry's end waits for.
// It reports whether it did: it does not once End has returned, or once the entry's producers
// have all returned after End, and then it starts no goroutine. A Go that races End may
// succeed, and then the end waits for fn.
//
// It never panics. A producer may call Go, and so may anyone else; every goroutine an entry has
// is started this way, so that none outlives its session.
func (e *Entry[S]) Go(fn func()) bool {
	if e.cause.Load() != uint32(CauseNone) {
		return false
	}
	if h := e.reg.seams.goChecked; h != nil {
		h(e.id)
	}
	if !e.prod.Start(1) {
		return false
	}
	go func() {
		defer e.prod.Done()
		fn()
	}()
	return true
}

// End ends the entry with cause c. The first call wins: it records the cause, cancels the
// context, closes Ended, starts the goroutine that waits for the producers and removes the
// entry, and reports true. Later calls change nothing and report false, and they return only
// once the first has finished. End does not wait for the producers, so a producer may call it.
// End panics when c is [CauseNone], which is a bug in the caller.
func (e *Entry[S]) End(c Cause) (first bool) {
	if c == CauseNone {
		panic("session: End with CauseNone")
	}
	e.end.Do(func() {
		first = true
		e.cause.Store(uint32(c))
		if h := e.reg.seams.endRecorded; h != nil {
			h(e.id)
		}
		e.cancel()
		close(e.ended)
		e.reg.startFinisher(e)
	})
	return first
}

// Watch ends the entry with cause c when ctx is done. The watcher is a producer of the entry,
// started with Go: it returns when the entry ends first, so it does not outlive it. Watch does
// nothing when ctx can never be done (ctx.Done() is nil), and when the entry is already
// ending and Go refuses. Watch panics when c is [CauseNone].
func (e *Entry[S]) Watch(ctx context.Context, c Cause) {
	if c == CauseNone {
		panic("session: Watch with CauseNone")
	}
	done := ctx.Done()
	if done == nil {
		return
	}
	e.Go(func() {
		select {
		case <-done:
			e.End(c)
		case <-e.ended:
		}
	})
}
