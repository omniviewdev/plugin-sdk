package exectest

import (
	"context"
	"errors"
	"math/rand/v2"
	"runtime"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// What a host does with a session that outlives its stream: attach, detach, break a stream, take the session over, end it with no stream,
// send frames on a stream that left, list the sessions and read the ring. Each op counts its calls before it makes them, checks every answer
// to what the contract says of it and to nothing the harness cannot know, and may be made at any moment against any other, by any worker, or
// by a goroutine of its own, so that an op meets itself.

// opCtx is the context of an op that waits for the engine: bounded, so that a hang is a failure and no outcome is decided by it.
func (r *stressRound) opCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.conns.Context(context.Background()), r.cfg.CloseBound)
}

// mayHaveEnded says whether anything could have ended the session by now: a request, the end of a lease, the engine closing, input that was
// closed, or a command that ends by itself. A refusal of an attach as unknown is a violation when none of them did.
func (r *stressRound) mayHaveEnded(c *stressCommand) bool {
	s := c.sess
	return s.endRequests.Load() > 0 || s.leases.Load() > 0 || s.inputEnds.Load() > 0 || r.closing.Load() || c.b.finishes != nil
}

// attachOp attaches a stream to the session from where a window is, as its host would: a window that has none (the one that detached, or
// whose stream broke) goes on from the offset it has, and a new one (fresh) is a second host that takes the session over, at the offset it
// has, which may be none. beyond asks for an offset the session cannot have reached. It returns the window the stream was attached to, or nil
// when nothing was attached.
func (r *stressRound) attachOp(c *stressCommand, rng *rand.Rand, fresh, beyond bool) *window {
	s := c.sess
	if s == nil || !r.ledger.isLatest(s.rec) { // the id is another session's now: this call was meant for the one that held it
		return nil
	}
	w := s.takeWindow(c, rng, fresh)
	if w == nil {
		return nil
	}
	// The pump of the new stream is a producer of the session and of the round from before the attach, so that neither can end between the
	// attach and the pump's start.
	if !r.readers.Start(1) {
		w.state.Store(winIdle)
		return nil
	}
	if !s.pumps.Start(1) {
		r.readers.Done()
		w.state.Store(winIdle)
		return nil
	}
	started := false
	defer func() {
		if !started {
			s.pumps.Done()
			r.readers.Done()
		}
	}()
	a := &w.audit
	from := a.next
	if beyond {
		from = beyondOutput
	}
	seq := s.attachSeq.Add(1)
	s.moved.Store(true)
	before := s.streamsBefore()
	endedBefore := s.rec.look(r.clock)
	ctx, cancel := r.opCtx()
	stream, err := r.eng.Attach(ctx, &execv2.OpenRequest{SessionId: c.id, Kind: &execv2.OpenRequest_Attach{Attach: &execv2.Attach{FromOffset: from}}})
	cancel()
	if err != nil || beyond || endedBefore {
		if v := checkAttachRefusal(err, attachAsk{beyond: beyond, mayEnd: r.mayHaveEnded(c)}); v != "" {
			r.found.add("%s: window %d: %s", c.id, w.n, v)
		}
		if err == nil && endedBefore && !beyond && r.regime.keeps() { // in a round that moves its clock the id may be another session's by now
			r.found.add("%s: window %d: an attach to a session that had ended was admitted: its tombstone is not a session to attach to", c.id, w.n)
		}
		r.tally.add("op/attach refused " + refusalName(err))
		if err == nil {
			stream.Leave() // it was admitted though it must not be: do not hold the session
		}
		w.state.Store(winIdle)
		return nil
	}
	rep := stream.Replay()
	for _, v := range checkReplay(from, rep, false) {
		r.found.add("%s: window %d: %s", c.id, w.n, v)
	}
	if id := stream.Opened().GetFacts().GetSessionId(); id != c.id {
		r.found.add("%s: window %d: the Opened of an attach carries the facts of session %q", c.id, w.n, id)
	}
	switch {
	case rep.Start > from && rep.Start <= rep.End:
		// What the ring no longer holds is the gap between the offset asked for and the replay's start: told once, here.
		for _, v := range a.take(exechook.Chunk{Offset: rep.Start, Dropped: []exechook.Drop{{From: from, Bytes: rep.Start - from}}}, nil, false) {
			r.found.add("%s: window %d: %s", c.id, w.n, v)
		}
	case rep.Start < from:
		a.next = rep.Start // the violation is said; the transcript goes on from what the engine says, so that one fault is one finding
	}
	taken := false
	for _, old := range before {
		if !old.over.Swap(true) && old.w.state.Load() == winStreaming {
			taken = true
		}
	}
	if taken {
		r.tally.add("op/supersede")
	} else {
		r.tally.add("op/attach")
	}
	ss := r.newStream(w, stream, seq, from)
	w.cur.Store(ss)
	w.nextA.Store(a.next)
	w.state.Store(winStreaming)
	started = true
	go r.pump(ss)
	return w
}

