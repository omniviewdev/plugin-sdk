package exec_test

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
)

func TestOutputBuffer_AppendAndGetAll(t *testing.T) {
	buf := exec.NewOutputBuffer(100)
	buf.Append([]byte("hello "))
	buf.Append([]byte("world"))

	got := buf.GetAll()
	if string(got) != "hello world" {
		t.Fatalf("expected %q, got %q", "hello world", string(got))
	}
}

func TestOutputBuffer_CyclicEviction(t *testing.T) {
	buf := exec.NewOutputBuffer(10) // small buffer
	buf.Append([]byte("12345"))     // 5 bytes
	buf.Append([]byte("67890"))     // 10 bytes total — at capacity
	buf.Append([]byte("ABC"))       // 13 bytes total — evict oldest 3

	got := buf.GetAll()
	if string(got) != "4567890ABC" {
		t.Fatalf("expected %q, got %q", "4567890ABC", string(got))
	}
}

func TestOutputBuffer_Len(t *testing.T) {
	buf := exec.NewOutputBuffer(100)
	if buf.Len() != 0 {
		t.Fatalf("expected len 0, got %d", buf.Len())
	}
	buf.Append([]byte("abc"))
	if buf.Len() != 3 {
		t.Fatalf("expected len 3, got %d", buf.Len())
	}
}

func TestOutputBuffer_GetAllReturnsCopy(t *testing.T) {
	buf := exec.NewOutputBuffer(100)
	buf.Append([]byte("abc"))
	data := buf.GetAll()
	data[0] = 'X' // mutate the copy
	got := buf.GetAll()
	if string(got) != "abc" {
		t.Fatal("GetAll returned a reference, not a copy")
	}
}

func TestOutputBuffer_ConcurrentAppend(t *testing.T) {
	buf := exec.NewOutputBuffer(10000)
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			buf.Append([]byte("x"))
		})
	}
	wg.Wait()
	if buf.Len() != 100 {
		t.Fatalf("expected 100, got %d", buf.Len())
	}
}

func TestOutputBuffer_DefaultCapacity(t *testing.T) {
	buf := exec.NewDefaultOutputBuffer()
	// Just verify it doesn't panic and has reasonable capacity
	buf.Append(make([]byte, 1000))
	if buf.Len() != 1000 {
		t.Fatalf("expected 1000, got %d", buf.Len())
	}
}

func TestOutputBuffer_NegativeCapacity(t *testing.T) {
	buf := exec.NewOutputBuffer(-1)
	buf.Append([]byte("abc"))
	// Capacity clamped to 0 — all appends discarded.
	if buf.Len() != 0 {
		t.Fatalf("expected len 0 after append to zero-capacity buffer, got %d", buf.Len())
	}
	got := buf.GetAll()
	if len(got) != 0 {
		t.Fatalf("expected empty GetAll, got %d bytes", len(got))
	}
}

func TestOutputBuffer_OversizedChunk(t *testing.T) {
	buf := exec.NewOutputBuffer(5)
	buf.Append([]byte("abcdefghij")) // 10 bytes into a capacity-5 buffer
	got := buf.GetAll()
	if string(got) != "fghij" {
		t.Fatalf("expected %q, got %q", "fghij", string(got))
	}
	if buf.Len() != 5 {
		t.Fatalf("expected len 5, got %d", buf.Len())
	}
}

// A ring is checked against the simplest thing that can be right: every byte appended so far,
// of which the last capacity are kept. Random chunk sizes, from nothing to more than the
// capacity, put the write position at every place of the ring, and the wrap at every offset.
func TestOutputBuffer_RingMatchesAModel(t *testing.T) {
	for _, capacity := range []int{1, 2, 3, 7, 16, 100} {
		rng := rand.New(rand.NewPCG(uint64(capacity), 1))
		ring := exec.NewOutputBuffer(capacity)
		var all []byte
		next := byte(0)
		for step := range 3000 {
			chunk := make([]byte, rng.IntN(2*capacity+2))
			for i := range chunk {
				chunk[i] = next
				next++
			}
			ring.Append(chunk)
			all = append(all, chunk...)

			want := all
			if len(want) > capacity {
				want = want[len(want)-capacity:]
			}
			if got := ring.GetAll(); !bytes.Equal(got, want) {
				t.Fatalf("capacity %d, step %d, after a chunk of %d: GetAll = %v, want %v", capacity, step, len(chunk), got, want)
			}
			if ring.Len() != len(want) {
				t.Fatalf("capacity %d, step %d: Len = %d, want %d", capacity, step, ring.Len(), len(want))
			}
			if ring.Total() != uint64(len(all)) {
				t.Fatalf("capacity %d, step %d: Total = %d, want %d", capacity, step, ring.Total(), len(all))
			}
		}
	}
}

