package exec_test

import (
	"context"
	"fmt"
	"io"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The control frames (resize, signal, close_input, query) and what they earn (Resized, Fact) when a session outlives its stream: a
// stream detaches, is superseded or breaks, and another attaches. What a frame earns belongs to the stream that sent it, a frame of a
// stream the session has left takes no effect, and the stream that attaches learns the state of the session from Opened, never from
// what the stream before it was owed. The streams are in memory and the clock is the bubble's, so what the server does without a
// goroutine running has been done when synctest.Wait returns, and no test decides anything by waiting.

// openedOf returns the Opened of a stream's frames, which has to be the first.
func openedOf(t testing.TB, frames []*execv2.ServerFrame) *execv2.Opened {
	t.Helper()
	if len(frames) == 0 || frames[0].GetOpened() == nil {
		t.Fatalf("the first frame of the stream is %d frames long and does not start with Opened", len(frames))
	}
	return frames[0].GetOpened()
}

// wind ends a test of streams in memory: every host leaves and the plugin shuts down, so that no goroutine is left in the bubble.
func (r *memoryRig) wind(t testing.TB, streams ...*memoryStream) {
	t.Helper()
	for _, s := range streams {
		s.hostLeaves()
	}
	if err := r.plugin.Shutdown(context.Background()); err != nil {
		t.Errorf("shut the plugin down: %v", err)
	}
}

// attachOf attaches a stream to a session from its start and waits until the server has applied the frame.
func (r *memoryRig) attachOf(id string) *memoryStream {
	r.t.Helper()
	s := r.streamOf(r.attachFrame(id, 0), nil)
	synctest.Wait()
	return s
}

// A host leaves the stream it holds in one of the three ways a stream leaves a session that lives on; the next host takes the session.
var streamLeaves = []struct {
	name string
	// takeOver is what happens to the stream a and what the next host does to attach: it returns the stream that attached.
	takeOver func(rig *memoryRig, a *memoryStream) *memoryStream
}{
	{"the next stream's attach supersedes it", func(rig *memoryRig, _ *memoryStream) *memoryStream { return rig.attachOf("s1") }},
	{"it detaches and the next stream attaches", func(rig *memoryRig, a *memoryStream) *memoryStream {
		a.ctlSend(detachFrame(0))
		synctest.Wait()
		return rig.attachOf("s1")
	}},
	{"it is cancelled, which is an implicit detach, and the next stream attaches", func(rig *memoryRig, a *memoryStream) *memoryStream {
		a.cancel()
		synctest.Wait()
		return rig.attachOf("s1")
	}},
}

// A size that a stream sent and the function has not taken when another stream attaches is taken later, and its ack is the old stream's
// and is dropped with it: the new stream is told nothing of a size it never sent. It learns the size in force from the replay in
// Opened (the size now, and the size the first byte of the replay was produced for), and its own resize, which may carry the same seq
// as the old stream's, is acknowledged to it, once, with its own size.
func TestTheAckOfAResizeGoesToTheStreamThatSentItAndAStreamThatAttachesLearnsTheSizeFromTheReplay(t *testing.T) {
	for _, leave := range streamLeaves {
		t.Run(leave.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				gate, sizes := ctlGate(t), make(chan exec.Size, 16)
				rig := newMemoryRig(t, ctlTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
					<-gate.Opened()
					for {
						select {
						case size, ok := <-p.Resize:
							if !ok {
								return exec.ExitCode(0), nil
							}
							sizes <- size
							_, _ = p.Stdout.Write([]byte{'.'}) // what the function writes after taking a size is laid out for it
						case <-ctx.Done():
							return exec.Exit{}, ctx.Err()
						}
					}
				}))
				a := rig.ctlSession("s1", 100, 30)
				a.ctlSend(ctlResize(7, 120, 40))
				synctest.Wait()
				if acks := ctlResized(a.ctlFrames()); len(acks) != 0 {
					t.Fatalf("an ack for a size the function has not taken: %v", acks)
				}
				b := leave.takeOver(rig, a)
				opened := openedOf(t, b.frames())
				if size := opened.GetReplay().GetSize(); size.GetCols() != 100 || size.GetRows() != 30 {
					t.Errorf("Opened says the size now is %v, want the one the session was opened with: the function has taken no other", size)
				}

				gate.Open()
				synctest.Wait()
				if got, want := drainSizes(sizes), []exec.Size{{Cols: 100, Rows: 30}, {Cols: 120, Rows: 40}}; !slices.Equal(got, want) {
					t.Errorf("the function was handed %v, want %v: the size the session was opened with and then the one the stream before sent", got, want)
				}
				if acks := ctlResized(b.frames()); len(acks) != 0 {
					t.Fatalf("the stream that attached was acknowledged %d resizes it never sent: %v", len(acks), acks)
				}

				b.ctlSend(ctlResize(7, 90, 25)) // the same seq as the old stream's
				synctest.Wait()
				acks := ctlResized(b.frames())
				if len(acks) != 1 {
					t.Fatalf("the stream that sent a resize was sent %d acks, want one", len(acks))
				}
				wantResized(t, acks[0], 7, 90, 25, execv2.ResizeResult_RESIZE_RESULT_APPLIED)

				c := rig.attachOf("s1")
				replay := openedOf(t, c.frames()).GetReplay()
				if size, start := replay.GetSize(), replay.GetStartSize(); size.GetCols() != 90 || size.GetRows() != 25 || start.GetCols() != 100 || start.GetRows() != 30 {
					t.Errorf("a later attach is told the size now %v and the size the replay started at %v, want 90x25 and 100x30", size, start)
				}
				if acks := ctlResized(c.frames()); len(acks) != 0 {
					t.Errorf("a stream that only attached was sent acks: %v", acks)
				}
				rig.wind(t, a, b, c)
			})
		})
	}
}

