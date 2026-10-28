package networker

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/interceptors"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	networkerpb "github.com/omniviewdev/plugin-sdk/proto/v1/networker"
	"google.golang.org/grpc"
)

func networkCatalogFixture(t *testing.T, manager *Manager, changed ...*atomic.Bool) (*resourcev2.Controller[string], *types.PluginContext) {
	t.Helper()
	controller, err := resourcev2.NewController(resourcev2.Config[string]{
		Catalog: func(context.Context, resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
			revision := "native-1"
			if len(changed) != 0 && changed[0].Load() {
				revision = "native-2"
			}
			return resourcev2.Catalog{Provider: "opaque-provider", Revision: revision, Coverage: resourcev2.Coverage{Kind: "complete", Paths: []string{}}, Types: []resourcev2.ResourceType{}}, nil
		},
		Lifecycle: &resourcev2.Lifecycle[string]{
			LoadConnections: func(context.Context) ([]types.Connection, error) {
				return []types.Connection{{ID: "fixture", Data: map[string]any{"source": "configured"}}}, nil
			},
			CreateClient:  func(context.Context, types.Connection) (*string, error) { value := "native"; return &value, nil },
			DestroyClient: func(context.Context, *string) error { return nil },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := controller.LoadConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.StartConnection(context.Background(), "fixture"); err != nil {
		t.Fatal(err)
	}
	manager.BindConnectionContextProvider(controller)
	catalog, err := controller.Catalog(context.Background(), resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: "fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	return controller, &types.PluginContext{RequestID: "network-pin-fixture", Context: resourcev2.WithCatalogRevision(context.Background(), catalog.Revision), Connection: &types.Connection{ID: "fixture"}}
}

func TestNetworkCatalogPinAdmission(t *testing.T) {
	var calls atomic.Int32
	manager := NewManager(ManagerConfig{PortChecker: canonicalPorts{}}, PluginOpts{ResourceForwarders: map[string]ResourceForwarder{"opaque/type": ResourceForwarderFunc(func(_ context.Context, pc *types.PluginContext, _ ResourcePortForwardHandlerOpts) (*ForwarderResult, error) {
		if pc.Connection.Data["source"] != "configured" {
			t.Error("metadata profile reached native forwarder")
		}
		calls.Add(1)
		ready := make(chan struct{})
		close(ready)
		return &ForwarderResult{Ready: ready, ErrCh: make(chan error)}, nil
	})}})
	controller, accepted := networkCatalogFixture(t, manager)
	t.Cleanup(manager.StopAll)
	server := grpc.NewServer(append(resourceplugin.ServerOptions(), grpc.UnaryInterceptor(interceptors.UnaryPluginContext()))...)
	RegisterNetworkerPluginServer(server, &PluginServer{Impl: manager})
	client := &PluginClient{client: networkerpb.NewNetworkerPluginClient(networkPipe(t, server, true))}
	opts := canonicalResourceOptions(nil)
	connection, _ := resourceConnection(opts.Connection)
	connection.Reference = &resourcev2.ResourceReference{Address: resourcev2.ResourceAddress{Provider: "opaque-provider", Authority: "fixture", Type: "opaque/type", ID: "id", Scope: []resourcev2.ScopeEntry{}}}
	connection.Route = &resourcev2.AccessRoute{Connection: "fixture"}
	opts.Connection = connection
	missing := *accepted
	missing.Context = context.Background()
	if _, err := client.StartResourcePortForwardSession(&missing, opts); resourcev2.ErrorCode(err) != "RESET_REQUIRED" {
		t.Fatalf("missing pin: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("missing pin reached native")
	}
	if _, err := controller.UpdateConnection(context.Background(), types.Connection{ID: "fixture", Data: map[string]any{"source": "replacement"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.StartConnection(context.Background(), "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.StartResourcePortForwardSession(accepted, opts); resourcev2.ErrorCode(err) != "RESET_REQUIRED" {
		t.Fatalf("stale pin: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("stale pin reached native")
	}
	// Restore a real configured profile, then prove opaque input requires no pin.
	if _, err := controller.UpdateConnection(context.Background(), types.Connection{ID: "fixture", Data: map[string]any{"source": "configured"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.StartConnection(context.Background(), "fixture"); err != nil {
		t.Fatal(err)
	}
	connection.Reference = nil
	opts.Connection = connection
	session, err := client.StartResourcePortForwardSession(&missing, opts)
	if err != nil || calls.Load() != 1 {
		t.Fatalf("opaque no-pin input: %v", err)
	}
	if _, err := manager.ClosePortForwardSession(&missing, session.ID); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkPostFenceClosesCreatedForward(t *testing.T) {
	var changed atomic.Bool
	var nativeContext context.Context
	manager := NewManager(ManagerConfig{PortChecker: canonicalPorts{}}, PluginOpts{ResourceForwarders: map[string]ResourceForwarder{"opaque/type": ResourceForwarderFunc(func(ctx context.Context, _ *types.PluginContext, _ ResourcePortForwardHandlerOpts) (*ForwarderResult, error) {
		nativeContext = ctx
		changed.Store(true)
		ready := make(chan struct{})
		close(ready)
		return &ForwarderResult{Ready: ready, ErrCh: make(chan error)}, nil
	})}})
	_, accepted := networkCatalogFixture(t, manager, &changed)
	t.Cleanup(manager.StopAll)
	opts := canonicalResourceOptions(nil)
	connection, _ := resourceConnection(opts.Connection)
	connection.Reference = &resourcev2.ResourceReference{Address: resourcev2.ResourceAddress{Provider: "opaque-provider", Authority: "fixture", Type: "opaque/type", ID: "id", Scope: []resourcev2.ScopeEntry{}}}
	connection.Route = &resourcev2.AccessRoute{Connection: "fixture"}
	opts.Connection = connection
	if _, err := manager.StartResourcePortForwardSession(accepted, opts); resourcev2.ErrorCode(err) != "RESET_REQUIRED" {
		t.Fatalf("post fence: %v", err)
	}
	if nativeContext == nil || nativeContext.Err() == nil {
		t.Fatal("created forward context was not canceled")
	}
}
