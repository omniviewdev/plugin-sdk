package resource

import (
	"context"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// ConnectionProvider manages connections and their typed clients.
// All resource plugins must implement this interface.
//
// The SDK's connectionManager calls these methods to manage the full
// connection lifecycle: load from config, create typed clients, check
// health, and retrieve namespaces.
type ConnectionProvider[ClientT any] interface {
	// CreateClient creates a typed client for a connection.
	// ctx carries Session with connection data. Deadline applies to creation.
	CreateClient(ctx context.Context) (*ClientT, error)

	// DestroyClient tears down a client. Called once per CreateClient.
	// Use for cleanup (close sockets, cancel watches, etc.).
	DestroyClient(ctx context.Context, client *ClientT) error

	// LoadConnections returns all connections from plugin configuration.
	// Called on plugin start and when config changes.
	LoadConnections(ctx context.Context) ([]types.Connection, error)

	// CheckConnection verifies a connection is alive and healthy.
	// Returns structured status (CONNECTED, UNAUTHORIZED, TIMEOUT, etc.).
	CheckConnection(ctx context.Context, conn *types.Connection, client *ClientT) (types.ConnectionStatus, error)

	// GetNamespaces returns namespaces for a connection.
	// Return nil if the backend doesn't support namespaces.
	GetNamespaces(ctx context.Context, client *ClientT) ([]string, error)
}

// ConnectionWatcher watches for external connection changes (e.g., kubeconfig
// file modifications). Optional — type-asserted on the ConnectionProvider
// implementation at registration time.
type ConnectionWatcher interface {
	WatchConnections(ctx context.Context) (<-chan []types.Connection, error)
}

// ClientRefresher refreshes credentials without recreating the client.
// Optional — type-asserted on the ConnectionProvider implementation.
type ClientRefresher[ClientT any] interface {
	RefreshClient(ctx context.Context, client *ClientT) error
}

// SchemaProvider provides editor schemas for a connection (e.g., OpenAPI schemas).
// Optional — type-asserted on the ConnectionProvider implementation.
// Connection-level, not per-resource — schemas are fetched once per connection.
type SchemaProvider[ClientT any] interface {
	GetEditorSchemas(ctx context.Context, client *ClientT) ([]EditorSchema, error)
}
