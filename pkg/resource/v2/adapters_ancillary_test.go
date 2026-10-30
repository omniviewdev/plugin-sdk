package resourcev2

import (
	"errors"
	"testing"
	"time"

	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

func TestAncillaryDomainAdapters(t *testing.T) {
	r := codecRecord()
	route := AccessRoute{Connection: "route"}
	instant := time.Unix(-1, 999999999).UTC()
	descriptor := RelationshipDescriptor{
		Type:            RelUses,
		TargetType:      "target/type",
		Label:           "uses",
		Extractor:       &RelationshipExtractor{Method: "fieldPath", FieldPath: "spec.target"},
		TargetScope:     []ScopeEntry{{Dimension: "literal", Value: "opaque/value"}},
		ScopeExtractors: []ScopeExtractor{{Dimension: "region", FieldPath: "metadata.region"}},
	}
	target := RelationshipTarget{Reference: r.Reference, Route: route, DisplayName: "target"}
	condition := HealthCondition{Type: "Ready", Status: "True", LastProbeTime: &instant, LastTransitionTime: &instant}
	adapterRoundTrip(t, descriptor, ToProtoRelationshipDescriptor, FromProtoRelationshipDescriptor)
	adapterRoundTrip(
		t,
		ResolvedRelationship{Descriptor: descriptor, Targets: []RelationshipTarget{target}},
		ToProtoResolvedRelationship,
		FromProtoResolvedRelationship,
	)
	adapterRoundTrip(
		t,
		ResourceHealth{Status: HealthHealthy, Since: &instant, Conditions: []HealthCondition{condition}},
		ToProtoResourceHealth,
		FromProtoResourceHealth,
	)
	adapterRoundTrip(
		t,
		ResourceEvent{
			Type:      SeverityNormal,
			Reason:    "reason",
			Message:   "message",
			Count:     1,
			FirstSeen: instant,
			LastSeen:  instant,
		},
		ToProtoResourceEvent,
		FromProtoResourceEvent,
	)
	adapterRoundTrip(
		t,
		EditorSchema{
			Type:      r.Reference.Address.Type,
			FileMatch: "*.json",
			URI:       "omniview://schema",
			Language:  "json",
			Binding:   r.Representation.Schema,
		},
		ToProtoEditorSchema,
		FromProtoEditorSchema,
	)
}

func TestAncillaryStrictPayloadAdmission(t *testing.T) {
	descriptor := &resourcepb.RelationshipDescriptor{
		Type:          string(RelUses),
		TargetType:    "target",
		ExtractorJson: []byte(`{"method":"fieldPath","unknown":true}`),
	}
	if _, err := FromProtoRelationshipDescriptor(descriptor); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("unknown extractor field: %v", err)
	}
	descriptor.ExtractorJson = []byte(`{"method":"fieldPath","method":"fieldPath"}`)
	if _, err := FromProtoRelationshipDescriptor(descriptor); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("duplicate extractor field: %v", err)
	}
	descriptor.ExtractorJson = []byte(`{"method":"fieldPath"}`)
	descriptor.TargetScope = []*resourcepb.ScopeEntry{{Dimension: "scope", Value: "literal"}}
	descriptor.ScopeExtractors = []*resourcepb.ScopeExtractor{{Dimension: "scope", FieldPath: "metadata.scope"}}
	if _, err := FromProtoRelationshipDescriptor(descriptor); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("ambiguous target scope: %v", err)
	}
	health := &resourcepb.ResourceHealth{
		Status: string(HealthHealthy),
		Since:  &resourcepb.TimestampValue{Nanos: 1000000000},
	}
	if _, err := FromProtoResourceHealth(health); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("invalid health timestamp: %v", err)
	}
}

