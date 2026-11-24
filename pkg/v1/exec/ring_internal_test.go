package exec

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/session"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The tests of the ring hold it as a data structure: no engine, no stream. A byte of the output is a function of its offset
// (ringByte), so a reader verifies every byte it takes from the offset alone, and no test keeps a copy of what was written.

// ringByte is the byte written at an offset.
func ringByte(offset uint64) byte { return byte(offset*131 + offset>>7) }

// ringPattern returns n bytes of the pattern from offset.
func ringPattern(offset uint64, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = ringByte(offset + uint64(i))
	}
	return b
}

// fakeHost is the host of a ring that a test controls.
type fakeHost struct {
	mu     sync.Mutex
	over   bool
	causes []session.Cause
	kills  []bool
}

func (h *fakeHost) ending() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.over
}

func (h *fakeHost) end(cause session.Cause, kill bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.over = true
	h.causes = append(h.causes, cause)
	h.kills = append(h.kills, kill)
}

// testRing is a ring with the attachment a new session has, or none.
type testRing struct {
	*ring
	host    *fakeHost
	creator *attachment
}

// newTestRing makes a ring of capacity bytes. A blocking ring holds its writer at the unread limit while its creator reads;
// a lossy one never does; with detached set the session has no stream at all.
func newTestRing(capacity int, lossy, detached bool) *testRing {
	r := newRing(ringConfig{capacity: capacity, lossy: lossy, unreadLimit: stdioQueueBytes})
	host := &fakeHost{}
	r.att.host = host
	tr := &testRing{ring: r, host: host}
	if !detached {
		tr.creator = r.newAttachment(0)
		r.att.cur = tr.creator
	}
	return tr
}

// await polls cond until it holds. The condition is the oracle; the bound only keeps a bug from hanging the suite.
func await(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for spins := 0; !cond(); spins++ {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		if spins < 200 {
			runtime.Gosched()
		} else {
			time.Sleep(50 * time.Microsecond)
		}
	}
}

func TestARingHoldsAtMostItsCapacityInWholeChunksAndTheLargestChunkIs64KiB(t *testing.T) {
	for _, tc := range []struct {
		capacity, chunk, chunks int
	}{
		{1 << 10, 256, 4},
		{1500, 256, 5},
		{100 << 10, 16 << 10, 6},
		{255 << 10, 32 << 10, 7},
		{256 << 10, 64 << 10, 4},
		{1 << 20, 64 << 10, 16},
		{4 << 20, 64 << 10, 64},
		{64 << 20, 64 << 10, 1024},
	} {
		chunk, chunks := ringGeometry(tc.capacity)
		if chunk != tc.chunk || chunks != tc.chunks {
			t.Errorf("a ring of %d bytes is %d chunks of %d, want %d of %d", tc.capacity, chunks, chunk, tc.chunks, tc.chunk)
		}
		if chunk*chunks > tc.capacity {
			t.Errorf("a ring of %d bytes holds %d: more than it was declared to", tc.capacity, chunk*chunks)
		}
	}
}

// The ring holds the newest whole chunks: after every write, its front is where the arithmetic says, which is the check of the
// place where it wraps. Every byte it holds is the byte written at its offset, and no more than its capacity is held.
func TestARingKeepsTheNewestWholeChunksWhateverTheSizesOfTheWritesAndItsOffsetsAreAbsolute(t *testing.T) {
	const capacity, chunk = 1024, 256
	rng := rand.New(rand.NewPCG(1, 2))
	tr := newTestRing(capacity, false, true)
	var end uint64
	for i := range 400 {
		n := 1 + rng.IntN(700)
		if i%50 == 0 {
			n = capacity * (1 + i/50) // a write larger than the ring, then larger again
		}
		if got, err := tr.Write(execv2.Stream_STREAM_TTY, ringPattern(end, n)); err != nil || got != n {
			t.Fatalf("write %d: %d, %v", i, got, err)
		}
		end += uint64(n)
		chunks := (end + chunk - 1) / chunk
		wantStart := (max(chunks, 4) - 4) * chunk
		runs, start, next := tr.readAt(0, 1<<20, 1<<20)
		if start != wantStart {
			t.Fatalf("after %d bytes the ring starts at %d, want %d: it holds the newest whole chunks", end, start, wantStart)
		}
		if end-start > capacity || (end > capacity && end-start <= capacity-chunk) {
			t.Fatalf("after %d bytes the ring holds %d (from %d), want more than %d and at most %d", end, end-start, start, capacity-chunk, capacity)
		}
		var got []byte
		var at = start
		for _, run := range runs {
			if run.Offset != at {
				t.Fatalf("after %d bytes a run at offset %d, want %d: the runs are contiguous", end, run.Offset, at)
			}
			got = append(got, run.Data...)
			at += uint64(len(run.Data))
		}
		if next != end || !bytes.Equal(got, ringPattern(start, int(end-start))) {
			t.Fatalf("after %d bytes the ring reads back %d bytes to %d, not the %d bytes written from %d", end, len(got), next, end-start, start)
		}
	}
}

