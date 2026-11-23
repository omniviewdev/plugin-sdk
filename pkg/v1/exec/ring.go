package exec

import (
	"context"
	"io"
	"math/bits"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The output of an interactive session is one structure, the replay ring: what the function writes goes in at the tail, in
// whole chunks, and every reader takes from a cursor of its own. The queue that carries output to the host and the buffer
// that lets a host that reloads its window ask for it again are the same bytes, so a byte is copied into the session once.
//
//   - Offsets are absolute: the first byte of a session is offset 0, the ring never renumbers, and a chunk's place in the ring
//     is its offset divided by the chunk size. The ring holds the newest whole chunks, at most Session.ReplayBytes (or the
//     Start's replay_bytes) of them: it trims at its front a chunk at a time, so its front is always a chunk boundary, and it
//     allocates a chunk when it first needs one (a session that writes a few bytes holds a few KiB) and reuses the oldest one
//     for the newest after that, so a ring that has filled allocates nothing more.
//   - A chunk is 64 KiB (the largest Output frame). A ring too small to hold four of them uses the largest power of two, down
//     to 256 bytes, that lets it.
//   - Output of a command that runs without a terminal comes from two streams, and a chunk may hold both: the chunk says which
//     bytes are the second stream with one bit for each byte, allocated only for a chunk that mixes them, so a terminal's ring
//     and a command that writes one stream carry nothing for it. A frame is always one run of one stream.
//   - What a full ring does to the writer is decided by who reads. A reader that is attached to a target that stalls (a
//     blocking session) holds the writer when the bytes it has not read reach stdioQueueBytes, and loses nothing: the ring
//     trims only chunks the reader has read. A reader that is attached to a target that must not be stalled (lossy), a
//     session that is ending, and a session with no reader (detached) never hold the writer: the ring trims its oldest chunk
//     and counts the bytes that no reader had taken. The reader of a lossy stream is told the range it lost (Dropped); a
//     host that reattaches reads the gap from Replay.start.
//   - The ring keeps the size log of the terminal: every size the function took, with the offset it took it at, so that
//     Replay.start_size is the size the bytes at the start of a replay were produced for.
//
// One mutex guards the ring and the attachment of the session (replay.go), so that a snapshot of the ring and the registration
// of the stream that replays it are one step.
const (
	// ringChunkBytes is the chunk of a ring that can hold four of them.
	ringChunkBytes = maxOutputFrame
	// ringMinChunks is how many chunks the smallest ring holds: its replay is never less than three quarters of its size.
	ringMinChunks = 4
	// ringMinChunkBytes is the smallest chunk, for a ring declared at its smallest size.
	ringMinChunkBytes = 256
	// ringInitialChunkBytes is what a chunk allocates for its first byte; it doubles, up to the chunk size, as it fills.
	ringInitialChunkBytes = 4 << 10
	// maxSizeLog is the most sizes the ring remembers. A function that takes more sizes than this between the front of the
	// ring and its tail loses the oldest, and a replay that starts before the oldest it remembers says its start size is not
	// known.
	maxSizeLog = 256
)

// ringGeometry is the chunk size and the chunk count of a ring declared to hold capacity bytes. The ring never holds more than
// capacity.
func ringGeometry(capacity int) (chunkBytes, chunks int) {
	chunkBytes = ringChunkBytes
	for chunkBytes > ringMinChunkBytes && capacity/chunkBytes < ringMinChunks {
		chunkBytes >>= 1
	}
	return chunkBytes, max(capacity/chunkBytes, ringMinChunks)
}

// ringChunk is one chunk of the ring, or a copy of the part of one that a replay takes.
type ringChunk struct {
	buf    []byte // the bytes written; capacity grows by doubling up to the chunk size
	n      int    // bytes in use: len(buf) of a copy
	lo     int    // the index of buf[0] in the chunk it was copied from, which is where its bits are counted from; 0 in the ring
	stream execv2.Stream
	// mixed says that bytes of a second stream, alt, are in the chunk: bits holds one bit for each byte of the chunk, set for alt.
	mixed bool
	alt   execv2.Stream
	bits  []uint64
}

// streamAt is the stream of the byte at index i of buf.
func (c *ringChunk) streamAt(i int) execv2.Stream {
	if c.mixed && c.bit(c.lo+i) {
		return c.alt
	}
	return c.stream
}

func (c *ringChunk) bit(i int) bool { return c.bits[i>>6]>>(i&63)&1 == 1 }

// setBits sets the bits of the bytes [from, to) of the chunk, which must be allocated, to v.
func (c *ringChunk) setBits(from, to int, v bool) {
	for from < to {
		w, off := from>>6, from&63
		n := min(to-from, 64-off)
		mask := (^uint64(0) >> (64 - n)) << off
		if v {
			c.bits[w] |= mask
		} else {
			c.bits[w] &^= mask
		}
		from += n
	}
}

// run returns how many bytes from buf[i], at most limit, are of the stream of buf[i], and that stream. It does not look past
// the chunk.
func (c *ringChunk) run(i, limit int) (int, execv2.Stream) {
	limit = min(limit, c.n-i)
	if !c.mixed {
		return limit, c.stream
	}
	first := c.bit(c.lo + i)
	stream := c.stream
	if first {
		stream = c.alt
	}
	end, last := i+1, i+limit
	for end < last {
		pos := c.lo + end
		w, off := pos>>6, pos&63
		// x has a zero at each byte, from end on, that is of the stream of buf[i]: the run ends at the first one. The bits that
		// the shift brings in are zeros, and a run is never longer than the rest of the word.
		x := c.bits[w] >> off
		if first {
			x = ^x
		}
		run := min(bits.TrailingZeros64(x), 64-off)
		end += run
		if run < 64-off {
			break
		}
	}
	return min(end, last) - i, stream
}

// ringSize is a terminal size the function took and the offset of the output when it did.
type ringSize struct {
	off  uint64
	size Size
}

// ringConfig is what a ring is made with.
type ringConfig struct {
	// capacity is the most bytes the ring holds for replay.
	capacity int
	// lossy makes the ring never hold its writer: a full ring drops its oldest chunk even if its reader has not read it.
	lossy bool
	// unreadLimit is how many bytes a blocking ring lets its attached reader leave unread before it holds the writer.
	unreadLimit int
	// initialSize is the size the terminal was opened with; zero for a command without a terminal.
	initialSize Size
	// now is the clock of the activity time; nil is the wall clock.
	now func() time.Time
}

// ring is the output of one session. See the comment at the top of this file.
type ring struct {
	mu      sync.Mutex
	cond    *sync.Cond
	waiters int // goroutines in cond.Wait, so that nobody calls Broadcast for nobody

	chunkBytes  uint64
	maxChunks   uint64
	unreadLimit uint64
	slots       []ringChunk
	firstQ      uint64 // the number of the oldest chunk the ring holds: its first byte is at offset firstQ * chunkBytes
	endOff      uint64 // the offset after the last accepted byte
	lossy       bool   // a full ring drops its oldest chunk whoever has not read it
	closed      bool   // the writer side is closed: readers drain and then get io.EOF

	// delivered is the offset up to which the bytes have been taken by a reader (or handed to one in a replay): what lies
	// below it and leaves the ring is not lost, what lies above it and leaves the ring is counted as dropped.
	delivered       uint64
	dropped         uint64 // bytes that left the ring before any reader took them
	droppedDetached uint64 // of those, bytes that left while no stream was attached
	stalls          uint64 // writes that had to wait for the reader
	late            uint64 // writes refused because the writer side was closed
	lateB           uint64

	sizes     []ringSize
	sizesFrom uint64 // the size is known from here on: before it the log lost its entries

	now          func() time.Time
	lastActivity atomic.Int64 // unix milliseconds of the last output or input; 0 before any

	att attachments // who reads and the life of the session around it: replay.go
}

// newRing returns the ring of a session.
func newRing(cfg ringConfig) *ring {
	chunkBytes, chunks := ringGeometry(cfg.capacity)
	r := &ring{
		chunkBytes: uint64(chunkBytes), maxChunks: uint64(chunks),
		unreadLimit: uint64(min(cfg.unreadLimit, (chunks-1)*chunkBytes)),
		slots:       make([]ringChunk, chunks),
		lossy:       cfg.lossy,
		now:         cfg.now,
	}
	if r.now == nil {
		r.now = time.Now
	}
	r.cond = sync.NewCond(&r.mu)
	if !cfg.initialSize.IsZero() {
		r.sizes = append(r.sizes, ringSize{off: 0, size: cfg.initialSize})
	}
	return r
}

func (r *ring) slot(q uint64) *ringChunk { return &r.slots[q%r.maxChunks] }

// start is the offset of the oldest byte the ring holds. The caller holds mu.
func (r *ring) start() uint64 { return r.firstQ * r.chunkBytes }

// wait parks the caller until the next wake. The caller holds mu and rechecks its condition afterwards.
func (r *ring) wait() {
	r.waiters++
	r.cond.Wait()
	r.waiters--
}

// wake releases every parked caller, if there is one.
func (r *ring) wake() {
	if r.waiters > 0 {
		r.cond.Broadcast()
	}
}

// mayDrop says whether a full ring may lose bytes that no reader has taken: it may when it has no reader, when its target
// must not be stalled, and when the session is ending. The caller holds mu.
func (r *ring) mayDrop() bool { return r.att.cur == nil || r.lossy }

// Write queues b as output of stream and returns how many bytes it took. It takes all of b, or fails: a ring whose reader may
// not lose bytes waits for room, any other drops its oldest chunk instead, and a ring that is closed refuses b with
// io.ErrClosedPipe (and counts it as a late write). A partial count comes with an error only when the ring closed under a write
// that had already placed part of b. A zero-length write queues nothing.
func (r *ring) Write(stream execv2.Stream, b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		r.refuse(len(b))
		return 0, io.ErrClosedPipe
	}
	if len(b) > 0 {
		r.lastActivity.Store(r.now().UnixMilli())
	}
	written, stalled := 0, false
	for len(b) > 0 {
		if r.closed {
			r.refuse(len(b))
			return written, io.ErrClosedPipe
		}
		limit := uint64(len(b))
		if !r.mayDrop() {
			// A reader that may not lose a byte is attached: the writer is held when it has left too much unread.
			unread := r.endOff - r.att.cur.cursor
			if unread >= r.unreadLimit {
				if !stalled {
					r.stalls++
					stalled = true
				}
				r.wait()
				continue
			}
			limit = min(limit, r.unreadLimit-unread)
		}
		idx := r.endOff % r.chunkBytes
		if idx == 0 {
			if r.endOff/r.chunkBytes-r.firstQ >= r.maxChunks && !r.trimOldest() {
				if !stalled {
					r.stalls++
					stalled = true
				}
				r.wait()
				continue
			}
			r.startChunk(r.endOff / r.chunkBytes)
		}
		n := int(min(limit, r.chunkBytes-idx))
		r.place(r.slot(r.endOff/r.chunkBytes), int(idx), stream, b[:n])
		r.endOff += uint64(n)
		written, b = written+n, b[n:]
		r.wake()
	}
	return written, nil
}

