package exec_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// RunStream on the wire: the events of a run as they happen, in the order the contract gives them: RunStarted before the first
// Output, Output events with their offsets up to each stream's cap, RunProgress at the SDK's pace, one RunEnded, and nothing
// after it.

// stream opens a RunStream; the stream ends with the test.
func (r *runSrvRig) stream(ctx context.Context, req *execv2.RunRequest) execv2.Exec_RunStreamClient {
	r.t.Helper()
	stream, err := r.raw.RunStream(ctx, req)
	if err != nil {
		r.t.Fatalf("RunStream: %v", err)
	}
	return stream
}

// cancellable is the context of a call that the test may cancel, and that ends with the test.
func (r *runSrvRig) cancellable() (context.Context, context.CancelFunc) {
	r.t.Helper()
	ctx, cancel := context.WithCancel(r.ctx())
	r.t.Cleanup(cancel)
	return ctx, cancel
}

// runSrvCollect reads a stream to its end: the events in order, and how it ended (io.EOF for the status OK).
func runSrvCollect(s execv2.Exec_RunStreamClient) ([]*execv2.RunEvent, error) {
	var events []*execv2.RunEvent
	for {
		event, err := s.Recv()
		if err != nil {
			return events, err
		}
		events = append(events, event)
	}
}

// runSrvKind names the kind of an event.
func runSrvKind(e *execv2.RunEvent) string {
	switch e.GetEvent().(type) {
	case *execv2.RunEvent_Started:
		return "started"
	case *execv2.RunEvent_Output:
		return "output"
	case *execv2.RunEvent_Progress:
		return "progress"
	case *execv2.RunEvent_Ended:
		return "ended"
	}
	return "unknown"
}

func runSrvKinds(events []*execv2.RunEvent) []string {
	kinds := make([]string, len(events))
	for i, e := range events {
		kinds[i] = runSrvKind(e)
	}
	return kinds
}

// runSrvRequireKinds requires the kinds of the events, in order.
func runSrvRequireKinds(t testing.TB, events []*execv2.RunEvent, want ...string) {
	t.Helper()
	if got := runSrvKinds(events); !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
}

// runSrvOutput concatenates the output events of a stream and checks their offsets: in arrival order across both streams, and
// contiguous from zero when nothing was left out (a stream that stops delivering at its cap leaves a gap, as the engine's own
// offsets do).
func runSrvOutput(t testing.TB, events []*execv2.RunEvent, contiguous bool) (stdout, stderr []byte) {
	t.Helper()
	var next uint64
	for _, e := range events {
		o := e.GetOutput()
		if o == nil {
			continue
		}
		if (contiguous && o.GetOffset() != next) || o.GetOffset() < next {
			t.Fatalf("an Output event at offset %d, want %d (contiguous: %v): offsets count the bytes the command wrote", o.GetOffset(), next, contiguous)
		}
		if len(o.GetData()) == 0 || len(o.GetData()) > 64<<10 {
			t.Fatalf("an Output event of %d bytes: want 1 to 65,536", len(o.GetData()))
		}
		next = o.GetOffset() + uint64(len(o.GetData()))
		switch o.GetStream() {
		case execv2.Stream_STREAM_STDOUT:
			stdout = append(stdout, o.GetData()...)
		case execv2.Stream_STREAM_STDERR:
			stderr = append(stderr, o.GetData()...)
		default:
			t.Fatalf("an Output event of stream %v: a run has no terminal", o.GetStream())
		}
	}
	return stdout, stderr
}

func runSrvEnded(t testing.TB, events []*execv2.RunEvent) *execv2.RunResult {
	t.Helper()
	if len(events) == 0 || events[len(events)-1].GetEnded() == nil {
		t.Fatalf("the last event is %v, want RunEnded", runSrvKinds(events))
	}
	return events[len(events)-1].GetEnded().GetResult()
}

