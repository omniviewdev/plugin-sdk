package networker

import (
	"context"
	"fmt"
	"math"
	"net"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/interceptors"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	networkerpb "github.com/omniviewdev/plugin-sdk/proto/v1/networker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/mem"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/types/known/emptypb"
)

type canonicalPorts struct{}

func (canonicalPorts) FindFreePort() (int32, error) { return 32001, nil }
func (canonicalPorts) IsPortUnavailable(int32) bool { return false }

func networkPipe(t *testing.T, server *grpc.Server, pluginMetadata ...bool) *grpc.ClientConn {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	options := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() })}
	if len(pluginMetadata) != 0 && pluginMetadata[0] {
		options = append(options, grpc.WithUnaryInterceptor(sdk.ClientPluginContextInterceptor(nil)))
	}
	connection, err := grpc.NewClient("passthrough:///network-test", options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Close() })
	return connection
}

func canonicalResourceOptions(body *resourcev2.Value) PortForwardSessionOptions {
	return PortForwardSessionOptions{Protocol: PortForwardProtocolTCP, ConnectionType: PortForwardConnectionTypeResource, RemotePort: 80, Connection: PortForwardResourceConnection{PluginID: "opaque-provider", ResourceKey: "opaque/type", ResourceID: "opaque::id/%2F", ResourceData: body}}
}

func TestResourceNetworkActualNativeGRPCLosslessAndLifecycle(t *testing.T) {
	for _, size := range []int{0, 70 << 10, 3 << 20} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			var seen PortForwardResourceConnection
			var calls atomic.Int32
			manager := NewManager(ManagerConfig{PortChecker: canonicalPorts{}}, PluginOpts{ResourceForwarders: map[string]ResourceForwarder{"opaque/type": ResourceForwarderFunc(func(ctx context.Context, _ *types.PluginContext, o ResourcePortForwardHandlerOpts) (*ForwarderResult, error) {
				seen = o.Resource
				calls.Add(1)
				ready := make(chan struct{})
				close(ready)
				return &ForwarderResult{Ready: ready, ErrCh: make(chan error)}, nil
			})}})
			t.Cleanup(manager.StopAll)
			_, accepted := networkCatalogFixture(t, manager)
			server := grpc.NewServer(append(resourceplugin.ServerOptions(), grpc.UnaryInterceptor(interceptors.UnaryPluginContext()))...)
			RegisterNetworkerPluginServer(server, &PluginServer{Impl: manager})
			client := &PluginClient{client: networkerpb.NewNetworkerPluginClient(networkPipe(t, server, true))}
			var body *resourcev2.Value
			var native any
			if size > 0 {
				native = map[string]any{"min": int64(math.MinInt64), "max": uint64(math.MaxUint64), "preEpoch": time.Unix(-1, 123).UTC(), "nested": []any{[]any{math.Copysign(0, -1)}}, "large": strings.Repeat("x", size)}
				value, err := resourcev2.ValueFromNative(native)
				if err != nil {
					t.Fatal(err)
				}
				body = &value
			}
			opts := canonicalResourceOptions(body)
			target, _ := resourceConnection(opts.Connection)
			if size > 0 {
				token := "opaque::incarnation/%2F"
				target.Reference = &resourcev2.ResourceReference{Address: resourcev2.ResourceAddress{Provider: "opaque-provider", Authority: "fixture", Type: "opaque/type", Scope: []resourcev2.ScopeEntry{{Dimension: "namespace", Value: "opaque::scope/%2F"}}, ID: "opaque::id/%2F"}, Incarnation: &token}
				target.Route = &resourcev2.AccessRoute{Connection: "fixture"}
			}
			opts.Connection = target
			pc := *accepted
			if target.Reference == nil {
				pc.Context = context.Background()
			}
			session, err := client.StartPortForwardSession(&pc, opts)
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 {
				t.Fatalf("native calls: %d", calls.Load())
			}
			if !reflect.DeepEqual(target.Reference, seen.Reference) || !reflect.DeepEqual(target.Route, seen.Route) {
				t.Fatal("reference or route changed")
			}
			if body != nil {
				got, err := resourcev2.NativeFromValue(*seen.ResourceData)
				if err != nil || !reflect.DeepEqual(got, native) {
					t.Fatalf("native loss: %v", err)
				}
				if !math.Signbit(got.(map[string]any)["nested"].([]any)[0].([]any)[0].(float64)) {
					t.Fatal("negative zero lost")
				}
			} else if seen.ResourceData != nil {
				t.Fatal("invented resource body")
			}
			assertSession := func(got *PortForwardSession, err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
				connection, ok := resourceConnection(got.Connection)
				if !ok || !reflect.DeepEqual(connection, seen) {
					t.Fatal("session body changed")
				}
				if body != nil && !math.Signbit(connection.ResourceData.Map["nested"].List[0].List[0].Double) {
					t.Fatal("response negative zero lost")
				}
			}
			assertSession(client.GetPortForwardSession(&pc, session.ID))
			listed, err := client.ListPortForwardSessions(&pc)
			if err != nil || len(listed) != 1 {
				t.Fatalf("list: %v", err)
			}
			assertSession(listed[0], nil)
			found, err := client.FindPortForwardSessions(&pc, FindPortForwardSessionRequest{ResourceID: "opaque::id/%2F"})
			if err != nil || len(found) != 1 {
				t.Fatalf("find: %v", err)
			}
			assertSession(found[0], nil)
			assertSession(client.ClosePortForwardSession(&pc, session.ID))
		})
	}
}

