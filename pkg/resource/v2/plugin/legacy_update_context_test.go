package plugin

import (
	"context"
	"testing"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	"google.golang.org/grpc"
)

type legacyUpdateProvider struct {
	qualifiedProvider
	features resourcev2.FeatureSet
}

func (p *legacyUpdateProvider) Features(context.Context) (resourcev2.FeatureSet, error) {
	return p.features, nil
}

func (p *legacyUpdateProvider) Catalog(ctx context.Context, request resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
	catalog, err := p.qualifiedProvider.Catalog(ctx, request)
	catalog.Types[0].Capabilities = append(catalog.Types[0].Capabilities, resourcev2.Capability{
		Operation: "update", Scope: resourcev2.QueryScope{}, Guarantee: "bestEffort",
	})
	return catalog, err
}

type legacyUpdateWireServer struct {
	resourcepb.UnimplementedResourceProviderServer
	provider *legacyUpdateProvider
}

func (s *legacyUpdateWireServer) Call(ctx context.Context, request *resourcepb.Request) (*resourcepb.Response, error) {
	if request.GetFeatures() != nil {
		traits := make([]string, len(s.provider.features.Traits))
		for i, trait := range s.provider.features.Traits {
			traits[i] = string(trait)
		}
		return &resourcepb.Response{Version: resourcev2.WireVersion, Result: &resourcepb.Response_Features{Features: &resourcepb.FeatureSet{Traits: traits}}}, nil
	}
	catalog, err := s.provider.Catalog(ctx, resourcev2.CatalogRequest{})
	if err != nil {
		return nil, err
	}
	wire, err := resourcev2.ToProtoCatalog(catalog)
	return &resourcepb.Response{Version: resourcev2.WireVersion, Result: &resourcepb.Response_Catalog{Catalog: wire}}, err
}

func TestLegacyUpdateProviderContext(t *testing.T) {
	for _, tc := range []struct {
		name   string
		traits []resourcev2.Feature
		code   string
	}{
		{name: "old", traits: []resourcev2.Feature{"bestEffortUpdates"}},
		{name: "modern", traits: []resourcev2.Feature{resourcev2.FeatureSourceSessions}, code: "INVALID_REQUEST"},
		{name: "conflicting", traits: []resourcev2.Feature{resourcev2.FeatureSourceSessions, "bestEffortUpdates"}, code: "INVALID_REQUEST"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &legacyUpdateProvider{qualifiedProvider: qualifiedProvider{allowed: true}, features: resourcev2.FeatureSet{Traits: tc.traits}}
			ctx := resourcev2.WithCatalogRevisionForRoute(context.Background(), "test", testGet().Route, "cat-1")
			_, err := resourcev2.AdmitOperation(ctx, p, testGet())
			if resourcev2.ErrorCode(err) != tc.code {
				t.Fatalf("local admission: %v, want %s", err, tc.code)
			}
			for _, raw := range []bool{false, true} {
				server := grpc.NewServer()
				if raw {
					resourcepb.RegisterResourceProviderServer(server, &legacyUpdateWireServer{provider: p})
				} else {
					RegisterServer(server, p)
				}
				client := NewClient(pipe(t, server))
				catalog, err := client.Catalog(context.Background(), resourcev2.CatalogRequest{Route: testGet().Route})
				if resourcev2.ErrorCode(err) != tc.code {
					t.Fatalf("raw=%v catalog: %v, want %s", raw, err, tc.code)
				}
				if tc.code == "" {
					if _, err := resourcev2.AdmitCatalogOperation(catalog, testGet()); err != nil {
						t.Fatalf("finite operation rejected: %v", err)
					}
					target := resourcev2.ReadTarget{Route: testGet().Route, Provider: "test", Authority: "authority", Type: "opaque/type", Representation: "document", Scope: resourcev2.QueryScope{}}
					if _, err := resourcev2.AdmitCatalogOperation(catalog, target); resourcev2.ErrorCode(err) != "UNSUPPORTED" {
						t.Fatalf("legacy update granted source: %v", err)
					}
				}
			}
		})
	}
}
