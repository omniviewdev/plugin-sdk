package exec_test

import (
	"context"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// Resize on the wire (plan section 4.5): the host's Resize{seq, size} reaches the function through Proc.Resize, latest wins, and the
// host is answered with Resized{seq, size, result} in order with the stream's other frames. One ack covers every resize up to its
// seq, so a storm costs the function two sizes and the host one ack.

// resizeReader is a function that hands every size of Proc.Resize to sizes until its session ends.
func resizeReader(sizes chan<- exec.Size) exec.ExecFunc {
	return func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		for {
			select {
			case size, ok := <-p.Resize:
				if !ok {
					return exec.ExitCode(0), nil
				}
				sizes <- size
			case <-ctx.Done():
				return exec.Exit{}, ctx.Err()
			}
		}
	}
}

// drainSizes returns the sizes the function has handed over and nobody has read, and does not wait for more.
func drainSizes(sizes <-chan exec.Size) []exec.Size {
	var out []exec.Size
	for {
		select {
		case size := <-sizes:
			out = append(out, size)
		default:
			return out
		}
	}
}

// wantResized holds an ack to what the host expects of it.
func wantResized(t *testing.T, got *execv2.Resized, seq, cols, rows uint32, result execv2.ResizeResult) {
	t.Helper()
	if got.GetSeq() != seq || got.GetSize().GetCols() != cols || got.GetSize().GetRows() != rows || got.GetResult() != result {
		t.Errorf("Resized{seq %d, %dx%d, %v}, want {seq %d, %dx%d, %v}",
			got.GetSeq(), got.GetSize().GetCols(), got.GetSize().GetRows(), got.GetResult(), seq, cols, rows, result)
	}
}

// A storm of resizes sent while the function does not read Proc.Resize costs it the size it was reading and the newest, and costs the
// host one ack, whose seq is the newest and whose size is that size. The function does not read until the storm has been applied,
// which synctest.Wait makes certain of.
func TestAStormOfResizesCostsTheFunctionTwoSizesAndTheHostOneAck(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate, sizes := ctlGate(t), make(chan exec.Size, 16)
		rig := newMemoryRig(t, ctlTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			<-gate.Opened()
			return resizeReader(sizes)(ctx, p)
		}))
		host := rig.ctlSession("s1", 100, 30)
		for seq := uint32(1); seq <= 6; seq++ {
			host.ctlSend(ctlResize(seq, 100+seq, 31))
		}
		synctest.Wait() // the six frames have been applied in order
		if acks := ctlResized(host.ctlFrames()); len(acks) != 0 {
			t.Fatalf("%d acks before the function took a size: an ack says the function took it", len(acks))
		}
		gate.Open()
		synctest.Wait()
		if got, want := drainSizes(sizes), []exec.Size{{Cols: 100, Rows: 30}, {Cols: 106, Rows: 31}}; !slices.Equal(got, want) {
			t.Errorf("the function was handed %v, want %v: the size the session was opened with and then the newest, and no other", got, want)
		}
		acks := ctlResized(host.ctlFrames())
		if len(acks) != 1 {
			t.Fatalf("%d acks for a storm of six resizes the function took two sizes of, want one", len(acks))
		}
		wantResized(t, acks[0], 6, 106, 31, execv2.ResizeResult_RESIZE_RESULT_APPLIED)
		host.ctlLeave()
	})
}

