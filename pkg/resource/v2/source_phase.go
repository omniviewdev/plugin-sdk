package resourcev2

import (
	"errors"
	"io"
	"sync"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"
)

// SourcePhaseTracker validates frames from one source session. Its methods are safe for concurrent use.
type SourcePhaseTracker struct {
	mu                 sync.Mutex
	request            SourceOpenRequest
	descriptor         *ResourceType
	declaredContinuity string
	sequence           uint64
	phase              uint8 // 0: opening, 1: snapshot, 2: following, 3: terminal
	closed             bool
}

func NewSourcePhaseTracker(request SourceOpenRequest, declaredContinuity string) *SourcePhaseTracker {
	return &SourcePhaseTracker{request: request, declaredContinuity: declaredContinuity}
}

// NewCatalogBoundSourcePhaseTracker also validates records and removals against
// the descriptor admitted for this session. The descriptor must remain immutable.
func NewCatalogBoundSourcePhaseTracker(request SourceOpenRequest, descriptor ResourceType) *SourcePhaseTracker {
	return &SourcePhaseTracker{request: request, descriptor: &descriptor, declaredContinuity: SourceGuaranteeForTarget(descriptor, request.Target)}
}

// Close marks the tracker cancelled. It is idempotent and does not wait for the peer.
func (t *SourcePhaseTracker) Close() { t.mu.Lock(); t.closed = true; t.mu.Unlock() }

// TransportError reports EOF or another transport loss. A terminal stream ends with io.EOF.
func (t *SourcePhaseTracker) TransportError(err error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return NewError("CANCELLED", "source stream closed")
	}
	if err == nil {
		return nil
	}
	if errors.Is(err, io.EOF) && t.phase == 3 {
		return io.EOF
	}
	return NewError("RESET_REQUIRED", "source transport ended before completion")
}

func sourceToken(s string) bool {
	if len(s) == 0 || len(s) > MaxSourceTokenBytes {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == ':' || c == '-') {
			return false
		}
	}
	return true
}

// ValidateSourceOpenRequest checks the request's target, acquisition and negotiated limits.
func ValidateSourceOpenRequest(r SourceOpenRequest) error {
	if !sourceToken(r.SessionID) || !sourceToken(r.Generation) {
		return invalid("invalid source token")
	}
	if err := validateTarget(r.Target); err != nil {
		return err
	}
	if r.Limits.MaxFrameBytes < MinSourceFrameBytes || r.Limits.MaxFrameBytes > MaxSourceFrameBytes || r.Limits.MaxBatchItems == 0 || r.Limits.MaxBatchItems > MaxSourceBatchItems {
		return invalid("invalid source limits")
	}
	if r.Mode != SourceModeSnapshot && r.Mode != SourceModeSnapshotAndUpdates {
		return invalid("invalid source mode")
	}
	switch r.Acquisition.Kind {
	case SourceAcquisitionEnumerate:
		if r.Acquisition.Language != "" || r.Acquisition.Query != nil {
			return invalid("invalid enumerate acquisition")
		}
	case SourceAcquisitionNativeQuery:
		if r.Mode == SourceModeSnapshotAndUpdates {
			return NewError("UNSUPPORTED", "native query updates are unsupported")
		}
		if err := text(r.Acquisition.Language, true); err != nil {
			return err
		}
		if r.Acquisition.Query == nil {
			return invalid("native query is required")
		}
		if err := ValidateValue(*r.Acquisition.Query); err != nil {
			return err
		}
	default:
		return invalid("invalid source acquisition")
	}
	return nil
}

func sourceContainsReference(target ReadTarget, reference ResourceReference) bool {
	a := reference.Address
	if target.Provider != a.Provider || target.Authority != a.Authority || target.Type != a.Type {
		return false
	}
	return identityWithinQueryScope(a, target.Scope)
}

// ValidateSourceObservation validates a record and binds it to the requested target.
func ValidateSourceObservation(target ReadTarget, representation string, observation SourceObservation) error {
	if err := ValidateRecord(observation.Record); err != nil {
		return err
	}
	if observation.Record.Representation.ID != representation || representation != target.Representation || !sourceContainsReference(target, observation.Record.Reference) {
		return NewError("PROVIDER_ERROR", "source observation escapes target")
	}
	return nil
}

func checkSourceObservation(descriptor ResourceType, target ReadTarget, observation SourceObservation) error {
	if err := checkRecord(observation.Record, descriptor, target.Provider, target.Authority, target.Representation, nil, target.Scope); err != nil {
		return NewError("PROVIDER_ERROR", "invalid source observation")
	}
	return nil
}

func checkSourceReference(descriptor ResourceType, target ReadTarget, reference ResourceReference) error {
	if err := validateReference(reference); err != nil {
		return NewError("PROVIDER_ERROR", "invalid source removal")
	}
	if err := ValidateAddress(reference.Address, descriptor.IdentityDimensions); err != nil {
		return NewError("PROVIDER_ERROR", "invalid source removal")
	}
	if !sourceContainsReference(target, reference) {
		return NewError("PROVIDER_ERROR", "source removal escapes target")
	}
	return nil
}

func (t *SourcePhaseTracker) checkObservation(observation SourceObservation) error {
	if t.descriptor != nil {
		return checkSourceObservation(*t.descriptor, t.request.Target, observation)
	}
	return ValidateSourceObservation(t.request.Target, t.request.Target.Representation, observation)
}

