package exec

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/producers"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

const (
	streamOut = execv2.Stream_STREAM_STDOUT
	streamErr = execv2.Stream_STREAM_STDERR
	streamTTY = execv2.Stream_STREAM_TTY
)

// mustHappen bounds the waits for something that has to happen: it turns a hang into a failure and decides nothing.
const mustHappen = 10 * time.Second

// eventually polls cond until it holds. The condition is the oracle; the wait only keeps a bug from hanging the suite.
func eventually(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(mustHappen)
	for spins := 0; !cond(); spins++ {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		if spins < 100 {
			runtime.Gosched()
		} else {
			time.Sleep(time.Millisecond)
		}
	}
}

// awaitClosed waits for c to be closed.
func awaitClosed(t testing.TB, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(mustHappen):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// assertOpen asserts that c is not closed now. It is a state read, not a wait: callers use it after the state that
// makes c's closing impossible has been observed.
func assertOpen(t testing.TB, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-c:
		t.Fatalf("%s", what)
	default:
	}
}

// waiting returns how many goroutines are parked in the pipe.
func (p *outputPipe) waiting() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waiters
}

func (p *inputPipe) waiting() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waiters
}

// started runs fn in a goroutine and returns a channel that is closed when it returns.
func started(fn func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	return done
}

func randomBytes(rng *rand.Rand, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rng.IntN(256))
	}
	return b
}

// readAll reads an output pipe to the end and returns what it held: every chunk in order and the ranges it lost.
type readRecord struct {
	chunks  []outputChunk
	data    [][]byte
	dropped []dropRun
}

