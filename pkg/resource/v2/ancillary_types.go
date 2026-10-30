package resourcev2

import (
	"bytes"
	"encoding/json"
	"time"
)

// EditorSchema associates editor selection metadata with an explicit schema
// binding. URI is editor metadata, not permission to fetch an external document;
// Binding distinguishes unavailable acquisition from known schema absence.
type EditorSchema struct {
	Type      string        `json:"type"`
	FileMatch string        `json:"fileMatch"`
	URI       string        `json:"uri"`
	Language  string        `json:"language"`
	Binding   SchemaBinding `json:"binding"`
}

// ResourceGroup organizes opaque resource type IDs for presentation. Group
// membership does not add capabilities or change identity and authority.
type ResourceGroup struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Icon        string   `json:"icon"`
	Resources   []string `json:"resources"`
}

// ResourceDefinition supplies display columns without changing record bodies.
// Return stable column slices and nested metadata rather than mutable shared buffers.
type ResourceDefinition struct {
	ColumnDefs []ColumnDefinition `json:"columnDefs"`
}

// ColumnDefinition describes client-side display and extraction hints. Accessors
// are presentation expressions, not canonical addresses or server query predicates.
// Maps, ComponentParams, and ResourceLink remain mutable caller-visible metadata.
type ColumnDefinition struct {
	ID               string            `json:"id"`
	Header           string            `json:"header"`
	Accessors        string            `json:"accessor"`
	AccessorPriority string            `json:"accessorPriority,omitempty"`
	ColorMap         map[string]string `json:"colorMap,omitempty"`
	Color            string            `json:"color,omitempty"`
	Alignment        string            `json:"align,omitempty"`
	Hidden           bool              `json:"hidden,omitempty"`
	Width            int               `json:"width,omitempty"`
	Formatter        string            `json:"formatter,omitempty"`
	Component        string            `json:"component,omitempty"`
	ComponentParams  any               `json:"componentParams,omitempty"`
	ResourceLink     *ResourceLink     `json:"resourceLink,omitempty"`
	ValueMap         map[string]string `json:"valueMap,omitempty"`
}

// ResourceLink is a presentation extractor. Resolved identity is always a
// ResourceReference; access routes are supplied independently by the host.
type ResourceLink struct {
	// Type is a literal canonical type. Otherwise TypeAccessor and an explicit
	// TypeMap translate observed native values; the two selection forms conflict.
	Type           string            `json:"type,omitempty"`
	TypeAccessor   string            `json:"typeAccessor,omitempty"`
	TypeMap        map[string]string `json:"typeMap,omitempty"`
	IDAccessor     string            `json:"idAccessor"`
	ScopeAccessors []ScopeExtractor  `json:"scopeAccessors"`
	DisplayID      bool              `json:"displayId"`
}

// SchemaType names a primitive in the action-schema subset, not a resource
// representation or an exhaustive schema dialect.
type SchemaType string

const (
	// SchemaString describes textual action input without coercion.
	SchemaString SchemaType = "string"
	// SchemaInteger describes integral action input.
	SchemaInteger SchemaType = "integer"
	// SchemaNumber describes numeric action input, including nonintegral values.
	SchemaNumber SchemaType = "number"
	// SchemaBoolean describes true-or-false action input.
	SchemaBoolean SchemaType = "boolean"
	// SchemaObject describes named nested properties.
	SchemaObject SchemaType = "object"
	// SchemaArray describes a list-valued property.
	SchemaArray SchemaType = "array"
)

// SchemaProperty describes only the retained action-schema constraints. Default
// and Enum are metadata, not automatic input coercion or validation by dispatch.
type SchemaProperty struct {
	Type        SchemaType `json:"type"`
	Description string     `json:"description,omitempty"`
	Enum        []string   `json:"enum,omitempty"`
	Default     any        `json:"default,omitempty"`
	// Nil bounds are absent; pointers to zero express an actual numeric bound.
	Minimum    *float64                  `json:"minimum,omitempty"`
	Maximum    *float64                  `json:"maximum,omitempty"`
	Properties map[string]SchemaProperty `json:"properties,omitempty"`
	Required   []string                  `json:"required,omitempty"`
}

// Schema is the retained action-schema convenience subset. It is not P07's
// normalized resource schema and does not claim support for omitted constraints.
type Schema struct {
	Properties map[string]SchemaProperty `json:"properties,omitempty"`
	Required   []string                  `json:"required,omitempty"`
}

