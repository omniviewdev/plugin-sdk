package exec_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// An attach is a request that takes a moment to reach the session: the handler of its stream reads the open frame, finds the session, and
// registers the stream, and between the first and the last another attach can register, and the host can give up on the stream. The
// ledger's host cancels attaches (cancelledAttach) and attaches again at once, and on a runner with few processors the handler of the
// cancelled one was late enough to register after the next attach and take the session from it: the stream that the host held was told
// Detached{SUPERSEDED} by an attach that the host had abandoned before it made the one it held (replay_ledger_test.go).
//
// These tests hold an attach in that window with the engine's seam (SetPluginBeforeAttach), in a bubble, with the streams in memory: the
// order of the registrations is the test's, and nothing waits for a clock or a scheduler. What they hold the engine to:
//
//   - an attach whose stream has ended when the engine would register it registers nothing: it takes the session from no one, and the
//     stream that the host holds keeps it;
//   - an attach that registers is the session's, the one that registers last, whatever the order in which the requests arrived: the older
//     stream is told Detached{SUPERSEDED}, and the host that attaches from what it has loses nothing and takes nothing twice;
//   - an attach that registered and whose stream then ended before Opened was sent is an attach that left: the session is detached for
//     its lease, and the host that attaches again from what it has loses nothing and takes nothing twice.

// lateRig is a session of the ledger's author, whose host assembles its output in a ledger, and a gate that holds the first attach on its
// way to the session.
type lateRig struct {
	t       *testing.T
	rig     *memoryRig
	author  *ledgerAuthor
	lg      *ledger
	streams []*memoryStream
	// release lets the held attach go on; arrived counts the attaches that reached the seam.
	release     chan struct{}
	releaseOnce sync.Once
	arrived     atomic.Int32
}

// newLateRig builds the rig; the caller is inside the bubble. Whatever the test does, when it ends every stream is cancelled, the held
// attach goes on and the plugin is stopped, so that a test that fails fails with its message and not with the bubble's deadlock.
func newLateRig(t *testing.T) *lateRig {
	t.Helper()
	author := newLedgerAuthor(31, false, true)
	l := &lateRig{
		t: t, rig: newMemoryRig(t, engTarget(author.run)), author: author,
		lg: newLedger(true, false, false, ledgerRing), release: make(chan struct{}),
	}
	exec.SetPluginBeforeAttach(l.rig.plugin, func(context.Context, string, uint64) {
		if l.arrived.Add(1) == 1 {
			<-l.release
		}
	})
	t.Cleanup(func() {
		l.let()
		for _, s := range l.streams {
			s.cancel()
			s.hostLeaves()
		}
		_ = l.rig.plugin.Shutdown(context.Background())
	})
	return l
}

// let lets the held attach go on.
func (l *lateRig) let() { l.releaseOnce.Do(func() { close(l.release) }) }

// track remembers a stream of the host's, which the rig lets go when the test ends.
func (l *lateRig) track(s *memoryStream) *memoryStream {
	l.streams = append(l.streams, s)
	return s
}

// write lets the author write n more bytes, and waits until they are in the ring and every stream has been sent what it can be.
func (l *lateRig) write(n uint64) {
	l.t.Helper()
	l.author.allow(l.author.allowed.Load() + n)
	synctest.Wait()
}

// takeOpened is the Opened that a stream was sent first.
func takeOpened(t testing.TB, s *memoryStream) *execv2.Opened {
	t.Helper()
	select {
	case frame := <-s.out:
		if frame.GetOpened() == nil {
			t.Fatalf("the first frame of the stream is %v, want Opened", frame)
		}
		return frame.GetOpened()
	case err := <-s.returned:
		t.Fatalf("the open was refused: %v", err)
	}
	return nil
}

// takeInto applies what a stream has been sent to a ledger, and returns the last frame of the stream, or nil when it has not ended.
func (l *lateRig) takeInto(s *memoryStream, into *ledger) *execv2.ServerFrame {
	l.t.Helper()
	var last *execv2.ServerFrame
	for _, f := range s.frames() {
		if last != nil {
			l.t.Fatalf("a %s frame came after the last frame of the stream", frameKind(f))
		}
		if isLast(f) {
			last = f
			continue
		}
		into.frame(f)
	}
	if len(into.problems) > 0 {
		l.t.Fatalf("the ledger: %s", strings.Join(into.problems, "\n\t"))
	}
	return last
}