func readAll(t testing.TB, p *outputPipe, bufSize int) readRecord {
	t.Helper()
	var rec readRecord
	buf := make([]byte, bufSize)
	for {
		chunk, err := p.Read(context.Background(), buf)
		rec.dropped = append(rec.dropped, chunk.Dropped...)
		if chunk.N > 0 {
			rec.chunks = append(rec.chunks, chunk)
			rec.data = append(rec.data, bytes.Clone(buf[:chunk.N]))
		}
		if errors.Is(err, io.EOF) {
			return rec
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
	}
}

func (r readRecord) bytes() []byte {
	var all []byte
	for _, d := range r.data {
		all = append(all, d...)
	}
	return all
}

// ---------------------------------------------------------------------------------------------------------------
// byteRing
// ---------------------------------------------------------------------------------------------------------------

// A ring that wraps, grows while it holds data and is read in odd sizes is a FIFO of bytes: it is held against a
// plain slice as the model over a long random run of writes and reads.
func TestAByteRingIsAFIFOOfBytesAcrossWrapsAndGrowth(t *testing.T) {
	for seed := uint64(1); seed <= 20; seed++ {
		rng := rand.New(rand.NewPCG(seed, seed*7919))
		limit := 1 + rng.IntN(3*initialRingBytes)
		ring := byteRing{max: limit}
		var model []byte
		var next byte
		for range 4000 {
			if rng.IntN(2) == 0 {
				p := make([]byte, rng.IntN(1+limit/2))
				for i := range p {
					next++
					p[i] = next
				}
				n := ring.write(p)
				if want := min(len(p), limit-len(model)); n != want {
					t.Fatalf("seed %d: write of %d took %d, want %d (limit %d, held %d)", seed, len(p), n, want, limit, len(model))
				}
				model = append(model, p[:n]...)
				// bytes that were not taken must not have been consumed from the sequence
				next -= byte(len(p) - n)
			} else {
				dst := make([]byte, rng.IntN(1+limit/2))
				n := ring.read(dst)
				if want := min(len(dst), len(model)); n != want {
					t.Fatalf("seed %d: read took %d, want %d", seed, n, want)
				}
				if !bytes.Equal(dst[:n], model[:n]) {
					t.Fatalf("seed %d: read returned other bytes than were written", seed)
				}
				model = model[n:]
			}
			if ring.n != len(model) || ring.free() != limit-len(model) {
				t.Fatalf("seed %d: ring holds %d (free %d), model %d", seed, ring.n, ring.free(), len(model))
			}
			if len(ring.buf) > limit {
				t.Fatalf("seed %d: allocation %d exceeds the bound %d", seed, len(ring.buf), limit)
			}
		}
	}
}

// ---------------------------------------------------------------------------------------------------------------
// outputPipe
// ---------------------------------------------------------------------------------------------------------------

func TestAnOutputPipeKeepsTheOrderTheOffsetsAndTheStreamsOfWhatWasWritten(t *testing.T) {
	p := newOutputPipe(1024, false)
	for _, w := range []struct {
		s execv2.Stream
		d string
	}{{streamOut, "aa"}, {streamOut, "a"}, {streamErr, "bb"}, {streamOut, "cc"}, {streamErr, ""}, {streamErr, "d"}} {
		if n, err := p.Write(w.s, []byte(w.d)); n != len(w.d) || err != nil {
			t.Fatalf("write %q: %d, %v", w.d, n, err)
		}
	}
	p.CloseWrite()
	rec := readAll(t, p, 64)
	want := []struct {
		s   execv2.Stream
		off uint64
		d   string
	}{{streamOut, 0, "aaa"}, {streamErr, 3, "bb"}, {streamOut, 5, "cc"}, {streamErr, 7, "d"}}
	if len(rec.chunks) != len(want) {
		t.Fatalf("read %d chunks, want %d: %+v", len(rec.chunks), len(want), rec.chunks)
	}
	for i, w := range want {
		if c := rec.chunks[i]; c.Stream != w.s || c.Offset != w.off || string(rec.data[i]) != w.d {
			t.Errorf("chunk %d: stream %v offset %d %q, want %v %d %q", i, c.Stream, c.Offset, rec.data[i], w.s, w.off, w.d)
		}
	}
	if len(rec.dropped) != 0 {
		t.Errorf("a blocking pipe reported dropped ranges: %v", rec.dropped)
	}
}

func TestAnOutputPipeSplitsARunToTheReadersBufferAndKeepsTheOffsetsContiguous(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	data := randomBytes(rng, 1000)
	p := newOutputPipe(4096, false)
	if n, err := p.Write(streamTTY, data); n != len(data) || err != nil {
		t.Fatalf("write: %d, %v", n, err)
	}
	p.CloseWrite()
	rec := readAll(t, p, 7)
	var offset uint64
	for i, c := range rec.chunks {
		if c.Offset != offset || c.Stream != streamTTY || c.N > 7 {
			t.Fatalf("chunk %d at offset %d stream %v n %d, want offset %d, at most 7 bytes", i, c.Offset, c.Stream, c.N, offset)
		}
		offset += uint64(c.N)
	}
	if !bytes.Equal(rec.bytes(), data) {
		t.Fatal("the bytes read are not the bytes written")
	}
}

// With a blocking pipe a writer that fills the queue waits for the reader, and nothing is lost: the proof is the state
// of the pipe while the writer waits (full, one parked goroutine) and the bytes at the end.
func TestABlockingOutputPipeHoldsTheWriterAtItsBoundAndLosesNothing(t *testing.T) {
	const bound = 1024
	rng := rand.New(rand.NewPCG(5, 6))
	data := randomBytes(rng, 4*bound+100)
	p := newOutputPipe(bound, false)
	var wrote int
	var werr error
	done := started(func() { wrote, werr = p.Write(streamTTY, data) })

	eventually(t, "the writer to wait on a full queue", func() bool { return p.waiting() == 1 })
	if got := p.Stats(); got.Queued != bound || got.Written != bound || got.Stalls != 1 {
		t.Fatalf("a waiting writer: %+v, want %d bytes queued and accepted and one stall", got, bound)
	}
	assertOpen(t, done, "Write returned with the queue full and nobody reading")

	var got []byte
	buf := make([]byte, 300)
	for len(got) < len(data) {
		chunk, err := p.Read(context.Background(), buf)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		got = append(got, buf[:chunk.N]...)
	}
	awaitClosed(t, done, "the writer to finish once the reader made room")
	if wrote != len(data) || werr != nil {
		t.Fatalf("Write returned %d, %v, want %d, nil", wrote, werr, len(data))
	}
	if !bytes.Equal(got, data) {
		t.Fatal("the reader got other bytes than the writer wrote")
	}
	if s := p.Stats(); s.Dropped != 0 || s.Written != uint64(len(data)) || s.Stalls < 1 {
		t.Fatalf("stats %+v: a blocking pipe drops nothing, and its writer stalled", s)
	}
}

// A writer that waits is released by the close of the pipe with what it had placed counted, and the reader still gets
// what was placed.
func TestAClosedOutputPipeFailsAWaitingWriterAndKeepsWhatItPlaced(t *testing.T) {
	p := newOutputPipe(100, false)
	data := bytes.Repeat([]byte("x"), 250)
	var wrote int
	var werr error
	done := started(func() { wrote, werr = p.Write(streamTTY, data) })
	eventually(t, "the writer to wait", func() bool { return p.waiting() == 1 })

	p.CloseWrite()
	awaitClosed(t, done, "the writer to return after the close")
	if wrote != 100 || !errors.Is(werr, io.ErrClosedPipe) {
		t.Fatalf("Write returned %d, %v, want 100, io.ErrClosedPipe", wrote, werr)
	}
	if s := p.Stats(); s.Late != 1 || s.LateB != 150 {
		t.Fatalf("stats %+v: the 150 bytes that arrived after the close are one late write", s)
	}
	rec := readAll(t, p, 1000)
	if len(rec.bytes()) != 100 {
		t.Fatalf("the reader got %d bytes, want the 100 that were placed", len(rec.bytes()))
	}
}

func TestAnOutputPipeRefusesAndCountsWritesAfterItClosed(t *testing.T) {
	p := newOutputPipe(100, false)
	p.CloseWrite()
	p.CloseWrite() // again: harmless
	for i, w := range [][]byte{[]byte("abc"), []byte("de")} {
		if n, err := p.Write(streamOut, w); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("write %d after the close: %d, %v", i, n, err)
		}
	}
	if s := p.Stats(); s.Late != 2 || s.LateB != 5 || s.Written != 0 {
		t.Fatalf("stats %+v, want two late writes of 5 bytes and nothing accepted", s)
	}
}

func TestALossyOutputPipeNeverBlocksAndCountsWhatItOverwrites(t *testing.T) {
	const bound = 100
	rng := rand.New(rand.NewPCG(7, 8))
	data := randomBytes(rng, 300)
	p := newOutputPipe(bound, true)
	for i := 0; i < len(data); i += 30 {
		if n, err := p.Write(streamTTY, data[i:i+30]); n != 30 || err != nil {
			t.Fatalf("write at %d: %d, %v", i, n, err)
		}
	}
	if p.waiting() != 0 {
		t.Fatal("a lossy pipe parked a writer")
	}
	s := p.Stats()
	if s.Written != 300 || s.Dropped != 200 || s.Queued != 100 || s.Stalls != 0 {
		t.Fatalf("stats %+v, want 300 written, 200 dropped, 100 queued and no stall", s)
	}
	p.CloseWrite()
	rec := readAll(t, p, 1000)
	if len(rec.dropped) != 1 || rec.dropped[0] != (dropRun{Stream: streamTTY, From: 0, Bytes: 200}) {
		t.Fatalf("dropped ranges %v, want [0,200) of the terminal stream", rec.dropped)
	}
	if len(rec.chunks) != 1 || rec.chunks[0].Offset != 200 || !bytes.Equal(rec.bytes(), data[200:]) {
		t.Fatalf("what is left is not the last 100 bytes at offset 200: %+v", rec.chunks)
	}
}

