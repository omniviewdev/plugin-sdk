package exec

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/session"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// A session outlives its stream: a stream is an attachment of the session, and one attachment at a time reads its output.
//
//	new session ──▶ attached ──┬─ Detach frame ───────────▶ detached (a lease runs) ──┬─ attach ──▶ attached
//	                           ├─ the stream breaks ──────▶ detached (the default lease) ┤
//	                           ├─ another stream attaches ▶ attached to that stream      └─ the lease runs out ──▶ ended
//	                           └─ Close, exit, shutdown ──▶ ended
//
// What the ring and the attachments share is one mutex, ring.mu, because three steps have to be one: the snapshot of the ring
// and the registration of the stream that replays it (so that no byte is lost between the replay and the live stream, and none
// is sent twice); the decision whether a stream is told Ended or Detached (a session's single end goes to the stream that is
// attached when the session ends, and a stream that left before is told Detached and never Ended); and the expiry of a lease
// against an attach (an attach cancels the lease, and a lease that has run out ends only a session nobody has attached to).
//
// A fourth step is the frames of the stream that is the session's. A frame whose effect does not wait (a resize, a signal, close_input, a
// query: see whileCurrent) takes effect while it holds attachments.frames for reading, and an attach, a detach and a release take it
// for writing: so a stream that the session has moved away from never has a frame take effect after the move, and a frame that is
// taking effect is done before the move. frames is taken before every other lock of the session (ring.mu, and the locks that a frame's
// effect takes: the start gate's, the input pipe's, the signal queue's, the resizer's), and nothing that holds one of them takes it.
//
// The host of the ring (the command) is asked three things under that lock, and none of them waits: whether the session is
// ending, to end it, and to kill it.

// attState is what became of an attachment.
type attState uint8

const (
	// attLive: the stream reads the session. The session's current attachment is the one that is live or ended.
	attLive attState = iota
	// attEnded: the session ended while this was its attachment: its stream is told Ended once it has read the output to its end.
	attEnded
	// attDetached: the host asked to leave (Detach): its stream is told Detached, with the lease.
	attDetached
	// attSuperseded: another stream attached and took the session over: its stream is told Detached, with no lease.
	attSuperseded
	// attReleased: the stream left without asking (it broke, or its host closed its side): an implicit detach.
	attReleased
)

// errStreamLeft is what a read of an attachment returns once its stream is no longer the session's: it was detached, superseded
// or released. The stream asks the attachment which.
var errStreamLeft = errors.New("exec: the stream left its session")

// sessionHost is what the ring asks of its session, under the ring's lock. None of it blocks.
type sessionHost interface {
	// ending reports whether the session's end has been recorded.
	ending() bool
	// end ends the session with a cause; with kill the end does not wait for the function. The first end stands.
	end(cause session.Cause, kill bool)
}

// attachments is the state of the ring that says who reads it and what the session is doing around that; guarded by ring.mu.
type attachments struct {
	host sessionHost // nil once the session has ended: a ring that a tombstone holds refers to nothing of the session
	cur  *attachment // who reads the session; nil while it is detached
	// ended is true once the session's end has been decided: no one attaches, detaches or ends it any more.
	ended bool
	// startedClaimed is true once a stream has been given the Started frame: a session has at most one.
	startedClaimed bool

	// leaseGen numbers the leases: an attach and the end of the session make the running lease stale. leaseCancel is closed with
	// that, so that the goroutine that waits for the lease goes at once.
	leaseGen     uint64
	leaseCancel  chan struct{}
	leaseExpires time.Time // when a detached session ends; zero while it is attached

	// frames orders the frames of the stream that is the session's against the moves of the session between streams (see above).
	frames sync.RWMutex
}