// A resize the function takes is acknowledged APPLIED with its own seq and size, and one that follows its ack is acknowledged the same
// way: an operation is told after its own effect as it is before it. The same resize sent twice is two resizes the function takes.
func TestAResizeTheFunctionTakesIsAcknowledgedAppliedEveryTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sizes := make(chan exec.Size, 16)
		rig := newMemoryRig(t, ctlTarget(resizeReader(sizes)))
		host := rig.ctlSession("s1", 100, 30)
		synctest.Wait()
		if got := drainSizes(sizes); !slices.Equal(got, []exec.Size{{Cols: 100, Rows: 30}}) {
			t.Fatalf("the first sizes are %v, want the one the session was opened with", got)
		}
		if acks := ctlResized(host.ctlFrames()); len(acks) != 0 {
			t.Fatalf("an ack for the size the session was opened with: %d acks", len(acks))
		}
		for _, step := range []struct {
			seq, cols, rows uint32
		}{{1, 120, 40}, {2, 90, 25}, {3, 90, 25}, {1000000, 200, 60}} {
			host.ctlSend(ctlResize(step.seq, step.cols, step.rows))
			synctest.Wait()
			if got, want := drainSizes(sizes), []exec.Size{{Cols: uint16(step.cols), Rows: uint16(step.rows)}}; !slices.Equal(got, want) {
				t.Errorf("seq %d: the function was handed %v, want %v", step.seq, got, want)
			}
			acks := ctlResized(host.ctlFrames())
			if len(acks) != 1 {
				t.Fatalf("seq %d: %d acks, want one", step.seq, len(acks))
			}
			wantResized(t, acks[0], step.seq, step.cols, step.rows, execv2.ResizeResult_RESIZE_RESULT_APPLIED)
		}
		host.ctlLeave()
	})
}

// A resize for a session that cannot be resized is acknowledged IGNORED at once, with its seq and size, whoever is not reading and
// whatever time passes: a target that declared no Resize, and a session without a terminal on one that did. The function is handed
// nothing, and a later resize is acknowledged the same way.
func TestAResizeForASessionThatCannotBeResizedIsAcknowledgedIgnoredAtOnce(t *testing.T) {
	cases := []struct {
		name string
		tty  bool
		caps func(*exec.Capabilities)
	}{
		{"a target that declared no Resize", true, func(c *exec.Capabilities) { c.Resize = false }},
		{"a session without a terminal", false, func(*exec.Capabilities) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sizes := make(chan exec.Size, 16)
				rig := newMemoryRig(t, ctlTarget(resizeReader(sizes), tc.caps))
				var host *memoryStream
				if tc.tty {
					host = rig.ctlSession("s1", 100, 30)
				} else {
					host = rig.ctlPlainSession("s1")
				}
				synctest.Wait()
				for _, seq := range []uint32{7, 8} {
					host.ctlSend(ctlResize(seq, 90+seq, 20))
					synctest.Wait()
					acks := ctlResized(host.ctlFrames())
					if len(acks) != 1 {
						t.Fatalf("seq %d: %d acks with no time passed, want one at once", seq, len(acks))
					}
					wantResized(t, acks[0], seq, 90+seq, 20, execv2.ResizeResult_RESIZE_RESULT_IGNORED)
				}
				time.Sleep(time.Minute) // nothing else follows: the function was never going to take a size
				synctest.Wait()
				if acks := ctlResized(host.ctlFrames()); len(acks) != 0 {
					t.Errorf("%d more acks a minute later, want none", len(acks))
				}
				want := []exec.Size(nil)
				if tc.tty {
					want = []exec.Size{{Cols: 100, Rows: 30}} // the size the session was opened with, which is no resize
				}
				if got := drainSizes(sizes); !slices.Equal(got, want) {
					t.Errorf("the function was handed %v, want %v: a resize for a session that cannot be resized is not delivered", got, want)
				}
				host.ctlLeave()
			})
		})
	}
}

// A resize with a zero dimension or one above the target's limit is a malformed frame: the stream ends with INVALID_REQUEST, no
// frame is sent for it, and the session ends with its stream. It is so on a target that can resize and on one that cannot: the frame
// is checked before what it would do.
func TestAResizeWithAZeroOrOversizeDimensionEndsTheStreamInvalidRequestAndTheSession(t *testing.T) {
	limit := uint32(exec.DefaultSessionLimits().MaxDimension)
	cases := []struct {
		name       string
		cols, rows uint32
	}{
		{"no columns", 0, 24},
		{"no rows", 80, 0},
		{"neither", 0, 0},
		{"one column too many", limit + 1, 24},
		{"one row too many", 80, limit + 1},
		{"more than the engine can hold", 1 << 20, 24},
	}
	for _, canResize := range []bool{true, false} {
		for _, tc := range cases {
			name := tc.name + ", target that cannot resize"
			if canResize {
				name = tc.name + ", target that can resize"
			}
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					cancelled := make(chan struct{})
					rig := newMemoryRig(t, ctlTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
						<-ctx.Done()
						close(cancelled)
						return exec.Exit{}, ctx.Err()
					}, func(c *exec.Capabilities) { c.Resize = canResize }))
					host := rig.ctlSession("s1", 100, 30)
					host.ctlSend(ctlResize(5, tc.cols, tc.rows))
					synctest.Wait()
					returned, err := host.ctlStatus()
					if !returned {
						t.Fatal("the stream did not end for a malformed resize")
					}
					if class, _ := ctlClass(err); class != "INVALID_REQUEST" {
						t.Errorf("the stream ended with %v (class %q), want INVALID_REQUEST", err, class)
					}
					select {
					case <-cancelled:
					default:
						t.Error("the session did not end with its stream")
					}
					frames := host.ctlFrames()
					if len(ctlResized(frames)) != 0 || ctlEnded(frames) != nil {
						t.Errorf("a stream that ended with a status sent %d acks and an Ended", len(ctlResized(frames)))
					}
				})
			})
		}
	}
}

