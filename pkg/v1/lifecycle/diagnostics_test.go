package lifecycle

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/diagnostics"
	lifecyclepb "github.com/omniviewdev/plugin-sdk/proto/v1/lifecycle"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func dial(t *testing.T, register func(*grpc.Server)) *Client {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	register(server)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///diag", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
		return listener.Dial()
	}), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return NewClient(lifecyclepb.NewPluginLifecycleClient(conn))
}

func TestGetDiagnosticsRoundTrip(t *testing.T) {
	reg := diagnostics.NewRegistry()
	c, _ := reg.Counter("omniview.plugin.calls", "1")
	c.Add(3, diagnostics.Attr{Key: diagnostics.KeyOutcome, Value: "ok"})
	client := dial(t, func(s *grpc.Server) {
		lifecyclepb.RegisterPluginLifecycleServer(s, &Server{PluginID: "p", Diagnostics: reg.Snapshot, StartUnixNano: reg.StartUnixNano()})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := client.GetDiagnostics(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.SchemaVersion != 1 || resp.PluginId != "p" || resp.ProcessStartUnixNs != reg.StartUnixNano() || len(resp.Metrics) != 1 || resp.Metrics[0].Points[0].Value != 3 {
		t.Fatalf("%+v", resp)
	}
}

func TestGetDiagnosticsWithoutRegistryIsEmptyNotUnimplemented(t *testing.T) {
	client := dial(t, func(s *grpc.Server) { lifecyclepb.RegisterPluginLifecycleServer(s, &Server{PluginID: "p"}) })
	resp, err := client.GetDiagnostics(context.Background(), 1)
	if err != nil || len(resp.Metrics) != 0 || resp.SchemaVersion != 1 {
		t.Fatalf("%+v %v", resp, err)
	}
}

func TestOlderServerReportsUnimplemented(t *testing.T) {
	client := dial(t, func(s *grpc.Server) { // a server lacking the method entirely
		s.RegisterService(&grpc.ServiceDesc{ServiceName: "omniview.sdk.lifecycle.v1.PluginLifecycle", HandlerType: (*interface{})(nil)}, struct{}{})
	})
	_, err := client.GetDiagnostics(context.Background(), 1)
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("want Unimplemented, got %v", err)
	}
}