func TestServedRunStreamSendsStartedThenTheOutputInOrderThenOneRunEndedAndNothingAfter(t *testing.T) {
	rig := newRunSrvRig(t, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		_, _ = io.WriteString(p.Stdout, "a")
		_, _ = io.WriteString(p.Stderr, "b")
		_, _ = io.WriteString(p.Stdout, "cd")
		return exec.ExitCode(0), nil
	}))
	events, err := runSrvCollect(rig.stream(rig.ctx(), rig.request(runSrvID("stream-1"))))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("the stream ended with %v, want io.EOF: the status OK, after RunEnded", err)
	}
	runSrvRequireKinds(t, events, "started", "output", "output", "output", "ended")

	started := events[0].GetStarted()
	if !proto.Equal(started.GetTarget(), fakeTargetFacts) || !proto.Equal(started.GetProgram(), &execv2.Program{Argv: []string{"prog", "a b"}, Name: "prog"}) {
		t.Errorf("RunStarted %v: want the target and the program of the run", started)
	}
	type output struct {
		stream execv2.Stream
		offset uint64
		data   string
	}
	var got []output
	for _, e := range events[1:4] {
		o := e.GetOutput()
		got = append(got, output{o.GetStream(), o.GetOffset(), string(o.GetData())})
	}
	want := []output{{execv2.Stream_STREAM_STDOUT, 0, "a"}, {execv2.Stream_STREAM_STDERR, 1, "b"}, {execv2.Stream_STREAM_STDOUT, 2, "cd"}}
	if !slices.Equal(got, want) {
		t.Errorf("output events %v, want %v", got, want)
	}
	requireRunSrvResult(t, runSrvEnded(t, events), &execv2.RunResult{
		RequestId:        "stream-1",
		Outcome:          execv2.Outcome_OUTCOME_EXITED,
		ExitCode:         new(int64(0)),
		ExitSource:       execv2.ExitSource_EXIT_SOURCE_PROCESS,
		StdoutAccount:    goodAccount(3),
		StderrAccount:    goodAccount(1),
		StartObserved:    true,
		RemoteMaySurvive: new(false),
		Target:           fakeTargetFacts,
		Program:          &execv2.Program{Argv: []string{"prog", "a b"}, Name: "prog"},
	})
}

// A stream cannot keep a head and a tail: what it delivers is the beginning, up to the cap of each stream, and the rest is
// counted, so the result says TRUNCATED.
func TestServedRunStreamDeliversEachStreamUpToItsCapAndCountsTheRest(t *testing.T) {
	const written = 300_000
	rig := newRunSrvRig(t, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		data := runSrvText(written)
		for rest := data; len(rest) > 0; {
			k := min(len(rest), 32<<10)
			if _, err := p.Stdout.Write(rest[:k]); err != nil {
				return exec.Exit{}, err
			}
			if _, err := p.Stderr.Write(rest[:min(k, 100)]); err != nil {
				return exec.Exit{}, err
			}
			rest = rest[k:]
		}
		return exec.ExitCode(0), nil
	}))
	// the policy asks for the tail: a stream cannot give it, it gives the beginning
	events, err := runSrvCollect(rig.stream(rig.ctx(), rig.request(runSrvCaps(100_000, 1000, execv2.TruncatePolicy_TRUNCATE_POLICY_TAIL))))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("stream ended with %v", err)
	}
	stdout, stderr := runSrvOutput(t, events, false)
	data := runSrvText(written)
	if string(stdout) != string(data[:100_000]) {
		t.Errorf("stdout delivered %d bytes, want the first 100,000 of the output and no others", len(stdout))
	}
	stderrWritten := 100 * ((written + 32<<10 - 1) / (32 << 10))
	if len(stderr) != min(1000, stderrWritten) || string(stderr[:100]) != string(data[:100]) {
		t.Errorf("stderr delivered %d bytes, want %d from the beginning", len(stderr), min(1000, stderrWritten))
	}
	res := runSrvEnded(t, events)
	wantStdout := &execv2.OutputAccount{
		TotalBytes: written, KeptBytes: 100_000, DroppedBytes: written - 100_000, Truncated: true, HeadBytes: 100_000, ValidUtf8: true,
		Completeness: completeTruncated,
	}
	if !proto.Equal(res.GetStdoutAccount(), wantStdout) {
		t.Errorf("stdout account %v, want %v", res.GetStdoutAccount(), wantStdout)
	}
	if got := res.GetStderrAccount(); got.GetTotalBytes() != uint64(stderrWritten) || got.GetKeptBytes() != uint64(len(stderr)) || got.GetTailBytes() != 0 ||
		got.GetKeptBytes()+got.GetDroppedBytes() != got.GetTotalBytes() {
		t.Errorf("stderr account %v for %d bytes written and %d delivered", got, stderrWritten, len(stderr))
	}
	if len(res.GetStdout()) != 0 || len(res.GetStderr()) != 0 {
		t.Error("RunEnded carries the payload that was streamed")
	}
}