// MarshalJSON emits the object-root action-schema subset without applying defaults.
func (s Schema) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type       string                    `json:"type"`
		Properties map[string]SchemaProperty `json:"properties,omitempty"`
		Required   []string                  `json:"required,omitempty"`
	}{"object", s.Properties, s.Required})
}

// UnmarshalJSON rejects malformed JSON, unknown fields and non-object roots.
// It replaces s only after decoding succeeds; nested constraints remain metadata.
func (s *Schema) UnmarshalJSON(data []byte) error {
	if err := ValidateJSON(data); err != nil {
		return err
	}
	var raw struct {
		Type       string                    `json:"type"`
		Properties map[string]SchemaProperty `json:"properties,omitempty"`
		Required   []string                  `json:"required,omitempty"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	d.DisallowUnknownFields()
	if err := d.Decode(&raw); err != nil {
		return err
	}
	if raw.Type != "object" {
		return NewError("INVALID_REQUEST", "action schema root must be object")
	}
	s.Properties = raw.Properties
	s.Required = raw.Required
	return nil
}

// Ptr returns a shallow pointer copy; nested maps and slices remain shared with s.
func (s Schema) Ptr() *Schema { return &s }

// PtrFloat64 distinguishes a present numeric bound, including zero, from absence.
func PtrFloat64(v float64) *float64 { return &v }

// ActionScope distinguishes object actions from type-level actions. It is
// descriptive metadata, not authority to widen a request's admitted reference.
type ActionScope string

const (
	// ActionScopeInstance associates an action with an exact resource reference.
	ActionScopeInstance ActionScope = "instance"
	// ActionScopeType associates an action with a resource type rather than an instance.
	ActionScopeType ActionScope = "type"
)

// ActionDescriptor advertises one named action's presentation and input/output
// hints. Catalog action capability and invocation admission remain independent.
type ActionDescriptor struct {
	ID          string      `json:"id"`
	Label       string      `json:"label"`
	Description string      `json:"description"`
	Icon        string      `json:"icon"`
	Scope       ActionScope `json:"scope"`
	Streaming   bool        `json:"streaming"`
	// Nil schemas mean no schema was supplied, not that all inputs or outputs
	// have been validated. Dispatch does not fill defaults from these schemas.
	ParamsSchema *Schema `json:"paramsSchema,omitempty"`
	OutputSchema *Schema `json:"outputSchema,omitempty"`
	// Dangerous is a UI warning hint, never an authorization decision.
	Dangerous bool `json:"dangerous,omitempty"`
}

// ActionEvent carries native readiness or subsequent action output.
// Emit exactly one ready before progress/output; both require Data. Return setup
// errors before readiness rather than claiming native establishment.
// Errors use typed public-safe classification, never raw diagnostics in Data.
// Producers must not mutate nested Data after sending or close caller-owned
// channels; observe cancellation while sending and return to signal completion.
type ActionEvent struct {
	Type  string                  `json:"type"`
	Data  *Value                  `json:"data,omitempty"`
	Error *ResourceOperationError `json:"error,omitempty"`
}

// HealthStatus describes observed resource health, not connection readiness.
// Unknown is explicit uncertainty; the zero string is not a health observation.
type HealthStatus string

const (
	// HealthHealthy reports normal observed resource health.
	HealthHealthy HealthStatus = "healthy"
	// HealthDegraded reports impaired but not wholly failed resource health.
	HealthDegraded HealthStatus = "degraded"
	// HealthUnhealthy reports failed resource health.
	HealthUnhealthy HealthStatus = "unhealthy"
	// HealthPending reports health assessment or establishment still in progress.
	HealthPending HealthStatus = "pending"
	// HealthUnknown explicitly reports insufficient evidence to assess health.
	HealthUnknown HealthStatus = "unknown"
)

// EventSeverity classifies diagnostic events, not operation errors or watch state.
type EventSeverity string

const (
	// SeverityNormal marks informational resource events.
	SeverityNormal EventSeverity = "normal"
	// SeverityWarning marks resource events requiring attention.
	SeverityWarning EventSeverity = "warning"
	// SeverityError marks resource events reporting a failure.
	SeverityError EventSeverity = "error"
)

// HealthCondition retains a provider-defined condition type and status without
// interpreting native condition vocabularies. Optional nil times are unknown,
// not the zero instant. Reason and Message must be safe for public display.
type HealthCondition struct {
	Type               string     `json:"type"`
	Status             string     `json:"status"`
	Reason             string     `json:"reason,omitempty"`
	Message            string     `json:"message,omitempty"`
	LastProbeTime      *time.Time `json:"lastProbeTime,omitempty"`
	LastTransitionTime *time.Time `json:"lastTransitionTime,omitempty"`
}

// ResourceHealth is a health observation, independent of lifecycle registration
// and finite-operation support. Nil Since means no transition time was supplied;
// an empty Conditions slice does not establish healthy status.
type ResourceHealth struct {
	Status     HealthStatus      `json:"status"`
	Reason     string            `json:"reason,omitempty"`
	Message    string            `json:"message,omitempty"`
	Since      *time.Time        `json:"since,omitempty"`
	Conditions []HealthCondition `json:"conditions,omitempty"`
}

// ResourceEvent is a diagnostic observation, not advisory add/update/delete
// delivery or a continuity log. Count cannot be negative and LastSeen must not
// precede FirstSeen. Reason, Message, and Source must be safe for public display.
type ResourceEvent struct {
	Type      EventSeverity `json:"type"`
	Reason    string        `json:"reason"`
	Message   string        `json:"message"`
	Source    string        `json:"source,omitempty"`
	Count     int32         `json:"count,omitempty"`
	FirstSeen time.Time     `json:"firstSeen"`
	LastSeen  time.Time     `json:"lastSeen"`
}

// RelationshipType describes the semantic link; it does not authorize following
// that link or accessing its target.
type RelationshipType string

const (
	// RelOwns links an owner to an owned resource.
	RelOwns RelationshipType = "owns"
	// RelRunsOn links a workload to its execution resource.
	RelRunsOn RelationshipType = "runs_on"
	// RelUses links a consumer to a dependency.
	RelUses RelationshipType = "uses"
	// RelExposes links an exposing resource to the resource it makes accessible.
	RelExposes RelationshipType = "exposes"
	// RelManages links a manager to a managed resource.
	RelManages RelationshipType = "manages"
	// RelMemberOf links a member to its containing group.
	RelMemberOf RelationshipType = "member_of"
	// RelSelects links a selector to a matching resource.
	RelSelects RelationshipType = "selects"
)

// EdgeDirection orients a descriptor relative to the source resource.
// The empty value is the explicit outgoing direction, not unknown direction.
type EdgeDirection string

const (
	// EdgeOutgoing places the source at the origin of the relationship.
	EdgeOutgoing EdgeDirection = ""
	// EdgeIncoming places the source at the destination of the relationship.
	EdgeIncoming EdgeDirection = "incoming"
)

// RelationshipExtractor describes how native link evidence is found. Native
// labels and field values cannot supply target authority or replace host bindings.
type RelationshipExtractor struct {
	Method        string            `json:"method"`
	FieldPath     string            `json:"fieldPath,omitempty"`
	LabelSelector map[string]string `json:"labelSelector,omitempty"`
}

// ScopeExtractor explicitly derives a target scope dimension from source data.
// TargetScope entries remain literal values; no accessor-string heuristics apply.
type ScopeExtractor struct {
	Dimension string `json:"dimension"`
	FieldPath string `json:"fieldPath"`
}

// RelationshipDescriptor combines observed-link extraction and presentation.
// It does not admit a target route, imply enumeration support, or prove that
// every possible link has been resolved.
type RelationshipDescriptor struct {
	Type         RelationshipType `json:"type"`
	TargetType   string           `json:"targetType"`
	Label        string           `json:"label"`
	InverseLabel string           `json:"inverseLabel,omitempty"`
	Cardinality  string           `json:"cardinality,omitempty"`
	Direction    EdgeDirection    `json:"direction,omitempty"`
	// Nil Extractor leaves native resolution to the provider; it is not an
	// instruction for the host to scan for matching resources.
	Extractor *RelationshipExtractor `json:"extractor,omitempty"`
	// TargetScope contains literal dimensions; ScopeExtractors derive the rest.
	// A dimension must not occur in both or repeat within either collection.
	TargetScope     []ScopeEntry     `json:"targetScope"`
	ScopeExtractors []ScopeExtractor `json:"scopeExtractors"`
}

// RelationshipTarget carries independently bound target identity and access.
// Route must come from an explicit matching host binding, not source authority.
type RelationshipTarget struct {
	Reference   ResourceReference `json:"reference"`
	Route       AccessRoute       `json:"route"`
	DisplayName string            `json:"displayName,omitempty"`
}

// ResolvedRelationship returns concrete observed links under one descriptor.
// Empty Targets cannot stand in for unsupported resolution or a missing binding.
// Result slices and nested descriptor containers must remain stable for callers.
type ResolvedRelationship struct {
	Descriptor RelationshipDescriptor `json:"descriptor"`
	Targets    []RelationshipTarget   `json:"targets"`
}
