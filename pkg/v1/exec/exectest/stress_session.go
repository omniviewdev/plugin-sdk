package exectest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/internal/producers"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The sessions of the stress: what a host does with a session that outlives its stream. A session is read by windows, each a host's view
// of it with a transcript of its own, and a window reads through streams, one at a time, that attach, detach, break and are superseded
// while the session's function writes on. The stress proves what a window got by offset: every byte of the output is either read, at
// its offset, or was told lost, and none is read twice; it proves what a stream was told last (Ended or Detached, and why), what ended
// the session, and what the tombstone of a session that ended says of it, against what the harness did.

const (
	// A window is idle when it has no stream and nobody is using it, busy while one call attaches or completes it, streaming while a pump
	// reads its stream, and ended once the session ended while its stream was attached. The pump owns the transcript of a streaming
	// window and the one that took a window busy owns it then, so that a transcript is never written by two goroutines.
	winIdle int32 = iota
	winBusy
	winStreaming
	winEnded

	// maxWindows is how many windows a session has at most.
	maxWindows = 6
	// maxStale is how many streams that left a session remembers, to send frames on.
	maxStale = 8
	// beyondOutput is an offset that no session of the stress reaches.
	beyondOutput = uint64(1) << 50
)

// leaseChoices are the leases a Detach asks for: the session's own (zero), a few milliseconds that a quick attach beats and a slow one
// does not, and a day, which the target's maximum clamps. startLeases are the leases, in milliseconds, a session is started asking for.
var (
	leaseChoices = []time.Duration{0, 0, 2 * time.Millisecond, 10 * time.Millisecond, 40 * time.Millisecond, 24 * time.Hour}
	startLeases  = []uint32{0, 0, 0, 3, 15, 60}
	startRings   = []uint32{0, 0, 1 << 10, 4 << 10, 64 << 10}
)

// ---------------------------------------------------------------------------------------------------------------
// what the round remembers of a session when its command has been let go
// ---------------------------------------------------------------------------------------------------------------

// sessionRec is what the round keeps of a session: enough to check a list and the id, and nothing that holds the session's output.
type sessionRec struct {
	id, key string
	tty     bool
	own     time.Duration // the lease the session was started asking for
	admitAt int64         // the clock before the Open that admitted it, in nanoseconds
	done    <-chan struct{}

	aliveAt atomic.Int64 // the clock before a look that found the session not ended
	doneAt  atomic.Int64 // the clock after a look that found it ended; zero until one did
	res     atomic.Pointer[exechook.Result]
}

func storeMax(a *atomic.Int64, v int64) {
	for {
		cur := a.Load()
		if v <= cur || a.CompareAndSwap(cur, v) {
			return
		}
	}
}

// look reads the clock and then whether the session has ended, and remembers what that proves about when its tombstone was made: made
// before the end that the look found, and, if it found none, not before the reading (less one tick, see maxTick).
func (s *sessionRec) look(clock *stressClock) (ended bool) {
	before := clock.Now().UnixNano()
	select {
	case <-s.done:
		if s.doneAt.Load() == 0 {
			s.doneAt.CompareAndSwap(0, clock.Now().UnixNano())
		}
		return true
	default:
		storeMax(&s.aliveAt, before)
		return false
	}
}

func (s *sessionRec) reuseFacts() reuseFacts {
	f := reuseFacts{admitAt: time.Unix(0, s.admitAt).UTC()}
	if t := s.aliveAt.Load(); t != 0 {
		f.aliveAt = time.Unix(0, t).UTC()
	}
	if t := s.doneAt.Load(); t != 0 {
		f.doneAt = time.Unix(0, t).UTC()
	}
	return f
}

// listFacts is the rec as a list is checked against it: whether the session had ended before the call (the look before) and after it.
func (s *sessionRec) listFacts(endedBefore, endedAfter bool) listFacts {
	return listFacts{id: s.id, endedBefore: endedBefore, endedAfter: endedAfter, reuse: s.reuseFacts(), res: s.res.Load()}
}