// A query that waits for the function when its stream leaves is answered to nobody: the function's answer, and the SDK's answer when
// the deadline passes, are the old stream's and are dropped with it, and never reach the stream that attaches, which may have asked a
// question with the same id. The places the queries held are free again once they are answered or expired.
func TestAQueryThatWaitsWhenItsStreamLeavesIsNeverAnsweredToTheNextStreamAndItsPlaceIsFree(t *testing.T) {
	ask := func(t *testing.T, rig *memoryRig, host *memoryStream, asked chan exec.Query, ids ...uint32) []exec.Query {
		t.Helper()
		for _, id := range ids {
			host.ctlSend(ctlQuery(id, execv2.FactKind_FACT_KIND_CWD))
		}
		synctest.Wait()
		return takeQueries(t, asked, len(ids))
	}
	for _, leave := range streamLeaves {
		t.Run(leave.name+", and the function answers late", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				asked := make(chan exec.Query, 32)
				rig := newMemoryRig(t, factTarget(queryCollector(asked), exec.FactModePull, exec.FactModePull))
				a := rig.ctlPlainSession("s1")
				old := ask(t, rig, a, asked, 5)
				b := leave.takeOver(rig, a)
				b.frames() // Opened
				mine := ask(t, rig, b, asked, 5)
				old[0].Answer(exec.Fact{Kind: exec.FactKindCwd, Cwd: "/old"})
				mine[0].Answer(exec.Fact{Kind: exec.FactKindCwd, Cwd: "/mine"})
				synctest.Wait()
				wantFacts(t, ctlFacts(b.frames()), cwdFact(5, "/mine", noReason))
				for _, f := range a.ctlFrames() {
					if f.GetFact().GetCwd().GetPath() == "/old" && lastOf(a.ctlFrames()) == f {
						t.Error("the answer came after the last frame of its stream")
					}
				}
				// Both places are free: eight more queries all reach the function.
				ids := []uint32{10, 11, 12, 13, 14, 15, 16, 17}
				more := ask(t, rig, b, asked, ids...)
				for _, q := range more {
					q.Answer(exec.Fact{Kind: exec.FactKindCwd, Cwd: "/x"})
				}
				synctest.Wait()
				if got := len(ctlFacts(b.frames())); got != len(ids) {
					t.Errorf("%d answers to the eight queries that followed, want %d", got, len(ids))
				}
				rig.wind(t, a, b)
			})
		})
		t.Run(leave.name+", and the deadline passes", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				asked := make(chan exec.Query, 32)
				rig := newMemoryRig(t, factTarget(queryCollector(asked), exec.FactModePull, exec.FactModePull))
				a := rig.ctlPlainSession("s1")
				ask(t, rig, a, asked, 5)
				b := leave.takeOver(rig, a)
				b.frames() // Opened
				time.Sleep(6 * time.Second)
				synctest.Wait()
				if facts := ctlFacts(b.frames()); len(facts) != 0 {
					t.Fatalf("the stream that attached was sent the answer to a query of the stream before it: %v", facts)
				}
				if more := ask(t, rig, b, asked, 1, 2, 3, 4, 5, 6, 7, 8); len(more) != 8 {
					t.Fatalf("%d queries reached the function, want eight: the place of the expired query is free", len(more))
				}
				rig.wind(t, a, b)
			})
		})
	}
}