// refuse counts a write that arrived after the ring closed.
func (r *ring) refuse(n int) {
	r.late++
	r.lateB += uint64(n)
}

// startChunk makes the slot of chunk q empty for the bytes that follow, allocating it on first use.
func (r *ring) startChunk(q uint64) {
	c := r.slot(q)
	if c.buf == nil {
		c.buf = make([]byte, min(ringInitialChunkBytes, int(r.chunkBytes)))
	}
	c.n, c.mixed = 0, false
}

// place copies b into chunk c at index idx, which is its end.
func (r *ring) place(c *ringChunk, idx int, stream execv2.Stream, b []byte) {
	if need := idx + len(b); need > len(c.buf) {
		grown := make([]byte, min(max(2*len(c.buf), need), int(r.chunkBytes)))
		copy(grown, c.buf[:c.n])
		c.buf = grown
	}
	copy(c.buf[idx:], b)
	switch {
	case idx == 0:
		c.stream = stream
	case stream != c.stream && !c.mixed:
		c.mixed, c.alt = true, stream
		if c.bits == nil {
			c.bits = make([]uint64, (int(r.chunkBytes)+63)/64)
		}
		c.setBits(0, idx, false)
		c.setBits(idx, idx+len(b), true)
	case c.mixed:
		// Only two streams write one session, and a chunk that has met both names the second: a third would be taken for it.
		c.setBits(idx, idx+len(b), stream != c.stream)
	}
	c.n = idx + len(b)
}