// The end-state accounting of a lossy pipe: what the reader took plus what was dropped is what was written, for every
// size of write, including one larger than the whole queue, and every byte the reader takes is the byte that was
// written at its offset.
func TestALossyOutputPipeAccountsForEveryByteWhateverTheWriteSizes(t *testing.T) {
	for seed := uint64(1); seed <= 30; seed++ {
		rng := rand.New(rand.NewPCG(seed, 99))
		bound := 1 + rng.IntN(200)
		p := newOutputPipe(bound, true)
		var written []byte
		var taken, dropped, next uint64
		buf := make([]byte, 1+rng.IntN(64))
		// consume checks one Read against the bytes written so far: the reader moves forward without a hole that was not
		// reported, and what it took is what was written at its offset.
		consume := func(chunk outputChunk) {
			t.Helper()
			for _, d := range chunk.Dropped {
				if d.From != next {
					t.Fatalf("seed %d: dropped range at %d, the reader is at %d: a hole that was not reported", seed, d.From, next)
				}
				next += d.Bytes
				dropped += d.Bytes
			}
			if chunk.N == 0 {
				return
			}
			if chunk.Offset != next {
				t.Fatalf("seed %d: chunk at %d, the reader is at %d", seed, chunk.Offset, next)
			}
			if !bytes.Equal(buf[:chunk.N], written[chunk.Offset:chunk.Offset+uint64(chunk.N)]) {
				t.Fatalf("seed %d: the bytes at offset %d are not what was written there", seed, chunk.Offset)
			}
			next += uint64(chunk.N)
			taken += uint64(chunk.N)
		}
		for range 300 {
			stream := []execv2.Stream{streamOut, streamErr}[rng.IntN(2)]
			w := randomBytes(rng, rng.IntN(3*bound+1))
			if n, err := p.Write(stream, w); n != len(w) || err != nil {
				t.Fatalf("seed %d: write: %d, %v", seed, n, err)
			}
			written = append(written, w...)
			if rng.IntN(3) == 0 && p.Stats().Queued > 0 { // a Read of an empty queue waits for output: only read what is there
				chunk, err := p.Read(context.Background(), buf)
				if err != nil {
					t.Fatalf("seed %d: read: %v", seed, err)
				}
				consume(chunk)
			}
		}
		p.CloseWrite()
		for {
			chunk, err := p.Read(context.Background(), buf)
			consume(chunk)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("seed %d: read: %v", seed, err)
			}
		}
		s := p.Stats()
		if uint64(len(written)) != s.Written || s.Dropped != dropped || taken+dropped != s.Written || next != s.Written {
			t.Fatalf("seed %d: written %d (pipe says %d), read %d, dropped %d (pipe says %d), the reader ended at %d: read + dropped must be written",
				seed, len(written), s.Written, taken, dropped, s.Dropped, next)
		}
	}
}

func TestALossyOutputPipeWithAWriteLargerThanTheQueueKeepsOnlyItsLastBytes(t *testing.T) {
	rng := rand.New(rand.NewPCG(9, 10))
	data := randomBytes(rng, 250)
	p := newOutputPipe(100, true)
	if n, err := p.Write(streamErr, []byte("old")); n != 3 || err != nil {
		t.Fatalf("write: %d, %v", n, err)
	}
	if n, err := p.Write(streamOut, data); n != 250 || err != nil {
		t.Fatalf("write: %d, %v", n, err)
	}
	p.CloseWrite()
	rec := readAll(t, p, 1000)
	want := []dropRun{{Stream: streamErr, From: 0, Bytes: 3}, {Stream: streamOut, From: 3, Bytes: 150}}
	if len(rec.dropped) != 2 || rec.dropped[0] != want[0] || rec.dropped[1] != want[1] {
		t.Fatalf("dropped %v, want %v", rec.dropped, want)
	}
	if len(rec.chunks) != 1 || rec.chunks[0].Offset != 153 || rec.chunks[0].Stream != streamOut || !bytes.Equal(rec.bytes(), data[150:]) {
		t.Fatalf("what is left: %+v", rec.chunks)
	}
}

// A dropped range names the stream the bytes were of, also when it overwrites part of a run.
func TestALossyOutputPipeNamesTheStreamOfWhatItOverwrote(t *testing.T) {
	p := newOutputPipe(100, true)
	p.Write(streamOut, bytes.Repeat([]byte("o"), 60)) //nolint:errcheck // the pipe never fails an open write
	p.Write(streamErr, bytes.Repeat([]byte("e"), 60)) //nolint:errcheck // idem
	p.CloseWrite()
	rec := readAll(t, p, 1000)
	if len(rec.dropped) != 1 || rec.dropped[0] != (dropRun{Stream: streamOut, From: 0, Bytes: 20}) {
		t.Fatalf("dropped %v, want the first 20 bytes of stdout", rec.dropped)
	}
	if len(rec.chunks) != 2 ||
		rec.chunks[0].Stream != streamOut || rec.chunks[0].Offset != 20 || rec.chunks[0].N != 40 ||
		rec.chunks[1].Stream != streamErr || rec.chunks[1].Offset != 60 || rec.chunks[1].N != 60 {
		t.Fatalf("chunks %+v, want 40 bytes of stdout at 20 and 60 bytes of stderr at 60", rec.chunks)
	}
}

