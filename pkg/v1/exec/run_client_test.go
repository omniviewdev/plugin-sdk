package exec

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/log/logtest"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The client of Run and RunStream, tested against the generated client of a plugin that answers what a test scripts: the
// context the plugin received, the request it got and how often it was called are what the client did, and no test of this file
// decides anything by a clock. The same client over a real transport is run_client_wire_test.go.

// runWait bounds a wait for something that has to happen. It turns a hang into a failure and decides no outcome.
const runWait = 10 * time.Second

// scriptedRunStream is the client side of a RunStream that yields what a test scripts: its events, then its end.
type scriptedRunStream struct {
	grpc.ClientStream

	ctx    context.Context
	events []*execv2.RunEvent
	// end is what Recv returns once the events are read: io.EOF for a stream that ended with status OK, or an error.
	end error
	// blocked, when set, makes the Recv that has no event to give close it and wait for the stream's context to end, and then
	// return the status of that end: a stream that never ends by itself.
	blocked chan struct{}
	// recvs counts the calls of Recv.
	recvs int
}

func (s *scriptedRunStream) Context() context.Context { return s.ctx }

func (s *scriptedRunStream) Recv() (*execv2.RunEvent, error) {
	s.recvs++
	if len(s.events) > 0 {
		event := s.events[0]
		s.events = s.events[1:]
		return event, nil
	}
	if s.blocked != nil {
		close(s.blocked)
		<-s.ctx.Done()
		return nil, status.FromContextError(s.ctx.Err()).Err()
	}
	return nil, s.end
}

// runRPC is the generated client of a plugin that answers Run and RunStream with canned answers and keeps what it received.
// The other methods of the service are the nil interface's: a test that calls one panics.
type runRPC struct {
	execv2.ExecClient

	result *execv2.RunResult
	err    error
	// blockedRun, when set, makes Run close it and wait for its context to end, and return the status of that end.
	blockedRun chan struct{}
	// stream is what RunStream returns; streamErr is the error RunStream itself returns, with no stream.
	stream    *scriptedRunStream
	streamErr error

	calls int
	ctx   context.Context
	req   *execv2.RunRequest
}

