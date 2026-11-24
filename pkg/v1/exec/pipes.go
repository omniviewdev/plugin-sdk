package exec

import (
	"context"
	"io"
	"sync"
	"time"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// This file holds the bounded pipes between the host and an author's function: the input the function reads as
// Proc.Stdin, the output of a run, which it writes as Proc.Stdout and Proc.Stderr, and the size channel it reads as
// Proc.Resize. They are what makes the engine's memory and blocking rules true, so each one states its bound:
//
//   - the input queue holds at most stdioQueueBytes (256 KiB). A host that sends more waits in Write until the function
//     reads, which is the backpressure the host sees;
//   - the output queue of a run (outputPipe) holds at most stdioQueueBytes (256 KiB) and at most maxOutputSegments (4,096)
//     runs of one stream. For a target that blocks (StallPropagates, or nothing declared) a full queue blocks the function's
//     Write; for a target that stalls lossily (StallLossy) it overwrites the oldest bytes and counts them, and never blocks
//     for longer than it takes to take a lock. Either way an ending run never waits for a reader: its pipe overwrites the
//     oldest bytes instead of blocking. The output of a session is not a queue but the replay ring (ring.go), which holds
//     the bytes after they are read, so that a host that leaves can ask for them again;
//   - the size channel holds one size in flight and one pending: the latest wins.
//
// Every pipe is safe for use by many goroutines and allocates when it grows (the rings double up to their bound) and
// when it reports a dropped range, and not otherwise.

const (
	// stdioQueueBytes is the bound of the input queue and of the output queue of a run, and the most output that a session
	// whose reader may not lose a byte leaves unread before its function's Write waits.
	stdioQueueBytes = 256 << 10
	// maxOutputSegments is the most runs of one stream the output queue holds at once. A terminal has one stream and so
	// one run; a command without a terminal that alternates stdout and stderr in tiny writes makes a run for each
	// alternation, and this bound keeps that metadata as bounded as the bytes.
	maxOutputSegments = 4096
	// maxPendingDrops is the most dropped runs the output queue remembers for its reader. A drop that would make one more
	// is merged into the last (see outputPipe.recordDrop).
	maxPendingDrops = 64
	// initialRingBytes is the allocation of a ring that holds its first byte; it doubles from there up to its bound.
	initialRingBytes = 4 << 10
)

// byteRing is a FIFO of bytes in one allocation that doubles, up to a fixed bound, as it fills. It is not safe for
// concurrent use: the pipes hold their lock around it.
type byteRing struct {
	buf  []byte
	head int // index of the first byte
	n    int // bytes held
	max  int // the most bytes it ever holds
}

// free returns how many bytes can still be written before the bound.
func (r *byteRing) free() int { return r.max - r.n }

// reserve makes the allocation hold at least need bytes (need must not exceed max), moving the content to the start
// of a new allocation when it has to grow.
func (r *byteRing) reserve(need int) {
	if need <= len(r.buf) {
		return
	}
	size := max(2*len(r.buf), initialRingBytes)
	for size < need {
		size *= 2
	}
	size = min(size, r.max)
	next := make([]byte, size)
	r.copyOut(next, r.n)
	r.buf, r.head = next, 0
}

// copyOut copies the first n bytes into dst, which must hold them, and does not consume them.
func (r *byteRing) copyOut(dst []byte, n int) {
	if n == 0 {
		return
	}
	end := r.head + n
	if end <= len(r.buf) {
		copy(dst, r.buf[r.head:end])
		return
	}
	first := copy(dst, r.buf[r.head:])
	copy(dst[first:n], r.buf[:end-len(r.buf)])
}

// write appends as much of p as fits before the bound and returns how much it took.
func (r *byteRing) write(p []byte) int {
	n := min(len(p), r.free())
	if n == 0 {
		return 0
	}
	r.reserve(r.n + n)
	tail := r.head + r.n
	if tail >= len(r.buf) {
		tail -= len(r.buf)
	}
	first := copy(r.buf[tail:], p[:n])
	copy(r.buf, p[first:n])
	r.n += n
	return n
}

// read moves up to len(dst) bytes into dst and returns how many.
func (r *byteRing) read(dst []byte) int {
	n := min(len(dst), r.n)
	r.copyOut(dst, n)
	r.discard(n)
	return n
}

// discard consumes n bytes (n must not exceed the bytes held).
func (r *byteRing) discard(n int) {
	r.n -= n
	if r.n == 0 {
		r.head = 0
		return
	}
	r.head += n
	if r.head >= len(r.buf) {
		r.head -= len(r.buf)
	}
}

// segment is a run of bytes of one stream in the output queue.
type segment struct {
	stream execv2.Stream
	n      int
}

// segQueue is the FIFO of the runs of the output queue. Neighbours of one stream are one run.
type segQueue struct {
	s    []segment
	head int
}

func (q *segQueue) len() int { return len(q.s) - q.head }

// extends reports whether n more bytes of stream would join the last run instead of starting one.
func (q *segQueue) extends(stream execv2.Stream) bool {
	return q.len() > 0 && q.s[len(q.s)-1].stream == stream
}

// push appends n bytes of stream.
func (q *segQueue) push(stream execv2.Stream, n int) {
	if q.extends(stream) {
		q.s[len(q.s)-1].n += n
		return
	}
	if q.head > 0 && len(q.s) == cap(q.s) {
		// Reuse the consumed front instead of growing: the live runs are bounded, the slice must be too.
		live := copy(q.s, q.s[q.head:])
		q.s, q.head = q.s[:live], 0
	}
	q.s = append(q.s, segment{stream: stream, n: n})
}

// front returns the oldest run. The queue must not be empty.
func (q *segQueue) front() *segment { return &q.s[q.head] }

// pop removes the oldest run.
func (q *segQueue) pop() {
	q.head++
	if q.head == len(q.s) {
		q.s, q.head = q.s[:0], 0
	}
}

// dropRun is a range of output the session lost: [From, From+Bytes) of one stream.
type dropRun struct {
	Stream execv2.Stream
	From   uint64
	Bytes  uint64
}

// outputChunk is what the reader of an output queue gets from one Read: the ranges lost since its last Read, oldest
// first, all before Offset, and then up to len(buf) bytes of ONE stream at Offset. N is zero when only a dropped range
// is reported.
type outputChunk struct {
	Stream  execv2.Stream
	Offset  uint64
	N       int
	Dropped []dropRun
}

// pipeStats are the counts of an output queue.
type pipeStats struct {
	Written         uint64 // bytes the function wrote and the queue accepted, dropped ones included: the session's output offset
	Dropped         uint64 // of those, bytes overwritten before any reader took them
	DroppedDetached uint64 // of those, bytes that were overwritten while no stream was attached (a ring only)
	Queued          uint64 // bytes waiting for the reader
	Stalls          uint64 // writes that had to wait for the reader
	Late            uint64 // writes refused because the queue was closed
	LateB           uint64 // bytes in those writes
}

// outputPipe carries what the function writes to Proc.Stdout and Proc.Stderr to the reader, in the order it was written,
// with the byte offset of each run in the session's one output sequence (stdout and stderr share it, in arrival
// order, as the wire does).
//
// A full queue (stdioQueueBytes, or maxOutputSegments runs) has two policies. Blocking holds the writer until the reader
// makes room, and loses nothing: it is the policy of a target whose stalled reader slows the process (StallPropagates).
// Lossy never blocks: it overwrites the oldest queued bytes, in whole runs or part of one, counts them, and tells the
// reader which ranges it lost (dropRun) before the bytes that follow, so that nothing is silently missing: it is the
// policy of a target that a stalled reader would harm (StallLossy), and the policy any pipe takes when its session
// ends (beginEnding), so that an ending session never waits for a reader.
type outputPipe struct {
	mu      sync.Mutex
	cond    *sync.Cond
	waiters int // goroutines in cond.Wait, so that nobody calls Broadcast for nobody

	ring   byteRing
	segs   segQueue
	lossy  bool
	closed bool // the writer side is closed: the reader drains and then gets io.EOF

	readOff  uint64 // offset of the first queued byte; readOff + ring.n == writeOff
	writeOff uint64 // offset after the last accepted byte
	dropped  uint64
	drops    []dropRun // ranges lost that the reader has not been told about, oldest first
	stalls   uint64
	late     uint64
	lateB    uint64
}

// newOutputPipe returns an output queue of the given bound. lossy selects the full-queue policy.
func newOutputPipe(capacity int, lossy bool) *outputPipe {
	p := &outputPipe{lossy: lossy, ring: byteRing{max: capacity}}
	p.cond = sync.NewCond(&p.mu)
	return p
}

// wait parks the caller until the next wake. The caller holds p.mu and rechecks its condition afterwards.
func (p *outputPipe) wait() {
	p.waiters++
	p.cond.Wait()
	p.waiters--
}

// wake releases every parked caller, if there is one.
func (p *outputPipe) wake() {
	if p.waiters > 0 {
		p.cond.Broadcast()
	}
}

// Write queues b as output of stream and returns how many bytes it took. It takes all of b, or fails: with a blocking
// pipe it waits for room, with a lossy one it overwrites old bytes instead, and a pipe that is closed refuses b with
// io.ErrClosedPipe (and counts it as a late write). A partial count comes with an error only when the pipe closed under a
// write that had already placed part of b. A zero-length write queues nothing.
func (p *outputPipe) Write(stream execv2.Stream, b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		p.refuse(len(b))
		return 0, io.ErrClosedPipe
	}
	written, stalled := 0, false
	for len(b) > 0 {
		if p.closed {
			p.refuse(len(b))
			return written, io.ErrClosedPipe
		}
		if p.lossy {
			p.writeLossy(stream, b)
			written, b = written+len(b), nil
			break
		}
		room := p.ring.free()
		if room == 0 || (p.segs.len() >= maxOutputSegments && !p.segs.extends(stream)) {
			if !stalled {
				p.stalls++
				stalled = true
			}
			p.wait()
			continue
		}
		n := min(room, len(b))
		p.enqueue(stream, b[:n])
		written, b = written+n, b[n:]
	}
	return written, nil
}

