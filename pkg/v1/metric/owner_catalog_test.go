package metric

import (
	"github.com/omniviewdev/plugin-sdk/pkg/interceptors"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	metricpb "github.com/omniviewdev/plugin-sdk/proto/v1/metric"
	"google.golang.org/grpc"
	"strings"
	"testing"
)

func TestGenericSourceIdentity(t *testing.T) {
	for _, source := range []MetricSourceRef{
		{PluginID: "database", ConnectionID: "production", Native: true},
		{PluginID: "observability", ConnectionID: "shared", DatasourceKind: "prometheus", DatasourceUID: "primary"},
	} {
		if err := validateFiniteSource(source); err != nil {
			t.Fatal(err)
		}
		if got := sourceFromProto(sourceToProto(source)); got != source {
			t.Fatal("source changed", got)
		}
	}
	for _, source := range []MetricSourceRef{
		{PluginID: "database", ConnectionID: "production"},
		{PluginID: "database", ConnectionID: "production", Native: true, DatasourceKind: "sentinel"},
		{PluginID: "database", ConnectionID: "production", Native: true, DatasourceUID: "unexpected"},
		{PluginID: "observability", ConnectionID: "shared", DatasourceKind: "prometheus"},
		{PluginID: "observability", ConnectionID: "shared", DatasourceUID: "primary"},
	} {
		if validateFiniteSource(source) == nil {
			t.Fatal("invalid source admitted", source)
		}
	}
}

func TestGenericOwnerScopeBoundsAndDeclarations(t *testing.T) {
	d := finiteMappingFixture()
	d.FieldBindings = []MetricFieldBinding{{Field: "account", Label: "account_id", Source: "scope", Required: true}}
	catalog := &MetricOwnerCatalog{Definitions: []MetricDefinition{d}, ScopeFields: []MetricScopeField{{Key: "account", Name: "Account", Description: "Public account identifier"}}}
	if err := ValidateMetricOwnerCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMetricQueryContext(MetricQueryContext{PublicScope: map[string]string{"account": "public", "region": "west"}}); err != nil {
		t.Fatal(err)
	}
	if ValidateMetricQueryContext(MetricQueryContext{PublicScope: map[string]string{"account": strings.Repeat("x", 1025)}}) == nil {
		t.Fatal("oversize scope admitted")
	}
	catalog.ScopeFields = nil
	if ValidateMetricOwnerCatalog(catalog) == nil {
		t.Fatal("undeclared scope admitted")
	}
	catalog.Definitions = nil
	for i := range 33 {
		catalog.ScopeFields = append(catalog.ScopeFields, MetricScopeField{Key: strings.Repeat("x", i+1), Name: "Scope"})
	}
	if ValidateMetricOwnerCatalog(catalog) == nil {
		t.Fatal("too many scope fields admitted")
	}
	scope := map[string]string{}
	for i := range 33 {
		scope[strings.Repeat("x", i+1)] = "value"
	}
	if ValidateMetricQueryContext(MetricQueryContext{PublicScope: scope}) == nil {
		t.Fatal("too many scope entries admitted")
	}
}
func TestRegisteredOwnerOnlyCatalogTransport(t *testing.T) {
	_, registry := metricRegistry(t, []types.Connection{{ID: "one", Data: map[string]any{"endpoint": "configured"}}}, nil, nil)
	manager := NewManager(nil, nil, ProviderInfo{ID: "display-only"}, nil, nil, nil)
	manager.BindConnectionContextProvider(registry)
	calls := 0
	manager.getMetricOwnerCatalogFunc = func(pc *types.PluginContext) (*MetricOwnerCatalog, error) {
		calls++
		if pc.Connection.ID != "one" || pc.Connection.Data["endpoint"] != "configured" {
			t.Error("owner lease lost")
		}
		d := finiteMappingFixture()
		d.ResourceKeys = []string{"database/table"}
		d.FieldBindings = []MetricFieldBinding{{Field: "name", Label: "table", Source: "resource", ResourcePath: "/metadata/name", Required: true}, {Field: "account", Label: "account", Source: "scope", Required: true}}
		return &MetricOwnerCatalog{Definitions: []MetricDefinition{d}, ScopeFields: []MetricScopeField{{Key: "account", Name: "Account", Description: "Public account"}}}, nil
	}
	options := append(resourceplugin.ServerOptions(), grpc.ChainUnaryInterceptor(interceptors.UnaryPluginContext()), grpc.ChainStreamInterceptor(interceptors.StreamPluginContext()))
	server := grpc.NewServer(options...)
	if err := (&Plugin{Impl: manager}).GRPCServer(nil, server); err != nil {
		t.Fatal(err)
	}
	client := &PluginClient{client: metricpb.NewMetricPluginClient(metricPipe(t, server))}
	ctx := resourcev2.WithCatalogRevision(metricConnectionContext("one"), metricPin(t, registry, "one"))
	pc := types.PluginContextFromContext(ctx)
	pc.Context = ctx
	supported, err := client.GetMetricOwnerCapabilities(ctx)
	if err != nil || !supported {
		t.Fatalf("owner-only capability: %v %v", supported, err)
	}
	catalog, err := client.GetMetricOwnerCatalog(pc)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(catalog.Definitions) != 1 || len(catalog.ScopeFields) != 1 || catalog.ScopeFields[0].Description != "Public account" || catalog.Definitions[0].FieldBindings[0].ResourcePath != "/metadata/name" {
		t.Fatal("owner catalog changed", catalog, calls)
	}
	if err := client.negotiateFinite(ctx); err == nil {
		t.Fatal("owner-only provider advertised finite execution")
	}
	manager.getMetricOwnerCatalogFunc = func(*types.PluginContext) (*MetricOwnerCatalog, error) {
		return nil, metricError("CONFLICT", "Owner catalog changed.")
	}
	if _, err := client.GetMetricOwnerCatalog(pc); resourcev2.ErrorCode(err) != "CONFLICT" {
		t.Fatalf("owner error hidden: %v", err)
	}
	manager.getMetricOwnerCatalogFunc = nil
	supported, err = client.GetMetricOwnerCapabilities(ctx)
	if err != nil || supported {
		t.Fatalf("absent owner capability: %v %v", supported, err)
	}
	if _, err := client.GetMetricOwnerCatalog(pc); resourcev2.ErrorCode(err) != "UNSUPPORTED" {
		t.Fatalf("absent owner trait: %v", err)
	}
}
