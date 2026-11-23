package exec_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// patternByte is the byte a function writes at an offset of its output: a reader that knows the offset of each byte it
// reads verifies every one, with no copy of what was written.
func patternByte(offset uint64) byte { return byte(offset*131 + offset>>7) }

// writePattern is a function that writes total bytes of the pattern in writes of changing sizes and ends with exit 0.
func writePattern(total int) exec.ExecFunc {
	return func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		buf := make([]byte, 40000)
		for offset, size := 0, 1; offset < total; size = size%37000 + 997 {
			n := min(size, total-offset)
			for i := range n {
				buf[i] = patternByte(uint64(offset + i))
			}
			if _, err := p.Stdout.Write(buf[:n]); err != nil {
				return exec.Exit{}, err
			}
			offset += n
		}
		return exec.ExitCode(0), nil
	}
}

// verifyPattern reads the command to its end and checks every byte against the pattern at its offset, every range that
// was dropped against the hole it leaves, and the end-state accounting: what was read plus what was dropped is what was
// written, with no byte twice. It returns the bytes read and dropped.
func verifyPattern(t testing.TB, cmd exechook.Command, written uint64) (read, dropped uint64) {
	t.Helper()
	buf := make([]byte, 8192)
	ctx, cancel := context.WithTimeout(context.Background(), engineWait)
	defer cancel()
	var next uint64
	for {
		chunk, err := cmd.Read(ctx, buf)
		for _, d := range chunk.Dropped {
			if d.From != next {
				t.Fatalf("a dropped range starts at %d, the reader is at %d: a hole that was not reported", d.From, next)
			}
			next += d.Bytes
			dropped += d.Bytes
		}
		if chunk.N > 0 {
			if chunk.Offset != next {
				t.Fatalf("a chunk at offset %d, the reader is at %d", chunk.Offset, next)
			}
			for i := range chunk.N {
				if buf[i] != patternByte(chunk.Offset+uint64(i)) {
					t.Fatalf("the byte at offset %d is %d, not what was written there", chunk.Offset+uint64(i), buf[i])
				}
			}
			next += uint64(chunk.N)
			read += uint64(chunk.N)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
	}
	if next != written || read+dropped != written {
		t.Fatalf("read %d and dropped %d of %d written (the reader ended at %d): read + dropped must be written", read, dropped, written, next)
	}
	return read, dropped
}

func TestOutputArrivesInTheOrderItWasWrittenWithItsOffsetsAndStreams(t *testing.T) {
	t.Run("without a terminal", func(t *testing.T) {
		rig := newEngineRig(t, engineRigConfig{}, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
			_, _ = p.Stdout.Write([]byte("a"))
			_, _ = p.Stderr.Write([]byte("bb"))
			_, _ = p.Stdout.Write([]byte("c"))
			_, _ = p.Stdout.Write([]byte("d"))
			return exec.ExitCode(0), nil
		}))
		cmd := rig.mustOpen("s1", withoutTTY)
		res := awaitDone(t, cmd)
		out := drain(t, cmd)
		want := []struct {
			stream execv2.Stream
			offset uint64
			data   string
		}{{execv2.Stream_STREAM_STDOUT, 0, "a"}, {execv2.Stream_STREAM_STDERR, 1, "bb"}, {execv2.Stream_STREAM_STDOUT, 3, "cd"}}
		if len(out.chunks) != len(want) {
			t.Fatalf("%d chunks, want %d: %+v", len(out.chunks), len(want), out.chunks)
		}
		pos := 0
		for i, w := range want {
			c := out.chunks[i]
			if c.Stream != w.stream || c.Offset != w.offset || string(out.data[pos:pos+c.N]) != w.data {
				t.Errorf("chunk %d: %v at %d %q, want %v at %d %q", i, c.Stream, c.Offset, out.data[pos:pos+c.N], w.stream, w.offset, w.data)
			}
			pos += c.N
		}
		if res.BytesOut != 5 || res.End.GetExited() == nil {
			t.Errorf("bytes out %d, end %v", res.BytesOut, res.End)
		}
	})
	t.Run("with a terminal", func(t *testing.T) {
		rig := newEngineRig(t, engineRigConfig{}, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
			_, _ = p.Stdout.Write([]byte("tty"))
			return exec.ExitCode(0), nil
		}))
		cmd := rig.mustOpen("s1")
		awaitDone(t, cmd)
		out := drain(t, cmd)
		if len(out.chunks) != 1 || out.chunks[0].Stream != execv2.Stream_STREAM_TTY || string(out.data) != "tty" {
			t.Errorf("chunks %+v data %q, want one chunk of the terminal stream", out.chunks, out.data)
		}
	})
}