// ledger is every session the round admitted, by id: the newest admission of each, because an id is used again once its tombstone is gone, and
// what is known of each id's admissions. A call is addressed to an id and is meant for the session that held it when the call was made: if the
// id was taken again meanwhile, the call met another session, and what the harness knows of the first says nothing of the answer.
type ledger struct {
	mu       sync.Mutex
	latest   map[string]*sessionRec
	ids      map[string]*idState
	admitted int
}

// idState is what the ledger knows of one id.
type idState struct {
	admissions int // the sessions admitted under the id
	// pending counts the starts of the id that are in the engine and whose outcome the ledger has not yet heard: the engine may have admitted
	// one, and a call addressed to the id may meet that session, before the ledger knows of it.
	pending int
	// flight counts the calls that goroutines other than the worker's are making on a session of the id: the worker does not start the id again
	// meanwhile.
	flight atomic.Int32
}

// idLocked returns what is known of the id. The caller holds mu.
func (l *ledger) idLocked(id string) *idState {
	if l.ids == nil {
		l.ids = map[string]*idState{}
	}
	s := l.ids[id]
	if s == nil {
		s = &idState{}
		l.ids[id] = s
	}
	return s
}

func (l *ledger) admit(rec *sessionRec) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.latest == nil {
		l.latest = map[string]*sessionRec{}
	}
	l.latest[rec.id] = rec
	l.idLocked(rec.id).admissions++
	l.admitted++
}

// beginStart says that a start of the id is about to be made, and endStart that it has returned and its outcome, an admission if there was one,
// is in the ledger: between the two the engine may have admitted a session that the ledger does not know.
func (l *ledger) beginStart(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.idLocked(id).pending++
}

func (l *ledger) endStart(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.idLocked(id).pending--
}

// idStamp is what a call addressed to an id knows of the id's admissions when it begins.
type idStamp struct {
	id                  string
	admissions, pending int
}

func (l *ledger) stamp(id string) idStamp {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.idLocked(id)
	return idStamp{id: id, admissions: s.admissions, pending: s.pending}
}

// unchanged reports whether a call that began at was is meant for one session throughout: no start of the id was under way when it began or
// when it ended, and none was admitted meanwhile. A start that began and ended inside the call, and was refused, changed nothing.
func (l *ledger) unchanged(was idStamp) bool {
	now := l.stamp(was.id)
	return was.pending == 0 && now.pending == 0 && now.admissions == was.admissions
}

// flightOf is the count of the calls on goroutines of their own that are addressed to the id.
func (l *ledger) flightOf(id string) *atomic.Int32 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return &l.idLocked(id).flight
}

// isLatest reports whether rec is the newest admission of its id: a tombstone read by an id is the newest session's, so what is read
// of an older one's is only meant if no newer was admitted.
func (l *ledger) isLatest(rec *sessionRec) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.latest[rec.id] == rec
}

// holds reports whether rec is the session that holds its id, and no start of the id overlapped the call that began at was: what the call
// was answered is rec's.
func (l *ledger) holds(rec *sessionRec, was idStamp) bool { return l.isLatest(rec) && l.unchanged(was) }

func (l *ledger) total() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.admitted
}

// snapshot returns the newest admission of every id. With prune, it first forgets the sessions whose tombstone is surely gone at now (ended
// longer ago than the time to live, less the error of the looks), so that the ledger of a round whose clock moves stays as small as its store.
func (l *ledger) snapshot(now time.Time, ttl time.Duration, prune bool) []*sessionRec {
	l.mu.Lock()
	defer l.mu.Unlock()
	recs := make([]*sessionRec, 0, len(l.latest))
	for id, rec := range l.latest {
		if done := rec.doneAt.Load(); prune && done != 0 && !now.Before(time.Unix(0, done).Add(ttl+2*maxTick)) {
			delete(l.latest, id)
			continue
		}
		recs = append(recs, rec)
	}
	return recs
}

// ---------------------------------------------------------------------------------------------------------------
// windows and streams
// ---------------------------------------------------------------------------------------------------------------

