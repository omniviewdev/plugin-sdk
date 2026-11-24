package exec_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The sessions that outlive their stream are tested over the wire, with the stack a host and a plugin really use, and on a clock
// that the test moves: the lease of a detached session and the age of a tombstone are decided by fakeClock and fakeTimers
// (engine_timers_test.go), never by sleeping. This file holds what these tests share: the rig, the host's side of an attach, and
// the transcript that a host assembles of one session from the frames of all its streams.

// sessionRig is a wire rig whose engine runs on a clock and lease timers the test drives.
type sessionRig struct {
	*wireRig
	clock  *fakeClock
	leases *fakeTimers
}

// newSessionRig serves targets over the wire with the engine's clock and lease timers replaced, and its tombstone settings changed
// as cfg says (the zero value is the default).
func newSessionRig(t testing.TB, cfg exechook.EngineConfig, targets ...exec.Target) *sessionRig {
	t.Helper()
	rig := newWireRig(t, targets...)
	s := &sessionRig{wireRig: rig, clock: newFakeClock(), leases: newFakeTimers()}
	exec.SetPluginEngineSeams(rig.server.Plugin, exec.EngineSeams{Now: s.clock.Now, NewLeaseTimer: s.leases.New})
	exec.ConfigurePluginEngine(rig.server.Plugin, cfg)
	return s
}

// attach attaches a stream to a session: the first frame of an Open that is an attach from an offset.
func (r *wireRig) attach(ctx context.Context, id string, from uint64) (exec.Conn, error) {
	return r.client.Open(r.request(ctx), &execv2.OpenRequest{SessionId: id, Kind: &execv2.OpenRequest_Attach{Attach: &execv2.Attach{FromOffset: from}}})
}

// mustAttach attaches a stream that has to be accepted, and closes it when the test ends.
func (r *wireRig) mustAttach(id string, from uint64) exec.Conn {
	r.t.Helper()
	conn, err := r.attach(context.Background(), id, from)
	if err != nil {
		r.t.Fatalf("attach to %q from %d: %v", id, from, err)
	}
	r.t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// sendDetach asks to leave the session for a lease.
func sendDetach(t testing.TB, conn exec.Conn, leaseMs uint32) {
	t.Helper()
	if err := conn.Send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Detach{Detach: &execv2.Detach{LeaseMs: leaseMs}}}); err != nil {
		t.Fatalf("send detach: %v", err)
	}
}

// readFramesUntil reads frames until one satisfies done, and returns them all, that one last. The stream ending first fails the test.
func readFramesUntil(t testing.TB, conn exec.Conn, what string, done func(*execv2.ServerFrame) bool) []*execv2.ServerFrame {
	t.Helper()
	var frames []*execv2.ServerFrame
	for {
		frame, err := recvFrame(t, conn)
		if err != nil {
			t.Fatalf("waiting for %s: the stream ended after %d frames with %v", what, len(frames), err)
		}
		frames = append(frames, frame)
		if done(frame) {
			return frames
		}
	}
}

// readBytes reads output until n more bytes have arrived, which have to be the bytes of the pattern from offset, and returns the
// frames. It fails on a stream that ends, on a frame that is not output, a notice or a started, and on a byte that is wrong.
func readPatternBytes(t testing.TB, conn exec.Conn, offset uint64, n int) []*execv2.ServerFrame {
	t.Helper()
	var frames []*execv2.ServerFrame
	for got := 0; got < n; {
		frame, err := recvFrame(t, conn)
		if err != nil {
			t.Fatalf("waiting for %d more bytes of output at %d: %v", n-got, offset+uint64(got), err)
		}
		frames = append(frames, frame)
		switch f := frame.GetFrame().(type) {
		case *execv2.ServerFrame_Output:
			o := f.Output
			if o.GetOffset() != offset+uint64(got) {
				t.Fatalf("an Output frame at offset %d, want %d", o.GetOffset(), offset+uint64(got))
			}
			if want := proofChunk(o.GetOffset(), len(o.GetData())); string(o.GetData()) != string(want) {
				t.Fatalf("the bytes of the Output frame at %d are not the pattern", o.GetOffset())
			}
			got += len(o.GetData())
		case *execv2.ServerFrame_Started, *execv2.ServerFrame_Notice, *execv2.ServerFrame_Link:
		default:
			t.Fatalf("a %s frame while waiting for output", frameKind(frame))
		}
	}
	return frames
}

// drainToEnd reads the stream to its end, which has to be io.EOF, and returns every frame.
func drainToEnd(t testing.TB, conn exec.Conn) []*execv2.ServerFrame {
	t.Helper()
	var frames []*execv2.ServerFrame
	for {
		frame, err := recvFrame(t, conn)
		if errors.Is(err, io.EOF) {
			return frames
		}
		if err != nil {
			t.Fatalf("the stream broke after %d frames: %v", len(frames), err)
		}
		frames = append(frames, frame)
	}
}

// lastFrame is the last frame of a stream, and fails when there is none.
func lastFrame(t testing.TB, frames []*execv2.ServerFrame) *execv2.ServerFrame {
	t.Helper()
	if len(frames) == 0 {
		t.Fatal("the stream ended with no frame")
	}
	return frames[len(frames)-1]
}

