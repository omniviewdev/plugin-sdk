package exec_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// Plugin.Shutdown: what it waits for, and for how long. The streams of the server are in memory and the engine's clock is the
// bubble's, so that a Shutdown that is to be held, or to give up, is seen to be, and nothing is waited for.

// endedOn returns the Ended that a stream has been sent, or nil. It looks at what is there and does not wait.
func endedOn(s *memoryStream) *execv2.Ended {
	for {
		select {
		case frame := <-s.out:
			if ended := frame.GetEnded(); ended != nil {
				return ended
			}
		default:
			return nil
		}
	}
}

// Shutdown returns the error of its context when the context ends before the sessions have ended: it does not wait for a
// function that ignores its own context longer than it was told to (M25: a Shutdown that took no context from its caller waited
// for the whole grace and answered nil, so that the plugin could not be stopped in the time its host gives it).
func TestShutdownReturnsTheErrorOfItsContextWhenAFunctionIgnoresItsOwn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, gate := make(chan struct{}), exectest.NewGate()
		t.Cleanup(gate.Open)
		rig := newMemoryRig(t, engTarget(func(ctx context.Context, _ *exec.Proc) (exec.Exit, error) {
			close(entered)
			<-gate.Opened() // ignores its context until the test lets it go
			return exec.Exit{}, ctx.Err()
		}))
		host := rig.stream("s1", nil)
		host.opened(t)
		<-entered

		const budget = 100 * time.Millisecond
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()
		start := time.Now()
		err := rig.plugin.Shutdown(ctx)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Shutdown with a function that ignores its context and a context of %v: %v, want the context's error", budget, err)
		}
		// The clock is the bubble's, so the time a call took is exact: a Shutdown that gave its caller's context up waited for the
		// function's grace instead.
		if took := time.Since(start); took > budget {
			t.Errorf("Shutdown returned after %v, want within its context's %v: it waited for the function", took, budget)
		}

		// The function returns: a Shutdown that is given the time now completes, and the host's stream received its end.
		gate.Open()
		synctest.Wait()
		if err := rig.plugin.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown once the function returned: %v", err)
		}
		if ended := endedOn(host); ended.GetEnd().GetClosed().GetBy() != execv2.ClosedBy_CLOSED_BY_PLUGIN_SHUTDOWN {
			t.Errorf("the stream was sent %v, want Ended closed by the plugin's shutdown", ended)
		}
		host.hostLeaves()
	})
}

// Shutdown returns once the end of each session has been sent to its stream, and not when the engine has only removed the
// session: the stream's handler sends the Ended after that, and a plugin that stops its transport as soon as Shutdown returns
// would stop it before the frame is sent, which is what made the typed end unreachable (the review of #90, SHOULD 2).
func TestShutdownWaitsUntilTheEndOfEverySessionHasBeenSentToItsStream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, engTarget(blockUntilCancelled))
		gate := make(chan struct{})
		held, free := rig.stream("held", gate), rig.stream("free", nil)
		held.opened(t)
		free.opened(t)

		done := make(chan error, 1)
		go func() { done <- rig.plugin.Shutdown(context.Background()) }()
		synctest.Wait() // both sessions are ended and removed; one stream has been sent its Ended, the other is held

		select {
		case err := <-done:
			t.Fatalf("Shutdown returned %v while a stream had not been sent the end of its session", err)
		default:
		}
		if ended := endedOn(free); ended == nil {
			t.Error("the stream that takes frames was not sent its Ended")
		}

		close(gate)
		synctest.Wait()
		if err := <-done; err != nil {
			t.Errorf("Shutdown: %v", err)
		}
		if ended := endedOn(held); ended == nil || ended.GetEnd().GetClosed().GetBy() != execv2.ClosedBy_CLOSED_BY_PLUGIN_SHUTDOWN {
			t.Errorf("the stream that was held was sent %v, want Ended closed by the plugin's shutdown", ended)
		}
		held.hostLeaves()
		free.hostLeaves()
	})
}

// The wait for the streams is bounded by the context too: a host that takes no frame cannot hold a plugin that is stopping for
// longer than the time its caller gave it.
func TestShutdownGivesUpOnAStreamThatTakesNoFrameWhenItsContextEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, engTarget(blockUntilCancelled))
		gate := make(chan struct{})
		held := rig.stream("held", gate)
		held.opened(t)

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if err := rig.plugin.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Shutdown with a stream that takes no frame and a context of 100 ms: %v, want the context's error", err)
		}

		close(gate)
		synctest.Wait()
		if err := rig.plugin.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown once the stream took its frame: %v", err)
		}
		held.hostLeaves()
	})
}