// sessionState is a session of the stress as its windows and the calls made on it see it. The counters count calls before they are made,
// so that every cause and every last frame can be explained by one that was, and never by one that was not.
type sessionState struct {
	rec *sessionRec

	mu      sync.Mutex
	windows []*window
	streams []*stressStream // every stream the session has had, to find the ones that left
	pumps   producers.Group

	endRequests atomic.Int32 // calls that can end the session
	leases      atomic.Int32 // calls that leave it with no stream
	inputEnds   atomic.Int32 // close_input calls
	attachSeq   atomic.Int64 // attach calls begun, the session's start being the first
	moved       atomic.Bool  // a call that can take the session from a stream was begun
	endedFrames atomic.Int32 // streams told Ended
	// leaving counts the streams that a call is letting go of: broken, and not yet released, so that the engine still has them attached.
	leaving atomic.Int32
	// tombstoneOnly says that every stream of the session had left when it was ended, so that none may be told Ended.
	tombstoneOnly atomic.Bool
}

func (s *sessionState) causeFacts(closing bool) causeFacts {
	return causeFacts{
		endRequests: s.endRequests.Load(), leases: s.leases.Load(), endedFrames: s.endedFrames.Load(), closing: closing,
	}
}

// window is a host's view of a session, with a transcript: where it is in the output (next), and what it read and was told lost.
type window struct {
	s     *sessionState
	c     *stressCommand
	n     int
	state atomic.Int32
	nextA atomic.Uint64 // audit.next, for the ones that choose an offset from where another window is
	cur   atomic.Pointer[stressStream]
	base  uint64 // the offset the window began at: what it already had
	slow  bool   // its pump reads a few bytes at a time and lets the scheduler run between reads
	audit outputAudit
}

// stressStream is one stream of a window's. over is set once the call that took the stream from the session has returned: it is a stream that
// left, and every frame it sends must be refused and nothing may be read from it.
type stressStream struct {
	w      *window
	s      exechook.Stream
	ctx    context.Context
	cancel context.CancelFunc
	seq    int64 // the attach call that made the stream
	from   uint64
	replay exechook.Replay // how it was attached: the offset it asked from and the replay it was opened with, for the messages
	// stopped is closed when the pump has returned: nothing reads the stream any more, and it has been left, as the end of a handler leaves it.
	stopped chan struct{}

	over        atomic.Bool
	detachCalls atomic.Int32
	leaveCalls  atomic.Int32
	// leaveBefore is the clock before the first call that may have released the stream: another call may have done it, so the lease it gave
	// began no earlier than this.
	leaveBefore atomic.Int64
	refusedLeft atomic.Bool // a frame of the stream was refused as sent by a stream that left
}

// facts is what the harness did that can explain the last frame of the stream. Another attach call has begun when more were begun than the
// session's start and the stream's own: the order of the calls' counters is not the order the engine took them in, so this is as much as can be said.
func (ss *stressStream) facts(s *sessionState) streamFacts {
	others := s.attachSeq.Load() - 1
	if ss.seq != 1 {
		others-- // the stream's own call is one of the ones counted
	}
	return streamFacts{detachCalls: ss.detachCalls.Load(), leaveCalls: ss.leaveCalls.Load(), laterAttaches: others >= 1}
}

// newWindow makes a window that has read everything below base, and takes it for the caller: it is busy.
func (s *sessionState) newWindow(c *stressCommand, base uint64, slow bool) *window {
	w := &window{s: s, c: c, base: base, slow: slow, audit: outputAudit{tty: c.tty, patterned: true, next: base}}
	w.state.Store(winBusy)
	w.nextA.Store(base)
	s.mu.Lock()
	w.n = len(s.windows)
	s.windows = append(s.windows, w)
	s.mu.Unlock()
	return w
}

// takeWindow returns a window to attach, taken busy: an idle one, unless fresh asks for a new one or there is none and there is room for one.
func (s *sessionState) takeWindow(c *stressCommand, rng *rand.Rand, fresh bool) *window {
	s.mu.Lock()
	var idle []*window
	if !fresh {
		for _, w := range s.windows {
			if w.state.Load() == winIdle {
				idle = append(idle, w)
			}
		}
	}
	room := len(s.windows) < maxWindows
	var bases []uint64
	for _, w := range s.windows {
		bases = append(bases, w.nextA.Load())
	}
	s.mu.Unlock()
	rng.Shuffle(len(idle), func(i, j int) { idle[i], idle[j] = idle[j], idle[i] })
	for _, w := range idle {
		if w.state.CompareAndSwap(winIdle, winBusy) {
			return w
		}
	}
	if !room {
		return nil
	}
	// A new window has what its host had: nothing, or what another window had read, or some of it.
	base := uint64(0)
	if len(bases) > 0 {
		switch top := bases[rng.IntN(len(bases))]; rng.IntN(3) {
		case 1:
			base = top
		case 2:
			base = uint64(rng.Int64N(int64(top) + 1))
		}
	}
	return s.newWindow(c, base, rng.IntN(4) == 0)
}

