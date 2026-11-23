package exec_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The client of Run and RunStream over a real gRPC transport (a bufconn, so that a test can stop the server in the middle of a
// call) against a plugin written for the test: it answers what a test scripts, and says what it saw. What the client does with
// the generated client of a plugin that does not need a transport is run_client_test.go.

// wireWait bounds a wait for something that has to happen. It turns a hang into a failure and decides no outcome.
const wireWait = 30 * time.Second

// scriptedRunServer is a plugin that serves Run and RunStream from the functions a test gives, and Unimplemented for the rest.
type scriptedRunServer struct {
	execv2.UnimplementedExecServer

	run       func(ctx context.Context, req *execv2.RunRequest) (*execv2.RunResult, error)
	runStream func(req *execv2.RunRequest, stream execv2.Exec_RunStreamServer) error
}

func (s *scriptedRunServer) Run(ctx context.Context, req *execv2.RunRequest) (*execv2.RunResult, error) {
	if s.run == nil {
		return s.UnimplementedExecServer.Run(ctx, req)
	}
	return s.run(ctx, req)
}

func (s *scriptedRunServer) RunStream(req *execv2.RunRequest, stream execv2.Exec_RunStreamServer) error {
	if s.runStream == nil {
		return s.UnimplementedExecServer.RunStream(req, stream)
	}
	return s.runStream(req, stream)
}

// serveScripted serves the plugin over a bufconn and returns the host's client of it and the server, so that a test can stop it.
func serveScripted(t *testing.T, service execv2.ExecServer) (exec.Client, *grpc.Server) {
	t.Helper()
	server := grpc.NewServer()
	execv2.RegisterExecServer(server, service)
	listener := bufconn.Listen(1 << 20)
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		<-served
	})
	conn, err := grpc.NewClient("passthrough:///fake",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	plugin, err := (&exec.Plugin{}).GRPCClient(context.Background(), nil, conn)
	if err != nil {
		t.Fatal(err)
	}
	return plugin.(*exec.PluginClient).V2(), server
}

// refusalStatus is the status of a refusal of the exec lane, as the SDK sends one: the class as the reason of an ErrorInfo of the
// lane's domain, and the reason and the facts as its metadata.
func refusalStatus(t *testing.T, code codes.Code, class string, metadata map[string]string) error {
	t.Helper()
	detailed, err := status.New(code, "exec operation failed").WithDetails(&errdetails.ErrorInfo{Reason: class, Domain: "omniview.sdk.exec.v2", Metadata: metadata})
	if err != nil {
		t.Fatal(err)
	}
	return detailed.Err()
}

