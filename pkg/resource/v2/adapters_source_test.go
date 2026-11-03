package resourcev2

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

func TestSourceAdapterRoundTrips(t *testing.T) {
	record := codecRecord()
	record.Body.Map["signed"] = Value{Kind: "int64", Int: "-9223372036854775808"}
	record.Body.Map["unsigned"] = Value{Kind: "uint64", Uint: "18446744073709551615"}
	created := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	name := ""
	bare := SourceObservation{Record: CompactSourceRecord(record)}
	present := SourceObservation{Record: CompactSourceRecord(record), Metadata: &ResourceMetadata{Labels: map[string]string{"app": "test"}, CreatedAt: &created}, DisplayName: &name}
	coverage := SourceCoverage{Enumeration: EnumerationExhausted, Consistency: ConsistencyPointInTime, Limitations: []string{"limited"}}
	remove := SourceChange{Kind: SourceChangeRemove, Reference: &record.Reference, SourceRevision: codecToken("revision")}
	upsert := SourceChange{Kind: SourceChangeUpsert, Observation: &present}
	limits := SourceLimits{MaxFrameBytes: MinSourceFrameBytes, MaxBatchItems: 2}
	query := Value{Kind: "string", String: "query"}
	for _, acquisition := range []SourceAcquisition{{Kind: SourceAcquisitionEnumerate}, {Kind: SourceAcquisitionNativeQuery, Language: "query", Query: &query}} {
		adapterRoundTrip(t, acquisition, ToProtoSourceAcquisition, FromProtoSourceAcquisition)
	}
	adapterRoundTrip(t, limits, ToProtoSourceLimits, FromProtoSourceLimits)
	adapterRoundTrip(t, coverage, ToProtoSourceCoverage, FromProtoSourceCoverage)
	for _, observation := range []SourceObservation{bare, present} {
		adapterRoundTrip(t, observation, ToProtoSourceObservation, FromProtoSourceObservation)
	}
	for _, change := range []SourceChange{upsert, remove} {
		adapterRoundTrip(t, change, ToProtoSourceChange, FromProtoSourceChange)
	}
	adapterRoundTrip(t, SourceBegin{Mode: SourceModeSnapshot}, ToProtoSourceBegin, FromProtoSourceBegin)
	adapterRoundTrip(t, SourceSnapshot{Observations: []SourceObservation{bare, present}}, ToProtoSourceSnapshot, FromProtoSourceSnapshot)
	adapterRoundTrip(t, SourceSnapshotEnd{Coverage: coverage, Continuity: ContinuityNotApplicable}, ToProtoSourceSnapshotEnd, FromProtoSourceSnapshotEnd)
	for _, changes := range []SourceChanges{{Changes: []SourceChange{remove}}, {Changes: []SourceChange{upsert}, Coverage: &coverage}} {
		adapterRoundTrip(t, changes, ToProtoSourceChanges, FromProtoSourceChanges)
	}
	adapterRoundTrip(t, SourceReset{Reason: SourceResetOverflow, Message: "overflow"}, ToProtoSourceReset, FromProtoSourceReset)
	adapterRoundTrip(t, SourceFailure{Code: "INTERNAL", Message: "failed"}, ToProtoSourceFailure, FromProtoSourceFailure)
	frames := []SourceFrame{
		{Begin: &SourceBegin{Mode: SourceModeSnapshotAndUpdates}},
		{Snapshot: &SourceSnapshot{Observations: []SourceObservation{bare, present}}},
		{SnapshotEnd: &SourceSnapshotEnd{Coverage: coverage, Continuity: ContinuityBestEffort}},
		{Changes: &SourceChanges{Changes: []SourceChange{upsert, remove}, Coverage: &coverage}},
		{Reset: &SourceReset{Reason: SourceResetOverflow}},
		{Failed: &SourceFailure{Code: "INTERNAL"}},
	}
	for _, frame := range frames {
		frame.SessionID = "session"
		frame.Generation = "generation"
		frame.Sequence = 1
		adapterRoundTrip(t, frame, ToProtoSourceFrame, FromProtoSourceFrame)
	}
}

