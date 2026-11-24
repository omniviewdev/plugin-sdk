package exec_test

import (
	"bytes"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
)

// OnFirstRead is the honest trigger of Started for a command that is quiet: client-go reads the stdin it was handed only once its
// streams exist, so the first Read of the reader is the one observation of "the stream is up" that the library offers.

// readerFunc is an io.Reader made of a function.
type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// The reader that OnFirstRead returns reads what the reader it wraps reads, untouched, and fn is not called before there is a Read.
func TestOnFirstReadPassesTheReadsThroughUntouchedAndWaitsForTheFirstOne(t *testing.T) {
	var calls atomic.Int32
	wrapped := exec.OnFirstRead(bytes.NewReader([]byte("hello")), func() { calls.Add(1) })
	if calls.Load() != 0 {
		t.Fatal("fn was called when the reader was made, and nobody had read")
	}
	buf := make([]byte, 3)
	n, err := wrapped.Read(buf)
	if n != 3 || err != nil || string(buf[:n]) != "hel" {
		t.Errorf("first read: %d bytes %q and %v, want hel and nil", n, buf[:n], err)
	}
	n, err = wrapped.Read(buf)
	if n != 2 || err != nil || string(buf[:n]) != "lo" {
		t.Errorf("second read: %d bytes %q and %v, want lo and nil", n, buf[:n], err)
	}
	if n, err = wrapped.Read(buf); n != 0 || err != io.EOF {
		t.Errorf("third read: %d bytes and %v, want 0 and io.EOF: the end of the reader reaches the reader of the executor", n, err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("fn was called %d times by three reads, want once", got)
	}
}

// fn is called by the first Read and before that Read reaches the reader it wraps, so that what the first read may find (an input
// already there, an end) comes after the report of the start, which is a report of "the stream is up", not "the stream is up and
// has data".
func TestOnFirstReadCallsFnBeforeItReadsTheReaderItWraps(t *testing.T) {
	var called, readAfterFn atomic.Bool
	wrapped := exec.OnFirstRead(readerFunc(func([]byte) (int, error) {
		readAfterFn.Store(called.Load())
		return 0, io.EOF
	}), func() { called.Store(true) })
	if _, err := wrapped.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("read: %v", err)
	}
	if !readAfterFn.Load() {
		t.Error("the wrapped reader was read before fn was called")
	}
}

// A read that fn is slow to return from does not hold another read: the reader of an executor may be read from two goroutines (a
// copy and a close), and a report of the start that takes its time must not stop the bytes.
func TestOnFirstReadNeverBlocksAReadBehindFn(t *testing.T) {
	entered, release := make(chan struct{}), exectest.NewGate()
	t.Cleanup(release.Open)
	wrapped := exec.OnFirstRead(readerFunc(func(p []byte) (int, error) { p[0] = 'z'; return 1, nil }), func() {
		close(entered)
		<-release.Opened()
	})
	first := make(chan struct{})
	go func() {
		defer close(first)
		_, _ = wrapped.Read(make([]byte, 1))
	}()
	awaitChannel(t, entered, "fn to be running")

	second := make(chan struct{})
	var n int
	go func() {
		defer close(second)
		n, _ = wrapped.Read(make([]byte, 1))
	}()
	select {
	case <-second:
		if n != 1 {
			t.Errorf("the second read returned %d bytes, want 1", n)
		}
	case <-time.After(engineWait):
		t.Fatal("a read waited for fn, which was still running: the first Read holds the readers behind it")
	}
	release.Open()
	awaitChannel(t, first, "the first read to return")
}

// Readers that start together run fn once: the first to ask runs it and the others do not wait for it and do not run it again.
func TestOnFirstReadRunsFnOnceWhateverTheReadersDo(t *testing.T) {
	const readers = 64
	var calls atomic.Int32
	wrapped := exec.OnFirstRead(readerFunc(func(p []byte) (int, error) { p[0] = 'z'; return 1, nil }), func() { calls.Add(1) })
	start, returned := make(chan struct{}), make(chan struct{}, readers)
	for range readers {
		go func() {
			<-start
			_, _ = wrapped.Read(make([]byte, 1))
			returned <- struct{}{}
		}()
	}
	close(start)
	for range readers {
		select {
		case <-returned:
		case <-time.After(engineWait):
			t.Fatal("a reader did not return")
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("fn was called %d times by %d concurrent readers, want once", got, readers)
	}
}

// A command without an input has no reader to wrap: an executor reads a nil Stdin as "no input" and opens no stream for it, so the
// reader that OnFirstRead returns is nil too, and there is no first read to wait for (such a command has the status or its first
// byte for its start).
func TestOnFirstReadOfNoReaderIsNoReader(t *testing.T) {
	var calls atomic.Int32
	if got := exec.OnFirstRead(nil, func() { calls.Add(1) }); got != nil {
		t.Errorf("OnFirstRead(nil, fn) = %#v, want a nil reader: an executor would otherwise open a stream for an input that is not there", got)
	}
	if calls.Load() != 0 {
		t.Error("fn was called for no reader")
	}
}

// Without a function there is nothing to call: the reader is the one given.
func TestOnFirstReadWithoutFnIsTheReaderGiven(t *testing.T) {
	r := bytes.NewReader([]byte("x"))
	if got := exec.OnFirstRead(r, nil); got != io.Reader(r) {
		t.Errorf("OnFirstRead(r, nil) = %#v, want r itself", got)
	}
}