// attachment is one stream's reading of a session.
type attachment struct {
	r     *ring
	state attState
	// cursor is the offset of the next byte of the live stream. A replay that the attachment took is read first, from its own
	// copy, and the live stream begins where it ends.
	cursor uint64
	snap   *snapshot
	// watching is the context whose end wakes this reader's waits, and unwatch ends that registration: one registration serves every
	// wait of a stream (a reader that has caught up waits again and again), so that waiting for output allocates nothing.
	watching context.Context
	unwatch  func() bool
	// drop is the range the ring overwrote that this reader had not read, and has not been told about.
	drop dropRun
	// lease is when the session ends if nobody attaches, for an attachment that was detached.
	lease time.Time
	// over is true once the attachment has left the session (detached, superseded or released): its stream's frames are no longer
	// the session's host's. It is set under the ring's lock and read without it, for each frame the stream receives.
	over atomic.Bool
	// stream is the context of the served stream that reads the attachment, bound once the stream serves it: a stream whose context has ended
	// (its host cancelled it or lost its connection) is over before its handler has noticed, and nothing is sent to it. Nil for an attachment
	// that no served stream reads (the in-process engine's streams are released by their reader). Guarded by ring.mu.
	stream context.Context
}

// lose records that the ring overwrote [from, from+n), which this reader had not read. The lost ranges of a reader are always
// contiguous, because the ring loses its oldest bytes first.
func (a *attachment) lose(from, n uint64, stream execv2.Stream) {
	if a.drop.Bytes == 0 {
		a.drop = dropRun{Stream: stream, From: from, Bytes: n}
		return
	}
	a.drop.Bytes += n
}

// newAttachment makes the attachment of a stream that reads from cursor on. The caller holds mu.
func (r *ring) newAttachment(cursor uint64) *attachment {
	return &attachment{r: r, cursor: cursor}
}

// bindStream records the context of the served stream that reads the attachment: once it has ended, the attachment is not attached for a
// call that needs to know (EndSession), however soon its handler notices.
func (a *attachment) bindStream(ctx context.Context) {
	r := a.r
	r.mu.Lock()
	defer r.mu.Unlock()
	a.stream = ctx
}

// overStream returns the session's current attachment when its stream's context has ended: the stream that a host abandoned (cancelled, or
// lost its connection) and whose handler has not yet left. Whether anything is left to release is release's to say.
func (r *ring) overStream() *attachment {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a := r.att.cur; a != nil && a.stream != nil && a.stream.Err() != nil {
		return a
	}
	return nil
}

// Read waits until the attachment has something for its stream and returns, in one outputChunk, the range the ring lost since the
// last Read and up to len(buf) bytes of one stream: first the bytes of the replay it was given, and then the live stream. It
// returns io.EOF once the session's output was read to its end, errStreamLeft once the stream is no longer the session's, and
// ctx's error when ctx ends first. An empty buf is io.ErrShortBuffer. One reader at a time reads in order.
func (a *attachment) Read(ctx context.Context, buf []byte) (outputChunk, error) {
	if len(buf) == 0 {
		return outputChunk{}, io.ErrShortBuffer
	}
	r := a.r
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		switch a.state {
		case attDetached, attSuperseded, attReleased:
			return outputChunk{}, errStreamLeft
		case attLive, attEnded:
		}
		if a.snap != nil {
			if n, stream, offset := a.snap.read(buf); n > 0 {
				return outputChunk{Stream: stream, Offset: offset, N: n}, nil
			}
			a.snap = nil
		}
		var chunk outputChunk
		if a.drop.Bytes > 0 {
			chunk.Dropped = []dropRun{a.drop}
			a.cursor = a.drop.From + a.drop.Bytes
			r.delivered = max(r.delivered, a.cursor)
			a.drop = dropRun{}
		}
		if a.cursor < r.endOff {
			n, stream := r.readLocked(a.cursor, buf)
			chunk.Stream, chunk.Offset, chunk.N = stream, a.cursor, n
			a.cursor += uint64(n)
			r.delivered = max(r.delivered, a.cursor)
			r.wake()
			return chunk, nil
		}
		chunk.Offset = a.cursor
		if len(chunk.Dropped) > 0 {
			r.wake()
			return chunk, nil
		}
		if r.closed {
			return chunk, io.EOF
		}
		if err := ctx.Err(); err != nil {
			return outputChunk{}, err
		}
		if a.watching != ctx {
			if a.unwatch != nil {
				a.unwatch()
			}
			a.watching, a.unwatch = ctx, r.watch(ctx)
		}
		r.wait()
	}
}

// outcome is what became of the attachment, and for a detached one when its lease ends.
func (a *attachment) outcome() (attState, time.Time) {
	r := a.r
	r.mu.Lock()
	defer r.mu.Unlock()
	return a.state, a.lease
}