// The runs the reader has not been told about are bounded: a flood that alternates streams in tiny writes while nobody
// reads merges them, with the byte counts exact.
func TestALossyOutputPipeBoundsTheDroppedRangesItRemembers(t *testing.T) {
	p := newOutputPipe(8, true)
	total := 40 * maxPendingDrops
	for i := range total {
		stream := streamOut
		if i%2 == 1 {
			stream = streamErr
		}
		if n, err := p.Write(stream, []byte{byte(i)}); n != 1 || err != nil {
			t.Fatalf("write %d: %d, %v", i, n, err)
		}
	}
	p.mu.Lock()
	remembered := len(p.drops)
	p.mu.Unlock()
	if remembered > maxPendingDrops {
		t.Fatalf("the pipe remembers %d dropped ranges, at most %d", remembered, maxPendingDrops)
	}
	p.CloseWrite()
	rec := readAll(t, p, 100)
	var sum uint64
	next := uint64(0)
	for _, d := range rec.dropped {
		if d.From != next {
			t.Fatalf("dropped ranges are not contiguous: %v", rec.dropped)
		}
		next += d.Bytes
		sum += d.Bytes
	}
	if s := p.Stats(); sum != s.Dropped || s.Dropped != uint64(total-8) {
		t.Fatalf("the ranges sum to %d, the pipe dropped %d, want %d", sum, s.Dropped, total-8)
	}
}

// The runs of one stream are bounded too, not only the bytes: a blocking pipe parks the writer at maxOutputSegments
// alternations whatever the byte count, and a lossy one drops its oldest run.
func TestAnOutputPipeBoundsTheRunsOfAlternatingStreamsAsItBoundsTheBytes(t *testing.T) {
	t.Run("blocking", func(t *testing.T) {
		p := newOutputPipe(stdioQueueBytes, false)
		for i := range maxOutputSegments {
			stream := streamOut
			if i%2 == 1 {
				stream = streamErr
			}
			if n, err := p.Write(stream, []byte{1}); n != 1 || err != nil {
				t.Fatalf("write %d: %d, %v", i, n, err)
			}
		}
		// One more run of the same stream as the last extends it and needs no new run.
		last := streamOut
		if maxOutputSegments%2 == 0 {
			last = streamErr
		}
		if n, err := p.Write(last, []byte{1}); n != 1 || err != nil {
			t.Fatalf("a write that extends the last run: %d, %v", n, err)
		}
		other := streamOut
		if last == streamOut {
			other = streamErr
		}
		done := started(func() { p.Write(other, []byte{2}) }) //nolint:errcheck // the pipe closes below
		eventually(t, "the writer that needs one run too many to wait", func() bool { return p.waiting() == 1 })
		assertOpen(t, done, "a write that needed one run too many did not wait")
		buf := make([]byte, 1)
		if _, err := p.Read(context.Background(), buf); err != nil {
			t.Fatalf("read: %v", err)
		}
		awaitClosed(t, done, "the writer to finish when a run was freed")
		if s := p.Stats(); s.Dropped != 0 {
			t.Fatalf("a blocking pipe dropped %d bytes", s.Dropped)
		}
	})
	t.Run("lossy", func(t *testing.T) {
		p := newOutputPipe(stdioQueueBytes, true)
		extra := 10
		for i := range maxOutputSegments + extra {
			stream := streamOut
			if i%2 == 1 {
				stream = streamErr
			}
			p.Write(stream, []byte{1}) //nolint:errcheck // the pipe never fails an open write
		}
		p.mu.Lock()
		runs := p.segs.len()
		p.mu.Unlock()
		if runs > maxOutputSegments {
			t.Fatalf("the pipe holds %d runs, at most %d", runs, maxOutputSegments)
		}
		if s := p.Stats(); s.Dropped != uint64(extra) || s.Queued != uint64(maxOutputSegments) {
			t.Fatalf("stats %+v, want the %d oldest runs dropped", s, extra)
		}
	})
}

// An ending session never waits for a reader: a blocking pipe that begins to end releases the writer it holds and
// drops its oldest bytes for the rest of the writes.
func TestAnOutputPipeThatBeginsToEndReleasesItsWriterAndDropsInsteadOfBlocking(t *testing.T) {
	p := newOutputPipe(100, false)
	p.Write(streamTTY, bytes.Repeat([]byte("a"), 100)) //nolint:errcheck // fits
	done := started(func() { p.Write(streamTTY, bytes.Repeat([]byte("b"), 50)) })
	eventually(t, "the writer to wait", func() bool { return p.waiting() == 1 })

	p.beginEnding()
	awaitClosed(t, done, "the writer to be released")
	if n, err := p.Write(streamTTY, bytes.Repeat([]byte("c"), 30)); n != 30 || err != nil {
		t.Fatalf("a write after the pipe began to end: %d, %v", n, err)
	}
	if p.waiting() != 0 {
		t.Fatal("a writer waits on a pipe that is ending")
	}
	s := p.Stats()
	if s.Written != 180 || s.Dropped != 80 || s.Queued != 100 {
		t.Fatalf("stats %+v, want 180 written, 80 dropped, 100 queued", s)
	}
	p.CloseWrite()
	rec := readAll(t, p, 1000)
	if got, want := rec.bytes(), append(bytes.Repeat([]byte("a"), 20), append(bytes.Repeat([]byte("b"), 50), bytes.Repeat([]byte("c"), 30)...)...); !bytes.Equal(got, want) {
		t.Fatalf("the queue holds %q, want the newest 100 bytes", got)
	}
}

