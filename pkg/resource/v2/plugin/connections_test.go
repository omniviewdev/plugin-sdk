package plugin

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

type mismatchConnectionProvider struct{ countProvider }

func (*mismatchConnectionProvider) LoadConnections(context.Context) ([]types.Connection, error) {
	return []types.Connection{}, nil
}

func (*mismatchConnectionProvider) GetConnection(context.Context, string) (types.Connection, error) {
	return types.Connection{ID: "other"}, nil
}

func (*mismatchConnectionProvider) StopConnection(context.Context, string) (types.Connection, error) {
	return types.Connection{ID: "other"}, nil
}

func (*mismatchConnectionProvider) StartConnection(context.Context, string) (types.ConnectionStatus, error) {
	return types.ConnectionStatus{Connection: &types.Connection{ID: "other"}}, nil
}

func (*mismatchConnectionProvider) CheckConnection(context.Context, string) (types.ConnectionStatus, error) {
	return types.ConnectionStatus{Connection: &types.Connection{ID: "other"}}, nil
}

func (*mismatchConnectionProvider) GetConnectionStatus(context.Context, string) (types.ConnectionStatus, error) {
	return types.ConnectionStatus{
		Connection: &types.Connection{ID: "other"},
		Status:     types.ConnectionStatusDisconnected,
	}, nil
}

func assertConnectionMismatch(t *testing.T, c *Client) {
	t.Helper()
	for _, operation := range []string{"get", "stop", "start", "check", "state"} {
		t.Run(operation, func(t *testing.T) {
			var err error
			switch operation {
			case "get":
				_, err = c.GetConnection(context.Background(), "requested")
			case "stop":
				_, err = c.StopConnection(context.Background(), "requested")
			case "start":
				_, err = c.StartConnection(context.Background(), "requested")
			case "check":
				_, err = c.CheckConnection(context.Background(), "requested")
			case "state":
				_, err = c.GetConnectionStatus(context.Background(), "requested")
			}
			if resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
				t.Fatalf("connection mismatch accepted: %v", err)
			}
		})
	}
}

func TestServerBindsConnectionResponseIDs(t *testing.T) {
	s := grpc.NewServer()
	RegisterServer(s, &mismatchConnectionProvider{})
	assertConnectionMismatch(t, NewClient(pipe(t, s)))
}

type maliciousConnectionServer struct {
	resourcepb.UnimplementedResourceProviderServer
	json []byte
}

func (s *maliciousConnectionServer) Call(_ context.Context, in *resourcepb.Request) (*resourcepb.Response, error) {
	out := &resourcepb.ConnectionOutput{}
	operation := in.GetConnection().Operation
	if s.json != nil {
		out.ConnectionJson = s.json
	} else if operation == "start" || operation == "check" || operation == "state" {
		out.StatusJson = []byte(`{"connection":{"id":"other"},"status":0,"error":"","details":""}`)
	} else {
		out.ConnectionJson = []byte(`{"id":"other"}`)
	}
	return &resourcepb.Response{Version: 2, Result: &resourcepb.Response_Connection{Connection: out}}, nil
}

func TestClientBindsUntrustedConnectionResponseIDs(t *testing.T) {
	s := grpc.NewServer()
	resourcepb.RegisterResourceProviderServer(s, &maliciousConnectionServer{})
	assertConnectionMismatch(t, NewClient(pipe(t, s)))
}

func TestActualGRPCConnectionJSONRejectsAmbiguity(t *testing.T) {
	for _, data := range [][]byte{[]byte(`{"id":"requested","id":"requested"}`), []byte(`{"id":"requested","unknown":true}`), {'{', '"', 'i', 'd', '"', ':', '"', 255, '"', '}'}} {
		s := grpc.NewServer()
		resourcepb.RegisterResourceProviderServer(s, &maliciousConnectionServer{json: data})
		_, err := NewClient(pipe(t, s)).GetConnection(context.Background(), "requested")
		if resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
			t.Fatalf("malformed connection JSON accepted: %v", err)
		}
	}
}

type blockedCloser struct {
	countProvider
	started chan struct{}
	release chan struct{}
}

func (p *blockedCloser) Close() error { close(p.started); <-p.release; return nil }
func TestActualGRPCCloseUsesFiniteDeadline(t *testing.T) {
	if closeTimeout != 5*time.Second {
		t.Fatal("production close deadline changed")
	}
	p := &blockedCloser{started: make(chan struct{}), release: make(chan struct{})}
	defer close(p.release)
	s := grpc.NewServer()
	RegisterServer(s, p)
	c := NewClient(pipe(t, s))
	start := time.Now()
	err := c.closeWithTimeout(50 * time.Millisecond)
	elapsed := time.Since(start)
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("close deadline class: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("close exceeded teardown deadline: %s", elapsed)
	}
	select {
	case <-p.started:
	default:
		t.Fatal("native Close was not invoked")
	}
}