// A result crosses the transport as the plugin made it: every outcome is a result and no error, the bytes are untouched (a NUL,
// invalid UTF-8, an escape sequence), and what the plugin did not say is absent on the host's side: an exit code that is absent is
// none and never 0, and a remote_may_survive that is absent is not false.
func TestRunOverATransportReturnsTheResultAsThePluginMadeIt(t *testing.T) {
	raw := []byte("a\x00b\xff\xfe\x1b[31m\xe2\x82")
	for name, sent := range map[string]*execv2.RunResult{
		"an exit": {
			RequestId: "audit-1", Outcome: execv2.Outcome_OUTCOME_EXITED, ExitCode: new(int64(-1073741819)), ExitSource: execv2.ExitSource_EXIT_SOURCE_PROCESS,
			Stdout: raw, Stderr: []byte("warn"), DurationMs: 12, StartObserved: true, RemoteMaySurvive: new(false),
			StdoutAccount: &execv2.OutputAccount{TotalBytes: 20, KeptBytes: 10, DroppedBytes: 10, Truncated: true, HeadBytes: 5, TailBytes: 5, Completeness: execv2.OutputCompleteness_OUTPUT_COMPLETENESS_TRUNCATED},
			Target:        &execv2.TargetFacts{Provider: "p", Type: "t", Id: "i"}, Program: &execv2.Program{Argv: []string{"ls"}, Name: "ls"},
		},
		"a signal":  {Outcome: execv2.Outcome_OUTCOME_SIGNALED, Signal: "SIGKILL", SignalNumber: 9, ExitSource: execv2.ExitSource_EXIT_SOURCE_PROCESS, RemoteMaySurvive: new(false)},
		"a timeout": {Outcome: execv2.Outcome_OUTCOME_TIMED_OUT, ExitSource: execv2.ExitSource_EXIT_SOURCE_HOST, Stdout: []byte("partial")},
		"lost": {
			Outcome: execv2.Outcome_OUTCOME_LOST, RemoteMaySurvive: new(true), Stdout: []byte("partial"),
			StdoutAccount: &execv2.OutputAccount{TotalBytes: 7, KeptBytes: 7, HeadBytes: 7, ValidUtf8: true},
		},
		"a failure": {
			Outcome: execv2.Outcome_OUTCOME_FAILED,
			Failed: &execv2.Failed{
				Class: execv2.ErrorClass_ERROR_CLASS_NOT_FOUND, Reason: execv2.ErrorReason_ERROR_REASON_NO_SUCH_EXECUTABLE,
				Facts: &execv2.ErrorFacts{Program: "kubectl"}, Stage: execv2.FailureStage_FAILURE_STAGE_START,
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			client, _ := serveScripted(t, &scriptedRunServer{run: func(context.Context, *execv2.RunRequest) (*execv2.RunResult, error) { return sent, nil }})
			got, err := client.Run(context.Background(), &execv2.RunRequest{Argv: []string{"x"}})
			if err != nil {
				t.Fatalf("a run that ended %s is a result and not an error, got %v", name, err)
			}
			if !proto.Equal(got, sent) {
				t.Errorf("the result is %v, want %v", got, sent)
			}
			if (got.ExitCode == nil) != (sent.ExitCode == nil) || (got.RemoteMaySurvive == nil) != (sent.RemoteMaySurvive == nil) {
				t.Errorf("exit_code present %v and remote_may_survive present %v, the plugin sent %v and %v: absence is a value of its own",
					got.ExitCode != nil, got.RemoteMaySurvive != nil, sent.ExitCode != nil, sent.RemoteMaySurvive != nil)
			}
		})
	}
}

// The largest answer a target may give crosses the transport: the output caps of a target are at most 1 MiB for a stream, so a
// result of two streams of the largest cap and a request with the largest stdin are within the message size the transport allows.
func TestTheLargestResultAndTheLargestRequestCrossTheTransport(t *testing.T) {
	big := bytes.Repeat([]byte{0xfe}, 1<<20)
	client, _ := serveScripted(t, &scriptedRunServer{run: func(_ context.Context, req *execv2.RunRequest) (*execv2.RunResult, error) {
		return &execv2.RunResult{Outcome: execv2.Outcome_OUTCOME_EXITED, ExitCode: new(int64), Stdout: req.GetStdin(), Stderr: big}, nil
	}})
	got, err := client.Run(context.Background(), &execv2.RunRequest{Argv: []string{"x"}, Stdin: big})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.GetStdout(), big) || !bytes.Equal(got.GetStderr(), big) {
		t.Errorf("a result of two streams of 1 MiB came back as %d and %d bytes", len(got.GetStdout()), len(got.GetStderr()))
	}
}

// await takes what the plugin said it received, within the bound of a wait: a call that never reached it fails the test and does
// not hang it.
func await[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(wireWait):
		var zero T
		t.Fatal("the call did not reach the plugin")
		return zero
	}
}

