package lifecycle

import (
	"context"

	"google.golang.org/protobuf/types/known/emptypb"

	lifecyclepb "github.com/omniviewdev/plugin-sdk/proto/v1/lifecycle"
)

// Client wraps the generated gRPC client for the PluginLifecycle service.
type Client struct {
	client lifecyclepb.PluginLifecycleClient
}

func (c *Client) GetInfo(ctx context.Context) (*lifecyclepb.GetInfoResponse, error) {
	return c.client.GetInfo(ctx, &emptypb.Empty{})
}

func (c *Client) GetCapabilities(ctx context.Context) (*lifecyclepb.GetCapabilitiesResponse, error) {
	return c.client.GetCapabilities(ctx, &emptypb.Empty{})
}

func (c *Client) HealthCheck(ctx context.Context) (*lifecyclepb.HealthCheckResponse, error) {
	return c.client.HealthCheck(ctx, &emptypb.Empty{})
}