// detachOp sends Detach on a stream the session has, for a lease: the stream leaves, the session lives, and the lease the engine says
// applies is the one asked for, as far as the target's maximum.
func (r *stressRound) detachOp(c *stressCommand, rng *rand.Rand, lease time.Duration, forced bool) {
	s := c.sess
	if s == nil {
		return
	}
	ss := s.liveStream(rng)
	if ss == nil {
		return
	}
	if !forced {
		lease = leaseChoices[rng.IntN(len(leaseChoices))]
	}
	s.leases.Add(1)
	s.moved.Store(true)
	ss.detachCalls.Add(1)
	before := r.clock.Now()
	expires, ok := ss.s.Detach(lease)
	after := r.clock.Now()
	if !ok {
		r.tally.add("op/detach moot") // the stream was no longer the session's, or the session was ending
		return
	}
	ss.over.Store(true)
	r.tally.add("op/detach")
	if v := checkLease(expires, before, after, effectiveLease(lease, s.rec.own)); v != "" {
		r.found.add("%s: window %d: a Detach for %v: %s", c.id, ss.w.n, lease, v)
	}
}

// leaveOp is a stream that is over without having left: the host's connection breaks, or it closes its side. Both are an implicit detach,
// for the session's own lease. A connection that broke ends the handler: the pump of the stream returns with its context and releases the
// stream, and the stream has no last frame. A host that closed its side is released while its stream is read, and the stream goes on to its
// last frame, Detached with no reason.
func (r *stressRound) leaveOp(c *stressCommand, rng *rand.Rand) {
	s := c.sess
	if s == nil {
		return
	}
	ss := s.liveStream(rng)
	if ss == nil {
		return
	}
	s.leases.Add(1)
	s.moved.Store(true)
	ss.leaveCalls.Add(1)
	s.leaving.Add(1) // the engine has the stream until it is released, whatever the harness's own view of it is
	before := r.clock.Now()
	ss.leaveBefore.CompareAndSwap(0, before.UnixNano())
	broken := rng.IntN(2) == 0
	if broken {
		ss.cancel()
		timer := time.NewTimer(r.cfg.CloseBound)
		select {
		case <-ss.stopped:
		case <-r.readCtx.Done():
		case <-timer.C:
			r.found.add("%s: window %d: the pump of a stream whose context ended did not return within %v: a Read returns when its context ends", c.id, ss.w.n, r.cfg.CloseBound)
		}
		timer.Stop()
	} else {
		ss.s.Release()
	}
	after := r.clock.Now()
	s.leaving.Add(-1)
	final := ss.s.Final()
	if !final.Detached {
		return // the session ended while the stream was attached: nobody left it
	}
	ss.over.Store(true)
	r.tally.add("op/leave")
	for _, v := range checkFinal(final, ss.facts(s)) {
		r.found.add("%s: window %d: after the stream was released: %s", c.id, ss.w.n, v)
	}
	if final.Reason == execv2.DetachReason_DETACH_REASON_UNSPECIFIED && !final.Lease.IsZero() {
		if v := checkLease(final.Lease, time.Unix(0, ss.leaveBefore.Load()).UTC(), after, effectiveLease(0, s.rec.own)); v != "" {
			r.found.add("%s: window %d: a stream was released: %s", c.id, ss.w.n, v)
		}
	}
}