// The input of a session stays closed when the stream that closed it leaves. What the function read before the EOF is what the old
// stream sent, the input of the stream that attaches is dropped as the input of a command that has ended is (it is not a violation of
// that stream, which was never told of the EOF), and it can end the session as any stream can.
func TestCloseInputThenDetachKeepsTheInputClosedAndTheNextStreamsInputIsDroppedAndNotAViolation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		read := make(chan []byte, 4)
		rig := newMemoryRig(t, ctlTarget(readAllThenHold(read)))
		a := rig.ctlPlainSession("s1")
		a.ctlSend(ctlInput("abc"), ctlCloseInput(), detachFrame(0))
		synctest.Wait()
		if d := lastOf(a.ctlFrames()).GetDetached(); d == nil || d.GetReason() != execv2.DetachReason_DETACH_REASON_REQUESTED {
			t.Fatalf("the stream that closed the input and detached did not end with Detached{REQUESTED}")
		}
		if got := string(<-read); got != "abc" {
			t.Fatalf("the function read %q before the EOF, want the bytes sent before close_input", got)
		}
		b := rig.attachOf("s1")
		b.frames() // Opened
		b.ctlSend(ctlInput("zzz"), ctlCloseInput(), ctlSignal(execv2.Signal_SIGNAL_INT))
		synctest.Wait()
		if returned, err := b.ctlStatus(); returned {
			t.Fatalf("the stream that attached ended with %v: its input after an EOF it never sent is dropped, not a violation", err)
		}
		select {
		case more := <-read:
			t.Fatalf("the function read %q after the EOF: the input of a session that is closed stays closed", more)
		default:
		}
		b.ctlSend(ctlClose(execv2.CloseMode_CLOSE_MODE_GRACEFUL))
		synctest.Wait()
		ended := ctlEnded(b.ctlFrames())
		if ended.GetEnd().GetClosed().GetBy() != execv2.ClosedBy_CLOSED_BY_REQUEST {
			t.Errorf("the stream that attached closed the session and was told %v", ended.GetEnd())
		}
		rig.wind(t, a, b)
	})
}