// refuse counts a write that arrived after the pipe closed.
func (p *outputPipe) refuse(n int) {
	p.late++
	p.lateB += uint64(n)
}

// enqueue appends b, which fits, and wakes the reader.
func (p *outputPipe) enqueue(stream execv2.Stream, b []byte) {
	p.ring.write(b)
	p.segs.push(stream, len(b))
	p.writeOff += uint64(len(b))
	p.wake()
}

// writeLossy queues all of b, overwriting the oldest queued bytes as far as that takes. A b larger than the whole
// queue keeps only its last bytes: everything before them, queued or not, is older and is dropped.
func (p *outputPipe) writeLossy(stream execv2.Stream, b []byte) {
	if len(b) > p.ring.max {
		for p.ring.n > 0 {
			p.dropFront(p.segs.front().n)
		}
		skip := len(b) - p.ring.max
		p.recordDrop(stream, p.writeOff, uint64(skip))
		p.writeOff += uint64(skip)
		p.readOff = p.writeOff
		p.dropped += uint64(skip)
		b = b[skip:]
	}
	for p.ring.n > 0 {
		free := p.ring.free()
		switch {
		case free < len(b):
			p.dropFront(min(p.segs.front().n, len(b)-free))
		case p.segs.len() >= maxOutputSegments && !p.segs.extends(stream):
			p.dropFront(p.segs.front().n)
		default:
			p.enqueue(stream, b)
			return
		}
	}
	p.enqueue(stream, b)
}

