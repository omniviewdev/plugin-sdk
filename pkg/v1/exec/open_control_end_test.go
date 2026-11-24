package exec_test

import (
	"context"
	"fmt"
	"io"
	"testing"
	"testing/synctest"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// What the host sends reaches the function in the order it sent it, and a frame that the SDK accepted is never discarded by the SDK at
// the end of the session: it is in the function's queue (Proc.Signals, Proc.Queries, the unread bytes of Proc.Stdin, the size Proc.Resize
// still holds) before the function's context is cancelled and before its channel is closed, and it is still there to read afterwards. A
// function that reads until its channel is closed, or its input reports io.EOF, finds everything that was accepted before the end, in
// order; one that stops reading when its context ends may not read it, which is its choice and not the SDK's. A frame that comes after the
// end is refused. These tests hold the function before its first read, so that the frames arrive and the session ends first, and then let
// it read: what it finds is what the SDK kept, whatever the scheduler does.

// A frame of a kind, and how a function that reads until the end reads it.
type endKind struct {
	name string
	// sent are the frames the host sends before the session ends: the SDK accepts each of them.
	sent []*execv2.ClientFrame
	// after is a frame that comes once the session has ended and is refused; nil for a kind that has none (an input frame that follows
	// the host's EOF is the host breaking the protocol, which is not a refusal of the end).
	after *execv2.ClientFrame
	// read is what the function finds of the frames when it reads to the end of its channel, or of its input, after the session ended.
	// giveUp is opened when the test is over, so that a build whose channel is not closed fails the test and does not deadlock the bubble.
	read func(p *exec.Proc, giveUp <-chan struct{}) string
	// want is what it finds, and wantGivenUp what it finds when the SDK gave up on it before it read (it ignored its cancelled context
	// for the grace or the kill wait): every queue keeps what it holds, and the size is the one thing the SDK stops delivering then.
	want, wantGivenUp string
}

// receiveAll reads a channel until it is closed, or until giveUp is.
func receiveAll[T any](ch <-chan T, giveUp <-chan struct{}) []T {
	var got []T
	for {
		select {
		case v, open := <-ch:
			if !open {
				return got
			}
			got = append(got, v)
		case <-giveUp:
			return got
		}
	}
}

func endKinds() []endKind {
	signals := "[INT TERM INT]"
	return []endKind{
		{
			name:  "signals",
			sent:  []*execv2.ClientFrame{ctlSignal(execv2.Signal_SIGNAL_INT), ctlSignal(execv2.Signal_SIGNAL_TERM), ctlSignal(execv2.Signal_SIGNAL_INT)},
			after: ctlSignal(execv2.Signal_SIGNAL_KILL),
			read: func(p *exec.Proc, giveUp <-chan struct{}) string {
				return fmt.Sprint(receiveAll(p.Signals, giveUp))
			},
			want: signals, wantGivenUp: signals,
		},
		{
			name:  "a resize",
			sent:  []*execv2.ClientFrame{ctlResize(1, 100, 30), ctlResize(2, 120, 40)},
			after: ctlResize(3, 150, 50),
			read: func(p *exec.Proc, giveUp <-chan struct{}) string {
				var got []string
				for _, size := range receiveAll(p.Resize, giveUp) {
					got = append(got, fmt.Sprintf("%dx%d", size.Cols, size.Rows))
				}
				return fmt.Sprint(got)
			},
			// The size the session was opened with, and the newest the host sent (it replaced the one before it, as every size does).
			want: "[80x24 120x40]", wantGivenUp: "[]",
		},
		{
			name:  "input",
			sent:  []*execv2.ClientFrame{ctlInput("ab"), ctlInput("cd")},
			after: ctlInput("ef"),
			read:  readInput,
			want:  `"abcd"`, wantGivenUp: `"abcd"`,
		},
		{
			name: "input and the host's EOF",
			sent: []*execv2.ClientFrame{ctlInput("ab"), ctlInput("cd"), ctlCloseInput()},
			read: readInput,
			want: `"abcd"`, wantGivenUp: `"abcd"`,
		},
		{
			name:  "queries",
			sent:  []*execv2.ClientFrame{ctlQuery(1, execv2.FactKind_FACT_KIND_CWD), ctlQuery(2, execv2.FactKind_FACT_KIND_FOREGROUND)},
			after: ctlQuery(3, execv2.FactKind_FACT_KIND_CWD),
			read: func(p *exec.Proc, giveUp <-chan struct{}) string {
				var got []string
				for _, q := range receiveAll(p.Queries, giveUp) {
					got = append(got, fmt.Sprintf("%v answerable %v", q.Kind, q.Answer != nil))
				}
				return fmt.Sprint(got)
			},
			want: "[CWD answerable true FOREGROUND answerable true]", wantGivenUp: "[CWD answerable true FOREGROUND answerable true]",
		},
	}
}

// readInput reads the function's input to io.EOF.
func readInput(p *exec.Proc, _ <-chan struct{}) string {
	data, err := io.ReadAll(p.Stdin)
	if err != nil {
		return fmt.Sprintf("%q and the error %v", data, err)
	}
	return fmt.Sprintf("%q", data)
}

// A way the session ends, which the host's frames precede.
type endPath struct {
	name string
	// end ends the session once the host's frames have been applied, and returns what has to be true when the function has finished.
	end func(t *testing.T, rig *memoryRig, host *memoryStream) (finished func(t *testing.T))
	// afterEnd reports whether a host's frame can still come once the session has ended: a stream whose host broke the protocol is
	// over, and a session that no stream holds is not sent frames.
	afterEnd bool
	// told reports whether the host has been told the session ended, which a host that is attached reads as an Ended and one that is
	// not attached reads in the tombstone; a host that broke the protocol is told by the status of its stream and is never sent Ended.
	told func(t *testing.T, rig *memoryRig, host *memoryStream) bool
}

// endedFrameSent is told for a stream that is attached to its session to its end.
func endedFrameSent(_ *testing.T, _ *memoryRig, host *memoryStream) bool {
	return ctlEnded(host.ctlFrames()) != nil
}

func hostCloses(mode execv2.CloseMode) endPath {
	return endPath{
		name: "the host sends a close " + mode.String(),
		end: func(_ *testing.T, _ *memoryRig, host *memoryStream) func(*testing.T) {
			host.ctlSend(ctlClose(mode))
			return func(t *testing.T) {
				if ended := ctlEnded(host.ctlFrames()); ended.GetEnd().GetClosed().GetBy() != execv2.ClosedBy_CLOSED_BY_REQUEST {
					t.Errorf("Ended %v, want closed by request", ended)
				}
			}
		},
		afterEnd: true,
		told:     endedFrameSent,
	}
}

// endSessionOfADetachedSession is the host's EndSession for a session that no stream holds: the stream leaves, and another call ends the
// session. The call returns once the session has ended, so it is made from a goroutine of the bubble while the function is held.
func endSessionOfADetachedSession(mode execv2.CloseMode) endPath {
	return endPath{
		name: "EndSession " + mode.String() + " of a detached session",
		end: func(t *testing.T, rig *memoryRig, host *memoryStream) func(*testing.T) {
			host.ctlSend(detachFrame(0))
			synctest.Wait()
			if returned, err := host.ctlStatus(); !returned || err != nil {
				t.Fatalf("the stream that detached ended with %v (returned %v), want a clean end", err, returned)
			}
			ended := make(chan error, 1)
			go func() { ended <- rig.endSession("s1", mode) }()
			return func(t *testing.T) {
				select {
				case err := <-ended:
					if err != nil {
						t.Errorf("EndSession: %v", err)
					}
				default:
					t.Error("EndSession did not return once the session had ended")
				}
				if by := rig.sessionInfo(t, "s1").GetEndedWith().GetEnd().GetClosed().GetBy(); by != execv2.ClosedBy_CLOSED_BY_REQUEST {
					t.Errorf("the tombstone says the session ended %v, want closed by request", by)
				}
			}
		},
		told: func(t *testing.T, rig *memoryRig, _ *memoryStream) bool {
			return rig.sessionInfo(t, "s1").GetEndedWith() != nil
		},
	}
}

func endPaths() []endPath {
	return []endPath{
		{
			name: "the host sends a signal the session does not declare",
			end: func(t *testing.T, _ *memoryRig, host *memoryStream) func(*testing.T) {
				host.ctlSend(ctlSignal(execv2.Signal(99)))
				synctest.Wait()
				returned, err := host.ctlStatus()
				if class, _ := ctlClass(err); !returned || class != "UNSUPPORTED" {
					t.Fatalf("the stream ended with %v (returned %v), want UNSUPPORTED", err, returned)
				}
				return func(t *testing.T) {
					if ended := ctlEnded(host.ctlFrames()); ended != nil {
						t.Errorf("a stream that broke the protocol was sent an Ended: %v", ended)
					}
				}
			},
			told: func(*testing.T, *memoryRig, *memoryStream) bool { return false },
		},
		hostCloses(execv2.CloseMode_CLOSE_MODE_GRACEFUL),
		hostCloses(execv2.CloseMode_CLOSE_MODE_KILL),
		endSessionOfADetachedSession(execv2.CloseMode_CLOSE_MODE_GRACEFUL),
		endSessionOfADetachedSession(execv2.CloseMode_CLOSE_MODE_KILL),
	}
}

// When the function reads, relative to the end of the session.
type readWhen struct {
	name string
	// held is true for a function that waits at a gate until the test lets it go, and false for one that waits for its context.
	held bool
	// givenUp is true when the test lets the function go only after the SDK gave up on it: the grace and the kill wait are over.
	givenUp bool
}

var readWhens = []readWhen{
	{"a function woken by its cancelled context reads", false, false},
	{"a function held until the session ended reads", true, false},
	{"a function the SDK gave up on reads", true, true},
}

// readAfterTheEnd is what the function found, and whether its context had ended when it read.
type readAfterTheEnd struct {
	ended bool
	what  string
}

// A frame the SDK accepted is still there to read when the session has ended, for every kind of frame and every way the session ends, and
// the host is told the end only after the function was given what it was sent.
func TestWhatTheHostSentBeforeASessionEndedIsStillThereToReadAfterIt(t *testing.T) {
	for _, kind := range endKinds() {
		t.Run(kind.name, func(t *testing.T) {
			for _, path := range endPaths() {
				t.Run(path.name, func(t *testing.T) {
					for _, when := range readWhens {
						t.Run(when.name, func(t *testing.T) {
							synctest.Test(t, func(t *testing.T) {
								gate, giveUp, found := ctlGate(t), ctlGate(t), make(chan readAfterTheEnd, 1)
								rig := newMemoryRig(t, ctlTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
									if when.held {
										<-gate.Opened()
									} else {
										<-ctx.Done()
									}
									found <- readAfterTheEnd{ended: ctx.Err() != nil, what: kind.read(p, giveUp.Opened())}
									return exec.Exit{}, ctx.Err()
								}, func(c *exec.Capabilities) { c.CwdFact, c.ForegroundFact = exec.FactModePull, exec.FactModePull }))
								host := rig.ctlSession("s1", 80, 24)
								host.ctlSend(kind.sent...)
								synctest.Wait() // every frame was applied, and the function has read none of them
								finished := path.end(t, rig, host)
								synctest.Wait() // the session has ended
								if when.held {
									if len(found) != 0 {
										t.Fatal("the function read before it was let go")
									}
									if path.told(t, rig, host) {
										t.Fatal("the host was told the session ended while the function had not been given what it sent")
									}
									if when.givenUp {
										time.Sleep(10 * time.Second) // past the grace (5 s) and the kill wait (50 ms), and well inside the tombstone's minute
										synctest.Wait()
									}
									gate.Open()
									synctest.Wait()
								}
								var got readAfterTheEnd
								select {
								case got = <-found:
								default:
									t.Fatal("the function did not reach the end of what the host sent: its channel was not closed, or its input did not end, with the session")
								}
								if !got.ended {
									t.Error("the function read before its context ended: the session had not ended")
								}
								want := kind.want
								if when.givenUp {
									want = kind.wantGivenUp
								}
								if got.what != want {
									t.Errorf("the function read %s, want %s: what the host sent before the end is never discarded", got.what, want)
								}
								finished(t)
							})
						})
					}
				})
			}
		})
	}
}

