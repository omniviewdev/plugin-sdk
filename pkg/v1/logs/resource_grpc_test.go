package logs

import (
	"context"
	"io"
	"math"
	"net"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/pkg/interceptors"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	logspb "github.com/omniviewdev/plugin-sdk/proto/v1/logs"
)

func logsConnection(t *testing.T, register func(*grpc.Server)) *grpc.ClientConn {
	t.Helper()
	server := grpc.NewServer(
		grpc.MaxRecvMsgSize(resourceplugin.MaxWireBytes),
		grpc.MaxSendMsgSize(resourceplugin.MaxWireBytes),
		grpc.ChainUnaryInterceptor(interceptors.UnaryPluginContext()),
	)
	register(server)
	listener := bufconn.Listen(1 << 20)
	go server.Serve(listener)
	connection, err := grpc.NewClient(
		"passthrough:///logs",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithUnaryInterceptor(
			func(ctx context.Context, method string, request, response any, cc *grpc.ClientConn, invoke grpc.UnaryInvoker, options ...grpc.CallOption) error {
				if pc := types.PluginContextFromContext(ctx); pc != nil {
					pairs := []string{"omniview-request-id", pc.RequestID, "omniview-requester-id", pc.RequesterID}
					if pc.Connection != nil {
						pairs = append(pairs, "omniview-connection-id", pc.Connection.ID)
					}
					ctx = metadata.AppendToOutgoingContext(ctx, pairs...)
				}
				return invoke(ctx, method, request, response, cc, options...)
			},
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Close(); server.Stop(); listener.Close() })
	return connection
}

func exactLogBody() *resourcev2.Value {
	return &resourcev2.Value{Kind: "map", Map: map[string]resourcev2.Value{
		"int":    {Kind: "int64", Int: "-9223372036854775808"},
		"uint":   {Kind: "uint64", Uint: "18446744073709551615"},
		"before": {Kind: "timestamp", Timestamp: "1960-01-02T03:04:05.123456789Z"},
		"nested": {Kind: "list", List: []resourcev2.Value{{Kind: "double", Double: math.Copysign(0, -1)}}},
		"large":  {Kind: "string", String: strings.Repeat("x", 5<<20)},
	}}
}