// dropFront overwrites the oldest k bytes, which lie in the front run, and records the range.
func (p *outputPipe) dropFront(k int) {
	front := p.segs.front()
	p.recordDrop(front.stream, p.readOff, uint64(k))
	p.ring.discard(k)
	front.n -= k
	if front.n == 0 {
		p.segs.pop()
	}
	p.readOff += uint64(k)
	p.dropped += uint64(k)
}

// recordDrop remembers a lost range for the reader. A range that continues the last remembered one of the same stream
// extends it. When maxPendingDrops are remembered already, the range is merged into the last one whatever its stream:
// the byte counts stay exact and only the stream label of a few runs of an alternating flood the reader never read
// is that of the run it was merged into.
func (p *outputPipe) recordDrop(stream execv2.Stream, from, n uint64) {
	if last := len(p.drops) - 1; last >= 0 && (p.drops[last].Stream == stream || len(p.drops) >= maxPendingDrops) {
		p.drops[last].Bytes += n
		return
	}
	p.drops = append(p.drops, dropRun{Stream: stream, From: from, Bytes: n})
}

// Read waits until the queue holds something for the reader, and returns, in one outputChunk, the ranges that were
// lost since the last Read and up to len(buf) bytes of the oldest run (one stream). It returns io.EOF once the pipe is
// closed and everything it held was read, and ctx's error when ctx ends first. An empty buf is io.ErrShortBuffer. One
// reader at a time reads in order; the pipe does not stop a second one from taking the next chunk.
func (p *outputPipe) Read(ctx context.Context, buf []byte) (outputChunk, error) {
	if len(buf) == 0 {
		return outputChunk{}, io.ErrShortBuffer
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ring.n == 0 && len(p.drops) == 0 && !p.closed {
		if err := ctx.Err(); err != nil {
			return outputChunk{}, err
		}
		stop := context.AfterFunc(ctx, func() {
			p.mu.Lock()
			p.cond.Broadcast()
			p.mu.Unlock()
		})
		defer stop()
		for p.ring.n == 0 && len(p.drops) == 0 && !p.closed {
			if err := ctx.Err(); err != nil {
				return outputChunk{}, err
			}
			p.wait()
		}
	}
	chunk := outputChunk{Dropped: p.drops, Offset: p.readOff}
	p.drops = nil
	if p.ring.n == 0 {
		if len(chunk.Dropped) == 0 {
			return chunk, io.EOF
		}
		return chunk, nil
	}
	front := p.segs.front()
	n := min(len(buf), front.n)
	p.ring.read(buf[:n])
	chunk.Stream, chunk.N = front.stream, n
	front.n -= n
	if front.n == 0 {
		p.segs.pop()
	}
	p.readOff += uint64(n)
	p.wake()
	return chunk, nil
}

// makeLossy switches a blocking pipe to the lossy policy and releases the writers it holds. A session that is ending gets it
// (beginEnding), and so does a function that narrows its session to StallLossy: a reader that does not read must not hold
// a function in a Write that the target said a stalled reader would harm.
func (p *outputPipe) makeLossy() {
	p.mu.Lock()
	p.lossy = true
	p.cond.Broadcast()
	p.mu.Unlock()
}

// beginEnding is makeLossy for a session that is ending: the end never waits for a reader, so a function blocked in a Write
// on a full queue is released and can return.
func (p *outputPipe) beginEnding() { p.makeLossy() }

// CloseWrite closes the writer side: a later Write fails with io.ErrClosedPipe, a Write that is waiting fails with it,
// and the reader drains what is queued and then gets io.EOF. It may be called again.
func (p *outputPipe) CloseWrite() {
	p.mu.Lock()
	p.closed = true
	p.cond.Broadcast()
	p.mu.Unlock()
}

// Stats returns the counts of the pipe now.
func (p *outputPipe) Stats() pipeStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return pipeStats{
		Written: p.writeOff, Dropped: p.dropped, Queued: uint64(p.ring.n),
		Stalls: p.stalls, Late: p.late, LateB: p.lateB,
	}
}

