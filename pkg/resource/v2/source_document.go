package resourcev2

import (
	"encoding/json"
	"google.golang.org/protobuf/encoding/protowire"

	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

func validateSourceRecord(r SourceRecord) error {
	if err := validateReference(r.Reference); err != nil { return err }
	if r.Body.Kind != valueKindMap { return invalid("resource body root must be map") }
	if err := ValidateValue(r.Body); err != nil { return err }
	if err := text(r.Representation.ID, true); err != nil { return err }
	if err := text(r.Representation.Revision, true); err != nil { return err }
	if err := token(r.Representation.SchemaRevision); err != nil { return err }
	if len(r.Coverage.Paths) > 256 { return limited("coverage path limit") }
	if err := validateCoverage(r.Coverage); err != nil { return err }
	return token(r.SourceRevision)
}

func validateDocumentCoverage(c DocumentCoverage, body Value) error {
	if len(c.KnownAbsent) > 256 { return limited("known absent path limit") }
	seen := make(map[string]struct{}, len(c.KnownAbsent))
	for _, path := range c.KnownAbsent {
		if len(path.Segments) == 0 || len(path.Segments) > 256 { return invalid("invalid known absent path") }
		current, present := body, true
		for _, segment := range path.Segments {
			if (segment.Member == nil) == (segment.Index == nil) { return invalid("invalid field path segment") }
			if segment.Member != nil {
				if err := text(*segment.Member, false); err != nil { return err }
				if present && current.Kind == "map" { current, present = current.Map[*segment.Member] } else { present = false }
			} else if present && current.Kind == "list" && uint64(*segment.Index) < uint64(len(current.List)) {
				current = current.List[*segment.Index]
			} else { present = false }
		}
		if present { return invalid("known absent path is present") }
		key, err := json.Marshal(path)
		if err != nil { return invalid("invalid known absent path") }
		if _, exists := seen[string(key)]; exists { return invalid("duplicate known absent path") }
		seen[string(key)] = struct{}{}
	}
	return nil
}

func toDocumentCoverage(c DocumentCoverage) *resourcepb.DocumentCoverage {
	p := &resourcepb.DocumentCoverage{KnownAbsent: make([]*resourcepb.DocumentFieldPath, len(c.KnownAbsent))}
	for i, path := range c.KnownAbsent {
		q := &resourcepb.DocumentFieldPath{Segments: make([]*resourcepb.FieldPathSegment, len(path.Segments))}
		for j, segment := range path.Segments {
			if segment.Member != nil { q.Segments[j] = &resourcepb.FieldPathSegment{Segment: &resourcepb.FieldPathSegment_Member{Member: &resourcepb.FieldPathMember{Name: *segment.Member}}} } else { q.Segments[j] = &resourcepb.FieldPathSegment{Segment: &resourcepb.FieldPathSegment_Index{Index: &resourcepb.FieldPathIndex{Index: *segment.Index}}} }
		}
		p.KnownAbsent[i] = q
	}
	return p
}

func (d *adapterDecoder) documentCoverage(p *resourcepb.DocumentCoverage) DocumentCoverage {
	if p == nil { d.fail("missing document coverage"); return DocumentCoverage{} }
	if len(p.KnownAbsent) > 256 { d.err = limited("known absent path limit"); return DocumentCoverage{} }
	if len(p.KnownAbsent) == 0 { return DocumentCoverage{} }
	c := DocumentCoverage{KnownAbsent: make([]DocumentFieldPath, len(p.KnownAbsent))}
	for i, path := range p.KnownAbsent {
		if path == nil || len(path.Segments) > 256 { d.fail("invalid field path"); return DocumentCoverage{} }
		c.KnownAbsent[i].Segments = make([]FieldPathSegment, len(path.Segments))
		for j, segment := range path.Segments {
			if segment == nil { d.fail("missing field path segment"); return DocumentCoverage{} }
			switch x := segment.Segment.(type) {
			case *resourcepb.FieldPathSegment_Member:
				if x.Member == nil { d.fail("missing member"); return DocumentCoverage{} }; value := x.Member.Name; c.KnownAbsent[i].Segments[j].Member = &value
			case *resourcepb.FieldPathSegment_Index:
				if x.Index == nil { d.fail("missing index"); return DocumentCoverage{} }; value := x.Index.Index; c.KnownAbsent[i].Segments[j].Index = &value
			default: d.fail("missing field path selection"); return DocumentCoverage{}
			}
		}
	}
	return c
}

// SourceObservationWireSize reports protobuf bytes without materializing a wire tree.
func SourceObservationWireSize(observation SourceObservation) (int, error) {
	if err := validateSourceObservationAdapter(observation); err != nil { return 0, err }
	r := observation.Record
	ref := stringFieldSize(r.Representation.ID) + stringFieldSize(r.Representation.Revision) + optionalStringFieldSize(r.Representation.SchemaRevision)
	record := messageFieldSize(referenceWireSize(r.Reference)) + messageFieldSize(valueWireSize(r.Body)) + messageFieldSize(ref) + messageFieldSize(coverageWireSize(r.Coverage)) + optionalStringFieldSize(r.SourceRevision)
	size := messageFieldSize(record) + optionalStringFieldSize(observation.DisplayName)
	if observation.Metadata != nil {
		metadata := 0
		for key, value := range observation.Metadata.Labels {
			metadata += messageFieldSize(stringFieldSize(key) + stringFieldSize(value))
		}
		if created := observation.Metadata.CreatedAt; created != nil {
			timestamp := 0
			if seconds := created.Unix(); seconds != 0 { timestamp += 1 + protowire.SizeVarint(protowire.EncodeZigZag(seconds)) }
			if nanos := created.Nanosecond(); nanos != 0 { timestamp += 1 + protowire.SizeVarint(uint64(nanos)) }
			metadata += messageFieldSize(timestamp)
		}
		size += messageFieldSize(metadata)
	}
	coverage := 0
	for _, path := range observation.DocumentCoverage.KnownAbsent {
		pathSize := 0
		for _, segment := range path.Segments {
			part := 0
			if segment.Member != nil {
				part = stringFieldSize(*segment.Member)
			} else if *segment.Index != 0 {
				part = 1 + protowire.SizeVarint(uint64(*segment.Index))
			}
			pathSize += messageFieldSize(messageFieldSize(part))
		}
		coverage += messageFieldSize(pathSize)
	}
	return size + messageFieldSize(coverage), nil
}

func sourceOpenWireSize(r SourceOpenRequest) int {
	scope := 0
	for _, constraint := range r.Target.Scope {
		selection := 0
		if constraint.Values != nil {
			for _, value := range *constraint.Values { selection += messageFieldSize(len(value)) }
		}
		scope += messageFieldSize(stringFieldSize(constraint.Dimension) + messageFieldSize(selection))
	}
	t := r.Target
	target := messageFieldSize(stringFieldSize(t.Route.Connection)) + stringFieldSize(t.Provider) + stringFieldSize(t.Authority) + stringFieldSize(t.Type) + messageFieldSize(scope) + stringFieldSize(t.Representation)
	acquisition := messageFieldSize(0)
	if r.Acquisition.Kind == SourceAcquisitionNativeQuery {
		acquisition = messageFieldSize(stringFieldSize(r.Acquisition.Language) + messageFieldSize(valueWireSize(*r.Acquisition.Query)))
	}
	representation := r.Binding.Representation
	schema := schemaWireSize(representation.Schema)
	binding := messageFieldSize(stringFieldSize(representation.ID) + stringFieldSize(representation.Revision) + messageFieldSize(schema))
	limits := 1 + protowire.SizeVarint(uint64(r.Limits.MaxFrameBytes)) + 1 + protowire.SizeVarint(uint64(r.Limits.MaxBatchItems))
	return 2 + stringFieldSize(r.SessionID) + stringFieldSize(r.Generation) + messageFieldSize(target) + stringFieldSize(r.Mode) + messageFieldSize(acquisition) + messageFieldSize(limits) + messageFieldSize(binding)
}

// SourceChangesWireSize reports the exact protobuf payload size before encoding.
func SourceChangesWireSize(changes SourceChanges) (int, error) {
	if err := validateSourceChangesAdapter(changes); err != nil { return 0, err }
	size := 0
	for _, change := range changes.Changes {
		var inner int
		if change.Kind == SourceChangeUpsert {
			observationSize, err := SourceObservationWireSize(*change.Observation)
			if err != nil { return 0, err }
			inner = messageFieldSize(observationSize)
		} else {
			inner = messageFieldSize(messageFieldSize(referenceWireSize(*change.Reference)) + optionalStringFieldSize(change.SourceRevision))
		}
		size += messageFieldSize(inner)
	}
	if changes.Coverage != nil {
		coverage := stringFieldSize(changes.Coverage.Enumeration) + stringFieldSize(changes.Coverage.Consistency)
		for _, limitation := range changes.Coverage.Limitations { coverage += messageFieldSize(len(limitation)) }
		size += messageFieldSize(coverage)
	}
	return size, nil
}
