package exec

import (
	"context"
	"time"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// TargetRef names one thing to ask about: the canonical reference of the resource and the route of the connection
// that reaches it. It carries no selection: Options takes none, and the requests that do are not part of this build.
type TargetRef struct {
	// Reference is the canonical resource reference, with its incarnation when the host has one. Required.
	Reference resourcev2.ResourceReference
	// Route is the connection that reaches the resource, and must be the caller's connection. Required.
	Route resourcev2.AccessRoute
}

// OptionsResult is the answer to Options.
type OptionsResult struct {
	// Options lists what a user can choose for the resource; empty means the target has nothing to choose.
	Options []Option
	// ObservedAt is the plugin's clock when its callback returned.
	ObservedAt time.Time
}

// Client is what the host calls: the exec lane of one plugin, as far as this build goes. The SDK's gRPC client
// (PluginClient.V2) implements it; the interface grows with the engine steps, so do not implement it, embed it.
//
// Every method takes the context of the call: cancelling it cancels the call, and a call without a deadline is
// bounded by the SDK. A method that addresses a resource needs the call's types.PluginContext in ctx
// (types.WithPluginContext): its Connection is the caller's connection and must match the route.
//
// Errors are *Error for a refusal or failure the exec lane classified; the resource-lane errors of connection and
// catalog admission (*resourcev2.ResourceOperationError, such as RESET_REQUIRED) as they are on every utility lane;
// and an *Error with ClassUnsupported for a plugin that does not serve the lane (it answers Unimplemented).
type Client interface {
	// Describe asks what the plugin can run commands in and what it serves. Static. The answer is the wire message,
	// validated: a field the plugin did not send is absent in it, which a consumer reads as "not known", never as
	// false, zero or none (see the file header of proto/exec/v2/exec.proto).
	Describe(ctx context.Context) (*execv2.DescribeResponse, error)
	// Options asks what a user can choose for one resource, with the state of each choice.
	Options(ctx context.Context, ref TargetRef) (*OptionsResult, error)
}
