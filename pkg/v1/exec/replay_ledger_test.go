package exec_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/internal/lifecycletest"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// THE proof of X3c: a host that detaches and attaches again, at any moment, in any order, under a flood, assembles the session's
// output byte for byte, by offset. A flooding author writes the pattern (the byte at offset i is the top byte of i * 0x9E3779B97F4A7C15, so
// a byte that moved, repeated or came from the wrong chunk shows at once) while a host leaves and comes back a thousand times: it
// asks the session to detach, drops the stream (a client cancel is an implicit detach), takes the session over with a second attach
// while the first is attached and flooding, attaches twice in a row, attaches from two goroutines at once, cancels an attach
// before its Opened, reads the replay only in part and leaves, reads the ring with ReadOutput while it has no stream, and at the end
// ends the session on a stream or without one and reads what is left from the tombstone. Every frame goes to the ledger, which
// places it by its offset (replay_support_test.go's transcript, grown to hold a thousand streams).
//
// Two kinds of run, both floods in which the author is never more than a budget ahead of what the host holds (a flood that nothing
// bounded would hold a single processor for a time slice at each turn, and the host that has to read it would not run). In an EXACT
// run the budget is half of what the ring keeps (it keeps at least 15/16 of its size), so the ring always has what the host asks
// for, whatever the host does: nothing may be lost, no Dropped frame and no replay that starts late, and the SHA-256 of the assembled
// output is the author's. In an OVERFLOW run the budget is four times the ring: a detached session overflows its ring and a lossy one
// overflows its host's reading: every loss has to be announced (a Dropped frame or a replay that starts after the offset asked for),
// every byte that arrives has to be right and exactly once, what arrives and what was announced has to tile the output with no hole,
// and a target that propagates a stalled reader never drops a byte to a stream that is attached.
//
// Every last frame of every stream has a cause in what the host did before it came, and the host keeps the record of it
// (replay_ledger_events_test.go): Ended after a Close that it sent on the stream, Detached{REQUESTED} after a Detach, Detached{SUPERSEDED} when
// an attach that it made can have registered after the stream's own, and Detached with no reason after it cancelled the stream. A last frame
// that none of them explains fails the run, with the record. A cancelled attach is one that the host gives up on at once: it is made while
// the host is away, before the attach that the host holds, and while the host holds a stream, before the attach that supersedes it. The
// plugin may never register it (its stream had ended), or register it and find its stream over: then it took the session from the stream that
// held it, and its leaving left the session detached for its lease. The host cannot tell which, holds nothing of what it did not read, and
// attaches from what it has. The engine of a run has a clock that stands still and leases that nobody runs out, so that the lease of a Detach
// is held to the millisecond and the pace of the machine decides nothing.

// ledgerVariant is one kind of run.
type ledgerVariant struct {
	name  string
	lossy bool // the target declares STALL_LOSSY
	exact bool // nothing may be lost
	tty   bool // a terminal (one stream); otherwise standard output and error alternate in runs of 32 KiB
	// budget is how far the author may run ahead of what the host holds.
	budget uint64
}

var ledgerVariants = []ledgerVariant{
	{"blocking-exact-tty", false, true, true, ledgerExactBudget},
	{"lossy-exact-mixed", true, true, false, ledgerExactBudget},
	{"blocking-overflow-mixed", false, false, false, ledgerOverflowBudget},
	{"lossy-overflow-tty", true, false, true, ledgerOverflowBudget},
}

const (
	// ledgerExactBudget is how far an author of an exact run may run ahead of what the host holds. The ring of 1 MiB keeps at least 15
	// of its 16 chunks, 960 KiB, and the budget is half of that. An overflow run's is four times the ring.
	ledgerExactBudget    = 512 << 10
	ledgerOverflowBudget = 4 << 20
	ledgerRing           = 1 << 20
	// ledgerMaxLease is Session.MaxDetachTimeout of the target: 30 minutes.
	ledgerMaxLease = 30 * time.Minute
)

// ledgerStream is one stream of the host: a goroutine reads it a frame ahead, so that the host can wait on many streams at once and
// stop waiting on one without cancelling it.
type ledgerStream struct {
	conn    exec.Conn
	n       int
	opened  *execv2.Opened
	asked   uint64
	out     chan ledgerRecv
	quit    chan struct{}
	timer   *time.Timer
	retired sync.Once

	// attach is the attach that made the stream. The numbers are the events of the host's record at which the host sent Detach on the
	// stream, sent Close on it and cancelled it, and at which its last frame came: zero for not at all. A last frame is put down to what
	// the host did before it came (ledgerHost.attribution).
	attach                            *ledgerAttach
	ev                                *ledgerEvents
	detachAt, closeAt, leftAt, lastAt atomic.Int64
}

type ledgerRecv struct {
	frame *execv2.ServerFrame
	err   error
}

// newLedgerStream makes the stream of a host that keeps no record of what it does (the tests that use the ledger's streams for what they
// read, and not for what the host did).
func newLedgerStream(conn exec.Conn, n int, asked uint64) *ledgerStream {
	return newRecordedStream(conn, n, asked, nil, nil)
}