func TestAnOutputPipeReaderWaitsForOutputAndEndsWithTheContextOrTheClose(t *testing.T) {
	t.Run("data wakes it", func(t *testing.T) {
		p := newOutputPipe(100, false)
		var got outputChunk
		buf := make([]byte, 10)
		done := started(func() { got, _ = p.Read(context.Background(), buf) })
		eventually(t, "the reader to wait", func() bool { return p.waiting() == 1 })
		p.Write(streamTTY, []byte("hi")) //nolint:errcheck // fits
		awaitClosed(t, done, "the reader to wake")
		if got.N != 2 || string(buf[:2]) != "hi" {
			t.Fatalf("read %+v %q", got, buf[:got.N])
		}
	})
	t.Run("the context ends it", func(t *testing.T) {
		p := newOutputPipe(100, false)
		ctx, cancel := context.WithCancel(context.Background())
		var err error
		done := started(func() { _, err = p.Read(ctx, make([]byte, 10)) })
		eventually(t, "the reader to wait", func() bool { return p.waiting() == 1 })
		cancel()
		awaitClosed(t, done, "the reader to return")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("read returned %v, want context.Canceled", err)
		}
		eventually(t, "the reader to leave the pipe", func() bool { return p.waiting() == 0 })
	})
	t.Run("an ended context returns at once", func(t *testing.T) {
		p := newOutputPipe(100, false)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := p.Read(ctx, make([]byte, 10)); !errors.Is(err, context.Canceled) {
			t.Fatalf("read returned %v, want context.Canceled", err)
		}
		// Output that is queued is still read with a context that has ended: the context bounds the wait, not the data.
		p.Write(streamTTY, []byte("x")) //nolint:errcheck // fits
		if chunk, err := p.Read(ctx, make([]byte, 10)); err != nil || chunk.N != 1 {
			t.Fatalf("read of queued output: %+v, %v", chunk, err)
		}
	})
	t.Run("the close ends it", func(t *testing.T) {
		p := newOutputPipe(100, false)
		var err error
		done := started(func() { _, err = p.Read(context.Background(), make([]byte, 10)) })
		eventually(t, "the reader to wait", func() bool { return p.waiting() == 1 })
		p.CloseWrite()
		awaitClosed(t, done, "the reader to return")
		if !errors.Is(err, io.EOF) {
			t.Fatalf("read returned %v, want io.EOF", err)
		}
	})
	t.Run("an empty buffer is refused", func(t *testing.T) {
		p := newOutputPipe(100, false)
		if _, err := p.Read(context.Background(), nil); !errors.Is(err, io.ErrShortBuffer) {
			t.Fatalf("read into nothing: %v", err)
		}
	})
}

func TestAClosedOutputPipeReportsTheRangesItDroppedBeforeItsEnd(t *testing.T) {
	p := newOutputPipe(10, true)
	p.Write(streamTTY, bytes.Repeat([]byte("z"), 25)) //nolint:errcheck // lossy
	p.CloseWrite()
	buf := make([]byte, 100)
	first, err := p.Read(context.Background(), buf)
	if err != nil || first.N != 10 || len(first.Dropped) != 1 || first.Dropped[0].Bytes != 15 {
		t.Fatalf("first read: %+v, %v", first, err)
	}
	if _, err := p.Read(context.Background(), buf); !errors.Is(err, io.EOF) {
		t.Fatalf("second read: %v, want io.EOF", err)
	}
}

func TestAZeroLengthWriteQueuesNothingAndDoesNotWait(t *testing.T) {
	p := newOutputPipe(10, false)
	p.Write(streamTTY, bytes.Repeat([]byte("a"), 10)) //nolint:errcheck // fits
	if n, err := p.Write(streamTTY, nil); n != 0 || err != nil {
		t.Fatalf("a zero-length write to a full pipe: %d, %v", n, err)
	}
	if p.waiting() != 0 || p.Stats().Written != 10 {
		t.Fatal("a zero-length write waited or was counted")
	}
}

// Two writers (a command's stdout and stderr copy from separate goroutines) and one reader, a queue far smaller than
// the data: every byte arrives once, in the order its writer wrote it, at contiguous offsets, and the pipe holds its
// bound throughout. The race detector watches the locking.
func TestAnOutputPipeCarriesTwoConcurrentWritersAndOneReaderWithoutLoss(t *testing.T) {
	const perWriter = 200 << 10
	p := newOutputPipe(1000, false)
	rng := rand.New(rand.NewPCG(11, 12))
	streams := map[execv2.Stream][]byte{streamOut: randomBytes(rng, perWriter), streamErr: randomBytes(rng, perWriter)}
	var writers producers.Group
	for stream, data := range streams {
		writers.Start(1)
		go func() {
			defer writers.Done()
			wrng := rand.New(rand.NewPCG(uint64(stream), 5))
			for off := 0; off < len(data); {
				n := min(1+wrng.IntN(700), len(data)-off)
				if w, err := p.Write(stream, data[off:off+n]); w != n || err != nil {
					t.Errorf("write: %d, %v", w, err)
					return
				}
				off += n
			}
		}()
	}
	closed := started(func() { writers.Wait(); p.CloseWrite() })
	got := map[execv2.Stream][]byte{}
	var next uint64
	buf := make([]byte, 333)
	for {
		chunk, err := p.Read(context.Background(), buf)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if chunk.Offset != next {
			t.Fatalf("chunk at offset %d, want %d", chunk.Offset, next)
		}
		next += uint64(chunk.N)
		got[chunk.Stream] = append(got[chunk.Stream], buf[:chunk.N]...)
	}
	awaitClosed(t, closed, "the writers")
	for stream, want := range streams {
		if !bytes.Equal(got[stream], want) {
			t.Errorf("stream %v: %d bytes arrived, %d were written, or they differ", stream, len(got[stream]), len(want))
		}
	}
	if s := p.Stats(); s.Written != 2*perWriter || s.Dropped != 0 {
		t.Fatalf("stats %+v", s)
	}
}

