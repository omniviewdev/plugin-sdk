package resource

import "encoding/json"

// SyncPolicy controls when watches start for a resource type.
type SyncPolicy int

const (
	// SyncOnConnect starts watching as soon as a connection is established.
	SyncOnConnect SyncPolicy = iota

	// SyncOnFirstQuery starts watching on the first List/Get call.
	SyncOnFirstQuery

	// SyncNever disables watching entirely (CRUD-only).
	SyncNever
)

// WatchState represents the current state of a watch.
type WatchState int

const (
	// WatchStateIdle means the watch is registered but not started.
	WatchStateIdle WatchState = iota

	// WatchStateSyncing means the watch is running but initial sync is in progress.
	WatchStateSyncing

	// WatchStateSynced means the watch is running and the cache is fully populated.
	WatchStateSynced

	// WatchStateError means an error occurred during watching.
	WatchStateError

	// WatchStateStopped means the watch was explicitly stopped.
	WatchStateStopped

	// WatchStateFailed means the watch failed after exhausting retry attempts.
	WatchStateFailed
)

// WatchEventSink receives all events from a Watch.
// The SDK provides the implementation — plugin authors never implement this.
// Thread-safe: multiple Watch goroutines may call methods concurrently.
type WatchEventSink interface {
	OnAdd(payload WatchAddPayload)
	OnUpdate(payload WatchUpdatePayload)
	OnDelete(payload WatchDeletePayload)
	OnStateChange(event WatchStateEvent)
}

// WatchAddPayload represents a resource addition event.
type WatchAddPayload struct {
	// Data is the resource data as pre-serialized JSON.
	Data json.RawMessage `json:"data"`

	// PluginID identifies the source plugin.
	PluginID string `json:"pluginId"`

	// Key is the resource type key (e.g., "core::v1::Pod").
	Key string `json:"key"`

	// Connection is the connection ID that produced this event.
	Connection string `json:"connection"`

	// ID is the resource instance identifier.
	ID string `json:"id"`

	// Namespace is the resource namespace (empty for cluster-scoped resources).
	Namespace string `json:"namespace"`
}

// WatchUpdatePayload represents a resource update event.
// Carries only the new state — OldData is dropped (frontend doesn't use it).
type WatchUpdatePayload struct {
	// Data is the new resource state as pre-serialized JSON.
	Data json.RawMessage `json:"data"`

	// PluginID identifies the source plugin.
	PluginID string `json:"pluginId"`

	// Key is the resource type key.
	Key string `json:"key"`

	// Connection is the connection ID that produced this event.
	Connection string `json:"connection"`

	// ID is the resource instance identifier.
	ID string `json:"id"`

	// Namespace is the resource namespace.
	Namespace string `json:"namespace"`
}

// WatchDeletePayload represents a resource deletion event.
type WatchDeletePayload struct {
	// Data is the deleted resource's last-known state as pre-serialized JSON.
	Data json.RawMessage `json:"data"`

	// PluginID identifies the source plugin.
	PluginID string `json:"pluginId"`

	// Key is the resource type key.
	Key string `json:"key"`

	// Connection is the connection ID that produced this event.
	Connection string `json:"connection"`

	// ID is the resource instance identifier.
	ID string `json:"id"`

	// Namespace is the resource namespace.
	Namespace string `json:"namespace"`
}

// WatchStateEvent represents a state change in a watch.
type WatchStateEvent struct {
	// ResourceKey is the resource type key (e.g., "core::v1::Pod").
	ResourceKey string `json:"resourceKey"`

	// State is the new watch state.
	State WatchState `json:"state"`

	// Error is the error that caused the state change (if any).
	// Only set when State is WatchStateError or WatchStateFailed.
	Error error `json:"-"`

	// Message is a human-readable description of the state change.
	Message string `json:"message,omitempty"`

	// ResourceCount is the number of resources observed (if known).
	ResourceCount int `json:"resourceCount,omitempty"`
}

// WatchConnectionSummary provides a snapshot of all watch states for a connection.
type WatchConnectionSummary struct {
	ConnectionID string                `json:"connectionId"`
	Resources    map[string]WatchState `json:"resources"`
}
