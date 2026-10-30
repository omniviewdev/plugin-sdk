package resourcev2

import (
	"context"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// ConnectionOverlay is the host's explicit desired configuration, not a native
// discovery snapshot. The host owns durable storage. Providers retain this only
// in memory and merge it over subsequent native discovery. Upserts and disabled
// IDs must be disjoint and unique. An empty overlay restores native discovery.
// Nil and empty slices both contribute no entries. A zero overlay removes all
// overrides and tombstones. Nil private upsert data may inherit source credentials
// for unchanged public client configuration; an explicit empty map clears them.
// Hosts must not persist source-owned private credentials.
type ConnectionOverlay struct {
	Upserts     []types.Connection `json:"upserts"`
	DisabledIDs []string           `json:"disabledIDs"`
}

// ConnectionOverlayProvider replaces the entire desired overlay transactionally.
// It validates before mutation and cancels/drains clients for changed effective
// routes before returning success. Native configuration files are never changed.
// Hosts can reapply the previous explicit overlay if durable storage fails.
// Failure preserves prior desired state, not clients already retired while
// draining. Reapplying a prior overlay cannot restore a retired generation.
// Calls may serialize inventory mutation; do not synchronously reenter mutation
// from teardown callbacks or affected leased work.
type ConnectionOverlayProvider interface {
	ApplyConnectionOverlay(context.Context, ConnectionOverlay) error
	// NativeConnections returns independent latest native-discovery snapshots
	// before the overlay. It does not reload discovery or include manual profiles.
	NativeConnections(context.Context) ([]types.Connection, error)
}

// ValidateConnectionOverlay checks metadata bounds and unique, nonempty IDs across
// both entry sets. It accepts a zero overlay and does not mutate or retain inputs.
// Success does not verify credentials, native connectivity, or access authority.
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
