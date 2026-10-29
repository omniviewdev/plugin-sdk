package resourcev2

import (
	"context"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// Config composes the optional Controller around required route-qualified Catalog
// discovery. A nil Lifecycle disables managed connections; operation callbacks
// then receive a nil client. Nil operation callbacks leave that operation absent,
// regardless of Controller's Go method set.
//
// NewController shallow-copies Registrations, not their nested scopes or Lifecycle.
// Keep these construction inputs immutable afterward. Callbacks can run
// concurrently, including Catalog and ResolveRegistration for the same route.
// Synchronize captured mutable state and return independently owned results.
// Public error text must be safe for users; keep native diagnostics in
// ResourceOperationError.Err, not its title, message, or suggestions.
type Config[ClientT any] struct {
	// Catalog receives the explicit route. Dynamic providers can use
	// Controller.WithClient inside this handler to discover native types.
	Catalog       func(context.Context, CatalogRequest) (Catalog, error)
	Registrations []Registration[ClientT]
	Lifecycle     *Lifecycle[ClientT]
	// ResolveRegistration optionally supplements static registrations for each
	// discovered type. It must return that exact Type and may restrict Connection
	// to the requested route, never another route. It may run repeatedly and
	// concurrently; returned scopes must remain stable during SDK use.
	ResolveRegistration func(context.Context, AccessRoute, string) (Registration[ClientT], error)
	// DynamicUpdates declares that ResolveRegistration installs real Watch
	// callbacks. It enables ancillary negotiation before connection discovery;
	// each actual target still requires catalog and callback admission.
	DynamicUpdates bool
	// ErrorClassifier runs after a registration classifier declines the error.
	// Existing typed errors and context cancellation retain their classes.
	ErrorClassifier ErrorClassifier
}

// Registration binds independently optional callbacks to an exact resource type.
// Empty Connection permits any explicitly supplied route, never a default.
// Nil Scope adds no restriction to catalog admission; a nonnil Scope narrows it.
// Overlapping registrations for the same operation are rejected at dispatch.
// A callback alone does not advertise support: the catalog must independently
// declare that operation and scope. Mutate handles only admitted create, update,
// or delete declarations; Watch never implies finite read or mutation support.
//
// Callbacks share a leased native client and can run concurrently. Their contexts
// end when the request, controller, or generation ends; do not retain the client
// beyond callback return or synchronously retire its generation. Observe
// cancellation so retirement can drain. With no Lifecycle, the client is nil.
// Finite request containers are borrowed during the call; do not mutate them
// concurrently. Return results that remain stable while SDK validation and
// transport consume them. Watch targets and representation containers are copied
// independently from retained admission; its sink remains framework-owned.
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

// Lifecycle owns configured profiles and native client generations. When supplied,
// LoadConnections, CreateClient, and DestroyClient are required; other hooks are
// optional. Discovery does not start clients. Connection arguments are independent
// configuration snapshots, not writable registry state.
//
// CreateClient receives a generation context: the initiating request can cancel
// startup until construction finishes, but a successful start outlives that request.
// Stop, reconfiguration, Close, or startup failure cancel the generation.
// A nil client with nil error is invalid. Every returned nonnil client is destroyed
// exactly once, including clients returned with an error or rejected by OnStart.
//
// OnStart must finish before leases become usable. Retirement cancels the
// generation, stops watches, and drains leases before OnStop and DestroyClient.
// Cleanup contexts retain values but not the retired context's cancellation or
// deadline; cleanup must terminate without waiting for that cancellation.
// Hooks may run concurrently for different connections and with discovery.
// Do not synchronously reenter inventory mutation from teardown hooks, or acquire
// work on the generation being started or destroyed.
//
// Nil CheckConnection reports local lifecycle state only, not network health.
// A supplied check must return a status bound to the requested connection.
// Hooks do not negotiate live resources, watches, or other optional traits.
type Lifecycle[ClientT any] struct {
	LoadConnections func(context.Context) ([]types.Connection, error)
	CreateClient    func(context.Context, types.Connection) (*ClientT, error)
	DestroyClient   func(context.Context, *ClientT) error
	CheckConnection func(context.Context, types.Connection, *ClientT) (types.ConnectionStatus, error)
	OnStart         func(context.Context, types.Connection, *ClientT) error
	OnStop          func(context.Context, types.Connection, *ClientT) error
}

// ConnectionContextProvider binds utility work to the sole started native-client
// registry without implicitly starting, probing, or selecting a connection.
// WithConnection requires an explicit configured route and a nonnil callback.
// It supplies the actual configured profile snapshot, not caller metadata.
//
// The callback context combines caller and generation cancellation. Retirement
// cancels it and waits for callback return before destroying the native client.
// Do not retain the lease-bound context for unowned background work or retire the
// same generation synchronously inside the callback. Calls may run concurrently.
// A connection lease does not itself authorize canonical resource access.
type ConnectionContextProvider interface {
	WithConnection(context.Context, AccessRoute, func(context.Context, types.Connection) error) error
}

// ConnectionContextConsumer receives the shared registry during SDK capability
// setup, before utility work begins. Bind once rather than creating a second
// resource registry. A source without resource composition may supply its own
// registry. A nil provider means managed connection admission is unavailable;
// selected-connection utility work must not bypass that absence.
type ConnectionContextConsumer interface {
	BindConnectionContextProvider(ConnectionContextProvider)
}
