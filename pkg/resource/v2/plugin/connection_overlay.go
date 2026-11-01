package plugin

import (
	"context"
	"encoding/json"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

const (
	connectionOverlayOperation = "applyConnectionOverlay"
	nativeConnectionsOperation = "nativeConnections"
)

// Private credentials belong only to the host-to-provider overlay request.
// A missing private map inherits credentials; an explicit empty map clears them.
type connectionOverlayWire struct {
	Upserts     []connectionOverlayUpsert `json:"upserts"`
	DisabledIDs []string                  `json:"disabledIDs"`
}

type connectionOverlayUpsert struct {
	types.Connection
	SensitiveData *map[string]any `json:"sensitiveData,omitempty"`
}

// ApplyConnectionOverlay replaces host intent; an empty overlay restores native discovery.
//
//nolint:musttag // Nested types.Connection retains its established JSON field names.
func (c *Client) ApplyConnectionOverlay(ctx context.Context, overlay resourcev2.ConnectionOverlay) error {
	if err := resourcev2.ValidateConnectionOverlay(overlay); err != nil {
		return err
	}
	wire := connectionOverlayWire{
		Upserts: make([]connectionOverlayUpsert, len(overlay.Upserts)), DisabledIDs: overlay.DisabledIDs,
	}
	for i := range overlay.Upserts {
		wire.Upserts[i].Connection = overlay.Upserts[i]
		if private := overlay.Upserts[i].GetSensitiveData(); private != nil {
			wire.Upserts[i].SensitiveData = &private
		}
	}
	if err := resourcev2.ValidateMetadata(wire); err != nil {
		return err
	}
	data, err := json.Marshal(wire)
	if err != nil {
		return invalid("invalid connection overlay JSON")
	}
	if err = resourcev2.ValidateJSON(data); err != nil {
		return err
	}
	return c.ancillaryJSON(
		ctx,
		&resourcepb.AncillaryRequest{Operation: connectionOverlayOperation, ConnectionJson: data},
		&struct{}{},
	)
}

// NativeConnections returns source-owned profiles before host overlays apply.
func (c *Client) NativeConnections(ctx context.Context) ([]types.Connection, error) {
	var connections []types.Connection
	err := c.ancillaryJSON(ctx, &resourcepb.AncillaryRequest{Operation: nativeConnectionsOperation}, &connections)
	return connections, err
}