// The unread deadline. A function that declared Resize and does not read Proc.Resize is told to the host after the engine's
// ResizeUnreadAfter, as FAILED, for the newest resize sent (which covers the ones before it), and never before: the stream and the
// SDK do not wait for the function. A later read is told too, APPLIED, so the host ends with the truth.
func TestAResizeTheFunctionNeverTakesIsAcknowledgedFailedAtTheUnreadDeadlineAndAppliedIfItIsTakenLater(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const unread = 3 * time.Second
		gate, sizes := ctlGate(t), make(chan exec.Size, 16)
		rig := newMemoryRig(t, ctlTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			<-gate.Opened()
			return resizeReader(sizes)(ctx, p)
		}))
		exec.ConfigurePluginEngine(rig.plugin, exechook.EngineConfig{ResizeUnreadAfter: unread})
		host := rig.ctlSession("s1", 100, 30)
		for seq := uint32(1); seq <= 6; seq++ {
			host.ctlSend(ctlResize(seq, 100+seq, 31))
		}
		synctest.Wait()
		time.Sleep(unread - time.Millisecond)
		synctest.Wait()
		if acks := ctlResized(host.ctlFrames()); len(acks) != 0 {
			t.Fatalf("%d acks before the deadline", len(acks))
		}
		time.Sleep(2 * time.Millisecond)
		synctest.Wait()
		acks := ctlResized(host.ctlFrames())
		if len(acks) != 1 {
			t.Fatalf("%d acks at the deadline, want one for the newest resize", len(acks))
		}
		wantResized(t, acks[0], 6, 106, 31, execv2.ResizeResult_RESIZE_RESULT_FAILED)

		time.Sleep(10 * unread)
		synctest.Wait()
		if acks := ctlResized(host.ctlFrames()); len(acks) != 0 {
			t.Fatalf("%d more acks while nothing changed: one deadline, one ack", len(acks))
		}

		gate.Open() // the function reads at last: the initial size and the newest
		synctest.Wait()
		acks = ctlResized(host.ctlFrames())
		if len(acks) != 1 {
			t.Fatalf("%d acks once the function took the size, want one", len(acks))
		}
		wantResized(t, acks[0], 6, 106, 31, execv2.ResizeResult_RESIZE_RESULT_APPLIED)
		host.ctlLeave()
	})
}

// The deadline counts from the oldest resize that no ack covers, so a resize that comes later does not postpone it, and is covered by
// the ack that it earns; and a resize that comes after that ack starts a deadline of its own.
func TestTheUnreadDeadlineCountsFromTheOldestResizeAndStartsAgainAfterItsAck(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const unread = 5 * time.Second
		rig := newMemoryRig(t, ctlTarget(ctlHold(func(*exec.Proc) {})))
		host := rig.ctlSession("s1", 100, 30)
		host.ctlSend(ctlResize(1, 101, 31))
		synctest.Wait()
		start := time.Now()
		time.Sleep(unread - time.Second)
		host.ctlSend(ctlResize(2, 102, 32))
		synctest.Wait()
		time.Sleep(time.Second)
		synctest.Wait()
		acks := ctlResized(host.ctlFrames())
		if len(acks) != 1 || time.Since(start) != unread {
			t.Fatalf("%d acks %v after the first resize, want one at %v", len(acks), time.Since(start), unread)
		}
		wantResized(t, acks[0], 2, 102, 32, execv2.ResizeResult_RESIZE_RESULT_FAILED)

		host.ctlSend(ctlResize(3, 103, 33))
		synctest.Wait()
		time.Sleep(unread - time.Millisecond)
		synctest.Wait()
		if acks := ctlResized(host.ctlFrames()); len(acks) != 0 {
			t.Fatalf("%d acks before the deadline of the resize that followed the ack", len(acks))
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		acks = ctlResized(host.ctlFrames())
		if len(acks) != 1 {
			t.Fatalf("%d acks at the second deadline, want one", len(acks))
		}
		wantResized(t, acks[0], 3, 103, 33, execv2.ResizeResult_RESIZE_RESULT_FAILED)
		host.ctlLeave()
	})
}