// closeStreamOp is the host's Close frame on a stream it has: it ends the session, gracefully or with a kill.
func (r *stressRound) closeStreamOp(c *stressCommand, rng *rand.Rand, kill bool) {
	s := c.sess
	if s == nil {
		return
	}
	ss := s.liveStream(rng)
	if ss == nil {
		return
	}
	if kill {
		r.mayHold.Store(true) // a function given up on holds its place until it returns
	}
	s.endRequests.Add(1)
	ss.s.Close(kill)
	r.tally.add("op/close frame")
}

// endSessionPatience is how long a worker waits for the answer of an EndSession that the dice made: the session's function may ignore its context, and
// then the answer comes only when the harness lets the function go, at the end of the round, so the worker gives up and goes on (the engine ends the
// session all the same, and the answer of a call whose context ended is a timeout, which the check allows). A call whose answer is an event that has to
// happen (the canary's EndSession of an echo that nobody holds) waits for it, with the round's bound as the upper limit: how long it takes is the
// machine's to say, and no check decides by it.
const endSessionPatience = 50 * time.Millisecond

// endSessionOp is EndSession, which ends a session that no stream is attached to and answers once it has ended, its place free and its
// tombstone readable, and gives up after wait. It returns what the engine answered.
func (r *stressRound) endSessionOp(c *stressCommand, kill bool, wait time.Duration) error {
	s := c.sess
	if s == nil || !r.ledger.isLatest(s.rec) {
		return nil
	}
	if kill {
		r.mayHold.Store(true)
	}
	s.endRequests.Add(1)
	mode := execv2.CloseMode_CLOSE_MODE_GRACEFUL
	if kill {
		mode = execv2.CloseMode_CLOSE_MODE_KILL
	}
	attaches := s.attachSeq.Load()
	attachedBefore := s.mayHaveStream()
	ctx, cancel := context.WithTimeout(r.conns.Context(context.Background()), wait)
	err := r.eng.EndSession(ctx, &execv2.EndSessionRequest{SessionId: c.id, Mode: mode})
	ctxEnded := ctx.Err() != nil
	cancel()
	ask := endAsk{
		streamMayBeAttached: attachedBefore || s.attachSeq.Load() != attaches || s.mayHaveStream(),
		expired:             !r.regime.keeps(),
		ctxEnded:            ctxEnded,
	}
	if v := checkEndSession(err, ask); v != "" {
		r.found.add("%s: %s", c.id, v)
	}
	if err != nil {
		r.tally.add("op/end session refused " + refusalName(err))
		return err
	}
	r.tally.add("op/end session")
	if r.regime.keeps() {
		// It answered, so the session has ended and its tombstone is readable.
		readCtx, stop := r.opCtx()
		_, readErr := r.eng.ReadOutput(readCtx, &execv2.ReadOutputRequest{SessionId: c.id, MaxBytes: 1})
		stop()
		if refusalName(readErr) == "NOT_FOUND/SESSION_UNKNOWN" {
			r.found.add("%s: EndSession answered and the tombstone of the session is not readable", c.id)
		}
	}
	return nil
}

// staleOp sends a frame on a stream that left the session: it is refused, and nothing it sends takes effect. A Close or a Leave that did
// would end or release a session that its owner did not, which the cause of the end and the last frame of the owner's stream would say.
func (r *stressRound) staleOp(c *stressCommand, rng *rand.Rand) {
	s := c.sess
	if s == nil {
		return
	}
	if ss := s.staleStream(rng); ss != nil {
		r.staleFrame(c, ss, rng.IntN(staleKinds), rng)
	}
}

// staleKinds is how many frames a stream that left can send: input, a resize, a signal, close_input, Detach, Close and the stream breaking.
const staleKinds = 7

