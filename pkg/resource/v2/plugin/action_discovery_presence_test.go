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
			calls := 0
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
				calls++
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
				if err := controller.Close(); err != nil {
					t.Error(err)
				}
			})
			server := grpc.NewServer()
			RegisterServer(server, controller)
			client := NewClient(pipe(t, server))
			route := testGet().Route
			ctx := acceptedCatalogContext(context.Background(), t, client, route)
			descriptors, err := client.GetActions(ctx, route, "opaque/type")
			if err != nil {
				t.Fatal(err)
			}
			if len(descriptors) != 0 || (descriptors == nil) != (mode == "unknown") {
				t.Fatalf("discovery presence lost: %#v", descriptors)
			}
			request := resourcev2.ActionRequest{
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
			descriptor, err := resourcev2.ResolveActionDescriptor(ctx, client, request, false)
			if mode == "unknown" {
				if err != nil || descriptor != nil {
					t.Fatalf("unknown discovery became authoritative: %#v %v", descriptor, err)
				}
			} else if resourcev2.ErrorCode(err) != "UNSUPPORTED" {
				t.Fatalf("empty discovery admitted action: %#v %v", descriptor, err)
			}
			_, err = client.Action(ctx, request)
			if mode == "empty" {
				if resourcev2.ErrorCode(err) != "UNSUPPORTED" || calls != 0 {
					t.Fatalf("explicit empty discovery dispatched: calls=%d err=%v", calls, err)
				}
			} else if err != nil || calls != 1 {
				t.Fatalf("native scoped action rejected: calls=%d err=%v", calls, err)
			}
			request.ExpectedDescriptor = &resourcev2.ActionDescriptor{ID: "run", Scope: resourcev2.ActionScopeType}
			_, err = client.Action(ctx, request)
			if resourcev2.ErrorCode(err) != "RESET_REQUIRED" {
				t.Fatalf("missing advertised pin did not reset: %v", err)
			}
		})
	}
}