// A ring that nobody reads never holds its writer, whatever it is asked to hold, and counts exactly the bytes that left it
// before any reader took them.
func TestADetachedRingNeverHoldsItsWriterAndCountsWhatNobodyRead(t *testing.T) {
	tr := newTestRing(1<<20, false, true)
	const total = 10<<20 + 12345
	for written := 0; written < total; {
		n := min(100<<10, total-written)
		if got, err := tr.Write(execv2.Stream_STREAM_TTY, make([]byte, n)); err != nil || got != n {
			t.Fatalf("write at %d: %d, %v", written, got, err)
		}
		written += n
	}
	start, end := tr.window()
	stats := tr.Stats()
	if end != total || stats.Written != total || stats.Dropped != start || stats.DroppedDetached != start || stats.Stalls != 0 {
		t.Fatalf("the ring is [%d, %d) and says written %d, dropped %d (detached %d), stalls %d: want dropped = start, all of it while detached, no stall",
			start, end, stats.Written, stats.Dropped, stats.DroppedDetached, stats.Stalls)
	}
	if stats.Queued != total {
		t.Fatalf("queued %d, want %d: no reader has taken a byte", stats.Queued, total)
	}
}

// A reader that may not lose a byte holds the writer when it has left the unread bound unread, and the ring then loses nothing, however
// many times it wraps. The writer is held at exactly the bound and never past it. The bound is stdioQueueBytes (256 KiB), and for a ring
// that cannot hold that and still keep a chunk the reader has read, the ring less one chunk: for every size of ring that a host can ask for.
func TestAReaderThatMayNotLoseAByteHoldsTheWriterAtTheUnreadBoundAndTheRingLosesNothing(t *testing.T) {
	for _, tc := range []struct {
		name     string
		capacity int
		bound    uint64
	}{
		{"1 KiB: 4 chunks of 256 bytes, three of them unread", 1 << 10, 3 * 256},
		{"64 KiB: 4 chunks of 16 KiB, three of them unread", 64 << 10, 3 * (16 << 10)},
		{"256 KiB: 4 chunks of 64 KiB, three of them unread", 256 << 10, 3 * (64 << 10)},
		{"1 MiB: 256 KiB unread", 1 << 20, 256 << 10},
		{"4 MiB, the largest: 256 KiB unread", 4 << 20, 256 << 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := newTestRing(tc.capacity, false, false)
			const total, piece = 6 << 20, 32 << 10
			done := make(chan error, 1)
			go func() {
				for off := 0; off < total; off += piece {
					if _, err := tr.Write(execv2.Stream_STREAM_TTY, ringPattern(uint64(off), piece)); err != nil {
						done <- err
						return
					}
				}
				done <- nil
			}()
			// Nobody has read: the writer is held when the unread bytes reach the bound, to the byte.
			await(t, "the writer to be held", func() bool { return tr.Stats().Stalls > 0 })
			if stats := tr.Stats(); stats.Queued != tc.bound || stats.Written != tc.bound {
				t.Fatalf("the writer was held with %d unread and %d written, want %d: the bound", stats.Queued, stats.Written, tc.bound)
			}
			// Reading releases it, and the reader takes every byte in order, with the writer held again and again.
			buf := make([]byte, 20_000)
			var at uint64
			for at < total {
				chunk, err := tr.creator.Read(context.Background(), buf)
				if err != nil {
					t.Fatalf("read at %d: %v", at, err)
				}
				if len(chunk.Dropped) != 0 || chunk.Offset != at {
					t.Fatalf("a chunk at %d with %d lost ranges, the reader is at %d: a blocking ring loses nothing", chunk.Offset, len(chunk.Dropped), at)
				}
				if !bytes.Equal(buf[:chunk.N], ringPattern(at, chunk.N)) {
					t.Fatalf("the bytes at %d are not the bytes written there", at)
				}
				at += uint64(chunk.N)
				if queued := tr.Stats().Queued; queued > tc.bound {
					t.Fatalf("%d bytes unread, the bound is %d", queued, tc.bound)
				}
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if stats := tr.Stats(); stats.Dropped != 0 || stats.Stalls == 0 || stats.Written != total {
				t.Fatalf("stats %+v: want nothing dropped, the writer held, %d written", stats, total)
			}
		})
	}
}

