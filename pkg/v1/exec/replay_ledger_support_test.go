package exec_test

import (
	"cmp"
	"context"
	"crypto/sha256"
	"fmt"
	"hash"
	"math/rand/v2"
	"slices"
	"sort"
	"sync/atomic"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The ledger is what a host would build to prove that a session's output survives the host coming and going: it assembles the
// output of ONE session by offset from every stream the host had of it (the replay and the live frames of each attach, and the
// cursor reads of ReadOutput), and holds each byte to the one rule that makes the assembly trustworthy: it is received exactly
// once, it is the byte that the author wrote at that offset (the pattern is a pure function of the offset, so a byte that moved by
// one, came from another chunk or was taken twice shows at once), and an offset that was not received lies in a range that the
// plugin announced (a Dropped frame, a replay that starts after the offset the host asked for). Nothing is trusted to be in
// order: each frame is placed by its offset, and a byte placed twice or a hole nobody announced is a failure.

// ledgerMul is the multiplier of the pattern: the byte at offset i is the top byte of i * ledgerMul. It has no period that any
// write, frame, chunk or ring size could line up with, and a reader can check it from the offset alone.
const ledgerMul = 0x9E3779B97F4A7C15

func ledgerByte(i uint64) byte { return byte(i * ledgerMul >> 56) }

// ledgerFill writes the pattern from offset off into buf.
func ledgerFill(buf []byte, off uint64) {
	x := off * ledgerMul
	for i := range buf {
		buf[i] = byte(x >> 56)
		x += ledgerMul
	}
}

// ledgerMismatch returns the index of the first byte of data that is not the pattern at off, or -1.
func ledgerMismatch(data []byte, off uint64) int {
	x := off * ledgerMul
	for i, b := range data {
		if b != byte(x>>56) {
			return i
		}
		x += ledgerMul
	}
	return -1
}

// ledgerRunBits: a session without a terminal writes two streams, and the stream of the byte at offset i is a function of i too:
// runs of 1 << ledgerRunBits bytes alternate standard output and standard error, so a chunk of the ring holds both, a frame is
// always one run of one stream, and no frame may cross the end of a run.
const ledgerRunBits = 15

func ledgerStreamAt(i uint64, tty bool) execv2.Stream {
	switch {
	case tty:
		return execv2.Stream_STREAM_TTY
	case i>>ledgerRunBits&1 == 0:
		return execv2.Stream_STREAM_STDOUT
	}
	return execv2.Stream_STREAM_STDERR
}

// ---------------------------------------------------------------------------------------------------------------
// the author
// ---------------------------------------------------------------------------------------------------------------

// ledgerAuthor is a function that floods the session with the pattern, in writes of every size that matters (one byte, a few,
// a page, a frame, a chunk, more than a chunk), and ends when its session does. It keeps the SHA-256 of what the session accepted.
//
// A gated author writes no further than allowed, which the host moves to what it has plus the budget: the flood is continuous, and
// never more than the budget ahead of the host. A budget under what the ring keeps means nothing the host asks for is ever gone (an
// exact run); a budget over the ring means a detached session overflows it, and a lossy one overflows its host's reading (an
// overflow run). An author that is not gated would write as fast as it can whatever the host does; no run uses one, because on a
// single processor such a flood holds the processor and the host that reads it does not run.
type ledgerAuthor struct {
	seed  uint64
	tty   bool
	gated bool

	allowed atomic.Uint64
	raise   chan struct{}
	written atomic.Uint64 // bytes the session accepted: the offset of the author's next byte
	done    chan struct{} // closed when the function has returned; sum is set before
	sum     []byte        // SHA-256 of the bytes that the session accepted, in order
	calls   atomic.Int32
}

func newLedgerAuthor(seed uint64, tty, gated bool) *ledgerAuthor {
	return &ledgerAuthor{seed: seed, tty: tty, gated: gated, raise: make(chan struct{}, 1), done: make(chan struct{})}
}

// allow lets the author write up to limit; the limit only moves forward.
func (a *ledgerAuthor) allow(limit uint64) {
	for {
		cur := a.allowed.Load()
		if limit <= cur || a.allowed.CompareAndSwap(cur, limit) {
			break
		}
	}
	select {
	case a.raise <- struct{}{}:
	default:
	}
}

var ledgerWriteSizes = [...]int{1, 7, 100, 4096, 32 << 10, 64 << 10, 100_000, 200_000}

func (a *ledgerAuthor) run(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
	if a.calls.Add(1) != 1 {
		return exec.Exit{}, fmt.Errorf("the ledger author runs once")
	}
	defer close(a.done)
	rng := rand.New(rand.NewPCG(a.seed, 0xA07))
	digest := sha256.New()
	buf := make([]byte, 200_000)
	var off uint64
	for ctx.Err() == nil {
		n := ledgerWriteSizes[rng.IntN(len(ledgerWriteSizes))]
		if a.gated {
			var allowed uint64
			for {
				if allowed = a.allowed.Load(); off < allowed {
					break
				}
				select {
				case <-a.raise:
				case <-ctx.Done():
					a.sum = digest.Sum(nil)
					return exec.Exit{}, ctx.Err()
				}
			}
			n = int(min(uint64(n), allowed-off))
		}
		w := p.Stdout
		if !a.tty {
			// a write never crosses the end of a run of one stream
			n = int(min(uint64(n), 1<<ledgerRunBits-off&(1<<ledgerRunBits-1)))
			if ledgerStreamAt(off, false) == execv2.Stream_STREAM_STDERR {
				w = p.Stderr
			}
		}
		ledgerFill(buf[:n], off)
		got, err := w.Write(buf[:n])
		digest.Write(buf[:got])
		off += uint64(got)
		a.written.Store(off)
		if err != nil {
			break
		}
	}
	a.sum = digest.Sum(nil)
	return exec.Exit{}, ctx.Err()
}

// ---------------------------------------------------------------------------------------------------------------
// the intervals
// ---------------------------------------------------------------------------------------------------------------

type span struct{ lo, hi uint64 }

// spans is a set of disjoint intervals of offsets, sorted, with the ones that touch merged.
type spans []span

// add puts [lo, hi) in the set. It reports false, and changes nothing, when part of it is already there.
func (s *spans) add(lo, hi uint64) bool {
	i := sort.Search(len(*s), func(i int) bool { return (*s)[i].hi > lo })
	if i < len(*s) && (*s)[i].lo < hi {
		return false
	}
	merge := span{lo, hi}
	from, to := i, i
	if i > 0 && (*s)[i-1].hi == lo {
		merge.lo, from = (*s)[i-1].lo, i-1
	}
	if i < len(*s) && (*s)[i].lo == hi {
		merge.hi, to = (*s)[i].hi, i+1
	}
	*s = slices.Replace(*s, from, to, merge)
	return true
}

// overlaps reports whether any of [lo, hi) is in the set.
func (s spans) overlaps(lo, hi uint64) bool {
	i := sort.Search(len(s), func(i int) bool { return s[i].hi > lo })
	return i < len(s) && s[i].lo < hi
}

// ---------------------------------------------------------------------------------------------------------------
// the ledger
// ---------------------------------------------------------------------------------------------------------------

// ledger is the host's assembly of one session. It is fed by one goroutine at a time.
type ledger struct {
	// exact: nothing may be lost. A Dropped frame or a replay that starts after the offset asked for is a failure.
	exact bool
	// lossy: the target declared STALL_LOSSY, so a stream may be told of a range it lost, as EAGER_DRAIN_OVERFLOW.
	lossy bool
	tty   bool
	// ringBytes and chunk are the ring's declared size and the chunk it is cut in (a ring of 1 MiB has chunks of 64 KiB): the front
	// of a ring and the end of a range it lost are chunk boundaries.
	ringBytes uint64
	chunk     uint64
	// closed says the host has asked the session to end: a session that is ending never holds its author, so a target that
	// propagates a stalled reader may lose bytes to it (REPLAY_FULL) from then on, and only then.
	closed bool

	have             uint64 // every offset below it was received or lies in an announced range: the offset to attach from
	got, lost        spans
	digest           hash.Hash // SHA-256 over the bytes received, in offset order
	bytesIn, bytesLo uint64
	dropFrames       int
	dropBytes        uint64 // bytes the Dropped frames said were lost, which the end's dropped_bytes has to cover
	streams          int
	started          int // Started frames seen, across the streams of the session

	// the stream in hand
	next, replayEnd uint64
	asked           uint64

	problems []string
}

func newLedger(exact, lossy, tty bool, ringBytes uint64) *ledger {
	return &ledger{exact: exact, lossy: lossy, tty: tty, ringBytes: ringBytes, chunk: 64 << 10, digest: sha256.New()}
}

func (l *ledger) fail(format string, args ...any) {
	if len(l.problems) < 6 {
		l.problems = append(l.problems, fmt.Sprintf(format, args...))
	}
}

func (l *ledger) inReplay() bool { return l.next < l.replayEnd }

// lose records the range [lo, hi) as one that the plugin said is lost.
func (l *ledger) lose(lo, hi uint64) {
	if l.got.overlaps(lo, hi) {
		l.fail("the lost range [%d, %d) holds bytes that were received", lo, hi)
		return
	}
	if !l.lost.add(lo, hi) {
		l.fail("the lost range [%d, %d) overlaps one that was announced before", lo, hi)
		return
	}
	l.bytesLo += hi - lo
}

// begin starts a stream: the Opened of an attach from offset asked. Its replay is held to the contract: bytes = end - start, and
// the start is never before the offset asked for (a byte would be taken twice); after it, the gap is the plugin's announcement of
// bytes that its ring no longer holds.
func (l *ledger) begin(opened *execv2.Opened, asked uint64) {
	l.streams++
	r := opened.GetReplay()
	if r == nil {
		l.fail("the attach from %d was answered with no replay", asked)
		return
	}
	if r.GetEnd() < r.GetStart() || r.GetBytes() != r.GetEnd()-r.GetStart() {
		l.fail("the attach from %d: replay [%d, %d) says %d bytes, want end - start", asked, r.GetStart(), r.GetEnd(), r.GetBytes())
		return
	}
	if r.GetBytes() > l.ringBytes {
		l.fail("the attach from %d: a replay of %d bytes from a ring of %d", asked, r.GetBytes(), l.ringBytes)
	}
	switch {
	case r.GetStart() < asked:
		l.fail("the attach from %d: replay.start is %d, before the offset asked for: a byte would be taken twice", asked, r.GetStart())
		return
	case r.GetStart() > asked:
		if l.exact {
			l.fail("the attach from %d: replay.start is %d in a run that must lose nothing: the ring did not hold what the host had to ask for", asked, r.GetStart())
		}
		if r.GetStart()%l.chunk != 0 {
			l.fail("the attach from %d: replay.start is %d, which is not a chunk boundary (%d): a ring's front is one", asked, r.GetStart(), l.chunk)
		}
		l.lose(asked, r.GetStart())
	}
	l.asked, l.next, l.have, l.replayEnd = asked, r.GetStart(), r.GetStart(), r.GetEnd()
}

// frame applies a frame of the stream in hand.
func (l *ledger) frame(f *execv2.ServerFrame) {
	switch v := f.GetFrame().(type) {
	case *execv2.ServerFrame_Output:
		l.output(v.Output)
	case *execv2.ServerFrame_Dropped:
		l.dropped(v.Dropped)
	case *execv2.ServerFrame_Started:
		if l.started++; l.started > 1 {
			l.fail("a second Started in the life of the session: it has at most one")
		}
	case *execv2.ServerFrame_Notice, *execv2.ServerFrame_Link:
	default:
		l.fail("an unexpected %s frame in a stream", frameKind(f))
	}
}

// check holds one run of bytes (a frame, a chunk of ReadOutput) to the pattern and to the stream that its offset says.
func (l *ledger) check(what string, stream execv2.Stream, off uint64, data []byte, limit int) {
	n := uint64(len(data))
	if n == 0 || n > uint64(limit) {
		l.fail("%s of %d bytes at offset %d", what, n, off)
		return
	}
	if want := ledgerStreamAt(off, l.tty); stream != want || ledgerStreamAt(off+n-1, l.tty) != want {
		l.fail("%s at offset %d (%d bytes) is on stream %v, the author wrote [%d, %d) on %v: a frame is one run of one stream", what, off, n, stream, off, off+n, want)
	}
	if i := ledgerMismatch(data, off); i >= 0 {
		l.fail("%s at offset %d: byte %d (offset %d) is %#x, the author wrote %#x", what, off, i, off+uint64(i), data[i], ledgerByte(off+uint64(i)))
	}
}

func (l *ledger) output(o *execv2.Output) {
	off, n := o.GetOffset(), uint64(len(o.GetData()))
	if off != l.next {
		l.fail("an Output frame at offset %d, the stream is at %d: a byte was taken twice, or skipped without a word", off, l.next)
		l.next = off + n
		return
	}
	if l.inReplay() && off+n > l.replayEnd {
		l.fail("an Output frame [%d, %d) crosses replay.end %d: Opened describes exactly the frames that follow", off, off+n, l.replayEnd)
	}
	l.check("an Output frame", o.GetStream(), off, o.GetData(), proofMaxFrame)
	if l.lost.overlaps(off, off+n) {
		l.fail("the bytes [%d, %d) were received, and a range that holds them was announced lost", off, off+n)
	}
	if !l.got.add(off, off+n) {
		l.fail("the bytes [%d, %d) were received twice", off, off+n)
	}
	l.digest.Write(o.GetData())
	l.next, l.have, l.bytesIn = off+n, off+n, l.bytesIn+n
}

func (l *ledger) dropped(d *execv2.Dropped) {
	from, n := d.GetFromOffset(), d.GetBytes()
	switch {
	case l.exact:
		l.fail("a Dropped frame of %d bytes from %d in a run that must lose nothing", n, from)
		return
	case l.inReplay():
		l.fail("a Dropped frame at %d inside the replay that ends at %d: a replay that Opened described is exact", from, l.replayEnd)
	case n == 0 || from != l.next:
		l.fail("a Dropped range of %d bytes from %d, the stream is at %d: what is lost follows what was delivered", n, from, l.next)
		return
	}
	switch {
	case l.lossy && d.GetReason() != execv2.DropReason_DROP_REASON_EAGER_DRAIN_OVERFLOW:
		l.fail("a Dropped range of a lossy target with reason %v, want EAGER_DRAIN_OVERFLOW", d.GetReason())
	case !l.lossy && !l.closed:
		l.fail("a target that propagates a stalled reader dropped %d bytes from %d to a stream that was attached: it loses bytes only to a session that is ending", n, from)
	case !l.lossy && d.GetReason() != execv2.DropReason_DROP_REASON_REPLAY_FULL:
		l.fail("a Dropped range of a session that is ending with reason %v, want REPLAY_FULL", d.GetReason())
	}
	if (from+n)%l.chunk != 0 {
		l.fail("a Dropped range [%d, %d) does not end at a chunk boundary (%d): the ring loses whole chunks", from, from+n, l.chunk)
	}
	l.dropFrames++
	l.dropBytes += n
	l.lose(from, from+n)
	l.next, l.have = from+n, from+n
}

// read applies the answer of ReadOutput(from, maxBytes) to the ledger: the chunks are contiguous from the later of the offset
// asked for and the front of the ring, and a front after the offset asked for is a range that the ring no longer holds. It is for a
// host that has no stream.
func (l *ledger) read(resp *execv2.ReadOutputResponse, from uint64, maxBytes int) {
	start := resp.GetReplayStart()
	pos := max(from, start)
	if start > from {
		if l.exact {
			l.fail("ReadOutput from %d: the ring starts at %d in a run that must lose nothing", from, start)
		}
		if start%l.chunk != 0 {
			l.fail("ReadOutput from %d: replay_start is %d, which is not a chunk boundary (%d)", from, start, l.chunk)
		}
		l.lose(from, start)
	}
	total := 0
	for _, c := range resp.GetChunks() {
		if c.GetOffset() != pos {
			l.fail("ReadOutput from %d: a chunk at offset %d, the read is at %d", from, c.GetOffset(), pos)
			pos = c.GetOffset()
		}
		l.check("a ReadOutput chunk", c.GetStream(), c.GetOffset(), c.GetData(), maxBytes)
		n := uint64(len(c.GetData()))
		if !l.got.add(pos, pos+n) {
			l.fail("ReadOutput from %d: the bytes [%d, %d) were received twice", from, pos, pos+n)
		}
		l.digest.Write(c.GetData())
		l.bytesIn += n
		pos += n
		total += len(c.GetData())
	}
	if total > maxBytes {
		l.fail("ReadOutput from %d answered %d bytes, asked for at most %d", from, total, maxBytes)
	}
	if resp.GetNextOffset() != pos {
		l.fail("ReadOutput from %d: next_offset is %d, the chunks end at %d", from, resp.GetNextOffset(), pos)
	}
	l.next, l.have, l.replayEnd = pos, pos, 0
}

// finish holds the whole assembly to the end of the session: every offset of [0, last) is received or in an announced range, and
// the output is what the author wrote. The digest is the SHA-256 of what was received, which is the author's when nothing was lost.
func (l *ledger) finish(last uint64, authorSum []byte, authorWrote uint64) {
	if l.have != last {
		l.fail("the host holds output up to %d, the session ended at %d", l.have, last)
	}
	if authorWrote != last {
		l.fail("the author wrote %d bytes, the session ended at %d", authorWrote, last)
	}
	all := make(spans, 0, len(l.got)+len(l.lost))
	all = append(all, l.got...)
	all = append(all, l.lost...)
	slices.SortFunc(all, func(a, b span) int { return cmp.Compare(a.lo, b.lo) })
	var pos uint64
	for _, s := range all {
		if s.lo != pos {
			l.fail("the output has a hole at [%d, %d) that nobody announced", pos, s.lo)
			break
		}
		pos = s.hi
	}
	if pos != last {
		l.fail("the received and announced ranges end at %d, the session's output at %d", pos, last)
	}
	if l.bytesIn+l.bytesLo != last {
		l.fail("%d bytes received and %d announced lost: %d, the session's output is %d", l.bytesIn, l.bytesLo, l.bytesIn+l.bytesLo, last)
	}
	// The digest is the SHA-256 of the pattern over the ranges the ledger holds, in order: what it hashed is what it placed.
	if got, want := l.digest.Sum(nil), ledgerExpectedSum(l.got); string(got) != string(want) {
		l.fail("the SHA-256 of the assembled output is %x, the pattern over the ranges received hashes to %x", got[:8], want[:8])
	}
	if l.bytesLo == 0 {
		if got := l.digest.Sum(nil); string(got) != string(authorSum) {
			l.fail("nothing was lost and the SHA-256 of the assembled output is %x, the author's is %x", got[:8], authorSum[:8])
		}
	}
	if l.exact && l.bytesLo != 0 {
		l.fail("%d bytes were lost in a run that must lose nothing", l.bytesLo)
	}
}

// ledgerExpectedSum is the SHA-256 of the pattern over the ranges in order.
func ledgerExpectedSum(ranges spans) []byte {
	h := sha256.New()
	buf := make([]byte, proofMaxFrame)
	for _, s := range ranges {
		for off := s.lo; off < s.hi; {
			n := int(min(uint64(len(buf)), s.hi-off))
			ledgerFill(buf[:n], off)
			h.Write(buf[:n])
			off += uint64(n)
		}
	}
	return h.Sum(nil)
}
