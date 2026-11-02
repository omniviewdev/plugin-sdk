package resourcev2

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	"google.golang.org/protobuf/proto"
)

func TestSourceDocumentProtoRoundTrip(t *testing.T) {
	for _, schemaRevision := range []*string{nil, codecToken("schema-1")} {
		name := "absent"
		if schemaRevision != nil {
			name = "present"
		}
		t.Run(name, func(t *testing.T) {
			record := codecRecord()
			record.Representation.Schema.Revision = schemaRevision
			record.Body.Map["integer"] = Value{Kind: "int64", Int: "-9223372036854775808"}
			record.Body.Map["timestamp"] = Value{Kind: "timestamp", Timestamp: "1969-12-31T23:59:59.999999999Z"}
			record.Body.Map["items"] = Value{Kind: "list", List: []Value{{Kind: "map", Map: map[string]Value{"present": {Kind: "null"}}}}}
			items, missing, present := "items", "missing", "present"
			zero, one := uint32(0), uint32(1)
			observation := SourceObservation{
				Record: CompactSourceRecord(record),
				DocumentCoverage: DocumentCoverage{KnownAbsent: []DocumentFieldPath{
					{Segments: []FieldPathSegment{{Member: &missing}}},
					{Segments: []FieldPathSegment{{Member: &items}, {Index: &zero}, {Member: &missing}}},
					{Segments: []FieldPathSegment{{Member: &items}, {Index: &one}, {Member: &present}}},
				}},
			}
			wire, err := ToProtoSourceObservation(observation)
			if err != nil { t.Fatal(err) }
			size, err := SourceObservationWireSize(observation)
			if err != nil || size != proto.Size(wire) { t.Fatalf("size = %d, protobuf = %d, error = %v", size, proto.Size(wire), err) }
			if (wire.Record.Representation.SchemaRevision == nil) != (schemaRevision == nil) {
				t.Fatal("schema revision presence lost")
			}
			data, err := proto.Marshal(wire)
			if err != nil { t.Fatal(err) }
			var decoded resourcepb.SourceObservation
			if err := proto.Unmarshal(data, &decoded); err != nil { t.Fatal(err) }
			got, err := FromProtoSourceObservation(&decoded)
			if err != nil { t.Fatal(err) }
			if !reflect.DeepEqual(got, observation) { t.Fatalf("round trip: got %#v, want %#v", got, observation) }
		})
	}
}

func TestSourceDocumentBindingSchemaRevisionPresence(t *testing.T) {
	for _, tc := range []struct {
		name string
		binding *string
		record *string
		valid bool
	}{
		{"both absent", nil, nil, true},
		{"both present", codecToken("schema-1"), codecToken("schema-1"), true},
		{"missing record revision", codecToken("schema-1"), nil, false},
		{"unexpected record revision", nil, codecToken("schema-1"), false},
		{"different revision", codecToken("schema-1"), codecToken("schema-2"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := phaseRequest(SourceModeSnapshot)
			request.Binding.Representation.Schema.Revision = tc.binding
			tracker := NewSourcePhaseTracker(request, "")
			phaseAdmit(t, tracker, phaseBegin(1, SourceModeSnapshot))
			record := CompactSourceRecord(codecRecord())
			record.Representation.SchemaRevision = tc.record
			frame := phaseFrame(2)
			frame.Snapshot = &SourceSnapshot{Observations: []SourceObservation{{Record: record}}}
			err := tracker.Admit(frame)
			if tc.valid && err != nil || !tc.valid && ErrorCode(err) != "RESET_REQUIRED" {
				t.Fatalf("binding admission: %v", err)
			}
		})
	}
}

func TestSourceDocumentCoverageRejectsInvalidPaths(t *testing.T) {
	member, missing := "integer", "missing"
	index := uint32(0)
	absent := DocumentFieldPath{Segments: []FieldPathSegment{{Member: &missing}}}
	for _, tc := range []struct {
		name string
		paths []DocumentFieldPath
	}{
		{"empty path", []DocumentFieldPath{{}}},
		{"empty segment", []DocumentFieldPath{{Segments: []FieldPathSegment{{}}}}},
		{"ambiguous segment", []DocumentFieldPath{{Segments: []FieldPathSegment{{Member: &missing, Index: &index}}}}},
		{"present path", []DocumentFieldPath{{Segments: []FieldPathSegment{{Member: &member}}}}},
		{"duplicate path", []DocumentFieldPath{absent, absent}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observation := SourceObservation{Record: CompactSourceRecord(codecRecord()), DocumentCoverage: DocumentCoverage{KnownAbsent: tc.paths}}
			if _, err := ToProtoSourceObservation(observation); ErrorCode(err) != "INVALID_REQUEST" {
				t.Fatalf("invalid coverage accepted: %v", err)
			}
		})
	}
}