func TestServedRunStreamEndsAtItsTimeoutAfterTheOutputSoFar(t *testing.T) {
	rig := newRunSrvRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		_, _ = io.WriteString(p.Stdout, "x")
		<-ctx.Done()
		return exec.Exit{}, ctx.Err()
	}))
	events, err := runSrvCollect(rig.stream(rig.ctx(), rig.request(runSrvTimeout(50))))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("stream ended with %v", err)
	}
	runSrvRequireKinds(t, events, "started", "output", "ended")
	res := runSrvEnded(t, events)
	if res.GetOutcome() != execv2.Outcome_OUTCOME_TIMED_OUT || res.GetExitSource() != execv2.ExitSource_EXIT_SOURCE_HOST {
		t.Errorf("result %v, want TIMED_OUT from the host", res)
	}
}

// A refusal is a status and no event at all: nothing ran.
func TestServedRunStreamRefusalIsAStatusWithNoEvent(t *testing.T) {
	var ran atomic.Int32
	rig := newRunSrvRig(t, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
		ran.Add(1)
		return exec.ExitCode(0), nil
	}))
	for name, mod := range map[string]func(*execv2.RunRequest){
		"no argv":           runSrvArgv(),
		"a timeout too big": runSrvTimeout(1 << 30),
	} {
		t.Run(name, func(t *testing.T) {
			events, err := runSrvCollect(rig.stream(rig.ctx(), rig.request(mod)))
			requireRunSrvRefusal(t, err, codes.InvalidArgument, "INVALID_REQUEST", "")
			if len(events) != 0 {
				t.Errorf("a refused stream sent %v first", runSrvKinds(events))
			}
		})
	}
	if ran.Load() != 0 {
		t.Error("the function ran for a request that was refused")
	}
}

// A run that fails or is lost before its start is observed sends RunEnded alone: a start is never made up.
func TestServedRunStreamThatEndsBeforeItsStartWasObservedSendsRunEndedAlone(t *testing.T) {
	rig := newRunSrvRig(t, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		if p.Request.Labels["how"] == "lost" {
			return exec.ExitLost(exec.LostLinkDead), nil
		}
		return exec.Exit{}, errors.New("the target could not be reached")
	}))
	for how, want := range map[string]execv2.Outcome{"lost": execv2.Outcome_OUTCOME_LOST, "failed": execv2.Outcome_OUTCOME_FAILED} {
		t.Run(how, func(t *testing.T) {
			events, err := runSrvCollect(rig.stream(rig.ctx(), rig.request(runSrvLabel("how", how))))
			if !errors.Is(err, io.EOF) {
				t.Fatalf("stream ended with %v", err)
			}
			runSrvRequireKinds(t, events, "ended")
			res := runSrvEnded(t, events)
			if res.GetOutcome() != want || res.GetStartObserved() || res.GetTarget() != nil || res.GetProgram() != nil {
				t.Errorf("result %v, want %v with no observed start and no facts", res, want)
			}
			if how == "failed" && res.GetFailed().GetStage() != execv2.FailureStage_FAILURE_STAGE_START {
				t.Errorf("failure %v, want stage START", res.GetFailed())
			}
		})
	}
}