// A function that ends on the EOF of its input while its host is away ends its session in the tombstone: no stream is told Ended, and
// what it wrote, and how it ended, are read from ListSessions and ReadOutput.
func TestAFunctionThatEndsOnTheEOFOfAClosedInputWhileDetachedEndsTheSessionInTheTombstoneAndNoStreamIsToldEnded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := ctlGate(t)
		rig := newMemoryRig(t, ctlTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			<-gate.Opened()
			_, _ = io.Copy(p.Stdout, p.Stdin)
			return exec.ExitCode(0), nil
		}))
		a := rig.ctlPlainSession("s1")
		a.ctlSend(ctlInput("abc"), ctlCloseInput(), detachFrame(0))
		synctest.Wait()
		frames := a.ctlFrames()
		if d := lastOf(frames).GetDetached(); d == nil {
			t.Fatalf("the stream ended with %s, want Detached last", frameKind(lastOf(frames)))
		}
		if info := rig.sessionInfo(t, "s1"); info.GetEndedWith() != nil || info.GetAttached() {
			t.Fatalf("the session is %v before its function read the input, want detached and alive", info)
		}
		gate.Open()
		synctest.Wait()
		info := rig.sessionInfo(t, "s1")
		ended := info.GetEndedWith()
		if ended.GetEnd().GetExited() == nil || ended.GetEnd().GetExited().GetExitCode() != 0 || ended.GetBytesIn() != 3 || ended.GetBytesOut() != 3 || ended.GetLastOffset() != 3 {
			t.Fatalf("the tombstone says %v, want exited 0 with three bytes in and out", ended)
		}
		if data := chunkBytes(t, rig.mustReadOutput(t, "s1", 0, 0)); string(data) != "abc" {
			t.Errorf("the tombstone holds %q, want what the function wrote", data)
		}
		if more := a.ctlFrames(); len(more) != 0 {
			t.Errorf("the stream that left was sent %d frames after Detached, the end among them: %v", len(more), more)
		}
		rig.wind(t, a)
	})
}

// streamStartedHeld is a stream of a host that takes no Started frame until the test lets it: the goroutine that sends the session's
// events waits in the send, and what the host's frames earn waits behind it.
func (r *memoryRig) streamStartedHeld(first *execv2.ClientFrame, startedGate chan struct{}) *memoryStream {
	r.t.Helper()
	ctx, cancel := context.WithCancel(r.callContext())
	s := &memoryStream{
		ctx: ctx, cancel: cancel, startedGate: startedGate,
		in: make(chan *execv2.ClientFrame, 16), out: make(chan *execv2.ServerFrame, 256), returned: make(chan error, 1),
	}
	s.in <- first
	go func() { s.returned <- r.server.Open(s) }()
	return s
}

// What a stream's frames earned before it left is sent before its last frame, whichever of its goroutines comes first: the answer to a
// query that came before the Detach comes before Detached. The goroutine that sends the events is held while the frames are applied,
// so that when it is let go the answer is waiting at the same moment as the end of the stream, and which it takes first is its own
// choice (it chooses at random between them): it must send the same frames in either order, so each of the 24 sessions (the target
// serves 32 at once, and a detached session keeps its place) is one more throw of that choice.
func TestWhatAStreamsFramesEarnedBeforeItsDetachIsSentBeforeDetached(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, ctlTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			p.Started(exec.SessionFacts{})
			<-ctx.Done()
			return exec.Exit{}, ctx.Err()
		}))
		var streams []*memoryStream
		for i := range 24 {
			held := make(chan struct{})
			a := rig.streamStartedHeld(rig.ctlOpenFrame(fmt.Sprintf("s%d", i), false, 0, 0), held)
			streams = append(streams, a)
			synctest.Wait() // Opened is sent and the events are held in the send of Started
			a.ctlSend(ctlQuery(3, execv2.FactKind_FACT_KIND_INTEGRATION_TIER), detachFrame(0))
			synctest.Wait() // both frames are applied, and the stream has left
			close(held)
			synctest.Wait()
			frames := a.frames()
			if len(frames) != 4 || frames[0].GetOpened() == nil || frames[1].GetStarted() == nil || frames[2].GetFact().GetQueryId() != 3 || frames[3].GetDetached() == nil {
				kinds := make([]string, len(frames))
				for j, f := range frames {
					kinds[j] = frameKind(f)
				}
				t.Fatalf("session %d: the frames of the stream are %v, want Opened, Started, Fact{3} and Detached last", i, kinds)
			}
		}
		rig.wind(t, streams...)
	})
}