// liveStream returns a stream the harness has no reason to think left, at random, or nil.
func (s *sessionState) liveStream(rng *rand.Rand) *stressStream {
	s.mu.Lock()
	defer s.mu.Unlock()
	var live []*stressStream
	for _, ss := range s.streams {
		if !ss.over.Load() && ss.w.cur.Load() == ss && ss.w.state.Load() == winStreaming {
			live = append(live, ss)
		}
	}
	if len(live) == 0 {
		return nil
	}
	return live[rng.IntN(len(live))]
}

// mayHaveStream reports whether the engine may have a stream attached to the session: one the harness has, one that an attach is making, one
// that a call is letting go of and has not yet. The answer of EndSession for a session with a stream is CONFLICT, and it is the engine's to give.
func (s *sessionState) mayHaveStream() bool {
	if s.leaving.Load() > 0 {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.windows {
		if state := w.state.Load(); state == winStreaming || state == winBusy {
			return true
		}
	}
	return false
}

// staleStream returns a stream that left, at random, or nil.
func (s *sessionState) staleStream(rng *rand.Rand) *stressStream {
	s.mu.Lock()
	defer s.mu.Unlock()
	var stale []*stressStream
	for _, ss := range s.streams {
		if ss.over.Load() {
			stale = append(stale, ss)
		}
	}
	if len(stale) == 0 {
		return nil
	}
	return stale[rng.IntN(len(stale))]
}

// register adds a stream to the session's, forgetting the oldest that left when there are too many.
func (s *sessionState) register(ss *stressStream) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.streams) >= 4*maxStale {
		kept := s.streams[:0]
		for i, old := range s.streams {
			if old.over.Load() && i < len(s.streams)-maxStale {
				continue
			}
			kept = append(kept, old)
		}
		clear(s.streams[len(kept):])
		s.streams = kept
	}
	s.streams = append(s.streams, ss)
}

// streamsBefore returns the streams the session has had, which an attach that is about to be made may take the session from.
func (s *sessionState) streamsBefore() []*stressStream {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.streams)
}

// ---------------------------------------------------------------------------------------------------------------
// admitting a session
// ---------------------------------------------------------------------------------------------------------------

// admitSession takes a session the engine admitted: its first window reads the stream that started it, and a watcher of its own sees it to
// the end and checks it.
func (r *stressRound) admitSession(c *stressCommand, admitAt int64) {
	rec := &sessionRec{id: c.id, key: c.key, tty: c.tty, own: c.startLease, admitAt: admitAt, done: c.cmd.Done()}
	r.ledger.admit(rec)
	s := &sessionState{rec: rec}
	s.attachSeq.Store(1)
	c.sess = s
	w := s.newWindow(c, 0, c.mode == readSlow)
	stream := c.cmd.Stream()
	if stream == nil {
		r.found.add("%s: a session has no stream", c.id)
		w.state.Store(winIdle)
		return
	}
	for _, v := range checkReplay(0, stream.Replay(), true) {
		r.found.add("%s: %s", c.id, v)
	}
	if !r.readers.Start(2) { // the watcher, and the pump of the first stream
		r.found.add("%s: the readers of the round had ended when a session was admitted", c.id)
		return
	}
	s.pumps.Start(1)
	ss := r.newStream(w, stream, 1, 0)
	w.cur.Store(ss)
	w.state.Store(winStreaming)
	go r.pump(ss)
	go r.watch(c)
}