// A command that exits with a status and never wrote or reported has an observed start, so the host is told it started.
func TestServedRunStreamOfACommandThatSaidNothingStillSendsRunStartedBeforeRunEnded(t *testing.T) {
	rig := newRunSrvRig(t, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) { return exec.ExitCode(7), nil }))
	events, err := runSrvCollect(rig.stream(rig.ctx(), rig.request()))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("stream ended with %v", err)
	}
	runSrvRequireKinds(t, events, "started", "ended")
	res := runSrvEnded(t, events)
	if !res.GetStartObserved() || res.GetExitCode() != 7 {
		t.Errorf("result %v, want an exit with code 7 and an observed start", res)
	}
}

func TestServedRunStreamSendsTheProgramThatReallyRanAtItsStart(t *testing.T) {
	rig := newRunSrvRig(t, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		p.Started(exec.SessionFacts{Program: exec.Program{Name: "custom", Path: "/usr/bin/custom"}})
		_, _ = io.WriteString(p.Stdout, "go")
		return exec.ExitCode(0), nil
	}))
	events, err := runSrvCollect(rig.stream(rig.ctx(), rig.request(runSrvArgv("custom", "--flag"))))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("stream ended with %v", err)
	}
	runSrvRequireKinds(t, events, "started", "output", "ended")
	want := &execv2.Program{Argv: []string{"custom", "--flag"}, Name: "custom", Path: "/usr/bin/custom"}
	if got := events[0].GetStarted().GetProgram(); !proto.Equal(got, want) {
		t.Errorf("RunStarted program %v, want %v", got, want)
	}
	if got := runSrvEnded(t, events).GetProgram(); !proto.Equal(got, want) {
		t.Errorf("RunEnded program %v, want %v", got, want)
	}
}

// A host that goes away ends the run: the function's context ends, and the engine holds nothing.
func TestServedRunStreamWhoseClientGoesAwayEndsTheRun(t *testing.T) {
	entered := make(chan struct{})
	observed := make(chan struct{})
	rig := newRunSrvRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		_, _ = io.WriteString(p.Stdout, "x")
		close(entered)
		<-ctx.Done()
		close(observed)
		return exec.Exit{}, ctx.Err()
	}, runSrvLimits(exec.RunLimits{MaxConcurrent: 1})))
	ctx, cancel := rig.cancellable()
	stream := rig.stream(ctx, rig.request(runSrvTimeout(60_000)))
	awaitChannel(t, entered, "the run to start")
	if event, err := stream.Recv(); err != nil || event.GetStarted() == nil {
		t.Fatalf("first event %v, %v, want RunStarted", event, err)
	}
	cancel()
	awaitChannel(t, observed, "the function to see its context end: a client that left ends the run")
	eventually(t, "the engine to hold nothing", func() bool { return len(exec.RunEngineLive(rig.server.Plugin)) == 0 })
	var last error
	for last == nil {
		_, last = stream.Recv() // the events that were sent before the client left, and then how the stream ended
	}
	if status.Code(last) != codes.Canceled {
		t.Errorf("the cancelled stream ended with %v, want Canceled", last)
	}
}

// ---------------------------------------------------------------------------------------------------------------
// what only a stream the test controls can show: the pace of the progress, a client that cannot take what is sent
// ---------------------------------------------------------------------------------------------------------------

