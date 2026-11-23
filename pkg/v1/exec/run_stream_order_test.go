package exec_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The order of a RunStream, and the accounting of a stream under pressure: what holds however the goroutines of the call are
// scheduled.

// A function that reports its start and then waits is a command that has started: the host is told so at once, and not when the
// first byte of output or the end gives the SDK something else to send.
func TestServedRunStreamSendsRunStartedAsSoonAsTheFunctionReportsItWithoutWaitingForOutput(t *testing.T) {
	gate := exectest.NewGate()
	rig := newRunSrvRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		p.Started(exec.SessionFacts{Program: exec.Program{Name: "slow", Path: "/bin/slow"}})
		select {
		case <-gate.Opened():
			return exec.ExitCode(0), nil
		case <-ctx.Done():
			return exec.Exit{}, ctx.Err()
		}
	}))
	t.Cleanup(gate.Open)
	stream := rig.stream(rig.ctx(), rig.request())
	event, err := stream.Recv() // the function has written nothing and is waiting for the gate: the only thing to send is this
	if err != nil || event.GetStarted() == nil {
		t.Fatalf("first event %v, %v, want RunStarted", event, err)
	}
	if got := event.GetStarted().GetProgram().GetPath(); got != "/bin/slow" {
		t.Errorf("RunStarted program %v, want the one the function reported", event.GetStarted().GetProgram())
	}
	gate.Open()
	rest, err := runSrvCollect(stream)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("stream ended with %v", err)
	}
	runSrvRequireKinds(t, rest, "ended")
}

// However the reader and the call's own goroutine are scheduled, RunStarted is the first event and is sent once: a function that
// reports its start and writes at once gives the SDK a start to report and output to send at the same moment, many times over.
func TestServedRunStreamNeverSendsOutputBeforeRunStarted(t *testing.T) {
	rig := newRunSrvRig(t, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		p.Started(exec.SessionFacts{})
		_, _ = io.WriteString(p.Stdout, "one")
		_, _ = io.WriteString(p.Stderr, "two")
		return exec.ExitCode(0), nil
	}))
	for i := range 300 {
		var kinds []string
		returned := rig.serveStream(context.Background(), rig.request(runSrvID(fmt.Sprintf("order-%d", i))), func(e *execv2.RunEvent) error {
			kinds = append(kinds, runSrvKind(e)) // one sender at a time: no lock is needed, and a race is a finding
			return nil
		})
		if err := <-returned; err != nil {
			t.Fatalf("run %d: the stream ended with %v", i, err)
		}
		if got := strings.Join(kinds, ","); got != "started,output,output,ended" {
			t.Fatalf("run %d: events %s, want started,output,output,ended: RunStarted first, once, and RunEnded last", i, got)
		}
	}
}

// A stream accounts for what the engine's queue lost as dropped, as a run does, and says the output is truncated: its bytes are
// not delivered and not counted twice.
func TestServedRunStreamCountsWhatTheEnginesQueueLostAsDropped(t *testing.T) {
	const written = 1 << 20
	rig := newRunSrvRig(t, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		if _, err := p.Stdout.Write(runSrvText(written)); err != nil {
			return exec.Exit{}, err
		}
		return exec.ExitCode(0), nil
	}, func(info *exec.TargetInfo) { info.Capabilities.Stall = exec.StallLossy }))
	events, err := runSrvCollect(rig.stream(rig.ctx(), rig.request(runSrvCaps(1<<20, 0, execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD_TAIL))))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("stream ended with %v", err)
	}
	stdout, _ := runSrvOutput(t, events, false)
	account := runSrvEnded(t, events).GetStdoutAccount()
	if account.GetTotalBytes() != written || account.GetKeptBytes() != uint64(len(stdout)) || account.GetDroppedBytes() == 0 ||
		account.GetKeptBytes()+account.GetDroppedBytes() != account.GetTotalBytes() || account.GetCompleteness() != completeTruncated {
		t.Errorf("account %v with %d bytes delivered of %d written: want total = written, kept = delivered, dropped > 0 and TRUNCATED", account, len(stdout), written)
	}
}

// The calls that hold a command are in flight for Plugin.Shutdown from before they start it until they return, so that a plugin
// that stops does not stop the transport before the ends have been handed to it.
func TestServedRunAndRunStreamAreInFlightUntilTheyReturn(t *testing.T) {
	hold := newRunSrvHold(t)
	rig := newRunSrvRig(t, engTarget(hold.run, runSrvLimits(exec.RunLimits{MaxConcurrent: 4})))
	if got := exec.RunInflight(rig.server.Plugin); got != 0 {
		t.Fatalf("%d calls in flight before any call", got)
	}
	unary := rig.start(rig.request(runSrvID("unary")))
	awaitChannel(t, hold.entered, "the run to start")
	if got := exec.RunInflight(rig.server.Plugin); got != 1 {
		t.Errorf("%d calls in flight while a Run is under way, want 1", got)
	}
	stream := rig.stream(rig.ctx(), rig.request(runSrvID("stream")))
	awaitChannel(t, hold.entered, "the streamed run to start")
	if got := exec.RunInflight(rig.server.Plugin); got != 2 {
		t.Errorf("%d calls in flight while a Run and a RunStream are under way, want 2", got)
	}
	// a refusal is never in flight: nothing was started for it
	if _, err := rig.run(rig.request(runSrvID("unary"))); err == nil {
		t.Fatal("a second run under a live id was admitted")
	}
	if got := exec.RunInflight(rig.server.Plugin); got != 2 {
		t.Errorf("%d calls in flight after a refusal, want 2", got)
	}

	hold.gate.Open()
	if a := <-unary; a.err != nil || a.res.GetOutcome() != execv2.Outcome_OUTCOME_EXITED {
		t.Fatalf("the run answered %v, %v", a.res, a.err)
	}
	if _, err := runSrvCollect(stream); !errors.Is(err, io.EOF) {
		t.Fatalf("stream ended with %v", err)
	}
	eventually(t, "the calls to return", func() bool { return exec.RunInflight(rig.server.Plugin) == 0 })
}

func TestServedRunStreamBytesAreTheCommandsAsOneComparisonOfOrder(t *testing.T) {
	// The bytes of a stream and the bytes of a Run are the same bytes: the same function, run both ways, gives the same output.
	write := func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		for i := range 50 {
			_, _ = fmt.Fprintf(p.Stdout, "out %d\n", i)
			_, _ = fmt.Fprintf(p.Stderr, "err %d\n", i)
		}
		return exec.ExitCode(4), nil
	}
	rig := newRunSrvRig(t, engTarget(write))
	res := rig.mustRun(rig.request())
	events, err := runSrvCollect(rig.stream(rig.ctx(), rig.request()))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("stream ended with %v", err)
	}
	stdout, stderr := runSrvOutput(t, events, true)
	if string(stdout) != string(res.GetStdout()) || string(stderr) != string(res.GetStderr()) {
		t.Errorf("a stream delivered %d and %d bytes, a Run returned %d and %d: they are the same command", len(stdout), len(stderr), len(res.GetStdout()), len(res.GetStderr()))
	}
	got := proto.Clone(runSrvEnded(t, events)).(*execv2.RunResult)
	got.Stdout, got.Stderr = res.Stdout, res.Stderr
	requireRunSrvResult(t, got, res)
}