func (r *stressRound) newStream(w *window, stream exechook.Stream, seq int64, from uint64) *stressStream {
	ctx, cancel := context.WithCancel(r.readCtx)
	ss := &stressStream{w: w, s: stream, ctx: ctx, cancel: cancel, seq: seq, from: from, replay: stream.Replay(), stopped: make(chan struct{})}
	w.s.register(ss)
	return ss
}

// describe says how the stream was attached.
func (ss *stressStream) describe() string {
	return fmt.Sprintf("stream #%d, attached from %d with the replay [%d, %d)", ss.seq, ss.from, ss.replay.Start, ss.replay.End)
}

// ---------------------------------------------------------------------------------------------------------------
// the pump of a stream: what a window reads
// ---------------------------------------------------------------------------------------------------------------

// pump reads a stream until its last frame, checking every read: the window's transcript takes each range lost and each byte at its offset,
// nothing is sent to a stream that left, and the last frame is Ended, once and only for the stream attached when the session ended, or
// Detached, for a reason that a call of the harness explains, with nothing after either. A stream that the harness broke ends with neither.
func (r *stressRound) pump(ss *stressStream) {
	w, c := ss.w, ss.w.c
	s := w.s
	defer r.readers.Done()
	defer s.pumps.Done()
	defer close(ss.stopped)
	defer ss.cancel()
	defer ss.s.Leave() // the end of the stream's handler: a stream that was broken is released, and what it held of its replay is dropped
	audit := &w.audit
	mode := readEager
	if w.n == 0 {
		mode = c.mode
	}
	if mode == readLate {
		select {
		case <-c.cmd.Done():
		case <-r.readCtx.Done():
			w.state.Store(winIdle)
			return
		}
	}
	buf := make([]byte, c.buf)
	for {
		wasOver := ss.over.Load()
		chunk, err := ss.s.Read(ss.ctx, buf)
		eof, left := errors.Is(err, io.EOF), errors.Is(err, exechook.ErrStreamLeft)
		if err != nil && !eof && !left {
			if ss.ctx.Err() == nil {
				r.found.add("%s: window %d: Read returned %v: the only errors of a stream's Read are io.EOF, ErrStreamLeft and the caller's context", c.id, w.n, err)
			}
			w.cur.Store(nil) // the stream was broken: it has no last frame
			w.state.Store(winIdle)
			return
		}
		if wasOver && (chunk.N > 0 || len(chunk.Dropped) > 0 || eof) {
			r.found.add("%s: window %d: a stream that had left the session (the call that took it from the session had returned) was read and gave %d bytes, %d ranges and eof %v: nothing is sent to a stream that left",
				c.id, w.n, chunk.N, len(chunk.Dropped), eof)
		}
		if left {
			if chunk.N != 0 || len(chunk.Dropped) != 0 {
				r.found.add("%s: window %d: a read that says the stream left returned %d bytes and %d ranges with it", c.id, w.n, chunk.N, len(chunk.Dropped))
			}
			r.streamLeft(ss)
			return
		}
		for _, v := range audit.take(chunk, buf[:chunk.N], eof) {
			r.found.add("%s: window %d (%s): %s", c.id, w.n, ss.describe(), v)
		}
		w.nextA.Store(audit.next)
		if eof {
			r.streamReadToEnd(ss)
			return
		}
		if w.slow {
			runtime.Gosched()
		}
	}
}

// streamLeft sees the last of a stream that left: its last frame is Detached, for a reason a call explains, and stays so; nothing more is read
// from it. The window is free to attach again.
func (r *stressRound) streamLeft(ss *stressStream) {
	w, c := ss.w, ss.w.c
	final := ss.s.Final()
	for _, v := range checkFinal(final, ss.facts(w.s)) {
		r.found.add("%s: window %d: %s", c.id, w.n, v)
	}
	r.tally.add(fmt.Sprintf("final/detached %s", detachWord(final)))
	for range 2 {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		chunk, err := ss.s.Read(ctx, make([]byte, 16))
		if !errors.Is(err, exechook.ErrStreamLeft) || chunk.N != 0 || len(chunk.Dropped) != 0 {
			r.found.add("%s: window %d: a read after the stream left returned %d bytes, %d ranges and %v: nothing follows the last frame", c.id, w.n, chunk.N, len(chunk.Dropped), err)
		}
	}
	if again := ss.s.Final(); again != final {
		r.found.add("%s: window %d: the last frame of a stream changed from %+v to %+v", c.id, w.n, final, again)
	}
	w.cur.Store(nil)
	w.state.Store(winIdle)
}

