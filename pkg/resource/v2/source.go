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

	MaxSourceBatchItems   = 256
	MinSourceFrameBytes   = 1
	MaxSourceFrameBytes   = 4 << 20
	MaxSourceTokenBytes   = 128
	MaxSourceLimitations  = 256
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
type RepresentationBinding struct {
	Representation Representation `json:"representation"`
}

type FieldPathSegment struct {
	Member *string `json:"member,omitempty"`
	Index  *uint32 `json:"index,omitempty"`
}

type DocumentFieldPath struct {
	Segments []FieldPathSegment `json:"segments"`
}

type DocumentCoverage struct {
	KnownAbsent []DocumentFieldPath `json:"knownAbsent"`
}

type SourceRecord struct {
	Reference      ResourceReference `json:"reference"`
	Body           Value             `json:"body"`
	Representation Representation    `json:"representation"`
	Coverage       Coverage          `json:"coverage"`
	SourceRevision *string           `json:"sourceRevision,omitempty"`
}

// CompactSourceRecord adapts a finite record to the source observation envelope.
func CompactSourceRecord(record Record) SourceRecord {
	return SourceRecord{
		Reference: record.Reference, Body: record.Body, Coverage: record.Coverage,
		SourceRevision: record.SourceRevision,
		Representation: record.Representation,
	}
}

type SourceOpenRequest struct {
	SessionID   string                `json:"sessionId"`
	Generation  string                `json:"generation"`
	Target      ReadTarget            `json:"target"`
	Mode        string                `json:"mode"`
	Acquisition SourceAcquisition     `json:"acquisition"`
	Limits      SourceLimits          `json:"limits"`
	Binding     RepresentationBinding `json:"binding"`
}

type SourceCoverage struct {
	Enumeration string   `json:"enumeration"`
	Consistency string   `json:"consistency"`
	Limitations []string `json:"limitations"`
}

type SourceObservation struct {
	Record           SourceRecord      `json:"record"`
	Metadata         *ResourceMetadata `json:"metadata,omitempty"`
	DisplayName      *string           `json:"displayName,omitempty"`
	DocumentCoverage DocumentCoverage  `json:"documentCoverage"`
}

type SourceChange struct {
	Kind           string             `json:"kind"`
	Observation    *SourceObservation `json:"observation,omitempty"`
	Reference      *ResourceReference `json:"reference,omitempty"`
	SourceRevision *string            `json:"sourceRevision,omitempty"`
}

type SourceBegin struct {
	Mode string `json:"mode"`
}
type SourceSnapshot struct {
	Observations []SourceObservation `json:"observations"`
}
type SourceSnapshotEnd struct {
	Coverage   SourceCoverage `json:"coverage"`
	Continuity string         `json:"continuity"`
}
type SourceChanges struct {
	Changes  []SourceChange  `json:"changes"`
	Coverage *SourceCoverage `json:"coverage,omitempty"`
}
type SourceReset struct {
	Reason  string `json:"reason"`
	Message string `json:"message"`
}
type SourceFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type SourceFrame struct {
	SessionID   string             `json:"sessionId"`
	Generation  string             `json:"generation"`
	Sequence    uint64             `json:"-"`
	Begin       *SourceBegin       `json:"begin,omitempty"`
	Snapshot    *SourceSnapshot    `json:"snapshot,omitempty"`
	SnapshotEnd *SourceSnapshotEnd `json:"snapshotEnd,omitempty"`
	Changes     *SourceChanges     `json:"changes,omitempty"`
	Reset       *SourceReset       `json:"reset,omitempty"`
	Failed      *SourceFailure     `json:"failed,omitempty"`
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

// SourceStream is a caller-owned source session. Callers must Close the stream
// or cancel the opening context when they stop receiving, including on errors.
// Close is idempotent and unblocks a concurrent Recv.
type SourceStream interface {
	Recv() (SourceFrame, error)
	Close()
}

// SourceRequest is passed to Registration.Source. Limits are the session
// limits; one Changes call must stay within Limits.MaxBatchItems.
type SourceRequest struct {
	Target         ReadTarget
	Representation Representation
	Limits         SourceLimits
}