// A lossy ring never holds its writer: it drops its oldest chunk, counts the bytes its reader had not taken, and the reader is told
// the range it lost before the bytes that follow, with the stream of the first byte of the range.
func TestALossyRingNeverHoldsItsWriterAndTheReaderIsToldExactlyWhatItLost(t *testing.T) {
	// The reader has taken part of a chunk when the ring starts to lose what it has not read: the range it is told it lost
	// begins where it stopped, and the count says the same.
	for _, readFirst := range []int{0, 10_000} {
		tr := newTestRing(1<<20, true, false)
		buf := make([]byte, 64<<10)
		const total = 5<<20 + 777
		written := 0
		write := func(n int) {
			t.Helper()
			if got, err := tr.Write(execv2.Stream_STREAM_TTY, ringPattern(uint64(written), n)); err != nil || got != n {
				t.Fatalf("write at %d: %d, %v", written, got, err)
			}
			written += n
		}
		var taken uint64
		if readFirst > 0 {
			write(30000)
			chunk, err := tr.creator.Read(context.Background(), buf[:readFirst])
			if err != nil || chunk.N != readFirst {
				t.Fatalf("the first read: %d, %v", chunk.N, err)
			}
			taken = uint64(chunk.N)
		}
		for written < total {
			write(min(30000, total-written))
		}
		start, end := tr.window()
		stats := tr.Stats()
		if start == 0 || stats.Dropped != start-taken || stats.DroppedDetached != 0 || stats.Stalls != 0 {
			t.Fatalf("ring [%d, %d), stats %+v, read %d: want the %d bytes between the reader and the front dropped, none while detached, no stall", start, end, stats, taken, start-taken)
		}
		chunk, err := tr.creator.Read(context.Background(), buf)
		if err != nil {
			t.Fatal(err)
		}
		if len(chunk.Dropped) != 1 || chunk.Dropped[0].From != taken || chunk.Dropped[0].Bytes != start-taken || chunk.Dropped[0].Stream != execv2.Stream_STREAM_TTY || chunk.Offset != start {
			t.Fatalf("the first read says %+v at offset %d, want one range [%d, %d) of the terminal stream, then the bytes from %d", chunk.Dropped, chunk.Offset, taken, start, start)
		}
		at := start
		for {
			if chunk.N > 0 {
				if chunk.Offset != at || !bytes.Equal(buf[:chunk.N], ringPattern(at, chunk.N)) {
					t.Fatalf("a chunk at %d, want %d, with the bytes written there", chunk.Offset, at)
				}
				at += uint64(chunk.N)
			}
			if at == end {
				break
			}
			if chunk, err = tr.creator.Read(context.Background(), buf); err != nil {
				t.Fatal(err)
			}
		}
		// What was read, and what the reader was told it lost, are everything that was written.
		if at != total {
			t.Fatalf("the reader ended at %d, want %d", at, total)
		}
	}
}