func TestInputReachesTheFunctionInOrderAndTheHostsEOFEndsIt(t *testing.T) {
	rig := newEngineRig(t, engineRigConfig{}, exectest.NewFakeTarget(exectest.FakeScript(exectest.CopyInput())))
	cmd := rig.mustOpen("s1")
	for _, part := range []string{"abc", "def", "ghi"} {
		if n, err := cmd.Write(context.Background(), []byte(part)); n != 3 || err != nil {
			t.Fatalf("write %q: %d, %v", part, n, err)
		}
	}
	cmd.CloseInput()
	res := awaitDone(t, cmd)
	if got := drain(t, cmd).data; string(got) != "abcdefghi" {
		t.Errorf("the echo is %q", got)
	}
	assertEnd(t, res.End, exited(0, byProcess, new(false)))
	if res.BytesIn != 9 || res.BytesOut != 9 {
		t.Errorf("bytes in %d out %d, want 9 and 9", res.BytesIn, res.BytesOut)
	}
	if _, err := cmd.Write(context.Background(), []byte("late")); !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("input after the session ended: %v, want io.ErrClosedPipe", err)
	}
}

// A function that does not read its input holds the host's Write at the bound, which is the backpressure the host sees:
// the host's write returns what was queued and the error when the session ends under it.
func TestAHostThatSendsMoreThanTheFunctionReadsIsHeldAtTheBoundUntilTheSessionEnds(t *testing.T) {
	rig := newEngineRig(t, engineRigConfig{}, engTarget(blockUntilCancelled))
	cmd := rig.mustOpen("s1")
	const bound = 256 << 10
	type result struct {
		n   int
		err error
	}
	written := make(chan result, 1)
	go func() {
		n, err := cmd.Write(context.Background(), bytes.Repeat([]byte("x"), 2*bound))
		written <- result{n, err}
	}()
	// The function never reads, so the write cannot complete: it ends only when the session does.
	cmd.Close(false)
	r := <-written
	awaitDone(t, cmd)
	if !errors.Is(r.err, io.ErrClosedPipe) || r.n > bound {
		t.Fatalf("the host's write: %d, %v, want io.ErrClosedPipe and at most the %d bytes the queue holds", r.n, r.err, bound)
	}
}

func TestAFunctionBlockedReadingItsInputReturnsWhenTheSessionEnds(t *testing.T) {
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		_, err := p.Stdin.Read(make([]byte, 1)) // ignores its context: only the end of the session can release it
		return exec.Exit{}, err
	}))
	cmd := rig.mustOpen("s1")
	cmd.Close(false)
	res := awaitDone(t, cmd)
	if res.Abandoned {
		t.Error("a function blocked reading stdin was given up on: the end of the session releases its read")
	}
	assertEnd(t, res.End, closedBy(execv2.ClosedBy_CLOSED_BY_REQUEST, new(false)))
}

// ---------------------------------------------------------------------------------------------------------------
// resize
// ---------------------------------------------------------------------------------------------------------------

func TestAResizeIsAcceptedAndTheFunctionTakesTheInitialSizeFirstAndThenTheNewest(t *testing.T) {
	const storm = 2000
	taken := make(chan exec.Size, 8)
	release := exectest.NewGate()
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		select {
		case <-release.Opened(): // the function does not read its sizes until the test lets it
		case <-ctx.Done():
		}
		for {
			select {
			case size, ok := <-p.Resize:
				if !ok {
					return exec.ExitCode(0), nil
				}
				taken <- size
			case <-ctx.Done():
				return exec.Exit{}, ctx.Err()
			}
		}
	}))
	cmd := rig.mustOpen("s1", withSize(100, 30))
	for seq := uint32(1); seq <= storm; seq++ {
		if got := cmd.Resize(seq, 100+uint16(seq%50), 30); got != exechook.ResizeAccepted {
			t.Fatalf("resize %d: %v, want accepted", seq, got)
		}
	}
	if _, _, _, ok := cmd.AppliedResize(); ok {
		t.Fatal("a size is reported as taken by a function that has read nothing")
	}
	release.Open()

	var got []exec.Size
	for {
		size := <-taken
		got = append(got, size)
		if size == (exec.Size{Cols: 100 + storm%50, Rows: 30}) && len(got) > 1 {
			break
		}
		if len(got) > 3 {
			t.Fatalf("%d sizes were delivered for one storm: %v", len(got), got)
		}
	}
	if got[0] != (exec.Size{Cols: 100, Rows: 30}) {
		t.Errorf("the first size %v is not the size the command was opened with", got[0])
	}
	if len(got) > 3 {
		t.Errorf("%d sizes for one storm, want the initial size, at most one in flight and the newest", len(got))
	}
	eventually(t, "the newest size to be reported as taken", func() bool {
		seq, cols, rows, ok := cmd.AppliedResize()
		return ok && seq == storm && cols == 100+storm%50 && rows == 30
	})
	cmd.Close(false)
	awaitDone(t, cmd)
}