func (r *runRPC) Run(ctx context.Context, req *execv2.RunRequest, _ ...grpc.CallOption) (*execv2.RunResult, error) {
	r.calls++
	r.ctx, r.req = ctx, req
	if r.blockedRun != nil {
		close(r.blockedRun)
		<-ctx.Done()
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	return r.result, r.err
}

func (r *runRPC) RunStream(ctx context.Context, req *execv2.RunRequest, _ ...grpc.CallOption) (execv2.Exec_RunStreamClient, error) {
	r.calls++
	r.ctx, r.req = ctx, req
	if r.streamErr != nil {
		return nil, r.streamErr
	}
	r.stream.ctx = ctx
	return r.stream, nil
}

// startKind is one way to start a run: the call, as a function of the client, the context and the request, that returns the error
// of the start (a RunStream's conn is closed once it was returned), and the plugin whose answer to that call is an error.
type startKind struct {
	name    string
	call    func(c *clientV2, ctx context.Context, req *execv2.RunRequest) error
	failing func(err error) *runRPC
}

// The ways to start a run. A refusal of RunStream reaches the client as the first thing the stream gives, which is how gRPC
// delivers the status of a server stream; the call of RunStream itself can fail too, when the stream cannot be made.
func startKinds() []startKind {
	runStream := func(c *clientV2, ctx context.Context, req *execv2.RunRequest) error {
		conn, err := c.RunStream(ctx, req)
		if conn != nil {
			_ = conn.Close()
		}
		return err
	}
	return []startKind{
		{
			name: "Run",
			call: func(c *clientV2, ctx context.Context, req *execv2.RunRequest) error {
				_, err := c.Run(ctx, req)
				return err
			},
			failing: func(err error) *runRPC { return &runRPC{err: err} },
		},
		{
			name:    "RunStream (the status is the first thing the stream gives)",
			call:    runStream,
			failing: func(err error) *runRPC { return &runRPC{stream: &scriptedRunStream{end: err}} },
		},
		{
			name:    "RunStream (the stream cannot be made)",
			call:    runStream,
			failing: func(err error) *runRPC { return &runRPC{streamErr: err} },
		},
	}
}

// newRunRPC is a plugin that takes a run and ends it at once: its result is an exit, and its stream is a RunEnded.
func newRunRPC() *runRPC {
	result := &execv2.RunResult{Outcome: execv2.Outcome_OUTCOME_EXITED, ExitCode: new(int64)}
	return &runRPC{
		result: result,
		stream: &scriptedRunStream{events: []*execv2.RunEvent{endedEvent(result)}, end: io.EOF},
	}
}

func endedEvent(result *execv2.RunResult) *execv2.RunEvent {
	return &execv2.RunEvent{Event: &execv2.RunEvent_Ended{Ended: &execv2.RunEnded{Result: result}}}
}

func outputEvent(data string) *execv2.RunEvent {
	return &execv2.RunEvent{Event: &execv2.RunEvent_Output{Output: &execv2.Output{Stream: execv2.Stream_STREAM_STDOUT, Data: []byte(data)}}}
}

func startedEvent() *execv2.RunEvent {
	return &execv2.RunEvent{Event: &execv2.RunEvent_Started{Started: &execv2.RunStarted{}}}
}

// loggedClient is a client of the plugin that records what it logs.
func loggedClient(rpc *runRPC) (*clientV2, *logtest.RecordingSink) {
	sink := &logtest.RecordingSink{}
	log := logging.New(logging.Config{Backend: logging.NewSinkBackend(sink), Level: logging.NewLevelController(logging.LevelDebug)})
	return newClientV2(rpc, log), sink
}

// loggedBreach reports whether the client logged that the plugin answered outside the contract, from the method.
func loggedBreach(sink *logtest.RecordingSink, method string) bool {
	for _, record := range sink.Records() {
		if record.Message != "exec v2 plugin answered outside the contract" {
			continue
		}
		for _, field := range record.Fields {
			if field.Key == "method" && field.Value == method {
				return true
			}
		}
	}
	return false
}

// A call whose context has no deadline is bounded by the run's own timeout and, after it, the client's call timeout: a run
// cannot outlast its timeout, so a plugin that does not answer by then is wedged, and the caller is not held for ever. A
// request that leaves timeout_ms at zero gets the target's default, at most an hour, so the bound is an hour and the call
// timeout. The caller's own deadline is never replaced, shorter or longer. The test reads the context that reaches the plugin
// and waits for nothing.
func TestRunAndRunStreamBoundACallWithNoDeadlineByTheRunsTimeoutAndKeepTheCallersOwn(t *testing.T) {
	const callTimeout = 7 * time.Minute
	for _, rc := range startKinds()[:2] {
		for _, tc := range []struct {
			name      string
			timeoutMS uint32
			want      time.Duration
		}{
			{"a timeout of 5 seconds", 5000, 5*time.Second + callTimeout},
			{"a timeout of 1 millisecond", 1, time.Millisecond + callTimeout},
			{"no timeout: the target's default, at most an hour", 0, time.Hour + callTimeout},
		} {
			t.Run(rc.name+" with "+tc.name, func(t *testing.T) {
				rpc := newRunRPC()
				c := newClientV2(rpc, nil)
				c.callTimeout = callTimeout
				before := time.Now()
				if err := rc.call(c, context.Background(), &execv2.RunRequest{TimeoutMs: tc.timeoutMS}); err != nil {
					t.Fatal(err)
				}
				after := time.Now()
				deadline, ok := rpc.ctx.Deadline()
				if !ok {
					t.Fatal("the call reached the plugin with no deadline: a wedged plugin would hold the caller for ever")
				}
				if deadline.Before(before.Add(tc.want)) || deadline.After(after.Add(tc.want)) {
					t.Errorf("the deadline is %v after the start of the call, want %v after it (the call started between %v and %v)",
						deadline.Sub(before), tc.want, before, after)
				}
				if rpc.ctx.Err() == nil {
					t.Error("the deadline the client added was not released when the call ended")
				}
			})
		}

		t.Run(rc.name+" keeps a deadline of the caller that is shorter than the bound", func(t *testing.T) {
			rpc := newRunRPC()
			want := time.Now().Add(time.Minute)
			ctx, cancel := context.WithDeadline(context.Background(), want)
			defer cancel()
			if err := rc.call(newClientV2(rpc, nil), ctx, &execv2.RunRequest{TimeoutMs: 1000}); err != nil {
				t.Fatal(err)
			}
			if got, ok := rpc.ctx.Deadline(); !ok || !got.Equal(want) {
				t.Errorf("deadline = %v, %v: the caller's own deadline %v was changed", got, ok, want)
			}
		})

		t.Run(rc.name+" keeps a deadline of the caller that is longer than the bound", func(t *testing.T) {
			rpc := newRunRPC()
			want := time.Now().Add(1000 * time.Hour)
			ctx, cancel := context.WithDeadline(context.Background(), want)
			defer cancel()
			if err := rc.call(newClientV2(rpc, nil), ctx, &execv2.RunRequest{TimeoutMs: 1000}); err != nil {
				t.Fatal(err)
			}
			if got, ok := rpc.ctx.Deadline(); !ok || !got.Equal(want) {
				t.Errorf("deadline = %v, %v: the caller's own deadline %v was replaced by the shorter bound", got, ok, want)
			}
		})
	}
}

func TestTheDefaultCallTimeoutIsWhatARunIsAllowedBeyondItsOwn(t *testing.T) {
	rpc := newRunRPC()
	before := time.Now()
	if _, err := newClientV2(rpc, nil).Run(context.Background(), &execv2.RunRequest{TimeoutMs: 1000}); err != nil {
		t.Fatal(err)
	}
	deadline, _ := rpc.ctx.Deadline()
	if got, want := deadline.Sub(before), time.Second+30*time.Second; got < want || got > want+time.Minute {
		t.Errorf("a run of 1 second with no deadline of the caller was bounded by %v, want 31 seconds: its timeout and the 30 seconds of the call timeout", got)
	}
}

// A stream outlives the call that made it, and its deadline ends with the stream: when the host has read it to its end, or
// closes it, the deadline the client added is released, so that no timer outlives the stream.
func TestARunStreamReleasesTheDeadlineTheClientAddedWhenTheStreamIsOver(t *testing.T) {
	for name, over := range map[string]func(RunConn){
		"read to its end": func(conn RunConn) {
			for {
				if _, err := conn.Recv(); err != nil {
					return
				}
			}
		},
		"closed": func(conn RunConn) { _ = conn.Close() },
	} {
		t.Run(name, func(t *testing.T) {
			rpc := newRunRPC()
			conn, err := newClientV2(rpc, nil).RunStream(context.Background(), &execv2.RunRequest{TimeoutMs: 1000})
			if err != nil {
				t.Fatal(err)
			}
			if err := rpc.ctx.Err(); err != nil {
				t.Fatalf("the stream's context was over while the stream was open: %v", err)
			}
			over(conn)
			if rpc.ctx.Err() == nil {
				t.Error("the stream's context was not released when the stream was over")
			}
		})
	}
}

// The call is made with the caller's catalog pin as metadata, as Options and Open make theirs: a plugin admits a run like any
// resource utility call. A pin that is malformed is refused before the plugin hears of the call.
func TestRunAndRunStreamSendTheCallersCatalogPinAndRefuseAMalformedOne(t *testing.T) {
	for _, rc := range startKinds()[:2] {
		t.Run(rc.name+" sends the pin", func(t *testing.T) {
			rpc := newRunRPC()
			ctx := resourcev2.WithCatalogRevision(context.Background(), "catalog-7")
			if err := rc.call(newClientV2(rpc, nil), ctx, &execv2.RunRequest{}); err != nil {
				t.Fatal(err)
			}
			md, _ := metadata.FromOutgoingContext(rpc.ctx)
			if got := md.Get("omniview-resource-catalog-revision-bin"); len(got) != 1 || got[0] != "catalog-7" {
				t.Errorf("the plugin received the catalog pin %q, want %q: a run is admitted like every resource utility call", got, "catalog-7")
			}
		})
		t.Run(rc.name+" refuses a malformed pin before the plugin is called", func(t *testing.T) {
			rpc := newRunRPC()
			ctx := resourcev2.WithCatalogRevision(context.Background(), strings.Repeat("x", 1<<16))
			err := rc.call(newClientV2(rpc, nil), ctx, &execv2.RunRequest{})
			var resourceErr *resourcev2.ResourceOperationError
			if !errors.As(err, &resourceErr) {
				t.Errorf("a malformed pin gave %#v, want the resource lane's refusal", err)
			}
			if rpc.calls != 0 {
				t.Errorf("the plugin was called %d times with a pin that cannot be admitted", rpc.calls)
			}
		})
	}
}

// A request that is missing is a refusal of the client's own: nothing is sent.
func TestRunAndRunStreamRefuseAMissingRequestWithoutCallingThePlugin(t *testing.T) {
	for _, rc := range startKinds()[:2] {
		t.Run(rc.name, func(t *testing.T) {
			rpc := newRunRPC()
			err := rc.call(newClientV2(rpc, nil), context.Background(), nil)
			var typed *Error
			if !errors.As(err, &typed) || typed.Class != ErrorClassInvalidRequest {
				t.Errorf("got %#v, want INVALID_REQUEST", err)
			}
			if rpc.calls != 0 {
				t.Errorf("the plugin was called %d times for no request", rpc.calls)
			}
		})
	}
}

// The request is sent as it is: the plugin refuses what it does not accept, and the client neither fills in nor clamps.
func TestRunAndRunStreamSendTheRequestAsItIs(t *testing.T) {
	want := &execv2.RunRequest{
		RequestId: "audit-1", Argv: []string{"sh", "-c", "echo"}, Stdin: []byte("in"), TimeoutMs: 1234,
		Output: &execv2.OutputLimits{MaxStdoutBytes: 7, MaxStderrBytes: 8, Policy: execv2.TruncatePolicy_TRUNCATE_POLICY_TAIL},
		Env:    []*execv2.NameValue{{Name: "A", Value: "b"}}, Labels: []*execv2.NameValue{{Name: "k", Value: "v"}}, Cwd: "/w",
	}
	for _, rc := range startKinds()[:2] {
		t.Run(rc.name, func(t *testing.T) {
			rpc := newRunRPC()
			if err := rc.call(newClientV2(rpc, nil), context.Background(), want); err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(rpc.req, want) {
				t.Errorf("the plugin received %v, want the request as it was sent: %v", rpc.req, want)
			}
		})
	}
}

// A run that was handed to the target answers with a result whatever happened to it: each outcome is the plugin's message as it
// came, and none of them is an error, LOST and FAILED included. An absent exit code stays absent.
func TestRunReturnsAResultOfEveryOutcomeAsItCameAndNoneIsAnError(t *testing.T) {
	failed := &execv2.Failed{Class: execv2.ErrorClass_ERROR_CLASS_UNAVAILABLE, Reason: execv2.ErrorReason_ERROR_REASON_NOT_RUNNING, Stage: execv2.FailureStage_FAILURE_STAGE_RUN}
	for name, sent := range map[string]*execv2.RunResult{
		"exited":     {RequestId: "r", Outcome: execv2.Outcome_OUTCOME_EXITED, ExitCode: new(int64(3)), ExitSource: execv2.ExitSource_EXIT_SOURCE_PROCESS, Stdout: []byte("out"), StartObserved: true},
		"signaled":   {Outcome: execv2.Outcome_OUTCOME_SIGNALED, Signal: "SIGKILL", SignalNumber: 9},
		"timed out":  {Outcome: execv2.Outcome_OUTCOME_TIMED_OUT, ExitSource: execv2.ExitSource_EXIT_SOURCE_HOST, RemoteMaySurvive: new(true)},
		"cancelled":  {Outcome: execv2.Outcome_OUTCOME_CANCELLED, ExitSource: execv2.ExitSource_EXIT_SOURCE_HOST},
		"lost":       {Outcome: execv2.Outcome_OUTCOME_LOST, RemoteMaySurvive: new(true), Stderr: []byte("partial")},
		"failed":     {Outcome: execv2.Outcome_OUTCOME_FAILED, Failed: failed},
		"no outcome": {},
	} {
		t.Run(name, func(t *testing.T) {
			rpc := &runRPC{result: proto.Clone(sent).(*execv2.RunResult)}
			got, err := newClientV2(rpc, nil).Run(context.Background(), &execv2.RunRequest{})
			if err != nil {
				t.Fatalf("a run that ended %s is a result and not an error, got %v", name, err)
			}
			if !proto.Equal(got, sent) {
				t.Errorf("the result is %v, want the plugin's message as it came: %v", got, sent)
			}
			if gotHas, sentHas := got.ExitCode != nil, sent.ExitCode != nil; gotHas != sentHas {
				t.Errorf("exit_code present is %v, the plugin's was %v: an absent exit code is none, never 0", gotHas, sentHas)
			}
		})
	}
}

// A plugin that answers a successful call with no message broke the contract: there is no result to hand the host, and the
// breach is logged locally.
func TestRunWithNoResultFromThePluginIsAnErrorOfThePluginAndNeverAnOutcome(t *testing.T) {
	c, logs := loggedClient(&runRPC{})
	got, err := c.Run(context.Background(), &execv2.RunRequest{})
	var typed *Error
	if !errors.As(err, &typed) || typed.Class != ErrorClassInternal || got != nil {
		t.Errorf("got %v and %#v, want no result and an INTERNAL error", got, err)
	}
	if !loggedBreach(logs, "Run") {
		t.Error("an answer with no result was not logged as the plugin's breach of the contract")
	}
}

// What a failed call is, by its status: a refusal of the exec lane keeps its class, reason and facts; the resource lane's own
// refusals keep their code; and a status of the transport's own is a class of its own, never a result and never an outcome.
func TestRunAndRunStreamTypeAFailedCallByItsStatus(t *testing.T) {
	refusal := &Error{
		Class: ErrorClassNotFound, Reason: ErrorReasonNoSuchExecutable, Message: "no kubectl",
		Facts: ErrorFacts{Program: "kubectl", SelectionName: "container", SelectionValue: "app", RetryAfter: 1500 * time.Millisecond},
	}
	for _, rc := range startKinds() {
		t.Run(rc.name+" a refusal of the exec lane", func(t *testing.T) {
			rpc := rc.failing(refusal.status())
			err := rc.call(newClientV2(rpc, nil), context.Background(), &execv2.RunRequest{})
			var typed *Error
			if !errors.As(err, &typed) || typed.Class != refusal.Class || typed.Reason != refusal.Reason || typed.Message != refusal.Message || typed.Facts != refusal.Facts {
				t.Errorf("got %#v, want the refusal as it was sent: %#v", err, refusal)
			}
			if rpc.ctx.Err() == nil {
				t.Error("the context of a call that was refused was not released")
			}
		})
		t.Run(rc.name+" a refusal of the resource lane", func(t *testing.T) {
			sent := resourceplugin.ResourceRPCError(resourcev2.NewError("RESET_REQUIRED", "pin"))
			err := rc.call(newClientV2(rc.failing(sent), nil), context.Background(), &execv2.RunRequest{})
			var resourceErr *resourcev2.ResourceOperationError
			if !errors.As(err, &resourceErr) || resourceErr.Code != "RESET_REQUIRED" {
				t.Errorf("got %#v, want the resource lane's RESET_REQUIRED", err)
			}
		})
		for _, tc := range []struct {
			code  codes.Code
			class ErrorClass
		}{
			{codes.Unimplemented, ErrorClassUnsupported},
			{codes.Unavailable, ErrorClassConnection},
			{codes.DeadlineExceeded, ErrorClassTimeout},
			{codes.Canceled, ErrorClassCancelled},
			{codes.Internal, ErrorClassInternal},
			{codes.PermissionDenied, ErrorClassForbidden},
		} {
			t.Run(rc.name+" a status of "+tc.code.String(), func(t *testing.T) {
				sent := status.Error(tc.code, "the plugin's own text, never shown")
				err := rc.call(newClientV2(rc.failing(sent), nil), context.Background(), &execv2.RunRequest{})
				var typed *Error
				if !errors.As(err, &typed) || typed.Class != tc.class {
					t.Fatalf("got %#v, want class %s", err, tc.class)
				}
				if strings.Contains(err.Error(), "the plugin's own text") {
					t.Errorf("the error carries the status text of the plugin: %v", err)
				}
			})
		}
	}
}

// A call whose own context ended is the context's error, typed: cancelled, or timed out when its deadline passed, and the
// error wraps the context's, so that errors.Is finds it. A status that is an exec refusal stays that refusal, though the context
// has ended by the time the client decodes it (the plugin's answer came first).
func TestRunAndRunStreamTypeTheEndOfTheCallersContextAndKeepARefusalThatCameFirst(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer stop()
	for _, rc := range startKinds() {
		for _, tc := range []struct {
			name  string
			ctx   context.Context
			class ErrorClass
			is    error
		}{
			{"cancelled", canceled, ErrorClassCancelled, context.Canceled},
			{"past its deadline", expired, ErrorClassTimeout, context.DeadlineExceeded},
		} {
			t.Run(rc.name+" "+tc.name, func(t *testing.T) {
				rpc := rc.failing(status.FromContextError(tc.ctx.Err()).Err())
				err := rc.call(newClientV2(rpc, nil), tc.ctx, &execv2.RunRequest{})
				var typed *Error
				if !errors.As(err, &typed) || typed.Class != tc.class || !errors.Is(err, tc.is) {
					t.Errorf("got %#v, want class %s wrapping %v", err, tc.class, tc.is)
				}
			})
		}
		t.Run(rc.name+" a refusal that came before the deadline passed", func(t *testing.T) {
			rpc := rc.failing((&Error{Class: ErrorClassLimit, Reason: ErrorReasonTooManyRuns}).status())
			err := rc.call(newClientV2(rpc, nil), expired, &execv2.RunRequest{})
			var typed *Error
			if !errors.As(err, &typed) || typed.Class != ErrorClassLimit || typed.Reason != ErrorReasonTooManyRuns || errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("got %#v, want the plugin's LIMIT/TOO_MANY_RUNS", err)
			}
		})
	}
}