// A session that is ending releases a writer that a blocking ring holds: the ring then never waits for a reader.
func TestAnEndingRingReleasesTheWriterItHoldsAndNeverHoldsItAgain(t *testing.T) {
	tr := newTestRing(1<<20, false, false)
	released := make(chan int, 1)
	go func() {
		n, _ := tr.Write(execv2.Stream_STREAM_TTY, make([]byte, 4<<20))
		released <- n
	}()
	await(t, "the writer to be held", func() bool { return tr.Stats().Stalls > 0 })
	tr.beginEnding()
	select {
	case n := <-released:
		if n != 4<<20 {
			t.Fatalf("the released write took %d of %d bytes", n, 4<<20)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the writer was not released by the end of the session")
	}
	if stats := tr.Stats(); stats.Dropped == 0 || stats.Dropped != tr.start() {
		t.Fatalf("stats %+v with the front at %d: the bytes that left the ring unread are counted", stats, tr.start())
	}
}

// A writer that was waiting is released when the reader detaches too: a ring with no reader never holds its writer.
func TestARingThatLosesItsReaderReleasesTheWriterItHolds(t *testing.T) {
	tr := newTestRing(1<<20, false, false)
	released := make(chan struct{})
	go func() {
		_, _ = tr.Write(execv2.Stream_STREAM_TTY, make([]byte, 3<<20))
		close(released)
	}()
	await(t, "the writer to be held", func() bool { return tr.Stats().Stalls > 0 })
	if _, ok := tr.detach(tr.creator, time.Minute, time.Unix(1000, 0)); !ok {
		t.Fatal("the stream could not detach")
	}
	select {
	case <-released:
	case <-time.After(30 * time.Second):
		t.Fatal("the writer was not released when the stream detached")
	}
	if stats := tr.Stats(); stats.DroppedDetached == 0 {
		t.Fatalf("stats %+v: what the detached ring lost is counted as lost while detached", stats)
	}
}

// Two streams write one session in the order they arrive. A chunk may hold both, and every run of a frame, a replay and a cursor
// read is of one stream, whatever the sizes of the writes and the place of the chunk boundaries.
func TestRunsOfTwoStreamsAreReadBackWithTheirStreamsThroughEveryPath(t *testing.T) {
	for _, capacity := range []int{1 << 10, 1 << 20} {
		for seed := uint64(1); seed <= 6; seed++ {
			rng := rand.New(rand.NewPCG(seed, 99))
			tr := newTestRing(capacity, false, true)
			var model []execv2.Stream
			for len(model) < 6*capacity {
				stream := execv2.Stream_STREAM_STDOUT
				if rng.IntN(2) == 0 {
					stream = execv2.Stream_STREAM_STDERR
				}
				n := 1 + rng.IntN(1+rng.IntN(300)*rng.IntN(40))
				if _, err := tr.Write(stream, ringPattern(uint64(len(model)), n)); err != nil {
					t.Fatal(err)
				}
				for range n {
					model = append(model, stream)
				}
			}
			start, end := tr.window()
			check := func(path string, offset uint64, stream execv2.Stream, data []byte) {
				t.Helper()
				if offset < start || offset+uint64(len(data)) > end {
					t.Fatalf("%s: a run [%d, %d) outside the ring [%d, %d)", path, offset, offset+uint64(len(data)), start, end)
				}
				for i, b := range data {
					if model[offset+uint64(i)] != stream || b != ringByte(offset+uint64(i)) {
						t.Fatalf("%s: the byte at %d is %#x of %v, want %#x of %v", path, offset+uint64(i), b, stream, ringByte(offset+uint64(i)), model[offset+uint64(i)])
					}
				}
			}
			// A cursor read, as ReadOutput makes it.
			runs, _, next := tr.readAt(start, 1<<30, 1<<30)
			at := start
			for _, run := range runs {
				if run.Offset != at || len(run.Data) == 0 {
					t.Fatalf("readAt: a run at %d with %d bytes, want %d", run.Offset, len(run.Data), at)
				}
				check("readAt", run.Offset, run.Stream, run.Data)
				at += uint64(len(run.Data))
			}
			if next != end || at != end {
				t.Fatalf("readAt ended at %d (%d), want %d", at, next, end)
			}
			// A replay: the attach of a stream from the middle of the ring.
			from := start + (end-start)/3
			a, info, err := tr.attach(context.Background(), from)
			if err != nil || info.Start != from || info.End != end {
				t.Fatalf("attach: %+v, %v, want a replay of [%d, %d)", info, err, from, end)
			}
			buf := make([]byte, 1+rng.IntN(70_000))
			at = from
			for at < end {
				chunk, err := a.Read(context.Background(), buf)
				if err != nil || chunk.Offset != at || chunk.N == 0 {
					t.Fatalf("replay: %+v, %v at %d", chunk, err, at)
				}
				check("replay", chunk.Offset, chunk.Stream, buf[:chunk.N])
				at += uint64(chunk.N)
			}
		}
	}
}

// A frame of a flood is as large as the buffer, whatever chunk boundary it crosses: the plan's bars count on frames of 64 KiB.
func TestAFrameOfOneStreamCrossesChunkBoundariesAndIsAsLargeAsTheBuffer(t *testing.T) {
	tr := newTestRing(1<<20, true, false)
	const total = 800 << 10
	for off := 0; off < total; off += 32 << 10 {
		if _, err := tr.Write(execv2.Stream_STREAM_TTY, ringPattern(uint64(off), 32<<10)); err != nil {
			t.Fatal(err)
		}
	}
	// The first read is short, so that every later frame starts inside a chunk and has to cross the next boundary to be whole.
	buf := make([]byte, 10_000)
	chunk, err := tr.creator.Read(context.Background(), buf)
	if err != nil || chunk.N != len(buf) {
		t.Fatalf("the short read: %d bytes, %v", chunk.N, err)
	}
	at := uint64(chunk.N)
	buf = make([]byte, 64<<10)
	for frames := 0; at < total; frames++ {
		chunk, err := tr.creator.Read(context.Background(), buf)
		if err != nil {
			t.Fatal(err)
		}
		want := min(len(buf), int(total-at))
		if chunk.N != want || chunk.Offset != at || !bytes.Equal(buf[:chunk.N], ringPattern(at, chunk.N)) {
			t.Fatalf("frame %d at %d has %d bytes at offset %d, want %d: a frame is as large as the buffer when the ring holds that much", frames, at, chunk.N, chunk.Offset, want)
		}
		at += uint64(chunk.N)
	}
}

// A ring that has filled once allocates nothing more, whatever flows through it: chunks are reused, and reads are into the
// reader's buffer. (The race detector allocates for its own bookkeeping, which this does not measure.)
func TestARingThatHasFilledAllocatesNothingForTheOutputThatFlowsThroughIt(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector allocates")
	}
	tr := newTestRing(1<<20, false, false)
	piece := make([]byte, 32<<10)
	buf := make([]byte, 64<<10)
	var off uint64
	step := func() {
		if _, err := tr.Write(execv2.Stream_STREAM_TTY, piece); err != nil {
			t.Fatal(err)
		}
		off += uint64(len(piece))
		for {
			if tr.Stats().Queued == 0 {
				return
			}
			if _, err := tr.creator.Read(context.Background(), buf); err != nil {
				t.Fatal(err)
			}
		}
	}
	for range 64 { // warm-up: more than the ring holds
		step()
	}
	if allocs := testing.AllocsPerRun(2000, step); allocs != 0 {
		t.Fatalf("%v allocations for each 32 KiB written and read after the ring filled, want 0", allocs)
	}
	if start, end := tr.window(); start == 0 || end != off {
		t.Fatalf("the ring is [%d, %d) after %d bytes: it should have wrapped", start, end, off)
	}
}