// take applies what a stream has been sent to the host's ledger.
func (l *lateRig) take(s *memoryStream) *execv2.ServerFrame {
	l.t.Helper()
	return l.takeInto(s, l.lg)
}

// start opens the session on a stream of the host's, and lets the author write n bytes, which the host takes.
func (l *lateRig) start(n uint64) *memoryStream {
	l.t.Helper()
	host := l.track(l.rig.streamOf(l.rig.startFrame("s1"), nil))
	l.lg.begin(takeOpened(l.t, host), 0)
	l.write(n)
	if last := l.take(host); last != nil {
		l.t.Fatalf("the stream that started the session ended with %v", last)
	}
	return host
}

// leave asks the session to let the stream go and takes what the stream was sent up to Detached, which has to answer the request.
func (l *lateRig) leave(s *memoryStream) {
	l.t.Helper()
	s.sendDetachFrame(0)
	synctest.Wait()
	if last := l.take(s); last.GetDetached().GetReason() != execv2.DetachReason_DETACH_REASON_REQUESTED {
		l.t.Fatalf("the answer to Detach is %v, want Detached{REQUESTED}", last)
	}
}

// attachFromHave makes an attach from what the host holds, and returns its stream with the offset asked for. The handler is called and
// has run as far as it can when this returns: a held one is in the seam.
func (l *lateRig) attachFromHave() (*memoryStream, uint64) {
	l.t.Helper()
	asked := l.lg.have
	s := l.track(l.rig.streamOf(l.rig.attachFrame("s1", asked), nil))
	synctest.Wait()
	return s, asked
}

// adopt makes the stream the host's: its Opened starts the ledger's stream.
func (l *lateRig) adopt(s *memoryStream, asked uint64) {
	l.t.Helper()
	l.lg.begin(takeOpened(l.t, s), asked)
	if len(l.lg.problems) > 0 {
		l.t.Fatalf("the ledger: %s", strings.Join(l.lg.problems, "\n\t"))
	}
	l.take(s)
}

// closeAndFinish ends the session on the stream the host holds and holds the host's assembly to the author's output.
func (l *lateRig) closeAndFinish(s *memoryStream) {
	l.t.Helper()
	l.lg.closed = true
	s.sendCloseFrame(execv2.CloseMode_CLOSE_MODE_GRACEFUL)
	synctest.Wait()
	last := l.take(s)
	if last.GetEnded() == nil {
		l.t.Fatalf("the stream that closed the session ended with %v, want Ended", last)
	}
	select {
	case <-l.author.done:
	default:
		l.t.Fatal("the author's function has not returned after the end of the session")
	}
	ended := last.GetEnded()
	l.lg.finish(ended.GetLastOffset(), l.author.sum, l.author.written.Load())
	if len(l.lg.problems) > 0 {
		l.t.Fatalf("the output the host assembled is not the author's: %s", strings.Join(l.lg.problems, "\n\t"))
	}
	if l.lg.bytesIn != l.author.written.Load() {
		l.t.Fatalf("the host holds %d bytes, the author wrote %d", l.lg.bytesIn, l.author.written.Load())
	}
}

// cancelled reports whether an error is the end of a stream that its host cancelled: the status of a cancelled stream, or the context's own
// error, which is what a send on a stream in memory returns.
func cancelled(err error) bool {
	return errors.Is(err, context.Canceled) || status.Code(err) == codes.Canceled
}

