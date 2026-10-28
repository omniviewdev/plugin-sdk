package resourcev2

import (
	"bytes"
	"encoding/json"
	"time"
)

// These presentation and diagnostic declarations are not resource identity and
// do not imply finite-operation support or a live continuity guarantee.
type EditorSchema struct {
	Type      string        `json:"type"`
	FileMatch string        `json:"fileMatch"`
	URI       string        `json:"uri"`
	Language  string        `json:"language"`
	Binding   SchemaBinding `json:"binding"`
}
type ResourceGroup struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Icon        string   `json:"icon"`
	Resources   []string `json:"resources"`
}
type ResourceDefinition struct {
	ColumnDefs []ColumnDefinition `json:"columnDefs"`
}
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
	Type           string            `json:"type,omitempty"`
	TypeAccessor   string            `json:"typeAccessor,omitempty"`
	TypeMap        map[string]string `json:"typeMap,omitempty"`
	IDAccessor     string            `json:"idAccessor"`
	ScopeAccessors []ScopeExtractor  `json:"scopeAccessors"`
	DisplayID      bool              `json:"displayId"`
}

type SchemaType string

const (
	SchemaString  SchemaType = "string"
	SchemaInteger SchemaType = "integer"
	SchemaNumber  SchemaType = "number"
	SchemaBoolean SchemaType = "boolean"
	SchemaObject  SchemaType = "object"
	SchemaArray   SchemaType = "array"
)

type SchemaProperty struct {
	Type        SchemaType                `json:"type"`
	Description string                    `json:"description,omitempty"`
	Enum        []string                  `json:"enum,omitempty"`
	Default     any                       `json:"default,omitempty"`
	Minimum     *float64                  `json:"minimum,omitempty"`
	Maximum     *float64                  `json:"maximum,omitempty"`
	Properties  map[string]SchemaProperty `json:"properties,omitempty"`
	Required    []string                  `json:"required,omitempty"`
}

// Schema is the retained action-schema convenience subset. It is not P07's
// normalized resource schema and does not claim support for omitted constraints.
type Schema struct {
	Properties map[string]SchemaProperty `json:"properties,omitempty"`
	Required   []string                  `json:"required,omitempty"`
}

func (s Schema) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type       string                    `json:"type"`
		Properties map[string]SchemaProperty `json:"properties,omitempty"`
		Required   []string                  `json:"required,omitempty"`
	}{"object", s.Properties, s.Required})
}
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
func (s Schema) Ptr() *Schema       { return &s }
func PtrFloat64(v float64) *float64 { return &v }

type ActionScope string

const (
	ActionScopeInstance ActionScope = "instance"
	ActionScopeType     ActionScope = "type"
)

type ActionDescriptor struct {
	ID           string      `json:"id"`
	Label        string      `json:"label"`
	Description  string      `json:"description"`
	Icon         string      `json:"icon"`
	Scope        ActionScope `json:"scope"`
	Streaming    bool        `json:"streaming"`
	ParamsSchema *Schema     `json:"paramsSchema,omitempty"`
	OutputSchema *Schema     `json:"outputSchema,omitempty"`
	Dangerous    bool        `json:"dangerous,omitempty"`
}

// ActionEvent starts with exactly one native-established ready event. Error
// events carry a typed error; raw diagnostics do not belong in Data.
type ActionEvent struct {
	Type  string                  `json:"type"`
	Data  *Value                  `json:"data,omitempty"`
	Error *ResourceOperationError `json:"error,omitempty"`
}

type HealthStatus string

const (
	HealthHealthy   HealthStatus = "healthy"
	HealthDegraded  HealthStatus = "degraded"
	HealthUnhealthy HealthStatus = "unhealthy"
	HealthPending   HealthStatus = "pending"
	HealthUnknown   HealthStatus = "unknown"
)

type EventSeverity string

const (
	SeverityNormal  EventSeverity = "normal"
	SeverityWarning EventSeverity = "warning"
	SeverityError   EventSeverity = "error"
)

type HealthCondition struct {
	Type               string     `json:"type"`
	Status             string     `json:"status"`
	Reason             string     `json:"reason,omitempty"`
	Message            string     `json:"message,omitempty"`
	LastProbeTime      *time.Time `json:"lastProbeTime,omitempty"`
	LastTransitionTime *time.Time `json:"lastTransitionTime,omitempty"`
}
type ResourceHealth struct {
	Status     HealthStatus      `json:"status"`
	Reason     string            `json:"reason,omitempty"`
	Message    string            `json:"message,omitempty"`
	Since      *time.Time        `json:"since,omitempty"`
	Conditions []HealthCondition `json:"conditions,omitempty"`
}
type ResourceEvent struct {
	Type      EventSeverity `json:"type"`
	Reason    string        `json:"reason"`
	Message   string        `json:"message"`
	Source    string        `json:"source,omitempty"`
	Count     int32         `json:"count,omitempty"`
	FirstSeen time.Time     `json:"firstSeen"`
	LastSeen  time.Time     `json:"lastSeen"`
}

type RelationshipType string

const (
	RelOwns     RelationshipType = "owns"
	RelRunsOn   RelationshipType = "runs_on"
	RelUses     RelationshipType = "uses"
	RelExposes  RelationshipType = "exposes"
	RelManages  RelationshipType = "manages"
	RelMemberOf RelationshipType = "member_of"
	RelSelects  RelationshipType = "selects"
)

type EdgeDirection string

const (
	EdgeOutgoing EdgeDirection = ""
	EdgeIncoming EdgeDirection = "incoming"
)

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
type RelationshipDescriptor struct {
	Type            RelationshipType       `json:"type"`
	TargetType      string                 `json:"targetType"`
	Label           string                 `json:"label"`
	InverseLabel    string                 `json:"inverseLabel,omitempty"`
	Cardinality     string                 `json:"cardinality,omitempty"`
	Direction       EdgeDirection          `json:"direction,omitempty"`
	Extractor       *RelationshipExtractor `json:"extractor,omitempty"`
	TargetScope     []ScopeEntry           `json:"targetScope"`
	ScopeExtractors []ScopeExtractor       `json:"scopeExtractors"`
}
type RelationshipTarget struct {
	Reference   ResourceReference `json:"reference"`
	Route       AccessRoute       `json:"route"`
	DisplayName string            `json:"displayName,omitempty"`
}
type ResolvedRelationship struct {
	Descriptor RelationshipDescriptor `json:"descriptor"`
	Targets    []RelationshipTarget   `json:"targets"`
}
