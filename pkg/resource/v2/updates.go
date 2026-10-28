package resourcev2

import (
	"context"
	"time"
)

// BestEffortGuarantee labels the existing advisory update workflow. It provides
// no atomic snapshot/update cutover, replay, gap detection, or continuity proof.
// P05 owns stronger SourceSession semantics; a finite page is not a subscription.
const BestEffortGuarantee = "bestEffort"

type SyncPolicy int

const (
	SyncOnConnect SyncPolicy = iota
	SyncOnFirstQuery
	SyncNever
)

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

// Synced reports native cache readiness only, not a point-in-time snapshot or a
// gap-free stream. Counts report observed cache membership, never source totals.
type WatchStateEvent struct {
	Route     AccessRoute             `json:"route"`
	Type      string                  `json:"type"`
	State     WatchState              `json:"state"`
	Count     Count                   `json:"count"`
	Error     *ResourceOperationError `json:"error,omitempty"`
	Message   string                  `json:"message,omitempty"`
	Guarantee string                  `json:"guarantee"`
}
type ResourceMetadata struct {
	Labels    map[string]string `json:"labels,omitempty"`
	CreatedAt *time.Time        `json:"createdAt,omitempty"`
}
type WatchAddPayload struct {
	Route     AccessRoute      `json:"route"`
	Record    Record           `json:"record"`
	Metadata  ResourceMetadata `json:"metadata"`
	Guarantee string           `json:"guarantee"`
}
type WatchUpdatePayload struct {
	Route     AccessRoute      `json:"route"`
	Record    Record           `json:"record"`
	Metadata  ResourceMetadata `json:"metadata"`
	Guarantee string           `json:"guarantee"`
}
type WatchDeletePayload struct {
	Route     AccessRoute       `json:"route"`
	Reference ResourceReference `json:"reference"`
	LastKnown *Record           `json:"lastKnown,omitempty"`
	Guarantee string            `json:"guarantee"`
}
type WatchEventSink interface {
	OnAdd(WatchAddPayload)
	OnUpdate(WatchUpdatePayload)
	OnDelete(WatchDeletePayload)
	OnStateChange(WatchStateEvent)
}
type WatchConnectionSummary struct {
	Route          AccessRoute           `json:"route"`
	Resources      map[string]WatchState `json:"resources"`
	ResourceCounts map[string]Count      `json:"resourceCounts"`
	Scope          QueryScope            `json:"scope"`
	Guarantee      string                `json:"guarantee"`
}

// BestEffortUpdateProvider ports the existing optional informer lifecycle, with
// canonical records and explicit advisory guarantees. It is not P05's source
// continuity protocol. Registration readiness precedes starting native sources;
// ListenForEvents blocks until cancellation and retires its sink before return.
type BestEffortUpdateProvider interface {
	// StartConnectionWatch receives the explicit host-admitted targets. This
	// prevents providers from inventing authority while auto-starting native work.
	StartConnectionWatch(context.Context, []ReadTarget) error
	StopConnectionWatch(context.Context, AccessRoute) error
	HasWatch(context.Context, AccessRoute) bool
	GetWatchState(context.Context, AccessRoute) (*WatchConnectionSummary, error)
	ListenForEvents(context.Context, WatchEventSink, func() error) error
	EnsureResourceWatch(context.Context, ReadTarget) error
	StopResourceWatch(context.Context, AccessRoute, string) error
	RestartResourceWatch(context.Context, ReadTarget) error
	IsResourceWatchRunning(context.Context, AccessRoute, string) (bool, error)
}

var AllWatchStates = []struct {
	Value  WatchState
	TSName string
}{
	{WatchStateIdle, "IDLE"}, {WatchStateSyncing, "SYNCING"}, {WatchStateSynced, "SYNCED"}, {WatchStateError, "ERROR"}, {WatchStateStopped, "STOPPED"}, {WatchStateFailed, "FAILED"}, {WatchStateForbidden, "FORBIDDEN"}, {WatchStateSkipped, "SKIPPED"},
}
var AllSyncPolicies = []struct {
	Value  SyncPolicy
	TSName string
}{
	{SyncOnConnect, "ON_CONNECT"}, {SyncOnFirstQuery, "ON_FIRST_QUERY"}, {SyncNever, "NEVER"},
}
