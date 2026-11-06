package metric

import (
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/interceptors"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	metricpb "github.com/omniviewdev/plugin-sdk/proto/v1/metric"
	"google.golang.org/grpc"
)

// This uses the real Plugin.GRPCServer registration, strict SDK codec and client
// negotiation. Direct Manager tests cannot detect a rejected capability envelope.
func TestFiniteRegisteredPluginSupportsActualCatalogAndBatchRPC(t *testing.T) {
	_, registry := metricRegistry(t, []types.Connection{{ID: "one", Data: map[string]any{"endpoint": "configured"}}}, nil, nil)
	manager := NewManager(nil, nil, ProviderInfo{ID: "display-only"}, nil, nil, nil)
	manager.sourcePluginID = "metric"
	manager.BindConnectionContextProvider(registry)
	source := finiteTestQuery().Source
	manager.resourceMetricsCatalogFunc = func(pc *types.PluginContext, r ResourceMetricsCatalogRequest) (*ResourceMetricsCatalog, error) {
		if pc.Connection.ID != "one" || pc.Connection.Data["endpoint"] != "configured" {
			t.Error("configured source lease lost")
		}
		return &ResourceMetricsCatalog{Source: source, MaxTargets: 256, Metrics: []FiniteMetricDescriptor{{ID: "cpu", Name: "CPU", ResourceKey: r.ResourceKey, Unit: "cores", Shapes: []string{"current"}}, {ID: "memory", Name: "Memory", ResourceKey: r.ResourceKey, Unit: "bytes", Shapes: []string{"current"}}}}, nil
	}
	calls := 0
	manager.queryResourcesFunc = func(_ *types.PluginContext, q ResourceMetricsQuery) (*ResourceMetricsResponse, error) {
		calls++
		response := &ResourceMetricsResponse{Complete: true}
		for _, target := range q.Targets {
			for _, id := range q.MetricIDs {
				response.Results = append(response.Results, ResourceMetricResult{ResourceToken: target.Token, MetricID: id, Status: "missing"})
			}
		}
		return response, nil
	}
	options := append(resourceplugin.ServerOptions(), grpc.ChainUnaryInterceptor(interceptors.UnaryPluginContext()), grpc.ChainStreamInterceptor(interceptors.StreamPluginContext()))
	server := grpc.NewServer(options...)
	capability := &Plugin{Impl: manager}
	if err := capability.GRPCServer(nil, server); err != nil {
		t.Fatal(err)
	}
	client := &PluginClient{client: metricpb.NewMetricPluginClient(metricPipe(t, server))}
	ctx := resourcev2.WithCatalogRevision(metricConnectionContext("one"), metricPin(t, registry, "one"))
	pc := types.PluginContextFromContext(ctx)
	pc.Context = ctx
	catalog, err := client.GetResourceMetricsCatalog(pc, ResourceMetricsCatalogRequest{Source: source, ResourceKey: "opaque::type"})
	if err != nil {
		t.Fatalf("registered finite catalog RPC failed: %v", err)
	}
	if catalog.Source != source || len(catalog.Metrics) != 2 {
		t.Fatal("finite catalog changed", catalog)
	}
	q := finiteTestQuery()
	q.MetricIDs = []string{"cpu", "memory"}
	response, err := client.QueryResources(pc, q)
	if err != nil {
		t.Fatalf("registered finite batch RPC failed: %v", err)
	}
	if calls != 1 || !response.Complete || len(response.Results) != 2 {
		t.Fatal("finite RPC did not reach one batch callback", calls, response)
	}
}
