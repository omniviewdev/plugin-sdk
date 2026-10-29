package resourcev2

import (
	"bytes"
	"encoding/json"
	"time"

	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

func validateRelationshipDescriptor(v RelationshipDescriptor) error {
	if err := admission(v); err != nil {
		return err
	}
	switch v.Type {
	case RelOwns, RelRunsOn, RelUses, RelExposes, RelManages, RelMemberOf, RelSelects:
	default:
		return invalid("unknown relationship type")
	}
	if err := text(v.TargetType, true); err != nil {
		return err
	}
	switch v.Direction {
	case EdgeOutgoing, EdgeIncoming:
	default:
		return invalid("unknown relationship direction")
	}
	if err := ValidateAddress(ResourceAddress{Provider: "validation", Authority: "validation", Type: v.TargetType, ID: "validation", Scope: v.TargetScope}); err != nil {
		return err
	}
	seen := make(map[string]bool, len(v.TargetScope)+len(v.ScopeExtractors))
	for _, entry := range v.TargetScope {
		seen[entry.Dimension] = true
	}
	for _, extractor := range v.ScopeExtractors {
		if err := validateScopeExtractor(extractor); err != nil {
			return err
		}
		if seen[extractor.Dimension] {
			return invalid("duplicate target scope dimension")
		}
		seen[extractor.Dimension] = true
	}
	if v.Extractor != nil {
		if err := text(v.Extractor.Method, true); err != nil {
			return err
		}
	}
	return nil
}
func validateRelationshipTarget(v RelationshipTarget) error {
	if err := admission(v); err != nil {
		return err
	}
	if err := validateReference(v.Reference); err != nil {
		return err
	}
	return validateRoute(v.Route)
}
func validateResolvedRelationship(v ResolvedRelationship) error {
	if err := admission(v); err != nil {
		return err
	}
	if err := validateRelationshipDescriptor(v.Descriptor); err != nil {
		return err
	}
	for _, t := range v.Targets {
		if err := validateRelationshipTarget(t); err != nil {
			return err
		}
	}
	return nil
}
func validateHealthCondition(v HealthCondition) error {
	if err := admission(v); err != nil {
		return err
	}
	if err := text(v.Type, true); err != nil {
		return err
	}
	return text(v.Status, true)
}
func validateResourceHealth(v ResourceHealth) error {
	if err := admission(v); err != nil {
		return err
	}
	switch v.Status {
	case HealthHealthy, HealthDegraded, HealthUnhealthy, HealthPending, HealthUnknown:
	default:
		return invalid("unknown health status")
	}
	for _, c := range v.Conditions {
		if err := validateHealthCondition(c); err != nil {
			return err
		}
	}
	return nil
}
func validateResourceEvent(v ResourceEvent) error {
	if err := admission(v); err != nil {
		return err
	}
	switch v.Type {
	case SeverityNormal, SeverityWarning, SeverityError:
	default:
		return invalid("unknown event severity")
	}
	if v.Count < 0 {
		return invalid("negative event count")
	}
	if v.LastSeen.Before(v.FirstSeen) {
		return invalid("event last-seen precedes first-seen")
	}
	return nil
}
func toTimestamp(t time.Time) *resourcepb.TimestampValue {
	return &resourcepb.TimestampValue{Seconds: t.Unix(), Nanos: uint32(t.Nanosecond())}
}
func toOptionalTimestamp(t *time.Time) *resourcepb.TimestampValue {
	if t == nil {
		return nil
	}
	return toTimestamp(*t)
}
func (d *adapterDecoder) timestamp(p *resourcepb.TimestampValue) time.Time {
	if p == nil {
		d.fail("missing timestamp")
		return time.Time{}
	}
	if p.Seconds < -62135596800 || p.Seconds > 253402300799 || p.Nanos > 999999999 {
		d.fail("timestamp out of range")
		return time.Time{}
	}
	return time.Unix(p.Seconds, int64(p.Nanos)).UTC()
}
func (d *adapterDecoder) optionalTimestamp(p *resourcepb.TimestampValue) *time.Time {
	if p == nil {
		return nil
	}
	t := d.timestamp(p)
	return &t
}
func toRelationshipDescriptor(v RelationshipDescriptor) *resourcepb.RelationshipDescriptor {
	p := &resourcepb.RelationshipDescriptor{Type: string(v.Type), TargetType: v.TargetType, Label: v.Label, InverseLabel: v.InverseLabel, Cardinality: v.Cardinality, Direction: string(v.Direction), TargetScope: make([]*resourcepb.ScopeEntry, len(v.TargetScope)), ScopeExtractors: make([]*resourcepb.ScopeExtractor, len(v.ScopeExtractors))}
	for i, x := range v.TargetScope {
		p.TargetScope[i] = toScopeEntry(x)
	}
	for i, x := range v.ScopeExtractors {
		p.ScopeExtractors[i] = toScopeExtractor(x)
	}
	if v.Extractor != nil {
		p.ExtractorJson, _ = json.Marshal(v.Extractor)
	}
	return p
}
func (d *adapterDecoder) relationshipDescriptor(p *resourcepb.RelationshipDescriptor) RelationshipDescriptor {
	if p == nil {
		d.fail("missing relationship descriptor")
		return RelationshipDescriptor{}
	}
	v := RelationshipDescriptor{Type: RelationshipType(p.Type), TargetType: p.TargetType, Label: p.Label, InverseLabel: p.InverseLabel, Cardinality: p.Cardinality, Direction: EdgeDirection(p.Direction), TargetScope: make([]ScopeEntry, len(p.TargetScope)), ScopeExtractors: make([]ScopeExtractor, len(p.ScopeExtractors))}
	for i, p := range p.TargetScope {
		v.TargetScope[i] = d.scopeEntry(p)
	}
	for i, p := range p.ScopeExtractors {
		v.ScopeExtractors[i] = d.scopeExtractor(p)
	}
	if len(p.ExtractorJson) > 0 {
		raw, err := parseJSON(p.ExtractorJson)
		if err != nil {
			if d.err == nil {
				d.err = err
			}
			return v
		}
		if _, ok := raw.(map[string]any); !ok {
			d.fail("extractor must be object")
			return v
		}
		decoder := json.NewDecoder(bytes.NewReader(p.ExtractorJson))
		decoder.DisallowUnknownFields()
		var extractor RelationshipExtractor
		if err := decoder.Decode(&extractor); err != nil {
			d.fail("invalid relationship extractor")
			return v
		}
		v.Extractor = &extractor
	}
	return v
}
func toRelationshipTarget(v RelationshipTarget) *resourcepb.RelationshipTarget {
	return &resourcepb.RelationshipTarget{Reference: toResourceReference(v.Reference), Route: toAccessRoute(v.Route), DisplayName: v.DisplayName}
}
func (d *adapterDecoder) relationshipTarget(p *resourcepb.RelationshipTarget) RelationshipTarget {
	if p == nil {
		d.fail("missing relationship target")
		return RelationshipTarget{}
	}
	return RelationshipTarget{Reference: d.reference(p.Reference), Route: d.route(p.Route), DisplayName: p.DisplayName}
}
func toResolvedRelationship(v ResolvedRelationship) *resourcepb.ResolvedRelationship {
	p := &resourcepb.ResolvedRelationship{Descriptor_: toRelationshipDescriptor(v.Descriptor), Targets: make([]*resourcepb.RelationshipTarget, len(v.Targets))}
	for i, x := range v.Targets {
		p.Targets[i] = toRelationshipTarget(x)
	}
	return p
}
func (d *adapterDecoder) resolvedRelationship(p *resourcepb.ResolvedRelationship) ResolvedRelationship {
	if p == nil {
		d.fail("missing resolved relationship")
		return ResolvedRelationship{}
	}
	v := ResolvedRelationship{Descriptor: d.relationshipDescriptor(p.Descriptor_), Targets: make([]RelationshipTarget, len(p.Targets))}
	for i, p := range p.Targets {
		v.Targets[i] = d.relationshipTarget(p)
	}
	return v
}
func toHealthCondition(v HealthCondition) *resourcepb.HealthCondition {
	return &resourcepb.HealthCondition{Type: v.Type, Status: v.Status, Reason: v.Reason, Message: v.Message, LastProbeTime: toOptionalTimestamp(v.LastProbeTime), LastTransitionTime: toOptionalTimestamp(v.LastTransitionTime)}
}
func (d *adapterDecoder) healthCondition(p *resourcepb.HealthCondition) HealthCondition {
	if p == nil {
		d.fail("missing health condition")
		return HealthCondition{}
	}
	return HealthCondition{Type: p.Type, Status: p.Status, Reason: p.Reason, Message: p.Message, LastProbeTime: d.optionalTimestamp(p.LastProbeTime), LastTransitionTime: d.optionalTimestamp(p.LastTransitionTime)}
}
func toResourceHealth(v ResourceHealth) *resourcepb.ResourceHealth {
	p := &resourcepb.ResourceHealth{Status: string(v.Status), Reason: v.Reason, Message: v.Message, Since: toOptionalTimestamp(v.Since), Conditions: make([]*resourcepb.HealthCondition, len(v.Conditions))}
	for i, x := range v.Conditions {
		p.Conditions[i] = toHealthCondition(x)
	}
	return p
}
func (d *adapterDecoder) resourceHealth(p *resourcepb.ResourceHealth) ResourceHealth {
	if p == nil {
		d.fail("missing resource health")
		return ResourceHealth{}
	}
	v := ResourceHealth{Status: HealthStatus(p.Status), Reason: p.Reason, Message: p.Message, Since: d.optionalTimestamp(p.Since), Conditions: make([]HealthCondition, len(p.Conditions))}
	for i, p := range p.Conditions {
		v.Conditions[i] = d.healthCondition(p)
	}
	return v
}
func toResourceEvent(v ResourceEvent) *resourcepb.ResourceEvent {
	return &resourcepb.ResourceEvent{Type: string(v.Type), Reason: v.Reason, Message: v.Message, Source: v.Source, Count: v.Count, FirstSeen: toTimestamp(v.FirstSeen), LastSeen: toTimestamp(v.LastSeen)}
}
func (d *adapterDecoder) resourceEvent(p *resourcepb.ResourceEvent) ResourceEvent {
	if p == nil {
		d.fail("missing resource event")
		return ResourceEvent{}
	}
	return ResourceEvent{Type: EventSeverity(p.Type), Reason: p.Reason, Message: p.Message, Source: p.Source, Count: p.Count, FirstSeen: d.timestamp(p.FirstSeen), LastSeen: d.timestamp(p.LastSeen)}
}

// ToProtoRelationshipDescriptor encodes relationship metadata and validated extractor JSON without resolving targets.
// It validates before encoding, returns nil on error, and owns output storage.
func ToProtoRelationshipDescriptor(v RelationshipDescriptor) (*resourcepb.RelationshipDescriptor, error) {
	p, err := toProto(v, validateRelationshipDescriptor, toRelationshipDescriptor)
	if err != nil {
		return nil, err
	}
	if len(p.ExtractorJson) > 0 {
		if err := ValidateJSON(p.ExtractorJson); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// FromProtoRelationshipDescriptor decodes relationship metadata and validated extractor JSON without resolving targets.
// It rejects nil or invalid messages; successful output owns mutable storage.
func FromProtoRelationshipDescriptor(p *resourcepb.RelationshipDescriptor) (RelationshipDescriptor, error) {
	return fromProto(p, (*adapterDecoder).relationshipDescriptor, validateRelationshipDescriptor)
}

// ToProtoResolvedRelationship encodes a descriptor and explicit targets without cross-route admission.
// It validates before encoding, returns nil on error, and owns output storage.
func ToProtoResolvedRelationship(v ResolvedRelationship) (*resourcepb.ResolvedRelationship, error) {
	p, err := toProto(v, validateResolvedRelationship, toResolvedRelationship)
	if err != nil {
		return nil, err
	}
	if len(p.Descriptor_.ExtractorJson) > 0 {
		if err := ValidateJSON(p.Descriptor_.ExtractorJson); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// FromProtoResolvedRelationship decodes a descriptor and explicit targets without cross-route admission.
// It rejects nil or invalid messages; successful output owns mutable storage.
func FromProtoResolvedRelationship(p *resourcepb.ResolvedRelationship) (ResolvedRelationship, error) {
	return fromProto(p, (*adapterDecoder).resolvedRelationship, validateResolvedRelationship)
}

// ToProtoResourceHealth encodes health conditions and observation times without probing native health.
// It validates before encoding, returns nil on error, and owns output storage.
func ToProtoResourceHealth(v ResourceHealth) (*resourcepb.ResourceHealth, error) {
	return toProto(v, validateResourceHealth, toResourceHealth)
}

// FromProtoResourceHealth decodes health conditions and observation times without probing native health.
// It rejects nil or invalid messages; successful output owns mutable storage.
func FromProtoResourceHealth(p *resourcepb.ResourceHealth) (ResourceHealth, error) {
	return fromProto(p, (*adapterDecoder).resourceHealth, validateResourceHealth)
}

// ToProtoResourceEvent encodes event severity and timestamps without establishing replay continuity.
// It validates before encoding, returns nil on error, and owns output storage.
func ToProtoResourceEvent(v ResourceEvent) (*resourcepb.ResourceEvent, error) {
	return toProto(v, validateResourceEvent, toResourceEvent)
}

// FromProtoResourceEvent decodes event severity and timestamps without establishing replay continuity.
// It rejects nil or invalid messages; successful output owns mutable storage.
func FromProtoResourceEvent(p *resourcepb.ResourceEvent) (ResourceEvent, error) {
	return fromProto(p, (*adapterDecoder).resourceEvent, validateResourceEvent)
}

func validateScopeExtractor(v ScopeExtractor) error {
	if err := text(v.Dimension, true); err != nil {
		return err
	}
	return text(v.FieldPath, true)
}
func toScopeExtractor(v ScopeExtractor) *resourcepb.ScopeExtractor {
	return &resourcepb.ScopeExtractor{Dimension: v.Dimension, FieldPath: v.FieldPath}
}
func (d *adapterDecoder) scopeExtractor(p *resourcepb.ScopeExtractor) ScopeExtractor {
	if p == nil {
		d.fail("missing scope extractor")
		return ScopeExtractor{}
	}
	return ScopeExtractor{Dimension: p.Dimension, FieldPath: p.FieldPath}
}

func validateEditorSchema(v EditorSchema) error {
	if err := admission(v); err != nil {
		return err
	}
	for _, s := range []string{v.Type, v.FileMatch, v.URI, v.Language} {
		if err := text(s, true); err != nil {
			return err
		}
	}
	return validateSchema(v.Binding)
}
func toEditorSchema(v EditorSchema) *resourcepb.EditorSchema {
	return &resourcepb.EditorSchema{Type: v.Type, FileMatch: v.FileMatch, Uri: v.URI, Language: v.Language, Binding: toSchemaBinding(v.Binding)}
}
func (d *adapterDecoder) editorSchema(p *resourcepb.EditorSchema) EditorSchema {
	if p == nil {
		d.fail("missing editor schema")
		return EditorSchema{}
	}
	return EditorSchema{Type: p.Type, FileMatch: p.FileMatch, URI: p.Uri, Language: p.Language, Binding: d.schema(p.Binding)}
}

// ToProtoEditorSchema encodes editor metadata and schema availability without fetching document references.
// It validates before encoding, returns nil on error, and owns output storage.
func ToProtoEditorSchema(v EditorSchema) (*resourcepb.EditorSchema, error) {
	return toProto(v, validateEditorSchema, toEditorSchema)
}

// FromProtoEditorSchema decodes editor metadata and schema availability without fetching document references.
// It rejects nil or invalid messages; successful output owns mutable storage.
func FromProtoEditorSchema(p *resourcepb.EditorSchema) (EditorSchema, error) {
	return fromProto(p, (*adapterDecoder).editorSchema, validateEditorSchema)
}