// staleFrame sends one frame of a kind on a stream that left. It sends input, so it is made by the worker that owns the command.
func (r *stressRound) staleFrame(c *stressCommand, ss *stressStream, kind int, rng *rand.Rand) {
	ctx, cancel := r.opCtx()
	defer cancel()
	r.tally.add("op/stale frame")
	name := c.id
	switch kind {
	case 0:
		n, err := ss.s.Write(ctx, patternBytes(c.sent, 1))
		if n > 0 {
			c.sent += uint64(n)
		}
		if n != 0 || !errors.Is(err, exechook.ErrStreamLeft) {
			r.found.add("%s: an input frame of a stream that left took %d bytes and returned %v: it takes none and is refused", name, n, err)
		}
	case 1:
		if _, err := ss.s.Resize(uint32(1+rng.IntN(1000)), uint16(1+rng.IntN(200)), uint16(1+rng.IntN(60))); !errors.Is(err, exechook.ErrStreamLeft) {
			r.found.add("%s: a resize of a stream that left returned %v: it is refused", name, err)
		}
	case 2:
		if _, err := ss.s.Signal(execv2.Signal_SIGNAL_INT); !errors.Is(err, exechook.ErrStreamLeft) {
			r.found.add("%s: a signal of a stream that left returned %v: it is refused", name, err)
		}
	case 3:
		if err := ss.s.CloseInput(); !errors.Is(err, exechook.ErrStreamLeft) {
			r.found.add("%s: close_input of a stream that left returned %v: it is refused", name, err)
		}
	case 4:
		if _, ok := ss.s.Detach(time.Duration(1+rng.IntN(50)) * time.Millisecond); ok {
			r.found.add("%s: a Detach of a stream that left was applied: it is not the session's", name)
		}
	case 5:
		ss.s.Close(rng.IntN(2) == 0) // no effect: it is not counted as a request, so that a session it ended is one nobody asked to end
	default:
		ss.s.Leave() // no effect: the session's stream is not released by a stream that left
	}
}

// signalOp sends a signal, from the session's consumer or on a stream: a declared signal is queued (or refused as closed for a command that ended),
// and one the session does not declare is never taken.
func (r *stressRound) signalOp(c *stressCommand, rng *rand.Rand) {
	s := c.sess
	if s == nil {
		return
	}
	list := c.cmd.Effective().GetSignals().GetSignals()
	declared := make(map[execv2.Signal]bool)
	for _, sig := range list {
		declared[sig] = true
	}
	all := []execv2.Signal{
		execv2.Signal_SIGNAL_INT, execv2.Signal_SIGNAL_QUIT, execv2.Signal_SIGNAL_TERM, execv2.Signal_SIGNAL_KILL,
		execv2.Signal_SIGNAL_HUP, execv2.Signal_SIGNAL_USR1, execv2.Signal_SIGNAL_USR2,
	}
	var sig execv2.Signal
	if len(list) > 0 && rng.IntN(5) != 0 {
		sig = list[rng.IntN(len(list))]
	} else {
		sig = all[rng.IntN(len(all))]
	}
	endedBefore := s.rec.look(r.clock)
	var d exechook.SignalDisposition
	var err error
	via := "the session's consumer"
	ss := (*stressStream)(nil)
	if rng.IntN(2) == 0 {
		if ss = s.liveStream(rng); ss != nil {
			via = "a stream"
			d, err = ss.s.Signal(sig)
		}
	}
	if ss == nil {
		d = c.cmd.Signal(sig)
	}
	if errors.Is(err, exechook.ErrStreamLeft) {
		ss.refusedLeft.Store(true)
		r.tally.add("op/signal refused (stream left)")
		return
	}
	switch {
	case declared[sig] && d == exechook.SignalUndeclared:
		r.found.add("%s: a signal that the session declares (%s) was refused as undeclared, sent by %s", c.id, sig, via)
	case declared[sig] && endedBefore && d != exechook.SignalClosed:
		r.found.add("%s: a signal (%s) sent after the command ended was %v, want closed", c.id, sig, d)
	case !declared[sig] && d != exechook.SignalUndeclared:
		r.found.add("%s: a signal that the session does not declare (%s) was %v, sent by %s: only a declared one is taken", c.id, sig, d, via)
	}
	r.tally.add("op/signal")
}

// listOp is ListSessions, held to the store: its bounds, its order, every session started before the call (live while it lives, a tombstone for
// its time to live after) and what each tombstone says of its session.
func (r *stressRound) listOp() {
	recs := r.ledger.snapshot(r.clock.Now(), r.regime.ttl, r.regime.ticks)
	before := make([]bool, len(recs))
	stamps := make([]idStamp, len(recs))
	for i, rec := range recs {
		stamps[i] = r.ledger.stamp(rec.id)
		before[i] = rec.look(r.clock)
	}
	ctx, cancel := r.opCtx()
	defer cancel()
	infos, err := r.eng.ListSessions(ctx)
	after := r.clock.Now()
	if err != nil {
		r.found.add("ListSessions failed: %v", err)
		return
	}
	known := make([]listFacts, 0, len(recs))
	for i, rec := range recs {
		f := rec.listFacts(before[i], rec.look(r.clock))
		if r.ledger.holds(rec, stamps[i]) { // an id that was admitted again during the call, or is being, lists the newer session, which the snapshot did not hold
			known = append(known, f)
		}
	}
	for _, v := range checkListing(infos, known, r.listRule(after)) {
		r.found.add("ListSessions: %s", v)
	}
	r.tally.add("op/list")
}

