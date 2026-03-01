package resource

import (
	"context"
	"encoding/json"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// Provider is the composite interface that crosses the gRPC boundary.
// The SDK's resourceController[ClientT] satisfies this on the plugin side.
// The engine's gRPC client stub also satisfies this on the engine side.
type Provider interface {
	OperationProvider
	ConnectionLifecycleProvider
	WatchProvider
	TypeProvider
	ActionProvider
	EditorSchemaProvider
	RelationshipProvider
	HealthProvider
}

// RelationshipProvider provides resource relationship information across the gRPC boundary.
type RelationshipProvider interface {
	// GetRelationships returns the declared relationship descriptors for a resource type.
	GetRelationships(ctx context.Context, resourceKey string) ([]RelationshipDescriptor, error)

	// ResolveRelationships resolves actual relationship instances for a specific resource.
	ResolveRelationships(ctx context.Context, connectionID string, resourceKey string, id string, namespace string) ([]ResolvedRelationship, error)
}

// HealthProvider provides resource health assessment across the gRPC boundary.
type HealthProvider interface {
	// GetHealth assesses health for a resource from its raw data.
	GetHealth(ctx context.Context, connectionID string, resourceKey string, data json.RawMessage) (*ResourceHealth, error)

	// GetResourceEvents returns diagnostic events for a resource instance.
	GetResourceEvents(ctx context.Context, connectionID string, resourceKey string, id string, namespace string, limit int32) ([]ResourceEvent, error)
}

// OperationProvider handles CRUD operations on resources across the gRPC boundary.
// The key parameter is the resource type key (e.g., "core::v1::Pod").
type OperationProvider interface {
	Get(ctx context.Context, key string, input GetInput) (*GetResult, error)
	List(ctx context.Context, key string, input ListInput) (*ListResult, error)
	Find(ctx context.Context, key string, input FindInput) (*FindResult, error)
	Create(ctx context.Context, key string, input CreateInput) (*CreateResult, error)
	Update(ctx context.Context, key string, input UpdateInput) (*UpdateResult, error)
	Delete(ctx context.Context, key string, input DeleteInput) (*DeleteResult, error)
}

// ConnectionLifecycleProvider manages connection lifecycle over the gRPC boundary.
// Named differently from the plugin-author ConnectionProvider[ClientT] to avoid confusion.
type ConnectionLifecycleProvider interface {
	// StartConnection starts a connection and returns its status.
	StartConnection(ctx context.Context, connectionID string) (types.ConnectionStatus, error)

	// StopConnection stops a connection and returns its final state.
	StopConnection(ctx context.Context, connectionID string) (types.Connection, error)

	// LoadConnections reads connections from plugin configuration (e.g., kubeconfig files).
	// Called on plugin start and when config changes.
	LoadConnections(ctx context.Context) ([]types.Connection, error)

	// ListConnections returns the current runtime state of all managed connections.
	ListConnections(ctx context.Context) ([]types.Connection, error)

	// GetConnection returns a single connection by ID.
	GetConnection(ctx context.Context, id string) (types.Connection, error)

	// GetConnectionNamespaces returns available namespaces for a connection.
	GetConnectionNamespaces(ctx context.Context, id string) ([]string, error)

	// UpdateConnection updates a connection's configuration.
	UpdateConnection(ctx context.Context, connection types.Connection) (types.Connection, error)

	// DeleteConnection removes a connection.
	DeleteConnection(ctx context.Context, id string) error

	// WatchConnections watches for external connection changes.
	// Blocks until ctx is cancelled, sending updates on the stream channel.
	WatchConnections(ctx context.Context, stream chan<- []types.Connection) error
}

// WatchProvider manages watch lifecycle and event streaming over the gRPC boundary.
type WatchProvider interface {
	// StartConnectionWatch starts all watches for a connection.
	StartConnectionWatch(ctx context.Context, connectionID string) error

	// StopConnectionWatch stops all watches for a connection.
	StopConnectionWatch(ctx context.Context, connectionID string) error

	// HasWatch returns whether any watches are active for a connection.
	HasWatch(ctx context.Context, connectionID string) bool

	// GetWatchState returns a snapshot of all watch states for a connection.
	GetWatchState(ctx context.Context, connectionID string) (*WatchConnectionSummary, error)

	// ListenForEvents opens a long-lived event stream.
	// Blocks until ctx is cancelled, delivering events via the sink.
	ListenForEvents(ctx context.Context, sink WatchEventSink) error

	// EnsureResourceWatch ensures a watch is running for a specific resource type.
	// No-op if already running.
	EnsureResourceWatch(ctx context.Context, connectionID string, resourceKey string) error

	// StopResourceWatch stops the watch for a specific resource type.
	StopResourceWatch(ctx context.Context, connectionID string, resourceKey string) error

	// RestartResourceWatch restarts the watch for a specific resource type.
	RestartResourceWatch(ctx context.Context, connectionID string, resourceKey string) error

	// IsResourceWatchRunning returns whether a watch is running for a specific resource type.
	IsResourceWatchRunning(ctx context.Context, connectionID string, resourceKey string) (bool, error)
}

// TypeProvider provides resource type metadata and introspection across the gRPC boundary.
type TypeProvider interface {
	// GetResourceGroups returns all resource groups for a connection.
	GetResourceGroups(ctx context.Context, connectionID string) map[string]ResourceGroup

	// GetResourceGroup returns a single resource group by ID.
	GetResourceGroup(ctx context.Context, id string) (ResourceGroup, error)

	// GetResourceTypes returns all resource types for a connection.
	GetResourceTypes(ctx context.Context, connectionID string) map[string]ResourceMeta

	// GetResourceType returns a single resource type by key.
	GetResourceType(ctx context.Context, id string) (*ResourceMeta, error)

	// HasResourceType checks whether a resource type exists.
	HasResourceType(ctx context.Context, id string) bool

	// GetResourceDefinition returns the table rendering definition for a resource type.
	GetResourceDefinition(ctx context.Context, id string) (ResourceDefinition, error)

	// GetResourceCapabilities returns the auto-derived capabilities for a resource type.
	GetResourceCapabilities(ctx context.Context, resourceKey string) (*ResourceCapabilities, error)

	// GetResourceSchema returns the raw JSON Schema for a resource type.
	GetResourceSchema(ctx context.Context, connectionID string, resourceKey string) (json.RawMessage, error)

	// GetFilterFields returns the declared filter fields for a resource type.
	GetFilterFields(ctx context.Context, connectionID string, resourceKey string) ([]FilterField, error)
}

// ActionProvider handles resource actions across the gRPC boundary.
type ActionProvider interface {
	// GetActions returns available actions for a resource type.
	GetActions(ctx context.Context, key string) ([]ActionDescriptor, error)

	// ExecuteAction executes a named action.
	ExecuteAction(ctx context.Context, key string, actionID string, input ActionInput) (*ActionResult, error)

	// StreamAction executes a streaming action, sending progress events on the channel.
	StreamAction(ctx context.Context, key string, actionID string, input ActionInput, stream chan<- ActionEvent) error
}

// EditorSchemaProvider provides editor schemas across the gRPC boundary.
type EditorSchemaProvider interface {
	// GetEditorSchemas returns editor schemas for a connection.
	GetEditorSchemas(ctx context.Context, connectionID string) ([]EditorSchema, error)
}