type oldNetworkServer struct {
	networkerpb.UnimplementedNetworkerPluginServer
	calls atomic.Int32
}

func (s *oldNetworkServer) StartPortForwardSession(context.Context, *networkerpb.PortForwardSessionOptions) (*networkerpb.PortForwardSessionByIdResponse, error) {
	s.calls.Add(1)
	return &networkerpb.PortForwardSessionByIdResponse{}, nil
}
func TestResourceNetworkOldServerNoFallback(t *testing.T) {
	server := grpc.NewServer()
	old := &oldNetworkServer{}
	networkerpb.RegisterNetworkerPluginServer(server, old)
	client := &PluginClient{client: networkerpb.NewNetworkerPluginClient(networkPipe(t, server))}
	_, err := client.StartPortForwardSession(&types.PluginContext{}, canonicalResourceOptions(nil))
	if resourcev2.ErrorCode(err) != "UNSUPPORTED" || old.calls.Load() != 0 {
		t.Fatalf("fallback: %v calls %d", err, old.calls.Load())
	}
}

type networkRawCodec struct{ data []byte }

func (networkRawCodec) Name() string { return "proto" }
func (c networkRawCodec) Marshal(any) (mem.BufferSlice, error) {
	return mem.BufferSlice{mem.SliceBuffer(c.data)}, nil
}
func (networkRawCodec) Unmarshal(data mem.BufferSlice, v any) error {
	return encoding.GetCodecV2("proto").Unmarshal(data, v)
}
func networkBytes(field protowire.Number, data []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(nil, field, protowire.BytesType), data)
}

type networkAdmissionProvider struct {
	Provider
	calls atomic.Int32
}

