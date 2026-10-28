package resourcev2

import (
	"context"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// Config requires only connection-qualified discovery. Providers without client
// lifecycle support leave Lifecycle nil; their handlers receive a nil client.
type Config[ClientT any] struct {
	// Catalog receives the explicit route. Dynamic providers can use
	// Controller.WithClient inside this handler to discover native types.
	Catalog       func(context.Context, CatalogRequest) (Catalog, error)
	Registrations []Registration[ClientT]
	Lifecycle     *Lifecycle[ClientT]
	// ResolveRegistration supports dynamically discovered exact types. It must
	// return the requested Type and may not widen the requested route.
	ResolveRegistration func(context.Context, AccessRoute, string) (Registration[ClientT], error)
	// DynamicUpdates declares that ResolveRegistration installs real Watch
	// callbacks. It enables ancillary negotiation before connection discovery;
	// each actual target still requires catalog and callback admission.
	DynamicUpdates bool
	// ErrorClassifier runs after a registration classifier declines the error.
	// Existing typed errors and context cancellation retain their classes.
	ErrorClassifier ErrorClassifier
}

// Registration binds independent operations to an exact resource type. Empty
// Connection permits any explicitly supplied route, never an implicit default.
// Nil Scope permits any catalog-admitted scope; a nonnil Scope restricts it.
// Overlapping registrations for the same operation are rejected at dispatch.
type Registration[ClientT any] struct {
	Type        string
	Connection  string
	Scope       QueryScope
	Get         func(context.Context, *ClientT, GetRequest) (Record, error)
	Enumerate   func(context.Context, *ClientT, EnumerateRequest) (SourcePage, error)
	NativeQuery func(context.Context, *ClientT, NativeQueryRequest) (SourcePage, error)
	Mutate      func(context.Context, *ClientT, MutationRequest) (Record, error)
	Action      func(context.Context, *ClientT, ActionRequest) (Value, error)
	// Watch supplies advisory best-effort updates, never P05 continuity.
	Watch           func(context.Context, *ClientT, ReadTarget, Representation, WatchEventSink) error
	SyncPolicy      SyncPolicy
	ErrorClassifier ErrorClassifier
}

// Lifecycle owns client contexts from StartConnection through StopConnection or
// Close. The context passed to CreateClient survives the start request, but is
// canceled on failed start, stop, or close. DestroyClient runs exactly once for
// every nonnil client returned by CreateClient, including a failed start.
// CheckConnection is optional and otherwise reports only local lifecycle state.
// OnStart and OnStop are optional hooks, not live-resource/watch negotiation.
type Lifecycle[ClientT any] struct {
	LoadConnections func(context.Context) ([]types.Connection, error)
	CreateClient    func(context.Context, types.Connection) (*ClientT, error)
	DestroyClient   func(context.Context, *ClientT) error
	CheckConnection func(context.Context, types.Connection, *ClientT) (types.ConnectionStatus, error)
	OnStart         func(context.Context, types.Connection, *ClientT) error
	OnStop          func(context.Context, types.Connection, *ClientT) error
}
