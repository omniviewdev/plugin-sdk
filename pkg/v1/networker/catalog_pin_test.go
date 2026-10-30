package networker

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/omniviewdev/plugin-sdk/pkg/interceptors"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	networkerpb "github.com/omniviewdev/plugin-sdk/proto/v1/networker"
)

func networkCatalogFixture(
	t *testing.T,
	manager *Manager,
	changed ...*atomic.Bool,
) (*resourcev2.Controller[string], *types.PluginContext) {
	t.Helper()
	controller, err := resourcev2.NewController(resourcev2.Config[string]{
		Catalog: func(context.Context, resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
			revision := "native-1"
			if len(changed) != 0 && changed[0].Load() {
				revision = "native-2"
			}
			return resourcev2.Catalog{
				Provider: "opaque-provider",
				Revision: revision,
				Coverage: resourcev2.Coverage{Kind: "complete", Paths: []string{}},
				Types:    []resourcev2.ResourceType{},
			}, nil
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
	catalog, err := controller.Catalog(
		context.Background(),
		resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: "fixture"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return controller, &types.PluginContext{
		RequestID:  "network-pin-fixture",
		Context:    resourcev2.WithCatalogRevision(context.Background(), catalog.Revision),
		Connection: &types.Connection{ID: "fixture"},
	}
}

func TestNetworkCatalogPinAdmission(t *testing.T) {
	var calls atomic.Int32
	manager := NewManager(
		ManagerConfig{PortChecker: canonicalPorts{}},
		PluginOpts{
			ResourceForwarders: map[string]ResourceForwarder{
				"opaque/type": ResourceForwarderFunc(
					func(_ context.Context, pc *types.PluginContext, _ ResourcePortForwardHandlerOpts) (*ForwarderResult, error) {
						if pc.Connection.Data["source"] != "configured" {
							t.Error("metadata profile reached native forwarder")
						}
						calls.Add(1)
						ready := make(chan struct{})
						close(ready)
						return &ForwarderResult{Ready: ready, ErrCh: make(chan error)}, nil
					},
				),
			},
		},
	)
	controller, accepted := networkCatalogFixture(t, manager)
	t.Cleanup(manager.StopAll)
	server := grpc.NewServer(
		append(resourceplugin.ServerOptions(), grpc.UnaryInterceptor(interceptors.UnaryPluginContext()))...)
	RegisterNetworkerPluginServer(server, &PluginServer{Impl: manager})
	client := &PluginClient{client: networkerpb.NewNetworkerPluginClient(networkPipe(t, server, true))}
	opts := canonicalResourceOptions(nil)
	connection, _ := resourceConnection(opts.Connection)
	connection.Reference = &resourcev2.ResourceReference{
		Address: resourcev2.ResourceAddress{
			Provider:  "opaque-provider",
			Authority: "fixture",
			Type:      "opaque/type",
			ID:        "id",
			Scope:     []resourcev2.ScopeEntry{},
		},
	}
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
	if _, err := controller.UpdateConnection(
		context.Background(),
		types.Connection{ID: "fixture", Data: map[string]any{"source": "replacement"}},
	); err != nil {
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
	if _, err := controller.UpdateConnection(
		context.Background(),
		types.Connection{ID: "fixture", Data: map[string]any{"source": "configured"}},
	); err != nil {
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
	manager := NewManager(
		ManagerConfig{PortChecker: canonicalPorts{}},
		PluginOpts{
			ResourceForwarders: map[string]ResourceForwarder{
				"opaque/type": ResourceForwarderFunc(
					func(ctx context.Context, _ *types.PluginContext, _ ResourcePortForwardHandlerOpts) (*ForwarderResult, error) {
						nativeContext = ctx
						changed.Store(true)
						ready := make(chan struct{})
						close(ready)
						return &ForwarderResult{Ready: ready, ErrCh: make(chan error)}, nil
					},
				),
			},
		},
	)
	_, accepted := networkCatalogFixture(t, manager, &changed)
	t.Cleanup(manager.StopAll)
	opts := canonicalResourceOptions(nil)
	connection, _ := resourceConnection(opts.Connection)
	connection.Reference = &resourcev2.ResourceReference{
		Address: resourcev2.ResourceAddress{
			Provider:  "opaque-provider",
			Authority: "fixture",
			Type:      "opaque/type",
			ID:        "id",
			Scope:     []resourcev2.ScopeEntry{},
		},
	}
	connection.Route = &resourcev2.AccessRoute{Connection: "fixture"}
	opts.Connection = connection
	if _, err := manager.StartResourcePortForwardSession(
		accepted,
		opts,
	); resourcev2.ErrorCode(
		err,
	) != "RESET_REQUIRED" {
		t.Fatalf("post fence: %v", err)
	}
	if nativeContext == nil || nativeContext.Err() == nil {
		t.Fatal("created forward context was not canceled")
	}
}

func TestForwardAdmissionCancellation(t *testing.T) {
	for _, phase := range []string{"creation", "readiness", "ready-race"} {
		for _, retire := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/retire=%t", phase, retire), func(t *testing.T) {
				entered := make(chan struct{})
				manager := NewManager(
					ManagerConfig{PortChecker: canonicalPorts{}},
					PluginOpts{ResourceForwarders: map[string]ResourceForwarder{
						"opaque/type": ResourceForwarderFunc(
							func(ctx context.Context, _ *types.PluginContext, _ ResourcePortForwardHandlerOpts) (*ForwarderResult, error) {
								close(entered)
								if phase == "creation" {
									<-ctx.Done()
									return nil, ctx.Err()
								}
								ready := make(chan struct{})
								if phase == "ready-race" {
									<-ctx.Done()
									close(ready)
								}
								return &ForwarderResult{Ready: ready}, nil
							},
						),
					}},
				)
				controller, pc := networkCatalogFixture(t, manager)
				t.Cleanup(manager.StopAll)
				request, cancel := context.WithCancel(pc.Context)
				defer cancel()
				pc.Context = request
				result := make(chan error, 1)
				go func() {
					session, err := manager.StartResourcePortForwardSession(pc, canonicalResourceOptions(nil))
					if session != nil {
						t.Error("canceled admission returned a session")
					}
					result <- err
				}()
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("native creation did not start")
				}
				stopped := make(chan error, 1)
				if retire {
					go func() {
						_, err := controller.StopConnection(context.Background(), "fixture")
						stopped <- err
					}()
				} else {
					cancel()
				}
				select {
				case err := <-result:
					if err == nil {
						t.Fatal("canceled admission succeeded")
					}
				case <-time.After(time.Second):
					t.Fatal("cancellation did not release admission")
				}
				if retire {
					select {
					case err := <-stopped:
						if err != nil {
							t.Fatal(err)
						}
					case <-time.After(time.Second):
						t.Fatal("retirement retained a lease")
					}
				}
				sessions, err := manager.ListPortForwardSessions(pc)
				if err != nil || len(sessions) != 0 {
					t.Fatalf("failed admission retained sessions: %v, %v", sessions, err)
				}
			})
		}
	}
}

func TestAcceptedForwardLifetime(t *testing.T) {
	for _, retire := range []bool{false, true} {
		t.Run(fmt.Sprintf("retire=%t", retire), func(t *testing.T) {
			var native context.Context
			manager := NewManager(
				ManagerConfig{PortChecker: canonicalPorts{}},
				PluginOpts{ResourceForwarders: map[string]ResourceForwarder{
					"opaque/type": ResourceForwarderFunc(
						func(ctx context.Context, _ *types.PluginContext, _ ResourcePortForwardHandlerOpts) (*ForwarderResult, error) {
							native = ctx
							ready := make(chan struct{})
							close(ready)
							return &ForwarderResult{Ready: ready}, nil
						},
					),
				}},
			)
			controller, pc := networkCatalogFixture(t, manager)
			t.Cleanup(manager.StopAll)
			request, cancel := context.WithCancel(pc.Context)
			defer cancel()
			pc.Context = request
			session, err := manager.StartResourcePortForwardSession(pc, canonicalResourceOptions(nil))
			if err != nil {
				t.Fatal(err)
			}
			cancel()
			if native.Err() != nil {
				t.Fatal("RPC completion canceled accepted forward")
			}
			if !retire {
				if _, err := manager.ClosePortForwardSession(pc, session.ID); err != nil {
					t.Fatal(err)
				}
			}
			stopped := make(chan error, 1)
			go func() {
				_, err := controller.StopConnection(context.Background(), "fixture")
				stopped <- err
			}()
			select {
			case err := <-stopped:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("closed or retired session retained its generation lease")
			}
			if native.Err() == nil {
				t.Fatal("close or retirement did not cancel native forward")
			}
		})
	}
}

func TestForwardFailureReleasesGenerationLease(t *testing.T) {
	for _, creation := range []bool{false, true} {
		t.Run(fmt.Sprintf("creation=%t", creation), func(t *testing.T) {
			failed := make(chan error, 1)
			manager := NewManager(
				ManagerConfig{PortChecker: canonicalPorts{}},
				PluginOpts{ResourceForwarders: map[string]ResourceForwarder{
					"opaque/type": ResourceForwarderFunc(
						func(context.Context, *types.PluginContext, ResourcePortForwardHandlerOpts) (*ForwarderResult, error) {
							if creation {
								return nil, fmt.Errorf("native creation failed")
							}
							ready := make(chan struct{})
							close(ready)
							return &ForwarderResult{Ready: ready, ErrCh: failed}, nil
						},
					),
				}},
			)
			controller, pc := networkCatalogFixture(t, manager)
			t.Cleanup(manager.StopAll)
			_, err := manager.StartResourcePortForwardSession(pc, canonicalResourceOptions(nil))
			if creation && err == nil {
				t.Fatal("native creation failure succeeded")
			}
			if !creation {
				if err != nil {
					t.Fatal(err)
				}
				failed <- fmt.Errorf("native tunnel failed")
				done := make(chan struct{})
				go func() { manager.wg.Wait(); close(done) }()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("native failure did not finish monitor")
				}
			}
			stopped := make(chan error, 1)
			go func() {
				_, err := controller.StopConnection(context.Background(), "fixture")
				stopped <- err
			}()
			select {
			case err := <-stopped:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("failed forward retained generation lease")
			}
		})
	}
}

func TestForwardCreationOwnsExactEntryAcrossIDReuse(t *testing.T) {
	manager := NewManager(
		ManagerConfig{PortChecker: canonicalPorts{}},
		PluginOpts{ResourceForwarders: map[string]ResourceForwarder{
			"opaque/type": ResourceForwarderFunc(
				func(context.Context, *types.PluginContext, ResourcePortForwardHandlerOpts) (*ForwarderResult, error) {
					ready := make(chan struct{})
					close(ready)
					return &ForwarderResult{SessionID: "reused-native-id", Ready: ready}, nil
				},
			),
		}},
	)
	t.Cleanup(manager.StopAll)
	pc := &types.PluginContext{Context: context.Background()}
	first, err := manager.createPortForwardSession(pc, canonicalResourceOptions(nil), true)
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce the interleaving between insertion and the retained lease's
	// lifetime wait: another caller closes and replaces the published ID.
	if _, err := manager.ClosePortForwardSession(pc, first.session.ID); err != nil {
		t.Fatal(err)
	}
	replacement, err := manager.createPortForwardSession(pc, canonicalResourceOptions(nil), true)
	if err != nil {
		t.Fatal(err)
	}
	if first == replacement || first.ctx.Err() == nil || replacement.ctx.Err() != nil {
		t.Fatal("creation did not retain the exact original session lifetime")
	}
	select {
	case <-first.ctx.Done():
		// The original lease can finish even while the replacement is active.
	default:
		t.Fatal("replacement hid the original session's closure")
	}
}
