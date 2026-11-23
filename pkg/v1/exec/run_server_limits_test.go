package exec_test

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/codes"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The places of a target, and the one id space of the engine: what a run shares with the runs and the sessions beside it.

// runSrvHold is a run that has been admitted and waits for the test: the function says it has started, and returns when the
// gate opens or its context ends.
type runSrvHold struct {
	entered chan struct{}
	gate    *exectest.Gate
	calls   atomic.Int32
}

func newRunSrvHold(t testing.TB) *runSrvHold {
	h := &runSrvHold{entered: make(chan struct{}, 64), gate: exectest.NewGate()}
	t.Cleanup(h.gate.Open)
	return h
}

func (h *runSrvHold) run(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
	h.calls.Add(1)
	_, _ = io.WriteString(p.Stdout, "held")
	h.entered <- struct{}{}
	select {
	case <-h.gate.Opened():
		return exec.ExitCode(0), nil
	case <-ctx.Done():
		return exec.Exit{}, ctx.Err()
	}
}

// start calls Run in the background and returns what it will answer.
func (r *runSrvRig) start(req *execv2.RunRequest) <-chan runSrvAnswer {
	answers := make(chan runSrvAnswer, 1)
	go func() {
		res, err := r.run(req)
		answers <- runSrvAnswer{res, err}
	}()
	return answers
}

type runSrvAnswer struct {
	res *execv2.RunResult
	err error
}

func TestServedRunsAreHeldToTheTargetsLimitOfConcurrentRuns(t *testing.T) {
	hold := newRunSrvHold(t)
	rig := newRunSrvRig(t, engTarget(hold.run, runSrvLimits(exec.RunLimits{MaxConcurrent: 2})))
	first := rig.start(rig.request(runSrvID("one")))
	second := rig.start(rig.request(runSrvID("two")))
	awaitChannel(t, hold.entered, "the first run to start")
	awaitChannel(t, hold.entered, "the second run to start")

	_, err := rig.run(rig.request(runSrvID("three")))
	requireRunSrvRefusal(t, err, codes.ResourceExhausted, "LIMIT", "TOO_MANY_RUNS")
	if hold.calls.Load() != 2 {
		t.Errorf("the function ran %d times, want 2: the run over the limit was refused before it ran", hold.calls.Load())
	}

	hold.gate.Open()
	for _, answers := range []<-chan runSrvAnswer{first, second} {
		a := <-answers
		if a.err != nil || a.res.GetOutcome() != execv2.Outcome_OUTCOME_EXITED {
			t.Fatalf("a held run answered %v, %v", a.res, a.err)
		}
	}
	// The places are free again, and a run over the limit is admitted once one is.
	if res := rig.mustRun(rig.request(runSrvID("four"))); res.GetOutcome() != execv2.Outcome_OUTCOME_EXITED {
		t.Errorf("a run after the others ended: %v", res)
	}
}

// The limit is the limit of a kind of target: runs of another kind are not counted against it.
func TestServedRunsOfAnotherKindOfTargetDoNotShareALimit(t *testing.T) {
	hold := newRunSrvHold(t)
	other := exectest.NewFakeTarget(exectest.FakeRouting("fake", "other"), exectest.FakeScript(exectest.WriteOut("ok")))
	rig := newRunSrvRig(t, engTarget(hold.run, runSrvLimits(exec.RunLimits{MaxConcurrent: 1})), other)
	held := rig.start(rig.request(runSrvID("held")))
	awaitChannel(t, hold.entered, "the run to start")
	res := rig.mustRun(rig.request(runSrvID("other"), func(r *execv2.RunRequest) { r.Target = rig.conns.WireTarget(t, "other", "o-1") }))
	if res.GetOutcome() != execv2.Outcome_OUTCOME_EXITED || string(res.GetStdout()) != "ok" {
		t.Errorf("a run of another kind of target beside a full one answered %v", res)
	}
	hold.gate.Open()
	<-held
}

// One id space: a run's id is the request id (or one the SDK makes), and a session's is the host's, and the engine holds both
// under one rule: an id that is live is refused, for a run and for a session alike.
func TestServedRunAndSessionShareOneIDSpace(t *testing.T) {
	hold := newRunSrvHold(t)
	rig := newRunSrvRig(t, engTarget(hold.run, runSrvLimits(exec.RunLimits{MaxConcurrent: 4})))
	v2 := rig.server.V2(t)
	open := func(id string) (exec.Conn, error) {
		start := &execv2.Start{Target: rig.conns.WireTarget(t, "thing", "thing-1")}
		return v2.Open(rig.conns.Context(context.Background()), &execv2.OpenRequest{SessionId: id, Kind: &execv2.OpenRequest_Start{Start: start}})
	}
	requireConflict := func(err error) {
		t.Helper()
		var typed *exec.Error
		if !errors.As(err, &typed) || typed.Class != exec.ErrorClassConflict || typed.Reason != exec.ErrorReasonSessionExists {
			t.Fatalf("refused with %v, want CONFLICT / SESSION_EXISTS: the id is live", err)
		}
	}

	// a live run's id is refused as a session id, and as another run's
	running := rig.start(rig.request(runSrvID("shared")))
	awaitChannel(t, hold.entered, "the run to start")
	if conn, err := open("shared"); err == nil {
		_ = conn.Close()
		t.Error("a session was opened under the id of a live run")
	} else {
		requireConflict(err)
	}
	_, err := rig.run(rig.request(runSrvID("shared")))
	requireRunSrvRefusal(t, err, codes.Aborted, "CONFLICT", "SESSION_EXISTS")
	hold.gate.Open()
	if a := <-running; a.err != nil || a.res.GetOutcome() != execv2.Outcome_OUTCOME_EXITED {
		t.Fatalf("the run answered %v, %v", a.res, a.err)
	}

	// and a live session's id is refused as a run's
	hold2 := newRunSrvHold(t)
	rig2 := newRunSrvRig(t, engTarget(hold2.run))
	v2 = rig2.server.V2(t)
	open = func(id string) (exec.Conn, error) {
		start := &execv2.Start{Target: rig2.conns.WireTarget(t, "thing", "thing-1")}
		return v2.Open(rig2.conns.Context(context.Background()), &execv2.OpenRequest{SessionId: id, Kind: &execv2.OpenRequest_Start{Start: start}})
	}
	conn, err := open("session-1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	awaitChannel(t, hold2.entered, "the session to start")
	_, err = rig2.run(rig2.request(runSrvID("session-1")))
	requireRunSrvRefusal(t, err, codes.Aborted, "CONFLICT", "SESSION_EXISTS")
}

// Until the run's result is read, its id is held; once it is read it is free, so that a host that reads a result and runs the
// same id again is never refused for the run that has just ended.
func TestServedRunsIDIsFreeAsSoonAsItsResultIsRead(t *testing.T) {
	rig := newRunSrvRig(t, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		_, _ = io.WriteString(p.Stdout, "x")
		return exec.ExitCode(0), nil
	}))
	for i := range 200 {
		res, err := rig.run(rig.request(runSrvID("same")))
		if err != nil || res.GetOutcome() != execv2.Outcome_OUTCOME_EXITED {
			t.Fatalf("run %d of the same id answered %v, %v: the id of an ended run is free", i, res, err)
		}
	}
}
