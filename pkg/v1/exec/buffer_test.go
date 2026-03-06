package exec_test

import (
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
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf.Append([]byte("x"))
		}()
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