// trimOldest drops the oldest chunk, which the ring needs for a new one. It reports false when the ring must not lose what
// the chunk holds: a reader that may not lose a byte has not read all of it.
func (r *ring) trimOldest() bool {
	base := r.firstQ * r.chunkBytes
	end := base + r.chunkBytes
	if r.delivered < end {
		if !r.mayDrop() {
			return false
		}
		from := max(base, r.delivered)
		lost := end - from
		r.dropped += lost
		if a := r.att.cur; a != nil {
			a.lose(from, lost, r.slot(r.firstQ).streamAt(int(from-base)))
		} else {
			r.droppedDetached += lost
		}
	}
	r.firstQ++
	r.compactSizes()
	return true
}

// compactSizes forgets the sizes that no byte of the ring can have been produced for: what is in force at the front of the ring
// is kept, and what came before it is not.
func (r *ring) compactSizes() {
	start := r.start()
	keep := -1
	for i, s := range r.sizes {
		if s.off > start {
			break
		}
		keep = i
	}
	if keep > 0 {
		r.sizes = slices.Delete(r.sizes, 0, keep)
	}
}

// noteSize records that the function took size, at the offset the output has reached. The function takes a size from
// Proc.Resize, and the bytes it writes after that are laid out for it.
func (r *ring) noteSize(size Size) {
	if size.IsZero() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if n := len(r.sizes); n > 0 {
		last := &r.sizes[n-1]
		switch {
		case last.size == size:
			return
		case last.off == r.endOff:
			last.size = size
			return
		}
	}
	r.sizes = append(r.sizes, ringSize{off: r.endOff, size: size})
	if len(r.sizes) > maxSizeLog {
		r.sizes = slices.Delete(r.sizes, 0, 1)
		r.sizesFrom = r.sizes[0].off
	}
}