// The size log says the size the bytes at an offset were produced for: the size the terminal was opened with, and each size the
// function took, from the offset it took it at; it is bounded, and it says "not known" where it lost its entries.
func TestTheSizeLogSaysTheSizeInForceAtAnOffsetAndForgetsWhatTheRingForgot(t *testing.T) {
	r := newRing(ringConfig{capacity: 1024, unreadLimit: stdioQueueBytes, initialSize: Size{Cols: 80, Rows: 24}})
	host := &fakeHost{}
	r.att.host = host
	at := func(offset uint64, want Size, known bool) {
		t.Helper()
		r.mu.Lock()
		got, ok := r.sizeAt(offset)
		r.mu.Unlock()
		if ok != known || got != want {
			t.Fatalf("the size at %d is %v (known %v), want %v (known %v)", offset, got, ok, want, known)
		}
	}
	write := func(n int) {
		t.Helper()
		if _, err := r.Write(execv2.Stream_STREAM_TTY, make([]byte, n)); err != nil {
			t.Fatal(err)
		}
	}
	at(0, Size{80, 24}, true)
	write(100)
	r.noteSize(Size{100, 30})
	r.noteSize(Size{100, 30}) // the same size again: nothing new
	write(50)
	r.noteSize(Size{120, 40})
	r.noteSize(Size{121, 41}) // at the same offset: the later one replaces it
	write(10)
	r.noteSize(Size{}) // no size: ignored
	at(0, Size{80, 24}, true)
	at(99, Size{80, 24}, true)
	at(100, Size{100, 30}, true)
	at(149, Size{100, 30}, true)
	at(150, Size{121, 41}, true)
	at(1<<40, Size{121, 41}, true)
	if got := len(r.sizes); got != 3 {
		t.Fatalf("the log holds %d entries, want 3: %+v", got, r.sizes)
	}
	// The ring forgets its front, and the log keeps what is in force there.
	write(1024)
	if start := r.start(); start == 0 {
		t.Fatal("the ring did not wrap")
	}
	r.mu.Lock()
	got, ok := r.sizeAt(r.start())
	r.mu.Unlock()
	if !ok || got != (Size{121, 41}) || len(r.sizes) != 1 {
		t.Fatalf("at the front of the ring the size is %v (known %v) with %d entries, want the one in force (121 x 41) and no older entry", got, ok, len(r.sizes))
	}
	// A function that takes more sizes than the log holds loses the oldest, and the log says it does not know the size before them.
	for i := range maxSizeLog + 10 {
		write(1)
		r.noteSize(Size{Cols: uint16(200 + i), Rows: 30})
	}
	if len(r.sizes) > maxSizeLog {
		t.Fatalf("the log holds %d entries, at most %d", len(r.sizes), maxSizeLog)
	}
	r.mu.Lock()
	_, knownBefore := r.sizeAt(r.sizes[0].off - 1)
	_, knownAt := r.sizeAt(r.sizes[0].off)
	r.mu.Unlock()
	if knownBefore || !knownAt {
		t.Fatalf("known before the oldest entry: %v, at it: %v, want false and true", knownBefore, knownAt)
	}
	// A size taken exactly where the front of the ring ends up is the one in force there, and nothing older is kept.
	edge := newRing(ringConfig{capacity: 1024, unreadLimit: stdioQueueBytes, initialSize: Size{Cols: 80, Rows: 24}})
	edge.att.host = &fakeHost{}
	if _, err := edge.Write(execv2.Stream_STREAM_TTY, make([]byte, 512)); err != nil {
		t.Fatal(err)
	}
	edge.noteSize(Size{Cols: 90, Rows: 30})
	if _, err := edge.Write(execv2.Stream_STREAM_TTY, make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	if edge.start() != 512 || len(edge.sizes) != 1 || edge.sizes[0].size != (Size{Cols: 90, Rows: 30}) {
		t.Fatalf("the front is at %d and the log is %+v, want the front at 512 and one entry: the size taken at 512", edge.start(), edge.sizes)
	}
	// A session without a terminal has no size at all.
	bare := newRing(ringConfig{capacity: 1024, unreadLimit: stdioQueueBytes})
	if _, ok := bare.currentSize(); ok {
		t.Fatal("a ring with no terminal has a size")
	}
	bare.mu.Lock()
	_, ok = bare.sizeAt(0)
	bare.mu.Unlock()
	if ok {
		t.Fatal("a ring with no terminal knows the size at 0")
	}
}

// A write after the ring was closed is refused and counted, a write that is waiting is released with the same refusal, and the
// readers drain what the ring holds and then get EOF.
func TestAClosedRingRefusesWritesAndGivesItsReaderEOFAfterTheBytes(t *testing.T) {
	tr := newTestRing(1<<20, false, false)
	if _, err := tr.Write(execv2.Stream_STREAM_TTY, []byte("abc")); err != nil {
		t.Fatal(err)
	}
	tr.CloseWrite()
	tr.CloseWrite()
	if n, err := tr.Write(execv2.Stream_STREAM_TTY, []byte("late")); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("a write after close: %d, %v", n, err)
	}
	if stats := tr.Stats(); stats.Late != 1 || stats.LateB != 4 || stats.Written != 3 {
		t.Fatalf("stats %+v, want one late write of 4 bytes and 3 written", stats)
	}
	buf := make([]byte, 16)
	chunk, err := tr.creator.Read(context.Background(), buf)
	if err != nil || string(buf[:chunk.N]) != "abc" {
		t.Fatalf("read %q, %v", buf[:chunk.N], err)
	}
	if _, err := tr.creator.Read(context.Background(), buf); !errors.Is(err, io.EOF) {
		t.Fatalf("read after the bytes: %v, want EOF", err)
	}
	if _, err := tr.creator.Read(context.Background(), nil); !errors.Is(err, io.ErrShortBuffer) {
		t.Fatalf("read with no buffer: %v, want io.ErrShortBuffer", err)
	}

	// A write that is waiting when the ring closes is refused, with the bytes it had placed counted as written.
	held := newTestRing(1<<20, false, false)
	result := make(chan error, 1)
	go func() {
		_, err := held.Write(execv2.Stream_STREAM_TTY, make([]byte, 1<<20))
		result <- err
	}()
	await(t, "the writer to be held", func() bool { return held.Stats().Stalls > 0 })
	held.CloseWrite()
	if err := <-result; !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("the write that was waiting: %v, want io.ErrClosedPipe", err)
	}
}

