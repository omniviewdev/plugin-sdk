package metric

import (
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	metricpb "github.com/omniviewdev/plugin-sdk/proto/v1/metric"
	"google.golang.org/grpc"
	"math"
	"testing"
	"time"
)

func finiteTestQuery() ResourceMetricsQuery {
	return ResourceMetricsQuery{Source: MetricSourceRef{PluginID: "metric", ConnectionID: "one", DatasourceKind: "metrics-server"}, Targets: []ResourceMetricTarget{{Token: "pod", Reference: *metricReference(), Route: resourcev2.AccessRoute{Connection: "one"}}}, MetricIDs: []string{"cpu"}, Shape: "current"}
}
func TestFiniteQueryRejectsAmbiguousTokensAndDomains(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*ResourceMetricsQuery)
	}{
		{"duplicate token", func(q *ResourceMetricsQuery) { q.Targets = append(q.Targets, q.Targets[0]) }},
		{"empty token", func(q *ResourceMetricsQuery) { q.Targets[0].Token = "" }},
		{"duplicate metric", func(q *ResourceMetricsQuery) { q.MetricIDs = append(q.MetricIDs, "cpu") }},
		{"current range", func(q *ResourceMetricsQuery) { q.StartTime = time.Now() }},
		{"range sample bound", func(q *ResourceMetricsQuery) {
			q.Shape = "range"
			q.StartTime = time.Unix(1, 0)
			q.EndTime = q.StartTime.Add(time.Hour)
			q.Step = time.Millisecond
		}},
		{"native uid", func(q *ResourceMetricsQuery) { q.Source.DatasourceUID = "caller-selected" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			q := finiteTestQuery()
			test.change(&q)
			if ValidateResourceMetricsQuery(q) == nil {
				t.Fatal("invalid query admitted")
			}
		})
	}
}
func TestFiniteUnknownTimeAndMissingStayDistinct(t *testing.T) {
	q := finiteTestQuery()
	zero := 0.0
	r := &ResourceMetricsResponse{Complete: true, Results: []ResourceMetricResult{{ResourceToken: "pod", MetricID: "cpu", Status: "ok", Value: &zero, IdentityScope: "name"}}}
	if err := ValidateResourceMetricsResponse(q, r); err != nil {
		t.Fatal(err)
	}
	wire := finiteResponseToProto(r)
	got, err := finiteResponseFromProto(q, wire)
	if err != nil {
		t.Fatal(err)
	}
	if got.Results[0].Value == nil || *got.Results[0].Value != 0 || got.Results[0].ObservedAt != nil || got.Results[0].IdentityScope != "name" {
		t.Fatal("zero sample, unknown time or attribution changed")
	}
	r.Results[0].Status = "missing"
	r.Results[0].Value = nil
	got, err = finiteResponseFromProto(q, finiteResponseToProto(r))
	if err != nil {
		t.Fatal(err)
	}
	if got.Results[0].Value != nil {
		t.Fatal("missing became zero")
	}
}
func TestFiniteResponseRejectsUnrequestedAndNonfiniteData(t *testing.T) {
	q := finiteTestQuery()
	value := 1.0
	base := ResourceMetricResult{ResourceToken: "pod", MetricID: "cpu", Status: "ok", Value: &value}
	for _, test := range []struct {
		name   string
		change func(*ResourceMetricsResponse)
	}{
		{"unknown token", func(r *ResourceMetricsResponse) { r.Results[0].ResourceToken = "other" }},
		{"duplicate result", func(r *ResourceMetricsResponse) { r.Results = append(r.Results, r.Results[0]) }},
		{"nan", func(r *ResourceMetricsResponse) { v := math.NaN(); r.Results[0].Value = &v }},
		{"infinity", func(r *ResourceMetricsResponse) { v := math.Inf(1); r.Results[0].Value = &v }},
		{"missing numeric", func(r *ResourceMetricsResponse) { r.Results[0].Status = "missing" }},
		{"unknown status", func(r *ResourceMetricsResponse) { r.Results[0].Status = "success" }},
		{"false completeness", func(r *ResourceMetricsResponse) { r.Results = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := &ResourceMetricsResponse{Complete: true, Results: []ResourceMetricResult{base}}
			test.change(r)
			if ValidateResourceMetricsResponse(q, r) == nil {
				t.Fatal("invalid response admitted")
			}
		})
	}
	if err := ValidateResourceMetricsResponse(q, &ResourceMetricsResponse{Complete: false}); err != nil {
		t.Fatal("explicit incomplete response rejected", err)
	}
}
func TestFiniteRangeGapsKeepSourceTimes(t *testing.T) {
	q := finiteTestQuery()
	q.Shape = "range"
	q.StartTime = time.Unix(100, 0)
	q.EndTime = q.StartTime.Add(time.Minute)
	q.Step = time.Minute
	value := 2.0
	r := &ResourceMetricsResponse{Complete: true, Results: []ResourceMetricResult{{ResourceToken: "pod", MetricID: "cpu", Status: "ok", Points: []ResourceMetricPoint{{Time: q.StartTime, Value: &value}, {Time: q.EndTime}}}}}
	got, err := finiteResponseFromProto(q, finiteResponseToProto(r))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Results[0].Points) != 2 || got.Results[0].Points[1].Value != nil || !got.Results[0].Points[1].Time.Equal(q.EndTime) {
		t.Fatal("range gap or source time changed")
	}
	r.Results[0].Points[1].Time = q.StartTime
	if ValidateResourceMetricsResponse(q, r) == nil {
		t.Fatal("duplicate point time admitted")
	}
}
func TestFiniteCatalogUsesBaseUnitsAndExplicitShapes(t *testing.T) {
	c := &ResourceMetricsCatalog{Source: finiteTestQuery().Source, MaxTargets: 256, Metrics: []FiniteMetricDescriptor{{ID: "cpu", ResourceKey: "opaque::type", Unit: "cores", Shapes: []string{"current"}}}}
	if err := ValidateResourceMetricsCatalog(c); err != nil {
		t.Fatal(err)
	}
	c.Metrics[0].Unit = "millicores"
	if ValidateResourceMetricsCatalog(c) == nil {
		t.Fatal("non-base unit admitted")
	}
	c.Metrics[0].Unit = "cores"
	c.Metrics[0].Shapes = []string{"0"}
	if ValidateResourceMetricsCatalog(c) == nil {
		t.Fatal("enum-number shape admitted")
	}
}

