package resourcev2

import (
	"context"
	"time"
)

// BestEffortGuarantee labels the existing advisory update workflow. It provides
// no atomic snapshot/update cutover, replay, gap detection, or continuity proof.
// P05 owns stronger SourceSession semantics; a finite page is not a subscription.
const BestEffortGuarantee = "bestEffort"

// SyncPolicy selects controller advisory-source startup, independently of finite
// read support. The zero policy is SyncOnConnect, not disabled updates.
type SyncPolicy int

const (
	// SyncOnConnect starts only the explicit targets passed to StartConnectionWatch.
	SyncOnConnect SyncPolicy = iota
	// SyncOnFirstQuery starts eligible sources on finite read demand.
	SyncOnFirstQuery
	// SyncNever disables explicit EnsureResourceWatch as well as automatic startup.
	SyncNever
)

// WatchState describes one advisory source's lifecycle, not connection health or
// source completeness. In particular, synced cannot prove gap-free delivery.
type WatchState int

const (
	WatchStateIdle WatchState = iota
	WatchStateSyncing
	WatchStateSynced
	WatchStateError
	WatchStateStopped
	WatchStateFailed
	WatchStateForbidden
	WatchStateSkipped
)

// WatchStateEvent reports advisory source state. WatchStateSynced means native
// cache readiness only, not a point-in-time snapshot or a gap-free stream.
// Count describes observed cache membership, never total native cardinality.
// Message and Error must contain public-safe text, not raw native diagnostics.
type WatchStateEvent struct {
	Route     AccessRoute             `json:"route"`
	Type      string                  `json:"type"`
	State     WatchState              `json:"state"`
	Count     Count                   `json:"count"`
	Error     *ResourceOperationError `json:"error,omitempty"`
	Message   string                  `json:"message,omitempty"`
	Guarantee string                  `json:"guarantee"`
}

// ResourceMetadata carries optional display metadata outside canonical identity
// and body provenance. Nil CreatedAt means no creation time was supplied.
type ResourceMetadata struct {
	Labels    map[string]string `json:"labels,omitempty"`
	CreatedAt *time.Time        `json:"createdAt,omitempty"`
}

// WatchAddPayload reports an advisory observed addition, not proof that the object
// did not previously exist. Record retains the admitted identity and representation.
type WatchAddPayload struct {
	Route     AccessRoute      `json:"route"`
	Record    Record           `json:"record"`
	Metadata  ResourceMetadata `json:"metadata"`
	Guarantee string           `json:"guarantee"`
}

// WatchUpdatePayload carries a full canonical record, not an implicit patch.
// Missing earlier events cannot be detected or recovered from this payload alone.
type WatchUpdatePayload struct {
	Route     AccessRoute      `json:"route"`
	Record    Record           `json:"record"`
	Metadata  ResourceMetadata `json:"metadata"`
	Guarantee string           `json:"guarantee"`
}

// WatchDeletePayload identifies the observed deletion without requiring a body.
// Nil LastKnown means no prior record is available; it is not an empty record.
// When supplied, LastKnown must agree with Reference and admitted representation.
type WatchDeletePayload struct {
	Route     AccessRoute       `json:"route"`
	Reference ResourceReference `json:"reference"`
	LastKnown *Record           `json:"lastKnown,omitempty"`
	Guarantee string            `json:"guarantee"`
}

// WatchEventSink receives borrowed advisory payloads. Callbacks may overlap;
// synchronize sink state and treat nested payload containers as read-only.
// Copy mutable data before retaining or editing it. Blocking callbacks delay
// source delivery and listener retirement; no callback may outlive listener return.
type WatchEventSink interface {
	OnAdd(WatchAddPayload)
	OnUpdate(WatchUpdatePayload)
	OnDelete(WatchDeletePayload)
	OnStateChange(WatchStateEvent)
}

// WatchConnectionSummary is a source-state observation, not a total inventory.
// Resources and ResourceCounts are keyed by opaque resource type; unknown counts
// must not be treated as zero or inferred from the number of map entries.
type WatchConnectionSummary struct {
	Route          AccessRoute           `json:"route"`
	Resources      map[string]WatchState `json:"resources"`
	ResourceCounts map[string]Count      `json:"resourceCounts"`
	// Scope is optional summary metadata, not permission to widen individual
	// admitted targets. Nil does not establish unrestricted acquisition.
	Scope     QueryScope `json:"scope"`
	Guarantee string     `json:"guarantee"`
}

// BestEffortUpdateProvider ports the existing optional informer lifecycle, with
// canonical records and explicit advisory guarantees. It is not P05's source
// continuity protocol. Registration readiness precedes starting native sources;
// ListenForEvents blocks until cancellation and retires its sink before return.
// Listener cancellation retires only that listener; connection generation
// retirement owns native source cancellation. Implementations must observe
// cancellation and keep callbacks within the listener lifetime.
type BestEffortUpdateProvider interface {
	// StartConnectionWatch receives the explicit host-admitted targets. This
	// prevents providers from inventing authority while auto-starting native work.
	StartConnectionWatch(context.Context, []ReadTarget) error
	// StopConnectionWatch cancels and joins sources on the explicit route.
	StopConnectionWatch(context.Context, AccessRoute) error
	// HasWatch is a lossy presence check; false cannot distinguish absence from
	// cancellation, stale admission, or an unavailable connection.
	HasWatch(context.Context, AccessRoute) bool
	GetWatchState(context.Context, AccessRoute) (*WatchConnectionSummary, error)
	// ListenForEvents requires a nonnil sink. A nil onReady skips that hook.
	// Run onReady before data delivery; its error rejects registration. Advisory
	// data arriving before readiness is dropped, never queued for replay.
	ListenForEvents(context.Context, WatchEventSink, func() error) error
	// EnsureResourceWatch retains its admitted target independently of caller
	// containers. Success does not claim a complete native cache or source total.
	EnsureResourceWatch(context.Context, ReadTarget) error
	StopResourceWatch(context.Context, AccessRoute, string) error
	RestartResourceWatch(context.Context, ReadTarget) error
	IsResourceWatchRunning(context.Context, AccessRoute, string) (bool, error)
}

// AllWatchStates maps lifecycle values to UI names. Treat this shared table as
// read-only; its order and size do not describe any source's current state.
var AllWatchStates = []struct {
	Value  WatchState
	TSName string
}{
	{
		WatchStateIdle,
		"IDLE",
	},
	{WatchStateSyncing, "SYNCING"},
	{WatchStateSynced, "SYNCED"},
	{WatchStateError, "ERROR"},
	{WatchStateStopped, "STOPPED"},
	{WatchStateFailed, "FAILED"},
	{WatchStateForbidden, "FORBIDDEN"},
	{WatchStateSkipped, "SKIPPED"},
}

// AllSyncPolicies maps startup policies to UI names. Treat this shared table as
// read-only; policy selection does not advertise a catalog capability.
var AllSyncPolicies = []struct {
	Value  SyncPolicy
	TSName string
}{
	{SyncOnConnect, "ON_CONNECT"}, {SyncOnFirstQuery, "ON_FIRST_QUERY"}, {SyncNever, "NEVER"},
}