// Cancelling the caller's context cancels the call, which is cancelling the run: the plugin sees its context end, and the call
// returns the cancel, typed, with the context's error inside.
func TestCancellingTheCallersContextCancelsARunCall(t *testing.T) {
	rpc := &runRPC{blockedRun: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan error, 1)
	go func() {
		_, err := newClientV2(rpc, nil).Run(ctx, &execv2.RunRequest{})
		returned <- err
	}()
	select {
	case <-rpc.blockedRun:
	case <-time.After(runWait):
		t.Fatal("the call did not reach the plugin")
	}
	cancel()
	select {
	case err := <-returned:
		var typed *Error
		if !errors.As(err, &typed) || typed.Class != ErrorClassCancelled || !errors.Is(err, context.Canceled) {
			t.Errorf("a call that was cancelled returned %#v, want CANCELLED wrapping context.Canceled", err)
		}
	case <-time.After(runWait):
		t.Fatal("cancelling the caller's context did not end the call")
	}
}

// The first call of Recv returns the event RunStream read to learn that the run was taken, and the rest follow in order: the
// stream is exactly what the plugin sent. After RunEnded the stream ends with status OK, and Recv returns io.EOF, again
// and again.
func TestARunStreamReturnsItsEventsInOrderFromTheFirstAndEndsWithEOFAfterRunEnded(t *testing.T) {
	result := &execv2.RunResult{Outcome: execv2.Outcome_OUTCOME_EXITED, ExitCode: new(int64(7)), ExitSource: execv2.ExitSource_EXIT_SOURCE_PROCESS}
	sent := []*execv2.RunEvent{
		startedEvent(), outputEvent("a"), {Event: &execv2.RunEvent_Progress{Progress: &execv2.RunProgress{ElapsedMs: 5, BytesOut: 1}}},
		outputEvent("b"), endedEvent(result),
	}
	stream := &scriptedRunStream{events: append([]*execv2.RunEvent(nil), sent...), end: io.EOF}
	conn, err := newClientV2(&runRPC{stream: stream}, nil).RunStream(context.Background(), &execv2.RunRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if stream.recvs != 1 {
		t.Fatalf("RunStream read %d events to learn that the run was taken, want 1", stream.recvs)
	}
	for i, want := range sent {
		got, err := conn.Recv()
		if err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		if got != want {
			t.Errorf("event %d is %v, want %v: the events are the plugin's messages, in the order it sent them", i, got, want)
		}
	}
	var recvsAtEnd int
	for i := range 2 {
		if got, err := conn.Recv(); !errors.Is(err, io.EOF) || got != nil {
			t.Errorf("after RunEnded: %v and %v, want io.EOF", got, err)
		}
		if i == 0 {
			recvsAtEnd = stream.recvs
		}
	}
	if stream.recvs != recvsAtEnd {
		t.Errorf("Recv asked the stream %d more times after it was over: the end is final", stream.recvs-recvsAtEnd)
	}
}

// An event kind this reader does not know arrives as a RunEvent whose Event is nil: the consumer ignores it, and the stream
// goes on.
func TestARunStreamPassesAnEventItDoesNotKnowAsOneWithNoEvent(t *testing.T) {
	unknown := &execv2.RunEvent{}
	stream := &scriptedRunStream{events: []*execv2.RunEvent{unknown, outputEvent("x"), endedEvent(&execv2.RunResult{})}, end: io.EOF}
	conn, err := newClientV2(&runRPC{stream: stream}, nil).RunStream(context.Background(), &execv2.RunRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	first, err := conn.Recv()
	if err != nil || first != unknown || first.Event != nil {
		t.Fatalf("the first event is %v, %v, want the event of no kind as it came", first, err)
	}
	if next, err := conn.Recv(); err != nil || next.GetOutput() == nil {
		t.Errorf("after an event of a kind the reader does not know: %v, %v, want the stream to go on", next, err)
	}
}

// A stream that ends with status OK before RunEnded breaks the contract: the host must not read it as a run that ended well.
// Recv never returns io.EOF for a run it did not see end, whether the stream had sent nothing at all or some events, and the
// breach is logged locally. Once the stream is over, Recv gives the same answer without asking the stream again.
func TestARunStreamNeverEndsWithEOFForARunItDidNotSeeEnd(t *testing.T) {
	t.Run("nothing at all: RunStream itself fails", func(t *testing.T) {
		c, logs := loggedClient(&runRPC{stream: &scriptedRunStream{end: io.EOF}})
		conn, err := c.RunStream(context.Background(), &execv2.RunRequest{})
		var typed *Error
		if !errors.As(err, &typed) || typed.Class != ErrorClassInternal || conn != nil {
			t.Errorf("got %v and %#v, want no conn and an INTERNAL error", conn, err)
		}
		if !loggedBreach(logs, "RunStream") {
			t.Error("a stream that ended before its first event was not logged as the plugin's breach of the contract")
		}
	})
	t.Run("some events and then status OK", func(t *testing.T) {
		stream := &scriptedRunStream{events: []*execv2.RunEvent{startedEvent(), outputEvent("x")}, end: io.EOF}
		c, logs := loggedClient(&runRPC{stream: stream})
		conn, err := c.RunStream(context.Background(), &execv2.RunRequest{})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		for range 2 {
			if _, err := conn.Recv(); err != nil {
				t.Fatalf("an event the plugin sent: %v", err)
			}
		}
		if loggedBreach(logs, "RunStream") {
			t.Fatal("the breach was logged before the stream ended")
		}
		var recvsAtEnd int
		for i := range 2 {
			_, err := conn.Recv()
			var typed *Error
			if errors.Is(err, io.EOF) || !errors.As(err, &typed) || typed.Class != ErrorClassInternal {
				t.Fatalf("a stream that ended with status OK and no RunEnded: %#v, want an INTERNAL error and never io.EOF", err)
			}
			if i == 0 {
				recvsAtEnd = stream.recvs
			}
		}
		if stream.recvs != recvsAtEnd {
			t.Errorf("Recv asked the stream %d more times after it was over: the end is final", stream.recvs-recvsAtEnd)
		}
		if !loggedBreach(logs, "RunStream") {
			t.Error("a stream that ended with status OK and no RunEnded was not logged as the plugin's breach of the contract")
		}
	})
}

// A stream that breaks before RunEnded is an error of the transport's own class, with the events that came before it
// delivered first: what became of the command is not known from it.
func TestARunStreamThatBreaksBeforeRunEndedIsATypedErrorAfterItsEvents(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		class ErrorClass
	}{
		{"the connection broke", status.Error(codes.Unavailable, "gone"), ErrorClassConnection},
		{"the plugin's own status", (&Error{Class: ErrorClassUnavailable, Reason: ErrorReasonNotRunning}).status(), ErrorClassUnavailable},
		{"cancelled", status.Error(codes.Canceled, "gone"), ErrorClassCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := &scriptedRunStream{events: []*execv2.RunEvent{startedEvent(), outputEvent("partial")}, end: tc.err}
			conn, err := newClientV2(&runRPC{stream: stream}, nil).RunStream(context.Background(), &execv2.RunRequest{})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			for i := range 2 {
				if _, err := conn.Recv(); err != nil {
					t.Fatalf("event %d: %v", i, err)
				}
			}
			for range 2 {
				_, err := conn.Recv()
				var typed *Error
				if !errors.As(err, &typed) || typed.Class != tc.class || errors.Is(err, io.EOF) {
					t.Fatalf("got %#v, want class %s and never io.EOF", err, tc.class)
				}
			}
		})
	}
}

