package metric

import (
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	metricpb "github.com/omniviewdev/plugin-sdk/proto/v1/metric"
	"google.golang.org/protobuf/proto"
	"reflect"
	"testing"
)

func finiteMappingFixture() MetricDefinition {
	return MetricDefinition{ID: "cpu", Name: "CPU usage", ResourceKeys: []string{"opaque::type"}, Unit: "cores", SupportedShapes: []string{"current", "range"}, QueryLanguage: "promql", Query: "sum by (cluster,namespace,pod,uid) (rate(container_cpu_usage_seconds_total{${matchers}}[5m]))", FieldBindings: []MetricFieldBinding{{Field: "name", Label: "pod", Source: "resource", ResourcePath: "/metadata/name", Required: true}, {Field: "cluster", Label: "cluster", Source: "scope", Required: true}}, IdentityLabels: []string{"cluster", "namespace", "pod", "uid"}, Reducer: "sum", BatchStrategy: "vector", Revision: "mapping-1"}
}

func TestFiniteMappingAndProjectionSurviveTypedWire(t *testing.T) {
	q := finiteTestQuery()
	definition := finiteMappingFixture()
	definition.RangeReuse = true
	q.Context = MetricQueryContext{Definitions: []MetricDefinition{definition}, PublicScope: map[string]string{"cluster": "approved"}, MappingRevision: "source-1", ProjectionRevision: "projection-1"}
	q.Targets[0].PublicData = map[string]string{"node": "worker-01"}
	p, err := finiteQueryToProto(q)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := proto.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var decoded metricpb.ResourceMetricsQuery
	if err = proto.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	got, err := finiteQueryFromProto(&decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, q) {
		t.Fatalf("typed context changed: %#v", got)
	}
}

func TestFiniteMappingContextRejectsEmptyScopeAndDuplicateMappings(t *testing.T) {
	for _, c := range []MetricQueryContext{
		{PublicScope: map[string]string{"": "invalid"}},
		{Definitions: []MetricDefinition{finiteMappingFixture(), finiteMappingFixture()}},
	} {
		if ValidateMetricQueryContext(c) == nil {
			t.Fatal("unapproved context admitted")
		}
	}
}

func TestFiniteMappingWireRejectsNilDefinitionsAndBindings(t *testing.T) {
	for _, p := range []*metricpb.MetricQueryContext{
		{Definitions: []*metricpb.MetricDefinition{nil}},
		{Definitions: []*metricpb.MetricDefinition{{FieldBindings: []*metricpb.MetricFieldBinding{nil}}}},
	} {
		if _, err := finiteContextFromProto(p); err == nil {
			t.Fatal("nil mapping wire member admitted")
		}
	}
}

func TestFiniteProvenanceRejectsWrongSource(t *testing.T) {
	q := finiteTestQuery()
	other := q.Source
	other.ConnectionID = "other"
	response := &ResourceMetricsResponse{Complete: true, Results: []ResourceMetricResult{{ResourceToken: "pod", MetricID: "cpu", Status: "missing", Source: &other}}}
	if ValidateResourceMetricsResponse(q, response) == nil {
		t.Fatal("wrong source provenance admitted")
	}
	response.Results[0].Source = &q.Source
	wire := finiteResponseToProto(response)
	got, err := finiteResponseFromProto(q, wire)
	if err != nil {
		t.Fatal(err)
	}
	if got.Results[0].Source == nil || *got.Results[0].Source != q.Source {
		t.Fatal("source provenance lost")
	}
}

func TestFiniteUnitsPreservePhysicalQuantity(t *testing.T) {
	for _, unit := range []string{"cores", "bytes", "seconds", "count", "ratio", "percent", "bytes_per_second", "count_per_second"} {
		d := finiteMappingFixture()
		d.Unit = unit
		if err := ValidateMetricDefinitionContract(d); err != nil {
			t.Fatal(unit, err)
		}
	}
	for _, unit := range []string{"", "millicores", "kilobytes", "0", "requests"} {
		if ValidFiniteUnit(unit) {
			t.Fatal("invented or scaled unit admitted", unit)
		}
	}
}

func TestFiniteRangeReuseDeclarationRequiresRangeShape(t *testing.T) {
	d := finiteMappingFixture()
	d.RangeReuse = true
	owner := func(definition MetricDefinition) *MetricOwnerCatalog {
		return &MetricOwnerCatalog{Definitions: []MetricDefinition{definition}, ScopeFields: []MetricScopeField{{Key: "cluster", Name: "Cluster"}}}
	}
	for _, shapes := range [][]string{{"current", "range"}, {"range"}} {
		d.SupportedShapes = shapes
		if err := ValidateMetricDefinitionContract(d); err != nil {
			t.Fatal("range reuse rejected for", shapes, err)
		}
		if err := ValidateMetricOwnerCatalog(owner(d)); err != nil {
			t.Fatal("owner declaration rejected for", shapes, err)
		}
	}
	d.SupportedShapes = []string{"current"}
	if err := ValidateMetricDefinitionContract(d); resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatal("range reuse admitted without the range shape", err)
	}
	if err := ValidateMetricOwnerCatalog(owner(d)); resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatal("owner declared range reuse without the range shape", err)
	}
	mapping := MetricQueryContext{Definitions: []MetricDefinition{d}}
	if err := ValidateMetricQueryContext(mapping); resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatal("host mapping carried range reuse without the range shape", err)
	}
	if _, err := finiteContextFromProto(finiteContextToProto(mapping)); resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatal("peer mapping declared range reuse without the range shape", err)
	}
	d.RangeReuse = false
	if err := ValidateMetricDefinitionContract(d); err != nil {
		t.Fatal("absent range reuse changed current-only definition validity", err)
	}
}

func TestFiniteCatalogRequestRequiresExactSourceBeforeCallback(t *testing.T) {
	m := NewManager(nil, nil, ProviderInfo{ID: "display-provider-id"}, nil, nil, nil)
	m.sourcePluginID = "metric"
	_, registry := metricRegistry(t, []types.Connection{{ID: "one"}}, nil, nil)
	m.BindConnectionContextProvider(registry)
	calls := 0
	m.resourceMetricsCatalogFunc = func(_ *types.PluginContext, r ResourceMetricsCatalogRequest) (*ResourceMetricsCatalog, error) {
		calls++
		return &ResourceMetricsCatalog{Source: r.Source, MaxTargets: 1}, nil
	}
	m.queryResourcesFunc = func(_ *types.PluginContext, _ ResourceMetricsQuery) (*ResourceMetricsResponse, error) {
		return nil, nil
	}
	pc := &types.PluginContext{Context: t.Context(), Connection: &types.Connection{ID: "one"}}
	for _, source := range []MetricSourceRef{{}, {PluginID: "other", ConnectionID: "one", Native: true}, {PluginID: "metric", ConnectionID: "other", Native: true}} {
		if _, err := m.GetResourceMetricsCatalog(pc, ResourceMetricsCatalogRequest{Source: source, ResourceKey: "opaque::type"}); err == nil {
			t.Fatal("unselected source reached catalog")
		}
	}
	if calls != 0 {
		t.Fatal("unselected source invoked callback", calls)
	}
	if _, err := m.GetResourceMetricsCatalog(pc, ResourceMetricsCatalogRequest{Source: finiteTestQuery().Source, ResourceKey: "opaque::type"}); err != nil || calls != 1 {
		t.Fatal("matching source did not execute catalog", err, calls)
	}
}