// An attach that the host cancelled does not take the session from the stream that the host attached after it, however late the handler
// of the cancelled one runs: the engine registers an attach only while its stream has not ended, and decides that in the step that
// registers it, so the order of the two registrations cannot take the session from a stream that the host holds to one that it left.
func TestAnAttachWhoseStreamEndedBeforeTheEngineRegisteredItTakesTheSessionFromNoOneWhateverTheOrderOfTheHandlers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLateRig(t)
		first := l.start(300_000)
		l.leave(first)
		l.write(150_000) // while the host is away

		// The host attaches, and gives up at once: the handler of that attach is late, held with the open frame read and nothing registered.
		late, _ := l.attachFromHave()
		if _, returned := late.handlerReturned(); returned {
			t.Fatal("the handler of the late attach returned: it was to be held before it registered the stream")
		}
		if l.rig.sessionInfo(t, "s1").GetAttached() {
			t.Fatal("the session is attached before any attach registered")
		}

		// The host attaches again, from what it has, and holds that stream: it registers, and is sent its replay and then the live stream.
		held, asked := l.attachFromHave()
		l.adopt(held, asked)
		l.write(100_000)
		if last := l.take(held); last != nil {
			t.Fatalf("the stream the host holds ended with %v before the late attach was let go", last)
		}

		// The host's cancel of the late attach reached the plugin, and the handler goes on at last.
		late.cancel()
		l.let()
		synctest.Wait()

		if last := l.take(held); last != nil {
			t.Fatalf("the stream the host holds ended with %v: an attach whose stream had ended, and that the host made before this one, took the session from it", last)
		}
		if info := l.rig.sessionInfo(t, "s1"); !info.GetAttached() || info.GetLeaseExpiresUnixMs() != 0 {
			t.Fatalf("the session is %v after the late attach, want it attached to the stream the host holds, with no lease", info)
		}
		err, returned := late.handlerReturned()
		if !returned || !cancelled(err) {
			t.Fatalf("the handler of the cancelled attach returned %v (returned %v), want it to return the status of a cancelled stream", err, returned)
		}
		if frames := late.frames(); len(frames) != 0 {
			t.Fatalf("the cancelled attach was sent %v: an attach that is refused is a status with no frame", frames)
		}

		// The session is the held stream's, and goes on with it: what the author writes next arrives on it, and the host ends the session
		// there.
		l.write(50_000)
		if last := l.take(held); last != nil {
			t.Fatalf("the stream the host holds ended with %v", last)
		}
		l.closeAndFinish(held)
	})
}

// Attaches register in the order the engine takes them, whatever the order of their requests: an attach that registers last has the
// session, and the stream that was attached before it is told Detached{SUPERSEDED}, with no lease. The host that made both from the same
// offset holds the later registration's stream, and what the older one was sent is not the host's: nothing is lost and nothing is taken
// twice.
func TestTheAttachThatRegistersLastHasTheSessionWhateverTheOrderOfItsRequestAndTheOlderStreamIsToldSuperseded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLateRig(t)
		first := l.start(300_000)
		l.leave(first)
		l.write(150_000)

		// The request that arrives first is held; the one that arrives second registers first.
		slow, asked := l.attachFromHave()
		quick, _ := l.attachFromHave()
		shadow := newLedger(true, false, false, ledgerRing)
		shadow.begin(takeOpened(t, quick), asked)
		l.takeInto(quick, shadow)
		l.write(70_000)
		if last := l.takeInto(quick, shadow); last != nil {
			t.Fatalf("the stream that registered first ended with %v before the other attach registered", last)
		}

		l.let()
		synctest.Wait()

		// The stream that registered first was taken over: its last frame says so, with no lease, and nothing follows it.
		last := l.takeInto(quick, shadow)
		if d := last.GetDetached(); d == nil || d.GetReason() != execv2.DetachReason_DETACH_REASON_SUPERSEDED || d.GetLeaseExpiresUnixMs() != 0 {
			t.Fatalf("the stream that registered first ended with %v, want Detached{SUPERSEDED} with no lease", last)
		}
		// The stream that registered last is the session's, from the offset the host asked for: the host holds it.
		l.adopt(slow, asked)
		l.write(50_000)
		if last := l.take(slow); last != nil {
			t.Fatalf("the stream that registered last ended with %v", last)
		}
		if info := l.rig.sessionInfo(t, "s1"); !info.GetAttached() || info.GetLeaseExpiresUnixMs() != 0 {
			t.Fatalf("the session is %v, want it attached to the stream that registered last", info)
		}
		l.closeAndFinish(slow)
	})
}

