package metric

import (
	"context"
	"encoding/json"
	metricpb "github.com/omniviewdev/plugin-sdk/proto/v1/metric"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"net"
	"testing"
	"time"
)

type standaloneMetricProvider struct{ queries, discoveries int }

func (p *standaloneMetricProvider) QueryResources(q *QueryContext, req ResourceSetQueryRequest) (*ResourceSetQueryResponse, error) {
	if q.Connection != nil || q.ConnectionID != req.Source.ConnectionID {
		return nil, metricError("INVALID_REQUEST", "Saved provider identity was lost or turned into a resource profile.")
	}
	p.queries++
	return &ResourceSetQueryResponse{Results: []ResourceMetricResult{{ResourceToken: req.Resources[0].Token, MetricID: req.MetricIDs[0], Status: MetricStatusMissing}}}, nil
}
func (p *standaloneMetricProvider) DiscoverMetricLabels(q *QueryContext, req MetricDiscoveryRequest) (*MetricDiscoveryResult, error) {
	if q.Connection != nil || q.ConnectionID != req.Source.ConnectionID {
		return nil, metricError("INVALID_REQUEST", "Saved discovery identity was lost or turned into a resource profile.")
	}
	p.discoveries++
	return &MetricDiscoveryResult{Values: []string{"cpu"}}, nil
}
func TestStandaloneProviderIdentityManagerAndRPC(t *testing.T) {
	provider := &standaloneMetricProvider{}
	manager := NewManager(nil, nil, ProviderInfo{ID: "grafana"}, nil, nil, nil)
	manager.resourceSetProvider = provider
	manager.discovery = provider
	req := resourceSetFixture()
	discovery := MetricDiscoveryRequest{Source: req.Source, Limit: 1, LabelName: "__name__"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	q := &QueryContext{Context: ctx, ConnectionID: "shared"}
	if _, err := manager.QueryResources(q, req); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.DiscoverMetricLabels(q, discovery); err != nil {
		t.Fatal(err)
	}
	mismatch := &QueryContext{Context: ctx, ConnectionID: "other"}
	if _, err := manager.QueryResources(mismatch, req); err == nil {
		t.Fatal("manager accepted mismatched query context")
	}
	if _, err := manager.DiscoverMetricLabels(mismatch, discovery); err == nil {
		t.Fatal("manager accepted mismatched discovery context")
	}
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	if err := (&Plugin{Impl: manager}).GRPCServer(nil, server); err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.DialContext(ctx, "bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := &PluginClient{resourceSetClient: metricpb.NewMetricResourceSetClient(conn)}
	if _, err = client.QueryResources(q, req); err != nil {
		t.Fatal(err)
	}
	if _, err = client.DiscoverMetricLabels(q, discovery); err != nil {
		t.Fatal(err)
	}
	if _, err = client.QueryResources(mismatch, req); err == nil {
		t.Fatal("RPC accepted mismatched query context")
	}
	if _, err = client.DiscoverMetricLabels(mismatch, discovery); err == nil {
		t.Fatal("RPC accepted mismatched discovery context")
	}
	// Exercise server admission directly, independently of client-side validation.
	wire := metricpb.NewMetricResourceSetClient(conn)
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = wire.QueryResources(ctx, &metricpb.MetricContractRequest{Version: MetricContractVersion, Json: payload, ProviderConnectionId: "other"}); err == nil {
		t.Fatal("wire query mismatch dispatched")
	}
	payload, err = json.Marshal(discovery)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = wire.DiscoverMetricLabels(ctx, &metricpb.MetricContractRequest{Version: MetricContractVersion, Json: payload, ProviderConnectionId: "other"}); err == nil {
		t.Fatal("wire discovery mismatch dispatched")
	}
	if provider.queries != 2 || provider.discoveries != 2 {
		t.Fatalf("rejected request reached provider: queries=%d discovery=%d", provider.queries, provider.discoveries)
	}
}