func (t *SourcePhaseTracker) checkReference(reference ResourceReference) error {
	if t.descriptor != nil {
		return checkSourceReference(*t.descriptor, t.request.Target, reference)
	}
	if err := validateReference(reference); err != nil {
		return err
	}
	if !sourceContainsReference(t.request.Target, reference) {
		return NewError("PROVIDER_ERROR", "source removal escapes target")
	}
	return nil
}

func sourceCoverageValid(c SourceCoverage) bool {
	if c.Enumeration != EnumerationExhausted && c.Enumeration != EnumerationPartial && c.Enumeration != EnumerationUnknown {
		return false
	}
	if c.Consistency != ConsistencyPointInTime && c.Consistency != ConsistencyBestEffort && c.Consistency != ConsistencyUnknown {
		return false
	}
	if len(c.Limitations) > MaxSourceLimitations {
		return false
	}
	for _, s := range c.Limitations {
		if len(s) == 0 || len(s) > 256 || !utf8.ValidString(s) {
			return false
		}
	}
	return true
}

// Admit validates one frame and advances the session only when it is valid.
func (t *SourcePhaseTracker) Admit(f SourceFrame) error {
	return t.admit(f, -1)
}

// AdmitSized validates a frame using its already encoded protobuf size.
// The caller must supply the size of the exact frame being admitted.
func (t *SourcePhaseTracker) AdmitSized(f SourceFrame, size int) error {
	return t.admit(f, size)
}

func (t *SourcePhaseTracker) admit(f SourceFrame, size int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return NewError("CANCELLED", "source stream closed")
	}
	bad := func() error { return NewError("PROVIDER_ERROR", "invalid source frame") }
	if t.phase == 3 {
		return bad()
	}
	if f.SessionID != t.request.SessionID || f.Generation != t.request.Generation {
		return bad()
	}
	if t.sequence == ^uint64(0) || f.Sequence != t.sequence+1 {
		return NewError("RESET_REQUIRED", "source sequence discontinuity")
	}
	count := 0
	for _, present := range []bool{f.Begin != nil, f.Snapshot != nil, f.SnapshotEnd != nil, f.Changes != nil, f.Reset != nil, f.Failed != nil} {
		if present {
			count++
		}
	}
	if count != 1 {
		return bad()
	}
	phase := t.phase
	switch {
	case f.Begin != nil:
		if phase != 0 || f.Begin.Mode != t.request.Mode {
			return bad()
		}
		phase = 1
	case f.Snapshot != nil:
		if phase != 1 || len(f.Snapshot.Observations) > int(t.request.Limits.MaxBatchItems) {
			return bad()
		}
		for _, o := range f.Snapshot.Observations {
			if t.checkObservation(o) != nil {
				return bad()
			}
		}
	case f.SnapshotEnd != nil:
		if phase != 1 || !sourceCoverageValid(f.SnapshotEnd.Coverage) {
			return bad()
		}
		c := f.SnapshotEnd.Continuity
		if t.request.Mode == SourceModeSnapshot {
			if c != ContinuityNotApplicable {
				return bad()
			}
			phase = 3
		} else {
			if c != ContinuityGapFree && c != ContinuityBestEffort {
				return bad()
			}
			if t.declaredContinuity != ContinuityGapFree && t.declaredContinuity != ContinuityBestEffort || c == ContinuityGapFree && t.declaredContinuity != ContinuityGapFree {
				return bad()
			}
			phase = 2
		}
	case f.Changes != nil:
		if phase != 2 || len(f.Changes.Changes) > int(t.request.Limits.MaxBatchItems) || f.Changes.Coverage != nil && !sourceCoverageValid(*f.Changes.Coverage) {
			return bad()
		}
		for _, c := range f.Changes.Changes {
			switch c.Kind {
			case SourceChangeUpsert:
				if c.Observation == nil || c.Reference != nil || c.SourceRevision != nil || t.checkObservation(*c.Observation) != nil {
					return bad()
				}
			case SourceChangeRemove:
				if c.Reference == nil || c.Observation != nil || token(c.SourceRevision) != nil || t.checkReference(*c.Reference) != nil {
					return bad()
				}
			default:
				return bad()
			}
		}
	case f.Reset != nil:
		if phase == 0 || len(f.Reset.Message) > MaxSourceMessageBytes || !utf8.ValidString(f.Reset.Message) {
			return bad()
		}
		switch f.Reset.Reason {
		case SourceResetContinuityLost, SourceResetOverflow, SourceResetBindingChanged, SourceResetSourceInvalidated:
		default:
			return bad()
		}
		phase = 3
	case f.Failed != nil:
		if !IsErrorCode(f.Failed.Code) || len(f.Failed.Message) > MaxSourceMessageBytes || !utf8.ValidString(f.Failed.Message) {
			return bad()
		}
		phase = 3
	}
	if size < 0 {
		wire, err := ToProtoSourceFrame(f)
		if err != nil {
			return bad()
		}
		size = proto.Size(wire)
	}
	if size > int(t.request.Limits.MaxFrameBytes) {
		return NewError("RESOURCE_LIMIT", "source frame byte limit")
	}
	t.sequence = f.Sequence
	t.phase = phase
	return nil
}
