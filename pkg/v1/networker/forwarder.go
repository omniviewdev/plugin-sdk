package networker

import (
	"context"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// ForwarderResult carries the outcome of starting a port forward. The Ready
// channel is closed once the tunnel is established. The ErrCh channel receives
// a fatal error or is closed on clean exit. The manager monitors ErrCh to
// detect post-start failures.
//
// LocalHost is the host the forwarder's listener is bound to, as the forwarder
// states it: an IP address ("127.0.0.1", "::1", "0.0.0.0", "::"; an IPv6
// address may carry a zone of letters, digits, '.', '_' and '-') or "localhost"
// in any case. A forwarder that opens its own listeners reads it from their
// addresses with LocalHostOf; one that hands listening to a library states the
// address it asked the library to bind. Empty means the forwarder does not say.
// The manager copies it into the session's local_host and never fills it in
// itself: the port it chose before starting the forwarder says nothing about
// where the forwarder listens. A value that cannot be stated (not UTF-8, longer
// than 255 bytes, a zone of other bytes, anything that is not an IP address or
// "localhost") is dropped, the session states none, and the log says how long
// the value was and which of these it was, never the value.
//
// A consumer takes the forward as reachable only from this computer if and only
// if the host is not empty and is "localhost" (ASCII, any case) or an IP
// address that is loopback once an IPv4-mapped IPv6 address is unmapped:
// 127.0.0.0/8, ::1 or ::ffff:127.x.y.z. Any other host (0.0.0.0 and :: for
// every interface, a LAN or a link-local address) is reachable from other
// computers, and empty is unknown and never loopback.
type ForwarderResult struct {
	SessionID string
	LocalHost string
	Ready     <-chan struct{} // closed when tunnel established
	ErrCh     <-chan error    // receives fatal error; closed on clean exit
}

// ResourceForwarder initiates a port forward for a resource.
type ResourceForwarder interface {
	ForwardResource(
		ctx context.Context,
		pctx *types.PluginContext,
		opts ResourcePortForwardHandlerOpts,
	) (*ForwarderResult, error)
}

// StaticForwarder initiates a port forward for a static address.
type StaticForwarder interface {
	ForwardStatic(
		ctx context.Context,
		pctx *types.PluginContext,
		opts StaticPortForwardHandlerOpts,
	) (*ForwarderResult, error)
}

// ResourceForwarderFunc adapts a plain function to the ResourceForwarder interface.
type ResourceForwarderFunc func(ctx context.Context, pctx *types.PluginContext, opts ResourcePortForwardHandlerOpts) (*ForwarderResult, error)

// ForwardResource calls f with the resource forwarding request.
func (f ResourceForwarderFunc) ForwardResource(
	ctx context.Context,
	pctx *types.PluginContext,
	opts ResourcePortForwardHandlerOpts,
) (*ForwarderResult, error) {
	return f(ctx, pctx, opts)
}

// StaticForwarderFunc adapts a plain function to the StaticForwarder interface.
type StaticForwarderFunc func(ctx context.Context, pctx *types.PluginContext, opts StaticPortForwardHandlerOpts) (*ForwarderResult, error)

// ForwardStatic calls f with the static forwarding request.
func (f StaticForwarderFunc) ForwardStatic(
	ctx context.Context,
	pctx *types.PluginContext,
	opts StaticPortForwardHandlerOpts,
) (*ForwarderResult, error) {
	return f(ctx, pctx, opts)
}
