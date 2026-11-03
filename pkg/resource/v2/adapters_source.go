package resourcev2

import (
	"sort"

	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

func validateMetadata(v ResourceMetadata) error { return admission(v) }
func toResourceMetadata(v ResourceMetadata) *resourcepb.ResourceMetadata {
	keys := make([]string, 0, len(v.Labels))
	for k := range v.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	p := &resourcepb.ResourceMetadata{
		Labels:    make([]*resourcepb.LabelEntry, 0, len(keys)),
		CreatedAt: toOptionalTimestamp(v.CreatedAt),
	}
	for _, k := range keys {
		p.Labels = append(p.Labels, &resourcepb.LabelEntry{Key: k, Value: v.Labels[k]})
	}
	return p
}

func (d *adapterDecoder) metadata(p *resourcepb.ResourceMetadata) ResourceMetadata {
	if p == nil {
		return ResourceMetadata{Labels: map[string]string{}}
	}
	v := ResourceMetadata{Labels: make(map[string]string, len(p.GetLabels()))}
	for _, entry := range p.GetLabels() {
		if entry == nil {
			d.fail("missing label entry")
			return ResourceMetadata{}
		}
		if _, ok := v.Labels[entry.GetKey()]; ok {
			d.fail("duplicate metadata label")
			return ResourceMetadata{}
		}
		v.Labels[entry.GetKey()] = entry.GetValue()
	}
	v.CreatedAt = d.optionalTimestamp(p.GetCreatedAt())
	return v
}

func validateSourceLimits(v SourceLimits) error {
	if v.MaxFrameBytes < MinSourceFrameBytes || v.MaxFrameBytes > MaxSourceFrameBytes || v.MaxBatchItems == 0 || v.MaxBatchItems > MaxSourceBatchItems {
		return invalid("invalid source limits")
	}
	return nil
}
func validateSourceAcquisition(v SourceAcquisition) error {
	switch v.Kind {
	case SourceAcquisitionEnumerate:
		if v.Language != "" || v.Query != nil {
			return invalid("invalid enumerate acquisition")
		}
	case SourceAcquisitionNativeQuery:
		if err := text(v.Language, true); err != nil {
			return err
		}
		if v.Query == nil {
			return invalid("missing native query")
		}
		return ValidateValue(*v.Query)
	default:
		return invalid("invalid source acquisition")
	}
	return nil
}
func validateSourceCoverageAdapter(v SourceCoverage) error {
	switch v.Enumeration {
	case EnumerationExhausted, EnumerationPartial, EnumerationUnknown:
	default:
		return invalid("invalid source enumeration")
	}
	switch v.Consistency {
	case ConsistencyPointInTime, ConsistencyBestEffort, ConsistencyUnknown:
	default:
		return invalid("invalid source consistency")
	}
	if len(v.Limitations) > MaxSourceLimitations {
		return limited("source limitations limit")
	}
	for _, s := range v.Limitations {
		if len(s) > 64<<10 {
			return limited("source limitation length")
		}
		if err := text(s, true); err != nil {
			return err
		}
	}
	return nil
}
func validateSourceObservationAdapter(v SourceObservation) error {
	if err := validateSourceRecord(v.Record); err != nil {
		return err
	}
	if err := validateDocumentCoverage(v.DocumentCoverage, v.Record.Body); err != nil {
		return err
	}
	if v.Metadata != nil {
		if err := validateMetadata(*v.Metadata); err != nil {
			return err
		}
	}
	if v.DisplayName != nil {
		return text(*v.DisplayName, false)
	}
	return nil
}
func validateSourceChangeAdapter(v SourceChange) error {
	switch v.Kind {
	case SourceChangeUpsert:
		if v.Observation == nil || v.Reference != nil || v.SourceRevision != nil {
			return invalid("invalid source upsert")
		}
		return validateSourceObservationAdapter(*v.Observation)
	case SourceChangeRemove:
		if v.Observation != nil || v.Reference == nil {
			return invalid("invalid source remove")
		}
		if err := validateReference(*v.Reference); err != nil {
			return err
		}
		return token(v.SourceRevision)
	default:
		return invalid("invalid source change")
	}
}
func validateSourceBeginAdapter(v SourceBegin) error {
	if v.Mode != SourceModeSnapshot && v.Mode != SourceModeSnapshotAndUpdates {
		return invalid("invalid source mode")
	}
	return nil
}
func validateSourceSnapshotAdapter(v SourceSnapshot) error {
	if len(v.Observations) > MaxSourceBatchItems {
		return limited("source batch limit")
	}
	for _, o := range v.Observations {
		if err := validateSourceObservationAdapter(o); err != nil {
			return err
		}
	}
	return nil
}
func validateSourceSnapshotEndAdapter(v SourceSnapshotEnd) error {
	if err := validateSourceCoverageAdapter(v.Coverage); err != nil {
		return err
	}
	switch v.Continuity {
	case ContinuityNotApplicable, ContinuityGapFree, ContinuityBestEffort:
		return nil
	}
	return invalid("invalid source continuity")
}
func validateSourceChangesAdapter(v SourceChanges) error {
	if len(v.Changes) > MaxSourceBatchItems {
		return limited("source batch limit")
	}
	if len(v.Changes) == 0 && v.Coverage == nil {
		return invalid("source changes contain no information")
	}
	for _, c := range v.Changes {
		if err := validateSourceChangeAdapter(c); err != nil {
			return err
		}
	}
	if v.Coverage != nil {
		return validateSourceCoverageAdapter(*v.Coverage)
	}
	return nil
}
func validateSourceResetAdapter(v SourceReset) error {
	switch v.Reason {
	case SourceResetContinuityLost, SourceResetOverflow, SourceResetBindingChanged, SourceResetSourceInvalidated:
	default:
		return invalid("invalid source reset reason")
	}
	if len(v.Message) > MaxSourceMessageBytes {
		return limited("source message limit")
	}
	return text(v.Message, false)
}
func validateSourceFailureAdapter(v SourceFailure) error {
	if !IsErrorCode(v.Code) {
		return invalid("invalid source failure code")
	}
	if len(v.Message) > MaxSourceMessageBytes {
		return limited("source message limit")
	}
	return text(v.Message, false)
}
func validateSourceFrameAdapter(v SourceFrame) error {
	n := 0
	if v.Begin != nil {
		n++
		if err := validateSourceBeginAdapter(*v.Begin); err != nil {
			return err
		}
	}
	if v.Snapshot != nil {
		n++
		if err := validateSourceSnapshotAdapter(*v.Snapshot); err != nil {
			return err
		}
	}
	if v.SnapshotEnd != nil {
		n++
		if err := validateSourceSnapshotEndAdapter(*v.SnapshotEnd); err != nil {
			return err
		}
	}
	if v.Changes != nil {
		n++
		if err := validateSourceChangesAdapter(*v.Changes); err != nil {
			return err
		}
	}
	if v.Reset != nil {
		n++
		if err := validateSourceResetAdapter(*v.Reset); err != nil {
			return err
		}
	}
	if v.Failed != nil {
		n++
		if err := validateSourceFailureAdapter(*v.Failed); err != nil {
			return err
		}
	}
	if n != 1 {
		return invalid("source frame requires one payload")
	}
	return nil
}
func toSourceLimits(v SourceLimits) *resourcepb.SourceLimits {
	return &resourcepb.SourceLimits{MaxFrameBytes: v.MaxFrameBytes, MaxBatchItems: v.MaxBatchItems}
}
func (d *adapterDecoder) sourceLimits(p *resourcepb.SourceLimits) SourceLimits {
	if p == nil {
		d.fail("missing source limits")
		return SourceLimits{}
	}
	return SourceLimits{MaxFrameBytes: p.GetMaxFrameBytes(), MaxBatchItems: p.GetMaxBatchItems()}
}
func toSourceAcquisition(v SourceAcquisition) *resourcepb.SourceAcquisition {
	p := &resourcepb.SourceAcquisition{}
	switch v.Kind {
	case SourceAcquisitionEnumerate:
		p.Kind = &resourcepb.SourceAcquisition_Enumerate{Enumerate: &resourcepb.SourceEnumerate{}}
	case SourceAcquisitionNativeQuery:
		p.Kind = &resourcepb.SourceAcquisition_NativeQuery{NativeQuery: &resourcepb.SourceNativeQuery{Language: v.Language, Query: toValue(*v.Query)}}
	}
	return p
}
func (d *adapterDecoder) sourceAcquisition(p *resourcepb.SourceAcquisition) SourceAcquisition {
	if p == nil {
		d.fail("missing source acquisition")
		return SourceAcquisition{}
	}
	switch x := p.GetKind().(type) {
	case *resourcepb.SourceAcquisition_Enumerate:
		if x.Enumerate == nil {
			d.fail("missing source enumerate")
		}
		return SourceAcquisition{Kind: SourceAcquisitionEnumerate}
	case *resourcepb.SourceAcquisition_NativeQuery:
		if x.NativeQuery == nil {
			d.fail("missing source native query")
			return SourceAcquisition{}
		}
		q := d.value(x.NativeQuery.GetQuery())
		return SourceAcquisition{Kind: SourceAcquisitionNativeQuery, Language: x.NativeQuery.GetLanguage(), Query: &q}
	default:
		d.fail("missing source acquisition kind")
		return SourceAcquisition{}
	}
}
func toSourceOpenRequest(v SourceOpenRequest) *resourcepb.SourceOpenRequest {
	return &resourcepb.SourceOpenRequest{Version: WireVersion, SessionId: v.SessionID, Generation: v.Generation, Target: toReadTarget(v.Target), Mode: v.Mode, Acquisition: toSourceAcquisition(v.Acquisition), Limits: toSourceLimits(v.Limits), Binding: &resourcepb.RepresentationBinding{Representation: toRepresentation(v.Binding.Representation)}}
}
func (d *adapterDecoder) sourceOpenRequest(p *resourcepb.SourceOpenRequest) SourceOpenRequest {
	if p.GetVersion() != WireVersion {
		d.fail("invalid wire version")
	}
	var binding RepresentationBinding
	if p.GetBinding() == nil {
		d.fail("missing representation binding")
	} else {
		binding.Representation = d.representation(p.GetBinding().GetRepresentation())
	}
	return SourceOpenRequest{SessionID: p.GetSessionId(), Generation: p.GetGeneration(), Target: d.target(p.GetTarget()), Mode: p.GetMode(), Acquisition: d.sourceAcquisition(p.GetAcquisition()), Limits: d.sourceLimits(p.GetLimits()), Binding: binding}
}
func toSourceCoverage(v SourceCoverage) *resourcepb.SourceCoverage {
	return &resourcepb.SourceCoverage{Enumeration: v.Enumeration, Consistency: v.Consistency, Limitations: copyStrings(v.Limitations)}
}
func (d *adapterDecoder) sourceCoverage(p *resourcepb.SourceCoverage) SourceCoverage {
	if p == nil {
		d.fail("missing source coverage")
		return SourceCoverage{}
	}
	return SourceCoverage{Enumeration: p.GetEnumeration(), Consistency: p.GetConsistency(), Limitations: copyStrings(p.GetLimitations())}
}
func toSourceObservation(v SourceObservation) *resourcepb.SourceObservation {
	r := v.Record
	p := &resourcepb.SourceObservation{Record: &resourcepb.SourceRecord{Reference: toResourceReference(r.Reference), Body: toValue(r.Body), Representation: &resourcepb.RepresentationRef{Id: r.Representation.ID, Revision: r.Representation.Revision, SchemaRevision: copyToken(r.Representation.SchemaRevision)}, Coverage: toCoverage(r.Coverage), SourceRevision: copyToken(r.SourceRevision)}, DisplayName: copyToken(v.DisplayName), DocumentCoverage: toDocumentCoverage(v.DocumentCoverage)}
	if v.Metadata != nil {
		p.Metadata = toResourceMetadata(*v.Metadata)
	}
	return p
}
func (d *adapterDecoder) sourceObservation(p *resourcepb.SourceObservation) SourceObservation {
	if p == nil {
		d.fail("missing source observation")
		return SourceObservation{}
	}
	record := p.GetRecord()
	if record == nil || record.GetRepresentation() == nil {
		d.fail("missing source record or representation")
		return SourceObservation{}
	}
	ref := record.GetRepresentation()
	v := SourceObservation{Record: SourceRecord{Reference: d.reference(record.GetReference()), Body: d.value(record.GetBody()), Representation: RepresentationRef{ID: ref.GetId(), Revision: ref.GetRevision(), SchemaRevision: copyToken(ref.SchemaRevision)}, Coverage: d.coverage(record.GetCoverage()), SourceRevision: copyToken(record.SourceRevision)}, DisplayName: copyToken(p.DisplayName), DocumentCoverage: d.documentCoverage(p.GetDocumentCoverage())}
	if p.GetMetadata() != nil {
		m := d.metadata(p.GetMetadata())
		v.Metadata = &m
	}
	return v
}
func toSourceChange(v SourceChange) *resourcepb.SourceChange {
	p := &resourcepb.SourceChange{}
	if v.Kind == SourceChangeUpsert {
		p.Change = &resourcepb.SourceChange_Upsert{Upsert: toSourceObservation(*v.Observation)}
	} else {
		p.Change = &resourcepb.SourceChange_Remove{Remove: &resourcepb.SourceRemove{Reference: toResourceReference(*v.Reference), SourceRevision: copyToken(v.SourceRevision)}}
	}
	return p
}
func (d *adapterDecoder) sourceChange(p *resourcepb.SourceChange) SourceChange {
	if p == nil {
		d.fail("missing source change")
		return SourceChange{}
	}
	switch x := p.GetChange().(type) {
	case *resourcepb.SourceChange_Upsert:
		o := d.sourceObservation(x.Upsert)
		return SourceChange{Kind: SourceChangeUpsert, Observation: &o}
	case *resourcepb.SourceChange_Remove:
		if x.Remove == nil {
			d.fail("missing source remove")
			return SourceChange{}
		}
		r := d.reference(x.Remove.GetReference())
		return SourceChange{Kind: SourceChangeRemove, Reference: &r, SourceRevision: copyToken(x.Remove.SourceRevision)}
	default:
		d.fail("missing source change kind")
		return SourceChange{}
	}
}
func toSourceBegin(v SourceBegin) *resourcepb.SourceBegin {
	return &resourcepb.SourceBegin{Mode: v.Mode}
}
func (d *adapterDecoder) sourceBegin(p *resourcepb.SourceBegin) SourceBegin {
	if p == nil {
		d.fail("missing source begin")
		return SourceBegin{}
	}
	return SourceBegin{Mode: p.GetMode()}
}
func toSourceSnapshot(v SourceSnapshot) *resourcepb.SourceSnapshot {
	p := &resourcepb.SourceSnapshot{Observations: make([]*resourcepb.SourceObservation, len(v.Observations))}
	for i, o := range v.Observations {
		p.Observations[i] = toSourceObservation(o)
	}
	return p
}
func (d *adapterDecoder) sourceSnapshot(p *resourcepb.SourceSnapshot) SourceSnapshot {
	if p == nil {
		d.fail("missing source snapshot")
		return SourceSnapshot{}
	}
	if len(p.GetObservations()) > MaxSourceBatchItems {
		d.err = limited("source batch limit")
		return SourceSnapshot{}
	}
	v := SourceSnapshot{Observations: make([]SourceObservation, len(p.GetObservations()))}
	for i, o := range p.GetObservations() {
		v.Observations[i] = d.sourceObservation(o)
	}
	return v
}
func toSourceSnapshotEnd(v SourceSnapshotEnd) *resourcepb.SourceSnapshotEnd {
	return &resourcepb.SourceSnapshotEnd{Coverage: toSourceCoverage(v.Coverage), Continuity: v.Continuity}
}
func (d *adapterDecoder) sourceSnapshotEnd(p *resourcepb.SourceSnapshotEnd) SourceSnapshotEnd {
	if p == nil {
		d.fail("missing source snapshot end")
		return SourceSnapshotEnd{}
	}
	return SourceSnapshotEnd{Coverage: d.sourceCoverage(p.GetCoverage()), Continuity: p.GetContinuity()}
}
func toSourceChanges(v SourceChanges) *resourcepb.SourceChanges {
	p := &resourcepb.SourceChanges{Changes: make([]*resourcepb.SourceChange, len(v.Changes))}
	for i, c := range v.Changes {
		p.Changes[i] = toSourceChange(c)
	}
	if v.Coverage != nil {
		p.Coverage = toSourceCoverage(*v.Coverage)
	}
	return p
}
func (d *adapterDecoder) sourceChanges(p *resourcepb.SourceChanges) SourceChanges {
	if p == nil {
		d.fail("missing source changes")
		return SourceChanges{}
	}
	if len(p.GetChanges()) > MaxSourceBatchItems {
		d.err = limited("source batch limit")
		return SourceChanges{}
	}
	v := SourceChanges{Changes: make([]SourceChange, len(p.GetChanges()))}
	for i, c := range p.GetChanges() {
		v.Changes[i] = d.sourceChange(c)
	}
	if p.GetCoverage() != nil {
		c := d.sourceCoverage(p.GetCoverage())
		v.Coverage = &c
	}
	return v
}
func toSourceReset(v SourceReset) *resourcepb.SourceReset {
	return &resourcepb.SourceReset{Reason: v.Reason, Message: v.Message}
}
func (d *adapterDecoder) sourceReset(p *resourcepb.SourceReset) SourceReset {
	if p == nil {
		d.fail("missing source reset")
		return SourceReset{}
	}
	return SourceReset{Reason: p.GetReason(), Message: p.GetMessage()}
}
func toSourceFailure(v SourceFailure) *resourcepb.SourceFailure {
	return &resourcepb.SourceFailure{Code: v.Code, Message: v.Message}
}
func (d *adapterDecoder) sourceFailure(p *resourcepb.SourceFailure) SourceFailure {
	if p == nil {
		d.fail("missing source failure")
		return SourceFailure{}
	}
	return SourceFailure{Code: p.GetCode(), Message: p.GetMessage()}
}
func toSourceFrame(v SourceFrame) *resourcepb.SourceFrame {
	p := &resourcepb.SourceFrame{Version: WireVersion, SessionId: v.SessionID, Generation: v.Generation, Sequence: v.Sequence}
	switch {
	case v.Begin != nil:
		p.Payload = &resourcepb.SourceFrame_Begin{Begin: toSourceBegin(*v.Begin)}
	case v.Snapshot != nil:
		p.Payload = &resourcepb.SourceFrame_Snapshot{Snapshot: toSourceSnapshot(*v.Snapshot)}
	case v.SnapshotEnd != nil:
		p.Payload = &resourcepb.SourceFrame_SnapshotEnd{SnapshotEnd: toSourceSnapshotEnd(*v.SnapshotEnd)}
	case v.Changes != nil:
		p.Payload = &resourcepb.SourceFrame_Changes{Changes: toSourceChanges(*v.Changes)}
	case v.Reset != nil:
		p.Payload = &resourcepb.SourceFrame_Reset_{Reset_: toSourceReset(*v.Reset)}
	case v.Failed != nil:
		p.Payload = &resourcepb.SourceFrame_Failed{Failed: toSourceFailure(*v.Failed)}
	}
	return p
}
func (d *adapterDecoder) sourceFrame(p *resourcepb.SourceFrame) SourceFrame {
	if p.GetVersion() != WireVersion {
		d.fail("invalid wire version")
	}
	v := SourceFrame{SessionID: p.GetSessionId(), Generation: p.GetGeneration(), Sequence: p.GetSequence()}
	switch x := p.GetPayload().(type) {
	case *resourcepb.SourceFrame_Begin:
		b := d.sourceBegin(x.Begin)
		v.Begin = &b
	case *resourcepb.SourceFrame_Snapshot:
		s := d.sourceSnapshot(x.Snapshot)
		v.Snapshot = &s
	case *resourcepb.SourceFrame_SnapshotEnd:
		s := d.sourceSnapshotEnd(x.SnapshotEnd)
		v.SnapshotEnd = &s
	case *resourcepb.SourceFrame_Changes:
		c := d.sourceChanges(x.Changes)
		v.Changes = &c
	case *resourcepb.SourceFrame_Reset_:
		r := d.sourceReset(x.Reset_)
		v.Reset = &r
	case *resourcepb.SourceFrame_Failed:
		f := d.sourceFailure(x.Failed)
		v.Failed = &f
	default:
		d.fail("missing source frame payload")
	}
	return v
}

func ToProtoSourceLimits(v SourceLimits) (*resourcepb.SourceLimits, error) {
	return toProto(v, validateSourceLimits, toSourceLimits)
}
func FromProtoSourceLimits(p *resourcepb.SourceLimits) (SourceLimits, error) {
	return fromProto(p, (*adapterDecoder).sourceLimits, validateSourceLimits)
}
func ToProtoSourceAcquisition(v SourceAcquisition) (*resourcepb.SourceAcquisition, error) {
	return toProto(v, validateSourceAcquisition, toSourceAcquisition)
}
func FromProtoSourceAcquisition(p *resourcepb.SourceAcquisition) (SourceAcquisition, error) {
	return fromProto(p, (*adapterDecoder).sourceAcquisition, validateSourceAcquisition)
}
func ToProtoSourceOpenRequest(v SourceOpenRequest) (*resourcepb.SourceOpenRequest, error) {
	return toProto(v, ValidateSourceOpenRequest, toSourceOpenRequest)
}
func FromProtoSourceOpenRequest(p *resourcepb.SourceOpenRequest) (SourceOpenRequest, error) {
	return fromProto(p, (*adapterDecoder).sourceOpenRequest, ValidateSourceOpenRequest)
}
func ToProtoSourceCoverage(v SourceCoverage) (*resourcepb.SourceCoverage, error) {
	return toProto(v, validateSourceCoverageAdapter, toSourceCoverage)
}
func FromProtoSourceCoverage(p *resourcepb.SourceCoverage) (SourceCoverage, error) {
	return fromProto(p, (*adapterDecoder).sourceCoverage, validateSourceCoverageAdapter)
}
func ToProtoSourceObservation(v SourceObservation) (*resourcepb.SourceObservation, error) {
	return toProto(v, validateSourceObservationAdapter, toSourceObservation)
}
func FromProtoSourceObservation(p *resourcepb.SourceObservation) (SourceObservation, error) {
	return fromProto(p, (*adapterDecoder).sourceObservation, validateSourceObservationAdapter)
}
func ToProtoSourceChange(v SourceChange) (*resourcepb.SourceChange, error) {
	return toProto(v, validateSourceChangeAdapter, toSourceChange)
}
func FromProtoSourceChange(p *resourcepb.SourceChange) (SourceChange, error) {
	return fromProto(p, (*adapterDecoder).sourceChange, validateSourceChangeAdapter)
}
func ToProtoSourceBegin(v SourceBegin) (*resourcepb.SourceBegin, error) {
	return toProto(v, validateSourceBeginAdapter, toSourceBegin)
}
func FromProtoSourceBegin(p *resourcepb.SourceBegin) (SourceBegin, error) {
	return fromProto(p, (*adapterDecoder).sourceBegin, validateSourceBeginAdapter)
}
func ToProtoSourceSnapshot(v SourceSnapshot) (*resourcepb.SourceSnapshot, error) {
	return toProto(v, validateSourceSnapshotAdapter, toSourceSnapshot)
}
func FromProtoSourceSnapshot(p *resourcepb.SourceSnapshot) (SourceSnapshot, error) {
	return fromProto(p, (*adapterDecoder).sourceSnapshot, validateSourceSnapshotAdapter)
}
func ToProtoSourceSnapshotEnd(v SourceSnapshotEnd) (*resourcepb.SourceSnapshotEnd, error) {
	return toProto(v, validateSourceSnapshotEndAdapter, toSourceSnapshotEnd)
}
func FromProtoSourceSnapshotEnd(p *resourcepb.SourceSnapshotEnd) (SourceSnapshotEnd, error) {
	return fromProto(p, (*adapterDecoder).sourceSnapshotEnd, validateSourceSnapshotEndAdapter)
}
func ToProtoSourceChanges(v SourceChanges) (*resourcepb.SourceChanges, error) {
	return toProto(v, validateSourceChangesAdapter, toSourceChanges)
}
func FromProtoSourceChanges(p *resourcepb.SourceChanges) (SourceChanges, error) {
	return fromProto(p, (*adapterDecoder).sourceChanges, validateSourceChangesAdapter)
}
func ToProtoSourceReset(v SourceReset) (*resourcepb.SourceReset, error) {
	return toProto(v, validateSourceResetAdapter, toSourceReset)
}
func FromProtoSourceReset(p *resourcepb.SourceReset) (SourceReset, error) {
	return fromProto(p, (*adapterDecoder).sourceReset, validateSourceResetAdapter)
}
func ToProtoSourceFailure(v SourceFailure) (*resourcepb.SourceFailure, error) {
	return toProto(v, validateSourceFailureAdapter, toSourceFailure)
}
func FromProtoSourceFailure(p *resourcepb.SourceFailure) (SourceFailure, error) {
	return fromProto(p, (*adapterDecoder).sourceFailure, validateSourceFailureAdapter)
}
func ToProtoSourceFrame(v SourceFrame) (*resourcepb.SourceFrame, error) {
	return toProto(v, validateSourceFrameAdapter, toSourceFrame)
}
func FromProtoSourceFrame(p *resourcepb.SourceFrame) (SourceFrame, error) {
	return fromProto(p, (*adapterDecoder).sourceFrame, validateSourceFrameAdapter)
}
