package exec_test

import (
	"context"
	"net"
	"os"
	"reflect"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/omniviewdev/plugin-sdk/pkg/interceptors"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
)

func TestResourceExecActualGRPCNativeCatalogAdmission(t *testing.T) {
	configured := types.Connection{ID: "route", Name: "configured", Data: map[string]any{"endpoint": "native"}}
	revision := "catalog-1"
	controller, controllerErr := resourcev2.NewController(resourcev2.Config[string]{
		Catalog: func(context.Context, resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
			return resourcev2.Catalog{
				Provider: "test",
				Revision: revision,
				Coverage: resourcev2.Coverage{Kind: "complete", Paths: []string{}},
				Types: []resourcev2.ResourceType{
					{
						Type:               "pod",
						IdentityDimensions: []resourcev2.Dimension{},
						QueryDimensions:    []resourcev2.Dimension{},
						Representations: []resourcev2.Representation{
							{
								ID:       "document",
								Revision: "representation-1",
								Schema: resourcev2.SchemaBinding{
									State:       "absent",
									Validation:  "none",
									Unvalidated: []string{},
								},
							},
						},
						Capabilities: []resourcev2.Capability{},
					},
				},
			}, nil
		},
		Lifecycle: &resourcev2.Lifecycle[string]{
			LoadConnections: func(context.Context) ([]types.Connection, error) { return []types.Connection{configured}, nil },
			CreateClient:    func(context.Context, types.Connection) (*string, error) { client := "native"; return &client, nil },
			DestroyClient:   func(context.Context, *string) error { return nil },
		},
	})
	if controllerErr != nil {
		t.Fatal(controllerErr)
	}
	t.Cleanup(func() { _ = controller.Close() })
	if _, err := controller.LoadConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.StartConnection(context.Background(), configured.ID); err != nil {
		t.Fatal(err)
	}
	catalog, catalogErr := controller.Catalog(
		context.Background(),
		resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: configured.ID}},
	)
	if catalogErr != nil {
		t.Fatal(catalogErr)
	}
	body := resourcev2.Value{
		Kind: "map",
		Map:  map[string]resourcev2.Value{"max": {Kind: "uint64", Uint: "18446744073709551615"}},
	}
	reference := resourcev2.ResourceReference{
		Address: resourcev2.ResourceAddress{
			Provider:  "test",
			Authority: "authority",
			Type:      "pod",
			Scope:     []resourcev2.ScopeEntry{},
			ID:        "opaque/id",
		},
	}
	factoryCalls, handlerCalls := 0, 0
	changeCatalog := false
	handler := defaultHandler()
	handler.TTYHandler = func(pc *types.PluginContext, opts exec.SessionOptions, _ *os.File, _ chan error, _ <-chan exec.SessionResizeInput) error {
		handlerCalls++
		assertNativeExecAdmission(t, pc, opts, configured, body, reference, catalog.Revision)
		if changeCatalog {
			revision = "catalog-2"
		}
		return nil
	}
	h := exectest.Mount(
		t,
		exectest.WithHandler(handler),
		exectest.WithTerminalFactory(
			func() (exec.Terminal, error) { factoryCalls++; return exectest.NewFakeTerminal() },
		),
	)
	client := nativeExecClient(t, h.Manager, controller)
	opts := exec.SessionOptions{
		ResourcePlugin: "test",
		ResourceKey:    "pod",
		ResourceData:   &body,
		Reference:      &reference,
		Route:          &resourcev2.AccessRoute{Connection: configured.ID},
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.CreateResourceSession(
		&types.PluginContext{
			Context:    canceled,
			RequestID:  "exec-canceled",
			Connection: &types.Connection{ID: configured.ID},
		},
		opts,
	); err == nil || factoryCalls != 0 ||
		handlerCalls != 0 {
		t.Fatalf("canceled caller reached native: err=%v factory=%d handler=%d", err, factoryCalls, handlerCalls)
	}
	for _, pin := range []string{"", catalog.Revision + "-stale"} {
		ctx := context.Background()
		if pin != "" {
			ctx = resourcev2.WithCatalogRevision(ctx, pin)
		}
		_, err := client.CreateResourceSession(
			&types.PluginContext{
				Context:    ctx,
				RequestID:  "exec-pin-admission",
				Connection: &types.Connection{ID: configured.ID},
			},
			opts,
		)
		if resourcev2.ErrorCode(err) != "RESET_REQUIRED" || factoryCalls != 0 || handlerCalls != 0 {
			t.Fatalf("pin=%q err=%v factory=%d handler=%d", pin, err, factoryCalls, handlerCalls)
		}
	}
	pc := &types.PluginContext{
		Context:    resourcev2.WithCatalogRevision(context.Background(), catalog.Revision),
		RequestID:  "exec-accepted-pin",
		Connection: &types.Connection{ID: configured.ID},
	}
	createAndCloseNativeExecSession(t, client, h.Manager, pc, opts)
	if pc.Connection.Name != "" {
		t.Fatal("caller profile mutated")
	}
	// Opaque utility selectors and bodies need no catalog observation.
	opts.Reference, opts.Route = nil, nil
	createAndCloseNativeExecSession(
		t,
		client,
		h.Manager,
		&types.PluginContext{
			Context:    context.Background(),
			RequestID:  "exec-opaque",
			Connection: &types.Connection{ID: configured.ID},
		},
		opts,
	)
	// A catalog change after native creation must close the newly created session.
	opts.Reference, opts.Route = &reference, &resourcev2.AccessRoute{Connection: configured.ID}
	changeCatalog = true
	if _, createErr := client.CreateResourceSession(pc, opts); resourcev2.ErrorCode(createErr) != "RESET_REQUIRED" {
		t.Fatalf("postfence error=%v", createErr)
	}
	sessions, err := h.Manager.ListSessions(nil)
	if err != nil || len(sessions) != 0 {
		t.Fatalf("orphan sessions=%v err=%v", sessions, err)
	}
}