// inputPipe carries what the host sends to the function's Proc.Stdin. The host's side blocks when the queue is full
// (stdioQueueBytes), which is the backpressure the host sees, and the function's side blocks when it is empty. What the host sent is
// never discarded by the SDK, the end of the session included: the function reads every byte that was queued, and io.EOF after it.
type inputPipe struct {
	mu      sync.Mutex
	cond    *sync.Cond
	waiters int

	ring     byteRing
	eof      bool // the host closed the input: the reader drains what is queued and then gets io.EOF
	closed   bool // the session ended: no byte is taken any more, and the reader drains what is queued and then gets io.EOF
	accepted uint64
}

// newInputPipe returns an input queue of the given bound.
func newInputPipe(capacity int) *inputPipe {
	p := &inputPipe{ring: byteRing{max: capacity}}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *inputPipe) wait() {
	p.waiters++
	p.cond.Wait()
	p.waiters--
}

func (p *inputPipe) wake() {
	if p.waiters > 0 {
		p.cond.Broadcast()
	}
}

// Read is the function's Proc.Stdin. It waits for input, and returns io.EOF when the host closed the input or the session ended
// and everything the host sent was read.
func (p *inputPipe) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for {
		switch {
		case p.ring.n > 0:
			n := p.ring.read(b)
			p.wake()
			return n, nil
		case p.closed, p.eof:
			return 0, io.EOF
		}
		p.wait()
	}
}

