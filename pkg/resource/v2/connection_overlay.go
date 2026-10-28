package resourcev2

import (
	"context"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// ConnectionOverlay is the host's explicit desired configuration, not a native
// discovery snapshot. The host owns durable storage. Providers retain this only
// in memory and merge it over subsequent native discovery. Upserts and disabled
// IDs must be disjoint and unique. An empty overlay restores native discovery.
type ConnectionOverlay struct {
	Upserts     []types.Connection `json:"upserts"`
	DisabledIDs []string           `json:"disabledIDs"`
}

// ConnectionOverlayProvider replaces the entire desired overlay transactionally.
// It validates before mutation and cancels/drains clients for changed effective
// routes before returning success. Native configuration files are never changed.
// Hosts can reapply the previous explicit overlay if durable storage fails.
type ConnectionOverlayProvider interface {
	ApplyConnectionOverlay(context.Context, ConnectionOverlay) error
	// NativeConnections returns the latest source discovery snapshot before the
	// desired overlay. It performs no implicit reload and never returns profiles.
	NativeConnections(context.Context) ([]types.Connection, error)
}

func ValidateConnectionOverlay(overlay ConnectionOverlay) error {
	if err := ValidateMetadata(overlay); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, connection := range overlay.Upserts {
		if err := text(connection.ID, true); err != nil {
			return err
		}
		if seen[connection.ID] {
			return invalid("duplicate connection overlay ID")
		}
		seen[connection.ID] = true
	}
	for _, id := range overlay.DisabledIDs {
		if err := text(id, true); err != nil {
			return err
		}
		if seen[id] {
			return invalid("conflicting connection overlay ID")
		}
		seen[id] = true
	}
	return nil
}