// A reader waiting for output is released by its context ending, by the ring closing and by its attachment leaving.
func TestAReaderThatWaitsIsReleasedByItsContextTheEndOfTheOutputAndItsStreamLeaving(t *testing.T) {
	t.Run("its context", func(t *testing.T) {
		tr := newTestRing(1<<20, false, false)
		ctx, cancel := context.WithCancel(context.Background())
		got := make(chan error, 1)
		go func() {
			_, err := tr.creator.Read(ctx, make([]byte, 8))
			got <- err
		}()
		await(t, "the reader to wait", func() bool { tr.mu.Lock(); defer tr.mu.Unlock(); return tr.waiters > 0 })
		cancel()
		if err := <-got; !errors.Is(err, context.Canceled) {
			t.Fatalf("the reader's error is %v, want its context's", err)
		}
		cancelled, cancel2 := context.WithCancel(context.Background())
		cancel2()
		if _, err := tr.creator.Read(cancelled, make([]byte, 8)); !errors.Is(err, context.Canceled) {
			t.Fatalf("a read with a context that ended: %v", err)
		}
	})
	t.Run("the end of the output", func(t *testing.T) {
		tr := newTestRing(1<<20, false, false)
		got := make(chan error, 1)
		go func() {
			_, err := tr.creator.Read(context.Background(), make([]byte, 8))
			got <- err
		}()
		await(t, "the reader to wait", func() bool { tr.mu.Lock(); defer tr.mu.Unlock(); return tr.waiters > 0 })
		tr.CloseWrite()
		if err := <-got; !errors.Is(err, io.EOF) {
			t.Fatalf("the reader's error is %v, want io.EOF", err)
		}
	})
	t.Run("its stream leaving", func(t *testing.T) {
		tr := newTestRing(1<<20, false, false)
		got := make(chan error, 1)
		go func() {
			_, err := tr.creator.Read(context.Background(), make([]byte, 8))
			got <- err
		}()
		await(t, "the reader to wait", func() bool { tr.mu.Lock(); defer tr.mu.Unlock(); return tr.waiters > 0 })
		if _, ok := tr.detach(tr.creator, time.Minute, time.Unix(1, 0)); !ok {
			t.Fatal("the stream could not detach")
		}
		if err := <-got; !errors.Is(err, errStreamLeft) {
			t.Fatalf("the reader's error is %v, want errStreamLeft", err)
		}
	})
}