func TestAnOutputPipeWriteAndReadDoNotAllocateInSteadyState(t *testing.T) {
	p := newOutputPipe(stdioQueueBytes, false)
	chunk := bytes.Repeat([]byte("x"), 4096)
	buf := make([]byte, 4096)
	ctx := context.Background()
	step := func() {
		p.Write(streamTTY, chunk) //nolint:errcheck // fits
		p.Read(ctx, buf)          //nolint:errcheck // data is queued
	}
	step() // warm up: the ring takes its allocation
	if allocs := testing.AllocsPerRun(200, step); allocs != 0 {
		t.Fatalf("a write and a read allocated %.1f times in steady state, want 0", allocs)
	}
}

// ---------------------------------------------------------------------------------------------------------------
// inputPipe
// ---------------------------------------------------------------------------------------------------------------

func TestAnInputPipeDeliversWhatTheHostSentInOrderThenEOFAfterItClosesTheInput(t *testing.T) {
	p := newInputPipe(1024)
	if n, err := p.Write(context.Background(), []byte("hello ")); n != 6 || err != nil {
		t.Fatalf("write: %d, %v", n, err)
	}
	if n, err := p.Write(context.Background(), []byte("world")); n != 5 || err != nil {
		t.Fatalf("write: %d, %v", n, err)
	}
	p.CloseWrite()
	p.CloseWrite() // again: harmless
	got, err := io.ReadAll(p)
	if err != nil || string(got) != "hello world" {
		t.Fatalf("read %q, %v", got, err)
	}
	if p.Accepted() != 11 {
		t.Fatalf("accepted %d", p.Accepted())
	}
	if n, err := p.Write(context.Background(), []byte("late")); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write after the host closed the input: %d, %v", n, err)
	}
}

func TestAnInputPipeReaderWaitsForInputAndIsEndedByTheClose(t *testing.T) {
	t.Run("input wakes it", func(t *testing.T) {
		p := newInputPipe(100)
		buf := make([]byte, 10)
		var n int
		done := started(func() { n, _ = p.Read(buf) })
		eventually(t, "the reader to wait", func() bool { return p.waiting() == 1 })
		p.Write(context.Background(), []byte("abc")) //nolint:errcheck // fits
		awaitClosed(t, done, "the reader to wake")
		if n != 3 || string(buf[:n]) != "abc" {
			t.Fatalf("read %d %q", n, buf[:n])
		}
	})
	t.Run("the end of the session gives it EOF at once and drops what was queued", func(t *testing.T) {
		p := newInputPipe(100)
		p.Write(context.Background(), []byte("unread")) //nolint:errcheck // fits
		p.Close()
		if n, err := p.Read(make([]byte, 10)); n != 0 || !errors.Is(err, io.EOF) {
			t.Fatalf("read after the end: %d, %v", n, err)
		}
		if n, err := p.Write(context.Background(), []byte("x")); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("write after the end: %d, %v", n, err)
		}
	})
	t.Run("the end of the session wakes a waiting reader", func(t *testing.T) {
		p := newInputPipe(100)
		var err error
		done := started(func() { _, err = p.Read(make([]byte, 10)) })
		eventually(t, "the reader to wait", func() bool { return p.waiting() == 1 })
		p.Close()
		awaitClosed(t, done, "the reader to return")
		if !errors.Is(err, io.EOF) {
			t.Fatalf("read returned %v, want io.EOF", err)
		}
	})
	t.Run("an empty read returns at once", func(t *testing.T) {
		p := newInputPipe(100)
		if n, err := p.Read(nil); n != 0 || err != nil {
			t.Fatalf("read into nothing: %d, %v", n, err)
		}
	})
}