// Total counts what was appended, evicted or not, and what a buffer that keeps nothing was given.
func TestOutputBuffer_Total(t *testing.T) {
	ring := exec.NewOutputBuffer(4)
	if ring.Total() != 0 {
		t.Fatalf("a new buffer has Total %d, want 0", ring.Total())
	}
	ring.Append([]byte("abc"))
	ring.Append(nil)
	ring.Append([]byte("defghij")) // more than the capacity: only "ghij" is kept
	if ring.Total() != 10 || string(ring.GetAll()) != "ghij" {
		t.Fatalf("Total = %d, kept %q; want 10 and %q", ring.Total(), ring.GetAll(), "ghij")
	}

	keeps := exec.NewOutputBuffer(0)
	keeps.Append([]byte("abcdef"))
	if keeps.Total() != 6 || keeps.Len() != 0 {
		t.Fatalf("a buffer of no capacity: Total = %d, Len = %d; want 6 and 0", keeps.Total(), keeps.Len())
	}
}

// A buffer that is read while it is written must give, every time, a run of the bytes that were
// appended: a copy that crossed the wrap wrongly, or that raced an append, has a hole or a seam.
func TestOutputBuffer_ReadsDuringAppendsAreContiguousRuns(t *testing.T) {
	const capacity = 1000
	ring := exec.NewOutputBuffer(capacity)
	stop := make(chan struct{})
	started := make(chan struct{})
	var writer sync.WaitGroup
	writer.Go(func() {
		next := byte(0)
		rng := rand.New(rand.NewPCG(1, 2))
		for first := true; ; first = false {
			select {
			case <-stop:
				return
			default:
			}
			chunk := make([]byte, 1+rng.IntN(300))
			for i := range chunk {
				chunk[i] = next
				next++
			}
			ring.Append(chunk)
			if first {
				close(started)
			}
		}
	})
	defer func() {
		close(stop)
		writer.Wait()
	}()
	<-started // the reads below are of a buffer that has something in it

	reads := 0
	for range 2000 {
		got := ring.GetAll()
		if len(got) > 0 {
			reads++
		}
		for i := 1; i < len(got); i++ {
			if got[i] != got[i-1]+1 {
				t.Fatalf("a read of %d bytes has a seam at %d: %d then %d", len(got), i, got[i-1], got[i])
			}
		}
	}
	if reads == 0 {
		t.Fatal("no read saw any of what was appended")
	}
}

// Once the buffer is full an append is a copy into the ring and nothing else: it allocates
// nothing, whatever the size of the chunk and wherever the write position is. (It used to
// allocate the whole buffer again, a megabyte for each chunk of a terminal's output.)
func TestRingAppendDoesNotAllocate(t *testing.T) {
	const capacity = 1 << 16
	for _, size := range []int{1, 100, 1024, 4096, capacity - 1, capacity, capacity + 1, 3 * capacity} {
		ring := exec.NewOutputBuffer(capacity)
		chunk := bytes.Repeat([]byte{'x'}, size)
		for range 2*capacity/size + 2 {
			ring.Append(chunk) // fills the ring, and moves the write position
		}
		if allocs := testing.AllocsPerRun(100, func() { ring.Append(chunk) }); allocs != 0 {
			t.Errorf("a chunk of %d bytes into a full buffer of %d allocated %v times per append, want 0", size, capacity, allocs)
		}
	}
}

// GetAll is the one place a copy is made: one allocation, of the size of what is kept.
func TestOutputBuffer_GetAllAllocatesOnce(t *testing.T) {
	ring := exec.NewOutputBuffer(1 << 16)
	ring.Append(bytes.Repeat([]byte{'x'}, 3<<16)) // full, and not at the start of the array
	ring.Append([]byte("tail"))
	if allocs := testing.AllocsPerRun(100, func() { _ = ring.GetAll() }); allocs != 1 {
		t.Errorf("GetAll allocated %v times, want 1", allocs)
	}
}

// BenchmarkOutputBufferAppend is the cost of keeping a session's output: a terminal appends the
// chunk it has read, a kilobyte on macOS, four on Linux, to a buffer that is full once the session
// has said more than DefaultOutputBufferSize.
//
//	go test ./pkg/v1/exec -run '^$' -bench OutputBufferAppend -benchmem
func BenchmarkOutputBufferAppend(b *testing.B) {
	for _, size := range []int{1 << 10, 4 << 10, 32 << 10} {
		b.Run(fmt.Sprintf("chunk=%dKiB", size>>10), func(b *testing.B) {
			ring := exec.NewDefaultOutputBuffer()
			chunk := bytes.Repeat([]byte{'x'}, size)
			for range 2*exec.DefaultOutputBufferSize/size + 1 {
				ring.Append(chunk)
			}
			b.SetBytes(int64(size))
			b.ReportAllocs()
			for b.Loop() {
				ring.Append(chunk)
			}
		})
	}
}
