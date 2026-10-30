package logs

import (
	"context"
	"io"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	logspb "github.com/omniviewdev/plugin-sdk/proto/v1/logs"
)

type lostAcceptanceLogServer struct{ *PluginServer }

func (s lostAcceptanceLogServer) CreateResourceSession(
	ctx context.Context,
	in *logspb.CreateLogSessionRequest,
) (*logspb.ResourceLogSessionResponse, error) {
	_, err := s.PluginServer.CreateResourceSession(ctx, in)
	if err != nil {
		return nil, err
	}
	// The provider has accepted, but the transport does not deliver its receipt.
	return nil, status.Error(codes.Unavailable, "acceptance reply lost")
}

func TestCorrelatedResourceSessionClosesAfterActualGRPCLostReply(t *testing.T) {
	controller, _ := logsSourceController(t)
	opened := make(chan struct{})
	reader := &cancelReader{closed: make(chan struct{})}
	manager := NewManager(ManagerConfig{Sink: nativeLogSink{}, Handlers: map[string]Handler{
		"logs/opaque::pod": {
			Plugin:   "logs",
			Resource: "opaque::pod",
			Handler:  func(*types.PluginContext, LogStreamRequest) (io.ReadCloser, error) { close(opened); return reader, nil },
		},
	}})
	defer manager.Close()
	manager.BindConnectionContextProvider(controller)
	connection := logsConnection(t, func(s *grpc.Server) {
		RegisterLogPluginServer(s, lostAcceptanceLogServer{&PluginServer{Impl: manager}})
	})
	client := &PluginClient{client: logspb.NewLogPluginClient(connection)}
	pc := types.NewPluginContext(context.Background(), "logs", nil, nil, nil)
	pc.Connection = &types.Connection{ID: "explicit"}
	id := "00000000-0000-4000-8000-000000000000"
	session, err := client.CreateResourceSession(
		pc,
		CreateSessionOptions{
			SessionID:   id,
			ResourceKey: "opaque::pod",
			ResourceID:  "opaque/id",
			Route:       &resourcev2.AccessRoute{Connection: "explicit"},
			Options:     LogSessionOptions{Follow: true},
		},
	)
	if status.Code(err) != codes.Unavailable || session != nil {
		t.Fatalf("expected lost accepted receipt, got: %v %v", session, err)
	}
	awaitLifetime(t, opened)
	if err := client.CloseSession(pc, id); err != nil {
		t.Fatal(err)
	}
	awaitLifetime(t, reader.closed)
	sessions, err := manager.ListSessions(pc)
	if err != nil || len(sessions) != 0 {
		t.Fatalf("lost-reply session survived cleanup: %v %v", sessions, err)
	}
	if _, err := controller.StopConnection(context.Background(), "explicit"); err != nil {
		t.Fatal(err)
	}
}