// The host's Write waits at the bound: the backpressure the host sees. It is released by the reader, by the end of the
// session and by its own context, and says how much it placed.
func TestAnInputPipeHoldsTheHostsWriteAtItsBound(t *testing.T) {
	rng := rand.New(rand.NewPCG(13, 14))
	t.Run("the reader releases it", func(t *testing.T) {
		const bound = 1000
		data := randomBytes(rng, 5*bound+17)
		p := newInputPipe(bound)
		var wrote int
		var werr error
		done := started(func() { wrote, werr = p.Write(context.Background(), data) })
		eventually(t, "the host's write to wait", func() bool { return p.waiting() == 1 })
		if got := p.Accepted(); got != bound {
			t.Fatalf("accepted %d while the queue is full, want %d", got, bound)
		}
		assertOpen(t, done, "Write returned with the queue full and nobody reading")
		got := make([]byte, 0, len(data))
		buf := make([]byte, 257)
		for len(got) < len(data) {
			n, err := p.Read(buf)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			got = append(got, buf[:n]...)
		}
		awaitClosed(t, done, "the write to finish")
		if wrote != len(data) || werr != nil || !bytes.Equal(got, data) {
			t.Fatalf("write returned %d, %v; the bytes match: %v", wrote, werr, bytes.Equal(got, data))
		}
	})
	t.Run("the end of the session fails it with what it placed", func(t *testing.T) {
		p := newInputPipe(100)
		var wrote int
		var werr error
		done := started(func() { wrote, werr = p.Write(context.Background(), make([]byte, 250)) })
		eventually(t, "the host's write to wait", func() bool { return p.waiting() == 1 })
		p.Close()
		awaitClosed(t, done, "the write to return")
		if wrote != 100 || !errors.Is(werr, io.ErrClosedPipe) {
			t.Fatalf("write returned %d, %v, want 100, io.ErrClosedPipe", wrote, werr)
		}
	})
	t.Run("the host's context fails it with what it placed", func(t *testing.T) {
		p := newInputPipe(100)
		ctx, cancel := context.WithCancel(context.Background())
		var wrote int
		var werr error
		done := started(func() { wrote, werr = p.Write(ctx, make([]byte, 250)) })
		eventually(t, "the host's write to wait", func() bool { return p.waiting() == 1 })
		cancel()
		awaitClosed(t, done, "the write to return")
		if wrote != 100 || !errors.Is(werr, context.Canceled) {
			t.Fatalf("write returned %d, %v, want 100, context.Canceled", wrote, werr)
		}
		eventually(t, "the writer to leave the pipe", func() bool { return p.waiting() == 0 })
	})
	t.Run("closing the input fails it", func(t *testing.T) {
		p := newInputPipe(100)
		var werr error
		done := started(func() { _, werr = p.Write(context.Background(), make([]byte, 250)) })
		eventually(t, "the host's write to wait", func() bool { return p.waiting() == 1 })
		p.CloseWrite()
		awaitClosed(t, done, "the write to return")
		if !errors.Is(werr, io.ErrClosedPipe) {
			t.Fatalf("write returned %v, want io.ErrClosedPipe", werr)
		}
	})
}

