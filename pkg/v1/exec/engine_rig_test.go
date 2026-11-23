package exec_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"runtime"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/internal/exectestkit"
	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/log/logtest"
	sdkdiag "github.com/omniviewdev/plugin-sdk/pkg/diagnostics"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The tests of the engine drive it in process, through the same handle the conformance suite uses (internal/exechook): an
// Open or a Run admitted as the served RPC will admit it, a command to feed and read, and its one typed end.

// engineWait bounds a wait for something that has to happen: it turns a hang into a failure and decides no outcome.
const engineWait = 30 * time.Second

// engineRig is one in-process engine over a fake connection registry, with its logs and diagnostics recorded.
type engineRig struct {
	t     testing.TB
	conns *exectestkit.Connections
	eng   exechook.Engine
	logs  *logtest.RecordingSink
	diag  *sdkdiag.Registry
}

// engineRigConfig is what a test changes about its rig.
type engineRigConfig struct {
	// grace shortens how long an end waits for a function that ignores its cancelled context.
	grace time.Duration
	// resizeUnread shortens how long a size may wait for a function that does not read Proc.Resize.
	resizeUnread time.Duration
	// metricsEvery shortens how often a running session reports its traffic.
	metricsEvery time.Duration
	// noConnections binds no connection registry, as a plugin without a resource provider.
	noConnections bool
	// diag is the diagnostics registry the engine publishes to; a test that wants two engines on one registry gives both the
	// same one.
	diag *sdkdiag.Registry
	// optionsDeadline and classifyDeadline shorten how long the engine waits for an Options callback and a Classify function
	// before it gives up on them.
	optionsDeadline, classifyDeadline time.Duration
	// killWait shortens (or, for a test that has to see a function return after a kill, lengthens) how long a kill waits for a
	// function to return before the engine gives up on it; shutdownGrace is the grace of a function when the engine closes.
	killWait, shutdownGrace time.Duration
	// optionsInFlight is how many Options callbacks of one target the unary Options RPC may have in flight.
	optionsInFlight int
}

// newEngineRig builds an engine for targets and closes it when the test ends: every command ended and removed, within a
// bound, and the engine holding nothing.
func newEngineRig(t testing.TB, cfg engineRigConfig, targets ...exec.Target) *engineRig {
	t.Helper()
	r := &engineRig{t: t, logs: &logtest.RecordingSink{}, diag: cfg.diag}
	if r.diag == nil {
		r.diag = sdkdiag.NewRegistry()
	}
	r.conns = exectestkit.NewConnectionsT(t, "fake")
	logger := logging.New(logging.Config{
		Backend: logging.NewSinkBackend(r.logs),
		Level:   logging.NewLevelController(logging.LevelDebug),
	})
	engineCfg := exechook.EngineConfig{
		Logger: logger, Diagnostics: r.diag,
		CloseGrace: cfg.grace, ResizeUnreadAfter: cfg.resizeUnread, MetricsEvery: cfg.metricsEvery,
		OptionsDeadline: cfg.optionsDeadline, ClassifyDeadline: cfg.classifyDeadline,
		KillWait: cfg.killWait, ShutdownGrace: cfg.shutdownGrace, OptionsInFlight: cfg.optionsInFlight,
	}
	if !cfg.noConnections {
		engineCfg.Connections = r.conns
	}
	eng, err := exechook.NewEngine(targets, engineCfg)
	if err != nil {
		t.Fatalf("build the engine: %v", err)
	}
	r.eng = eng
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), engineWait)
		defer cancel()
		if err := eng.Close(ctx); err != nil {
			t.Errorf("the engine did not close within %v: %v; it holds %v", engineWait, err, eng.Live())
			return
		}
		if live := eng.Live(); len(live) != 0 {
			t.Errorf("a closed engine holds %v", live)
		}
	})
	return r
}

// request is the context a host's call arrives with: the connection and the catalog pin.
func (r *engineRig) request() context.Context { return r.conns.Context(context.Background()) }

// openRequest is the first frame of an Open for the fake target's thing, a terminal by default.
func (r *engineRig) openRequest(id string, mods ...func(*execv2.Start)) *execv2.OpenRequest {
	start := &execv2.Start{Target: r.conns.WireTarget(r.t, "thing", "thing-1"), Tty: true}
	for _, mod := range mods {
		mod(start)
	}
	return &execv2.OpenRequest{SessionId: id, Kind: &execv2.OpenRequest_Start{Start: start}}
}

// open starts a session.
func (r *engineRig) open(id string, mods ...func(*execv2.Start)) (exechook.Command, error) {
	return r.eng.Open(r.request(), r.openRequest(id, mods...))
}

// mustOpen starts a session that has to be admitted.
func (r *engineRig) mustOpen(id string, mods ...func(*execv2.Start)) exechook.Command {
	r.t.Helper()
	cmd, err := r.open(id, mods...)
	if err != nil {
		r.t.Fatalf("open %q: %v", id, err)
	}
	return cmd
}