// The input that was queued when the function returned is read by a goroutine the function left behind, and then io.EOF: as for every
// read of Proc.Stdin after the end, the SDK discards none of what the host sent, and holds no reader (the queue is bounded, and a read
// that finds it empty ends). A late write is another matter and is refused, as the tests of the late writers say.
func TestAGoroutineTheFunctionLeftBehindReadsTheInputQueuedWhenItReturnedAndThenEOF(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		returnNow, late, read := ctlGate(t), ctlGate(t), make(chan string, 1)
		rig := newMemoryRig(t, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
			go func() {
				<-late.Opened()
				data, _ := io.ReadAll(p.Stdin)
				read <- string(data)
			}()
			<-returnNow.Opened() // the function returns while the host's input is still queued
			return exec.ExitCode(0), nil
		}))
		host := rig.ctlPlainSession("s1")
		host.ctlSend(ctlInput("abc"))
		synctest.Wait()
		returnNow.Open()
		synctest.Wait()
		if ctlEnded(host.ctlFrames()) == nil {
			t.Fatal("the session did not end when its function returned")
		}
		late.Open()
		synctest.Wait()
		select {
		case got := <-read:
			if got != "abc" {
				t.Errorf("the goroutine read %q, want the input that was queued when the function returned", got)
			}
		default:
			t.Fatal("the goroutine did not reach the end of its input")
		}
	})
}

