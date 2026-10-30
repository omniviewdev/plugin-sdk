package plugin

import (
	"context"
	"testing"

	"google.golang.org/grpc"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
)

func TestControllerRPCPreservesActionDiscoveryPresence(t *testing.T) {
	for _, mode := range []string{"unknown", "empty", "conflicting"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newActionDiscoveryFixture(t, mode)
			fixture.assertTransitions(t, mode)
		})
	}
}

type actionDiscoveryFixture struct {
	client  *Client
	ctx     context.Context
	request resourcev2.ActionRequest
	calls   int
}

// newActionDiscoveryFixture retains the native scoped dispatcher behind its RPC
// catalog, including unknown, explicit empty, and cross-scope conflicting metadata.
func newActionDiscoveryFixture(t *testing.T, mode string) *actionDiscoveryFixture {
	t.Helper()
	fixture := &actionDiscoveryFixture{}
	base := &nativeOnlyActionProvider{}
	cfg := resourcev2.Config[string]{
		Catalog: func(ctx context.Context, request resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
			catalog, err := base.Catalog(ctx, request)
			catalog.Types[0].QueryDimensions = []resourcev2.Dimension{{Name: "region"}}
			catalog.Types[0].Capabilities[0].Scope = resourcev2.QueryScope{
				{Dimension: "region", Match: resourcev2.ScopeAny},
			}
			return catalog, err
		},
	}
	action := func(context.Context, *string, resourcev2.ActionRequest) (resourcev2.Value, error) {
		fixture.calls++
		return resourcev2.Value{Kind: "null"}, nil
	}
	scope := func(region string) resourcev2.QueryScope {
		values := []string{region}
		return resourcev2.QueryScope{{Dimension: "region", Match: resourcev2.ScopeOneOf, Values: &values}}
	}
	registration := resourcev2.Registration[string]{Type: "opaque/type", Action: action}
	switch mode {
	case "unknown":
		cfg.Registrations = []resourcev2.Registration[string]{registration}
	case "empty":
		registration.Actions = []resourcev2.ActionDescriptor{}
		cfg.Registrations = []resourcev2.Registration[string]{registration}
	case "conflicting":
		for _, region := range []string{"west", "east"} {
			scoped := registration
			scoped.Scope = scope(region)
			scoped.Actions = []resourcev2.ActionDescriptor{
				{ID: "run", Label: region, Scope: resourcev2.ActionScopeType},
			}
			cfg.Registrations = append(cfg.Registrations, scoped)
		}
	}
	controller, err := resourcev2.NewController(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := controller.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	server := grpc.NewServer()
	RegisterServer(server, controller)
	fixture.client = NewClient(pipe(t, server))
	route := testGet().Route
	fixture.ctx = acceptedCatalogContext(context.Background(), t, fixture.client, route)
	fixture.request = resourcev2.ActionRequest{
		Route: route,
		TypeTarget: &resourcev2.TypeActionTarget{
			Provider:  "test",
			Authority: "authority",
			Type:      "opaque/type",
			Scope:     scope("west"),
		},
		Action: "run",
		Input:  resourcev2.Value{Kind: "null"},
	}
	return fixture
}

// assertTransitions checks discovery presence, unpinned native dispatch, and
// reset of an unavailable expected descriptor against the same scoped fixture.
func (f *actionDiscoveryFixture) assertTransitions(t *testing.T, mode string) {
	t.Helper()
	descriptors, err := f.client.GetActions(f.ctx, f.request.Route, "opaque/type")
	if err != nil {
		t.Fatal(err)
	}
	if len(descriptors) != 0 || (descriptors == nil) != (mode == "unknown") {
		t.Fatalf("discovery presence lost: %#v", descriptors)
	}
	descriptor, err := resourcev2.ResolveActionDescriptor(f.ctx, f.client, f.request, false)
	if mode == "unknown" {
		if err != nil || descriptor != nil {
			t.Fatalf("unknown discovery became authoritative: %#v %v", descriptor, err)
		}
	} else if resourcev2.ErrorCode(err) != "UNSUPPORTED" {
		t.Fatalf("empty discovery admitted action: %#v %v", descriptor, err)
	}
	_, err = f.client.Action(f.ctx, f.request)
	if mode == "empty" {
		if resourcev2.ErrorCode(err) != "UNSUPPORTED" || f.calls != 0 {
			t.Fatalf("explicit empty discovery dispatched: calls=%d err=%v", f.calls, err)
		}
	} else if err != nil || f.calls != 1 {
		t.Fatalf("native scoped action rejected: calls=%d err=%v", f.calls, err)
	}
	f.request.ExpectedDescriptor = &resourcev2.ActionDescriptor{ID: "run", Scope: resourcev2.ActionScopeType}
	_, err = f.client.Action(f.ctx, f.request)
	if resourcev2.ErrorCode(err) != "RESET_REQUIRED" {
		t.Fatalf("missing advertised pin did not reset: %v", err)
	}
}
