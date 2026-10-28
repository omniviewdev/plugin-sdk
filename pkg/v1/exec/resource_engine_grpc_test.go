package exec_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execpb "github.com/omniviewdev/plugin-sdk/proto/v1/exec"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type streamReadyManager struct {
	*exec.Manager
	ready chan struct{}
}

func (m *streamReadyManager) Stream(ctx context.Context, in chan exec.StreamInput) (chan exec.StreamOutput, error) {
	out, err := m.Manager.Stream(ctx, in)
	close(m.ready)
	return out, err
}

func TestManagerNonresourceActualGRPCTerminalLifecycle(t *testing.T) {
	terminal, err := exectest.NewFakeTerminal()
	if err != nil {
		t.Fatal(err)
	}
	h := exectest.Mount(t, exectest.WithTerminalFactory(exectest.FakeTerminalFactory(terminal)), exectest.WithHandler(exec.Handler{TTYHandler: exectest.NoopTTYHandler()}))
	exec.SetSink(h.Manager, nil)
	provider := &streamReadyManager{Manager: h.Manager, ready: make(chan struct{})}
	server := grpc.NewServer()
	exec.RegisterExecPluginServer(server, &exec.PluginServer{Impl: provider})
	listener := bufconn.Listen(1 << 20)
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	connection, err := grpc.NewClient("passthrough:///terminal-lifecycle", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Close() })
	client := execpb.NewExecPluginClient(connection)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	created, err := client.CreateSession(ctx, &execpb.SessionOptions{Id: "plain-terminal", Command: []string{"sh"}})
	if err != nil || created.Session.Id != "plain-terminal" {
		t.Fatalf("create: %+v %v", created, err)
	}
	attached, err := client.AttachSession(ctx, &execpb.AttachSessionRequest{Id: created.Session.Id})
	if err != nil || attached.Session.Id != created.Session.Id {
		t.Fatalf("attach: %+v %v", attached, err)
	}
	stream, err := client.Stream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&execpb.StreamInput{Id: created.Session.Id}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.ready:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := terminal.InjectOutput([]byte("actual RPC terminal output")); err != nil {
		t.Fatal(err)
	}
	output, err := stream.Recv()
	if err != nil || output.Id != created.Session.Id || string(output.Data) != "actual RPC terminal output" {
		t.Fatalf("output: %+v %v", output, err)
	}
	closed, err := client.CloseSession(ctx, &execpb.CloseSessionRequest{Id: created.Session.Id})
	if err != nil || !closed.Success {
		t.Fatalf("close: %+v %v", closed, err)
	}
	signal, err := stream.Recv()
	if err != nil || signal.Id != created.Session.Id || signal.Signal != execpb.StreamSignal_STREAM_SIGNAL_CLOSE {
		t.Fatalf("close signal: %+v %v", signal, err)
	}
}
