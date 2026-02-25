package lifecycle

import (
	"context"

	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/omniviewdev/plugin-sdk/proto"
)

// Client wraps the generated gRPC client for the PluginLifecycle service.
type Client struct {
	client proto.PluginLifecycleClient
}

func (c *Client) GetInfo(ctx context.Context) (*proto.GetInfoResponse, error) {
	return c.client.GetInfo(ctx, &emptypb.Empty{})
}

func (c *Client) GetCapabilities(ctx context.Context) (*proto.GetCapabilitiesResponse, error) {
	return c.client.GetCapabilities(ctx, &emptypb.Empty{})
}

func (c *Client) HealthCheck(ctx context.Context) (*proto.HealthCheckResponse, error) {
	return c.client.HealthCheck(ctx, &emptypb.Empty{})
}
