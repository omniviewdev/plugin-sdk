package command

import (
	"context"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	commandpb "github.com/omniviewdev/plugin-sdk/proto/v1/command"
)

// PluginClient forwards commands over a shared gRPC client connection.
type PluginClient struct {
	client commandpb.CommandClient
}

var _ Provider = (*PluginClient)(nil)

// ============================== GENERAL COMMANDER ============================== //

// Call forwards a command with request metadata. RPC lifetime uses a background
// context; cancellation in the supplied PluginContext does not cancel this legacy transport.
func (p *PluginClient) Call(
	ctx *types.PluginContext,
	path string,
	body []byte,
) ([]byte, error) {
	req := &commandpb.CallCommandRequest{
		Path: path,
		Body: body,
	}

	resp, err := p.client.Call(types.WithPluginContext(context.Background(), ctx), req)
	if err != nil {
		return nil, err
	}

	return resp.Body, nil
}