// Write queues b for the function, waiting while the queue is full, and returns how many bytes it took. It fails with
// io.ErrClosedPipe once the host closed the input or the session ended, and with ctx's error when ctx ends first; the
// count then says how much was queued.
func (p *inputPipe) Write(ctx context.Context, b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.eof || p.closed {
		return 0, io.ErrClosedPipe
	}
	var stop func() bool
	defer func() {
		if stop != nil {
			stop()
		}
	}()
	written := 0
	for len(b) > 0 {
		if p.eof || p.closed {
			return written, io.ErrClosedPipe
		}
		if err := ctx.Err(); err != nil {
			return written, err
		}
		n := p.ring.write(b)
		if n == 0 {
			if stop == nil {
				stop = context.AfterFunc(ctx, func() {
					p.mu.Lock()
					p.cond.Broadcast()
					p.mu.Unlock()
				})
			}
			p.wait()
			continue
		}
		p.accepted += uint64(n)
		written, b = written+n, b[n:]
		p.wake()
	}
	return written, nil
}

// CloseWrite is the host's end of the input: stdin EOF once what was sent is read. It may be called again.
func (p *inputPipe) CloseWrite() {
	p.mu.Lock()
	p.eof = true
	p.cond.Broadcast()
	p.mu.Unlock()
}

// Close ends the pipe with the session: every writer fails and a reader that waits for input gets io.EOF. What was queued is kept, and
// the reader reads it before io.EOF: the bytes the host sent before the end are accepted, and are never discarded by the SDK. The queue
// is as bounded at the end as it was before it (stdioQueueBytes). It may be called again.
func (p *inputPipe) Close() {
	p.mu.Lock()
	p.closed = true
	p.cond.Broadcast()
	p.mu.Unlock()
}

// Accepted returns how many bytes the host has sent that the pipe took.
func (p *inputPipe) Accepted() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.accepted
}

// resizeRequest is a size on its way to the function.
type resizeRequest struct {
	seq     uint32
	size    Size
	initial bool        // the size the command was opened with, not a host's resize
	owner   resizeOwner // who sent it: the stream of the host that is told when the function takes it; nil for none
}

// resizeOwner is the stream of a host that sent a size, which acknowledges it. Only the owner of a size is told what became of it: a
// stream that attaches later never hears of the sizes of the one before it, and learns the size in force from what Opened says of
// the replay.
type resizeOwner interface {
	// resizeAccepted is called as the size is accepted and before the function can take it, so that what the owner records for it
	// is there when it is taken. It runs under the resizer's lock: it must not block or call the resizer.
	resizeAccepted(seq uint32, size Size)
	// resizeTaken is called when the function took the size, after the command has recorded it. It must not block.
	resizeTaken(seq uint32, size Size)
}

// resizer delivers terminal sizes to Proc.Resize: the initial size first, and then latest wins, and it never blocks the
// SDK. A function reads the channel at its own pace: the resizer holds the size it is trying to deliver and at most one
// newer, so a storm of resizes costs at most two deliveries, the last of them the newest size (and the initial size
// before them when the function has not read it yet: a resize that arrives before the first read does not replace it, the
// first value of Proc.Resize is Request.Size). It tells the engine when the function took a size that a host sent, and
// once when a size has waited unreadAfter for a function that does not read the channel.
//
// run is the one goroutine of the resizer. It closes the channel as it ends, which is what "closed when the session ends" means to
// the function, and it ends in one of two ways. The end of the session (end) stops it taking sizes from the host and lets it go on until
// the sizes it holds were taken: a size the host sent before the end is never discarded by the SDK, and a goroutine of the function that
// ranges over the channel finds it and then returns with the rest. The end of the function (stop: it returned, or the SDK gave up on it
// after its grace) ends it at once, with what it held: nobody is waited for.
type resizer struct {
	mu      sync.Mutex
	initial Size
	pending resizeRequest
	has     bool
	ending  bool // the session ended: no size is taken from the host, and run ends once it holds none
	stopped bool
	wakeup  chan struct{} // capacity 1: a size is pending, or the session ended
	quit    chan struct{}
	out     chan Size

	applied     func(seq uint32, size Size)
	unreadAfter time.Duration
	unread      func()
}