func detachWord(f exechook.Final) string {
	switch f.Reason {
	case execv2.DetachReason_DETACH_REASON_REQUESTED:
		return "requested"
	case execv2.DetachReason_DETACH_REASON_SUPERSEDED:
		return "superseded"
	}
	return "released"
}

// streamReadToEnd sees a stream that read the output to its end, which means that the session is ending. What the stream is told last is decided
// once the session's end is: Ended for the stream that was attached then, and Detached for one that left just before, which read everything and is
// told that it left. (The engine seals the session, and tells no stream Ended that is not the one attached at that moment: a stream that
// was released as the output ended is not.)
func (r *stressRound) streamReadToEnd(ss *stressStream) {
	w, c := ss.w, ss.w.c
	s := w.s
	select {
	case <-c.cmd.Done():
	case <-r.readCtx.Done():
		w.cur.Store(nil)
		w.state.Store(winIdle)
		return
	}
	// The result is the one the watcher saw first, so that the first look at a command's result is a single one, which every later look is
	// held to (dispose): the pump waits for it, which the watcher stores as soon as Done is closed.
	for c.first.Load() == nil {
		select {
		case <-r.readCtx.Done():
			w.cur.Store(nil)
			w.state.Store(winIdle)
			return
		default:
			runtime.Gosched()
		}
	}
	res := *c.first.Load()
	final := ss.s.Final()
	r.checkWindowEnd(c, w, res)
	if final.Detached {
		for _, v := range checkFinal(final, ss.facts(s)) {
			r.found.add("%s: window %d: %s", c.id, w.n, v)
		}
		r.tally.add("final/detached " + detachWord(final) + " as the output ended")
		w.cur.Store(nil)
		w.state.Store(winIdle)
		return
	}
	r.tally.add("final/ended")
	if n := s.endedFrames.Add(1); n > 1 {
		r.found.add("%s: window %d: %d streams were told Ended: a session has at most one Ended, and a stream that left is never told it", c.id, w.n, n)
	}
	if ss.refusedLeft.Load() {
		r.found.add("%s: window %d: a frame of the stream was refused as sent by a stream that left, and the stream was then told Ended: it never left", c.id, w.n)
	}
	r.rec.Ended(c.key) // the first end emitted is the one the recorder counts, and a second is its violation
	chunk, err := ss.s.Read(ss.ctx, make([]byte, 16))
	if !errors.Is(err, io.EOF) || chunk.N != 0 || len(chunk.Dropped) != 0 {
		if ss.ctx.Err() == nil {
			r.found.add("%s: window %d: a read after Ended returned %d bytes, %d ranges and %v: nothing may come after the end", c.id, w.n, chunk.N, len(chunk.Dropped), err)
		}
	}
	w.state.Store(winEnded)
}

// checkWindowEnd holds the transcript of a window whose session has ended to the whole output: it is at the end of it, and what it read plus
// what it was told lost is exactly what lies between where the window began and the end, no byte twice and none missing.
func (r *stressRound) checkWindowEnd(c *stressCommand, w *window, res exechook.Result) {
	a := &w.audit
	switch {
	case a.next != res.BytesOut:
		r.found.add("%s: window %d: the transcript is at offset %d and the session wrote %d: what it read plus what it was told was lost must be the whole output", c.id, w.n, a.next, res.BytesOut)
	case a.read+a.dropped != res.BytesOut-w.base:
		r.found.add("%s: window %d: it read %d bytes and was told %d lost between offset %d and the end at %d: read plus dropped must be exactly what lies between, no byte twice and none missing",
			c.id, w.n, a.read, a.dropped, w.base, res.BytesOut)
	}
	r.tally.addN("bytes read", int(a.read))
	r.tally.addN("bytes dropped", int(a.dropped))
}

// ---------------------------------------------------------------------------------------------------------------
// the watcher of a session: its end, and the tombstone
// ---------------------------------------------------------------------------------------------------------------