// Many hosts' writers (several goroutines send input of one session) and one reader: nothing is lost or duplicated.
func TestAnInputPipeCarriesConcurrentWritersAndAReaderWithoutLoss(t *testing.T) {
	const writers, each = 4, 64 << 10
	p := newInputPipe(512)
	var sent atomic.Uint64
	var wg producers.Group
	for w := range writers {
		wg.Start(1)
		go func() {
			defer wg.Done()
			for range each / 100 {
				b := bytes.Repeat([]byte{byte('a' + w)}, 100)
				if n, err := p.Write(context.Background(), b); n != len(b) || err != nil {
					t.Errorf("write: %d, %v", n, err)
					return
				}
				sent.Add(100)
			}
		}()
	}
	done := started(func() { wg.Wait(); p.CloseWrite() })
	counts := map[byte]int{}
	buf := make([]byte, 77)
	for {
		n, err := p.Read(buf)
		for _, c := range buf[:n] {
			counts[c]++
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	awaitClosed(t, done, "the writers")
	for w := range writers {
		if got := counts[byte('a'+w)]; got != each/100*100 {
			t.Errorf("writer %d: %d bytes arrived, want %d", w, got, each/100*100)
		}
	}
}

// ---------------------------------------------------------------------------------------------------------------
// resizer
// ---------------------------------------------------------------------------------------------------------------

// startResizer runs the resizer's goroutine and returns a function that stops it and waits for it to end.
func startResizer(r *resizer) (stop func()) {
	done := started(r.run)
	return func() {
		r.stop()
		<-done
	}
}

func TestAResizerDeliversTheInitialSizeFirstAndCountsItNoHostResize(t *testing.T) {
	applied := make(chan resizeRequest, 4)
	r := newResizer(Size{Cols: 80, Rows: 24}, func(seq uint32, size Size) { applied <- resizeRequest{seq: seq, size: size} }, 0, nil)
	stop := startResizer(r)
	defer stop()
	if got := <-r.channel(); got != (Size{Cols: 80, Rows: 24}) {
		t.Fatalf("the first size is %v, want the initial 80 x 24", got)
	}
	r.submit(7, Size{Cols: 100, Rows: 30})
	if got := <-r.channel(); got != (Size{Cols: 100, Rows: 30}) {
		t.Fatalf("the next size is %v", got)
	}
	if got := <-applied; got.seq != 7 || got.size != (Size{Cols: 100, Rows: 30}) {
		t.Fatalf("applied %+v: the initial size is no host resize, and the host's is reported with its seq", got)
	}
	select {
	case extra := <-applied:
		t.Fatalf("a second size was reported applied: %+v", extra)
	default:
	}
}

func TestAResizerWithNoInitialSizeDeliversNothingUntilAHostResizes(t *testing.T) {
	r := newResizer(Size{}, nil, 0, nil)
	stop := startResizer(r)
	defer stop()
	select {
	case got := <-r.channel():
		t.Fatalf("a size %v arrived before any was sent", got)
	default:
	}
	r.submit(1, Size{Cols: 90, Rows: 20})
	if got := <-r.channel(); got != (Size{Cols: 90, Rows: 20}) {
		t.Fatalf("size %v", got)
	}
}

// A storm of resizes while the function does not read costs at most two deliveries, and the last is the newest.
func TestAResizerDeliversAtMostTwoSizesOfAStormAndTheLastIsTheNewest(t *testing.T) {
	const storm = 20000
	for range 20 {
		var appliedLast atomic.Uint32
		r := newResizer(Size{}, func(seq uint32, _ Size) { appliedLast.Store(seq) }, 0, nil)
		stop := startResizer(r)
		for seq := uint32(1); seq <= storm; seq++ {
			if !r.submit(seq, Size{Cols: uint16(seq), Rows: 24}) {
				t.Fatal("a resizer that is running refused a size")
			}
		}
		var received []Size
		for {
			select {
			case size := <-r.channel():
				received = append(received, size)
			case <-time.After(mustHappen):
				t.Fatal("timed out waiting for the newest size")
			}
			if received[len(received)-1] == (Size{Cols: uint16(storm), Rows: 24}) {
				break
			}
			if len(received) > 2 {
				t.Fatalf("%d sizes were delivered for one storm: %v", len(received), received)
			}
		}
		if len(received) > 2 {
			t.Fatalf("%d sizes were delivered for one storm", len(received))
		}
		stop()
		if got := appliedLast.Load(); got != storm {
			t.Fatalf("the last size reported applied has seq %d, want %d", got, storm)
		}
	}
}

// The SDK is never held by a function that does not read Proc.Resize.
func TestAResizerNeverBlocksTheSubmitterWhateverTheFunctionDoes(t *testing.T) {
	r := newResizer(Size{Cols: 80, Rows: 24}, nil, 0, nil)
	stop := startResizer(r)
	done := started(func() {
		for seq := uint32(1); seq <= 100000; seq++ {
			r.submit(seq, Size{Cols: 120, Rows: 40})
		}
	})
	awaitClosed(t, done, "100,000 submits with nobody reading")
	stop()
}

func TestAResizerClosesTheChannelWhenItStopsAndRefusesLaterSizes(t *testing.T) {
	r := newResizer(Size{Cols: 80, Rows: 24}, nil, 0, nil)
	stop := startResizer(r)
	stop()
	stop() // again: harmless
	for range r.channel() {
		// a size that was delivered before the stop may still be there to read; the loop must end because the channel closed
	}
	if r.submit(1, Size{Cols: 1, Rows: 1}) {
		t.Fatal("a stopped resizer took a size")
	}
}

// A function that never reads the channel is told once, after the wait, and is not told when it reads in time.
func TestAResizerReportsAFunctionThatDoesNotReadItsChannelOnce(t *testing.T) {
	t.Run("a function that does not read", func(t *testing.T) {
		var told atomic.Int32
		fired := make(chan struct{})
		r := newResizer(Size{}, nil, time.Millisecond, func() {
			if told.Add(1) == 1 {
				close(fired)
			}
		})
		stop := startResizer(r)
		r.submit(1, Size{Cols: 90, Rows: 20})
		awaitClosed(t, fired, "the report")
		r.submit(2, Size{Cols: 91, Rows: 20})
		stop()
		if got := told.Load(); got != 1 {
			t.Fatalf("the function was told %d times about the size it never read, want once", got)
		}
	})
	t.Run("a function that reads in time", func(t *testing.T) {
		var told atomic.Int32
		r := newResizer(Size{Cols: 80, Rows: 24}, nil, time.Hour, func() { told.Add(1) })
		stop := startResizer(r)
		<-r.channel()
		r.submit(1, Size{Cols: 90, Rows: 20})
		<-r.channel()
		stop()
		if told.Load() != 0 {
			t.Fatal("a function that reads its sizes was reported")
		}
	})
}

// The first value of Proc.Resize is the size the command was opened with, whatever a host sends before the function reads it:
// the newer size follows it, it does not replace it.
func TestAResizeBeforeTheFirstReadDoesNotReplaceTheInitialSize(t *testing.T) {
	r := newResizer(Size{Cols: 80, Rows: 24}, nil, 0, nil)
	stop := startResizer(r)
	defer stop()
	r.submit(1, Size{Cols: 100, Rows: 30})
	first, second := <-r.channel(), <-r.channel()
	if first != (Size{Cols: 80, Rows: 24}) || second != (Size{Cols: 100, Rows: 30}) {
		t.Fatalf("the function read %v then %v, want the initial 80 x 24 and then the host's 100 x 30", first, second)
	}
}

// BenchmarkOutputPipeFlood is the queue alone, with a writer goroutine and a reader that takes every byte: the bar is at
// least 1 GB/s in process and no allocation per operation.
func BenchmarkOutputPipeFlood(b *testing.B) {
	const chunk = 32 << 10
	for name, lossy := range map[string]bool{"blocking": false, "lossy": true} {
		b.Run(name, func(b *testing.B) {
			p := newOutputPipe(stdioQueueBytes, lossy)
			payload := make([]byte, chunk)
			go func() {
				for range b.N {
					if _, err := p.Write(streamOut, payload); err != nil {
						break
					}
				}
				p.CloseWrite()
			}()
			buf := make([]byte, 64<<10)
			b.SetBytes(chunk)
			b.ReportAllocs()
			b.ResetTimer()
			var moved uint64
			for {
				c, err := p.Read(context.Background(), buf)
				if err != nil {
					break
				}
				moved += uint64(c.N)
				for _, d := range c.Dropped {
					moved += d.Bytes
				}
			}
			b.StopTimer()
			if want := uint64(b.N) * chunk; moved != want {
				b.Fatalf("moved %d, want %d", moved, want)
			}
		})
	}
}