func (p *networkAdmissionProvider) StartPortForwardSession(*types.PluginContext, PortForwardSessionOptions) (*PortForwardSession, error) {
	p.calls.Add(1)
	return nil, nil
}
func (p *networkAdmissionProvider) StartResourcePortForwardSession(*types.PluginContext, PortForwardSessionOptions) (*PortForwardSession, error) {
	p.calls.Add(1)
	return nil, nil
}
func TestResourceNetworkMalformedDefaultProtoRequestAdmission(t *testing.T) {
	version := []byte{56, 2}
	duplicate := append(append([]byte{}, version...), version...)
	cases := []struct {
		name, method, code string
		data               []byte
	}{
		{"old-resource", "StartPortForwardSession", "UNSUPPORTED", networkBytes(10, nil)},
		{"missing-version", "StartResourcePortForwardSession", "UNSUPPORTED", networkBytes(10, nil)},
		{"reserved-struct", "StartResourcePortForwardSession", "UNSUPPORTED", networkBytes(10, append(append([]byte{}, version...), networkBytes(5, nil)...))},
		{"duplicate-version", "StartResourcePortForwardSession", "INVALID_REQUEST", networkBytes(10, duplicate)},
		{"value-oneof", "StartResourcePortForwardSession", "INVALID_REQUEST", networkBytes(10, append(append([]byte{}, version...), networkBytes(6, []byte{10, 0, 26, 0})...))},
		{"utf8-selector", "StartResourcePortForwardSession", "INVALID_REQUEST", networkBytes(10, append(append([]byte{}, version...), networkBytes(4, []byte{255})...))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &networkAdmissionProvider{}
			server := grpc.NewServer()
			RegisterNetworkerPluginServer(server, &PluginServer{Impl: provider})
			connection := networkPipe(t, server)
			err := connection.Invoke(context.Background(), "/"+networkerpb.NetworkerPlugin_ServiceDesc.ServiceName+"/"+tc.method, &networkerpb.PortForwardSessionOptions{}, &networkerpb.ResourcePortForwardSessionResponse{}, grpc.ForceCodecV2(networkRawCodec{tc.data}))
			if resourcev2.ErrorCode(resourceplugin.ResourceDomainError(err)) != tc.code || provider.calls.Load() != 0 {
				t.Fatalf("admission: %v calls %d", err, provider.calls.Load())
			}
		})
	}
}

func TestResourceNetworkMalformedDefaultProtoResponses(t *testing.T) {
	version := []byte{56, 2}
	badConnections := [][]byte{nil, append(append([]byte{}, version...), version...), append(append([]byte{}, version...), networkBytes(5, nil)...), append(append([]byte{}, version...), networkBytes(6, []byte{10, 0, 26, 0})...)}
	for index, bad := range badConnections {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			session := networkBytes(20, bad)
			for _, method := range []string{"StartResourcePortForwardSession", "GetPortForwardSession", "ListPortForwardSessions", "FindPortForwardSessions", "ClosePortForwardSession"} {
				t.Run(method, func(t *testing.T) {
					data := networkBytes(1, session)
					if method == "StartResourcePortForwardSession" {
						data = append([]byte{8, 2}, networkBytes(2, session)...)
					}
					var served atomic.Int32
					server := grpc.NewServer(grpc.ForceServerCodecV2(networkRawCodec{data: data}), grpc.UnaryInterceptor(func(ctx context.Context, request any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
						served.Add(1)
						return handler(ctx, request)
					}))
					networkerpb.RegisterNetworkerPluginServer(server, &malformedNetworkServer{})
					connection := networkPipe(t, server)
					var response any = &networkerpb.PortForwardSessionByIdResponse{}
					if method == "StartResourcePortForwardSession" {
						response = &networkerpb.ResourcePortForwardSessionResponse{}
					}
					if method == "ListPortForwardSessions" || method == "FindPortForwardSessions" {
						response = &networkerpb.PortForwardSessionListResponse{}
					}
					var request any = &networkerpb.PortForwardSessionByIdRequest{Id: "session"}
					if method == "ListPortForwardSessions" {
						request = &emptypb.Empty{}
					}
					if method == "FindPortForwardSessions" {
						request = &networkerpb.FindPortForwardSessionRequest{}
					}
					if method == "StartResourcePortForwardSession" {
						options := canonicalResourceOptions(nil)
						wire, err := options.ToProto()
						if err != nil {
							t.Fatal(err)
						}
						request = wire
					}
					options, admissionError := networkResponseOptions()
					err := connection.Invoke(context.Background(), "/"+networkerpb.NetworkerPlugin_ServiceDesc.ServiceName+"/"+method, request, response, options...)
					if err == nil {
						err = admissionError()
					}
					if err == nil || served.Load() != 1 {
						t.Fatalf("response admission: %v served %d", err, served.Load())
					}
				})
			}
		})
	}
}

