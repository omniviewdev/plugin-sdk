package resource

import "context"

// ============================================================================
// Relationship Types (doc 21 — proto/v1/resource/relationship.proto)
// ============================================================================

// RelationshipType classifies the kind of relationship between resources.
type RelationshipType string

const (
	RelOwns     RelationshipType = "owns"
	RelRunsOn   RelationshipType = "runs_on"
	RelUses     RelationshipType = "uses"
	RelExposes  RelationshipType = "exposes"
	RelManages  RelationshipType = "manages"
	RelMemberOf RelationshipType = "member_of"
)

// RelationshipExtractor defines how to find related resource IDs from source data.
type RelationshipExtractor struct {
	Method        string            `json:"method"`
	FieldPath     string            `json:"fieldPath,omitempty"`
	OwnerRefKind  string            `json:"ownerRefKind,omitempty"`
	LabelSelector map[string]string `json:"labelSelector,omitempty"`
}

// RelationshipDescriptor declares a relationship from one resource type to another.
type RelationshipDescriptor struct {
	Type              RelationshipType      `json:"type"`
	TargetResourceKey string                `json:"targetResourceKey"`
	Label             string                `json:"label"`
	InverseLabel      string                `json:"inverseLabel,omitempty"`
	Cardinality       string                `json:"cardinality,omitempty"`
	Extractor         *RelationshipExtractor `json:"extractor,omitempty"`
}

// ResourceRef is a reference to a specific resource instance.
type ResourceRef struct {
	PluginID     string `json:"pluginId,omitempty"`
	ConnectionID string `json:"connectionId"`
	ResourceKey  string `json:"resourceKey"`
	ID           string `json:"id"`
	Namespace    string `json:"namespace,omitempty"`
	DisplayName  string `json:"displayName,omitempty"`
}

// ResolvedRelationship holds actual relationship instances for a resource.
type ResolvedRelationship struct {
	Descriptor RelationshipDescriptor `json:"descriptor"`
	Targets    []ResourceRef          `json:"targets"`
}

// ============================================================================
// Optional Resourcer Interfaces — Relationships
// ============================================================================

// RelationshipDeclarer can be implemented by a Resourcer to declare its
// relationship descriptors (static metadata about resource type edges).
type RelationshipDeclarer interface {
	DeclareRelationships() []RelationshipDescriptor
}

// RelationshipResolver can be implemented by a Resourcer to resolve
// relationships for a specific resource instance.
type RelationshipResolver[ClientT any] interface {
	ResolveRelationships(ctx context.Context, client *ClientT, meta ResourceMeta, id string, namespace string) ([]ResolvedRelationship, error)
}
