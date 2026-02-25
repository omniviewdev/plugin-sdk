package lifecycle

import (
	"context"

	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	"github.com/omniviewdev/plugin-sdk/proto"
)

// Plugin implements the go-plugin.Plugin interface for the lifecycle service.
// On the server side it registers the Server implementation.
// On the client side it returns a Client that wraps the gRPC stub.
type Plugin struct {
	plugin.Plugin
	// Impl is the concrete server implementation (set on plugin side).
	Impl *Server
}

func (p *Plugin) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	proto.RegisterPluginLifecycleServer(s, p.Impl)
	return nil
}

func (p *Plugin) GRPCClient(
	_ context.Context,
	_ *plugin.GRPCBroker,
	c *grpc.ClientConn,
) (interface{}, error) {
	return &Client{client: proto.NewPluginLifecycleClient(c)}, nil
}
