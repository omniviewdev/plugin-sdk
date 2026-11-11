package resourcev2

import (
	"context"
	"testing"
)

// streamingWrapper mirrors providers that embed *Controller and add their own
// StreamAction, so the Controller's descriptor resolver is promoted onto them.
type streamingWrapper struct {
	*Controller[string]
}

func (streamingWrapper) StreamAction(context.Context, ActionRequest, chan<- ActionEvent) error {
	return nil
}

func TestWrappedControllerResolvesStreamingActionDescriptor(t *testing.T) {
	cfg := controllerConfig()
	cfg.Registrations = []Registration[string]{{
		Type: "opaque/type",
		Actions: []ActionDescriptor{
			{ID: "stream", Scope: ActionScopeInstance, Streaming: true},
			{ID: "run", Scope: ActionScopeInstance},
		},
		Action: func(context.Context, *string, ActionRequest) (Value, error) { return Value{Kind: "null"}, nil },
	}}
	provider := streamingWrapper{newTestController(t, cfg)}
	ctx := acceptedControllerContext(context.Background(), t, provider)
	request := func(action string) ActionRequest {
		return ActionRequest{
			Route:     controllerTarget().Route,
			Reference: ResourceReference{Address: controllerAddress()},
			Action:    action,
			Input:     Value{Kind: "null"},
		}
	}

	descriptor, err := ResolveActionDescriptor(ctx, provider, request("stream"), true)
	if err != nil {
		t.Fatalf("streaming descriptor rejected: %v", err)
	}
	if descriptor == nil || !descriptor.Streaming {
		t.Fatalf("descriptor = %+v, want streaming", descriptor)
	}
	if _, err := AdmitStreamingAction(ctx, provider, request("stream")); err != nil {
		t.Fatalf("streaming admission failed: %v", err)
	}

	_, err = ResolveActionDescriptor(ctx, provider, request("run"), true)
	assertControllerCode(t, err, "UNSUPPORTED")
	_, err = ResolveActionDescriptor(ctx, provider, request("stream"), false)
	assertControllerCode(t, err, "UNSUPPORTED")
	_, err = ResolveActionDescriptor(ctx, provider, request("missing"), true)
	assertControllerCode(t, err, "UNSUPPORTED")
}

func TestWrappedControllerResolvesStreamingOnlyRegistration(t *testing.T) {
	cfg := controllerConfig()
	cfg.Registrations = []Registration[string]{{
		Type:    "opaque/type",
		Actions: []ActionDescriptor{{ID: "stream", Scope: ActionScopeInstance, Streaming: true}},
	}}
	provider := streamingWrapper{newTestController(t, cfg)}
	ctx := acceptedControllerContext(context.Background(), t, provider)
	request := ActionRequest{
		Route:     controllerTarget().Route,
		Reference: ResourceReference{Address: controllerAddress()},
		Action:    "stream",
		Input:     Value{Kind: "null"},
	}
	descriptor, err := ResolveActionDescriptor(ctx, provider, request, true)
	if err != nil {
		t.Fatalf("streaming-only registration rejected: %v", err)
	}
	if descriptor == nil || !descriptor.Streaming {
		t.Fatalf("descriptor = %+v, want streaming", descriptor)
	}
	_, err = ResolveActionDescriptor(ctx, provider, request, false)
	assertControllerCode(t, err, "UNSUPPORTED")
}
