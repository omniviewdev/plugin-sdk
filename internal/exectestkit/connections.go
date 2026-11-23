// Package exectestkit is the SDK's own test infrastructure for the exec v2 lane: a fake connection registry that
// stands in for a plugin's resource provider, a plugin served over a real transport with a client that dials it the
// way the host does, and the provider-neutral cases that every exec.Client must pass. The tests of pkg/v1/exec and
// the author-facing helpers of pkg/v1/exec/exectest are built on it; it is internal so that no plugin depends on
// it.
package exectestkit

import (
	"context"
	"sync"
	"testing"

	"github.com/omniviewdev/plugin-sdk/internal/producers"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// Connections stands in for the resource provider of a plugin: one configured, started connection and a catalog
// with a revision. A plugin that serves exec targets binds it as the connection registry, so that the calls that
// address a resource (Options, and later Open and Run) are admitted exactly as in a real plugin: the caller's
// connection must match the route, the catalog revision the host pinned must be the current one, and the author's
// callback runs inside a lease that is cancelled when the connection is retired.
//
// It is a deterministic fake: Retire and SetRevision are the two events a test can cause, and the lease waits for
// the callbacks that hold it.
type Connections struct {
	provider   string
	connection types.Connection

	mu       sync.Mutex
	revision string
	retired  bool
	cancels  map[int]context.CancelFunc
	next     int
	leases   producers.Group
}

var (
	_ resourcev2.ConnectionContextProvider = (*Connections)(nil)
	_ resourcev2.Provider                  = (*Connections)(nil)
)

// NewConnections returns a registry with one started connection, whose catalog belongs to the given provider (the
// Info.Provider of the targets under test) and starts at revision "catalog-1".
func NewConnections(provider string) *Connections {
	return &Connections{
		provider:   provider,
		connection: types.Connection{ID: "conn-1", Name: "configured"},
		revision:   "catalog-1",
		cancels:    map[int]context.CancelFunc{},
	}
}

// ConnectionID returns the id of the connection.
func (c *Connections) ConnectionID() string { return c.connection.ID }

// Route returns the route that reaches the connection.
func (c *Connections) Route() resourcev2.AccessRoute {
	return resourcev2.AccessRoute{Connection: c.connection.ID}
}

// Reference returns a canonical reference to the thing with the given id, of the given resource type.
func (c *Connections) Reference(resourceType, id string) resourcev2.ResourceReference {
	return resourcev2.ResourceReference{Address: resourcev2.ResourceAddress{
		Provider:  c.provider,
		Authority: "authority",
		Type:      resourceType,
		Scope:     []resourcev2.ScopeEntry{},
		ID:        id,
	}}
}

// TargetRef returns a TargetRef that addresses the thing with the given id through the connection.
func (c *Connections) TargetRef(resourceType, id string) exec.TargetRef {
	return exec.TargetRef{Reference: c.Reference(resourceType, id), Route: c.Route()}
}

// WireTarget returns the wire form of the target that addresses the thing with the given id through the connection,
// with the given selection, for a test that sends a request the SDK's own client refuses to build (a selection, an
// unknown field) through the generated client. A nil selection is none.
func (c *Connections) WireTarget(t testing.TB, resourceType, id string, selection ...*execv2.NameValue) *execv2.TargetRef {
	t.Helper()
	reference, err := resourcev2.ToProtoResourceReference(c.Reference(resourceType, id))
	if err != nil {
		t.Fatalf("encode the reference: %v", err)
	}
	route, err := resourcev2.ToProtoAccessRoute(c.Route())
	if err != nil {
		t.Fatalf("encode the route: %v", err)
	}
	return &execv2.TargetRef{Reference: reference, Route: route, Selection: selection}
}

// Context returns ctx as the host's call arrives: carrying the call's plugin context (the connection) and the
// catalog revision the host pinned, the current one.
func (c *Connections) Context(ctx context.Context) context.Context {
	c.mu.Lock()
	revision := c.revision
	c.mu.Unlock()
	return c.ContextPinned(ctx, revision)
}

// ContextPinned is Context with an explicit catalog pin: an empty pin is a call without one.
func (c *Connections) ContextPinned(ctx context.Context, pin string) context.Context {
	connection := c.connection
	ctx = types.WithPluginContext(ctx, &types.PluginContext{Connection: &connection, RequestID: "exectest"})
	if pin != "" {
		ctx = resourcev2.WithCatalogRevision(ctx, pin)
	}
	return ctx
}

// SetRevision changes the catalog revision: a call pinned to the old one is then refused (RESET_REQUIRED).
func (c *Connections) SetRevision(revision string) {
	c.mu.Lock()
	c.revision = revision
	c.mu.Unlock()
}

// Retire retires the connection: every lease is cancelled, Retire returns when the callbacks that held one have
// returned, and a later lease is refused.
func (c *Connections) Retire() {
	c.mu.Lock()
	c.retired = true
	cancels := make([]context.CancelFunc, 0, len(c.cancels))
	for _, cancel := range c.cancels {
		cancels = append(cancels, cancel)
	}
	c.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	c.leases.Wait()
}

// WithConnection leases the connection for fn, like the resource controller does: an unknown route is NOT_FOUND, a
// retired connection is CONNECTION_ERROR, and the lease context is cancelled when the connection is retired.
func (c *Connections) WithConnection(
	ctx context.Context,
	route resourcev2.AccessRoute,
	fn func(context.Context, types.Connection) error,
) error {
	if route.Connection != c.connection.ID {
		return resourcev2.NewError("NOT_FOUND", "Connection was not loaded.")
	}
	lease, cancel := context.WithCancel(ctx)
	defer cancel()
	c.mu.Lock()
	if c.retired || !c.leases.Start(1) {
		c.mu.Unlock()
		return resourcev2.NewError("CONNECTION_ERROR", "Selected connection is not started.")
	}
	id := c.next
	c.next++
	c.cancels[id] = cancel
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.cancels, id)
		c.mu.Unlock()
		c.leases.Done()
	}()
	err := fn(lease, c.connection)
	if err == nil {
		err = lease.Err()
	}
	return err
}

// Catalog returns the catalog: the provider, the current revision and no types. The catalog revision is what the
// admission of a canonical reference compares the host's pin with.
func (c *Connections) Catalog(_ context.Context, req resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
	if req.Route.Connection != c.connection.ID {
		return resourcev2.Catalog{}, resourcev2.NewError("NOT_FOUND", "Connection was not loaded.")
	}
	c.mu.Lock()
	revision := c.revision
	c.mu.Unlock()
	return resourcev2.Catalog{
		Provider: c.provider,
		Revision: revision,
		Coverage: resourcev2.Coverage{Kind: "complete", Paths: []string{}},
		Types:    []resourcev2.ResourceType{},
	}, nil
}

// NewConnectionsT is NewConnections for a test; the retirement of every lease is awaited at cleanup so that no
// callback outlives the test.
func NewConnectionsT(t testing.TB, provider string) *Connections {
	t.Helper()
	c := NewConnections(provider)
	t.Cleanup(c.Retire)
	return c
}
