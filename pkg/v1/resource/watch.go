package resource

import (
	"context"
	"encoding/json"
	"time"
)

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
//
// State Machine:
//
//	                      ┌─────────────────────────────┐
//	                      │                             │
//	  Idle ──► Syncing ──► Synced ──► Error ──► Syncing │  (retry)
//	              │                     │               │
//	              │                     ▼               │
//	              │                   Failed            │  (terminal: max retries)
//	              │                                     │
//	              ▼                                     │
//	           Error ──────────────────────────────────►│
//	              │
//	              ▼
//	           Stopped                                     (terminal: context cancelled)
//
// Transition rules:
//   - Idle → Syncing:   StartConnectionWatch or EnsureResourceWatch is called.
//   - Syncing → Synced: Initial list completed and cache populated. ResourceCount is set.
//   - Syncing → Error:  Initial list or informer setup failed. Error and Message are set.
//   - Synced → Error:   Watch stream returned an error during live operation.
//   - Error → Syncing:  Automatic retry after backoff (if retries remain).
//   - Error → Failed:   Max retry attempts exhausted. Terminal state.
//   - Error → Stopped:  Context cancelled during backoff. Terminal state.
//   - Any → Stopped:    Context cancelled (explicit StopConnectionWatch or shutdown).
//   - Idle → Forbidden: Backend detects 401/403 on initial sync. Terminal — no retry.
//   - Idle → Skipped:   Resource excluded by scope configuration. Terminal.
type WatchState int

const (
	// WatchStateIdle means the watch is registered but not started.
	// Entered: on registration. Exited: when watch startup begins (→ Syncing).
	// Not terminal.
	WatchStateIdle WatchState = iota

	// WatchStateSyncing means the watch is running but initial sync is in progress.
	// Entered: on first startup or retry. Exited: cache sync completes (→ Synced)
	// or fails (→ Error).
	// WatchStateEvent.ResourceCount is not yet meaningful in this state.
	// Not terminal.
	WatchStateSyncing

	// WatchStateSynced means the watch is running and the cache is fully populated.
	// Entered: when cache sync completes successfully.
	// Exited: watch stream error (→ Error) or context cancelled (→ Stopped).
	// WatchStateEvent.ResourceCount is set to the number of resources observed.
	// Not terminal.
	WatchStateSynced

	// WatchStateError means an error occurred during watching.
	// Entered: when the watch stream or initial list returns an error.
	// Exited: automatic retry (→ Syncing), max retries (→ Failed), or
	// context cancelled (→ Stopped).
	// WatchStateEvent.Error and Message are set.
	// Not terminal.
	WatchStateError

	// WatchStateStopped means the watch was explicitly stopped.
	// Entered: when context is cancelled (user-initiated stop or shutdown).
	// Terminal — no further transitions occur.
	WatchStateStopped

	// WatchStateFailed means the watch failed after exhausting retry attempts.
	// Entered: when max retries are exhausted after repeated errors.
	// WatchStateEvent.Error and Message are set.
	// Terminal — requires explicit restart via RestartResourceWatch.
	WatchStateFailed

	// WatchStateForbidden means the watch was denied due to 401/403 permissions.
	// Entered: when the backend detects a permission error (e.g., K8s 403 on List/Watch).
	// Terminal — stable, no auto-retry. User must fix permissions and reconnect.
	WatchStateForbidden

	// WatchStateSkipped means the watch was intentionally not started.
	// Entered: when the resource is excluded by scope configuration or other policy.
	// Terminal — no further transitions occur unless scope changes.
	WatchStateSkipped
)

// AllWatchStates is a list of all watch states. Necessary for Wails
// to bind the enums.
//
//nolint:gochecknoglobals // necessary for enum binding
var AllWatchStates = []struct {
	Value  WatchState
	TSName string
}{
	{WatchStateIdle, "IDLE"},
	{WatchStateSyncing, "SYNCING"},
	{WatchStateSynced, "SYNCED"},
	{WatchStateError, "ERROR"},
	{WatchStateStopped, "STOPPED"},
	{WatchStateFailed, "FAILED"},
	{WatchStateForbidden, "FORBIDDEN"},
	{WatchStateSkipped, "SKIPPED"},
}