// newRecordedStream makes the stream of the host of the ledger's run, which keeps the record of the attaches it made and of what its streams
// told it. attach is the attach that made the stream.
func newRecordedStream(conn exec.Conn, n int, asked uint64, attach *ledgerAttach, ev *ledgerEvents) *ledgerStream {
	s := &ledgerStream{
		conn: conn, n: n, opened: conn.Opened(), asked: asked, out: make(chan ledgerRecv, 1), quit: make(chan struct{}),
		timer: time.NewTimer(time.Hour), attach: attach, ev: ev,
	}
	s.timer.Stop()
	if ev != nil {
		replay := s.opened.GetReplay()
		ev.add("stream %d: Opened by attach #%d (%s), asked from %d, replay [%d, %d)", n, attach.n, attach.kind, asked, replay.GetStart(), replay.GetEnd())
	}
	go func() {
		for {
			frame, err := conn.Recv()
			switch {
			case ev == nil:
			case err != nil:
				ev.add("stream %d: the receive came back with %v", n, err)
			case isLast(frame):
				s.lastAt.Store(int64(ev.add("stream %d: last frame %s", n, ledgerLast(frame))))
			}
			select {
			case s.out <- ledgerRecv{frame, err}:
			case <-s.quit:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return s
}

// retire leaves the stream: its reader stops and the transport stream is cancelled. A stream that has not ended is an implicit detach.
// It may be called more than once and from any goroutine (two operations of a race may both break the stream).
func (s *ledgerStream) retire() {
	s.retired.Do(func() {
		if s.ev != nil {
			s.leftAt.Store(int64(s.ev.add("stream %d: the host cancels it", s.n)))
		}
		close(s.quit)
	})
	_ = s.conn.Close()
}

// ledgerHost is the host of one run.
type ledgerHost struct {
	t       *testing.T
	v       ledgerVariant
	seed    uint64
	rig     *proofRig
	author  *ledgerAuthor
	id      string
	lg      *ledger
	rng     *rand.Rand
	cur     *ledgerStream
	streams []*ledgerStream
	moment  int
	trace   []string
	tally   map[string]int
	frames  int
	ev      *ledgerEvents
	// late holds the cancelled attaches that the host made late on purpose (ledgerLate); pendingLate are the tags of the ones that it has
	// not let go yet, and lateMade counts the ones it made.
	late        *ledgerLate
	pendingLate []string
	lateMade    int
}

func (h *ledgerHost) where() string { return ledgerWhere(h.seed, h.v.name, h.moment) }

func ledgerWhere(seed uint64, variant string, moment int) string {
	return fmt.Sprintf("seed %d, variant %s, moment %d", seed, variant, moment)
}

// ledgerRecordShown is how many events of the host's record a failure prints.
const ledgerRecordShown = 40

// fatalf fails the run, with where it was, the moves that led there and the end of the host's record of what it did and was told.
func (h *ledgerHost) fatalf(format string, args ...any) {
	h.t.Helper()
	trace := h.trace
	if len(trace) > 16 {
		trace = trace[len(trace)-16:]
	}
	events := "the host kept none"
	if h.ev != nil {
		events = strings.Join(h.ev.tail(ledgerRecordShown), "\n\t")
	}
	h.t.Fatalf("%s: %s\nthe last moves: %s\nthe order of events, the last %d of the host's record:\n\t%s\nreplay it with the seed: go test -run 'TestDetachAndReattachUnderAFloodIsByteExactByOffset/%s/seed-%d'",
		h.where(), fmt.Sprintf(format, args...), strings.Join(trace, " > "), ledgerRecordShown, events, h.v.name, h.seed)
}

func (h *ledgerHost) note(move string) {
	h.ev.add("moment %d: %s", h.moment, move)
	h.tally[move]++
	h.trace = append(h.trace, move)
}

// check fails the run when the ledger found a problem.
func (h *ledgerHost) check() {
	h.t.Helper()
	if len(h.lg.problems) > 0 {
		h.fatalf("the ledger: %s", strings.Join(h.lg.problems, "\n\t"))
	}
}

func (h *ledgerHost) newStream(conn exec.Conn, asked uint64, a *ledgerAttach) *ledgerStream {
	s := newRecordedStream(conn, len(h.streams)+1, asked, a, h.ev)
	h.streams = append(h.streams, s)
	return s
}

// recv takes the next frame of a stream, within the wait bound.
func (h *ledgerHost) recv(s *ledgerStream) (*execv2.ServerFrame, error) {
	h.t.Helper()
	s.timer.Reset(engineWait)
	defer s.timer.Stop()
	select {
	case r := <-s.out:
		return r.frame, r.err
	case <-s.timer.C:
		h.fatalf("stream %d: no frame within %v", s.n, engineWait)
	}
	return nil, nil
}

// send sends a frame of the host on a stream. A stream that has ended cannot take one, and what ended it is what the failure is about.
func (h *ledgerHost) send(s *ledgerStream, f *execv2.ClientFrame, what string) {
	h.t.Helper()
	if err := s.conn.Send(f); err != nil {
		h.sendFailed(s, what, err)
	}
}

// sendFailed reads the stream that a send found ended to the frame that ended it, which is on its way to the reader or is there, and
// fails the run with what that frame is the answer to.
func (h *ledgerHost) sendFailed(s *ledgerStream, what string, err error) {
	h.t.Helper()
	for {
		frame, rerr := h.recv(s)
		if rerr != nil {
			h.fatalf("send %s on stream %d: %v: the stream had ended with %v", what, s.n, err, rerr)
		}
		if isLast(frame) {
			cause, _ := h.attribution(s, frame)
			h.fatalf("send %s on stream %d: %v: the stream had ended with %s already: %s", what, s.n, err, ledgerLast(frame), cause)
		}
	}
}

// isLast reports whether a frame is the last of its stream.
func isLast(f *execv2.ServerFrame) bool { return f.GetEnded() != nil || f.GetDetached() != nil }

// apply puts a frame of the stream in hand in the ledger, and lets a gated author run to the budget past what the host now has.
func (h *ledgerHost) apply(f *execv2.ServerFrame) {
	h.lg.frame(f)
	h.frames++
	if h.author.gated {
		h.author.allow(h.lg.have + h.v.budget)
	}
}

// read reads up to k frames of the stream in hand into the ledger. The stream may not end by itself.
func (h *ledgerHost) read(s *ledgerStream, k int) {
	h.t.Helper()
	for range k {
		frame, err := h.recv(s)
		if err != nil {
			h.fatalf("stream %d broke while it was read: %v", s.n, err)
		}
		if isLast(frame) {
			cause, _ := h.attribution(s, frame)
			h.fatalf("stream %d ended with %s before the host asked it to leave: %s", s.n, ledgerLast(frame), cause)
		}
		h.apply(frame)
		h.check()
	}
}

// finish reads the stream to its last frame, which it returns, and then to io.EOF: nothing follows the last frame. The frames go to
// the ledger when apply is true, and to a shadow of the stream's state otherwise (a stream that was superseded is read to be held
// to the contract, and never applied: the host has attached another from what it held).
func (h *ledgerHost) finish(s *ledgerStream, shadow *ledger) *execv2.ServerFrame {
	h.t.Helper()
	var last *execv2.ServerFrame
	for last == nil {
		frame, err := h.recv(s)
		if err != nil {
			h.fatalf("stream %d broke before its last frame: %v", s.n, err)
		}
		if isLast(frame) {
			last = frame
			break
		}
		if shadow == nil {
			h.apply(frame)
			h.check()
		} else {
			shadow.frame(frame)
		}
	}
	if _, err := h.recv(s); !errors.Is(err, io.EOF) {
		h.fatalf("stream %d: after its last frame (%s) the stream ended with %v, want io.EOF", s.n, frameKind(last), err)
	}
	h.attribute(s, last)
	if shadow != nil && len(shadow.problems) > 0 {
		h.fatalf("stream %d, which was superseded: %s", s.n, strings.Join(shadow.problems, "\n\t"))
	}
	return last
}

// shadowOf makes the ledger that holds a stream that is not applied to the contract: it starts where the ledger's stream in hand is,
// and keeps its own accounts, so that nothing a superseded stream sends reaches the host's.
func (h *ledgerHost) shadowOf(l *ledger) *ledger {
	return &ledger{
		exact: l.exact, lossy: l.lossy, tty: l.tty, ringBytes: l.ringBytes, chunk: l.chunk, closed: l.closed,
		digest: sha256.New(), next: l.next, replayEnd: l.replayEnd, asked: l.asked,
	}
}

// shadowFor makes the shadow of a stream that the host attached and is not going to apply.
func (h *ledgerHost) shadowFor(s *ledgerStream) *ledger {
	shadow := h.shadowOf(h.lg)
	shadow.next, shadow.replayEnd = 0, 0
	shadow.begin(s.opened, s.asked)
	if len(shadow.problems) > 0 {
		h.fatalf("stream %d, which was superseded: %s", s.n, strings.Join(shadow.problems, "\n\t"))
	}
	return shadow
}

// expectSuperseded holds the last frame of a stream that another attach took the session from.
func (h *ledgerHost) expectSuperseded(s *ledgerStream, last *execv2.ServerFrame) {
	h.t.Helper()
	if d := last.GetDetached(); d == nil || d.GetReason() != execv2.DetachReason_DETACH_REASON_SUPERSEDED || d.GetLeaseExpiresUnixMs() != 0 {
		h.fatalf("stream %d, which another attach superseded, ended with %v, want Detached{SUPERSEDED} with no lease", s.n, last)
	}
}

// attribution says what the last frame of a stream is the answer to among the things the host did, and whether it is one: a stream is told
// Ended after the host sent Close on it, Detached{REQUESTED} after the host sent Detach on it, Detached{SUPERSEDED} (with no lease) when an
// attach of the host can have registered after the stream's own, and Detached with no reason after the host cancelled it. Nothing else ends
// a stream.
func (h *ledgerHost) attribution(s *ledgerStream, last *execv2.ServerFrame) (string, bool) {
	if s.ev == nil {
		return "the host kept no record of this stream", true
	}
	at := int(s.lastAt.Load())
	did := func(when *atomic.Int64) bool {
		n := int(when.Load())
		return n != 0 && n < at
	}
	d := last.GetDetached()
	switch {
	case last.GetEnded() != nil:
		if did(&s.closeAt) {
			return "the host sent Close on it", true
		}
		return "the host sent no Close on this stream, and nothing but a Close ends the session of a run", false
	case d == nil:
		return fmt.Sprintf("a %s frame is not the last frame of a stream", frameKind(last)), false
	case d.GetReason() == execv2.DetachReason_DETACH_REASON_REQUESTED:
		if did(&s.detachAt) {
			return "the host sent Detach on it", true
		}
		return "the host sent no Detach on this stream", false
	case d.GetReason() == execv2.DetachReason_DETACH_REASON_SUPERSEDED:
		if d.GetLeaseExpiresUnixMs() != 0 {
			return "a superseded stream has no lease, and this one is told one", false
		}
		if x := h.ev.supersessor(s.attach, at); x != nil {
			return "it can be the answer to " + h.ev.describe(x), true
		}
		return fmt.Sprintf("no attach of the host can have taken the session from it: every attach but its own (%s) was over before the host made its own, or was made after this frame came",
			h.ev.describe(s.attach)), false
	default:
		if did(&s.leftAt) {
			return "the host cancelled the stream", true
		}
		return "the host neither half-closed nor cancelled this stream", false
	}
}

// attribute fails the run when the last frame of a stream is not the answer to anything that the host did.
func (h *ledgerHost) attribute(s *ledgerStream, last *execv2.ServerFrame) {
	h.t.Helper()
	if cause, ok := h.attribution(s, last); !ok {
		h.fatalf("stream %d ended with %s, which is the answer to nothing that the host did: %s", s.n, ledgerLast(last), cause)
	}
}

// attachConn attaches a stream from an offset, as the kind of attach that the host's record calls it. The attaches that the host made late
// on purpose are let go once this one has been answered: they come after it.
func (h *ledgerHost) attachConn(asked uint64, kind string) *ledgerStream {
	h.t.Helper()
	a := h.ev.issue(kind, asked)
	conn, err := h.attachCall(context.Background(), asked, "")
	h.ev.answer(a, err)
	if err != nil {
		h.fatalf("attach from %d: %v", asked, err)
	}
	h.letLate()
	return h.newStream(conn, asked, a)
}

// adopt makes the stream the host's: its Opened starts the ledger's stream.
func (h *ledgerHost) adopt(s *ledgerStream) {
	h.t.Helper()
	h.cur = s
	h.lg.begin(s.opened, s.asked)
	if got := s.opened.GetFacts().GetSessionId(); got != h.id {
		h.fatalf("the attach's Opened names the session %q", got)
	}
	h.check()
	if h.author.gated {
		h.author.allow(h.lg.have + h.v.budget)
	}
}

// attach is how a host that left comes back: from what it has, perhaps after attaches that die before their Opened, and perhaps from
// two goroutines at once.
func (h *ledgerHost) attach() {
	h.t.Helper()
	for h.rng.IntN(6) == 0 {
		h.note("cancelled-attach")
		h.cancelledAttach()
	}
	if h.rng.IntN(7) == 0 {
		h.note("concurrent-attach")
		h.concurrentAttach()
		return
	}
	h.adopt(h.attachConn(h.lg.have, "real"))
}

// cancelledAttach is an attach whose host gives up at once: the stream may die before the plugin answers, or right after. Either way
// the host has learned nothing and still asks from what it had. The plugin may never register it (its stream had ended), or register it
// and find the stream over: it then took the session from the stream that held it, and its leaving left the session detached for its lease.
// The host cannot tell which, and holds nothing of it. Half of them are late on purpose (lateAttach).
func (h *ledgerHost) cancelledAttach() {
	if h.rng.IntN(2) == 0 {
		h.lateAttach()
		return
	}
	a := h.ev.issue("cancelled", h.lg.have)
	ctx, cancel := context.WithCancel(context.Background())
	go cancel()
	conn, err := h.attachCall(ctx, h.lg.have, "")
	h.ev.answer(a, err)
	if err == nil {
		_ = conn.Close()
	}
	cancel()
}

// lateAttach is a cancelled attach whose handler is late on purpose (ledgerLate): the host waits until the plugin holds the handler with the
// open frame read and nothing registered, and then cancels the stream. The attach that the host makes next is registered, answered and
// held while the handler is held, and the handler goes on afterwards, with the stream's context ended: the order that a runner with few
// processors once reached by chance. The engine registers a stream only while its stream has not ended, so the handler takes the session
// from no one, and the host's stream keeps it.
func (h *ledgerHost) lateAttach() {
	h.t.Helper()
	a := h.ev.issue("cancelled, held late", h.lg.have)
	tag := fmt.Sprintf("late-%d", a.n)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type answer struct {
		conn exec.Conn
		err  error
	}
	done := make(chan answer, 1)
	go func() {
		conn, err := h.attachCall(ctx, a.from, tag)
		done <- answer{conn, err}
	}()
	arrived, _ := h.late.chans(tag)
	select {
	case <-arrived:
	case r := <-done:
		h.fatalf("the attach that was to be held came back with %v before its handler reached the plugin's seam", r.err)
	case <-time.After(engineWait):
		h.fatalf("the handler of the attach that was to be held did not reach the plugin's seam within %v", engineWait)
	}
	h.pendingLate = append(h.pendingLate, tag)
	h.lateMade++
	h.ev.add("attach #%d: its handler is held, the open frame read and nothing registered: the host cancels the stream", a.n)
	cancel()
	select {
	case r := <-done:
		h.ev.answer(a, r.err)
		if r.err == nil {
			_ = r.conn.Close()
			h.fatalf("the attach whose handler is held was answered with its Opened")
		}
	case <-time.After(engineWait):
		h.fatalf("the cancelled attach did not come back within %v", engineWait)
	}
}

// concurrentAttach attaches from two goroutines at the same time, as two windows would. One of them is the session's stream and the
// other was superseded; the host reads both until the loser says so, applies what the winner sent, and goes on with it.
func (h *ledgerHost) concurrentAttach() {
	h.t.Helper()
	asked := h.lg.have
	type attachAnswer struct {
		i    int
		conn exec.Conn
		err  error
	}
	made := [2]*ledgerAttach{h.ev.issue("concurrent", asked), h.ev.issue("concurrent", asked)}
	answers := make(chan attachAnswer, 2)
	for i := range made {
		go func() {
			conn, err := h.attachCall(context.Background(), asked, "")
			h.ev.answer(made[i], err)
			answers <- attachAnswer{i, conn, err}
		}()
	}
	var two [2]*ledgerStream
	for k := range two {
		a := <-answers
		if a.err != nil {
			h.fatalf("a concurrent attach from %d: %v", asked, a.err)
		}
		two[k] = h.newStream(a.conn, asked, made[a.i])
	}
	h.letLate()
	// Read both, a frame at a time from whichever has one, until one of them has its last frame: that one is the loser.
	var frames [2][]*execv2.ServerFrame
	loser := -1
	for loser < 0 {
		var i int
		var r ledgerRecv
		select {
		case r = <-two[0].out:
			i = 0
		case r = <-two[1].out:
			i = 1
		case <-time.After(engineWait):
			h.fatalf("a concurrent attach: neither stream said that it was superseded within %v", engineWait)
		}
		if r.err != nil {
			h.fatalf("a concurrent attach: stream %d broke: %v", two[i].n, r.err)
		}
		frames[i] = append(frames[i], r.frame)
		if isLast(r.frame) {
			loser = i
		}
	}
	winner := 1 - loser
	// The loser is held to the contract and dropped; the frames of the winner so far are the host's.
	shadow := h.shadowFor(two[loser])
	last := frames[loser][len(frames[loser])-1]
	for _, f := range frames[loser][:len(frames[loser])-1] {
		shadow.frame(f)
	}
	h.attribute(two[loser], last)
	h.expectSuperseded(two[loser], last)
	if _, err := h.recv(two[loser]); !errors.Is(err, io.EOF) {
		h.fatalf("stream %d: after Detached the stream ended with %v, want io.EOF", two[loser].n, err)
	}
	if len(shadow.problems) > 0 {
		h.fatalf("the stream that was superseded by a concurrent attach: %s", strings.Join(shadow.problems, "\n\t"))
	}
	h.adopt(two[winner])
	for _, f := range frames[winner] {
		if isLast(f) {
			cause, _ := h.attribution(two[winner], f)
			h.fatalf("both streams of a concurrent attach ended (stream %d with %s): a session has one stream at a time; %s", two[winner].n, ledgerLast(f), cause)
		}
		h.apply(f)
		h.check()
	}
}

// ---------------------------------------------------------------------------------------------------------------
// the moves
// ---------------------------------------------------------------------------------------------------------------

func detachFrame(leaseMs uint32) *execv2.ClientFrame {
	return &execv2.ClientFrame{Frame: &execv2.ClientFrame_Detach{Detach: &execv2.Detach{LeaseMs: leaseMs}}}
}

// let the author run while the host is away: until it has written some more, or, for a gated author, until it is held at its budget.
func (h *ledgerHost) letTheAuthorRun() {
	h.t.Helper()
	more := []uint64{0, 0, 0, 100, 4096, 64 << 10, 300_000, 1 << 20, 3 << 20}[h.rng.IntN(9)]
	target := h.author.written.Load() + more
	if h.author.gated {
		target = min(target, h.author.allowed.Load())
	}
	if !proofPoll(engineWait, func() bool { return h.author.written.Load() >= target }) {
		h.fatalf("the author wrote %d bytes within %v, it was to reach %d", h.author.written.Load(), engineWait, target)
	}
}

// detach asks the session to let the stream go: it reads what the stream still holds, up to Detached, which carries the lease.
func (h *ledgerHost) detach(twice bool) {
	h.t.Helper()
	lease := []uint32{0, 0, 60_000, 4_000_000}[h.rng.IntN(4)]
	want := 5 * time.Minute // Session.DetachTimeout
	switch {
	case lease > uint32(ledgerMaxLease/time.Millisecond):
		want = ledgerMaxLease
	case lease > 0:
		want = time.Duration(lease) * time.Millisecond
	}
	h.cur.detachAt.Store(int64(h.ev.add("stream %d: the host sends Detach (lease %d ms)", h.cur.n, lease)))
	h.send(h.cur, detachFrame(lease), "Detach")
	if twice {
		_ = h.cur.conn.Send(detachFrame(lease)) // never applied: the stream has left
	}
	last := h.finish(h.cur, nil)
	d := last.GetDetached()
	if d == nil || d.GetReason() != execv2.DetachReason_DETACH_REASON_REQUESTED {
		h.fatalf("the answer to Detach is %v, want Detached{REQUESTED}", last)
	}
	// The engine's clock stands still: the lease is the one that was asked for, clamped to the target's maximum, to the millisecond.
	if got, wantMs := d.GetLeaseExpiresUnixMs(), ledgerEpoch.UnixMilli()+want.Milliseconds(); got != wantMs {
		h.fatalf("a Detach with a lease of %d ms ended its stream with a lease that runs out at %d (unix ms), want %d: %v after the engine's clock, which stands still at %d",
			lease, got, wantMs, want, ledgerEpoch.UnixMilli())
	}
	h.cur.retire()
	h.cur = nil
}

// kill drops the stream, as a host that lost its connection or closed its window does: nothing of what is in flight is applied.
func (h *ledgerHost) kill() {
	h.cur.retire()
	h.cur = nil
}

// supersede attaches again while the stream is attached and flooding, once or twice in a row, without reading the older one, after
// attaches that the host gives up on at once (cancels of them): one can register before the host's own, take the session from the stream and
// leave it detached for its lease, or be refused, and the host's own attach finds the session either way. The streams that were superseded are
// read to their end and held to the contract; none of their frames is applied.
func (h *ledgerHost) supersede(times, cancels int) {
	h.t.Helper()
	old := h.cur
	oldShadow := h.shadowOf(h.lg)
	for range cancels {
		h.note("cancelled-attach")
		h.cancelledAttach()
	}
	var retired []*ledgerStream
	var shadows []*ledger
	for i := range times {
		s := h.attachConn(h.lg.have, "real")
		if i < times-1 {
			retired = append(retired, s)
			shadows = append(shadows, h.shadowFor(s))
			continue
		}
		h.adopt(s)
	}
	h.expectSuperseded(old, h.finish(old, oldShadow))
	for i, s := range retired {
		h.expectSuperseded(s, h.finish(s, shadows[i]))
	}
}

// detachAndKill sends Detach and cancels the stream at the same moment: the session is detached whichever the plugin sees first.
func (h *ledgerHost) detachAndKill() {
	h.cur.detachAt.Store(int64(h.ev.add("stream %d: the host sends Detach and cancels the stream", h.cur.n)))
	_ = h.cur.conn.Send(detachFrame(0))
	h.kill()
}

// readWhileAway reads the ring with ReadOutput, as a host does that has no stream, and puts what it gets in the ledger.
func (h *ledgerHost) readWhileAway() {
	h.t.Helper()
	for range 1 + h.rng.IntN(3) {
		maxBytes := []int{0, 1, 4096, 100_000, 1 << 20, 5 << 20}[h.rng.IntN(6)]
		from := h.lg.have
		resp, err := h.rig.client.ReadOutput(context.Background(), &execv2.ReadOutputRequest{SessionId: h.id, FromOffset: from, MaxBytes: uint32(maxBytes)})
		if err != nil {
			h.fatalf("ReadOutput(%d, %d): %v", from, maxBytes, err)
		}
		if resp.GetEnded() {
			h.fatalf("ReadOutput says the session ended")
		}
		h.lg.read(resp, from, ledgerReadBound(maxBytes))
		h.frames++
		h.check()
		// A gated author is not let go further here: a ReadOutput takes nothing from the ring's accounts (the session counts a byte as
		// dropped when it leaves the ring before a stream took it), so the author waits for the attach, which hands the ring over.
	}
	h.note("read-while-away")
}

// ledgerReadBound is the most bytes a ReadOutput of max_bytes may answer: the default of 64 KiB for zero, never more than the ring
// holds or 1 MiB.
func ledgerReadBound(maxBytes int) int {
	if maxBytes == 0 {
		return 64 << 10
	}
	return min(maxBytes, ledgerRing, 1<<20)
}

// ledgerMove is one thing the host does with the stream it has, which ends with the host attached again.
type ledgerMove struct {
	name   string
	weight int
	run    func(h *ledgerHost)
}

var ledgerMoves = []ledgerMove{
	{"detach", 4, func(h *ledgerHost) { h.detach(false); h.away(); h.attach() }},
	{"detach-twice", 1, func(h *ledgerHost) { h.detach(true); h.away(); h.attach() }},
	{"kill", 4, func(h *ledgerHost) { h.kill(); h.away(); h.attach() }},
	{"detach-and-kill", 1, func(h *ledgerHost) { h.detachAndKill(); h.away(); h.attach() }},
	{"supersede", 4, func(h *ledgerHost) { h.supersede(1, 0) }},
	{"supersede-twice", 1, func(h *ledgerHost) { h.supersede(2, 0) }},
	{"supersede-after-cancelled", 2, func(h *ledgerHost) { h.supersede(1, 1+h.rng.IntN(2)) }},
}

// away is what happens while the host has no stream: the author goes on, and the host may read the ring.
func (h *ledgerHost) away() {
	h.letTheAuthorRun()
	if h.rng.IntN(5) == 0 {
		h.readWhileAway()
	}
}

// step is one moment of the run: the host reads some of the stream it has, and then leaves it one way and comes back.
func (h *ledgerHost) step() {
	h.t.Helper()
	h.moment++
	h.read(h.cur, []int{0, 0, 1, 2, 3, 5, 8, 13, 34}[h.rng.IntN(9)])
	total := 0
	for _, m := range ledgerMoves {
		total += m.weight
	}
	pick := h.rng.IntN(total)
	for _, m := range ledgerMoves {
		if pick -= m.weight; pick < 0 {
			h.note(m.name)
			m.run(h)
			return
		}
	}
}

// ---------------------------------------------------------------------------------------------------------------
// the run
// ---------------------------------------------------------------------------------------------------------------

// ledgerRun is one run of a variant with a seed: open the session, leave and come back `moments` times, end the session one of two
// ways, read what is left, and hold the end to everything.
func ledgerRun(t *testing.T, v ledgerVariant, seed uint64, moments int) {
	t.Helper()
	author := newLedgerAuthor(seed, v.tty, true)
	rig := newProofRig(t,
		proofTarget("ledger", author.run, func(i *exec.TargetInfo) {
			i.Capabilities.Stall = exec.StallPropagates
			if v.lossy {
				i.Capabilities.Stall = exec.StallLossy
			}
		}),
		proofTarget("warm", proofEchoFunction),
	)
	late := newLedgerLate()
	exec.SetPluginEngineSeams(rig.server.Plugin, exec.EngineSeams{Now: ledgerNow, NewLeaseTimer: ledgerLeaseTimer})
	exec.SetPluginBeforeAttach(rig.server.Plugin, late.hold)
	// The connection and its goroutines exist before the baseline: a session that is echoed and ended.
	warm := rig.mustOpenKind("warm", "warm", proofTTY)
	if _, err := newProofEchoer(warm).measure(3); err != nil {
		t.Fatalf("warm-up: %v", err)
	}
	_ = warm.Close()
	proofEndDetached(t, rig.client, "warm")
	baseline := lifecycletest.TakeBaseline()

	h := &ledgerHost{
		t: t, v: v, seed: seed, rig: rig, author: author, id: fmt.Sprintf("ledger-%d", seed),
		lg: newLedger(v.exact, v.lossy, v.tty, ledgerRing), rng: rand.New(rand.NewPCG(seed, 0x1ED6)), tally: map[string]int{}, ev: newLedgerEvents(),
		late: late,
	}
	defer func() {
		h.letLate() // a handler that is held when the run fails is let go, or the plugin would wait for it
		for _, s := range h.streams {
			s.retire()
		}
	}()
	began := time.Now()
	author.allow(v.budget)
	var mods []func(*execv2.Start)
	if v.tty {
		mods = append(mods, proofTTY)
	}
	first := h.ev.issue("start", 0)
	conn, err := rig.openKind(context.Background(), "ledger", h.id, mods...)
	h.ev.answer(first, err)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	h.cur = h.newStream(conn, 0, first)
	h.lg.begin(h.cur.opened, 0)
	h.check()

	for h.moment < moments {
		h.step()
	}
	h.finishRun(baseline)
	t.Logf("%s: %d moments in %v: %d streams, %d frames and reads, %d bytes received, %d announced lost in %d Dropped frames; the author wrote %d bytes; %d attaches made late and held; moves %v",
		h.where(), h.moment, time.Since(began).Round(time.Millisecond), h.lg.streams, h.frames, h.lg.bytesIn, h.lg.bytesLo, h.lg.dropFrames, author.written.Load(), h.lateMade, h.tally)
}

// finishRun ends the session, one way or the other, reads the rest, and holds the end to the invariants.
func (h *ledgerHost) finishRun(baseline lifecycletest.GoroutineBaseline) {
	h.t.Helper()
	h.read(h.cur, []int{0, 1, 5, 21}[h.rng.IntN(4)])
	var ended *execv2.Ended
	h.lg.closed = true
	if h.rng.IntN(2) == 0 {
		// Close on the stream, and every frame to the Ended.
		h.note("final: Close on the stream")
		h.cur.closeAt.Store(int64(h.ev.add("stream %d: the host sends Close", h.cur.n)))
		h.send(h.cur, &execv2.ClientFrame{Frame: &execv2.ClientFrame_Close{Close: &execv2.Close{Mode: execv2.CloseMode_CLOSE_MODE_GRACEFUL}}}, "Close")
		ended = h.finish(h.cur, nil).GetEnded()
		if ended == nil {
			h.fatalf("the stream that closed the session ended with Detached, want Ended")
		}
		h.cur.retire()
	} else {
		// The host's stream breaks; EndSession ends the session once the plugin has noticed, and the rest is read from the tombstone.
		h.note("final: EndSession and ReadOutput")
		h.kill()
		if !proofPoll(engineWait, func() bool {
			err := h.rig.client.EndSession(context.Background(), &execv2.EndSessionRequest{SessionId: h.id, Mode: execv2.CloseMode_CLOSE_MODE_GRACEFUL})
			if err != nil && !isRefusalOf(err, exec.ErrorClassConflict, exec.ErrorReasonUnspecified) {
				h.fatalf("EndSession: %v", err)
			}
			return err == nil
		}) {
			h.fatalf("EndSession was refused for %v: the plugin did not notice that the stream was gone", engineWait)
		}
		info := proofListed(h.rig.client, h.id)
		if info == nil || info.GetEndedWith() == nil {
			h.fatalf("ListSessions after EndSession: %v, want the tombstone with its end", info)
		}
		ended = info.GetEndedWith()
		for {
			maxBytes := []int{0, 100_000, 1 << 20}[h.rng.IntN(3)]
			from := h.lg.have
			resp, err := h.rig.client.ReadOutput(context.Background(), &execv2.ReadOutputRequest{SessionId: h.id, FromOffset: from, MaxBytes: uint32(maxBytes)})
			if err != nil {
				h.fatalf("ReadOutput of the tombstone from %d: %v", from, err)
			}
			if !resp.GetEnded() {
				h.fatalf("ReadOutput of a tombstone does not say that the session ended")
			}
			h.lg.read(resp, from, ledgerReadBound(maxBytes))
			h.check()
			if h.lg.have >= ended.GetLastOffset() {
				break
			}
			if resp.GetNextOffset() == from && len(resp.GetChunks()) == 0 {
				h.fatalf("ReadOutput from %d answered nothing and the host holds %d of %d", from, h.lg.have, ended.GetLastOffset())
			}
		}
	}
	h.cur = nil
	select {
	case <-h.author.done:
	case <-time.After(engineWait):
		h.fatalf("the author's function did not return within %v of the end", engineWait)
	}
	wrote := h.author.written.Load()
	h.lg.finish(ended.GetLastOffset(), h.author.sum, wrote)
	h.check()

	// The accounts of the end.
	if closed := ended.GetEnd().GetClosed(); closed.GetBy() != execv2.ClosedBy_CLOSED_BY_REQUEST {
		h.fatalf("the session ended %v, want Closed by REQUEST", ended.GetEnd())
	}
	if ended.GetBytesOut() != wrote || ended.GetLastOffset() != wrote {
		h.fatalf("Ended.bytes_out is %d and last_offset %d, the author wrote %d", ended.GetBytesOut(), ended.GetLastOffset(), wrote)
	}
	if ended.GetDroppedBytes() < h.lg.dropBytes {
		h.fatalf("Ended.dropped_bytes is %d, the streams were told of %d in Dropped frames", ended.GetDroppedBytes(), h.lg.dropBytes)
	}
	if (ended.GetOutput() == execv2.OutputCompleteness_OUTPUT_COMPLETENESS_TRUNCATED) != (ended.GetDroppedBytes() > 0) {
		h.fatalf("Ended.output is %v after %d dropped bytes: TRUNCATED exactly when something was dropped", ended.GetOutput(), ended.GetDroppedBytes())
	}
	if h.v.exact && (ended.GetDroppedBytes() != 0 || ended.GetOutput() == execv2.OutputCompleteness_OUTPUT_COMPLETENESS_TRUNCATED) {
		h.fatalf("a run that must lose nothing dropped %d bytes: output %v", ended.GetDroppedBytes(), ended.GetOutput())
	}

	// The end state: the plugin holds no session, one tombstone with the end, and no goroutine over the baseline.
	plugin := h.rig.server.Plugin
	if !proofPoll(engineWait, func() bool { return len(exec.PluginLive(plugin)) == 0 }) {
		h.fatalf("the plugin still holds %v", exec.PluginLive(plugin))
	}
	count, retained := exec.PluginTombstones(plugin)
	if count < 1 || count > 2 || retained > ledgerRing+proofMaxFrame { // this session, and the warm-up's unless it expired
		h.fatalf("the plugin holds %d tombstones that keep %d bytes, want 2 (the warm-up and this session) and at most the ring", count, retained)
	}
	info := proofListed(h.rig.client, h.id)
	if info == nil || !proto.Equal(info.GetEndedWith(), ended) || info.GetAttached() || info.GetLeaseExpiresUnixMs() != 0 || info.GetNextOffset() != wrote {
		h.fatalf("ListSessions says %v of the session that ended with %v", info, ended)
	}
	for _, s := range h.streams {
		s.retire()
	}
	if err := baseline.Check(engineWait); err != nil {
		h.fatalf("goroutines at rest: %v", err)
	}
}

// The seeds are fixed so that a failure replays; a longer soak can name others: go test -run ... -args is not needed, the seeds
// are in the test's name.
func ledgerSeeds() []uint64 {
	if proofRace || testing.Short() {
		return []uint64{1, 2}
	}
	return []uint64{1, 2, 3}
}

func TestDetachAndReattachUnderAFloodIsByteExactByOffset(t *testing.T) {
	moments := proofScaled(1000, 120)
	for _, v := range ledgerVariants {
		t.Run(v.name, func(t *testing.T) {
			for _, seed := range ledgerSeeds() {
				t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
					ledgerRun(t, v, seed, moments)
				})
			}
		})
	}
}
