package exec_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
)

// A start that is refused after it was admitted (here its Options failed) leaves nothing behind before its caller hears
// of the refusal, so a retry of the same id is never refused for the command that has just been refused.
func TestARefusedStartLeavesNothingBehindSoItsIdCanBeRetriedAtOnce(t *testing.T) {
	const (
		retriers   = 8
		iterations = 3000
	)
	optionsFailed := errors.New("options failed")
	target := engTarget(blockUntilCancelled)
	target.Options = func(*types.PluginContext, resourcev2.ResourceReference) ([]exec.Option, error) {
		return nil, optionsFailed
	}
	rig := newEngineRig(t, engineRigConfig{}, target)

	var conflicts, refused atomic.Int64
	failures := make(chan string, retriers)
	done := make(chan struct{}, retriers)
	for r := range retriers {
		go func() {
			defer func() { done <- struct{}{} }()
			id := fmt.Sprintf("retry-%d", r)
			for range iterations {
				_, err := rig.open(id)
				if err == nil {
					failures <- fmt.Sprintf("a start whose Options failed was admitted as %q", id)
					return
				}
				var refusal *exec.Error
				if !errors.As(err, &refusal) {
					failures <- fmt.Sprintf("the refusal is %T %v, want an *exec.Error", err, err)
					return
				}
				if refusal.Class == exec.ErrorClassConflict && refusal.Reason == exec.ErrorReasonSessionExists {
					conflicts.Add(1)
					continue
				}
				refused.Add(1)
			}
		}()
	}
	for range retriers {
		select {
		case <-done:
		case <-time.After(engineWait):
			t.Fatalf("the retries did not finish within %v", engineWait)
		}
	}
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
	if n := conflicts.Load(); n != 0 {
		t.Errorf("%d retries were refused with CONFLICT/SESSION_EXISTS for the command that had just been refused", n)
	}
	if got, want := refused.Load(), int64(retriers*iterations); got != want {
		t.Errorf("%d retries were refused for their own cause, want %d", got, want)
	}
	if live := rig.eng.Live(); len(live) != 0 {
		t.Errorf("refused starts left %v behind", live)
	}
}

// A caller whose context ends while its Options callback is still running gets a typed refusal at once and no command, and
// the function never runs for it.
func TestACallerThatGivesUpDuringAdmissionGetsARefusalAtOnceAndTheFunctionNeverRuns(t *testing.T) {
	var calls atomic.Int32
	gate := exectest.NewGate()
	target := engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
		calls.Add(1)
		return exec.ExitCode(0), nil
	})
	target.Options = func(*types.PluginContext, resourcev2.ResourceReference) ([]exec.Option, error) {
		<-gate.Opened() // ignores its context, as a slow callback may
		return nil, nil
	}
	rig := newEngineRig(t, engineRigConfig{}, target)
	t.Cleanup(gate.Open) // runs before the rig closes the engine, so that nothing waits on the gate

	ctx, cancel := context.WithTimeout(rig.request(), 100*time.Millisecond)
	defer cancel()
	type outcome struct {
		cmd exechook.Command
		err error
	}
	opened := make(chan outcome, 1)
	go func() {
		cmd, err := rig.eng.Open(ctx, rig.openRequest("give-up"))
		opened <- outcome{cmd, err}
	}()
	var got outcome
	select {
	case got = <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("Open did not return when its context ended while Options was still running")
	}
	if got.err == nil || got.cmd != nil {
		t.Fatalf("a caller that gave up got cmd=%v err=%v, want a refusal and no command", got.cmd, got.err)
	}
	if refusal := refusalOf(t, got.err); refusal.Class != exec.ErrorClassTimeout {
		t.Errorf("the refusal is %v, want TIMEOUT", refusal.Class)
	}
	gate.Open()
	eventually(t, "the refused start to be removed", func() bool { return len(rig.eng.Live()) == 0 })
	if n := calls.Load(); n != 0 {
		t.Errorf("the function ran %d times for a caller that gave up", n)
	}
}

// The output queue of a blocking target holds 256 KiB and the input queue holds 256 KiB: a writer of one byte at a time is
// held by the 262,145th byte and by nothing before it.
func TestTheQueueBoundsAre256KiB(t *testing.T) {
	const bound = 256 << 10

	t.Run("output", func(t *testing.T) {
		var accepted atomic.Int64
		rig := newEngineRig(t, engineRigConfig{metricsEvery: time.Millisecond}, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
			one := []byte{'x'}
			for {
				if _, err := p.Stdout.Write(one); err != nil {
					return exec.Exit{}, err
				}
				accepted.Add(1)
			}
		}, func(i *exec.TargetInfo) { i.Capabilities.Stall = exec.StallPropagates }))
		cmd := rig.mustOpen("out")
		eventually(t, "the writer to be held at the bound", func() bool {
			v, ok := metricValue(rig, metricStalls, map[string]string{"queue": "output"})
			return ok && v >= 1
		})
		if got := accepted.Load(); got != bound {
			t.Errorf("the output queue took %d bytes before it held the writer, want %d", got, bound)
		}
		cmd.Close(true)
		awaitDone(t, cmd)
	})

	t.Run("input", func(t *testing.T) {
		rig := newEngineRig(t, engineRigConfig{}, engTarget(blockUntilCancelled))
		cmd := rig.mustOpen("in")

		type write struct {
			n   int
			err error
		}
		writeAsync := func(p []byte) <-chan write {
			result := make(chan write, 1)
			go func() {
				n, err := cmd.Write(context.Background(), p)
				result <- write{n, err}
			}()
			return result
		}

		// The function never reads: the queue takes its whole bound at once and no more.
		fill := writeAsync(make([]byte, bound))
		select {
		case w := <-fill:
			if w.n != bound || w.err != nil {
				t.Fatalf("filling the queue wrote %d bytes, err %v, want %d and none", w.n, w.err, bound)
			}
		case <-time.After(engineWait):
			t.Fatalf("a write of exactly the bound did not finish within %v: the queue holds less than %d", engineWait, bound)
		}

		// One byte more has no room. Nothing observable says "the write is held", so the window below can only hide a
		// fault (a write that is slow to finish), never invent one.
		held := writeAsync([]byte{'x'})
		select {
		case w := <-held:
			t.Fatalf("a write beyond the bound finished with %d bytes, err %v: the queue holds more than %d", w.n, w.err, bound)
		case <-time.After(200 * time.Millisecond):
		}

		// Ending the session releases the held write with the pipe's closed error.
		cmd.Close(true)
		select {
		case w := <-held:
			if w.n != 0 || !errors.Is(w.err, io.ErrClosedPipe) {
				t.Errorf("the held write ended with %d bytes, err %v, want 0 and io.ErrClosedPipe", w.n, w.err)
			}
		case <-time.After(engineWait):
			t.Fatalf("the held write was not released when the session ended, within %v", engineWait)
		}
		awaitDone(t, cmd)
	})
}