// whileCurrent runs fn if the stream of the attachment is still the session's, and reports whether it did. It is how a frame of the
// host takes effect when its effect does not wait: a resize, a signal, close_input, a query, and the end of the session that a
// protocol violation earns. An attach, a detach and a release wait for fn to return, and a frame that comes after them finds the
// attachment over, so that a frame takes effect before the session changes hands or never: nothing a stream that was superseded
// sends is done to the session its successor holds. fn must not wait, and must not move the session between streams. The input
// frames are not applied here: they wait for room in the function's queue, and the session must be able to change hands meanwhile.
func (a *attachment) whileCurrent(fn func()) bool {
	frames := &a.r.att.frames
	frames.RLock()
	defer frames.RUnlock()
	if a.over.Load() {
		return false
	}
	fn()
	return true
}

// ---------------------------------------------------------------------------------------------------------------
// attach, detach, release, end
// ---------------------------------------------------------------------------------------------------------------

// replayInfo is what an attach tells the host about the replay that follows Opened: exactly the bytes [Start, End).
type replayInfo struct {
	Start, End uint64
	// StartSize is the size the bytes at Start were produced for, and Size the size now; zero when not known.
	StartSize, Size Size
}

// Why an attach is refused.
var (
	// errSessionGone: the session has ended, or its end is under way.
	errSessionGone = errors.New("exec: the session has ended")
	// errBeyondOutput: the host claims bytes the session has not produced.
	errBeyondOutput = errors.New("exec: from_offset is beyond the session's output")
)

// attach registers a new stream on the session, and takes the replay for it: from the offset the host asked for, or from the front
// of the ring when the ring no longer holds it, up to the end of the output now. The older stream, if there is one, is
// superseded, and a lease that runs is cancelled. All of it is one step under the ring's lock, so that the replay and the live
// stream that begins where it ends lose no byte and repeat none, and it waits for the frames of the older stream that are taking
// effect (see whileCurrent): none takes effect after it.
func (r *ring) attach(from uint64) (*attachment, replayInfo, error) {
	r.att.frames.Lock()
	defer r.att.frames.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.att.ended || r.att.host == nil || r.att.host.ending() {
		return nil, replayInfo{}, errSessionGone
	}
	if from > r.endOff {
		return nil, replayInfo{}, errBeyondOutput
	}
	start := max(from, r.start())
	a := r.newAttachment(r.endOff)
	a.snap = r.snapshotLocked(start)
	info := replayInfo{Start: start, End: r.endOff}
	info.StartSize, _ = r.sizeAt(start)
	info.Size, _ = r.currentSize()
	if old := r.att.cur; old != nil && old.state == attLive {
		old.state = attSuperseded
		old.over.Store(true)
		old.snap = nil
	}
	r.att.cur = a
	r.cancelLeaseLocked()
	// What the replay holds is handed to the stream: it is not lost whatever the ring does next.
	r.delivered = r.endOff
	r.cond.Broadcast()
	return a, info, nil
}

// leaseArm is a lease that has been started: what the goroutine that waits for it needs.
type leaseArm struct {
	gen     uint64
	cancel  <-chan struct{}
	expires time.Time
}

// armLeaseLocked starts a lease of d from now, and makes the one that ran stale.
func (r *ring) armLeaseLocked(d time.Duration, now time.Time) leaseArm {
	r.cancelLeaseLocked()
	r.att.leaseGen++
	r.att.leaseCancel = make(chan struct{})
	r.att.leaseExpires = now.Add(d)
	return leaseArm{gen: r.att.leaseGen, cancel: r.att.leaseCancel, expires: r.att.leaseExpires}
}

// cancelLeaseLocked makes the lease that runs, if one does, stale and releases the goroutine that waits for it.
func (r *ring) cancelLeaseLocked() {
	r.att.leaseGen++
	if r.att.leaseCancel != nil {
		close(r.att.leaseCancel)
		r.att.leaseCancel = nil
	}
	r.att.leaseExpires = time.Time{}
}

