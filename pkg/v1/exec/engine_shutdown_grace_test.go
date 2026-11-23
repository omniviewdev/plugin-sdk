package exec_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The host stops a plugin two seconds after it asks, so the engine does not give a function that ignores its context the grace
// of an ordinary close (5 s) when the plugin is stopping: each function has the shutdown grace (1 s) to return, and one that
// has not is given up on, so that every session of the plugin still ends with Closed{PLUGIN_SHUTDOWN} before the host stops it.

// The grace is an hour and the shutdown grace a few milliseconds: the engine's Close completes within the test's bound only if it
// waited for the second. Every session ends by the plugin shutting down, and the stuck functions are given up on.
func TestAStuckFunctionDoesNotHoldTheEnginesCloseForTheCloseGrace(t *testing.T) {
	const sessions = 3
	gate := exectest.NewGate()
	rig := newEngineRig(t, engineRigConfig{grace: time.Hour, shutdownGrace: 5 * time.Millisecond}, stuckTarget(gate, 8))
	t.Cleanup(gate.Open)
	var cmds []exechook.Command
	for i := range sessions {
		cmds = append(cmds, rig.mustOpen(fmt.Sprintf("s%d", i), withoutTTY))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rig.eng.Close(ctx); err != nil {
		t.Fatalf("Close: %v: it waited for the close grace of an hour, and not for the shutdown grace", err)
	}
	for i, cmd := range cmds {
		res := awaitDone(t, cmd)
		if by := res.End.GetClosed().GetBy(); by != execv2.ClosedBy_CLOSED_BY_PLUGIN_SHUTDOWN || !res.Abandoned {
			t.Errorf("session %d ended %v (abandoned %v), want Closed by PLUGIN_SHUTDOWN with its function given up on", i, res.End, res.Abandoned)
		}
	}
	if got := rig.eng.Abandoned(); got != sessions {
		t.Errorf("the engine counts %d abandoned functions, want %d", got, sessions)
	}
}

// Every function has the shutdown grace of its own: a close with many stuck sessions arms one timer for each, and each ends
// when its own runs out.
func TestEveryFunctionHasItsOwnShutdownGrace(t *testing.T) {
	const sessions, shutdownGrace = 12, 2 * time.Second
	gate := exectest.NewGate()
	rig := newEngineRig(t, engineRigConfig{grace: 7 * time.Second, shutdownGrace: shutdownGrace}, stuckTarget(gate, sessions))
	t.Cleanup(gate.Open)
	timers := newFakeTimers()
	exec.SetEngineSeams(rig.eng, exec.EngineSeams{NewTimer: timers.New})
	for i := range sessions {
		rig.mustOpen(fmt.Sprintf("s%d", i), withoutTTY)
	}
	closed := make(chan error, 1)
	go func() { closed <- rig.eng.Close(context.Background()) }()
	for range sessions {
		timer := timers.next(t)
		if timer.d != shutdownGrace {
			t.Fatalf("the engine armed a timer of %v, want the shutdown grace of %v for each session", timer.d, shutdownGrace)
		}
		timer.fire()
	}
	if err := <-closed; err != nil {
		t.Errorf("Close: %v", err)
	}
	if got := rig.eng.Abandoned(); got != sessions {
		t.Errorf("the engine counts %d abandoned functions, want %d", got, sessions)
	}
	if live := rig.eng.Live(); len(live) != 0 {
		t.Errorf("a closed engine holds %v", live)
	}
}
