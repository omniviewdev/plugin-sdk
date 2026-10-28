package resourcev2

import (
	"context"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// ConnectionBrowser preserves connection UI workflows independently of finite
// resource operations.
type ConnectionBrowser interface {
	ListConnections(context.Context) ([]types.Connection, error)
	UpdateConnection(context.Context, types.Connection) (types.Connection, error)
	DeleteConnection(context.Context, string) error
	WatchConnections(context.Context, chan<- []types.Connection) error
}

// ScopeValuesProvider optionally discovers values for a declared query dimension.
// Namespaces are one Kubernetes adapter dimension, never a universal requirement.
type ScopeValuesProvider interface {
	ScopeValues(context.Context, AccessRoute, string) ([]string, error)
}
type PresentationProvider interface {
	GetResourceGroups(context.Context, AccessRoute) ([]ResourceGroup, error)
	GetResourceDefinition(context.Context, AccessRoute, string) (ResourceDefinition, error)
}
type EditorSchemaProvider interface {
	GetEditorSchemas(context.Context, AccessRoute) ([]EditorSchema, error)
}
type SchemaDocumentProvider interface {
	GetSchemaDocument(context.Context, AccessRoute, SchemaDocumentReference) (SchemaDocument, error)
}
type RelationshipProvider interface {
	GetRelationships(context.Context, AccessRoute, string) ([]RelationshipDescriptor, error)
	ResolveRelationships(context.Context, RelationshipResolutionRequest) ([]ResolvedRelationship, error)
}
type HealthProvider interface {
	GetHealth(context.Context, GetRequest, Value) (*ResourceHealth, error)
	GetResourceEvents(context.Context, GetRequest, uint32) ([]ResourceEvent, error)
}
type ActionCatalog interface {
	GetActions(context.Context, AccessRoute, string) ([]ActionDescriptor, error)
}

// StreamingActioner retains the accepted P04 action readiness contract. The
// stream belongs to the caller. Native establishment precedes exactly one ready;
// failed establishment returns an error without ready. Cancellation owns teardown.
type StreamingActioner interface {
	StreamAction(context.Context, ActionRequest, chan<- ActionEvent) error
}
