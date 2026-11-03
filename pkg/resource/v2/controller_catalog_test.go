package resourcev2

import (
	"context"
	"reflect"
	"testing"
)

func TestControllerCatalogDeduplicatesCapabilities(t *testing.T) {
	cfg := controllerConfig()
	catalog, err := cfg.Catalog(context.Background(), CatalogRequest{Route: controllerTarget().Route})
	if err != nil {
		t.Fatal(err)
	}
	east, west := []string{"east"}, []string{"west"}
	declarations := []Capability{
		{Operation: OperationGet, Scope: QueryScope{{Dimension: "region", Match: ScopeAny}}},
		{Operation: OperationGet, Scope: QueryScope{{Dimension: "region", Match: ScopeOneOf, Values: &east}}},
		{Operation: OperationGet, Scope: QueryScope{{Dimension: "region", Match: ScopeOneOf, Values: &west}}},
	}
	catalog.Types[0].QueryDimensions = []Dimension{{Name: "region"}}
	catalog.Types[0].Capabilities = declarations
	cfg.Catalog = func(context.Context, CatalogRequest) (Catalog, error) { return catalog, nil }
	get := func(context.Context, *string, GetRequest) (Record, error) { return Record{}, nil }
	cfg.Registrations = []Registration[string]{{Type: "opaque/type", Get: get}, {Type: "opaque/type", Get: get}}
	c := newTestController(t, cfg)
	got, err := c.Catalog(context.Background(), CatalogRequest{Route: controllerTarget().Route})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Types[0].Capabilities, declarations) {
		t.Fatalf("catalog changed capability order, scope, or uniqueness: %#v", got.Types[0].Capabilities)
	}
	got.Types[0].Capabilities[0].Operation = OperationDelete
	if declarations[0].Operation != OperationGet {
		t.Fatal("qualified capability slice aliases provider slice")
	}
}

func TestControllerCatalogReadsDynamicCapabilities(t *testing.T) {
	cfg := controllerConfig()
	catalog, err := cfg.Catalog(context.Background(), CatalogRequest{Route: controllerTarget().Route})
	if err != nil {
		t.Fatal(err)
	}
	catalog.Types[0].Capabilities = []Capability{{Operation: OperationGet, Scope: QueryScope{}}}
	cfg.Catalog = func(context.Context, CatalogRequest) (Catalog, error) { return catalog, nil }
	get := func(context.Context, *string, GetRequest) (Record, error) { return Record{}, nil }
	cfg.Registrations = []Registration[string]{{Type: "opaque/type", Get: get}, {Type: "opaque/type", Get: get}}
	c := newTestController(t, cfg)
	first, err := c.Catalog(context.Background(), CatalogRequest{Route: controllerTarget().Route})
	if err != nil {
		t.Fatal(err)
	}
	first.Types[0].Capabilities[0].Operation = OperationDelete
	if catalog.Types[0].Capabilities[0].Operation != OperationGet {
		t.Fatal("returned capability mutation changed provider declaration")
	}
	catalog.Types[0].Capabilities = []Capability{{Operation: OperationEnumerate, Scope: QueryScope{}}}
	second, err := c.Catalog(context.Background(), CatalogRequest{Route: controllerTarget().Route})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Types[0].Capabilities) != 0 {
		t.Fatal("dynamic catalog reused previous capabilities")
	}
	if catalog.Types[0].Capabilities[0].Operation != OperationEnumerate {
		t.Fatal("qualification changed provider declaration")
	}
}

func TestCatalogRegistrationScopeValidationOrder(t *testing.T) {
	cfg := controllerConfig()
	get := func(context.Context, *string, GetRequest) (Record, error) { return Record{}, nil }
	cfg.Registrations = []Registration[string]{
		{Type: "opaque/type", Scope: QueryScope{{Dimension: "unused", Match: ScopeAny}}},
		{Type: "opaque/type", Scope: QueryScope{{Dimension: "unknown", Match: ScopeAny}}, Get: get},
		{Type: "opaque/type", Scope: QueryScope{{Dimension: "region", Match: ScopeAny}}, Get: get},
	}
	c := newTestController(t, cfg)
	typ := ResourceType{
		Type: "opaque/type", QueryDimensions: []Dimension{{Name: "region"}},
		Capabilities: []Capability{{Operation: OperationEnumerate}, {Operation: OperationGet}},
	}
	err := c.catalogCapabilities(context.Background(), controllerTarget().Route, &typ)
	want := ValidateScope(cfg.Registrations[1].Scope, typ.QueryDimensions)
	if err == nil || want == nil || err.Error() != want.Error() {
		t.Fatalf("scope error order changed: got %v, want %v", err, want)
	}
}

func TestCatalogQualificationRefreshesRegistrationScope(t *testing.T) {
	cfg := controllerConfig()
	values := []string{"east"}
	calls := 0
	cfg.ResolveRegistration = func(_ context.Context, route AccessRoute, typ string) (Registration[string], error) {
		calls++
		return Registration[string]{
			Type: typ, Connection: route.Connection,
			Scope: QueryScope{{Dimension: "region", Match: ScopeOneOf, Values: &values}},
			Get:   func(context.Context, *string, GetRequest) (Record, error) { return Record{}, nil },
		}, nil
	}
	c := newTestController(t, cfg)
	for _, region := range []string{"east", "west"} {
		values = []string{region}
		typ := ResourceType{
			Type: "opaque/type", QueryDimensions: []Dimension{{Name: "region"}},
			Capabilities: []Capability{
				{Operation: OperationGet, Scope: QueryScope{{Dimension: "region", Match: ScopeAny}}},
				{Operation: OperationGet, Scope: QueryScope{{Dimension: "region", Match: ScopeAny}}},
			},
		}
		if err := c.catalogCapabilities(context.Background(), controllerTarget().Route, &typ); err != nil {
			t.Fatal(err)
		}
		if len(typ.Capabilities) != 1 || !reflect.DeepEqual(*typ.Capabilities[0].Scope[0].Values, []string{region}) {
			t.Fatalf("registration scope did not refresh: %#v", typ.Capabilities)
		}
	}
	if calls != 2 {
		t.Fatalf("registration resolver calls: got %d, want 2", calls)
	}
}