// watch sees a session to its end: when it has ended and every stream has read to the end of its own, it holds the end to what the harness did,
// and completes the transcript of every window that did not see the end on its stream from the tombstone.
func (r *stressRound) watch(c *stressCommand) {
	defer r.readers.Done()
	defer r.outstanding.remove(c)
	s := c.sess
	select {
	case <-c.cmd.Done():
	case <-r.readCtx.Done():
		return
	}
	s.rec.look(r.clock)
	res := c.cmd.Result()
	s.rec.res.Store(&res)
	c.first.Store(&res)
	if err := s.pumps.WaitContext(r.readCtx); err != nil {
		return // the round says which stream never returned
	}
	r.finishSession(c, res)
}

// finishSession is the end of a session, once its streams have all returned.
func (r *stressRound) finishSession(c *stressCommand, res exechook.Result) {
	s := c.sess
	moved := s.moved.Load()
	var audit outputAudit
	s.mu.Lock()
	windows := slices.Clone(s.windows)
	s.mu.Unlock()
	if len(windows) > 0 {
		audit = windows[0].audit
	}
	r.checkEndedSession(c, res, audit, moved)
	if v := checkCause(res.Cause, s.causeFacts(r.closing.Load())); v != "" {
		r.found.add("%s: %s", c.id, v)
	}
	if s.endedFrames.Load() == 0 {
		r.rec.Ended(c.key) // no stream was told: its end is the tombstone's
		r.tally.add("ended/tombstone only")
	} else if s.tombstoneOnly.Load() {
		r.found.add("%s: a stream was told Ended for a session that was ended with no stream attached to it: its end is the tombstone's, and no stream that left is told it", c.id)
	}
	for _, w := range windows {
		r.finishWindow(c, w, res)
	}
	s.mu.Lock()
	s.windows, s.streams = nil, nil // what holds the session's output goes
	s.mu.Unlock()
}

// finishWindow completes the window of a session that has ended and whose streams have all returned, from the tombstone, as a host that was
// away when the session ended does. The window is the check's to complete as it is an attach's to attach on: the check takes it (idle to busy)
// before it writes the transcript, so that an attach that the end of the session refuses, which the workers go on making, cannot take it
// meanwhile (two goroutines would write one transcript) or after, and leaves it ended. What the check sees of a window that is not its own is only
// ever a reason to wait: an attach holds it for a moment, gives it back, and another holds it, and no look at it says what it will be.
func (r *stressRound) finishWindow(c *stressCommand, w *window, res exechook.Result) {
	owned := false
	var state int32
	waited := poll(r.cfg.CloseBound, func() bool {
		state = w.state.Load()
		if hook := r.hooks.finishLooked; hook != nil {
			hook(w)
		}
		switch state {
		case winIdle:
			owned = w.state.CompareAndSwap(winIdle, winBusy)
			return owned
		case winBusy:
			return false // an attach has it: the session had ended, so no stream can be made, and the attach gives the window back
		}
		return true // ended: nothing to complete; streaming: a stream that never returned, said below
	})
	switch {
	case owned:
		if hook := r.hooks.finishCompleting; hook != nil {
			hook(w)
		}
		r.completeFromTombstone(c, w, res)
		w.state.Store(winEnded)
	case !waited:
		r.found.add("%s: window %d is still held by an attach that was made after the session ended, %v later: an attach that the end of the session refuses gives its window back",
			c.id, w.n, r.cfg.CloseBound)
	case state == winStreaming:
		r.found.add("%s: window %d still has a stream after every stream of the session returned: the pump of its stream never saw the stream leave", c.id, w.n)
	}
}

func windowStateName(state int32) string {
	switch state {
	case winBusy:
		return "busy"
	case winStreaming:
		return "streaming"
	case winEnded:
		return "ended"
	}
	return "idle"
}