// A ring of 1 MiB under a flood of 64 MiB, written by one goroutine and read by another at whatever pace they have, allocates nothing
// once it has filled: a blocking ring none at all, and a lossy ring only for the ranges it reports as dropped (one small slice for
// each report: "when it reports a dropped range, and not otherwise"). The measure is the process's own: every allocation of the run,
// the writer's goroutine and the reader's included, against the 64 MiB that went through.
func TestARingOf1MiBUnderA64MiBFloodWithAConcurrentReaderAllocatesNothingOnceItHasFilled(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector allocates for its own bookkeeping")
	}
	const total = 64 << 20
	for _, lossy := range []bool{false, true} {
		name := "blocking"
		if lossy {
			name = "lossy"
		}
		t.Run(name, func(t *testing.T) {
			tr := newTestRing(1<<20, lossy, false)
			piece := make([]byte, 32<<10)
			buf := make([]byte, maxOutputFrame)
			flood := func(bytes uint64) (read, dropped, reports uint64) {
				done := make(chan struct{})
				go func() {
					defer close(done)
					for w := uint64(0); w < bytes; w += uint64(len(piece)) {
						if _, err := tr.Write(execv2.Stream_STREAM_TTY, piece); err != nil {
							t.Error(err)
							return
						}
					}
				}()
				for read+dropped < bytes {
					chunk, err := tr.creator.Read(context.Background(), buf)
					if err != nil {
						t.Errorf("read: %v", err)
						break
					}
					read += uint64(chunk.N)
					for _, d := range chunk.Dropped {
						dropped += d.Bytes
						reports++
					}
				}
				<-done
				return read, dropped, reports
			}
			flood(8 << 20) // the ring fills and wraps: its chunks are allocated now
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			read, dropped, reports := flood(total)
			runtime.ReadMemStats(&after)
			allocs, bytes := after.Mallocs-before.Mallocs, after.TotalAlloc-before.TotalAlloc
			t.Logf("%s ring of 1 MiB, %d MiB through it: %d MiB read, %d MiB dropped in %d reports; %d allocations, %d bytes allocated (%.5f B/B)",
				name, total>>20, read>>20, dropped>>20, reports, allocs, bytes, float64(bytes)/float64(total))
			if read+dropped != total {
				t.Fatalf("read %d and dropped %d bytes of %d", read, dropped, total)
			}
			if !lossy && (dropped != 0 || reports != 0) {
				t.Fatalf("a blocking ring with a reader dropped %d bytes in %d reports", dropped, reports)
			}
			// The bounds: a goroutine and a channel for the run, and one slice for each report of a range that was dropped.
			if limit := 64 + 2*reports; allocs > limit {
				t.Errorf("%d allocations for 64 MiB and %d dropped reports, want at most %d: the ring allocates only to report what it dropped", allocs, reports, limit)
			}
			if limit := uint64(16<<10) + 64*reports; bytes > limit {
				t.Errorf("%d bytes allocated for 64 MiB and %d dropped reports, want at most %d", bytes, reports, limit)
			}
		})
	}
}

