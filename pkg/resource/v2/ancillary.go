package resourcev2

import (
	"context"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// ConnectionBrowser supplies connection UI workflows independently of finite
// resource operations. It does not imply ConnectionLifecycle or automatically
// start clients. Lists and watch events describe inventory, not selected access.
//
// Calls and watches may run concurrently; synchronize shared state. Honor request
// cancellation and make watch sends cancellation-aware. The caller owns the
// watch channel: send snapshots and return, never close it. Do not mutate returned
// or sent containers while the receiver consumes them. Nil slices are empty
// inventories, not discovery failures; return an error for failed discovery.
// Public error fields must be safe for display; raw diagnostics belong in
// ResourceOperationError.Err. Remote callers must negotiate optional features
// rather than infer support from a transport client's static method set.
type ConnectionBrowser interface {
	ListConnections(context.Context) ([]types.Connection, error)
	UpdateConnection(context.Context, types.Connection) (types.Connection, error)
	DeleteConnection(context.Context, string) error
	WatchConnections(context.Context, chan<- []types.Connection) error
}

// ScopeValuesProvider optionally discovers values for a named query dimension
// on an explicit route. Discovery does not authorize or constrain acquisition;
// catalogs and request admission remain authoritative. Namespaces are one
// Kubernetes dimension, never a universal requirement.
//
// A nil slice is an empty discovery result, not a wildcard or failure. Calls may
// overlap other provider work: honor cancellation, synchronize shared state, and
// return stable result containers. Errors follow Provider's public-safe contract.
type ScopeValuesProvider interface {
	ScopeValues(context.Context, AccessRoute, string) ([]string, error)
}

// PresentationProvider supplies optional grouping and display metadata for an
// explicit route and type. It does not advertise finite resource capabilities
// or replace the canonical catalog. Empty groups may be returned as a nil slice;
// failure to acquire metadata must be an error, not fabricated empty success.
// Calls may run concurrently. Honor cancellation and return stable, independently
// owned containers; errors follow Provider's public-safe contract.
type PresentationProvider interface {
	GetResourceGroups(context.Context, AccessRoute) ([]ResourceGroup, error)
	GetResourceDefinition(context.Context, AccessRoute, string) (ResourceDefinition, error)
}

// EditorSchemaProvider lists optional editor bindings for an explicit route.
// Schema absence, acquisition failure, and reduced validation are distinct states
// in EditorSchema; an empty list does not make a validation claim. Nil slices
// mean no bindings. Calls may run concurrently: honor cancellation, synchronize
// shared state, and keep returned containers stable during SDK use.
// Implementing this interface does not grant create or update capability.
type EditorSchemaProvider interface {
	GetEditorSchemas(context.Context, AccessRoute) ([]EditorSchema, error)
}

// SchemaDocumentProvider resolves an explicit schema-document reference without
// implicit external loading or normalization by the SDK. The returned document
// must retain the exact requested reference. Return acquisition errors rather
// than a zero document; public error text must not disclose private source URLs
// or diagnostics. Calls may run concurrently; honor cancellation and return
// independently owned, stable document containers.
type SchemaDocumentProvider interface {
	GetSchemaDocument(context.Context, AccessRoute, SchemaDocumentReference) (SchemaDocument, error)
}

// RelationshipProvider declares optional relationships and resolves explicitly
// supplied source and target contexts. Resolution must honor each selected route,
// authority, scope, and accepted catalog observation; a source observation does
// not authorize a different target route. Results must remain inside the request
// bindings checked by ValidateRelationshipResolutionResult.
//
// Nil descriptor/result slices are empty results, not acquisition failures.
// Calls may overlap: honor cancellation, synchronize shared state, and keep
// borrowed inputs and returned containers stable during SDK use. Errors follow
// Provider's public-safe contract.
type RelationshipProvider interface {
	GetRelationships(context.Context, AccessRoute, string) ([]RelationshipDescriptor, error)
	ResolveRelationships(context.Context, RelationshipResolutionRequest) ([]ResolvedRelationship, error)
}

// HealthProvider optionally derives health from the supplied resource body and
// fetches bounded native resource events. Neither method implies Getter support,
// network connection health, or advisory watch continuity.
//
// GetHealth may return nil, nil when no health value is available. A nil event
// slice is empty; acquisition failures must return errors. A positive event limit
// bounds returned count; zero does not remove the SDK's finite event ceiling.
// Calls may run concurrently: honor cancellation, synchronize shared state, and
// do not mutate borrowed body containers. Return stable results and public-safe
// errors as described by Provider.
type HealthProvider interface {
	GetHealth(context.Context, GetRequest, Value) (*ResourceHealth, error)
	GetResourceEvents(context.Context, GetRequest, uint32) ([]ResourceEvent, error)
}

// ActionCatalog lists optional actions for an explicit route and resource type.
// Descriptors do not implement execution: Actioner or StreamingActioner and exact
// catalog capability admission are independently required. Nil slices mean no
// actions, not discovery failure. Calls may run concurrently; honor cancellation,
// return stable containers, and keep error text public-safe.
type ActionCatalog interface {
	GetActions(context.Context, AccessRoute, string) ([]ActionDescriptor, error)
}

// StreamingActioner supplies independently optional action execution with native
// readiness. The framework owns the output channel and completion; producers send
// events and return, never close the channel. Emit exactly one ready event after
// native establishment and before output, progress, or completion. Establishment
// failure returns an error without ready; success without ready is invalid.
//
// Honor cancellation for establishment, event sends, and teardown. Calls may run
// concurrently; synchronize shared state and do not mutate request or event
// containers while the framework consumes them. Return public-safe errors.
// A streaming implementation does not require Actioner, but still requires exact
// action catalog admission. Remote callers negotiate FeatureStreamingActions;
// a transport client's static Go method set alone is not evidence of support.
type StreamingActioner interface {
	StreamAction(context.Context, ActionRequest, chan<- ActionEvent) error
}
