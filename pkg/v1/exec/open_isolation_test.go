package exec_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// Isolation (plan section 3.3 and 3.6): what one session does to another. One stream carries each session, so a session whose
// host is slow, absent or pasting, whose author never reads or ignores its context, must not move the echo of a quiet session on
// the same connection: the legacy lane's shared consumer put that echo at 431 to 469 ms at a consumer of 100 us a frame, and did
// not accept a keystroke at all in 3 s while another session was pasted into. The bar is p95 <= 2 ms; what is asserted is p95 <=
// 10 ms (50 ms under the race detector), and the numbers, with the baseline of the same echo alone, are logged for the PR.
const (
	isolationBar   = 2 * time.Millisecond
	isolationFloor = 10 * time.Millisecond
	// isolationConsumer is what the host of the flooded session spends on each frame, spent as CPU and not as sleep.
	isolationConsumer = 100 * time.Microsecond
	// isolationPasteBound is the most input a host's Send may get through before its transport blocks it, with a session whose
	// author never reads: the command's queue (256 KiB) and the windows of the transport, with a wide margin.
	isolationPasteBound = 64 << 20
)

// isolationAuthor is what the session that disturbs the echo does, and what it reports of itself.
type isolationAuthor struct {
	written atomic.Uint64 // bytes of the flood the session took
	gate    *exectest.Gate
}

// flood writes the pattern until the session ends. The lossy variant gives the scheduler back after each write, as an author
// that reads its stream between two writes does; the blocking one is held by its queue.
func (a *isolationAuthor) flood(yield bool) exec.ExecFunc {
	return func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		for offset := uint64(0); ; offset += floodChunk {
			if err := ctx.Err(); err != nil {
				return exec.Exit{}, err // an author honors its context: a write that is not refused would go on for the grace
			}
			if _, err := p.Stdout.Write(proofChunk(offset, floodChunk)); err != nil {
				return exec.Exit{}, err
			}
			a.written.Store(offset + floodChunk)
			if yield {
				runtime.Gosched()
			}
		}
	}
}

// settled polls the counter until it has not moved for as long as the longest pause of a running writer takes to show, and
// returns where it stopped. A writer that is held by a full queue stops at once and for good.
func settled(t *testing.T, what string, read func() uint64) uint64 {
	t.Helper()
	last, since := read(), time.Now()
	if !proofPoll(engineWait, func() bool {
		if now := read(); now != last {
			last, since = now, time.Now()
		}
		return time.Since(since) >= 50*time.Millisecond
	}) {
		t.Fatalf("%s did not settle", what)
	}
	return last
}

// isolationCase runs one session that disturbs the echo of another: start brings it to the state, and the check it returns is
// called after the echo was measured, to assert that the session was in the state all along, and to take it down.
func isolationCase(t *testing.T, name string, start func(t *testing.T, rig *proofRig, a *isolationAuthor) (check func())) {
	t.Run(name, func(t *testing.T) {
		a := &isolationAuthor{gate: exectest.NewGate()}
		rig := newProofRig(t,
			proofTarget("echo", proofEchoFunction),
			proofTarget("flood", a.flood(false)),
			proofTarget("lossy", a.flood(true), func(i *exec.TargetInfo) { i.Capabilities.Stall = exec.StallLossy }),
			proofTarget("sink", blockUntilCancelled),
			proofTarget("stuck", func(context.Context, *exec.Proc) (exec.Exit, error) { <-a.gate.Opened(); return exec.ExitCode(0), nil },
				func(i *exec.TargetInfo) { i.Session = exec.DefaultSessionLimits(); i.Session.MaxSessions = 256 }),
		)
		t.Cleanup(a.gate.Open) // registered after the rig, so it runs before the rig waits for the functions
		echo := newProofEchoer(rig.mustOpenKind("echo", "echo-b", proofTTY))
		samples := proofScaled(3000, 600)
		if _, err := echo.measure(200); err != nil {
			t.Fatalf("warm-up: %v", err)
		}
		alone, err := echo.measure(samples)
		if err != nil {
			t.Fatalf("the echo alone: %v", err)
		}
		check := start(t, rig, a)
		loaded, err := echo.measure(samples)
		if err != nil {
			t.Fatalf("the echo while the other session %s: %v", name, err)
		}
		check()
		baseline, under := proofSummarize(alone), proofSummarize(loaded)
		floor := isolationFloor
		if proofRace || runtime.GOMAXPROCS(0) < 4 {
			// The race detector, or one processor for the flood, its host, the echo and the transport together, which then
			// take turns: what is measured is the scheduler's slice and not the lane.
			floor = 5 * isolationFloor
		}
		t.Logf("echo of B alone:  %v", baseline)
		t.Logf("echo of B while A %s: %v (the plan's bar: p95 <= %v; floor %v)", name, under, isolationBar, floor)
		if under.P95 > floor {
			t.Errorf("the p95 of the echo is %v while the other session %s, the floor is %v (the plan's bar %v; alone: %v)",
				under.P95.Round(time.Microsecond), name, floor, isolationBar, baseline.P95.Round(time.Microsecond))
		}
	})
}