func TestAResizeThatCannotApplyIsIgnoredRefusedOrTooLate(t *testing.T) {
	t.Run("a target that cannot resize", func(t *testing.T) {
		rig := newEngineRig(t, engineRigConfig{}, engTarget(blockUntilCancelled, func(i *exec.TargetInfo) { i.Capabilities.Resize = false }))
		cmd := rig.mustOpen("s1")
		if got := cmd.Resize(1, 100, 30); got != exechook.ResizeIgnored {
			t.Errorf("resize on a target that cannot: %v, want ignored", got)
		}
		cmd.Close(false)
		awaitDone(t, cmd)
	})
	t.Run("a command without a terminal", func(t *testing.T) {
		rig := newEngineRig(t, engineRigConfig{}, engTarget(blockUntilCancelled))
		cmd := rig.mustOpen("s1", withoutTTY)
		if got := cmd.Resize(1, 100, 30); got != exechook.ResizeIgnored {
			t.Errorf("resize without a terminal: %v, want ignored", got)
		}
		cmd.Close(false)
		awaitDone(t, cmd)
	})
	t.Run("a size that is zero or above the limit", func(t *testing.T) {
		rig := newEngineRig(t, engineRigConfig{}, engTarget(blockUntilCancelled))
		cmd := rig.mustOpen("s1")
		for _, size := range [][2]uint16{{0, 30}, {100, 0}, {0, 0}, {1025, 30}, {100, 4096}} {
			if got := cmd.Resize(1, size[0], size[1]); got != exechook.ResizeInvalid {
				t.Errorf("resize to %v x %v: %v, want invalid", size[0], size[1], got)
			}
		}
		cmd.Close(false)
		awaitDone(t, cmd)
	})
	t.Run("a command that ended", func(t *testing.T) {
		rig := newEngineRig(t, engineRigConfig{}, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) { return exec.ExitCode(0), nil }))
		cmd := rig.mustOpen("s1")
		awaitDone(t, cmd)
		if got := cmd.Resize(1, 100, 30); got != exechook.ResizeClosed {
			t.Errorf("resize after the end: %v, want closed", got)
		}
	})
}

// ---------------------------------------------------------------------------------------------------------------
// the output queue's two rules
// ---------------------------------------------------------------------------------------------------------------

// A target whose stalled reader slows the process blocks the function's Write at the bound and loses nothing, however slowly
// the host reads (conformance case 4, at the engine level).
func TestABlockingTargetDeliversEveryByteToAReaderThatTakesItsTime(t *testing.T) {
	const total = 3 << 20
	rig := newEngineRig(t, engineRigConfig{}, engTarget(writePattern(total), func(i *exec.TargetInfo) {
		i.Capabilities.Stall = exec.StallPropagates
	}))
	cmd := rig.mustOpen("s1")
	read, dropped := verifyPattern(t, cmd, total)
	res := awaitDone(t, cmd)
	if read != total || dropped != 0 || res.DroppedBytes != 0 || res.Output != complete || res.BytesOut != total {
		t.Fatalf("read %d dropped %d: result %+v, want every byte delivered and the output complete", read, dropped, res)
	}
	assertEnd(t, res.End, exited(0, byProcess, new(false)))
}

