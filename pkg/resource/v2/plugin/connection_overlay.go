package plugin

import (
	"context"
	"encoding/json"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

// ApplyConnectionOverlay replaces host intent; an empty overlay restores native discovery.
func (c *Client) ApplyConnectionOverlay(ctx context.Context, overlay resourcev2.ConnectionOverlay) error {
	if err := resourcev2.ValidateConnectionOverlay(overlay); err != nil {
		return err
	}
	data, err := json.Marshal(overlay)
	if err != nil {
		return invalid("invalid connection overlay JSON")
	}
	if err = resourcev2.ValidateJSON(data); err != nil {
		return err
	}
	return c.ancillaryJSON(
		ctx,
		&resourcepb.AncillaryRequest{Operation: "applyConnectionOverlay", ConnectionJson: data},
		&struct{}{},
	)
}

func (c *Client) NativeConnections(ctx context.Context) ([]types.Connection, error) {
	var connections []types.Connection
	err := c.ancillaryJSON(ctx, &resourcepb.AncillaryRequest{Operation: "nativeConnections"}, &connections)
	return connections, err
}