func TestSourceAdapterPreservesOptionalAndExactValues(t *testing.T) {
	record := codecRecord()
	record.Body.Map["signed"] = Value{Kind: "int64", Int: "-9223372036854775808"}
	record.Body.Map["unsigned"] = Value{Kind: "uint64", Uint: "18446744073709551615"}
	name := ""
	input := SourceChanges{Changes: []SourceChange{
		{Kind: SourceChangeUpsert, Observation: &SourceObservation{Record: CompactSourceRecord(record)}},
		{Kind: SourceChangeUpsert, Observation: &SourceObservation{Record: CompactSourceRecord(record), Metadata: &ResourceMetadata{Labels: map[string]string{"a": "b"}}, DisplayName: &name}},
		{Kind: SourceChangeRemove, Reference: &record.Reference},
	}, Coverage: &SourceCoverage{Enumeration: EnumerationPartial, Consistency: ConsistencyUnknown}}
	p, err := ToProtoSourceChanges(input)
	if err != nil {
		t.Fatal(err)
	}
	data, err := proto.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var wire resourcepb.SourceChanges
	if err := proto.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	got, err := FromProtoSourceChanges(&wire)
	if err != nil {
		t.Fatal(err)
	}
	if got.Changes[0].Observation.Metadata != nil || got.Changes[0].Observation.DisplayName != nil {
		t.Fatal("absent observation optionals became present")
	}
	observation := got.Changes[1].Observation
	if observation.Metadata == nil || observation.Metadata.Labels["a"] != "b" || observation.DisplayName == nil || *observation.DisplayName != "" {
		t.Fatal("present observation optionals were lost")
	}
	if observation.Record.Body.Map["signed"].Int != "-9223372036854775808" || observation.Record.Body.Map["unsigned"].Uint != "18446744073709551615" {
		t.Fatal("integer precision lost")
	}
	if got.Coverage == nil || got.Coverage.Enumeration != EnumerationPartial {
		t.Fatal("coverage lost")
	}
	if got.Changes[2].Reference.Incarnation == nil || *got.Changes[2].Reference.Incarnation != "incarnation" {
		t.Fatal("remove incarnation lost")
	}
	p.Coverage = nil
	got, err = FromProtoSourceChanges(p)
	if err != nil || got.Coverage != nil {
		t.Fatal("absent coverage not preserved", err)
	}
}

func TestSourceOpenAndWireVersion(t *testing.T) {
	r := codecRecord()
	values := []string{"雪"}
	request := SourceOpenRequest{
		SessionID: "session", Generation: "generation",
		Target: ReadTarget{
			Route: AccessRoute{Connection: "route"}, Provider: r.Reference.Address.Provider,
			Authority: r.Reference.Address.Authority, Type: r.Reference.Address.Type,
			Scope:          QueryScope{{Dimension: "region", Match: ScopeOneOf, Values: &values}},
			Representation: r.Representation.ID,
		},
		Binding: RepresentationBinding{Representation: r.Representation},
		Mode: SourceModeSnapshot, Acquisition: SourceAcquisition{Kind: SourceAcquisitionEnumerate},
		Limits: SourceLimits{MaxFrameBytes: MinSourceFrameBytes, MaxBatchItems: 1},
	}
	adapterRoundTrip(t, request, ToProtoSourceOpenRequest, FromProtoSourceOpenRequest)
	p, err := ToProtoSourceOpenRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if p.GetVersion() != WireVersion {
		t.Fatal("open request version missing")
	}
	p.Version++
	if _, err := FromProtoSourceOpenRequest(p); err == nil {
		t.Fatal("accepted wrong open request version")
	}
	frame, err := ToProtoSourceFrame(SourceFrame{SessionID: "session", Generation: "generation", Sequence: 1, Begin: &SourceBegin{Mode: SourceModeSnapshot}})
	if err != nil {
		t.Fatal(err)
	}
	if frame.GetVersion() != WireVersion {
		t.Fatal("frame version missing")
	}
	frame.Version++
	if _, err := FromProtoSourceFrame(frame); err == nil {
		t.Fatal("accepted wrong frame version")
	}
}

func TestSourceObservationRejectsDuplicateMetadataLabels(t *testing.T) {
	observation, err := ToProtoSourceObservation(SourceObservation{
		Record:   CompactSourceRecord(codecRecord()),
		Metadata: &ResourceMetadata{Labels: map[string]string{"x": "1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	observation.Metadata.Labels = []*resourcepb.LabelEntry{{Key: "x", Value: "1"}, {Key: "x", Value: "2"}}
	if _, err := FromProtoSourceObservation(observation); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("duplicate label: %v", err)
	}
}
