package resourcev2

import (
	"context"
	"testing"
)

func TestControllerActionSchemaValidation(t *testing.T) {
	for _, invalidInput := range []bool{false, true} {
		t.Run(map[bool]string{false: "output", true: "input"}[invalidInput], func(t *testing.T) {
			cfg := controllerConfig()
			called := false
			descriptor := ActionDescriptor{ID: "run", Scope: ActionScopeInstance, OutputSchema: (&Schema{}).Ptr()}
			if invalidInput {
				descriptor.ParamsSchema = (&Schema{Properties: map[string]SchemaProperty{"name": {Type: SchemaString}}, Required: []string{"name"}}).Ptr()
			}
			cfg.Registrations = []Registration[string]{
				{
					Type:    "opaque/type",
					Actions: []ActionDescriptor{descriptor},
					Action: func(context.Context, *string, ActionRequest) (Value, error) {
						called = true
						return Value{Kind: "string", String: "invalid object output"}, nil
					},
				},
			}
			controller := newTestController(t, cfg)
			request := ActionRequest{
				Route:     controllerTarget().Route,
				Reference: ResourceReference{Address: controllerAddress()},
				Action:    "run",
				Input:     Value{Kind: "map"},
			}
			_, err := controller.Action(acceptedControllerContext(context.Background(), t, controller), request)
			if err == nil {
				t.Fatal("schema violation succeeded")
			}
			if called == invalidInput {
				t.Fatalf("wrong dispatch state: called=%v", called)
			}
		})
	}
}