// detach is the host's Detach: the stream leaves and the session lives for the lease. It reports false when the stream cannot
// leave: it is no longer the session's, or the session is ending or has ended, in which case the stream is told how it ended.
func (r *ring) detach(a *attachment, lease time.Duration, now time.Time) (leaseArm, bool) {
	r.att.frames.Lock()
	defer r.att.frames.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.att.cur != a || a.state != attLive || r.att.ended || r.att.host == nil || r.att.host.ending() {
		return leaseArm{}, false
	}
	a.state = attDetached
	a.over.Store(true)
	r.att.cur = nil
	arm := r.armLeaseLocked(lease, now)
	a.lease = arm.expires
	r.cond.Broadcast()
	return arm, true
}

// release is the implicit detach of a stream that is over without having left: the session lives for its default lease. It
// reports false when there is nothing to do: the stream already left, or the session is ending or has ended.
func (r *ring) release(a *attachment, lease time.Duration, now time.Time) (leaseArm, bool) {
	r.att.frames.Lock()
	defer r.att.frames.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.att.cur != a || a.state != attLive || r.att.ended {
		return leaseArm{}, false
	}
	a.state = attReleased
	a.over.Store(true)
	r.att.cur = nil
	r.cond.Broadcast()
	if r.att.host == nil || r.att.host.ending() {
		return leaseArm{}, false
	}
	arm := r.armLeaseLocked(lease, now)
	a.lease = arm.expires
	return arm, true
}

// forget is the end of a stream's handler: the stream is over, and what its attachment still holds of the replay it was given (a copy
// of the ring that it had not read) goes with it. A session that has ended stops referring to the attachment too: its ring is what a
// tombstone keeps and what the store's budget counts, and a copy of part of it that nobody counts would live as long as the
// tombstone. It is not release: a stream whose host half-closed may still be reading the tail of a session that ended, and must
// find it.
func (r *ring) forget(a *attachment) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a.snap = nil
	if a.unwatch != nil {
		a.unwatch() // the stream is over: its context's end has nothing to wake
		a.watching, a.unwatch = nil, nil
	}
	if r.att.cur == a && r.att.ended {
		r.att.cur = nil
	}
}

// closeFrom is the host's Close, from the stream that is the session's: it destroys the session. A stream that is no longer the
// session's, or a session that is ending already, is not asked.
func (r *ring) closeFrom(a *attachment, kill bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.att.cur != a || r.att.ended || r.att.host == nil {
		return
	}
	r.att.host.end(session.CauseRequested, kill)
}

// destroyFrom ends the session of a stream that broke the protocol, or that the host never learned the session of (cause cancelled,
// graceful). It is the session's to end while the stream holds it, and also while nobody holds it because this stream left (the stream
// returns the status it earned and leaves, which can come before this call): then the session lives for the lease that leaving began,
// and the violation ends it. A stream that another took the session from, or that detached, has no session to end; and a session that is
// ending already is not asked.
func (r *ring) destroyFrom(a *attachment) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.att.ended || r.att.host == nil || (r.att.cur != a && !(r.att.cur == nil && a.state == attReleased)) {
		return
	}
	r.att.host.end(session.CauseCancelled, false)
}

// endIfDetached destroys a session that has no stream, for EndSession. It reports false when a stream is attached.
func (r *ring) endIfDetached(kill bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.att.cur != nil && !r.att.ended {
		return false
	}
	if !r.att.ended && r.att.host != nil {
		r.att.host.end(session.CauseRequested, kill)
	}
	return true
}

// expireLease ends the session when the lease gen has run out, unless it was made stale: an attach or an end has come since.
func (r *ring) expireLease(gen uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.att.leaseGen != gen || r.att.cur != nil || r.att.ended || r.att.host == nil {
		return
	}
	r.att.host.end(session.CauseExpired, false)
}

// seal is the decision that the session has ended: from here on no stream attaches, detaches or ends it. The stream that is the
// session's at this moment, if there is one, is the one that is told Ended; a stream that left before is not. The ring then holds
// nothing of its session. It reports the attachment that is told Ended.
func (r *ring) seal() *attachment {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.att.ended = true
	r.att.host = nil
	r.cancelLeaseLocked()
	a := r.att.cur
	if a != nil && a.state == attLive {
		a.state = attEnded
	}
	r.cond.Broadcast()
	return a
}