// fakeRunStream is a RunStream as the server sees it, with a Send that the test decides: the transport stands behind a real
// stream, and here the test stands in for it.
type fakeRunStream struct {
	grpc.ServerStream
	ctx  context.Context
	send func(*execv2.RunEvent) error
	busy atomic.Int32
	t    testing.TB
}

func (s *fakeRunStream) Context() context.Context { return s.ctx }

func (s *fakeRunStream) Send(e *execv2.RunEvent) error {
	if s.busy.Add(1) != 1 {
		s.t.Errorf("two goroutines sent on one stream at once: a stream takes one sender at a time")
	}
	defer s.busy.Add(-1)
	return s.send(e)
}

// incoming is the context a call arrives with at the server: what the middleware would have made of the call's metadata.
func (r *runSrvRig) incoming(parent context.Context) context.Context {
	r.t.Helper()
	out, err := resourceplugin.OutgoingSessionContext(r.conns.Context(parent))
	if err != nil {
		r.t.Fatalf("the context of the call: %v", err)
	}
	md, _ := metadata.FromOutgoingContext(out)
	return metadata.NewIncomingContext(out, md)
}

// serveStream serves a RunStream on a stream the test implements, and returns what the handler returned. As the transport does,
// it ends the stream's context when the handler returns: that is what ends a command that the handler left running.
func (r *runSrvRig) serveStream(ctx context.Context, req *execv2.RunRequest, send func(*execv2.RunEvent) error) <-chan error {
	r.t.Helper()
	returned := make(chan error, 1)
	streamCtx, end := context.WithCancel(r.incoming(ctx))
	stream := &fakeRunStream{ctx: streamCtx, send: send, t: r.t}
	go func() {
		err := exec.RunStreamOn(r.server.Plugin, req, stream)
		end()
		returned <- err
	}()
	return returned
}

// The SDK reports progress at its own pace: on a tick, and only when output has advanced since the last one, and never before
// RunStarted. The ticks are the test's own.
func TestServedRunStreamReportsProgressOnATickOnlyWhileTheOutputAdvanced(t *testing.T) {
	gates := [3]*exectest.Gate{exectest.NewGate(), exectest.NewGate(), exectest.NewGate()}
	rig := newRunSrvRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		for i, text := range []string{"a", "bc", ""} {
			select {
			case <-gates[i].Opened():
			case <-ctx.Done():
				return exec.Exit{}, ctx.Err()
			}
			_, _ = io.WriteString(p.Stdout, text)
		}
		return exec.ExitCode(0), nil
	}))
	for _, g := range gates {
		t.Cleanup(g.Open)
	}
	ticks := make(chan time.Time)
	handled := make(chan struct{}, 1)
	var stopped atomic.Bool
	exec.SetRunSeams(rig.server.Plugin, exec.RunSeams{
		ProgressTicks: func() (<-chan time.Time, func()) { return ticks, func() { stopped.Store(true) } },
		TickHandled: func() {
			select {
			case handled <- struct{}{}:
			default:
			}
		},
	})

	events := make(chan *execv2.RunEvent, 64)
	returned := rig.serveStream(context.Background(), rig.request(), func(e *execv2.RunEvent) error {
		events <- proto.Clone(e).(*execv2.RunEvent)
		return nil
	})
	next := func(want string) *execv2.RunEvent {
		t.Helper()
		select {
		case e := <-events:
			if runSrvKind(e) != want {
				t.Fatalf("event %s, want %s", runSrvKind(e), want)
			}
			return e
		case <-time.After(engineWait):
			t.Fatalf("no %s event within %v", want, engineWait)
			return nil
		}
	}
	// A tick is over when the handler has handled it, and not when it has taken it: the handler that has taken a tick reads how far the output has
	// got when it gets to it, and on a busy machine that is after the author that the test let write has written. Only a tick that is over finds
	// what the test says it finds.
	tick := func() {
		t.Helper()
		select {
		case ticks <- time.Time{}:
		case <-time.After(engineWait):
			t.Fatal("the handler took no tick of progress")
		}
		select {
		case <-handled:
		case <-time.After(engineWait):
			t.Fatal("the handler took a tick of progress and did not finish with it")
		}
	}

	tick() // nothing has been written: there is nothing to report
	gates[0].Open()
	next("started")
	next("output")
	tick()
	if p := next("progress").GetProgress(); p.GetBytesOut() != 1 {
		t.Errorf("progress after one byte: %v, want bytes_out 1", p)
	}
	tick() // nothing new since the last report
	gates[1].Open()
	next("output")
	tick()
	if p := next("progress").GetProgress(); p.GetBytesOut() != 3 {
		t.Errorf("progress after three bytes: %v, want bytes_out 3", p)
	}
	gates[2].Open()
	next("ended")
	if err := <-returned; err != nil {
		t.Errorf("the stream ended with %v", err)
	}
	if !stopped.Load() {
		t.Error("the progress ticker was not stopped when the stream ended")
	}
	select {
	case e := <-events:
		t.Errorf("an event %s after RunEnded", runSrvKind(e))
	default:
	}
}