func TestOpenIsolationEchoWhileAnotherSessionFloodsAndItsHostIsSlow(t *testing.T) {
	isolationCase(t, "floods and its host spends 100us a frame", func(t *testing.T, rig *proofRig, a *isolationAuthor) func() {
		conn := rig.mustOpenKind("flood", "a")
		var frames atomic.Uint64
		done := make(chan struct{})
		audit := &proofAudit{}
		go func() {
			defer close(done)
			for {
				frame, err := conn.Recv()
				if err != nil {
					return
				}
				audit.take(frame)
				frames.Add(1)
				proofSpin(isolationConsumer)
			}
		}()
		eventually(t, "the slow consumer to read a first frames of the flood", func() bool { return frames.Load() >= 20 })
		before := frames.Load()
		return func() {
			if got := frames.Load(); got <= before {
				t.Errorf("the flooded session delivered no frame while the echo was measured: it was not flooding")
			}
			_ = conn.Close()
			awaitChannel(t, done, "the slow consumer to stop")
			for _, problem := range audit.problems {
				t.Errorf("the flooded stream: %s", problem)
			}
		}
	})
}

func TestOpenIsolationEchoWhileAnotherSessionIsPastedIntoAndItsAuthorNeverReads(t *testing.T) {
	isolationCase(t, "is pasted into and its author never reads", func(t *testing.T, rig *proofRig, a *isolationAuthor) func() {
		conn := rig.mustOpenKind("sink", "a")
		paste := &execv2.ClientFrame{Frame: &execv2.ClientFrame_Input{Input: proofChunk(0, floodChunk)}}
		var attempted, completed atomic.Uint64
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				attempted.Add(1)
				if conn.Send(paste) != nil {
					return
				}
				completed.Add(1)
			}
		}()
		stopped := settled(t, "the paste", completed.Load)
		return func() {
			if got := completed.Load(); got != stopped {
				t.Errorf("the host's Send got %d more frames through while the echo was measured: the author never reads, so the transport must hold it", got-stopped)
			}
			if got := attempted.Load(); got != completed.Load()+1 {
				t.Errorf("%d Sends began and %d completed: the host is not blocked in a Send", got, completed.Load())
			}
			if sent := stopped * floodChunk; sent > isolationPasteBound {
				t.Errorf("the host sent %d bytes before it was blocked, the bound is %d", sent, isolationPasteBound)
			} else {
				t.Logf("the host's Send blocked after %d frames of %d KiB: %d KiB", stopped, floodChunk>>10, stopped*floodChunk>>10)
			}
			_ = conn.Close()
			awaitChannel(t, done, "the blocked Send to return")
		}
	})
}