// An attach that registered and whose stream ended before Opened could be sent is an attach that left: it took the session from the stream
// that was attached, and its leaving is the implicit detach of any stream that breaks, so the session lives on for its lease with no stream.
// The host that attaches again from what it holds (the stream that was taken over told it of every byte it had before the end of its
// frames) loses nothing and takes nothing twice.
func TestAnAttachThatRegisteredAndWhoseStreamEndedBeforeItsOpenedTookTheSessionAndLeftItDetachedForItsLease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLateRig(t)
		first := l.start(300_000)
		l.leave(first)
		l.write(150_000)

		// The first attach is held in the seam, and then registered with its Opened held in the send: its stream ends there.
		openedGate := make(chan struct{})
		late := l.track(l.rig.streamOpenedHeld(l.rig.attachFrame("s1", l.lg.have), openedGate))
		synctest.Wait()
		held, asked := l.attachFromHave()
		l.adopt(held, asked)
		l.write(100_000)
		if last := l.take(held); last != nil {
			t.Fatalf("the stream the host holds ended with %v before the other attach registered", last)
		}

		l.let()
		synctest.Wait() // the late attach has registered, and its Opened is held
		late.cancel()
		synctest.Wait()

		// The stream the host held was told that another attach took the session, and the other attach left at once.
		last := l.take(held)
		if d := last.GetDetached(); d == nil || d.GetReason() != execv2.DetachReason_DETACH_REASON_SUPERSEDED || d.GetLeaseExpiresUnixMs() != 0 {
			t.Fatalf("the stream the host held ended with %v, want Detached{SUPERSEDED} with no lease: the attach that registered after it took the session", last)
		}
		if err, returned := late.handlerReturned(); !returned || !cancelled(err) {
			t.Fatalf("the handler of the attach whose stream ended returned %v (returned %v), want the status of a cancelled stream", err, returned)
		}
		wantLease := bubbleNow() + 5*60*1000
		if info := l.rig.sessionInfo(t, "s1"); info.GetAttached() || info.GetLeaseExpiresUnixMs() != wantLease {
			t.Fatalf("the session is %v, want it detached for its own lease of five minutes (%d): the attach that took it left", info, wantLease)
		}

		// The host attaches again from what it holds: the replay starts where its last frame ended, and the session goes on.
		again, asked := l.attachFromHave()
		l.adopt(again, asked)
		l.write(50_000)
		if last := l.take(again); last != nil {
			t.Fatalf("the stream the host attached again ended with %v", last)
		}
		l.closeAndFinish(again)
	})
}

// The in-process engine attaches as the served Open does, and a call whose context has ended is a stream that has ended: it is refused as
// cancelled (or as timed out, for a deadline that has passed) and registers nothing, so the stream that holds the session keeps it. An attach
// whose context is alive takes the session from it, as before.
func TestAnInProcessAttachWhoseContextEndedIsRefusedAndTakesTheSessionFromNoOne(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ended func(parent context.Context) context.Context
		want  exec.ErrorClass
	}{
		{"cancelled", func(parent context.Context) context.Context {
			ctx, cancel := context.WithCancel(parent)
			cancel()
			return ctx
		}, exec.ErrorClassCancelled},
		{"past its deadline", func(parent context.Context) context.Context {
			ctx, cancel := context.WithDeadline(parent, time.Now().Add(-time.Second))
			t.Cleanup(cancel)
			return ctx
		}, exec.ErrorClassTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newHSRig(t, exechook.EngineConfig{})
			cmd := r.open("s1")
			held := cmd.Stream()

			_, err := r.eng.Attach(tc.ended(r.request()), attachRequest("s1", 0))
			var refusal *exec.Error
			if !errors.As(err, &refusal) || refusal.Class != tc.want {
				t.Fatalf("an attach whose context had ended returned %v, want it refused as %v", err, tc.want)
			}
			if fin := held.Final(); fin.Detached {
				t.Fatalf("the stream that held the session was told %+v: an attach whose context had ended took the session from it", fin)
			}

			next := r.attach("s1", 0)
			if fin := held.Final(); !fin.Detached || fin.Reason != execv2.DetachReason_DETACH_REASON_SUPERSEDED {
				t.Fatalf("after an attach whose context is alive the stream that held the session was told %+v, want Detached{SUPERSEDED}", fin)
			}
			if fin := next.Final(); fin.Detached {
				t.Fatalf("the stream that attached with a live context was told %+v", fin)
			}
		})
	}
}