type malformedNetworkServer struct {
	networkerpb.UnimplementedNetworkerPluginServer
}

func (*malformedNetworkServer) StartResourcePortForwardSession(context.Context, *networkerpb.PortForwardSessionOptions) (*networkerpb.ResourcePortForwardSessionResponse, error) {
	return &networkerpb.ResourcePortForwardSessionResponse{}, nil
}
func (*malformedNetworkServer) GetPortForwardSession(context.Context, *networkerpb.PortForwardSessionByIdRequest) (*networkerpb.PortForwardSessionByIdResponse, error) {
	return &networkerpb.PortForwardSessionByIdResponse{}, nil
}
func (*malformedNetworkServer) ClosePortForwardSession(context.Context, *networkerpb.PortForwardSessionByIdRequest) (*networkerpb.PortForwardSessionByIdResponse, error) {
	return &networkerpb.PortForwardSessionByIdResponse{}, nil
}
func (*malformedNetworkServer) ListPortForwardSessions(context.Context, *emptypb.Empty) (*networkerpb.PortForwardSessionListResponse, error) {
	return &networkerpb.PortForwardSessionListResponse{}, nil
}
func (*malformedNetworkServer) FindPortForwardSessions(context.Context, *networkerpb.FindPortForwardSessionRequest) (*networkerpb.PortForwardSessionListResponse, error) {
	return &networkerpb.PortForwardSessionListResponse{}, nil
}

func TestResourceNetworkStaticLegacyLifecycle(t *testing.T) {
	var calls atomic.Int32
	manager := NewManager(ManagerConfig{PortChecker: canonicalPorts{}}, PluginOpts{StaticForwarders: map[string]StaticForwarder{"fixture": StaticForwarderFunc(func(context.Context, *types.PluginContext, StaticPortForwardHandlerOpts) (*ForwarderResult, error) {
		calls.Add(1)
		ready := make(chan struct{})
		close(ready)
		return &ForwarderResult{Ready: ready, ErrCh: make(chan error)}, nil
	})}})
	t.Cleanup(manager.StopAll)
	server := grpc.NewServer()
	RegisterNetworkerPluginServer(server, &PluginServer{Impl: manager})
	client := &PluginClient{client: networkerpb.NewNetworkerPluginClient(networkPipe(t, server))}
	session, err := client.StartPortForwardSession(&types.PluginContext{}, PortForwardSessionOptions{Protocol: PortForwardProtocolTCP, ConnectionType: PortForwardConnectionTypeStatic, Connection: PortForwardStaticConnection{Address: "fixture"}, RemotePort: 80})
	if err != nil || calls.Load() != 1 {
		t.Fatalf("static start: %v", err)
	}
	if _, err := client.GetPortForwardSession(&types.PluginContext{}, session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ClosePortForwardSession(&types.PluginContext{}, session.ID); err != nil {
		t.Fatal(err)
	}
}

type networkFailureProvider struct {
	Provider
	failure error
}

func (p *networkFailureProvider) StartResourcePortForwardSession(*types.PluginContext, PortForwardSessionOptions) (*PortForwardSession, error) {
	return nil, p.failure
}
func TestResourceNetworkC02Errors(t *testing.T) {
	for _, code := range []string{"INVALID_REQUEST", "RESET_REQUIRED", "FORBIDDEN", "UNAVAILABLE"} {
		t.Run(code, func(t *testing.T) {
			provider := &networkFailureProvider{failure: fmt.Errorf("forwarder: %w", resourcev2.NewError(code, "private detail"))}
			server := grpc.NewServer()
			RegisterNetworkerPluginServer(server, &PluginServer{Impl: provider})
			client := &PluginClient{client: networkerpb.NewNetworkerPluginClient(networkPipe(t, server))}
			_, err := client.StartPortForwardSession(&types.PluginContext{}, canonicalResourceOptions(nil))
			if resourcev2.ErrorCode(err) != code {
				t.Fatalf("error class: %v", err)
			}
		})
	}
}
