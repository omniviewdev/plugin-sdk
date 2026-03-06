package resource

import (
	"context"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// DiscoveryProvider discovers resource types available for a connection at runtime.
// Optional — without it, the plugin uses statically registered types only.
//
// Note: Discover takes an explicit *types.Connection rather than reading from
// Session context. This is intentional — discovery is a connection-lifecycle
// operation called by the SDK's typeManager during connection setup, not a
// user-initiated request.
type DiscoveryProvider interface {
	// Discover returns resource types available for a connection (e.g., CRDs in K8s).
	// Called when a connection starts and periodically to refresh.
	Discover(ctx context.Context, conn *types.Connection) ([]ResourceMeta, error)

	// OnConnectionRemoved cleans up any cached discovery state for a removed connection.
	OnConnectionRemoved(ctx context.Context, conn *types.Connection) error
}