// completeFromTombstone reads the rest of a window's transcript from the tombstone of its session, with ReadOutput, as a host that was away
// when the session ended does: what the ring still holds from where the window is, and the bytes between that were lost, told as a gap that
// the answer's replay start announces. It ends at the end of the output, which must be exactly what the session wrote. A tombstone that is
// gone is a violation only when nothing in the round could have removed it.
func (r *stressRound) completeFromTombstone(c *stressCommand, w *window, res exechook.Result) {
	var local []string
	stamp := r.ledger.stamp(c.id)
	add := func(format string, args ...any) { local = append(local, fmt.Sprintf(format, args...)) }
	a := &w.audit
	gone := false
	ctx, cancel := context.WithTimeout(r.conns.Context(context.Background()), r.cfg.CloseBound)
	defer cancel()
	for a.next < res.BytesOut && !gone {
		resp, err := r.eng.ReadOutput(ctx, &execv2.ReadOutputRequest{SessionId: c.id, FromOffset: a.next, MaxBytes: 1 << 20})
		switch {
		case err != nil && refusalName(err) == "NOT_FOUND/SESSION_UNKNOWN":
			gone = true
			if r.regime.keeps() {
				add("the tombstone of the session is gone, and nothing in the round could have removed it: the clock never moved and the store never filled")
			}
			continue
		case err != nil:
			add("ReadOutput of the tombstone failed: %v", err)
			gone = true
			continue
		case !resp.GetEnded():
			gone = true // the id is another session's now: this one's tombstone is gone
			continue
		}
		for _, v := range checkReadOutput(resp, a.next, c.tty, true) {
			add("%s", v)
		}
		before := a.next
		first := max(a.next, resp.GetReplayStart())
		var pending []exechook.Drop
		if first > a.next {
			pending = []exechook.Drop{{From: a.next, Bytes: first - a.next}} // what the ring no longer holds, told by the replay start
		}
		chunks := resp.GetChunks()
		if len(chunks) == 0 && len(pending) > 0 {
			for _, v := range a.take(exechook.Chunk{Offset: first, Dropped: pending}, nil, false) {
				add("%s", v)
			}
		}
		for i, chunk := range chunks {
			in := exechook.Chunk{Stream: chunk.GetStream(), Offset: chunk.GetOffset(), N: len(chunk.GetData())}
			if i == 0 {
				in.Dropped = pending
			}
			for _, v := range a.take(in, chunk.GetData(), false) {
				add("%s", v)
			}
		}
		w.nextA.Store(a.next)
		if a.next == before {
			add("ReadOutput of the tombstone made no progress at offset %d of %d", a.next, res.BytesOut)
			break
		}
	}
	if gone {
		r.tally.add("tombstone/gone before it was read")
	} else {
		switch {
		case a.next != res.BytesOut:
			add("the transcript read from the tombstone is at offset %d, and the session wrote %d", a.next, res.BytesOut)
		case a.read+a.dropped != res.BytesOut-w.base:
			add("it read %d bytes and was told %d lost between offset %d and the end at %d: read plus dropped must be exactly what lies between", a.read, a.dropped, w.base, res.BytesOut)
		}
		r.tally.addN("bytes read", int(a.read))
		r.tally.addN("bytes dropped", int(a.dropped))
		r.tally.add("tombstone/read to the end")
	}
	// What was read under an id is the session's only while no newer session holds the id, and no start of the id overlapped the reads: a tombstone
	// read after it was gone says nothing, and one read while the id was taken again may be the new session's.
	if r.ledger.holds(c.sess.rec, stamp) {
		for _, v := range local {
			r.found.add("%s: window %d: %s", c.id, w.n, v)
		}
	}
}

// checkEndedSession holds the end of a session whose streams have all returned to what must be true of every command and to what its behaviour says
// of it. The reader's accounts are held to the result only when the session was never taken from a stream: one that was has gaps and drops that
// are the window's and not the ring's, which the windows are held to one by one.
func (r *stressRound) checkEndedSession(c *stressCommand, res exechook.Result, audit outputAudit, moved bool) {
	shape := checkResultShape(res)
	var behaviour []string
	if moved {
		behaviour = c.b.check(res, audit, c.run, readEager, true)
	} else {
		shape = append(shape, checkResultAccounts(res, audit)...)
		behaviour = c.b.check(res, audit, c.run, c.mode, false)
	}
	for _, v := range shape {
		r.found.add("%s: %s", c.id, v)
	}
	for _, v := range behaviour {
		r.found.add("%s: %s", c.id, v)
	}
	r.tallyEnd(res)
}