// A host that stops taking events is given the post-exit drain to take what is left, and not longer: the stream then ends with a
// status of the transport's kind (the output was not delivered, which says nothing about the command), and holds nothing.
func TestServedRunStreamWhoseClientTakesNothingEndsWhenThePostExitDrainRunsOut(t *testing.T) {
	var ran atomic.Int32
	rig := newRunSrvRig(t, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		ran.Add(1)
		_, _ = io.WriteString(p.Stdout, "abc")
		return exec.ExitCode(0), nil
	}, runSrvLimits(exec.RunLimits{PostExitDrain: time.Millisecond})))
	ctx, cancel := context.WithTimeout(context.Background(), engineWait)
	t.Cleanup(cancel)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	returned := rig.serveStream(ctx, rig.request(), func(*execv2.RunEvent) error {
		<-release // a host whose window is closed: the send waits
		return errors.New("the stream is gone")
	})
	select {
	case err := <-returned:
		st, ok := status.FromError(err)
		if !ok || st.Code() != codes.DeadlineExceeded {
			t.Fatalf("the stream ended with %v, want a status of DeadlineExceeded", err)
		}
		if decodeRunSrvRefusal(t, err).domain != "" {
			t.Errorf("the status %v is typed as an exec refusal: a refusal says nothing ran, and this command ran", err)
		}
	case <-time.After(engineWait):
		t.Fatal("the stream did not end though its host takes nothing and the command has ended")
	}
	if ran.Load() != 1 {
		t.Errorf("the function ran %d times", ran.Load())
	}
	eventually(t, "the engine to hold nothing", func() bool { return len(exec.RunEngineLive(rig.server.Plugin)) == 0 })
}

// A send that fails is a client that is gone: the run ends, the function's context with it, and the stream returns the failure.
func TestServedRunStreamWhoseSendFailsEndsTheRun(t *testing.T) {
	observed := make(chan struct{})
	rig := newRunSrvRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		_, _ = io.WriteString(p.Stdout, "x")
		<-ctx.Done()
		close(observed)
		return exec.Exit{}, ctx.Err()
	}, runSrvLimits(exec.RunLimits{MaxConcurrent: 1})))
	gone := errors.New("the client is gone")
	var sends atomic.Int32
	returned := rig.serveStream(context.Background(), rig.request(runSrvTimeout(60_000)), func(*execv2.RunEvent) error {
		sends.Add(1)
		return gone
	})
	select {
	case err := <-returned:
		if !errors.Is(err, gone) {
			t.Errorf("the stream ended with %v, want the failure of the send", err)
		}
	case <-time.After(engineWait):
		t.Fatal("the stream did not end when its send failed")
	}
	awaitChannel(t, observed, "the function to see its context end")
	eventually(t, "the engine to hold nothing", func() bool { return len(exec.RunEngineLive(rig.server.Plugin)) == 0 })
	if sends.Load() != 1 {
		t.Errorf("%d events were sent on a stream whose first send failed, want 1", sends.Load())
	}
}