// A frame that comes once the session has ended is refused: it is not queued, so that the function never reads it, and a query is
// answered at once, unknown, as the SDK answers one it cannot hand over.
func TestAFrameThatComesAfterTheEndOfTheSessionIsRefusedAndNeverReachesTheFunction(t *testing.T) {
	for _, kind := range endKinds() {
		if kind.after == nil {
			continue
		}
		t.Run(kind.name, func(t *testing.T) {
			for _, path := range endPaths() {
				if !path.afterEnd {
					continue
				}
				t.Run(path.name, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						gate, giveUp, found := ctlGate(t), ctlGate(t), make(chan readAfterTheEnd, 1)
						rig := newMemoryRig(t, ctlTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
							<-gate.Opened()
							found <- readAfterTheEnd{ended: ctx.Err() != nil, what: kind.read(p, giveUp.Opened())}
							return exec.Exit{}, ctx.Err()
						}, func(c *exec.Capabilities) { c.CwdFact, c.ForegroundFact = exec.FactModePull, exec.FactModePull }))
						host := rig.ctlSession("s1", 80, 24)
						host.ctlSend(kind.sent...)
						synctest.Wait()
						finished := path.end(t, rig, host)
						synctest.Wait()
						host.ctlSend(kind.after)
						synctest.Wait()
						if kind.after.GetQuery() != nil {
							facts := ctlFacts(host.ctlFrames())
							if len(facts) != 1 || facts[0].GetQueryId() != 3 || facts[0].GetCwd().GetReason() != execv2.UnknownReason_UNKNOWN_REASON_QUERY_FAILED {
								t.Errorf("a query that came after the end was answered %v, want unknown at once, with QUERY_FAILED", facts)
							}
						}
						gate.Open()
						synctest.Wait()
						var got readAfterTheEnd
						select {
						case got = <-found:
						default:
							t.Fatal("the function did not reach the end of what the host sent")
						}
						if got.what != kind.want {
							t.Errorf("the function read %s, want %s: a frame that came after the end reaches nobody", got.what, kind.want)
						}
						finished(t)
					})
				})
			}
		})
	}
}