// AllSyncPolicies is a list of all sync policies. Necessary for Wails
// to bind the enums.
//
//nolint:gochecknoglobals // necessary for enum binding
var AllSyncPolicies = []struct {
	Value  SyncPolicy
	TSName string
}{
	{SyncOnConnect, "ON_CONNECT"},
	{SyncOnFirstQuery, "ON_FIRST_QUERY"},
	{SyncNever, "NEVER"},
}

// WatchEventSink receives all events from a Watch.
// The SDK provides the implementation — plugin authors never implement this.
//
// Thread safety: All methods are safe for concurrent use. Multiple Watch
// goroutines (one per resource type per connection) may call methods
// simultaneously. The SDK implementation serialises event delivery to the
// engine via an internal channel, so callers do not need external locking.
//
// Ordering: Events are delivered in the order received per resource type.
// No cross-resource-type ordering is guaranteed. Within a single Watch
// goroutine, OnAdd/OnUpdate/OnDelete calls are sequenced by the informer's
// event handler and will arrive in informer order.
//
// OnStateChange calls may interleave with data events. Consumers should
// treat state events as advisory — e.g., a brief ERROR→SYNCING transition
// during a retry does not invalidate previously received data.
type WatchEventSink interface {
	OnAdd(payload WatchAddPayload)
	OnUpdate(payload WatchUpdatePayload)
	OnDelete(payload WatchDeletePayload)
	OnStateChange(event WatchStateEvent)
}

// ResourceMetadata carries structured metadata extracted by the plugin from its
// typed objects. The engine stores this in the registry without parsing raw JSON.
type ResourceMetadata struct {
	UID       string            `json:"uid"`
	Labels    map[string]string `json:"labels,omitempty"`
	CreatedAt *time.Time        `json:"createdAt,omitempty"`
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

	// Metadata contains structured metadata extracted by the plugin.
	Metadata ResourceMetadata `json:"metadata"`
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

	// Metadata contains structured metadata extracted by the plugin.
	Metadata ResourceMetadata `json:"metadata"`
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
	// PluginID identifies the source plugin.
	PluginID string `json:"pluginId"`

	// Connection is the connection ID that produced this event.
	Connection string `json:"connection"`

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

	// ErrorCode is a machine-readable error classification (e.g., "FORBIDDEN", "TIMEOUT").
	// Only set when State is WatchStateError, WatchStateFailed, or WatchStateForbidden.
	ErrorCode string `json:"errorCode,omitempty"`
}

// WatchConnectionSummary provides a snapshot of all watch states for a connection.
type WatchConnectionSummary struct {
	ConnectionID   string                `json:"connectionId"`
	Resources      map[string]WatchState `json:"resources"`
	ResourceCounts map[string]int        `json:"resourceCounts"`
	Scope          *WatchScope           `json:"scope,omitempty"`
}

// WatchScope configures the scope/partitioning of a Watch invocation.
// Passed via context so the Watcher[ClientT] interface doesn't change.
//
// "Partitions" are backend-defined divisions of the resource space.
// K8s: partitions = namespaces. AWS: partitions = regions. GCP: partitions = projects.
type WatchScope struct {
	Partitions []string `json:"partitions,omitempty"` // empty = unscoped
}

type watchScopeKey struct{}

// WithWatchScope attaches a WatchScope to a context.
func WithWatchScope(ctx context.Context, scope *WatchScope) context.Context {
	return context.WithValue(ctx, watchScopeKey{}, scope)
}

// WatchScopeFromContext retrieves the WatchScope from a context, or nil if none.
func WatchScopeFromContext(ctx context.Context) *WatchScope {
	if s, ok := ctx.Value(watchScopeKey{}).(*WatchScope); ok {
		return s
	}
	return nil
}
