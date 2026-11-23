package exec_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/internal/lifecycletest"
	sdkdiag "github.com/omniviewdev/plugin-sdk/pkg/diagnostics"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The engine decides once whether it gave up on a function, and the function's own return makes the same decision: a
// function that has returned and ended its session is never taken for an abandoned one by a kill that arrives after it. The
// order that a real run reaches only by chance is forced here: the function is held right after it ended the session, the
// kill reaches the supervisor, and the supervisor's decision is read before the function is let go. (A function that
// compared-and-swapped after it reported would be abandoned here, with its status thrown away.)
func TestAKillThatFollowsTheFunctionsOwnEndNeverCallsItAbandoned(t *testing.T) {
	functionEnded, release := exectest.NewGate(), exectest.NewGate()
	decided := make(chan bool, 1)
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
		return exec.ExitCode(0), nil
	}, func(i *exec.TargetInfo) { i.Capabilities.Kill = exec.KillTerminates }))
	exec.SetEngineSeams(rig.eng, exec.EngineSeams{
		FunctionEnded:  func() { functionEnded.Open(); <-release.Opened() },
		AbandonDecided: func(abandoned bool) { decided <- abandoned },
	})
	t.Cleanup(release.Open)

	cmd := rig.mustOpen("s1", withoutTTY)
	awaitChannel(t, functionEnded.Opened(), "the function to end its session")
	cmd.Close(true)
	select {
	case abandoned := <-decided:
		if abandoned {
			t.Fatal("the engine gave up on a function that had already returned and ended its session")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the supervisor never decided about the function")
	}
	release.Open()
	res := awaitDone(t, cmd)
	assertEnd(t, res.End, exited(0, byProcess, new(false)))
	if res.Abandoned || res.Cause != "finished" {
		t.Errorf("abandoned %v, cause %q: the function ended the session and returned, so its status is the end", res.Abandoned, res.Cause)
	}
	if n := rig.eng.Abandoned(); n != 0 {
		t.Errorf("%d functions are counted abandoned", n)
	}
}

// The grace, like the kill, decides at the same compare-and-swap: a function held right after it ended its session while
// the grace runs out is not abandoned either.
func TestAGraceThatRunsOutAfterTheFunctionsOwnEndNeverCallsItAbandoned(t *testing.T) {
	functionEnded, release := exectest.NewGate(), exectest.NewGate()
	decided := make(chan bool, 1)
	rig := newEngineRig(t, engineRigConfig{grace: 10 * time.Millisecond}, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
		return exec.ExitCode(0), nil
	}))
	exec.SetEngineSeams(rig.eng, exec.EngineSeams{
		FunctionEnded:  func() { functionEnded.Open(); <-release.Opened() },
		AbandonDecided: func(abandoned bool) { decided <- abandoned },
	})
	t.Cleanup(release.Open)

	cmd := rig.mustOpen("s1", withoutTTY)
	awaitChannel(t, functionEnded.Opened(), "the function to end its session")
	select {
	case abandoned := <-decided: // the grace of 10 ms ran out while the function was held
		if abandoned {
			t.Fatal("the engine gave up on a function that had already returned and ended its session")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the grace never ran out")
	}
	release.Open()
	res := awaitDone(t, cmd)
	assertEnd(t, res.End, exited(0, byProcess, new(false)))
	if res.Abandoned {
		t.Error("the function was marked abandoned")
	}
}

// Engines that publish to one diagnostics registry share its gauges: a gauge that each engine set from its own count would
// show the last engine's count only.
func TestEnginesThatShareARegistryShareTheSessionGauge(t *testing.T) {
	shared := sdkdiag.NewRegistry()
	a := newEngineRig(t, engineRigConfig{diag: shared}, engTarget(blockUntilCancelled))
	b := newEngineRig(t, engineRigConfig{diag: shared}, engTarget(blockUntilCancelled))
	running := map[string]string{"state": "running"}
	ca, cb := a.mustOpen("a1"), b.mustOpen("b1")
	wantMetric(t, a, metricSessions, running, 2)
	ca.Close(false)
	awaitDone(t, ca)
	wantMetric(t, b, metricSessions, running, 1) // each engine's own count would say 0 here, or 1 from the other side
	cb.Close(false)
	awaitDone(t, cb)
	wantMetric(t, a, metricSessions, running, 0)
}

func TestEnginesThatShareARegistryShareTheAbandonedGauge(t *testing.T) {
	shared := sdkdiag.NewRegistry()
	stuck := exectest.NewGate()
	t.Cleanup(stuck.Open)
	script := func() exec.Target {
		return exectest.NewFakeTarget(exectest.FakeScript(exectest.IgnoreCancel(stuck)))
	}
	a := newEngineRig(t, engineRigConfig{diag: shared, grace: 10 * time.Millisecond}, script())
	b := newEngineRig(t, engineRigConfig{diag: shared, grace: 10 * time.Millisecond}, script())
	ca, cb := a.mustOpen("a1"), b.mustOpen("b1")
	ca.Close(false)
	cb.Close(false)
	awaitDone(t, ca)
	awaitDone(t, cb)
	wantMetric(t, a, metricAbandoned, map[string]string{}, 2)
	stuck.Open()
	eventually(t, "both abandoned functions to return", func() bool {
		v, ok := metricValue(a, metricAbandoned, map[string]string{})
		return ok && v == 0
	})
}