func assertNativeExecAdmission(
	t *testing.T,
	pc *types.PluginContext,
	opts exec.SessionOptions,
	configured types.Connection,
	body resourcev2.Value,
	reference resourcev2.ResourceReference,
	revision string,
) {
	t.Helper()
	if !reflect.DeepEqual(pc.Connection, &configured) {
		t.Fatalf("metadata replaced configured profile: %#v", pc.Connection)
	}
	if !reflect.DeepEqual(opts.ResourceData, &body) {
		t.Fatalf("body changed: %#v", opts.ResourceData)
	}
	if opts.Reference == nil {
		return
	}
	if !reflect.DeepEqual(opts.Reference, &reference) {
		t.Fatalf("reference changed: %#v", opts.Reference)
	}
	if pin, ok := resourcev2.CatalogRevisionFromContext(pc.Context); !ok || pin != revision {
		t.Fatal("native context lost accepted pin")
	}
}

func nativeExecClient(
	t *testing.T,
	manager *exec.Manager,
	controller resourcev2.ConnectionContextProvider,
) *exec.PluginClient {
	t.Helper()
	plugin := &exec.Plugin{Impl: manager}
	plugin.BindConnectionContextProvider(controller)
	serverOptions := append(
		resourceplugin.ServerOptions(),
		grpc.ChainUnaryInterceptor(interceptors.UnaryPluginContext()),
	)
	server := grpc.NewServer(serverOptions...)
	if registerErr := plugin.GRPCServer(nil, server); registerErr != nil {
		t.Fatal(registerErr)
	}
	listener := bufconn.Listen(1 << 20)
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
		if serveErr := <-serveDone; serveErr != nil {
			t.Errorf("serve: %v", serveErr)
		}
	})
	connection, connectionErr := grpc.NewClient(
		"passthrough:///exec-native-pins",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithChainUnaryInterceptor(sdk.ClientPluginContextInterceptor(nil)),
	)
	if connectionErr != nil {
		t.Fatal(connectionErr)
	}
	t.Cleanup(func() { _ = connection.Close() })
	clientValue, clientErr := plugin.GRPCClient(context.Background(), nil, connection)
	if clientErr != nil {
		t.Fatal(clientErr)
	}
	client, ok := clientValue.(*exec.PluginClient)
	if !ok {
		t.Fatalf("unexpected exec client type: %T", clientValue)
	}
	return client
}

func createAndCloseNativeExecSession(
	t *testing.T,
	client *exec.PluginClient,
	manager *exec.Manager,
	pc *types.PluginContext,
	opts exec.SessionOptions,
) {
	t.Helper()
	created, err := client.CreateResourceSession(pc, opts)
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := manager.CloseSession(nil, created.ID); closeErr != nil {
		t.Fatal(closeErr)
	}
}
