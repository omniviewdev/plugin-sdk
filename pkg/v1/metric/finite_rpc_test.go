package metric

import (
	"context"
	"testing"
	"time"

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
		return &ResourceMetricsCatalog{Source: source, MaxTargets: 256, Metrics: []FiniteMetricDescriptor{{ID: "cpu", Name: "CPU", ResourceKey: r.ResourceKey, Unit: "cores", Shapes: []string{"current", "range"}, RangeReuse: true, RangeToCurrent: true}, {ID: "memory", Name: "Memory", ResourceKey: r.ResourceKey, Unit: "bytes", Shapes: []string{"current"}}}}, nil
	}
	calls := 0
	manager.queryResourcesFunc = func(pc *types.PluginContext, q ResourceMetricsQuery) (*ResourceMetricsResponse, error) {
		if pc.Connection.ID != "one" || pc.Connection.Data["endpoint"] != "configured" {
			t.Error("collection lost configured source lease")
		}
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
	if !catalog.Metrics[0].RangeReuse || catalog.Metrics[1].RangeReuse || !catalog.Metrics[0].RangeToCurrent || catalog.Metrics[1].RangeToCurrent {
		t.Fatal("reuse opt-ins changed across the typed catalog RPC", catalog.Metrics)
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
	q.Targets[0].Route.Connection = "foreign-owner"
	if _, err := client.QueryResources(pc, q); err == nil || calls != 1 {
		t.Fatal("native query bypassed canonical owner admission", err, calls)
	}

	// The source registry deliberately has no foreign owner connection.
	source = MetricSourceRef{PluginID: "metric", ConnectionID: "one", DatasourceUID: "prometheus", DatasourceKind: "prometheus"}
	q.Source = source
	q.Targets[0].Route.Connection = "foreign-owner"
	ctx = metricConnectionContext("one")
	pc = types.PluginContextFromContext(ctx)
	pc.Context = ctx
	response, err = client.QueryResources(pc, q)
	if err != nil || calls != 2 || response == nil || len(response.Results) != 2 {
		t.Fatal("external owner was resolved through source registry", err, calls, response)
	}
	q.Source.ConnectionID = "foreign-owner"
	if _, err := client.QueryResources(pc, q); err == nil || calls != 2 {
		t.Fatal("external query bypassed selected source admission", err, calls)
	}
	q.Source = source

	started := make(chan context.Context, 1)
	manager.queryResourcesFunc = func(pc *types.PluginContext, _ ResourceMetricsQuery) (*ResourceMetricsResponse, error) {
		started <- pc.Context
		<-pc.Context.Done()
		// Even a collector returning success after retirement cannot publish.
		return &ResourceMetricsResponse{Complete: false}, nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := client.QueryResources(pc, q)
		done <- err
	}()
	var leased context.Context
	select {
	case leased = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("external collector did not start")
	}
	if _, err := registry.UpdateConnection(context.Background(), types.Connection{ID: "one", Data: map[string]any{"endpoint": "replacement"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-leased.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("source retirement did not cancel external collection")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("retired external source published success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retired external query did not return")
	}
}