// A target that a stalled reader would harm is never held by the host: its function finishes while nobody reads, the
// queue kept the newest bytes and counted the rest, the reader is told exactly what it lost, and the end says the output was
// truncated (conformance case 18, at the engine level).
func TestALossyTargetIsNeverHeldByAReaderThatDoesNotReadAndCountsWhatItLost(t *testing.T) {
	const total = 3 << 20
	rig := newEngineRig(t, engineRigConfig{}, engTarget(writePattern(total), func(i *exec.TargetInfo) {
		i.Capabilities.Stall = exec.StallLossy
	}))
	cmd := rig.mustOpen("s1")
	res := awaitDone(t, cmd) // nobody has read a byte, and the function still ran to its end
	if res.DroppedBytes == 0 || res.Output != truncated || res.BytesOut != total {
		t.Fatalf("result %+v, want bytes dropped, the output truncated and all %d bytes counted as written", res, total)
	}
	assertEnd(t, res.End, exited(0, byProcess, new(false)))
	read, dropped := verifyPattern(t, cmd, total)
	if dropped != res.DroppedBytes || read+dropped != total {
		t.Fatalf("read %d dropped %d, the end says %d dropped of %d written", read, dropped, res.DroppedBytes, total)
	}
	if read > 1<<20 {
		t.Fatalf("read %d bytes, more than the ring of 1 MiB holds", read)
	}
}

// A session that is ending never waits for a reader: a function blocked in Write because its reader has left the most it may
// leave unread (256 KiB) is released by the end, returns by itself within the grace, and what the ring cannot hold is counted, not
// silently lost. (The ring holds the bytes a reader has read as well as the ones it has not, 1 MiB by default, so the write that
// is released here is larger than the ring.)
func TestAnEndingSessionNeverWaitsForAReaderAndCountsWhatItDropped(t *testing.T) {
	filled, more := exectest.NewGate(), exectest.NewGate()
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		chunk := make([]byte, 64<<10)
		for range 4 { // a reader may leave 256 KiB unread: this reaches it
			if _, err := p.Stdout.Write(chunk); err != nil {
				return exec.Exit{}, err
			}
		}
		filled.Open()
		<-more.Opened()
		_, err := p.Stdout.Write(make([]byte, 2<<20)) // blocks: nobody reads, and the reader has left all it may
		return exec.Exit{}, err
	}, func(i *exec.TargetInfo) { i.Capabilities.Stall = exec.StallPropagates }))
	cmd := rig.mustOpen("s1")
	awaitChannel(t, filled.Opened(), "the queue to fill")
	more.Open()
	cmd.Close(false) // the write is blocked, or about to block: either way only the end can release it
	res := awaitDone(t, cmd)
	assertEnd(t, res.End, closedBy(execv2.ClosedBy_CLOSED_BY_REQUEST, new(false)))
	if res.Abandoned {
		t.Error("the function was given up on: the end of the session should have released its write")
	}
	if res.DroppedBytes == 0 || res.Output != truncated {
		t.Errorf("dropped %d output %v: the bytes the full queue could not hold are counted and the output is truncated", res.DroppedBytes, res.Output)
	}
}

// Two goroutines of one function write stdout and stderr at once: nothing is lost or reordered within a stream, and the
// offsets are one contiguous sequence.
func TestTwoWritersOfOneFunctionShareOneOrderedOutputSequence(t *testing.T) {
	const each = 600 << 10
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		done := make(chan error, 2)
		for _, w := range []io.Writer{p.Stdout, p.Stderr} {
			go func() {
				_, err := w.Write(bytes.Repeat([]byte{'x'}, each))
				done <- err
			}()
		}
		for range 2 {
			if err := <-done; err != nil {
				return exec.Exit{}, err
			}
		}
		return exec.ExitCode(0), nil
	}, func(i *exec.TargetInfo) { i.Capabilities.Stall = exec.StallPropagates }))
	cmd := rig.mustOpen("s1", withoutTTY)
	counts := map[execv2.Stream]int{}
	var next uint64
	buf := make([]byte, 5000)
	for {
		chunk, err := cmd.Read(context.Background(), buf)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if chunk.Offset != next {
			t.Fatalf("a chunk at offset %d, want %d", chunk.Offset, next)
		}
		next += uint64(chunk.N)
		counts[chunk.Stream] += chunk.N
	}
	if counts[execv2.Stream_STREAM_STDOUT] != each || counts[execv2.Stream_STREAM_STDERR] != each {
		t.Fatalf("stdout %d stderr %d bytes arrived, want %d each", counts[execv2.Stream_STREAM_STDOUT], counts[execv2.Stream_STREAM_STDERR], each)
	}
	awaitDone(t, cmd)
}