// A function that takes the size before the deadline is acknowledged APPLIED and never FAILED: the deadline is over when what it
// waited for happened.
func TestAResizeTheFunctionTakesBeforeTheDeadlineIsNeverAcknowledgedFailed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate, sizes := ctlGate(t), make(chan exec.Size, 16)
		rig := newMemoryRig(t, ctlTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			<-gate.Opened()
			return resizeReader(sizes)(ctx, p)
		}))
		host := rig.ctlSession("s1", 100, 30)
		host.ctlSend(ctlResize(1, 101, 31))
		synctest.Wait()
		time.Sleep(4 * time.Second) // nearly the engine's 5 seconds
		gate.Open()
		synctest.Wait()
		acks := ctlResized(host.ctlFrames())
		if len(acks) != 1 {
			t.Fatalf("%d acks once the function took the size, want one", len(acks))
		}
		wantResized(t, acks[0], 1, 101, 31, execv2.ResizeResult_RESIZE_RESULT_APPLIED)
		time.Sleep(time.Minute)
		synctest.Wait()
		if acks := ctlResized(host.ctlFrames()); len(acks) != 0 {
			t.Errorf("%d acks after the deadline of a resize that was applied: %+v", len(acks), acks)
		}
		host.ctlLeave()
	})
}

// A session that narrows its capabilities to no Resize (Started) is not resized from then on: a resize after it is acknowledged IGNORED
// at once, as for a target that never could.
func TestAResizeAfterTheSessionNarrowedItsResizeCapabilityIsAcknowledgedIgnoredAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, ctlTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			narrowed := p.Capabilities
			narrowed.Resize = false
			p.Started(exec.SessionFacts{Capabilities: &narrowed})
			<-ctx.Done()
			return exec.Exit{}, ctx.Err()
		}))
		host := rig.ctlSession("s1", 100, 30)
		synctest.Wait()
		host.ctlSend(ctlResize(2, 102, 32))
		synctest.Wait()
		acks := ctlResized(host.ctlFrames())
		if len(acks) != 1 {
			t.Fatalf("%d acks for a resize after the session narrowed, want one at once", len(acks))
		}
		wantResized(t, acks[0], 2, 102, 32, execv2.ResizeResult_RESIZE_RESULT_IGNORED)
		host.ctlLeave()
	})
}

// A resize that waits when the session narrows to no Resize is told IGNORED at its deadline and not FAILED: the target said it cannot,
// and a function that said so is not one that failed to read.
func TestAResizeThatWaitedWhenTheSessionNarrowedIsAcknowledgedIgnoredAtItsDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := ctlGate(t)
		rig := newMemoryRig(t, ctlTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			<-gate.Opened()
			narrowed := p.Capabilities
			narrowed.Resize = false
			p.Started(exec.SessionFacts{Capabilities: &narrowed})
			<-ctx.Done()
			return exec.Exit{}, ctx.Err()
		}))
		host := rig.ctlSession("s1", 100, 30)
		host.ctlSend(ctlResize(1, 101, 31)) // accepted: the session can be resized when the frame comes
		synctest.Wait()
		gate.Open()
		synctest.Wait()
		time.Sleep(time.Minute)
		synctest.Wait()
		acks := ctlResized(host.ctlFrames())
		if len(acks) != 1 {
			t.Fatalf("%d acks at the deadline, want one", len(acks))
		}
		wantResized(t, acks[0], 1, 101, 31, execv2.ResizeResult_RESIZE_RESULT_IGNORED)
		host.ctlLeave()
	})
}