// newResizer returns a resizer whose first delivery is initial, when it is not zero. applied and unread may be nil.
func newResizer(initial Size, applied func(uint32, Size), unreadAfter time.Duration, unread func()) *resizer {
	return &resizer{
		initial: initial, wakeup: make(chan struct{}, 1), quit: make(chan struct{}), out: make(chan Size),
		applied: applied, unreadAfter: unreadAfter, unread: unread,
	}
}

// channel returns the channel the function reads.
func (r *resizer) channel() <-chan Size { return r.out }

// submit makes size the one to deliver next, replacing a size that has not been taken, and tells owner (when there is one) that it
// was accepted, before the function can take it. It never blocks, and reports false once the session has ended or the resizer has
// stopped: a size that comes after the end is refused, and nobody is told it was accepted.
func (r *resizer) submit(seq uint32, size Size, owner resizeOwner) bool {
	r.mu.Lock()
	if r.stopped || r.ending {
		r.mu.Unlock()
		return false
	}
	if owner != nil {
		owner.resizeAccepted(seq, size)
	}
	r.pending, r.has = resizeRequest{seq: seq, size: size, owner: owner}, true
	r.mu.Unlock()
	r.wake()
	return true
}

// wake tells run that a size is pending or that the session ended. A wake that finds one waiting adds nothing.
func (r *resizer) wake() {
	select {
	case r.wakeup <- struct{}{}:
	default:
	}
}

// stop ends run at once: the pending size is dropped and the channel closes. It may be called again.
func (r *resizer) stop() {
	r.mu.Lock()
	if !r.stopped {
		r.stopped, r.has = true, false
		close(r.quit)
	}
	r.mu.Unlock()
}

// end is the end of the session: submit refuses every size from now on, and run closes the channel once the sizes it holds (the size
// the command was opened with, if the function has not read it, the one in flight, and the newest the host sent) were taken, or when
// stop says the function is gone. It never blocks, and it may be called again.
func (r *resizer) end() {
	r.mu.Lock()
	r.ending = true
	r.mu.Unlock()
	r.wake()
}

// take returns the pending size and clears it, and reports done when the session ended and no size is left to hand over.
func (r *resizer) take() (req resizeRequest, ok, done bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.has {
		r.has = false
		return r.pending, true, false
	}
	return resizeRequest{}, false, r.ending
}

// run delivers the initial size and then the sizes of the host, until the end of the session has left it nothing to deliver, or stop.
func (r *resizer) run() {
	defer close(r.out)
	if !r.initial.IsZero() && !r.deliver(resizeRequest{size: r.initial, initial: true}) {
		return
	}
	for {
		req, ok, done := r.take()
		if done {
			return
		}
		if !ok {
			select {
			case <-r.wakeup:
				continue
			case <-r.quit:
				return
			}
		}
		if !r.deliver(req) {
			return
		}
	}
}

// deliver hands one size to the function and reports whether the resizer goes on.
func (r *resizer) deliver(req resizeRequest) bool {
	var late <-chan time.Time
	if r.unreadAfter > 0 && r.unread != nil {
		timer := time.NewTimer(r.unreadAfter)
		defer timer.Stop()
		late = timer.C
	}
	for {
		select {
		case r.out <- req.size:
			if !req.initial {
				if r.applied != nil {
					r.applied(req.seq, req.size)
				}
				if req.owner != nil {
					req.owner.resizeTaken(req.seq, req.size)
				}
			}
			return true
		case <-late:
			r.unread()
			late = nil
		case <-r.quit:
			return false
		}
	}
}

// isClosed reports whether the writer side is closed.
func (p *outputPipe) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}