// What the plugin receives is what the host sent, with the catalog pin of the call as metadata and the deadline the client gave
// a call that has none: the run's own timeout and the call timeout after it. A caller that has a deadline sends its own.
func TestRunAndRunStreamReachThePluginWithTheRequestThePinAndTheDeadline(t *testing.T) {
	type seen struct {
		req      *execv2.RunRequest
		pin      []string
		deadline time.Time
		has      bool
	}
	sent := &execv2.RunRequest{RequestId: "r-1", Argv: []string{"sh", "-c", "true"}, Stdin: []byte("in"), TimeoutMs: 5000, Output: &execv2.OutputLimits{MaxStdoutBytes: 10}}
	arrived := make(chan seen, 1)
	note := func(ctx context.Context, req *execv2.RunRequest) {
		md, _ := metadata.FromIncomingContext(ctx)
		deadline, has := ctx.Deadline()
		arrived <- seen{req: proto.Clone(req).(*execv2.RunRequest), pin: md.Get("omniview-resource-catalog-revision-bin"), deadline: deadline, has: has}
	}
	client, _ := serveScripted(t, &scriptedRunServer{
		run: func(ctx context.Context, req *execv2.RunRequest) (*execv2.RunResult, error) {
			note(ctx, req)
			return &execv2.RunResult{Outcome: execv2.Outcome_OUTCOME_EXITED, ExitCode: new(int64)}, nil
		},
		runStream: func(req *execv2.RunRequest, stream execv2.Exec_RunStreamServer) error {
			note(stream.Context(), req)
			return stream.Send(&execv2.RunEvent{Event: &execv2.RunEvent_Ended{Ended: &execv2.RunEnded{Result: &execv2.RunResult{Outcome: execv2.Outcome_OUTCOME_EXITED, ExitCode: new(int64)}}}})
		},
	})
	for name, start := range map[string]func(ctx context.Context) error{
		"Run": func(ctx context.Context) error {
			_, err := client.Run(ctx, sent)
			return err
		},
		"RunStream": func(ctx context.Context) error {
			conn, err := client.RunStream(ctx, sent)
			if conn != nil {
				_ = conn.Close()
			}
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			before := time.Now()
			if err := start(resourcev2.WithCatalogRevision(context.Background(), "catalog-9")); err != nil {
				t.Fatal(err)
			}
			got := await(t, arrived)
			if !proto.Equal(got.req, sent) {
				t.Errorf("the plugin received %v, want the request as it was sent: %v", got.req, sent)
			}
			if len(got.pin) != 1 || got.pin[0] != "catalog-9" {
				t.Errorf("the plugin received the catalog pin %q, want catalog-9", got.pin)
			}
			// The deadline travels as a timeout of the call, so it is read against the clock of the test with the slack that a
			// slow machine needs: the bound is the run's 5 seconds and the call timeout's 30 seconds, never less than the first.
			if remaining := got.deadline.Sub(before); !got.has || remaining < 30*time.Second || remaining > 5*time.Second+30*time.Second+time.Minute {
				t.Errorf("the plugin received a deadline in %v (present: %v), want about 35 seconds: the timeout of the run and the call timeout", remaining, got.has)
			}

			own := 3 * time.Hour
			ctx, cancel := context.WithTimeout(context.Background(), own)
			defer cancel()
			if err := start(ctx); err != nil {
				t.Fatal(err)
			}
			if got := await(t, arrived); !got.has || got.deadline.Sub(before) < own-time.Minute {
				t.Errorf("the plugin received the deadline %v from now, want the caller's own, about %v", got.deadline.Sub(before), own)
			}
		})
	}
}