// The frames of a stream the session was taken from take no effect on it, whatever they are: not a size, a signal, an EOF, a query,
// input or a Close, and not a violation, which would end the session its successor holds. The stream is still being served (its host
// takes no output, so its last frame waits), which is when its receiver can still be asked to apply them. What the next stream
// sends is applied.
//
// The session's Started goes out before its first output is held in the send: the goroutine that sends the events and the pump send
// under one lock, so that an event which is still to be sent when the pump holds the lock in a send that takes nothing waits for the
// lock, and a goroutine that waits for a lock is not durably blocked, so that synctest.Wait would never return.
func TestTheFramesOfAStreamThatWasSupersededTakeNoEffectOnTheSessionItsSuccessorHolds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		seen := make(chan string, 64)
		proceed := make(chan struct{})
		rig := newMemoryRig(t, factTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			p.Started(exec.SessionFacts{})
			<-proceed
			_, _ = p.Stdout.Write([]byte("hello"))
			go func() {
				data, _ := io.ReadAll(p.Stdin)
				seen <- fmt.Sprintf("stdin ended after %q", data)
			}()
			sizes, signals, queries := p.Resize, p.Signals, p.Queries
			for {
				select {
				case size, ok := <-sizes:
					if !ok {
						sizes = nil
						continue
					}
					seen <- fmt.Sprintf("size %dx%d", size.Cols, size.Rows)
				case sig, ok := <-signals:
					if !ok {
						signals = nil
						continue
					}
					seen <- fmt.Sprintf("signal %v", sig)
				case q, ok := <-queries:
					if !ok {
						queries = nil
						continue
					}
					seen <- "query " + q.Kind.String()
				case <-ctx.Done():
					return exec.Exit{}, ctx.Err()
				}
			}
		}, exec.FactModePull, exec.FactModePull))
		outGate := make(chan struct{})
		a := rig.streamHeld(rig.ctlOpenFrame("s1", true, 100, 30), outGate)
		synctest.Wait() // Opened and Started are sent
		close(proceed)
		synctest.Wait() // the output is held in the send
		if got := <-seen; got != "size 100x30" {
			t.Fatalf("the function saw %q first, want the size the session was opened with", got)
		}
		b := rig.attachOf("s1")
		openedOf(t, b.frames())
		a.ctlSend(ctlResize(1, 50, 20), ctlSignal(execv2.Signal_SIGNAL_INT), ctlCloseInput(), ctlQuery(9, execv2.FactKind_FACT_KIND_CWD),
			ctlInput("stale"), ctlResize(2, 0, 0), ctlClose(execv2.CloseMode_CLOSE_MODE_KILL))
		synctest.Wait()
		select {
		case got := <-seen:
			t.Fatalf("the function saw %q: a frame of the stream the session was taken from was applied", got)
		default:
		}
		if !rig.live("s1") || ctlEnded(b.frames()) != nil {
			t.Fatal("the session was ended by a frame of the stream that it was taken from")
		}
		b.ctlSend(ctlSignal(execv2.Signal_SIGNAL_TERM))
		synctest.Wait()
		if got, want := <-seen, fmt.Sprintf("signal %v", exec.SignalTerm); got != want {
			t.Fatalf("the function saw %q after the signal of the stream that holds the session, want %q", got, want)
		}
		close(outGate)
		synctest.Wait()
		if d := lastOf(a.frames()).GetDetached(); d == nil || d.GetReason() != execv2.DetachReason_DETACH_REASON_SUPERSEDED {
			t.Error("the stream the session was taken from did not end with Detached{SUPERSEDED}")
		}
		rig.wind(t, a, b)
	})
}