// blockedStream makes a RunStream conn whose plugin sent its first event and then waits for the stream to end, and returns once a
// Recv is waiting on it.
func blockedStream(t *testing.T, ctx context.Context) (RunConn, *runRPC, <-chan error) {
	t.Helper()
	stream := &scriptedRunStream{events: []*execv2.RunEvent{startedEvent()}, blocked: make(chan struct{})}
	rpc := &runRPC{stream: stream}
	conn, err := newClientV2(rpc, nil).RunStream(ctx, &execv2.RunRequest{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.Recv(); err != nil {
		t.Fatal(err)
	}
	received := make(chan error, 1)
	go func() {
		_, err := conn.Recv()
		received <- err
	}()
	select {
	case <-stream.blocked:
	case <-time.After(runWait):
		t.Fatal("Recv did not wait for the plugin")
	}
	return conn, rpc, received
}

// wantCancelled waits for the Recv of a blocked stream to return, and holds it to the cancel of the stream, typed.
func wantCancelled(t *testing.T, received <-chan error, what string) {
	t.Helper()
	select {
	case err := <-received:
		var typed *Error
		if !errors.As(err, &typed) || typed.Class != ErrorClassCancelled || !errors.Is(err, context.Canceled) {
			t.Errorf("a Recv that %s woke returned %#v, want CANCELLED wrapping context.Canceled", what, err)
		}
	case <-time.After(runWait):
		t.Fatalf("%s did not unblock a Recv that was waiting", what)
	}
}

// Closing the conn cancels the run (the stream's context ends, which the plugin sees as the call being cancelled), unblocks a
// Recv that waits, which then returns the cancel as a typed error, and may be done again.
func TestClosingARunStreamCancelsTheRunAndUnblocksARecvThatIsWaiting(t *testing.T) {
	conn, rpc, received := blockedStream(t, context.Background())
	if rpc.ctx.Err() != nil {
		t.Fatal("the stream was cancelled before Close")
	}
	if err := conn.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	wantCancelled(t, received, "Close")
	if rpc.ctx.Err() == nil {
		t.Error("Close did not cancel the stream's context: the run goes on")
	}
	if err := conn.Close(); err != nil {
		t.Errorf("Close again: %v", err)
	}
}

// Cancelling the caller's context ends the stream, as Close does: the run is cancelled with the call.
func TestCancellingTheCallersContextEndsARunStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, rpc, received := blockedStream(t, ctx)
	cancel()
	wantCancelled(t, received, "cancelling the caller's context")
	if rpc.ctx.Err() == nil {
		t.Error("the stream's context was not cancelled with the caller's")
	}
}