// A refusal is the typed error it was sent as, with its class, reason and facts, and never a result: nothing ran. A plugin that
// does not serve the lane answers Unimplemented, which is UNSUPPORTED.
func TestARefusalOverATransportIsTheTypedErrorAndNeverAResult(t *testing.T) {
	refused := refusalStatus(t, codes.ResourceExhausted, "LIMIT", map[string]string{"reason": "TOO_MANY_RUNS", "retry_after_ms": "2000"})
	for name, tc := range map[string]struct {
		server *scriptedRunServer
		want   func(*exec.Error) bool
	}{
		"a limit": {
			&scriptedRunServer{
				run:       func(context.Context, *execv2.RunRequest) (*execv2.RunResult, error) { return nil, refused },
				runStream: func(*execv2.RunRequest, execv2.Exec_RunStreamServer) error { return refused },
			},
			func(e *exec.Error) bool {
				return e.Class == exec.ErrorClassLimit && e.Reason == exec.ErrorReasonTooManyRuns && e.Facts.RetryAfter == 2*time.Second
			},
		},
		"a plugin that does not serve the lane": {
			&scriptedRunServer{},
			func(e *exec.Error) bool { return e.Class == exec.ErrorClassUnsupported },
		},
	} {
		t.Run(name, func(t *testing.T) {
			client, _ := serveScripted(t, tc.server)
			result, err := client.Run(context.Background(), &execv2.RunRequest{Argv: []string{"x"}})
			if result != nil || !isTyped(err, tc.want) {
				t.Errorf("Run: %v and %#v, want no result and the typed refusal", result, err)
			}
			conn, err := client.RunStream(context.Background(), &execv2.RunRequest{Argv: []string{"x"}})
			if conn != nil || !isTyped(err, tc.want) {
				t.Errorf("RunStream: %v and %#v, want no conn and the typed refusal", conn, err)
			}
		})
	}
}

func isTyped(err error, want func(*exec.Error) bool) bool {
	var typed *exec.Error
	return errors.As(err, &typed) && want(typed)
}

