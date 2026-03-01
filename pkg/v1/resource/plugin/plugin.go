package plugin

import (
	"context"

	"google.golang.org/grpc"

	goplugin "github.com/hashicorp/go-plugin"

	resourcepb "github.com/omniviewdev/plugin-sdk/proto/v1/resource"

	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
)

// GRPCPlugin implements hashicorp/go-plugin.GRPCPlugin for the resource plugin.
type GRPCPlugin struct {
	goplugin.Plugin
	Impl resource.Provider
}

// GRPCServer registers the resource plugin server on the given gRPC server.
func (p *GRPCPlugin) GRPCServer(_ *goplugin.GRPCBroker, s *grpc.Server) error {
	resourcepb.RegisterResourcePluginServer(s, NewServer(p.Impl))
	return nil
}

// GRPCClient returns a resource.Provider backed by the given gRPC client connection.
func (p *GRPCPlugin) GRPCClient(_ context.Context, _ *goplugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	return NewClient(resourcepb.NewResourcePluginClient(c)), nil
}