// Proc.Resize is closed when the session ends, once the function has taken the size the host sent last, so that a goroutine that ranges
// over it returns with the rest: the end of a graceful close is not held for the function's grace by a function that waits for the channel.
func TestProcResizeClosesWhenTheSessionEndsAndAGoroutineThatRangesOverItReturnsWithTheRest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		giveUp := ctlGate(t) // ends the reader of a build whose channel does not close, so that its failure is not a deadlock
		sizes, closed := make(chan exec.Size, 8), make(chan struct{})
		rig := newMemoryRig(t, ctlTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			readerDone := make(chan struct{})
			go func() {
				defer close(readerDone)
				for {
					select {
					case size, open := <-p.Resize:
						if !open {
							close(closed)
							return
						}
						sizes <- size
					case <-giveUp.Opened():
						return
					}
				}
			}()
			<-ctx.Done()
			<-readerDone // a function that waits for its goroutines, as a function should
			return exec.Exit{}, ctx.Err()
		}))
		host := rig.ctlSession("s1", 80, 24)
		synctest.Wait()
		host.ctlSend(ctlResize(1, 100, 30))
		synctest.Wait()
		start := time.Now()
		host.ctlSend(ctlClose(execv2.CloseMode_CLOSE_MODE_GRACEFUL))
		synctest.Wait()
		select {
		case <-closed:
		default:
			t.Fatal("Proc.Resize was not closed when the session ended")
		}
		if took := time.Since(start); took != 0 {
			t.Errorf("the graceful close took %v: the function waited for its grace for a channel that closes with the session", took)
		}
		close(sizes)
		var got []exec.Size
		for size := range sizes {
			got = append(got, size)
		}
		if fmt.Sprint(got) != "[{80 24} {100 30}]" {
			t.Errorf("the goroutine was handed %v, want the size the session was opened with and the one the host sent", got)
		}
		if ended := ctlEnded(host.ctlFrames()); ended.GetEnd().GetClosed().GetBy() != execv2.ClosedBy_CLOSED_BY_REQUEST {
			t.Errorf("Ended %v, want closed by request", ended)
		}
	})
}