// withTTY and withoutTTY set whether the session runs with a terminal.
func withoutTTY(s *execv2.Start) { s.Tty = false }

// withSize asks for a terminal of a size.
func withSize(cols, rows uint32) func(*execv2.Start) {
	return func(s *execv2.Start) { s.Size = &execv2.Size{Cols: cols, Rows: rows} }
}

// withSelection selects a choice of an option.
func withSelection(name, value string) func(*execv2.Start) {
	return func(s *execv2.Start) {
		s.Target.Selection = append(s.Target.Selection, &execv2.NameValue{Name: name, Value: value})
	}
}

// engTarget is a target of the fake's routing and defaults whose function is fn, and whose info mods may change.
func engTarget(fn exec.ExecFunc, mods ...func(*exec.TargetInfo)) exec.Target {
	target := exectest.NewFakeTarget()
	target.Exec = fn
	for _, mod := range mods {
		mod(&target.Info)
	}
	return target
}

// blockUntilCancelled is a function that runs until its context ends.
func blockUntilCancelled(ctx context.Context, _ *exec.Proc) (exec.Exit, error) {
	<-ctx.Done()
	return exec.Exit{}, ctx.Err()
}

// awaitDone waits for the command to end.
func awaitDone(t testing.TB, cmd exechook.Command) exechook.Result {
	t.Helper()
	select {
	case <-cmd.Done():
	case <-time.After(engineWait):
		t.Fatalf("command %q did not end within %v", cmd.ID(), engineWait)
	}
	return cmd.Result()
}

// awaitChannel waits for c to be closed.
func awaitChannel(t testing.TB, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(engineWait):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// eventually polls cond until it holds. The condition is the oracle; the wait only keeps a bug from hanging the suite.
func eventually(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(engineWait)
	for spins := 0; !cond(); spins++ {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		if spins < 100 {
			runtime.Gosched()
		} else {
			time.Sleep(time.Millisecond)
		}
	}
}

// output is everything a command's output queue gave its reader.
type output struct {
	data    []byte
	chunks  []exechook.Chunk
	dropped []exechook.Drop
}

// drain reads the command's output to its end: the bytes in order, the chunks, and the ranges it lost.
func drain(t testing.TB, cmd exechook.Command) output {
	t.Helper()
	var out output
	buf := make([]byte, 4096)
	ctx, cancel := context.WithTimeout(context.Background(), engineWait)
	defer cancel()
	for {
		chunk, err := cmd.Read(ctx, buf)
		out.dropped = append(out.dropped, chunk.Dropped...)
		if chunk.N > 0 {
			out.data = append(out.data, buf[:chunk.N]...)
			out.chunks = append(out.chunks, chunk)
		}
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("read the output of %q: %v", cmd.ID(), err)
		}
	}
}

// ---------------------------------------------------------------------------------------------------------------
// the ends the tests expect
// ---------------------------------------------------------------------------------------------------------------

func exited(code int64, source execv2.ExitSource, survive *bool) *execv2.End {
	return &execv2.End{Kind: &execv2.End_Exited{Exited: &execv2.Exited{ExitCode: code, Source: source}}, RemoteMaySurvive: survive}
}

func signaled(name string, number int32, source execv2.ExitSource, survive *bool) *execv2.End {
	return &execv2.End{
		Kind:             &execv2.End_Signaled{Signaled: &execv2.Signaled{Signal: name, SignalNumber: number, Source: source}},
		RemoteMaySurvive: survive,
	}
}

func closedBy(by execv2.ClosedBy, survive *bool) *execv2.End {
	return &execv2.End{Kind: &execv2.End_Closed{Closed: &execv2.Closed{By: by}}, RemoteMaySurvive: survive}
}

func lost(cause execv2.LostCause) *execv2.End {
	return &execv2.End{Kind: &execv2.End_Lost{Lost: &execv2.Lost{Cause: cause}}, RemoteMaySurvive: new(true)}
}

func failed(class execv2.ErrorClass, reason execv2.ErrorReason, stage execv2.FailureStage, facts *execv2.ErrorFacts) *execv2.End {
	return &execv2.End{Kind: &execv2.End_Failed{Failed: &execv2.Failed{Class: class, Reason: reason, Stage: stage, Facts: facts}}}
}

// assertEnd compares the end of a command with the one the contract says.
func assertEnd(t testing.TB, got, want *execv2.End) {
	t.Helper()
	if !proto.Equal(got, want) {
		t.Errorf("the end is\n%s\nwant\n%s", prototext.Format(got), prototext.Format(want))
	}
}

// engContainsCanary reports whether any of the strings holds the canary.
func engContainsCanary(canary string, texts ...string) bool {
	for _, text := range texts {
		if bytes.Contains([]byte(text), []byte(canary)) {
			return true
		}
	}
	return false
}
