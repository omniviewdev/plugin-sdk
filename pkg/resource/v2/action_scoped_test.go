package resourcev2

import (
	"context"
	"reflect"
	"testing"
)

func scopedActionScope(region string) QueryScope {
	values := []string{region}
	return QueryScope{{Dimension: "region", Match: ScopeOneOf, Values: &values}}
}

func scopedActionController(t *testing.T, west, east ActionDescriptor, calls *int) *Controller[string] {
	t.Helper()
	cfg := controllerConfig()
	original := cfg.Catalog
	cfg.Catalog = func(ctx context.Context, request CatalogRequest) (Catalog, error) {
		catalog, err := original(ctx, request)
		catalog.Types[0].QueryDimensions = []Dimension{{Name: "region"}}
		catalog.Types[0].Capabilities = []Capability{
			{Operation: OperationAction, Scope: QueryScope{{Dimension: "region", Match: ScopeAny}}},
		}
		return catalog, err
	}
	for i, descriptor := range []ActionDescriptor{west, east} {
		region := []string{"west", "east"}[i]
		cfg.Registrations = append(cfg.Registrations, Registration[string]{
			Type: "opaque/type", Scope: scopedActionScope(region), Actions: []ActionDescriptor{descriptor},
			Action: func(context.Context, *string, ActionRequest) (Value, error) {
				(*calls)++
				return Value{Kind: "null"}, nil
			},
		})
	}
	return newTestController(t, cfg)
}

func scopedActionRequest(region string, input Value) ActionRequest {
	return ActionRequest{
		Route: controllerTarget().Route,
		TypeTarget: &TypeActionTarget{
			Provider:  "test",
			Authority: "authority",
			Type:      "opaque/type",
			Scope:     scopedActionScope(region),
		},
		Action: "run",
		Input:  input,
	}
}

func TestScopedActionsDeduplicateIdenticalDiscovery(t *testing.T) {
	calls := 0
	descriptor := ActionDescriptor{ID: "run", Scope: ActionScopeType}
	controller := scopedActionController(t, descriptor, descriptor, &calls)
	descriptors, err := controller.GetActions(context.Background(), controllerTarget().Route, "opaque/type")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(descriptors, []ActionDescriptor{descriptor}) {
		t.Fatalf("discovery = %#v", descriptors)
	}
	ctx := acceptedControllerContext(context.Background(), t, controller)
	for _, region := range []string{"west", "east"} {
		if _, err := ResolveActionDescriptor(
			ctx,
			controller,
			scopedActionRequest(region, Value{Kind: "null"}),
			false,
		); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 0 {
		t.Fatal("descriptor resolution dispatched native action")
	}
}

func TestScopedActionsKeepConflictingSchemasRequestSpecific(t *testing.T) {
	calls := 0
	descriptor := func(kind string) ActionDescriptor {
		return ActionDescriptor{ID: "run", Scope: ActionScopeType, ParamsSchema: &Schema{
			Properties: map[string]SchemaProperty{"input": {Type: SchemaType(kind)}}, Required: []string{"input"},
		}}
	}
	west, east := descriptor("string"), descriptor("boolean")
	controller := scopedActionController(t, west, east, &calls)
	unrelated := ActionDescriptor{ID: "inspect", Scope: ActionScopeType}
	controller.cfg.Registrations[0].Actions = append(controller.cfg.Registrations[0].Actions, unrelated)
	descriptors, err := controller.GetActions(context.Background(), controllerTarget().Route, "opaque/type")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(descriptors, []ActionDescriptor{unrelated}) {
		t.Fatalf("conflicting action widened or unrelated action lost: %#v", descriptors)
	}
	ctx := acceptedControllerContext(context.Background(), t, controller)
	input := Value{Kind: "map", Map: map[string]Value{"input": {Kind: "string", String: "west"}}}
	selected, err := ResolveActionDescriptor(ctx, controller, scopedActionRequest("west", input), false)
	if err != nil {
		t.Fatal(err)
	}
	if selected == nil || !reflect.DeepEqual(*selected, west) {
		t.Fatal("selected wrong scoped schema")
	}
	if _, err = ResolveActionDescriptor(ctx, controller, scopedActionRequest("east", input), false); err == nil {
		t.Fatal("accepted input against wrong scoped schema")
	}
	if _, err = controller.Action(ctx, scopedActionRequest("west", input)); err != nil {
		t.Fatal(err)
	}
	if _, err = controller.Action(ctx, scopedActionRequest("east", input)); err == nil {
		t.Fatal("native dispatch accepted wrong scoped input")
	}
	if calls != 1 {
		t.Fatalf("native calls = %d", calls)
	}
}
