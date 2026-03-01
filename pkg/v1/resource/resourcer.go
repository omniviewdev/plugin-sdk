package resource

import (
	"context"
	"encoding/json"
)

// Resourcer handles CRUD for a single resource type (or pattern of types).
// This is the core interface that all resource implementations must satisfy.
//
// Static resourcers (bound to one type at registration) may ignore the
// ResourceMeta parameter. Pattern resourcers (registered with wildcard
// patterns like "*") use it to determine the concrete resource being operated on.
type Resourcer[ClientT any] interface {
	// Get retrieves a single resource by ID (and optionally namespace).
	Get(ctx context.Context, client *ClientT, meta ResourceMeta, input GetInput) (*GetResult, error)

	// List returns all resources matching the input criteria.
	List(ctx context.Context, client *ClientT, meta ResourceMeta, input ListInput) (*ListResult, error)

	// Find searches for resources matching a filter expression or text query.
	Find(ctx context.Context, client *ClientT, meta ResourceMeta, input FindInput) (*FindResult, error)

	// Create creates a new resource.
	Create(ctx context.Context, client *ClientT, meta ResourceMeta, input CreateInput) (*CreateResult, error)

	// Update modifies an existing resource.
	Update(ctx context.Context, client *ClientT, meta ResourceMeta, input UpdateInput) (*UpdateResult, error)

	// Delete removes a resource.
	Delete(ctx context.Context, client *ClientT, meta ResourceMeta, input DeleteInput) (*DeleteResult, error)
}

// Watcher adds real-time event streaming to a Resourcer.
// Optional — type-asserted on Resourcer implementations.
//
// The Watch method MUST:
//   - Block until ctx is cancelled (clean shutdown) or an unrecoverable error occurs
//   - Emit events via the sink as they arrive (OnAdd, OnUpdate, OnDelete)
//   - Emit state changes via the sink (OnStateChange: Syncing -> Synced, Error, etc.)
//   - Return nil on clean shutdown (ctx cancelled), non-nil on failure
//
// The SDK WILL:
//   - Call Watch() in a goroutine — plugin authors never call it themselves
//   - Cancel ctx to stop watching (connection disconnect, resource unsubscribed, etc.)
//   - Restart Watch() on failure (with backoff) or when explicitly requested
//   - Track per-resource running state
//   - Honor SyncPolicyDeclarer to decide WHEN to start watching
type Watcher[ClientT any] interface {
	Watch(ctx context.Context, client *ClientT, meta ResourceMeta, sink WatchEventSink) error
}

// SyncPolicyDeclarer declares the sync policy for this resource type.
// Only meaningful if the Resourcer also implements Watcher.
// If not implemented, defaults to SyncOnConnect.
type SyncPolicyDeclarer interface {
	SyncPolicy() SyncPolicy
}

// ActionResourcer adds named actions beyond CRUD (restart, scale, drain, etc.).
// Optional — type-asserted on Resourcer implementations.
type ActionResourcer[ClientT any] interface {
	// GetActions returns available actions for a resource type.
	GetActions(ctx context.Context, client *ClientT, meta ResourceMeta) ([]ActionDescriptor, error)

	// ExecuteAction executes a named action and returns the result.
	ExecuteAction(ctx context.Context, client *ClientT, meta ResourceMeta, actionID string, input ActionInput) (*ActionResult, error)

	// StreamAction executes a streaming action, sending events on the channel.
	// The channel is closed by the caller when the context is cancelled.
	StreamAction(ctx context.Context, client *ClientT, meta ResourceMeta, actionID string, input ActionInput, stream chan<- ActionEvent) error
}

// DefinitionProvider provides column defs, ID/namespace accessors for a resource type.
// Optional — if not implemented, the SDK uses the Definition from ResourceRegistration,
// falling back to the DefaultDefinition from ResourcePluginConfig.
type DefinitionProvider interface {
	Definition() ResourceDefinition
}

// ErrorClassifier classifies raw errors into structured ResourceOperationErrors.
// Can be implemented on a Resourcer (per-resource classification) or provided
// at the plugin level via ResourcePluginConfig.ErrorClassifier.
type ErrorClassifier interface {
	ClassifyError(err error) error
}

// FilterableProvider declares filter fields available for a resource type.
// Optional — type-asserted on Resourcer implementations.
//
// If NOT implemented, Find() still works but callers cannot discover valid
// filter fields. MCP tool generators omit filter parameters.
//
// connectionID is provided because some backends have different filterable
// fields per connection (e.g., CRD custom columns).
type FilterableProvider interface {
	FilterFields(ctx context.Context, connectionID string) ([]FilterField, error)
}

// TextSearchProvider adds free-text search to a Resourcer.
// Optional — for command palette, AI natural language queries, global search.
// Separate from FilterableProvider (structured predicates vs. text matching).
type TextSearchProvider[ClientT any] interface {
	Search(ctx context.Context, client *ClientT, meta ResourceMeta, query string, limit int) (*FindResult, error)
}

// ResourceSchemaProvider provides raw JSON Schema for a resource type.
// Optional — describes the full resource data structure (all fields, types).
// Distinct from EditorSchema (which wraps schema in Monaco-specific metadata).
// Used by MCP tool generators as outputSchema.
type ResourceSchemaProvider[ClientT any] interface {
	GetResourceSchema(ctx context.Context, client *ClientT, meta ResourceMeta) (json.RawMessage, error)
}

// ScaleHintProvider declares expected cardinality for a resource type.
// Optional — helps AI agents decide list-all vs. paginate vs. filter-first.
type ScaleHintProvider interface {
	ScaleHint() *ScaleHint
}
