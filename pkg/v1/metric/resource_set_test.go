package metric

import (
	"context"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	metricpb "github.com/omniviewdev/plugin-sdk/proto/v1/metric"
	"testing"
)

func resourceSetFixture() ResourceSetQueryRequest {
	return ResourceSetQueryRequest{ResourcePluginID: "kubernetes", ResourceConnectionID: "cluster-a", Source: MetricSourceReference{PluginID: "grafana", ConnectionID: "shared", DatasourceUID: "prom"}, Resources: []MetricResource{{Token: "pod-a", ResourceKey: "Pod", ResourceID: "pod", ResourceNamespace: "ns", Route: &resourcev2.AccessRoute{Connection: "cluster-a"}}}, MetricIDs: []string{"cpu"}}
}
func TestResourceSetRejectsAmbiguousIdentity(t *testing.T) {
	request := resourceSetFixture()
	request.Resources = append(request.Resources, request.Resources[0])
	if ValidateResourceSetQuery(request) == nil {
		t.Fatal("duplicate tokens accepted")
	}
	request = resourceSetFixture()
	request.Resources[0].ResourceID = ""
	if ValidateResourceSetQuery(request) == nil {
		t.Fatal("empty-ID batch accepted")
	}
	request = resourceSetFixture()
	request.Resources[0].Route.Connection = "shared"
	if ValidateResourceSetQuery(request) == nil {
		t.Fatal("provider connection admitted as source route")
	}
}
func TestResourceSetDoesNotTurnMissingIntoZero(t *testing.T) {
	request := resourceSetFixture()
	response := &ResourceSetQueryResponse{Results: []ResourceMetricResult{{ResourceToken: "pod-a", MetricID: "cpu", Status: MetricStatusMissing}}}
	if err := ValidateResourceSetResponse(request, response); err != nil {
		t.Fatal(err)
	}
	response.Results[0].Value = &MetricResult{CurrentValue: &CurrentValue{Value: 0}}
	if ValidateResourceSetResponse(request, response) == nil {
		t.Fatal("missing accepted with numeric zero")
	}
	response.Results[0].Status = MetricStatusOK
	if err := ValidateResourceSetResponse(request, response); err != nil {
		t.Fatal(err)
	}
	response.Results = append(response.Results, response.Results[0])
	if ValidateResourceSetResponse(request, response) == nil {
		t.Fatal("duplicate result winner accepted")
	}
}
func TestResourceSetStrictJSON(t *testing.T) {
	for _, encoded := range []string{`{"resources":[],"unknown":true}`, `{"metric_ids":[],"metric_ids":["cpu"]}`, `{} {}`, `[]`} {
		var request ResourceSetQueryRequest
		if DecodeMetricContract([]byte(encoded), &request) == nil {
			t.Fatalf("unsafe JSON accepted: %s", encoded)
		}
	}
	if _, err := (&resourceSetServer{}).QueryResources(context.Background(), &metricpb.MetricContractRequest{Version: 99, Json: []byte(`{}`)}); err == nil {
		t.Fatal("unknown protocol accepted")
	}
}

type resourceSetRecorder struct {
	calls      int
	connection string
}

func (r *resourceSetRecorder) QueryResources(q *QueryContext, req ResourceSetQueryRequest) (*ResourceSetQueryResponse, error) {
	r.calls++
	r.connection = q.ConnectionID
	return &ResourceSetQueryResponse{Results: []ResourceMetricResult{{ResourceToken: req.Resources[0].Token, MetricID: req.MetricIDs[0], Status: MetricStatusMissing}}}, nil
}

type resourceSetLease struct {
	calls int
	route string
}

func (r *resourceSetLease) WithConnection(ctx context.Context, route resourcev2.AccessRoute, fn func(context.Context, types.Connection) error) error {
	r.calls++
	r.route = route.Connection
	return fn(ctx, types.Connection{ID: route.Connection})
}
func TestResourceSetLeasesProviderNotSource(t *testing.T) {
	adapter := &resourceSetRecorder{}
	lease := &resourceSetLease{}
	manager := NewManager(nil, nil, ProviderInfo{ID: "grafana"}, nil, nil, nil)
	manager.resourceSetProvider = adapter
	manager.BindConnectionContextProvider(lease)
	request := resourceSetFixture()
	_, err := manager.QueryResources(&QueryContext{Context: context.Background(), Connection: &types.Connection{ID: "shared"}}, request)
	if err != nil {
		t.Fatal(err)
	}
	if lease.calls != 1 || lease.route != "shared" || adapter.connection != "shared" {
		t.Fatal("provider execution did not use provider lease")
	}
	request.Resources[0].Route.Connection = "other-cluster"
	if _, err = manager.QueryResources(&QueryContext{Context: context.Background(), Connection: &types.Connection{ID: "shared"}}, request); err == nil {
		t.Fatal("invalid source route dispatched")
	}
	if adapter.calls != 1 {
		t.Fatal("invalid source reached provider")
	}
}
