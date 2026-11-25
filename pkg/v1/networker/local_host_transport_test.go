package networker

import (
	"context"
	"testing"

	"google.golang.org/grpc"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	networkerpb "github.com/omniviewdev/plugin-sdk/proto/v1/networker"
)

// A forwarder's stated host reaches the host's client through the real server
// and client of the plugin's gRPC service, on every call that returns a session,
// and a forwarder that states none leaves the client with none. A value that is
// not an address never reaches the encoder: the session still starts, and Get
// and List still answer for it (a frame the encoder refused would fail them for
// the plugin for as long as the session lives).
func TestSessionLocalHostThroughGRPC(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stated string
		want   string
	}{
		{"loopback v4", "127.0.0.1", "127.0.0.1"},
		{"loopback v6", "::1", "::1"},
		{"localhost", "localhost", "localhost"},
		{"an address with a zone", "fe80::1%en0", "fe80::1%en0"},
		{"not stated", "", ""},
		{"a zone that is not UTF-8", "fe80::1%\xff\xfe", ""},
		{"a zone with control characters", "fe80::1%\x01\x02", ""},
		{"an address with a port", "127.0.0.1:8080", ""},
		{"text that is not UTF-8", "127.0.0.1\xff", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(
				ManagerConfig{PortChecker: canonicalPorts{}},
				PluginOpts{
					StaticForwarders: map[string]StaticForwarder{
						"fixture": StaticForwarderFunc(
							func(context.Context, *types.PluginContext, StaticPortForwardHandlerOpts) (*ForwarderResult, error) {
								ready := make(chan struct{})
								close(ready)
								return &ForwarderResult{LocalHost: tc.stated, Ready: ready, ErrCh: make(chan error)}, nil
							},
						),
					},
				},
			)
			t.Cleanup(manager.StopAll)
			server := grpc.NewServer()
			RegisterNetworkerPluginServer(server, &PluginServer{Impl: manager})
			client := &PluginClient{client: networkerpb.NewNetworkerPluginClient(networkPipe(t, server))}

			started, err := client.StartPortForwardSession(
				&types.PluginContext{},
				PortForwardSessionOptions{
					Protocol:       PortForwardProtocolTCP,
					ConnectionType: PortForwardConnectionTypeStatic,
					Connection:     PortForwardStaticConnection{Address: "fixture"},
					RemotePort:     80,
				},
			)
			if err != nil {
				t.Fatalf("start: %v", err)
			}
			if started.LocalHost != tc.want || started.LocalPort != 32001 {
				t.Fatalf("started session has local host %q and port %d, want %q and 32001",
					started.LocalHost, started.LocalPort, tc.want)
			}

			got, err := client.GetPortForwardSession(&types.PluginContext{}, started.ID)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if got.LocalHost != tc.want {
				t.Fatalf("got session has local host %q, want %q", got.LocalHost, tc.want)
			}

			listed, err := client.ListPortForwardSessions(&types.PluginContext{})
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(listed) != 1 || listed[0].LocalHost != tc.want {
				t.Fatalf("listed sessions %+v, want one with local host %q", listed, tc.want)
			}
		})
	}
}
