package networker

import (
	"context"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// ForwarderResult carries the outcome of starting a port forward. The Ready
// channel is closed once the tunnel is established. The ErrCh channel receives
// a fatal error or is closed on clean exit. The manager monitors ErrCh to
// detect post-start failures.
type ForwarderResult struct {
	SessionID string
	Ready     <-chan struct{} // closed when tunnel established
	ErrCh     <-chan error    // receives fatal error; closed on clean exit
}

// ResourceForwarder initiates a port forward for a resource.
type ResourceForwarder interface {
	ForwardResource(ctx context.Context, pctx *types.PluginContext, opts ResourcePortForwardHandlerOpts) (*ForwarderResult, error)
}

// StaticForwarder initiates a port forward for a static address.
type StaticForwarder interface {
	ForwardStatic(ctx context.Context, pctx *types.PluginContext, opts StaticPortForwardHandlerOpts) (*ForwarderResult, error)
}

// ResourceForwarderFunc adapts a plain function to the ResourceForwarder interface.
type ResourceForwarderFunc func(ctx context.Context, pctx *types.PluginContext, opts ResourcePortForwardHandlerOpts) (*ForwarderResult, error)

func (f ResourceForwarderFunc) ForwardResource(ctx context.Context, pctx *types.PluginContext, opts ResourcePortForwardHandlerOpts) (*ForwarderResult, error) {
	return f(ctx, pctx, opts)
}

// StaticForwarderFunc adapts a plain function to the StaticForwarder interface.
type StaticForwarderFunc func(ctx context.Context, pctx *types.PluginContext, opts StaticPortForwardHandlerOpts) (*ForwarderResult, error)

func (f StaticForwarderFunc) ForwardStatic(ctx context.Context, pctx *types.PluginContext, opts StaticPortForwardHandlerOpts) (*ForwarderResult, error) {
	return f(ctx, pctx, opts)
}
