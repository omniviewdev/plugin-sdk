package resourcev2

import (
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
		SchemaBinding{State: "absent"},
		ToProtoSchemaBinding,
		FromProtoSchemaBinding,
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