// sizeAt is the size the byte at offset was produced for, and false when it is not known: the session has no terminal, or the
// log lost the entry in force there. The caller holds mu.
func (r *ring) sizeAt(offset uint64) (Size, bool) {
	if offset < r.sizesFrom {
		return Size{}, false
	}
	found := false
	var size Size
	for _, s := range r.sizes {
		if s.off > offset {
			break
		}
		size, found = s.size, true
	}
	return size, found
}

// currentSize is the size now, and false when there is none. The caller holds mu.
func (r *ring) currentSize() (Size, bool) {
	if len(r.sizes) == 0 {
		return Size{}, false
	}
	return r.sizes[len(r.sizes)-1].size, true
}

// readLocked copies up to len(buf) bytes of ONE stream, from offset off, which must be in [start, endOff), and returns how many
// and which stream. The caller holds mu.
func (r *ring) readLocked(off uint64, buf []byte) (int, execv2.Stream) {
	q := off / r.chunkBytes
	c := r.slot(q)
	i := int(off - q*r.chunkBytes)
	first, stream := c.run(i, len(buf))
	copy(buf, c.buf[i:i+first])
	n := first
	// A frame goes on into the next chunk when this one is full to its end and the next begins with the same stream.
	for n < len(buf) && i+first == c.n && uint64(c.n) == r.chunkBytes && (q+1)*r.chunkBytes < r.endOff {
		q++
		c = r.slot(q)
		if c.stream != stream {
			break
		}
		run, _ := c.run(0, len(buf)-n)
		copy(buf[n:], c.buf[:run])
		n += run
		i, first = 0, run
	}
	return n, stream
}

// makeLossy switches the ring to the policy that never holds its writer, and releases the writer it holds. A session that is
// ending gets it, and so does a function that narrows its session to a lossy stall: a reader that does not read must not hold a
// function in a Write that the target said a stalled reader would harm.
func (r *ring) makeLossy() {
	r.mu.Lock()
	r.lossy = true
	r.cond.Broadcast()
	r.mu.Unlock()
}

// beginEnding is makeLossy for a session that is ending: the end never waits for a reader.
func (r *ring) beginEnding() { r.makeLossy() }

// CloseWrite closes the writer side: a later Write fails with io.ErrClosedPipe, a Write that is waiting fails with it, and the
// readers drain what the ring holds and then get io.EOF. It may be called again.
func (r *ring) CloseWrite() {
	r.mu.Lock()
	r.closed = true
	r.cond.Broadcast()
	r.mu.Unlock()
}

// isClosed reports whether the writer side is closed.
func (r *ring) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// Stats returns the counts of the ring now. Queued is what no reader has taken.
func (r *ring) Stats() pipeStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return pipeStats{
		Written: r.endOff, Dropped: r.dropped, DroppedDetached: r.droppedDetached, Queued: r.endOff - min(r.delivered, r.endOff),
		Stalls: r.stalls, Late: r.late, LateB: r.lateB,
	}
}

// window reports the ring's front and tail.
func (r *ring) window() (start, end uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.start(), r.endOff
}

// capacity is the most bytes the ring holds.
func (r *ring) capacity() int { return int(r.chunkBytes * r.maxChunks) }

// ringStatus is what ListSessions says of a live session's ring, read in one step.
type ringStatus struct {
	start, end uint64
	attached   bool      // a stream is attached
	lease      time.Time // when a detached session ends; zero while it is attached
}