func TestOpenIsolationEchoWhileAnotherSessionFloodsAndItsHostNeverReceives(t *testing.T) {
	isolationCase(t, "floods and its host never calls Recv (blocking)", func(t *testing.T, rig *proofRig, a *isolationAuthor) func() {
		conn := rig.mustOpenKind("flood", "a")
		stopped := settled(t, "the flood", a.written.Load)
		return func() {
			if got := a.written.Load(); got != stopped {
				t.Errorf("the author wrote %d more bytes while its host read nothing: a target that propagates a stalled reader must be held", got-stopped)
			}
			if stopped > isolationPasteBound {
				t.Errorf("the author wrote %d bytes before it was held, the bound is %d", stopped, isolationPasteBound)
			} else {
				t.Logf("the author was held after %d KiB", stopped>>10)
			}
			_ = conn.Close()
		}
	})
	isolationCase(t, "floods and its host never calls Recv (lossy)", func(t *testing.T, rig *proofRig, a *isolationAuthor) func() {
		conn := rig.mustOpenKind("lossy", "a")
		eventually(t, "the lossy author to write 16 MiB", func() bool { return a.written.Load() >= 16<<20 })
		before := a.written.Load()
		return func() {
			if got := a.written.Load(); got <= before {
				t.Errorf("the lossy author wrote nothing while its host read nothing: it was held, and a lossy target never is")
			}
			// The host comes back: it asks the session to end, reads what is left, and every byte the author wrote is read or lost
			// in a range it was told of.
			sendClose(t, conn, execv2.CloseMode_CLOSE_MODE_GRACEFUL)
			audit := &proofAudit{verify: true, lossy: true}
			if err := proofDrain(conn, audit, nil); !errors.Is(err, io.EOF) {
				t.Errorf("the stream ended with %v, want io.EOF after Ended", err)
			}
			for _, problem := range audit.problems {
				t.Errorf("the lossy stream: %s", problem)
			}
			if audit.dropped == 0 {
				t.Errorf("a lossy author that wrote %d bytes to a host that read nothing lost nothing", a.written.Load())
			}
			_ = conn.Close()
		}
	})
}

func TestOpenIsolationEchoWhileSessionsWhoseAuthorIgnoresItsContextAreClosed(t *testing.T) {
	isolationCase(t, "ignores its context and is killed, again and again", func(t *testing.T, rig *proofRig, a *isolationAuthor) func() {
		// One session is closed gracefully and left in its grace (5 s) for the whole measurement; the others are killed, which
		// abandons the function at once, as often as the loop can.
		lingering := rig.mustOpenKind("stuck", "a-grace")
		sendClose(t, lingering, execv2.CloseMode_CLOSE_MODE_GRACEFUL)
		stop := make(chan struct{})
		done := make(chan struct{})
		var killed atomic.Uint64
		problems := make(chan string, 8)
		go func() {
			defer close(done)
			for i := 0; i < 200; i++ {
				select {
				case <-stop:
					return
				default:
				}
				conn, err := rig.openKind(context.Background(), "stuck", fmt.Sprintf("a-%d", i))
				if err != nil {
					select {
					case problems <- "open: " + err.Error():
					default:
					}
					return
				}
				if err := conn.Send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Close{Close: &execv2.Close{Mode: execv2.CloseMode_CLOSE_MODE_KILL}}}); err != nil {
					_ = conn.Close()
					continue
				}
				audit := &proofAudit{}
				_ = proofDrain(conn, audit, nil)
				if ended := audit.ended; ended == nil || ended.GetEnd().GetClosed().GetBy() != execv2.ClosedBy_CLOSED_BY_REQUEST || len(audit.problems) > 0 {
					select {
					case problems <- "a killed session of an author that ignores its context ended with " + ended.String():
					default:
					}
				}
				_ = conn.Close()
				killed.Add(1)
			}
		}()
		eventually(t, "a first session to be killed", func() bool { return killed.Load() >= 5 })
		before := killed.Load()
		return func() {
			after := killed.Load()
			close(stop)
			awaitChannel(t, done, "the loop of killed sessions to stop")
			close(problems)
			for problem := range problems {
				t.Error(problem)
			}
			if after <= before {
				t.Errorf("no session was killed while the echo was measured")
			}
			t.Logf("%d sessions of an author that ignores its context were killed and abandoned (%d of them while the echo was measured)", killed.Load(), after-before)
			a.gate.Open()
			_ = lingering.Close()
		}
	})
}