func TestBestEffortUpdateAdapters(t *testing.T) {
	r := codecRecord()
	route := AccessRoute{Connection: "route"}
	instant := time.Unix(-1, 999999999).UTC()
	metadata := ResourceMetadata{Labels: map[string]string{"z": "2", "a": "1"}, CreatedAt: &instant}
	adapterRoundTrip(
		t,
		WatchAddPayload{Route: route, Record: r, Metadata: metadata, Guarantee: BestEffortGuarantee},
		ToProtoWatchAddPayload,
		FromProtoWatchAddPayload,
	)
	adapterRoundTrip(
		t,
		WatchUpdatePayload{Route: route, Record: r, Metadata: metadata, Guarantee: BestEffortGuarantee},
		ToProtoWatchUpdatePayload,
		FromProtoWatchUpdatePayload,
	)
	adapterRoundTrip(
		t,
		WatchDeletePayload{Route: route, Reference: r.Reference, LastKnown: &r, Guarantee: BestEffortGuarantee},
		ToProtoWatchDeletePayload,
		FromProtoWatchDeletePayload,
	)
	adapterRoundTrip(
		t,
		WatchStateEvent{
			Route:     route,
			Type:      r.Reference.Address.Type,
			State:     WatchStateSynced,
			Count:     Count{Kind: "unknown"},
			Guarantee: BestEffortGuarantee,
		},
		ToProtoWatchStateEvent,
		FromProtoWatchStateEvent,
	)
	adapterRoundTrip(
		t,
		WatchStateEvent{
			Route:     route,
			Type:      r.Reference.Address.Type,
			State:     WatchStateError,
			Count:     Count{Kind: "unknown"},
			Error:     &ResourceOperationError{Code: "UNAVAILABLE", Err: errors.New("private")},
			Guarantee: BestEffortGuarantee,
		},
		ToProtoWatchStateEvent,
		FromProtoWatchStateEvent,
	)
	adapterRoundTrip(
		t,
		WatchConnectionSummary{
			Route:          route,
			Resources:      map[string]WatchState{"z": WatchStateSynced, "a": WatchStateIdle},
			ResourceCounts: map[string]Count{"a": {Kind: "unknown"}},
			Scope:          QueryScope{},
			Guarantee:      BestEffortGuarantee,
		},
		ToProtoWatchConnectionSummary,
		FromProtoWatchConnectionSummary,
	)
}

func TestUpdateDuplicateMapsAndGuaranteesReject(t *testing.T) {
	payload, err := ToProtoWatchAddPayload(
		WatchAddPayload{
			Route:     AccessRoute{Connection: "route"},
			Record:    codecRecord(),
			Metadata:  ResourceMetadata{Labels: map[string]string{}},
			Guarantee: BestEffortGuarantee,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	payload.Metadata.Labels = []*resourcepb.LabelEntry{{Key: "x", Value: "1"}, {Key: "x", Value: "2"}}
	if _, err := FromProtoWatchAddPayload(payload); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("duplicate label: %v", err)
	}
	summary := &resourcepb.WatchConnectionSummary{
		Route:     &resourcepb.AccessRoute{Connection: "route"},
		Guarantee: BestEffortGuarantee,
		Resources: []*resourcepb.ResourceWatchState{{Type: "x", State: 0}, {Type: "x", State: 0}},
	}
	if _, err := FromProtoWatchConnectionSummary(summary); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("duplicate resource state: %v", err)
	}
	summary.Resources = nil
	summary.ResourceCounts = []*resourcepb.ResourceWatchCount{
		{Type: "x", Count: &resourcepb.Count{Kind: "unknown"}},
		{Type: "x", Count: &resourcepb.Count{Kind: "unknown"}},
	}
	if _, err := FromProtoWatchConnectionSummary(summary); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("duplicate resource count: %v", err)
	}
	if _, err := ToProtoWatchAddPayload(
		WatchAddPayload{Route: AccessRoute{Connection: "route"}, Record: codecRecord(), Guarantee: "gapFree"},
	); ErrorCode(
		err,
	) != "INVALID_REQUEST" {
		t.Fatalf("invented continuity: %v", err)
	}
}