// status reads the ring's window, whether a stream is attached and when the lease ends, in one step.
func (r *ring) status() ringStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return ringStatus{start: r.start(), end: r.endOff, attached: r.att.cur != nil && !r.att.ended, lease: r.att.leaseExpires}
}

// ---------------------------------------------------------------------------------------------------------------
// the copy of a replay
// ---------------------------------------------------------------------------------------------------------------

// snapshot is a copy of the bytes a replay sends, taken under the ring's lock: the bytes [first, end) with the stream of each. It
// is the replay's own, so what the ring does while the host takes it (a lossy ring overwrites the oldest bytes, a detached one
// too) cannot take a byte out of a replay that Opened has described.
type snapshot struct {
	chunks []ringChunk
	idx    int // the chunk being read
	pos    int // the next byte of it
	next   uint64
}

// snapshotLocked copies the ring from offset from, which must be in [start, endOff]. The caller holds mu.
func (r *ring) snapshotLocked(from uint64) *snapshot {
	s := &snapshot{next: from}
	if from >= r.endOff {
		return s
	}
	data := make([]byte, r.endOff-from)
	s.chunks = make([]ringChunk, 0, (r.endOff-1)/r.chunkBytes-from/r.chunkBytes+1)
	at := 0
	for q := from / r.chunkBytes; q*r.chunkBytes < r.endOff; q++ {
		src := r.slot(q)
		lo := 0
		if q == from/r.chunkBytes {
			lo = int(from - q*r.chunkBytes)
		}
		n := src.n - lo
		c := ringChunk{buf: data[at : at+n : at+n], n: n, lo: lo, stream: src.stream, mixed: src.mixed, alt: src.alt}
		copy(c.buf, src.buf[lo:src.n])
		if src.mixed {
			c.bits = slices.Clone(src.bits)
		}
		s.chunks = append(s.chunks, c)
		at += n
	}
	return s
}

// read copies up to len(buf) bytes of one stream of what is left of the replay, and returns how many, the stream and the offset of
// the first. It returns zero when the replay has been read.
func (s *snapshot) read(buf []byte) (n int, stream execv2.Stream, offset uint64) {
	offset = s.next
	for s.idx < len(s.chunks) {
		c := &s.chunks[s.idx]
		if s.pos >= c.n {
			s.idx, s.pos = s.idx+1, 0
			continue
		}
		run, runStream := c.run(s.pos, len(buf)-n)
		if n == 0 {
			stream = runStream
		} else if runStream != stream {
			break
		}
		copy(buf[n:], c.buf[s.pos:s.pos+run])
		n += run
		s.pos += run
		s.next += uint64(run)
		if s.pos < c.n || n == len(buf) {
			break // the run ended inside the chunk (another stream follows), or the buffer is full
		}
	}
	return n, stream, offset
}

// ---------------------------------------------------------------------------------------------------------------
// a read of the ring that is not a stream's
// ---------------------------------------------------------------------------------------------------------------

// ringRead is one run of one stream that a cursor read returns.
type ringRead struct {
	Stream execv2.Stream
	Offset uint64
	Data   []byte
}

// readAt reads the ring for ReadOutput: runs contiguous from max(from, start), at most maxBytes bytes in at most maxRuns runs. It
// returns the start of the ring and the offset the next read starts at. A read changes nothing a stream sees. The runs share one
// allocation.
func (r *ring) readAt(from uint64, maxBytes, maxRuns int) (runs []ringRead, start, next uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	start = r.start()
	next = max(from, start)
	if next < r.endOff && maxBytes > 0 {
		data := make([]byte, min(uint64(maxBytes), r.endOff-next))
		for pos := 0; len(runs) < maxRuns && pos < len(data); {
			n, stream := r.readLocked(next, data[pos:])
			if n == 0 {
				break // a ring that holds no byte where it says it does: never loop on it
			}
			runs = append(runs, ringRead{Stream: stream, Offset: next, Data: data[pos : pos+n : pos+n]})
			pos += n
			next += uint64(n)
		}
	}
	return runs, start, min(next, r.endOff)
}

// ---------------------------------------------------------------------------------------------------------------
// what a cursor read of the stream needs from a context
// ---------------------------------------------------------------------------------------------------------------

// watch registers the wake of the ring's waiters for the end of ctx, and returns the call that undoes it. A reader registers once for
// its stream, not for each wait, because a registration allocates and a reader that has caught up waits again and again.
func (r *ring) watch(ctx context.Context) (stop func() bool) {
	return context.AfterFunc(ctx, func() {
		r.mu.Lock()
		r.cond.Broadcast()
		r.mu.Unlock()
	})
}
