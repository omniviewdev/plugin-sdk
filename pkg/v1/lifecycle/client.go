package lifecycle

import (
	"context"
	"errors"

	"google.golang.org/protobuf/types/known/emptypb"

	lifecyclepb "github.com/omniviewdev/plugin-sdk/proto/v1/lifecycle"
)

// Client wraps the generated gRPC client for the PluginLifecycle service.
type Client struct {
	client lifecyclepb.PluginLifecycleClient
}

// NewClient creates a properly initialized lifecycle Client.
func NewClient(c lifecyclepb.PluginLifecycleClient) *Client {
	return &Client{client: c}
}

func (c *Client) GetInfo(ctx context.Context) (*lifecyclepb.GetInfoResponse, error) {
	if c == nil || c.client == nil {
		return nil, errors.New("lifecycle client not initialized")
	}
	return c.client.GetInfo(ctx, &emptypb.Empty{})
}

func (c *Client) GetCapabilities(ctx context.Context) (*lifecyclepb.GetCapabilitiesResponse, error) {
	if c == nil || c.client == nil {
		return nil, errors.New("lifecycle client not initialized")
	}
	return c.client.GetCapabilities(ctx, &emptypb.Empty{})
}

func (c *Client) HealthCheck(ctx context.Context) (*lifecyclepb.HealthCheckResponse, error) {
	if c == nil || c.client == nil {
		return nil, errors.New("lifecycle client not initialized")
	}
	return c.client.HealthCheck(ctx, &emptypb.Empty{})
}