func TestSourceFrameJSONSequence(t *testing.T) {
	for _, tc := range []struct { value uint64; text string }{
		{1, "1"}, {9007199254740993, "9007199254740993"}, {math.MaxUint64, "18446744073709551615"},
	} {
		frame := phaseFrame(tc.value)
		data, err := json.Marshal(frame)
		if err != nil { t.Fatal(err) }
		if !strings.Contains(string(data), `"sequence":"`+tc.text+`"`) { t.Fatalf("sequence not encoded as decimal text: %s", data) }
		var got SourceFrame
		if err := json.Unmarshal(data, &got); err != nil || got.Sequence != tc.value { t.Fatalf("sequence round trip: %d, %v", got.Sequence, err) }
	}
	if _, err := json.Marshal(phaseFrame(0)); err == nil { t.Fatal("zero sequence accepted") }
	for _, text := range []string{`0`, `"0"`, `"01"`, `"+1"`, `"-1"`, `"1e3"`, `"18446744073709551616"`, `null`, `""`} {
		var frame SourceFrame
		if err := json.Unmarshal([]byte(`{"sequence":`+text+`}`), &frame); err == nil { t.Fatalf("accepted sequence %s", text) }
	}
}

func TestSourceOpenWireSize(t *testing.T) {
	emptyScope := phaseRequest(SourceModeSnapshot)
	emptyScope.Target.Scope = QueryScope{}
	scoped := phaseRequest(SourceModeSnapshot)
	values := []string{"a", strings.Repeat("x", 128)}
	scoped.Target.Scope = append(scoped.Target.Scope, ScopeConstraint{Dimension: "zone", Match: ScopeOneOf, Values: &values})
	query := Value{Kind: "map", Map: map[string]Value{"filter": {Kind: "string", String: strings.Repeat("q", 128)}}}
	native := scoped
	native.Acquisition = SourceAcquisition{Kind: SourceAcquisitionNativeQuery, Language: "query", Query: &query}
	for _, tc := range []struct {
		name string
		request SourceOpenRequest
	}{
		{"empty scope", emptyScope},
		{"nonempty scope", scoped},
		{"native query", native},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire, err := ToProtoSourceOpenRequest(tc.request)
			if err != nil { t.Fatal(err) }
			if got, want := sourceOpenWireSize(tc.request), proto.Size(wire); got != want {
				t.Fatalf("source open size = %d, want %d", got, want)
			}
		})
	}
}

func TestSourceChangesWireSize(t *testing.T) {
	record := codecRecord()
	record.Body.Map["items"] = Value{Kind: "list", List: []Value{{Kind: "null"}}}
	missing, items, display := "missing", "items", ""
	index := uint32(128)
	created := time.Unix(-1, 999999999).UTC()
	observation := SourceObservation{
		Record: CompactSourceRecord(record),
		Metadata: &ResourceMetadata{Labels: map[string]string{"name": strings.Repeat("x", 128)}, CreatedAt: &created},
		DisplayName: &display,
		DocumentCoverage: DocumentCoverage{KnownAbsent: []DocumentFieldPath{
			{Segments: []FieldPathSegment{{Member: &missing}}},
			{Segments: []FieldPathSegment{{Member: &items}, {Index: &index}}},
		}},
	}
	upsert := SourceChange{Kind: SourceChangeUpsert, Observation: &observation}
	remove := SourceChange{Kind: SourceChangeRemove, Reference: &record.Reference, SourceRevision: codecToken("revision")}
	coverage := SourceCoverage{Enumeration: EnumerationPartial, Consistency: ConsistencyBestEffort, Limitations: []string{"limited", strings.Repeat("x", 128)}}
	for _, tc := range []struct {
		name string
		changes SourceChanges
	}{
		{"upsert metadata and paths", SourceChanges{Changes: []SourceChange{upsert}}},
		{"removal", SourceChanges{Changes: []SourceChange{remove}}},
		{"coverage only", SourceChanges{Coverage: &coverage}},
		{"combined", SourceChanges{Changes: []SourceChange{upsert, remove}, Coverage: &coverage}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire, err := ToProtoSourceChanges(tc.changes)
			if err != nil { t.Fatal(err) }
			got, err := SourceChangesWireSize(tc.changes)
			if err != nil { t.Fatal(err) }
			if want := proto.Size(wire); got != want {
				t.Fatalf("source changes size = %d, want %d", got, want)
			}
		})
	}
}