func TestFiniteManagerAdmitsEveryTargetBeforeCollection(t *testing.T) {
	_, registry := metricRegistry(t, []types.Connection{{ID: "one", Data: map[string]any{"endpoint": "configured"}}, {ID: "two", Data: map[string]any{"endpoint": "other"}}}, nil, nil)
	m := NewManager(nil, nil, ProviderInfo{ID: "legacy-id"}, nil, nil, nil)
	m.sourcePluginID = "metric"
	m.BindConnectionContextProvider(registry)
	m.resourceMetricsCatalogFunc = func(pc *types.PluginContext, request ResourceMetricsCatalogRequest) (*ResourceMetricsCatalog, error) {
		key := request.ResourceKey
		if pc.Connection.Data["endpoint"] != "configured" {
			t.Fatal("caller metadata replaced configured profile")
		}
		return &ResourceMetricsCatalog{Source: finiteTestQuery().Source, MaxTargets: 256, Metrics: []FiniteMetricDescriptor{{ID: "cpu", ResourceKey: key, Unit: "cores", Shapes: []string{"current"}}}}, nil
	}
	calls := 0
	m.queryResourcesFunc = func(pc *types.PluginContext, q ResourceMetricsQuery) (*ResourceMetricsResponse, error) {
		calls++
		results := make([]ResourceMetricResult, 0, len(q.Targets))
		for _, target := range q.Targets {
			results = append(results, ResourceMetricResult{ResourceToken: target.Token, MetricID: "cpu", Status: "missing"})
		}
		return &ResourceMetricsResponse{Complete: true, Results: results}, nil
	}
	ctx := resourcev2.WithCatalogRevision(metricConnectionContext("one"), metricPin(t, registry, "one"))
	pc := types.PluginContextFromContext(ctx)
	pc.Context = ctx
	q := finiteTestQuery()
	q.Targets = append(q.Targets, ResourceMetricTarget{Token: "second", Reference: *metricReference(), Route: resourcev2.AccessRoute{Connection: "two"}})
	if _, err := m.QueryResources(pc, q); err == nil || calls != 0 {
		t.Fatal("second target bypassed connection admission", err, calls)
	}
	q.Targets[1].Route.Connection = "one"
	if _, err := m.QueryResources(pc, q); err != nil || calls != 1 {
		t.Fatal("valid batch failed", err, calls)
	}
	q.Source.PluginID = "legacy-id"
	if _, err := m.QueryResources(pc, q); err == nil || calls != 1 {
		t.Fatal("legacy provider ID became plugin authority", err, calls)
	}
}

func TestFiniteOldProcessNeverFallsBackToLegacyCollection(t *testing.T) {
	old := new(oldMetricServer)
	server := grpc.NewServer(resourceplugin.ServerOptions()...)
	metricpb.RegisterMetricPluginServer(server, old)
	client := &PluginClient{client: metricpb.NewMetricPluginClient(metricPipe(t, server))}
	pc := &types.PluginContext{Context: t.Context(), Connection: &types.Connection{ID: "one"}}
	if _, err := client.QueryResources(pc, finiteTestQuery()); resourcev2.ErrorCode(err) != "UNSUPPORTED" {
		t.Fatal("missing finite query capability not explicit", err)
	}
	if _, err := client.GetResourceMetricsCatalog(pc, ResourceMetricsCatalogRequest{Source: finiteTestQuery().Source, ResourceKey: "opaque::type"}); resourcev2.ErrorCode(err) != "UNSUPPORTED" {
		t.Fatal("missing finite catalog capability not explicit", err)
	}
	if old.calls.Load() != 0 {
		t.Fatal("finite negotiation invoked legacy collection")
	}
}