// claimStarted gives the Started frame to the attachment that asks, when no stream has had it and the attachment is still the
// session's. A session has at most one Started, and the stream that is attached when it is made is the one that sends it.
func (r *ring) claimStarted(a *attachment) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.att.startedClaimed || r.att.cur != a {
		return false
	}
	r.att.startedClaimed = true
	return true
}

// markStartedClaimed records that the facts of Started were given to a stream in Opened, which an attach does: no later stream is
// sent the frame.
func (r *ring) markStartedClaimed() {
	r.mu.Lock()
	r.att.startedClaimed = true
	r.mu.Unlock()
}

// ---------------------------------------------------------------------------------------------------------------
// what a stream asks of its session
// ---------------------------------------------------------------------------------------------------------------

// sessionFacts are the facts of the session as of now: what Opened carried, refined by what the function reported once it has
// (Started), the capabilities being the latest effective set. It reports whether there is a Started. The facts are the session's
// own: its request (the program it was asked to run, not the target's default), its target and limits, and what the target said.
func (c *command) sessionFacts() (*execv2.SessionFacts, bool) {
	opened := c.openedFacts()
	if facts, ok := c.startedFacts(opened); ok {
		return facts, true
	}
	return opened, false
}

// attach registers a new stream on the session and returns its attachment and the Opened it sends: the session's own latest facts
// and the description of the replay that follows (see ring.attach). The older stream, if any, is superseded. It is refused with
// errSessionGone for a session that has ended or is ending, and with errBeyondOutput for an offset the session has not reached.
func (c *command) attach(from uint64) (*attachment, *execv2.Opened, error) {
	a, info, err := c.ring.attach(from)
	if err != nil {
		return nil, nil, err
	}
	facts, started := c.sessionFacts()
	if started {
		// The facts of Started are in this Opened: no stream is sent the frame later.
		c.ring.markStartedClaimed()
	}
	return a, &execv2.Opened{Facts: facts, Replay: &execv2.Replay{
		Start: info.Start, End: info.End, Bytes: info.End - info.Start,
		StartSize: sizeToProto(info.StartSize), Size: sizeToProto(info.Size),
	}}, nil
}

// detach is the host's Detach: the stream of the attachment leaves and the session lives for the lease the host asked for (zero is
// the session's own, and the target's maximum is the most). It returns when the lease runs out, and false when the stream cannot
// leave: it is no longer the session's, or the session is ending.
func (c *command) detach(a *attachment, requested time.Duration) (time.Time, bool) {
	d := c.leaseFor(requested)
	arm, ok := c.ring.detach(a, d, c.e.clock())
	if !ok {
		return time.Time{}, false
	}
	c.startLease(arm, d)
	return arm.expires, true
}

// release is the implicit detach of a stream that is over: the session lives for its own lease.
func (c *command) release(a *attachment) {
	d := c.leaseFor(0)
	if arm, ok := c.ring.release(a, d, c.e.clock()); ok {
		c.startLease(arm, d)
	}
}

// leave is the last act of a stream's handler: it leaves its session as release says, and holds nothing of it any more.
func (c *command) leave(a *attachment) {
	c.release(a)
	c.ring.forget(a)
}

// releaseIfStreamOver releases the session's stream when its host has abandoned it: the stream's context has ended and its handler has not
// yet said so. It is the release that handler is about to make (release's: the session lives for its lease, and the handler that comes
// after finds the attachment left), made by a call that has to see the session as the host left it.
func (c *command) releaseIfStreamOver() {
	if a := c.ring.overStream(); a != nil {
		c.release(a)
	}
}

// sessionInfo is the session as ListSessions reports it while it lives.
func (c *command) sessionInfo() *execv2.SessionInfo {
	facts, _ := c.sessionFacts()
	st := c.ring.status()
	info := &execv2.SessionInfo{
		Facts: facts, Attached: st.attached, NextOffset: st.end, ReplayStart: st.start,
		LastActivityUnixMs: c.ring.lastActivity.Load(),
	}
	if !st.lease.IsZero() {
		info.LeaseExpiresUnixMs = st.lease.UnixMilli()
	}
	if state, ok := c.Link(); ok && c.gate.effective().LinkHealth {
		info.Link = state.toProto()
	}
	return info
}
