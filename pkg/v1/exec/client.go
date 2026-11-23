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
// and an *Error with ErrorClassUnsupported for a plugin that does not serve the lane (it answers Unimplemented).
type Client interface {
	// Describe asks what the plugin can run commands in and what it serves. Static. The answer is the wire message,
	// validated: a field the plugin did not send is absent in it, which a consumer reads as "not known", never as
	// false, zero or none (see the file header of proto/exec/v2/exec.proto).
	Describe(ctx context.Context) (*execv2.DescribeResponse, error)
	// Options asks what a user can choose for one resource, with the state of each choice.
	Options(ctx context.Context, ref TargetRef) (*OptionsResult, error)
	// Open starts an interactive session in a target: req is the first frame of an Open stream (a start). It returns
	// once the plugin has accepted the request and sent Opened, with the stream open; the session lives as long as the
	// stream does (this build keeps no session past its stream). A refusal is an error and means nothing ran: an *Error
	// for a refusal of the exec lane, the resource lane's own error for the connection and the catalog pin, and a typed
	// class of the transport's own when the call broke before the plugin answered. Cancelling ctx ends the stream and,
	// with it, the session. A call whose ctx has no deadline waits for Opened at most as long as the SDK's call timeout.
	// The request is sent as it is: the plugin refuses what it does not accept, and a host that wants to know before it
	// asks reads Describe.
	Open(ctx context.Context, req *execv2.OpenRequest) (Conn, error)
}

// Conn is one Open stream as the host sees it: the frames of the plugin in order, and the frames the host sends. It is
// the wire's own message, ClientFrame and ServerFrame, because a consumer's job is to read them exactly (the readers of
// the contract's optional facts live with the first consumer).
//
// One goroutine may call Send while another calls Recv; neither may be called from two goroutines at once. A frame
// kind a reader does not know arrives as a ServerFrame whose Frame is nil, and the consumer ignores it.
type Conn interface {
	// Opened returns the first frame of the stream, which Open received before it returned: the facts the SDK knew
	// before the author ran.
	Opened() *execv2.Opened
	// Recv returns the next frame the plugin sent, in order. Opened is not repeated. The last frame of a session is
	// Ended, after which Recv returns io.EOF: the stream ended with status OK and nothing follows. A stream that broke
	// before its last frame returns an error of the transport's own (a class, never an end): what became of the command
	// is not known from it.
	Recv() (*execv2.ServerFrame, error)
	// Send sends one frame to the plugin. Frames are delivered in the order they were sent. It blocks while the plugin
	// does not take frames (its input queue is full and the stream's window is closed): that is the backpressure. It
	// returns io.EOF once the stream is over; Recv then has the reason.
	Send(frame *execv2.ClientFrame) error
	// Close ends the stream from the host's side and releases it. In this build the session ends with its stream. It may
	// be called more than once and from any goroutine; it does not wait for the plugin.
	Close() error
}