// What the reader of a run's output has not taken within the post-exit drain is not claimed: the result is not complete, though
// every byte it read was kept.
func TestServedRunWhoseOutputWasNotReadToItsEndWithinThePostExitDrainIsNotComplete(t *testing.T) {
	rig := newRunSrvRig(t, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		_, _ = io.WriteString(p.Stdout, "abc")
		return exec.ExitCode(0), nil
	}, runSrvLimits(exec.RunLimits{PostExitDrain: time.Millisecond})))
	var reads atomic.Int32
	exec.SetRunSeams(rig.server.Plugin, exec.RunSeams{BeforeRead: func(ctx context.Context) {
		if reads.Add(1) > 1 {
			<-ctx.Done() // the second read never happens: the reader is held until the call is over
		}
	}})
	res := rig.mustRun(rig.request())
	if res.GetOutcome() != execv2.Outcome_OUTCOME_EXITED || string(res.GetStdout()) != "abc" {
		t.Fatalf("result %v, want the exit and the bytes the reader took", res)
	}
	for name, account := range map[string]*execv2.OutputAccount{"stdout": res.GetStdoutAccount(), "stderr": res.GetStderrAccount()} {
		if account.GetCompleteness() != completeUnknown {
			t.Errorf("%s completeness %v, want UNSPECIFIED: the stream was not read to its end", name, account.GetCompleteness())
		}
	}
}

// Everything a stream is sent is one RunEvent, whole and in order, whatever else the run does at the same time.
func TestServedRunStreamNeverHasTwoSendersAtOnce(t *testing.T) {
	rig := newRunSrvRig(t, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		for i := range 200 {
			_, _ = fmt.Fprintf(p.Stdout, "line %d\n", i)
		}
		return exec.ExitCode(0), nil
	}))
	ticks := make(chan time.Time, 1)
	exec.SetRunSeams(rig.server.Plugin, exec.RunSeams{ProgressTicks: func() (<-chan time.Time, func()) { return ticks, func() {} }})
	var mu sync.Mutex
	var got []string
	returned := rig.serveStream(context.Background(), rig.request(), func(e *execv2.RunEvent) error {
		select {
		case ticks <- time.Time{}: // a tick for every event: the handler sends progress as often as it may
		default:
		}
		mu.Lock()
		got = append(got, runSrvKind(e))
		mu.Unlock()
		return nil
	})
	if err := <-returned; err != nil {
		t.Fatalf("the stream ended with %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) < 3 || got[0] != "started" || got[len(got)-1] != "ended" || strings.Count(strings.Join(got, ","), "ended") != 1 {
		t.Errorf("events %v: want RunStarted first and exactly one RunEnded last", got)
	}
}

func TestServedRunStreamRefusalOfAStaleCatalogPinKeepsTheResourceLanesStatus(t *testing.T) {
	rig := newRunSrvRig(t, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) { return exec.ExitCode(0), nil }))
	ctx := rig.ctx()
	rig.conns.SetRevision("catalog-2")
	_, err := runSrvCollect(rig.stream(ctx, rig.request()))
	if got := decodeRunSrvRefusal(t, err); got.domain != "omniview.resource.v2" || got.class != "RESET_REQUIRED" {
		t.Errorf("a stale pin was refused with %+v, want the resource lane's RESET_REQUIRED", got)
	}
	_, runErr := rig.raw.Run(ctx, rig.request())
	if got := decodeRunSrvRefusal(t, runErr); got.domain != "omniview.resource.v2" || got.class != "RESET_REQUIRED" {
		t.Errorf("Run with a stale pin was refused with %+v, want the resource lane's RESET_REQUIRED", got)
	}
}