// endsWith holds a stream to its grammar: exactly one last frame, Ended or Detached, and nothing of either before it. It returns
// the last frame.
func endsWith(t testing.TB, frames []*execv2.ServerFrame) *execv2.ServerFrame {
	t.Helper()
	last := lastFrame(t, frames)
	for i, f := range frames[:len(frames)-1] {
		if f.GetEnded() != nil || f.GetDetached() != nil {
			t.Fatalf("frame %d of %d is %s: nothing follows Ended or Detached", i, len(frames), frameKind(f))
		}
	}
	if last.GetEnded() == nil && last.GetDetached() == nil {
		t.Fatalf("the stream's last frame is %s, want Ended or Detached", frameKind(last))
	}
	return last
}

// gatedPattern is a function that writes the pattern of the proofs in the pieces a test grants it (a grant is a number of bytes),
// and ends with exit 0 when the grants are closed. It reports how far it has written. A function that is told to stop ends with
// its context's error.
func gatedPattern(grants <-chan int, written *atomic.Uint64) exec.ExecFunc {
	return func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		var offset uint64
		for {
			select {
			case n, ok := <-grants:
				if !ok {
					return exec.ExitCode(0), nil
				}
				for n > 0 {
					piece := min(n, 32<<10)
					if _, err := p.Stdout.Write(proofChunk(offset, piece)); err != nil {
						return exec.Exit{}, err
					}
					offset += uint64(piece)
					n -= piece
					written.Store(offset)
				}
			case <-ctx.Done():
				return exec.Exit{}, ctx.Err()
			}
		}
	}
}

// transcript is what a host assembles of one session from the frames of every stream it had, by offset. A stream starts at
// the offset its replay says; the bytes of every Output frame are held to the pattern; a gap is allowed only where the plugin
// said so (a replay that starts after the offset the host asked for, a Dropped frame); and no byte is taken twice.
type transcript struct {
	t       testing.TB
	next    uint64 // the offset the next frame of the stream in hand has to be at
	have    uint64 // every byte below it was received or reported lost: the offset to attach from
	read    uint64 // bytes of Output frames
	lost    uint64 // bytes reported lost, by Dropped frames and by replays that started later than asked
	frames  int
	digest  hash.Hash // over the bytes received, in order: equal to the pattern's when nothing was lost
	problem []string
}

func newTranscript(t testing.TB) *transcript {
	return &transcript{t: t, digest: sha256.New()}
}

func (tr *transcript) fail(format string, args ...any) {
	if len(tr.problem) < 8 {
		tr.problem = append(tr.problem, fmt.Sprintf(format, args...))
	}
}

// attached starts a stream: Opened says where its replay starts, and a start after the offset the host asked for is a gap that the
// plugin reported.
func (tr *transcript) attached(opened *execv2.Opened, asked uint64) {
	r := opened.GetReplay()
	if r.GetBytes() != r.GetEnd()-r.GetStart() {
		tr.fail("replay bytes %d, want end %d - start %d", r.GetBytes(), r.GetEnd(), r.GetStart())
	}
	switch {
	case r.GetStart() < asked:
		tr.fail("the replay starts at %d, before the offset %d the host asked for: a byte would be taken twice", r.GetStart(), asked)
	case r.GetStart() > asked:
		tr.lost += r.GetStart() - asked
	}
	tr.next, tr.have = r.GetStart(), r.GetStart()
}

// take applies the next frame of the stream in hand.
func (tr *transcript) take(frame *execv2.ServerFrame) {
	switch f := frame.GetFrame().(type) {
	case *execv2.ServerFrame_Output:
		o := f.Output
		tr.frames++
		if o.GetOffset() != tr.next {
			tr.fail("an Output frame at offset %d, the stream is at %d: a hole that was not reported, or a byte taken twice", o.GetOffset(), tr.next)
			return
		}
		if len(o.GetData()) == 0 || len(o.GetData()) > proofMaxFrame {
			tr.fail("an Output frame of %d bytes at %d", len(o.GetData()), o.GetOffset())
			return
		}
		if string(o.GetData()) != string(proofChunk(o.GetOffset(), len(o.GetData()))) {
			tr.fail("the bytes of the Output frame at %d are not the pattern", o.GetOffset())
		}
		tr.digest.Write(o.GetData())
		tr.next += uint64(len(o.GetData()))
		tr.have, tr.read = tr.next, tr.read+uint64(len(o.GetData()))
	case *execv2.ServerFrame_Dropped:
		d := f.Dropped
		if d.GetFromOffset() != tr.next || d.GetBytes() == 0 {
			tr.fail("a Dropped range of %d bytes from %d, the stream is at %d", d.GetBytes(), d.GetFromOffset(), tr.next)
			return
		}
		tr.next += d.GetBytes()
		tr.have, tr.lost = tr.next, tr.lost+d.GetBytes()
	}
}

// check fails the test with what went wrong.
func (tr *transcript) check() {
	tr.t.Helper()
	for _, p := range tr.problem {
		tr.t.Errorf("transcript: %s", p)
	}
}

// leaseExpiry is when the lease of a Detached frame ends, as a time.
func leaseExpiry(d *execv2.Detached) time.Time { return time.UnixMilli(d.GetLeaseExpiresUnixMs()) }

// isRefusalOf reports whether err is a refusal of the exec lane with the class and reason.
func isRefusalOf(err error, class exec.ErrorClass, reason exec.ErrorReason) bool {
	var refusal *exec.Error
	return errors.As(err, &refusal) && refusal.Class == class && refusal.Reason == reason
}