// ---------------------------------------------------------------------------------------------------------------
// a thousand sessions, every kind of end
// ---------------------------------------------------------------------------------------------------------------

func withKind(kind string, tty bool) func(*execv2.Start) {
	return func(s *execv2.Start) {
		s.Tty = tty // half of the sessions have a terminal, and so a resizer
		s.Labels = []*execv2.NameValue{{Name: "kind", Value: kind}}
	}
}

// readToTheEnd reads a command's output to its end and returns what it delivered and what it was told was dropped.
func readToTheEnd(cmd exechook.Command) (read, dropped uint64) {
	buf := make([]byte, 4096)
	for {
		c, err := cmd.Read(context.Background(), buf)
		read += uint64(c.N)
		for _, d := range c.Dropped {
			dropped += d.Bytes
		}
		if err != nil {
			return read, dropped
		}
	}
}

// A thousand sessions, ended every way an end can come (the function returning a status, a failure, a panic, a lost stream,
// a close, a kill, and a function that ignores its cancel), leave at rest: no session in the engine, a session gauge at
// zero, no function abandoned once the stuck ones return, no goroutine over the baseline taken before the first, each
// session ended exactly once and every byte it wrote delivered or counted as dropped.
func TestAThousandSessionsOfEveryKindOfEndLeaveNoSessionAndNoGoroutine(t *testing.T) {
	const sessions, workers = 1000, 8
	stuck := exectest.NewGate()
	t.Cleanup(stuck.Open)
	rig := newEngineRig(t, engineRigConfig{grace: 20 * time.Millisecond}, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		switch p.Request.Labels["kind"] {
		case "exit":
			_, _ = p.Stdout.Write([]byte("hello"))
			return exec.ExitCode(0), nil
		case "echo":
			_, _ = io.Copy(p.Stdout, p.Stdin)
			return exec.ExitCode(0), nil
		case "fail":
			return exec.Exit{}, errors.New("failed")
		case "panic":
			panic("a function that panics")
		case "lost":
			return exec.ExitLost(exec.LostNoStatus), nil
		case "stuck":
			<-stuck.Opened() // ignores its cancelled context until the test lets it go
			return exec.Exit{}, ctx.Err()
		default: // close, kill
			<-ctx.Done()
			return exec.Exit{}, ctx.Err()
		}
	}))
	baseline := lifecycletest.TakeBaseline()

	kinds := []string{"exit", "echo", "fail", "panic", "lost", "close", "kill", "stuck"}
	type outcome struct {
		kind         string
		res          exechook.Result
		read, missed uint64
	}
	outcomes := make(chan outcome, sessions)
	for w := range workers {
		go func() {
			for i := w; i < sessions; i += workers {
				kind := kinds[i%len(kinds)]
				cmd, err := rig.open(fmt.Sprintf("s%d", i), withKind(kind, i%2 == 0))
				if err != nil {
					outcomes <- outcome{kind: "refused: " + err.Error()}
					continue
				}
				readDone := make(chan [2]uint64, 1)
				go func() { r, d := readToTheEnd(cmd); readDone <- [2]uint64{r, d} }()
				switch kind {
				case "echo":
					_, _ = cmd.Write(context.Background(), []byte("abc"))
					cmd.CloseInput()
				case "close", "stuck":
					cmd.Close(false)
				case "kill":
					cmd.Close(true)
				}
				<-cmd.Done()
				counts := <-readDone
				outcomes <- outcome{kind: kind, res: cmd.Result(), read: counts[0], missed: counts[1]}
			}
		}()
	}
	var exited, failed, lost, closed int
	for range sessions {
		o := <-outcomes
		if o.res.End == nil {
			t.Errorf("a session did not run: %s", o.kind)
			continue
		}
		switch {
		case o.res.End.GetExited() != nil:
			exited++
		case o.res.End.GetFailed() != nil:
			failed++
		case o.res.End.GetLost() != nil:
			lost++
		case o.res.End.GetClosed() != nil:
			closed++
		default:
			t.Errorf("a %s session ended with %v, which is none of the five", o.kind, o.res.End)
		}
		if o.read+o.missed != o.res.BytesOut {
			t.Errorf("a %s session wrote %d bytes and the reader got %d and was told %d dropped", o.kind, o.res.BytesOut, o.read, o.missed)
		}
	}
	each := sessions / len(kinds)
	if exited != 2*each || failed != 2*each || lost != each || closed != 3*each {
		t.Errorf("ends: exited %d failed %d lost %d closed %d, want %d %d %d %d", exited, failed, lost, closed, 2*each, 2*each, each, 3*each)
	}

	// At rest: the stuck functions return, and nothing of the thousand is left.
	stuck.Open()
	eventually(t, "the abandoned functions to return", func() bool { return rig.eng.Abandoned() == 0 })
	if live := rig.eng.Live(); len(live) != 0 {
		t.Errorf("%d sessions are still in the engine: %v", len(live), live)
	}
	wantMetric(t, rig, metricSessions, map[string]string{"state": "running"}, 0)
	if err := baseline.Check(10 * time.Second); err != nil {
		t.Errorf("goroutines after %d sessions: %v", sessions, err)
	}
}
