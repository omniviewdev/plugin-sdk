package lifecycle

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/profiling"
	lifecyclepb "github.com/omniviewdev/plugin-sdk/proto/v1/lifecycle"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestProfilingStatusRPC(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config profiling.Config
	}{
		{name: "off"},
		{name: "enabled", config: profiling.Config{Enabled: true}},
		{name: "failure", config: profiling.Config{Enabled: true, Diagnostic: "listener unavailable"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := profiling.New(tc.config, "environment")
			startErr := service.Start()
			defer service.Close()
			if (startErr != nil) != (tc.config.Diagnostic != "") {
				t.Fatalf("start error=%v", startErr)
			}
			listener := bufconn.Listen(1024 * 1024)
			defer listener.Close()
			server := grpc.NewServer()
			defer server.Stop()
			lifecyclepb.RegisterPluginLifecycleServer(server, &Server{ProfilingStatus: service.GetStatus})
			go server.Serve(listener)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, err := grpc.DialContext(ctx, "passthrough:///profiling", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
				return listener.Dial()
			}), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			client := NewClient(lifecyclepb.NewPluginLifecycleClient(conn))
			status, err := client.GetProfilingStatus(ctx)
			want := service.GetStatus()
			if err != nil || status.Running != want.Running || status.Url != want.URL || status.Error != want.Error || status.Pid != int64(os.Getpid()) {
				t.Fatalf("status=%+v want=%+v error=%v", status, want, err)
			}
			service.Close()
			status, err = client.GetProfilingStatus(ctx)
			if err != nil || status.Running || status.Url != "" {
				t.Fatalf("closed status=%+v error=%v", status, err)
			}
		})
	}
}

func TestUninitializedProfilingClient(t *testing.T) {
	var client *Client
	if _, err := client.GetProfilingStatus(context.Background()); err == nil {
		t.Fatal("nil client accepted")
	}
}
