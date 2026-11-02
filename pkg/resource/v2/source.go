package resourcev2

import (
	"context"
	"time"
)

const (
	SourceModeSnapshot           = "snapshot"
	SourceModeSnapshotAndUpdates = "snapshotAndUpdates"
	SourceAcquisitionEnumerate   = "enumerate"
	SourceAcquisitionNativeQuery = "nativeQuery"

	EnumerationExhausted   = "exhausted"
	EnumerationPartial     = "partial"
	EnumerationUnknown     = "unknown"
	ConsistencyPointInTime = "pointInTime"
	ConsistencyBestEffort  = "bestEffort"
	ConsistencyUnknown     = "unknown"

	ContinuityNotApplicable = "notApplicable"
	ContinuityGapFree       = "gapFree"
	ContinuityBestEffort    = "bestEffort"

	SourceResetContinuityLost    = "continuityLost"
	SourceResetOverflow          = "overflow"
	SourceResetBindingChanged    = "bindingChanged"
	SourceResetSourceInvalidated = "sourceInvalidated"

	SourceChangeUpsert = "upsert"
	SourceChangeRemove = "remove"

	OperationSourceUpdates Operation = "sourceUpdates"
	FeatureSourceSessions  Feature   = "sourceSessions"

	MaxSourceBatchItems   = MaxPageRecords
	MinSourceFrameBytes   = 64 << 10
	MaxSourceFrameBytes   = MaxMessageBytes
	MaxSourceTokenBytes   = 128
	MaxSourceLimitations  = 16
	MaxSourceMessageBytes = 1024
)

// ResourceMetadata carries optional display metadata outside canonical identity
// and body provenance. Nil CreatedAt means no creation time was supplied.
type ResourceMetadata struct {
	Labels    map[string]string `json:"labels,omitempty"`
	CreatedAt *time.Time        `json:"createdAt,omitempty"`
}

type SourceLimits struct {
	MaxFrameBytes uint32 `json:"maxFrameBytes"`
	MaxBatchItems uint32 `json:"maxBatchItems"`
}

type SourceAcquisition struct {
	Kind     string `json:"kind"`
	Language string `json:"language,omitempty"`
	Query    *Value `json:"query,omitempty"`
}

type SourceOpenRequest struct {
	SessionID   string            `json:"sessionId"`
	Generation  string            `json:"generation"`
	Target      ReadTarget        `json:"target"`
	Mode        string            `json:"mode"`
	Acquisition SourceAcquisition `json:"acquisition"`
	Limits      SourceLimits      `json:"limits"`
}

type SourceCoverage struct {
	Enumeration string   `json:"enumeration"`
	Consistency string   `json:"consistency"`
	Limitations []string `json:"limitations"`
}

type SourceObservation struct {
	Record      Record            `json:"record"`
	Metadata    *ResourceMetadata `json:"metadata,omitempty"`
	DisplayName *string           `json:"displayName,omitempty"`
}

type SourceChange struct {
	Kind           string             `json:"kind"`
	Observation    *SourceObservation `json:"observation,omitempty"`
	Reference      *ResourceReference `json:"reference,omitempty"`
	SourceRevision *string            `json:"sourceRevision,omitempty"`
}

type SourceBegin struct{ Mode string }
type SourceSnapshot struct{ Observations []SourceObservation }
type SourceSnapshotEnd struct {
	Coverage   SourceCoverage
	Continuity string
}
type SourceChanges struct {
	Changes  []SourceChange
	Coverage *SourceCoverage
}
type SourceReset struct{ Reason, Message string }
type SourceFailure struct{ Code, Message string }
type SourceFrame struct {
	SessionID, Generation string
	Sequence              uint64
	Begin                 *SourceBegin
	Snapshot              *SourceSnapshot
	SnapshotEnd           *SourceSnapshotEnd
	Changes               *SourceChanges
	Reset                 *SourceReset
	Failed                *SourceFailure
}

// SourceProvider opens a source session and emits its snapshot and updates.
type SourceProvider interface {
	OpenSource(ctx context.Context, request SourceOpenRequest, emitter SourceEmitter) error
}

// SourceEmitter emits source data while the framework enforces session phases and limits.
type SourceEmitter interface {
	Snapshot(ctx context.Context, observations []SourceObservation) error
	EndSnapshot(ctx context.Context, coverage SourceCoverage, continuity string) error
	Changes(ctx context.Context, changes []SourceChange, coverage *SourceCoverage) error
	Reset(ctx context.Context, reason, message string) error
}

// SourceStreamOpener opens validated host-side source streams.
type SourceStreamOpener interface {
	OpenSourceStream(ctx context.Context, request SourceOpenRequest) (SourceStream, error)
}

type SourceStream interface {
	Recv() (SourceFrame, error)
	Close()
}

type SourceRequest struct {
	Target         ReadTarget
	Representation Representation
}
