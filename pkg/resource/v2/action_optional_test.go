package resourcev2

import (
	"context"
	"encoding/json"
	"testing"
)

type optionalActionCatalog struct{ descriptors []ActionDescriptor }

func (p optionalActionCatalog) GetActions(context.Context, AccessRoute, string) ([]ActionDescriptor, error) {
	return p.descriptors, nil
}

func TestActionWithoutDiscoveryDoesNotInventDescriptor(t *testing.T) {
	request := typeActionRequest()
	descriptor, err := ResolveActionDescriptor(context.Background(), struct{}{}, request, false)
	if err != nil || descriptor != nil {
		t.Fatalf("unexpected discovery: %#v %v", descriptor, err)
	}
	request.ExpectedDescriptor = &ActionDescriptor{ID: request.Action, Scope: ActionScopeType}
	_, err = ResolveActionDescriptor(context.Background(), struct{}{}, request, false)
	assertControllerCode(t, err, "RESET_REQUIRED")
}

func TestNativeControllerActionWithoutDiscovery(t *testing.T) {
	calls := 0
	cfg := controllerConfig()
	cfg.Registrations = []Registration[string]{{
		Type: "opaque/type",
		Action: func(context.Context, *string, ActionRequest) (Value, error) {
			calls++
			return Value{Kind: "null"}, nil
		},
	}}
	controller := newTestController(t, cfg)
	ctx := acceptedControllerContext(context.Background(), t, controller)
	request := ActionRequest{
		Route:     controllerTarget().Route,
		Reference: controllerRecord().Reference,
		Action:    "native",
		Input:     Value{Kind: "null"},
	}
	if _, err := controller.Action(ctx, request); err != nil {
		t.Fatal(err)
	}
	request.ExpectedDescriptor = &ActionDescriptor{ID: request.Action, Scope: ActionScopeInstance}
	if _, err := controller.Action(ctx, request); ErrorCode(err) != "RESET_REQUIRED" {
		t.Fatalf("missing expectation: %v", err)
	}
	if calls != 1 {
		t.Fatalf("native calls = %d", calls)
	}
}

func TestExpectedActionDescriptorJSONSemanticEquality(t *testing.T) {
	actual := ActionDescriptor{
		ID:           "run",
		Scope:        ActionScopeType,
		ParamsSchema: &Schema{Properties: map[string]SchemaProperty{}, Required: []string{}},
	}
	encoded, err := json.Marshal(actual)
	if err != nil {
		t.Fatal(err)
	}
	var expected ActionDescriptor
	if err = json.Unmarshal(encoded, &expected); err != nil {
		t.Fatal(err)
	}
	request := typeActionRequest()
	request.Input = Value{Kind: "map", Map: map[string]Value{}}
	request.ExpectedDescriptor = &expected
	provider := optionalActionCatalog{descriptors: []ActionDescriptor{actual}}
	if _, err = ResolveActionDescriptor(context.Background(), provider, request, false); err != nil {
		t.Fatal(err)
	}
	calls := 0
	controller := scopedActionController(t, actual, expected, &calls)
	discovered, err := controller.GetActions(context.Background(), controllerTarget().Route, "opaque/type")
	if err != nil || len(discovered) != 1 {
		t.Fatalf("semantic duplicates conflicted: %#v %v", discovered, err)
	}
	actual.Dangerous = true
	provider.descriptors[0] = actual
	if _, err = ResolveActionDescriptor(
		context.Background(),
		provider,
		request,
		false,
	); ErrorCode(
		err,
	) != "RESET_REQUIRED" {
		t.Fatalf("descriptor drift: %v", err)
	}
}