// A host that cancels its call cancels the run: the plugin sees the call's context end. The call returns CANCELLED with the
// context's error inside, and the plugin's own answer, if it was already on its way, is not a result the host reads.
func TestCancellingTheCallersContextReachesThePluginAndEndsTheCall(t *testing.T) {
	for _, method := range []string{"Run", "RunStream"} {
		t.Run(method, func(t *testing.T) {
			running, cancelled := make(chan struct{}), make(chan struct{})
			hold := func(ctx context.Context) {
				close(running)
				<-ctx.Done()
				close(cancelled)
			}
			client, _ := serveScripted(t, &scriptedRunServer{
				run: func(ctx context.Context, _ *execv2.RunRequest) (*execv2.RunResult, error) {
					hold(ctx)
					return nil, ctx.Err()
				},
				runStream: func(_ *execv2.RunRequest, stream execv2.Exec_RunStreamServer) error {
					if err := stream.Send(&execv2.RunEvent{Event: &execv2.RunEvent_Started{Started: &execv2.RunStarted{}}}); err != nil {
						return err
					}
					hold(stream.Context())
					return stream.Context().Err()
				},
			})
			start := func(ctx context.Context) error {
				if method == "Run" {
					_, err := client.Run(ctx, &execv2.RunRequest{Argv: []string{"x"}})
					return err
				}
				conn, err := client.RunStream(ctx, &execv2.RunRequest{Argv: []string{"x"}})
				if err != nil {
					return err
				}
				defer conn.Close()
				for {
					if _, err := conn.Recv(); err != nil {
						return err
					}
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			returned := make(chan error, 1)
			go func() { returned <- start(ctx) }()
			select {
			case <-running:
			case err := <-returned:
				t.Fatalf("the call ended before it reached the plugin: %v", err)
			case <-time.After(wireWait):
				t.Fatal("the run did not reach the plugin")
			}
			cancel()
			select {
			case err := <-returned:
				var typed *exec.Error
				if !errors.As(err, &typed) || typed.Class != exec.ErrorClassCancelled || !errors.Is(err, context.Canceled) {
					t.Errorf("the call returned %#v, want CANCELLED wrapping context.Canceled", err)
				}
			case <-time.After(wireWait):
				t.Fatal("cancelling the caller's context did not end the call")
			}
			select {
			case <-cancelled:
			case <-time.After(wireWait):
				t.Error("the plugin never saw the call cancelled: the run goes on")
			}
		})
	}
}

// A call whose deadline passes before the plugin answers ends with TIMEOUT, with the context's own error inside; it says nothing
// about whether the command ran, and it is no outcome.
func TestADeadlineThatPassesBeforeTheResultIsATimeoutAndNeverAnOutcome(t *testing.T) {
	client, _ := serveScripted(t, &scriptedRunServer{run: func(ctx context.Context, _ *execv2.RunRequest) (*execv2.RunResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	result, err := client.Run(ctx, &execv2.RunRequest{Argv: []string{"x"}})
	var typed *exec.Error
	if result != nil || !errors.As(err, &typed) || typed.Class != exec.ErrorClassTimeout || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("got %v and %#v, want no result and TIMEOUT wrapping context.DeadlineExceeded", result, err)
	}
}

// A transport that breaks before the answer arrives is a typed error of the connection's class, and never a result: the host reads
// nothing of the command from it. A stream that breaks after some events delivers them first.
func TestATransportThatBreaksBeforeTheEndIsATypedErrorAfterTheEventsThatCameFirst(t *testing.T) {
	wantConnection := func(t *testing.T, err error) {
		t.Helper()
		var typed *exec.Error
		if !errors.As(err, &typed) || typed.Class != exec.ErrorClassConnection {
			t.Errorf("a call whose transport broke returned %#v, want the CONNECTION class", err)
		}
	}
	t.Run("Run", func(t *testing.T) {
		running := make(chan struct{})
		client, server := serveScripted(t, &scriptedRunServer{run: func(ctx context.Context, _ *execv2.RunRequest) (*execv2.RunResult, error) {
			close(running)
			<-ctx.Done()
			return nil, ctx.Err()
		}})
		returned := make(chan error, 1)
		go func() {
			result, err := client.Run(context.Background(), &execv2.RunRequest{Argv: []string{"x"}})
			if result != nil {
				err = errors.New("a call whose transport broke returned a result")
			}
			returned <- err
		}()
		select {
		case <-running:
		case err := <-returned:
			t.Fatalf("the call ended before it reached the plugin: %v", err)
		case <-time.After(wireWait):
			t.Fatal("the call did not reach the plugin")
		}
		server.Stop()
		select {
		case err := <-returned:
			wantConnection(t, err)
		case <-time.After(wireWait):
			t.Fatal("the call did not end when the transport broke")
		}
	})
	t.Run("RunStream", func(t *testing.T) {
		client, server := serveScripted(t, &scriptedRunServer{runStream: func(_ *execv2.RunRequest, stream execv2.Exec_RunStreamServer) error {
			for _, data := range []string{"one", "two"} {
				if err := stream.Send(outputEvent(data)); err != nil {
					return err
				}
			}
			<-stream.Context().Done()
			return stream.Context().Err()
		}})
		conn, err := client.RunStream(context.Background(), &execv2.RunRequest{Argv: []string{"x"}})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		for _, want := range []string{"one", "two"} {
			event, err := conn.Recv()
			if err != nil || string(event.GetOutput().GetData()) != want {
				t.Fatalf("the event %q before the break: %v, %v", want, event, err)
			}
		}
		server.Stop()
		received := make(chan error, 1)
		go func() {
			_, err := conn.Recv()
			received <- err
		}()
		select {
		case err := <-received:
			wantConnection(t, err)
		case <-time.After(wireWait):
			t.Fatal("the stream did not end when the transport broke")
		}
	})
}

func outputEvent(data string) *execv2.RunEvent {
	return &execv2.RunEvent{Event: &execv2.RunEvent_Output{Output: &execv2.Output{Stream: execv2.Stream_STREAM_STDOUT, Data: []byte(data)}}}
}

// recvEvents reads a conn to its end and returns the events and the error that ended it.
func recvEvents(conn exec.RunConn) ([]*execv2.RunEvent, error) {
	var events []*execv2.RunEvent
	for {
		event, err := conn.Recv()
		if err != nil {
			return events, err
		}
		events = append(events, event)
	}
}

// A stream over a transport is what the plugin sent, in order, and ends with io.EOF after RunEnded. The event of a kind the
// reader does not know, here one with no kind, is passed on for the consumer to ignore.
func TestARunStreamOverATransportDeliversTheEventsInOrderAndEndsAfterRunEnded(t *testing.T) {
	sent := []*execv2.RunEvent{
		{Event: &execv2.RunEvent_Started{Started: &execv2.RunStarted{Target: &execv2.TargetFacts{Provider: "p"}}}},
		outputEvent("a"), {},
		{Event: &execv2.RunEvent_Progress{Progress: &execv2.RunProgress{ElapsedMs: 3, BytesOut: 1}}},
		{Event: &execv2.RunEvent_Ended{Ended: &execv2.RunEnded{Result: &execv2.RunResult{Outcome: execv2.Outcome_OUTCOME_LOST, RemoteMaySurvive: new(true)}}}},
	}
	client, _ := serveScripted(t, &scriptedRunServer{runStream: func(_ *execv2.RunRequest, stream execv2.Exec_RunStreamServer) error {
		for _, event := range sent {
			if err := stream.Send(event); err != nil {
				return err
			}
		}
		return nil
	}})
	conn, err := client.RunStream(context.Background(), &execv2.RunRequest{Argv: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	got, err := recvEvents(conn)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("the stream ended with %v, want io.EOF after RunEnded", err)
	}
	if len(got) != len(sent) {
		t.Fatalf("%d events, the plugin sent %d", len(got), len(sent))
	}
	for i := range sent {
		if !proto.Equal(got[i], sent[i]) {
			t.Errorf("event %d is %v, want %v", i, got[i], sent[i])
		}
	}
	if ended := got[len(got)-1].GetEnded().GetResult(); ended.GetOutcome() != execv2.Outcome_OUTCOME_LOST || ended.RemoteMaySurvive == nil || !*ended.RemoteMaySurvive {
		t.Errorf("RunEnded carries %v", ended)
	}
}

// A plugin that ends a stream with status OK and no RunEnded broke the contract: the host is given an error and never the end of
// a run it did not see end. A stream that the plugin ends with a status after its events delivers the events and then the typed
// status.
func TestARunStreamThatEndsWithoutRunEndedIsAnErrorAndNeverTheEndOfARun(t *testing.T) {
	started := &execv2.RunEvent{Event: &execv2.RunEvent_Started{Started: &execv2.RunStarted{}}}
	for name, tc := range map[string]struct {
		end   error
		class exec.ErrorClass
	}{
		"status OK":                        {nil, exec.ErrorClassInternal},
		"a status of the connection":       {status.Error(codes.Unavailable, "gone"), exec.ErrorClassConnection},
		"a refusal of the plugin's own":    {refusalStatus(t, codes.Unavailable, "UNAVAILABLE", map[string]string{"reason": "NOT_RUNNING"}), exec.ErrorClassUnavailable},
		"a cancel the plugin reported":     {status.Error(codes.Canceled, "cancelled"), exec.ErrorClassCancelled},
		"a status the plugin made up":      {status.Error(codes.DataLoss, "text that is not shown"), exec.ErrorClassInternal},
		"a deadline the plugin reported":   {status.Error(codes.DeadlineExceeded, "late"), exec.ErrorClassTimeout},
		"an Unimplemented after an event":  {status.Error(codes.Unimplemented, "no"), exec.ErrorClassUnsupported},
		"a permission the plugin withdrew": {status.Error(codes.PermissionDenied, "no"), exec.ErrorClassForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			client, _ := serveScripted(t, &scriptedRunServer{runStream: func(_ *execv2.RunRequest, stream execv2.Exec_RunStreamServer) error {
				if err := stream.Send(started); err != nil {
					return err
				}
				if err := stream.Send(outputEvent("partial")); err != nil {
					return err
				}
				return tc.end
			}})
			conn, err := client.RunStream(context.Background(), &execv2.RunRequest{Argv: []string{"x"}})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			events, err := recvEvents(conn)
			var typed *exec.Error
			if errors.Is(err, io.EOF) || !errors.As(err, &typed) || typed.Class != tc.class {
				t.Errorf("the stream ended with %#v, want class %s and never io.EOF", err, tc.class)
			}
			if len(events) != 2 {
				t.Errorf("%d events before the end, the plugin sent 2: they are delivered first", len(events))
			}
		})
	}
}