// hostEnds is what the session was asked to end with, and whether each was a kill.
func hostEnds(h *fakeHost) (causes []session.Cause, kills []bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]session.Cause(nil), h.causes...), append([]bool(nil), h.kills...)
}

// A Close from a stream that another attach took the session over from does not destroy the session its successor holds, whatever the
// frame loop saw before: the ring decides, under its lock, whether the stream is still the session's. The Close of the session's own
// stream does, and a kill is a kill.
func TestACloseFromAStreamThatIsNoLongerTheSessionsIsNotAppliedAndTheSessionsOwnIs(t *testing.T) {
	tr := newTestRing(1<<20, false, false)
	old := tr.creator
	next, _, err := tr.attach(context.Background(), 0) // takes the session over from old
	if err != nil {
		t.Fatal(err)
	}
	tr.closeFrom(old, false)
	if causes, _ := hostEnds(tr.host); len(causes) != 0 {
		t.Fatalf("a stream that was superseded ended the session with %v", causes)
	}
	tr.closeFrom(next, true)
	if causes, kills := hostEnds(tr.host); len(causes) != 1 || causes[0] != session.CauseRequested || !kills[0] {
		t.Fatalf("the session's own stream asked for a kill and the session was asked to end with %v (kills %v)", causes, kills)
	}
}

// A lease ends a session only if it is the lease that runs and the session has no stream: one that an attach made stale ends nothing,
// not an attached session and not a session that has been detached again under a lease of its own.
func TestAStaleLeaseNeverEndsASessionAndTheCurrentOneEndsOnlyADetachedSession(t *testing.T) {
	tr := newTestRing(1<<20, false, false)
	now := time.Unix(1000, 0)
	first, ok := tr.detach(tr.creator, time.Minute, now)
	if !ok {
		t.Fatal("the stream could not detach")
	}
	next, _, err := tr.attach(context.Background(), 0) // cancels the lease
	if err != nil {
		t.Fatal(err)
	}
	tr.expireLease(first.gen) // the lease runs out after the attach: stale, and a stream is attached
	if causes, _ := hostEnds(tr.host); len(causes) != 0 {
		t.Fatalf("a lease that an attach cancelled ended an attached session with %v", causes)
	}
	second, ok := tr.detach(next, time.Minute, now)
	if !ok {
		t.Fatal("the stream could not detach again")
	}
	tr.expireLease(first.gen) // the first lease runs out now: stale, though the session is detached under another
	if causes, _ := hostEnds(tr.host); len(causes) != 0 {
		t.Fatalf("a stale lease ended a session that is detached under the lease of a later detach, with %v", causes)
	}
	tr.expireLease(second.gen)
	if causes, kills := hostEnds(tr.host); len(causes) != 1 || causes[0] != session.CauseExpired || kills[0] {
		t.Fatalf("the lease that runs ended the detached session with %v (kills %v), want CauseExpired, graceful", causes, kills)
	}
}