func TestResourceLogsActualGRPCNativeCallbacks(t *testing.T) {
	for _, resolverMode := range []bool{false, true} {
		t.Run(map[bool]string{false: "builder", true: "resolver"}[resolverMode], func(t *testing.T) {
			body := exactLogBody()
			reference := &resourcev2.ResourceReference{
				Address: resourcev2.ResourceAddress{
					Provider:  "provider",
					Authority: "host-issued",
					Type:      "opaque::pod",
					Scope:     []resourcev2.ScopeEntry{},
					ID:        "opaque/id",
				},
			}
			route := &resourcev2.AccessRoute{Connection: "explicit"}
			seen := make(chan *resourcev2.Value, 2)
			handler := Handler{
				Resource: "opaque::pod",
				Handler: func(ctx *types.PluginContext, request LogStreamRequest) (io.ReadCloser, error) {
					if ctx == nil || ctx.Context == nil || ctx.Connection == nil || ctx.Connection.ID != "explicit" ||
						ctx.Connection.Data["configured"] != true {
						t.Error("native handler lost actual RPC context or connection")
					}
					if !reflect.DeepEqual(request.Reference, reference) || !reflect.DeepEqual(request.Route, route) {
						t.Error("native handler lost actual resource bindings")
					}
					seen <- request.ResourceData
					return io.NopCloser(strings.NewReader("native line\n")), nil
				},
			}
			handler.SourceBuilder = func(id string, value *resourcev2.Value, options LogSessionOptions) []LogSource {
				if !reflect.DeepEqual(options.Reference, reference) || !reflect.DeepEqual(options.Route, route) {
					t.Error("native builder lost actual resource bindings")
				}
				seen <- value
				return []LogSource{{ID: id}}
			}
			config := ManagerConfig{Handlers: map[string]Handler{"logs/opaque::pod": handler}, Sink: nativeLogSink{}}
			if resolverMode {
				config.Handlers = map[string]Handler{"logs/source": {Resource: "source", Handler: handler.Handler}}
				config.Resolvers = map[string]SourceResolver{
					"opaque::pod": func(_ *types.PluginContext, value *resourcev2.Value, options SourceResolverOptions) (*SourceResolverResult, error) {
						if !reflect.DeepEqual(options.Reference, reference) ||
							!reflect.DeepEqual(options.Route, route) {
							t.Error("native resolver lost actual resource bindings")
						}
						seen <- value
						return &SourceResolverResult{Sources: []LogSource{{ID: "source"}}}, nil
					},
				}
			}
			manager := NewManager(config)
			controller, revision := logsSourceController(t)
			manager.BindConnectionContextProvider(controller)
			t.Cleanup(manager.Close)
			connection := logsConnection(
				t,
				func(s *grpc.Server) { RegisterLogPluginServer(s, &PluginServer{Impl: manager}) },
			)
			client := &PluginClient{client: logspb.NewLogPluginClient(connection)}
			ctx := types.NewPluginContext(
				resourcev2.WithCatalogRevision(context.Background(), revision),
				"logs",
				nil,
				nil,
				nil,
			)
			ctx.Connection = &types.Connection{ID: "explicit"}
			session, err := client.CreateSession(
				ctx,
				CreateSessionOptions{
					ResourceKey:  "opaque::pod",
					ResourceID:   "opaque/id",
					ResourceData: body,
					Reference:    reference,
					Route:        route,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.CloseSession(ctx, session.ID)
			for range 2 {
				select {
				case got := <-seen:
					if !reflect.DeepEqual(got, body) || !math.Signbit(got.Map["nested"].List[0].Double) {
						t.Fatal("native callback changed canonical body")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("native callback missing")
				}
			}
		})
	}
}

func logsSourceController(t *testing.T) (*resourcev2.Controller[string], string) {
	t.Helper()
	controller, err := resourcev2.NewController(resourcev2.Config[string]{
		Catalog: func(context.Context, resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
			return resourcev2.Catalog{
				Provider: "provider",
				Revision: "logs-catalog",
				Coverage: resourcev2.Coverage{Kind: "complete", Paths: []string{}},
				Types:    []resourcev2.ResourceType{},
			}, nil
		},
		Lifecycle: &resourcev2.Lifecycle[string]{
			LoadConnections: func(context.Context) ([]types.Connection, error) {
				return []types.Connection{{ID: "explicit", Data: map[string]any{"configured": true}}}, nil
			},
			CreateClient:  func(context.Context, types.Connection) (*string, error) { value := "native"; return &value, nil },
			DestroyClient: func(context.Context, *string) error { return nil },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controller.Close() })
	if _, err := controller.LoadConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.StartConnection(context.Background(), "explicit"); err != nil {
		t.Fatal(err)
	}
	catalog, err := controller.Catalog(
		context.Background(),
		resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: "explicit"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return controller, catalog.Revision
}

func TestResourceLogsCatalogAdmissionBeforeNative(t *testing.T) {
	controller, revision := logsSourceController(t)
	var calls atomic.Int32
	native := make(chan struct{}, 1)
	manager := NewManager(ManagerConfig{Sink: nativeLogSink{}, Handlers: map[string]Handler{
		"logs/opaque::pod": {
			Resource: "opaque::pod",
			SourceBuilder: func(id string, _ *resourcev2.Value, _ LogSessionOptions) []LogSource {
				calls.Add(1)
				native <- struct{}{}
				return []LogSource{{ID: id}}
			},
			Handler: func(pc *types.PluginContext, _ LogStreamRequest) (io.ReadCloser, error) {
				if pc.Connection.Data["configured"] != true {
					t.Error("native callback received metadata profile")
				}
				return io.NopCloser(strings.NewReader("")), nil
			},
		},
	}})
	t.Cleanup(manager.Close)
	manager.BindConnectionContextProvider(controller)
	connection := logsConnection(t, func(s *grpc.Server) { RegisterLogPluginServer(s, &PluginServer{Impl: manager}) })
	client := &PluginClient{client: logspb.NewLogPluginClient(connection)}
	opts := CreateSessionOptions{
		ResourceKey: "opaque::pod",
		ResourceID:  "opaque/id",
		Reference: &resourcev2.ResourceReference{
			Address: resourcev2.ResourceAddress{
				Provider:  "provider",
				Authority: "host-issued",
				Type:      "opaque::pod",
				Scope:     []resourcev2.ScopeEntry{},
				ID:        "opaque/id",
			},
		},
		Route: &resourcev2.AccessRoute{Connection: "explicit"},
	}
	for _, pin := range []string{"", revision + "-stale"} {
		ctx := context.Background()
		if pin != "" {
			ctx = resourcev2.WithCatalogRevision(ctx, pin)
		}
		pc := types.NewPluginContext(ctx, "logs", nil, nil, nil)
		pc.Connection = &types.Connection{ID: "explicit"}
		if _, err := client.CreateResourceSession(pc, opts); resourcev2.ErrorCode(err) != "RESET_REQUIRED" {
			t.Fatalf("pin %q: %v", pin, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("rejected pin reached native source builder")
	}
	opts.Reference = nil
	opts.ResourceData = exactLogBody()
	pc := types.NewPluginContext(context.Background(), "logs", nil, nil, nil)
	pc.Connection = &types.Connection{ID: "explicit"}
	session, err := client.CreateResourceSession(pc, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.CloseSession(pc, session.ID)
	select {
	case <-native:
	case <-time.After(5 * time.Second):
		t.Fatal("opaque no-reference request did not reach native builder")
	}
	if calls.Load() != 1 {
		t.Fatal("unexpected native builder count")
	}
}

type logsPostFenceProvider struct {
	*resourcev2.Controller[string]
}

func (p logsPostFenceProvider) WithConnection(
	ctx context.Context,
	route resourcev2.AccessRoute,
	fn func(context.Context, types.Connection) error,
) error {
	return p.Controller.WithConnection(ctx, route, func(bound context.Context, connection types.Connection) error {
		if err := fn(bound, connection); err != nil {
			return err
		}
		return resourcev2.NewError("RESET_REQUIRED", "Generation retired after native creation.")
	})
}

func TestResourceLogsPostFenceClosesCreatedSession(t *testing.T) {
	controller, revision := logsSourceController(t)
	manager := NewManager(ManagerConfig{Sink: nativeLogSink{}})
	t.Cleanup(manager.Close)
	manager.BindConnectionContextProvider(logsPostFenceProvider{controller})
	pc := types.NewPluginContext(resourcev2.WithCatalogRevision(context.Background(), revision), "logs", nil, nil, nil)
	pc.Connection = &types.Connection{ID: "explicit"}
	session, err := manager.CreateResourceSession(
		pc,
		CreateSessionOptions{
			ResourceKey: "opaque::pod",
			ResourceID:  "opaque/id",
			Reference: &resourcev2.ResourceReference{
				Address: resourcev2.ResourceAddress{
					Provider:  "provider",
					Authority: "host-issued",
					Type:      "opaque::pod",
					Scope:     []resourcev2.ScopeEntry{},
					ID:        "opaque/id",
				},
			},
			Route: &resourcev2.AccessRoute{Connection: "explicit"},
		},
	)
	if session != nil || resourcev2.ErrorCode(err) != "RESET_REQUIRED" {
		t.Fatalf("post-fence result: %v %v", session, err)
	}
	sessions, err := manager.ListSessions(pc)
	if err != nil || len(sessions) != 0 {
		t.Fatalf("orphan sessions: %v %v", sessions, err)
	}
}

type oldLogServer struct {
	logspb.UnimplementedLogPluginServer
	calls atomic.Int32
}

func (s *oldLogServer) CreateSession(
	context.Context,
	*logspb.CreateLogSessionRequest,
) (*logspb.CreateLogSessionResponse, error) {
	s.calls.Add(1)
	return &logspb.CreateLogSessionResponse{}, nil
}

func TestResourceLogsOldServerNeverFallsBack(t *testing.T) {
	server := new(oldLogServer)
	connection := logsConnection(t, func(s *grpc.Server) { logspb.RegisterLogPluginServer(s, server) })
	client := &PluginClient{client: logspb.NewLogPluginClient(connection)}
	_, err := client.CreateSession(
		types.NewPluginContext(context.Background(), "logs", nil, nil, nil),
		CreateSessionOptions{ResourceKey: "pod"},
	)
	if resourcev2.ErrorCode(err) != "UNSUPPORTED" || server.calls.Load() != 0 {
		t.Fatalf("old server: %v calls=%d", err, server.calls.Load())
	}
}

type rawLogsCodec struct{ data []byte }

func (c rawLogsCodec) Name() string                { return "proto" }
func (c rawLogsCodec) Marshal(any) ([]byte, error) { return c.data, nil }
func (c rawLogsCodec) Unmarshal(data []byte, message any) error {
	return proto.Unmarshal(data, message.(proto.Message))
}

func TestResourceLogsMalformedDefaultProtoBeforeNative(t *testing.T) {
	server := new(oldLogServer)
	connection := logsConnection(t, func(s *grpc.Server) { RegisterLogPluginServer(s, server) })
	for _, test := range []struct {
		name, method string
		code         string
		data         []byte
	}{
		{"retired", "CreateSession", "UNSUPPORTED", []byte{0x1a, 0}},
		{"selector", "CreateSession", "UNSUPPORTED", []byte{0x0a, 1, 'x'}},
		{"duplicateVersion", "CreateResourceSession", "INVALID_REQUEST", []byte{0x30, 2, 0x30, 2}},
		{"invalidValue", "CreateResourceSession", "INVALID_REQUEST", []byte{0x30, 2, 0x2a, 2, 0x08, 99}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := connection.Invoke(
				context.Background(),
				"/"+logspb.LogPlugin_ServiceDesc.ServiceName+"/"+test.method,
				&logspb.CreateLogSessionRequest{},
				&logspb.ResourceLogSessionResponse{},
				grpc.ForceCodec(rawLogsCodec{test.data}),
			)
			if resourcev2.ErrorCode(resourceplugin.ResourceDomainError(err)) != test.code {
				t.Fatalf("expected %s without native effects: %v", test.code, err)
			}
		})
	}
	if server.calls.Load() != 0 {
		t.Fatal("legacy native side effect")
	}
}

func TestLogsNonresourceActualGRPCLifecycle(t *testing.T) {
	manager := NewManager(ManagerConfig{Sink: nativeLogSink{}})
	t.Cleanup(manager.Close)
	connection := logsConnection(t, func(s *grpc.Server) { RegisterLogPluginServer(s, &PluginServer{Impl: manager}) })
	client := logspb.NewLogPluginClient(connection)
	response, err := client.CreateSession(context.Background(), &logspb.CreateLogSessionRequest{})
	if err != nil || response.Session.Id == "" {
		t.Fatalf("create: %v", err)
	}
	_, err = client.GetSession(context.Background(), &logspb.LogSessionByIdRequest{SessionId: response.Session.Id})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CloseSession(context.Background(), &logspb.LogSessionByIdRequest{SessionId: response.Session.Id})
	if err != nil {
		t.Fatal(err)
	}
}

type nativeLogSink struct{}

func (nativeLogSink) OnLine(LogLine)                 {}
func (nativeLogSink) OnEvent(string, LogStreamEvent) {}

func TestLogsRPCContextLifetime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pc := types.NewPluginContext(ctx, "logs", nil, nil, nil)
	original := pc.Context
	outgoing := logsClientContext(pc)
	if pc.Context != original {
		t.Fatal("client changed caller context")
	}
	native := logsPluginContext(outgoing)
	if native.Context != outgoing || native.Connection != nil {
		t.Fatal("native context lost RPC lifetime or invented connection")
	}
	bare := logsPluginContext(ctx)
	if bare.Context != ctx || bare.Connection != nil || bare.RequesterID != "" {
		t.Fatal("metadata-free context lost RPC lifetime or invented identity")
	}
	cancel()
	if outgoing.Err() != context.Canceled || native.Context.Err() != context.Canceled ||
		bare.Context.Err() != context.Canceled {
		t.Fatal("logs contexts lost RPC cancellation")
	}
}