// listRule is the rule a list is read by: the store's bounds and what the round's clock and bounds allow.
func (r *stressRound) listRule(after time.Time) listRule {
	return listRule{
		ttl: r.regime.ttl, count: r.regime.count, bytes: r.regime.bytes, keeps: r.regime.keeps(),
		displacementPossible: r.ledger.total() > r.regime.count, after: after,
	}
}

// readOutputOp is ReadOutput of a session by id, live or tombstoned, from an offset: the answer is held to the ring's contract.
func (r *stressRound) readOutputOp(rng *rand.Rand) {
	recs := r.ledger.snapshot(r.clock.Now(), r.regime.ttl, r.regime.ticks)
	if len(recs) == 0 {
		return
	}
	rec := recs[rng.IntN(len(recs))]
	from := uint64(0)
	if rng.IntN(3) == 0 {
		from = uint64(rng.IntN(4096))
	}
	limit := uint32(1 << 20)
	if rng.IntN(3) == 0 {
		limit = uint32(1 + rng.IntN(8192))
	}
	ended := rec.look(r.clock)
	endedBefore := ended && r.regime.keeps() // in a round that moves its clock the id may be another session's by now, which has not ended
	stamp := r.ledger.stamp(rec.id)
	ctx, cancel := r.opCtx()
	defer cancel()
	resp, err := r.eng.ReadOutput(ctx, &execv2.ReadOutputRequest{SessionId: rec.id, FromOffset: from, MaxBytes: limit})
	if !r.ledger.holds(rec, stamp) {
		// A start of the id overlapped the call, and the engine may have admitted it before the round heard: the answer may be the new session's, with
		// its own terminal, output and end, which what the round knows of this one says nothing of.
		r.tally.add("op/read output of an id taken again")
		return
	}
	switch name := refusalName(err); {
	case err == nil:
		for _, v := range checkReadOutput(resp, from, rec.tty, endedBefore) {
			r.found.add("%s: ReadOutput from %d: %s", rec.id, from, v)
		}
		if res := rec.res.Load(); res != nil && resp.GetNextOffset() > res.BytesOut {
			r.found.add("%s: ReadOutput from %d: the next offset %d is past the end of the output, %d", rec.id, from, resp.GetNextOffset(), res.BytesOut)
		}
	case name == "INVALID_REQUEST":
		// from is beyond the output: legal while the session is young.
	case name == "NOT_FOUND/SESSION_UNKNOWN" && !r.regime.keeps():
		// the tombstone is gone, or another session holds the id
	default:
		r.found.add("%s: ReadOutput from %d was refused as %s (%v)", rec.id, from, name, err)
	}
	r.tally.add("op/read output")
}

// tickOp moves the clock on by a few milliseconds, in a round whose tombstones expire while the workers run.
func (r *stressRound) tickOp(rng *rand.Rand) {
	r.clock.Advance(time.Duration(1+rng.IntN(int(maxTick/time.Millisecond))) * time.Millisecond)
	r.tally.add("op/tick")
}

// waitFor polls until cond holds, for an event that has to happen: it ends when the engine is closing, the command has ended or the round's
// reads were ended, and reports whether cond held.
func (r *stressRound) waitFor(c *stressCommand, cond func() bool) bool {
	deadline := time.Now().Add(r.cfg.CloseBound)
	for spins := 0; ; spins++ {
		if cond() {
			return true
		}
		select {
		case <-c.cmd.Done():
			return cond()
		case <-r.readCtx.Done():
			return false
		default:
		}
		if r.closing.Load() || time.Now().After(deadline) {
			return false
		}
		if spins < 200 {
			runtime.Gosched()
		} else {
			time.Sleep(200 * time.Microsecond)
		}
	}
}
