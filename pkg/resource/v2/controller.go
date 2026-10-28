package resourcev2

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// Controller dispatches finite source operations and independently optional
// best-effort updates. Strong continuity remains P05's separate contract;
// neither a finite handler nor advisory updates imply that guarantee.
type Controller[ClientT any] struct {
	cfg               Config[ClientT]
	ctx               context.Context
	cancel            context.CancelFunc
	mu                sync.Mutex
	configMu          sync.Mutex
	closed            bool
	connections       map[string]types.Connection
	nativeConnections map[string]types.Connection
	overrides         map[string]types.Connection
	disabled          map[string]struct{}
	changing          map[string]bool
	clients           map[string]*clientSlot[ClientT]
	// failedStarts retains only safe lifecycle outcome, never clients or errors.
	// Entries are bounded by configured IDs and survive failed-client cleanup.
	failedStarts map[string]bool
	closeOnce    sync.Once
	closeErr     error
	updates      *updateManager[ClientT]
	catalogNonce [32]byte
}

type clientSlot[T any] struct {
	connection   types.Connection
	client       *T
	ctx          context.Context
	cancel       context.CancelFunc
	ready        chan struct{}
	err          error
	stopping     bool
	users        sync.WaitGroup
	destroy      sync.Once
	destroyErr   error
	catalogNonce [32]byte
}

func NewController[T any](cfg Config[T]) (*Controller[T], error) {
	if cfg.Catalog == nil {
		return nil, NewError("INVALID_REQUEST", "Catalog handler is required.")
	}
	if cfg.Lifecycle != nil && (cfg.Lifecycle.LoadConnections == nil || cfg.Lifecycle.CreateClient == nil || cfg.Lifecycle.DestroyClient == nil) {
		return nil, NewError("INVALID_REQUEST", "Lifecycle requires load, create, and destroy handlers.")
	}
	if cfg.DynamicUpdates && cfg.ResolveRegistration == nil {
		return nil, NewError("INVALID_REQUEST", "Dynamic updates require an exact registration resolver.")
	}
	for _, r := range cfg.Registrations {
		if r.Type == "" {
			return nil, NewError("INVALID_REQUEST", "Registration requires an exact type.")
		}
		if r.SyncPolicy < SyncOnConnect || r.SyncPolicy > SyncNever {
			return nil, NewError("INVALID_REQUEST", "Registration has an unknown sync policy.")
		}
		if r.Scope != nil {
			if err := ValidateScope(r.Scope); err != nil {
				return nil, err
			}
		}
	}
	cfg.Registrations = append([]Registration[T](nil), cfg.Registrations...)
	nonce, err := newCatalogNonce()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	controller := &Controller[T]{cfg: cfg, ctx: ctx, cancel: cancel, connections: make(map[string]types.Connection), nativeConnections: make(map[string]types.Connection), overrides: make(map[string]types.Connection), disabled: make(map[string]struct{}), changing: make(map[string]bool), clients: make(map[string]*clientSlot[T]), failedStarts: make(map[string]bool)}
	controller.catalogNonce = nonce
	controller.updates = newUpdateManager(controller)
	return controller, nil
}

func (c *Controller[T]) classify(r *Registration[T], err error) (out error) {
	if err == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			out = NewError("INTERNAL", "Provider error classifier panicked.")
		}
	}()
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &ResourceOperationError{Code: ErrorCode(err), Err: err}
	}
	var typed *ResourceOperationError
	if errors.As(err, &typed) {
		return err
	}
	if r != nil && r.ErrorClassifier != nil {
		if out := r.ErrorClassifier.ClassifyError(err); out != nil {
			return out
		}
	}
	if c.cfg.ErrorClassifier != nil {
		if out := c.cfg.ErrorClassifier.ClassifyError(err); out != nil {
			return out
		}
	}
	return err
}

func (c *Controller[T]) guard(r **Registration[T], err *error) {
	if recover() != nil {
		*err = NewError("INTERNAL", "Provider handler panicked.")
	}
	*err = c.classify(*r, *err)
}

func (c *Controller[T]) routeContext(ctx context.Context, route AccessRoute) (context.Context, context.CancelFunc, error) {
	if route.Connection == "" {
		return nil, nil, NewError("INVALID_REQUEST", "An explicit connection route is required.")
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return nil, nil, NewError("CANCELLED", "Provider is closed.")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	return ctx, func() { stop(); cancel() }, nil
}

func (c *Controller[T]) lease(ctx context.Context, route AccessRoute) (context.Context, *T, func(), error) {
	ctx, cancel, err := c.routeContext(ctx, route)
	if err != nil {
		return nil, nil, nil, err
	}
	if c.cfg.Lifecycle == nil {
		return ctx, nil, cancel, nil
	}
	if held, ok := ctx.Value(catalogSlotKey{}).(*clientSlot[T]); ok {
		c.mu.Lock()
		current := c.clients[route.Connection] == held && !held.stopping && held.ctx.Err() == nil
		if current {
			held.users.Add(1)
		}
		c.mu.Unlock()
		if !current {
			cancel()
			return nil, nil, nil, NewError("RESET_REQUIRED", "The admitted client generation has retired.")
		}
		stop := context.AfterFunc(held.ctx, cancel)
		return ctx, held.client, func() { stop(); cancel(); held.users.Done() }, nil
	}
	c.mu.Lock()
	s := c.clients[route.Connection]
	if s == nil || s.stopping {
		c.mu.Unlock()
		cancel()
		return nil, nil, nil, NewError("INVALID_REQUEST", "Connection is not started.")
	}
	c.mu.Unlock()
	select {
	case <-s.ready:
	case <-ctx.Done():
		cancel()
		return nil, nil, nil, ctx.Err()
	}
	c.mu.Lock()
	if s.err != nil || s.stopping || c.closed || c.clients[route.Connection] != s {
		c.mu.Unlock()
		cancel()
		return nil, nil, nil, NewError("CANCELLED", "Connection is stopping or failed to start.")
	}
	s.users.Add(1)
	connection := cloneConnection(c.connections[route.Connection])
	c.mu.Unlock()
	stop := context.AfterFunc(s.ctx, cancel)
	session, _ := SessionFromContext(ctx)
	session.Connection = &connection
	ctx = WithSession(ctx, session)
	ctx = context.WithValue(ctx, catalogSlotKey{}, s)
	return ctx, s.client, func() { stop(); cancel(); s.users.Done() }, nil
}

// WithConnection holds an already-connected native client generation for
// utility work. Metadata is only a selector; the callback receives the actual
// configured profile. No probe, implicit start or default selection occurs.
func (c *Controller[T]) WithConnection(ctx context.Context, route AccessRoute, fn func(context.Context, types.Connection) error) (err error) {
	var registration *Registration[T]
	defer c.guard(&registration, &err)
	if c.cfg.Lifecycle == nil {
		return NewError("UNSUPPORTED", "Connected utility context is unavailable.")
	}
	if fn == nil {
		return NewError("INVALID_REQUEST", "Utility callback is required.")
	}
	if _, err = c.connection(ctx, route.Connection); err != nil {
		return err
	}
	native, _, done, err := c.lease(ctx, route)
	if err != nil {
		if ErrorCode(err) == "INVALID_REQUEST" {
			return NewError("CONNECTION_ERROR", "Selected connection is not started.")
		}
		return err
	}
	defer done()
	session, _ := SessionFromContext(native)
	if session.Connection == nil {
		return NewError("INTERNAL", "Active client has no configured profile.")
	}
	err = fn(native, *session.Connection)
	if err == nil {
		err = native.Err()
	}
	return err
}

// WithClient holds an active client lease for discovery or provider-specific
// work. Stop and Close cancel the callback and wait before destroying its client.
func (c *Controller[T]) WithClient(ctx context.Context, route AccessRoute, fn func(context.Context, *T) error) (err error) {
	var registration *Registration[T]
	defer c.guard(&registration, &err)
	ctx, client, done, err := c.lease(ctx, route)
	if err != nil {
		return err
	}
	defer done()
	if fn == nil {
		return NewError("INVALID_REQUEST", "Client callback is required.")
	}
	err = fn(ctx, client)
	if err == nil {
		err = ctx.Err()
	}
	return err
}

func (c *Controller[T]) registrations(ctx context.Context, route AccessRoute, typ string) ([]Registration[T], error) {
	var result []Registration[T]
	for _, r := range c.cfg.Registrations {
		if r.Type == typ && (r.Connection == "" || r.Connection == route.Connection) {
			result = append(result, r)
		}
	}
	if c.cfg.ResolveRegistration != nil {
		r, err := c.cfg.ResolveRegistration(ctx, route, typ)
		if err != nil {
			return nil, err
		}
		if r.Type != typ || (r.Connection != "" && r.Connection != route.Connection) {
			return nil, NewError("INTERNAL", "Dynamic registration changed the requested type or route.")
		}
		if r.SyncPolicy < SyncOnConnect || r.SyncPolicy > SyncNever {
			return nil, NewError("INTERNAL", "Dynamic registration has an unknown sync policy.")
		}
		result = append(result, r)
	}
	return result, nil
}

func handles[T any](r Registration[T], op Operation) bool {
	switch op {
	case OperationGet:
		return r.Get != nil
	case OperationEnumerate:
		return r.Enumerate != nil
	case OperationNativeQuery:
		return r.NativeQuery != nil
	case OperationMutation:
		return r.Mutate != nil
	case OperationAction:
		return r.Action != nil
	case OperationUpdate:
		return r.Watch != nil && r.SyncPolicy != SyncNever
	default:
		return false
	}
}

// intersectScope narrows a catalog declaration to a registration restriction.
func intersectScope(a, b QueryScope) (QueryScope, bool) {
	if b == nil {
		return a, true
	}
	if len(a) != len(b) {
		return nil, false
	}
	out := make(QueryScope, 0, len(a))
	for _, ac := range a {
		var bc *ScopeConstraint
		for i := range b {
			if b[i].Dimension == ac.Dimension {
				bc = &b[i]
				break
			}
		}
		if bc == nil {
			return nil, false
		}
		if ac.Match == ScopeAny {
			out = append(out, *bc)
			continue
		}
		if bc.Match == ScopeAny {
			out = append(out, ac)
			continue
		}
		if ac.Values == nil || bc.Values == nil {
			return nil, false
		}
		values := make([]string, 0)
		for _, av := range *ac.Values {
			for _, bv := range *bc.Values {
				if av == bv {
					values = append(values, av)
					break
				}
			}
		}
		out = append(out, ScopeConstraint{Dimension: ac.Dimension, Match: ScopeOneOf, Values: &values})
	}
	return out, true
}

func scopeContains(outer, inner QueryScope) bool {
	if outer == nil {
		return true
	}
	if len(outer) != len(inner) {
		return false
	}
	for _, wanted := range inner {
		found := false
		for _, allowed := range outer {
			if allowed.Dimension != wanted.Dimension {
				continue
			}
			found = true
			if allowed.Match == ScopeAny {
				break
			}
			if wanted.Match != ScopeOneOf || wanted.Values == nil || allowed.Values == nil {
				return false
			}
			for _, value := range *wanted.Values {
				ok := false
				for _, permit := range *allowed.Values {
					if value == permit {
						ok = true
						break
					}
				}
				if !ok {
					return false
				}
			}
			break
		}
		if !found {
			return false
		}
	}
	return true
}

func (c *Controller[T]) Catalog(ctx context.Context, request CatalogRequest) (catalog Catalog, err error) {
	var registration *Registration[T]
	defer c.guard(&registration, &err)
	ctx, _, done, err := c.lease(ctx, request.Route)
	if err != nil {
		return Catalog{}, err
	}
	defer done()
	catalog, err = c.cfg.Catalog(ctx, request)
	if err != nil {
		return Catalog{}, err
	}
	if err = ctx.Err(); err != nil {
		return Catalog{}, err
	}
	if err = ValidateCatalog(catalog); err != nil {
		return Catalog{}, err
	}
	catalog.Types = append([]ResourceType(nil), catalog.Types...)
	for i := range catalog.Types {
		typ := &catalog.Types[i]
		registrations, resolveErr := c.registrations(ctx, request.Route, typ.Type)
		if resolveErr != nil {
			return Catalog{}, resolveErr
		}
		caps := make([]Capability, 0, len(typ.Capabilities))
		for _, capability := range typ.Capabilities {
			for _, r := range registrations {
				if !handles(r, capability.Operation) {
					continue
				}
				if r.Scope != nil {
					if err := ValidateScope(r.Scope, typ.QueryDimensions); err != nil {
						return Catalog{}, err
					}
				}
				if scope, ok := intersectScope(capability.Scope, r.Scope); ok {
					candidate := Capability{Operation: capability.Operation, Scope: scope, Guarantee: capability.Guarantee}
					duplicate := false
					for _, previous := range caps {
						if reflect.DeepEqual(previous, candidate) {
							duplicate = true
							break
						}
					}
					if !duplicate {
						caps = append(caps, candidate)
					}
				}
			}
		}
		typ.Capabilities = caps
	}
	nonce := c.catalogNonce
	if slot, ok := ctx.Value(catalogSlotKey{}).(*clientSlot[T]); ok {
		c.mu.Lock()
		current := c.clients[request.Route.Connection] == slot && !slot.stopping && slot.ctx.Err() == nil
		c.mu.Unlock()
		if !current {
			return Catalog{}, NewError("RESET_REQUIRED", "Catalog client generation retired.")
		}
		nonce = slot.catalogNonce
	}
	if ctx.Err() != nil {
		return Catalog{}, NewError("RESET_REQUIRED", "Catalog client generation retired.")
	}
	catalog.Revision = boundCatalogRevision(nonce, catalog.Revision)
	return catalog, nil
}

// ScopeForAddress projects validated identity onto declared query dimensions.
// Selection-only dimensions remain explicit any/unknown. This is support
// qualification only: callers must independently admit trusted access covering
// that entire derived scope and must never infer a narrower native selection.
func ScopeForAddress(address ResourceAddress, descriptor ResourceType) (QueryScope, error) {
	if err := ValidateAddress(address, descriptor.IdentityDimensions); err != nil {
		return nil, err
	}
	if _, err := dimensions(descriptor.QueryDimensions); err != nil {
		return nil, err
	}
	scope, _ := queryScopeForIdentity(address, descriptor.QueryDimensions)
	return scope, nil
}

// queryScopeForIdentity does not infer selection-only dimensions from opaque IDs.
// An unknown dimension is any only for capability qualification, never evidence
// of narrow membership or authority to acquire a wider source.
func queryScopeForIdentity(address ResourceAddress, dimensions []Dimension) (QueryScope, bool) {
	scope := make(QueryScope, 0, len(dimensions))
	exact := true
	for _, dimension := range dimensions {
		constraint := ScopeConstraint{Dimension: dimension.Name, Match: ScopeAny}
		found := false
		for _, entry := range address.Scope {
			if entry.Dimension == dimension.Name {
				values := []string{entry.Value}
				constraint.Match, constraint.Values = ScopeOneOf, &values
				found = true
				break
			}
		}
		if !found {
			exact = false
		}
		scope = append(scope, constraint)
	}
	return scope, exact
}

// identityWithinQueryScope checks only dimensions shared by identity and query.
// Selection-only filtering and its coverage claim belong to the native source.
func identityWithinQueryScope(address ResourceAddress, scope QueryScope) bool {
	for _, entry := range address.Scope {
		for _, constraint := range scope {
			if entry.Dimension != constraint.Dimension {
				continue
			}
			if constraint.Match == ScopeAny {
				break
			}
			found := false
			if constraint.Values != nil {
				for _, value := range *constraint.Values {
					if entry.Value == value {
						found = true
						break
					}
				}
			}
			if !found {
				return false
			}
			break
		}
	}
	return true
}

func exactScopeEntries(entries []ScopeEntry, dimensions []Dimension) (QueryScope, error) {
	scope := make(QueryScope, 0, len(dimensions))
	for _, dimension := range dimensions {
		found := false
		for _, entry := range entries {
			if entry.Dimension == dimension.Name {
				values := []string{entry.Value}
				scope = append(scope, ScopeConstraint{Dimension: dimension.Name, Match: ScopeOneOf, Values: &values})
				found = true
				break
			}
		}
		if !found {
			return nil, NewError("UNSUPPORTED", "Exact identity cannot resolve the declared query scope.")
		}
	}
	return scope, nil
}

type operationTarget struct {
	route          AccessRoute
	provider       string
	typ            string
	representation string
	scope          QueryScope
	address        *ResourceAddress
	operation      Operation
}

func admitTarget(catalog Catalog, target operationTarget) (ResourceType, QueryScope, error) {
	if catalog.Provider != target.provider {
		return ResourceType{}, nil, NewError("INVALID_REQUEST", "Request provider does not match the route catalog.")
	}
	var descriptor *ResourceType
	for i := range catalog.Types {
		if catalog.Types[i].Type == target.typ {
			descriptor = &catalog.Types[i]
			break
		}
	}
	if descriptor == nil {
		return ResourceType{}, nil, NewError("UNSUPPORTED", "Resource type is not declared by the route catalog.")
	}
	scope := target.scope
	if target.address != nil {
		if err := ValidateAddress(*target.address, descriptor.IdentityDimensions); err != nil {
			return ResourceType{}, nil, err
		}
		scope, _ = queryScopeForIdentity(*target.address, descriptor.QueryDimensions)
	} else if err := ValidateScope(scope, descriptor.QueryDimensions); err != nil {
		return ResourceType{}, nil, err
	}
	if target.representation != "" {
		found := false
		for _, rep := range descriptor.Representations {
			if rep.ID == target.representation {
				found = true
				break
			}
		}
		if !found {
			return ResourceType{}, nil, NewError("UNSUPPORTED", "Representation is not declared by the route catalog.")
		}
	}
	for _, capability := range descriptor.Capabilities {
		if capability.Operation == target.operation && scopeContains(capability.Scope, scope) {
			return *descriptor, scope, nil
		}
	}
	return ResourceType{}, nil, NewError("UNSUPPORTED", "Operation is not declared for this type and scope.")
}

func describeOperation(request any) (operationTarget, error) {
	switch request := request.(type) {
	case GetRequest:
		a := request.Reference.Address
		return operationTarget{route: request.Route, provider: a.Provider, typ: a.Type, representation: request.Representation, address: &a, operation: OperationGet}, ValidateGetRequest(request)
	case EnumerateRequest:
		t := request.Target
		return operationTarget{route: t.Route, provider: t.Provider, typ: t.Type, representation: t.Representation, scope: t.Scope, operation: OperationEnumerate}, ValidateEnumerateRequest(request)
	case NativeQueryRequest:
		t := request.Target
		return operationTarget{route: t.Route, provider: t.Provider, typ: t.Type, representation: t.Representation, scope: t.Scope, operation: OperationNativeQuery}, ValidateNativeQueryRequest(request)
	case MutationRequest:
		a := request.Reference.Address
		return operationTarget{route: request.Route, provider: a.Provider, typ: a.Type, address: &a, operation: OperationMutation}, ValidateMutationRequest(request)
	case ActionRequest:
		a := request.Reference.Address
		return operationTarget{route: request.Route, provider: a.Provider, typ: a.Type, address: &a, operation: OperationAction}, ValidateActionRequest(request)
	case ReadTarget:
		return operationTarget{route: request.Route, provider: request.Provider, typ: request.Type, representation: request.Representation, scope: request.Scope, operation: OperationUpdate}, ValidateEnumerateRequest(EnumerateRequest{Target: request})
	default:
		return operationTarget{}, NewError("UNSUPPORTED", "Unsupported finite resource operation.")
	}
}

// AdmitOperation is shared by controller and transport admission. Direct
// providers must declare the route/type/scope and implement the optional method.
// Requests are domain values, not generated transport messages or pointers.
func AdmitOperation(ctx context.Context, provider Provider, request any) (descriptor ResourceType, err error) {
	defer func() {
		if recover() != nil {
			err = NewError("INTERNAL", "Provider admission handler panicked.")
		}
	}()
	target, err := describeOperation(request)
	if err != nil {
		return descriptor, err
	}
	if err := ctx.Err(); err != nil {
		return descriptor, &ResourceOperationError{Code: ErrorCode(err), Err: err}
	}
	supported := false
	switch target.operation {
	case OperationGet:
		_, supported = provider.(Getter)
	case OperationEnumerate:
		_, supported = provider.(Enumerator)
	case OperationNativeQuery:
		_, supported = provider.(NativeQuerier)
	case OperationMutation:
		_, supported = provider.(Mutator)
	case OperationAction:
		_, supported = provider.(Actioner)
	}
	if !supported {
		return descriptor, NewError("UNSUPPORTED", "Operation handler is not implemented.")
	}
	return admitProviderTarget(ctx, provider, target)
}

// AdmitStreamingAction admits the independent streaming action interface without
// requiring a finite Action method or fabricating finite action support.
func AdmitStreamingAction(ctx context.Context, provider Provider, request ActionRequest) (descriptor ResourceType, err error) {
	defer func() {
		if recover() != nil {
			err = NewError("INTERNAL", "Provider admission handler panicked.")
		}
	}()
	target, err := describeOperation(request)
	if err != nil {
		return descriptor, err
	}
	if _, ok := provider.(StreamingActioner); !ok {
		return descriptor, NewError("UNSUPPORTED", "Streaming action handler is not implemented.")
	}
	return admitProviderTarget(ctx, provider, target)
}

func admitProviderTarget(ctx context.Context, provider Provider, target operationTarget) (ResourceType, error) {
	revision, _ := CatalogRevisionForRouteFromContext(ctx, target.provider, target.route)
	if err := ValidateCatalogRevision(revision); err != nil {
		return ResourceType{}, err
	}
	if err := ctx.Err(); err != nil {
		return ResourceType{}, &ResourceOperationError{Code: ErrorCode(err), Err: err}
	}
	catalog, err := provider.Catalog(ctx, CatalogRequest{Route: target.route})
	if err != nil {
		return ResourceType{}, err
	}
	if err := ctx.Err(); err != nil {
		return ResourceType{}, &ResourceOperationError{Code: ErrorCode(err), Err: err}
	}
	if err := ValidateCatalog(catalog); err != nil {
		return ResourceType{}, err
	}
	if err := CheckCatalogRevisionForRoute(ctx, catalog, target.route); err != nil {
		return ResourceType{}, err
	}
	descriptor, _, err := admitTarget(catalog, target)
	return descriptor, err
}

// ValidateOperationResult binds finite provider output to its admitted request
// and catalog descriptor. Native source cursors remain opaque source tokens.
func ValidateOperationResult(request any, result any, descriptor ResourceType) error {
	switch request := request.(type) {
	case GetRequest:
		record, ok := result.(Record)
		if !ok {
			return NewError("INTERNAL", "Get returned the wrong result type.")
		}
		a := request.Reference.Address
		return checkRecord(record, descriptor, a.Provider, a.Authority, request.Representation, &request.Reference, nil)
	case EnumerateRequest:
		page, ok := result.(SourcePage)
		if !ok {
			return NewError("INTERNAL", "Enumeration returned the wrong result type.")
		}
		return checkPage(page, descriptor, request.Target, request.PageSize)
	case NativeQueryRequest:
		page, ok := result.(SourcePage)
		if !ok {
			return NewError("INTERNAL", "Native query returned the wrong result type.")
		}
		return checkPage(page, descriptor, request.Target, request.PageSize)
	case MutationRequest:
		record, ok := result.(Record)
		if !ok {
			return NewError("INTERNAL", "Mutation returned the wrong result type.")
		}
		a := request.Reference.Address
		return checkRecord(record, descriptor, a.Provider, a.Authority, "", &request.Reference, nil)
	case ActionRequest:
		value, ok := result.(Value)
		if !ok {
			return NewError("INTERNAL", "Action returned the wrong result type.")
		}
		return ValidateValue(value)
	default:
		return NewError("UNSUPPORTED", "Unsupported finite resource operation.")
	}
}
func (c *Controller[T]) fenceCatalog(ctx context.Context, route AccessRoute) error {
	if ctx.Err() != nil {
		return NewError("RESET_REQUIRED", "The admitted catalog generation retired during the operation.")
	}
	catalog, err := c.Catalog(ctx, CatalogRequest{Route: route})
	if err != nil {
		return err
	}
	return CheckCatalogRevisionForRoute(ctx, catalog, route)
}

func (c *Controller[T]) resolve(ctx context.Context, route AccessRoute, provider, typ, representation string, scope QueryScope, address *ResourceAddress, op Operation) (*Registration[T], ResourceType, error) {
	revision, _ := CatalogRevisionForRouteFromContext(ctx, provider, route)
	if err := ValidateCatalogRevision(revision); err != nil {
		return nil, ResourceType{}, err
	}
	catalog, err := c.Catalog(ctx, CatalogRequest{Route: route})
	if err != nil {
		return nil, ResourceType{}, err
	}
	if err := CheckCatalogRevisionForRoute(ctx, catalog, route); err != nil {
		return nil, ResourceType{}, err
	}
	descriptor, scope, err := admitTarget(catalog, operationTarget{route: route, provider: provider, typ: typ, representation: representation, scope: scope, address: address, operation: op})
	if err != nil {
		return nil, ResourceType{}, err
	}
	registrations, err := c.registrations(ctx, route, typ)
	if err != nil {
		return nil, ResourceType{}, err
	}
	var selected *Registration[T]
	for i := range registrations {
		r := &registrations[i]
		if handles(*r, op) && scopeContains(r.Scope, scope) {
			if selected != nil {
				return nil, ResourceType{}, NewError("INTERNAL", "Operation has overlapping registrations.")
			}
			selected = r
		}
	}
	if selected == nil {
		return nil, ResourceType{}, NewError("UNSUPPORTED", "Operation handler is not registered.")
	}
	return selected, descriptor, nil
}

func sameAddress(a, b ResourceAddress) bool {
	if a.Provider != b.Provider || a.Authority != b.Authority || a.Type != b.Type || a.ID != b.ID || len(a.Scope) != len(b.Scope) {
		return false
	}
	for _, ae := range a.Scope {
		found := false
		for _, be := range b.Scope {
			if ae == be {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func checkRecord(record Record, descriptor ResourceType, provider, authority, representation string, reference *ResourceReference, scope QueryScope) error {
	if err := ValidateRecord(record); err != nil {
		return err
	}
	a := record.Reference.Address
	if err := ValidateAddress(a, descriptor.IdentityDimensions); err != nil {
		return err
	}
	if a.Provider != provider || a.Authority != authority || a.Type != descriptor.Type {
		return NewError("INTERNAL", "Provider response changed resource target identity.")
	}
	if reference != nil {
		if !sameAddress(a, reference.Address) {
			return NewError("INTERNAL", "Provider response changed exact resource identity.")
		}
		if reference.Incarnation != nil && (record.Reference.Incarnation == nil || *reference.Incarnation != *record.Reference.Incarnation) {
			return NewError("RESET_REQUIRED", "Resource incarnation changed.")
		}
	}
	if scope != nil && !identityWithinQueryScope(a, scope) {
		return NewError("INTERNAL", "Provider response escaped requested identity scope.")
	}
	if representation != "" && record.Representation.ID != representation {
		return NewError("INTERNAL", "Provider response changed requested representation.")
	}
	for _, rep := range descriptor.Representations {
		if reflect.DeepEqual(rep, record.Representation) {
			return nil
		}
	}
	return NewError("INTERNAL", "Provider response representation is not catalog-bound.")
}

func checkPage(page SourcePage, descriptor ResourceType, target ReadTarget, size uint32) error {
	if err := ValidateSourcePage(page); err != nil {
		return err
	}
	if size != 0 && len(page.Records) > int(size) {
		return NewError("INTERNAL", "Provider response exceeded requested source page size.")
	}
	for i, record := range page.Records {
		if err := checkRecord(record, descriptor, target.Provider, target.Authority, target.Representation, nil, target.Scope); err != nil {
			return err
		}
		for j := range i {
			if sameAddress(record.Reference.Address, page.Records[j].Reference.Address) {
				return NewError("INTERNAL", "Source page repeats resource identity.")
			}
		}
	}
	return nil
}

func (c *Controller[T]) Get(ctx context.Context, request GetRequest) (record Record, err error) {
	var r *Registration[T]
	defer c.guard(&r, &err)
	if err = ValidateGetRequest(request); err != nil {
		return record, err
	}
	ctx, client, done, err := c.lease(ctx, request.Route)
	if err != nil {
		return record, err
	}
	defer done()
	a := request.Reference.Address
	var descriptor ResourceType
	r, descriptor, err = c.resolve(ctx, request.Route, a.Provider, a.Type, request.Representation, nil, &a, OperationGet)
	if err != nil {
		return record, err
	}
	scope, exact := queryScopeForIdentity(a, descriptor.QueryDimensions)
	// Unknown selection-only dimensions must not turn an exact Get into an
	// implicit unscoped watch or acquisition. An explicit query can start it.
	if exact {
		if err := c.ensureOnFirstQuery(ctx, ReadTarget{Route: request.Route, Provider: a.Provider, Authority: a.Authority, Type: a.Type, Scope: scope, Representation: request.Representation}); err != nil {
			return record, err
		}
	}
	record, err = r.Get(ctx, client, request)
	if err == nil {
		err = c.fenceCatalog(ctx, request.Route)
	}
	if err == nil {
		err = checkRecord(record, descriptor, a.Provider, a.Authority, request.Representation, &request.Reference, nil)
	}
	return record, err
}

func (c *Controller[T]) Enumerate(ctx context.Context, request EnumerateRequest) (page SourcePage, err error) {
	var r *Registration[T]
	defer c.guard(&r, &err)
	if err = ValidateEnumerateRequest(request); err != nil {
		return page, err
	}
	ctx, client, done, err := c.lease(ctx, request.Target.Route)
	if err != nil {
		return page, err
	}
	defer done()
	t := request.Target
	var descriptor ResourceType
	r, descriptor, err = c.resolve(ctx, t.Route, t.Provider, t.Type, t.Representation, t.Scope, nil, OperationEnumerate)
	if err != nil {
		return page, err
	}
	if err := c.ensureOnFirstQuery(ctx, t); err != nil {
		return page, err
	}
	page, err = r.Enumerate(ctx, client, request)
	if err == nil {
		err = c.fenceCatalog(ctx, t.Route)
	}
	if err == nil {
		err = checkPage(page, descriptor, t, request.PageSize)
	}
	return page, err
}

func (c *Controller[T]) NativeQuery(ctx context.Context, request NativeQueryRequest) (page SourcePage, err error) {
	var r *Registration[T]
	defer c.guard(&r, &err)
	if err = ValidateNativeQueryRequest(request); err != nil {
		return page, err
	}
	ctx, client, done, err := c.lease(ctx, request.Target.Route)
	if err != nil {
		return page, err
	}
	defer done()
	t := request.Target
	var descriptor ResourceType
	r, descriptor, err = c.resolve(ctx, t.Route, t.Provider, t.Type, t.Representation, t.Scope, nil, OperationNativeQuery)
	if err != nil {
		return page, err
	}
	if err := c.ensureOnFirstQuery(ctx, t); err != nil {
		return page, err
	}
	page, err = r.NativeQuery(ctx, client, request)
	if err == nil {
		err = c.fenceCatalog(ctx, t.Route)
	}
	if err == nil {
		err = checkPage(page, descriptor, t, request.PageSize)
	}
	return page, err
}

func (c *Controller[T]) Mutate(ctx context.Context, request MutationRequest) (record Record, err error) {
	var r *Registration[T]
	defer c.guard(&r, &err)
	if err = ValidateMutationRequest(request); err != nil {
		return record, err
	}
	ctx, client, done, err := c.lease(ctx, request.Route)
	if err != nil {
		return record, err
	}
	defer done()
	a := request.Reference.Address
	var descriptor ResourceType
	r, descriptor, err = c.resolve(ctx, request.Route, a.Provider, a.Type, "", nil, &a, OperationMutation)
	if err != nil {
		return record, err
	}
	record, err = r.Mutate(ctx, client, request)
	if err == nil {
		err = c.fenceCatalog(ctx, request.Route)
	}
	if err == nil {
		err = checkRecord(record, descriptor, a.Provider, a.Authority, "", &request.Reference, nil)
	}
	return record, err
}

func (c *Controller[T]) Action(ctx context.Context, request ActionRequest) (value Value, err error) {
	var r *Registration[T]
	defer c.guard(&r, &err)
	if err = ValidateActionRequest(request); err != nil {
		return value, err
	}
	ctx, client, done, err := c.lease(ctx, request.Route)
	if err != nil {
		return value, err
	}
	defer done()
	a := request.Reference.Address
	r, _, err = c.resolve(ctx, request.Route, a.Provider, a.Type, "", nil, &a, OperationAction)
	if err != nil {
		return value, err
	}
	value, err = r.Action(ctx, client, request)
	if err == nil {
		err = c.fenceCatalog(ctx, request.Route)
	}
	if err == nil {
		err = ValidateValue(value)
	}
	return value, err
}

func (c *Controller[T]) requireLifecycle() error {
	if c.cfg.Lifecycle == nil {
		return NewError("UNSUPPORTED", "Provider does not own connection lifecycle.")
	}
	return nil
}

func (c *Controller[T]) LoadConnections(ctx context.Context) (connections []types.Connection, err error) {
	var r *Registration[T]
	defer c.guard(&r, &err)
	if err = c.requireLifecycle(); err != nil {
		return nil, err
	}
	ctx, done, err := c.routeContext(ctx, AccessRoute{Connection: "connection-discovery"})
	if err != nil {
		return nil, err
	}
	defer done()
	connections, err = c.cfg.Lifecycle.LoadConnections(ctx)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = c.ApplyConnections(ctx, connections); err != nil {
		return nil, err
	}
	return c.ListConnections(ctx)
}

func (c *Controller[T]) GetConnection(ctx context.Context, id string) (types.Connection, error) {
	connection, err := c.connection(ctx, id)
	if err != nil {
		return types.Connection{}, err
	}
	return cloneConnection(connection), nil
}

// connection returns an immutable controller-owned value for internal use only.
func (c *Controller[T]) connection(ctx context.Context, id string) (types.Connection, error) {
	if err := c.requireLifecycle(); err != nil {
		return types.Connection{}, err
	}
	if id == "" {
		return types.Connection{}, NewError("INVALID_REQUEST", "An explicit connection ID is required.")
	}
	if err := ctx.Err(); err != nil {
		return types.Connection{}, c.classify(nil, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return types.Connection{}, NewError("CANCELLED", "Provider is closed.")
	}
	connection, ok := c.connections[id]
	if !ok {
		return types.Connection{}, NewError("NOT_FOUND", "Connection was not loaded.")
	}
	return connection, nil
}

func (c *Controller[T]) startClient(s *clientSlot[T]) {
	defer close(s.ready)
	defer func() {
		if recover() != nil {
			s.err = NewError("INTERNAL", "Connection start handler panicked.")
		}
		if s.err != nil {
			c.mu.Lock()
			if c.clients[s.connection.ID] == s && !s.stopping && !c.changing[s.connection.ID] && s.ctx.Err() == nil {
				c.failedStarts[s.connection.ID] = true
			}
			c.mu.Unlock()
		}
	}()
	connection := cloneConnection(s.connection)
	session, _ := SessionFromContext(s.ctx)
	session.Connection = &connection
	ctx := WithSession(s.ctx, session)
	s.client, s.err = c.cfg.Lifecycle.CreateClient(ctx, connection)
	if s.err == nil && s.client == nil {
		s.err = NewError("INTERNAL", "CreateClient returned a nil client.")
	}
	if s.err == nil && c.cfg.Lifecycle.OnStart != nil {
		s.err = c.cfg.Lifecycle.OnStart(ctx, connection, s.client)
	}
	if s.err == nil {
		s.err = s.ctx.Err()
	}
}

func (c *Controller[T]) StartConnection(ctx context.Context, id string) (status types.ConnectionStatus, err error) {
	var r *Registration[T]
	defer c.guard(&r, &err)
	connection, err := c.connection(ctx, id)
	if err != nil {
		return status, err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return status, NewError("CANCELLED", "Provider is closed.")
	}
	if c.changing[id] {
		c.mu.Unlock()
		return status, NewError("CANCELLED", "Connection configuration is changing.")
	}
	var exists bool
	connection, exists = c.connections[id]
	if !exists {
		c.mu.Unlock()
		return status, NewError("NOT_FOUND", "Connection was removed.")
	}
	s := c.clients[id]
	created := s == nil
	if created {
		nonce, nonceErr := newCatalogNonce()
		if nonceErr != nil {
			c.mu.Unlock()
			return status, nonceErr
		}
		clientCtx, cancel := context.WithCancel(c.ctx)
		session, _ := SessionFromContext(ctx)
		session.Connection = nil
		clientCtx = WithSession(clientCtx, session)
		s = &clientSlot[T]{connection: connection, ctx: clientCtx, cancel: cancel, ready: make(chan struct{})}
		s.catalogNonce = nonce
		c.clients[id] = s
		delete(c.failedStarts, id)
	}
	c.mu.Unlock()
	if created {
		stop := context.AfterFunc(ctx, s.cancel)
		c.startClient(s)
		stop()
	}
	select {
	case <-s.ready:
	case <-ctx.Done():
		if created {
			cleanupErr := c.stopSlot(s)
			return status, errors.Join(ctx.Err(), cleanupErr)
		}
		return status, ctx.Err()
	}
	if s.err != nil {
		cleanupErr := c.stopSlot(s)
		return status, errors.Join(s.err, cleanupErr)
	}
	if clientErr := s.ctx.Err(); clientErr != nil {
		cleanupErr := c.stopSlot(s)
		return status, errors.Join(clientErr, cleanupErr)
	}
	c.mu.Lock()
	stopping := s.stopping || c.closed
	c.mu.Unlock()
	if stopping {
		return status, NewError("CANCELLED", "Connection is stopping.")
	}
	connection = cloneConnection(connection)
	return types.ConnectionStatus{Connection: &connection, Status: types.ConnectionStatusConnected}, nil
}

func lifecycleCall(fn func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = NewError("INTERNAL", "Connection teardown handler panicked.")
		}
	}()
	return fn()
}

func (c *Controller[T]) stopSlot(s *clientSlot[T]) error {
	c.mu.Lock()
	s.stopping = true
	s.cancel()
	c.mu.Unlock()
	s.destroy.Do(func() {
		<-s.ready
		s.destroyErr = errors.Join(s.destroyErr, c.updates.stopRoute(s.connection.ID))
		s.users.Wait()
		if s.client != nil {
			// Cleanup must not inherit the canceled client context.
			ctx := context.WithoutCancel(s.ctx)
			connection := cloneConnection(s.connection)
			session, _ := SessionFromContext(ctx)
			session.Connection = &connection
			ctx = WithSession(ctx, session)
			if c.cfg.Lifecycle.OnStop != nil {
				s.destroyErr = lifecycleCall(func() error { return c.cfg.Lifecycle.OnStop(ctx, connection, s.client) })
			}
			s.destroyErr = errors.Join(s.destroyErr, lifecycleCall(func() error { return c.cfg.Lifecycle.DestroyClient(ctx, s.client) }))
		}
		c.mu.Lock()
		if c.clients[s.connection.ID] == s {
			delete(c.clients, s.connection.ID)
		}
		c.mu.Unlock()
	})
	return s.destroyErr
}

func (c *Controller[T]) StopConnection(ctx context.Context, id string) (connection types.Connection, err error) {
	var r *Registration[T]
	defer c.guard(&r, &err)
	connection, err = c.GetConnection(ctx, id)
	if err != nil {
		return connection, err
	}
	c.mu.Lock()
	s := c.clients[id]
	delete(c.failedStarts, id)
	if s != nil {
		s.stopping = true
		s.cancel()
	}
	c.mu.Unlock()
	if s == nil {
		return connection, nil
	}
	return connection, c.stopSlot(s)
}

// GetConnectionStatus snapshots the sole configured-client registry without
// starting, waiting, probing or performing catalog/resource work. CONNECTED
// means registration only, not network health or resource-source readiness.
func (c *Controller[T]) GetConnectionStatus(ctx context.Context, id string) (types.ConnectionStatus, error) {
	if err := c.requireLifecycle(); err != nil {
		return types.ConnectionStatus{}, err
	}
	if id == "" {
		return types.ConnectionStatus{}, NewError("INVALID_REQUEST", "An explicit connection ID is required.")
	}
	if err := ctx.Err(); err != nil {
		return types.ConnectionStatus{}, c.classify(nil, err)
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return types.ConnectionStatus{}, NewError("CANCELLED", "Provider is closed.")
	}
	connection, exists := c.connections[id]
	if !exists {
		c.mu.Unlock()
		return types.ConnectionStatus{}, NewError("NOT_FOUND", "Connection was not loaded.")
	}
	state := types.ConnectionStatusDisconnected
	if c.failedStarts[id] {
		state = types.ConnectionStatusFailed
	}
	slot := c.clients[id]
	if !c.failedStarts[id] && slot != nil && !slot.stopping && !c.changing[id] && slot.ctx.Err() == nil {
		select {
		case <-slot.ready:
			if slot.err == nil {
				state = types.ConnectionStatusConnected
			} else {
				state = types.ConnectionStatusFailed
			}
		default:
			state = types.ConnectionStatusPending
		}
	}
	c.mu.Unlock()
	owned := cloneConnection(connection)
	return types.ConnectionStatus{Connection: &owned, Status: state}, nil
}

func (c *Controller[T]) CheckConnection(ctx context.Context, id string) (status types.ConnectionStatus, err error) {
	var r *Registration[T]
	defer c.guard(&r, &err)
	connection, err := c.connection(ctx, id)
	if err != nil {
		return status, err
	}
	c.mu.Lock()
	s := c.clients[id]
	c.mu.Unlock()
	if s == nil {
		connection = cloneConnection(connection)
		return types.ConnectionStatus{Connection: &connection, Status: types.ConnectionStatusDisconnected}, nil
	}
	ctx, client, done, err := c.lease(ctx, AccessRoute{Connection: id})
	if err != nil {
		return status, err
	}
	defer done()
	session, _ := SessionFromContext(ctx)
	connection = *session.Connection
	if c.cfg.Lifecycle.CheckConnection == nil {
		return types.ConnectionStatus{Connection: &connection, Status: types.ConnectionStatusConnected}, nil
	}
	status, err = c.cfg.Lifecycle.CheckConnection(ctx, connection, client)
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && (status.Connection == nil || status.Connection.ID != id) {
		err = NewError("INTERNAL", "Connection check changed the requested connection.")
	}
	if status.Connection != nil {
		owned := cloneConnection(*status.Connection)
		status.Connection = &owned
	}
	return status, err
}

func (c *Controller[T]) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.cancel()
		slots := make([]*clientSlot[T], 0, len(c.clients))
		for _, s := range c.clients {
			s.stopping = true
			s.cancel()
			slots = append(slots, s)
		}
		c.mu.Unlock()
		c.updates.stopAll()
		for _, s := range slots {
			c.closeErr = errors.Join(c.closeErr, c.stopSlot(s))
		}
		c.closeErr = c.classify(nil, c.closeErr)
	})
	return c.closeErr
}

var _ Provider = (*Controller[struct{}])(nil)
var _ ConnectionLifecycle = (*Controller[struct{}])(nil)
var _ ConnectionReadinessProvider = (*Controller[struct{}])(nil)
var _ Closer = (*Controller[struct{}])(nil)

// AdmitCatalogOperation uses an already acquired catalog without another
// discovery RPC. A ReadTarget requests advisory update capability admission.
func AdmitCatalogOperation(catalog Catalog, request any) (ResourceType, error) {
	if err := ValidateCatalog(catalog); err != nil {
		return ResourceType{}, err
	}
	target, err := describeOperation(request)
	if err != nil {
		return ResourceType{}, err
	}
	descriptor, _, err := admitTarget(catalog, target)
	return descriptor, err
}

// AdmitBestEffortTarget checks the independently optional advisory update
// interface and its exact catalog scope. It makes no P05 continuity promise.
func AdmitBestEffortTarget(ctx context.Context, provider Provider, target ReadTarget) (descriptor ResourceType, err error) {
	defer func() {
		if recover() != nil {
			err = NewError("INTERNAL", "Provider admission handler panicked.")
		}
	}()
	operation, err := describeOperation(target)
	if err != nil {
		return descriptor, err
	}
	if _, ok := provider.(BestEffortUpdateProvider); !ok {
		return descriptor, NewError("UNSUPPORTED", "Best-effort update handler is not implemented.")
	}
	return admitProviderTarget(ctx, provider, operation)
}

// ListConnections returns the managed connection inventory, not an implicit
// selection. Native inventory watching belongs to a ConnectionBrowser provider.
func (c *Controller[T]) ListConnections(ctx context.Context) ([]types.Connection, error) {
	if err := c.requireLifecycle(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, c.classify(nil, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, NewError("CANCELLED", "Provider is closed.")
	}
	return connectionSnapshot(c.connections), nil
}

// NativeConnections snapshots the latest successful native discovery before
// overlay application. It neither reloads discovery nor exposes manual profiles.
func (c *Controller[T]) NativeConnections(ctx context.Context) ([]types.Connection, error) {
	if err := c.requireLifecycle(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, c.classify(nil, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, NewError("CANCELLED", "Provider is closed.")
	}
	return connectionSnapshot(c.nativeConnections), nil
}

func connectionSnapshot(inventory map[string]types.Connection) []types.Connection {
	ids := make([]string, 0, len(inventory))
	for id := range inventory {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	connections := make([]types.Connection, 0, len(ids))
	for _, id := range ids {
		connections = append(connections, cloneConnection(inventory[id]))
	}
	return connections
}

// ApplyConnections replaces only the native discovery snapshot. Explicit host
// overrides and disabled IDs survive refreshes and native disappearance.
func (c *Controller[T]) ApplyConnections(ctx context.Context, connections []types.Connection) error {
	if err := c.requireLifecycle(); err != nil {
		return err
	}
	if err := ValidateMetadata(connections); err != nil {
		return err
	}
	loaded := make(map[string]types.Connection, len(connections))
	for _, connection := range connections {
		if connection.ID == "" {
			return NewError("INVALID_REQUEST", "Connection requires an explicit ID.")
		}
		if _, exists := loaded[connection.ID]; exists {
			return NewError("INVALID_REQUEST", "Connection inventory contains duplicate IDs.")
		}
		loaded[connection.ID] = cloneConnection(connection)
	}
	c.configMu.Lock()
	defer c.configMu.Unlock()
	return c.applyInventory(ctx, loaded, c.overrides, c.disabled)
}

// ApplyConnectionOverlay replaces the entire host-owned desired overlay.
// Validation and client teardown must succeed before the new desired state is
// published. The controller never writes native configuration files.
func (c *Controller[T]) ApplyConnectionOverlay(ctx context.Context, overlay ConnectionOverlay) error {
	if err := c.requireLifecycle(); err != nil {
		return err
	}
	if err := ValidateConnectionOverlay(overlay); err != nil {
		return err
	}
	overrides := make(map[string]types.Connection, len(overlay.Upserts))
	for _, connection := range overlay.Upserts {
		overrides[connection.ID] = cloneConnection(connection)
	}
	disabled := make(map[string]struct{}, len(overlay.DisabledIDs))
	for _, id := range overlay.DisabledIDs {
		disabled[id] = struct{}{}
	}
	c.configMu.Lock()
	defer c.configMu.Unlock()
	return c.applyInventory(ctx, c.nativeConnections, overrides, disabled)
}

// applyInventory is serialized by configMu. Staged route changes block new
// clients until canceled generations drain, then publish one effective inventory.
// Failed teardown leaves the prior desired state, but retired clients stay retired.
func (c *Controller[T]) applyInventory(ctx context.Context, native, overrides map[string]types.Connection, disabled map[string]struct{}) error {
	if err := ctx.Err(); err != nil {
		return c.classify(nil, err)
	}
	effective := make(map[string]types.Connection, len(native)+len(overrides))
	for id, connection := range native {
		if _, hidden := disabled[id]; !hidden {
			effective[id] = connection
		}
	}
	for id, connection := range overrides {
		effective[id] = connection
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return NewError("CANCELLED", "Provider is closed.")
	}
	// Host overlays omit private credentials. Merge only into the ephemeral
	// effective inventory, never into the host-owned desired overlay.
	for id, override := range overrides {
		if override.GetSensitiveData() != nil {
			continue
		}
		source, exists := native[id]
		candidate := override
		if exists {
			candidate.SetSensitiveData(source.GetSensitiveData())
			if SameConnectionClientConfig(candidate, source) {
				effective[id] = candidate
				continue
			}
		}
		if source, exists = c.connections[id]; exists {
			candidate.SetSensitiveData(source.GetSensitiveData())
			if SameConnectionClientConfig(candidate, source) {
				effective[id] = candidate
			}
		}
	}
	changed := make([]string, 0)
	for id, previous := range c.connections {
		current, exists := effective[id]
		if !exists || !SameConnectionClientConfig(previous, current) {
			c.changing[id] = true
			changed = append(changed, id)
		} else {
			if current.LastRefresh.IsZero() {
				current.LastRefresh = previous.LastRefresh
			}
			if current.ExpiryTime == 0 {
				current.ExpiryTime = previous.ExpiryTime
			}
			if current.Client == nil {
				current.Client = previous.Client
			}
			effective[id] = current
		}
	}
	for id := range effective {
		if _, exists := c.connections[id]; !exists {
			c.changing[id] = true
			changed = append(changed, id)
		}
	}
	var retired []*clientSlot[T]
	for _, id := range changed {
		delete(c.failedStarts, id)
		if slot := c.clients[id]; slot != nil {
			slot.stopping = true
			slot.cancel()
			retired = append(retired, slot)
		}
	}
	c.mu.Unlock()
	var err error
	for _, slot := range retired {
		err = errors.Join(err, c.stopSlot(slot))
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		err = errors.Join(err, ctxErr)
	}
	c.mu.Lock()
	if c.closed && err == nil {
		err = NewError("CANCELLED", "Provider is closed.")
	}
	if err == nil {
		c.nativeConnections = native
		c.overrides = overrides
		c.disabled = disabled
		c.connections = effective
	}
	for _, id := range changed {
		delete(c.changing, id)
	}
	c.mu.Unlock()
	return c.classify(nil, err)
}

func copyConnectionOverlay(upserts map[string]types.Connection, disabledIDs map[string]struct{}) (map[string]types.Connection, map[string]struct{}) {
	overrides := make(map[string]types.Connection, len(upserts)+1)
	for id, connection := range upserts {
		overrides[id] = connection
	}
	disabled := make(map[string]struct{}, len(disabledIDs))
	for id := range disabledIDs {
		disabled[id] = struct{}{}
	}
	return overrides, disabled
}

// UpdateConnection upserts an explicitly identified desired record. Native
// refreshes cannot overwrite it; ApplyConnectionOverlay owns reset/restore.
func (c *Controller[T]) UpdateConnection(ctx context.Context, connection types.Connection) (types.Connection, error) {
	if err := c.requireLifecycle(); err != nil {
		return types.Connection{}, err
	}
	if err := ValidateConnectionOverlay(ConnectionOverlay{Upserts: []types.Connection{connection}}); err != nil {
		return types.Connection{}, err
	}
	c.configMu.Lock()
	defer c.configMu.Unlock()
	overrides, disabled := copyConnectionOverlay(c.overrides, c.disabled)
	connection = cloneConnection(connection)
	overrides[connection.ID] = connection
	delete(disabled, connection.ID)
	if err := c.applyInventory(ctx, c.nativeConnections, overrides, disabled); err != nil {
		return types.Connection{}, err
	}
	return cloneConnection(connection), nil
}

// DeleteConnection hides a native route or removes a manual desired profile.
// Its tombstone survives native refresh until the host replaces the overlay.
func (c *Controller[T]) DeleteConnection(ctx context.Context, id string) error {
	if err := c.requireLifecycle(); err != nil {
		return err
	}
	if id == "" {
		return NewError("INVALID_REQUEST", "An explicit connection ID is required.")
	}
	c.configMu.Lock()
	defer c.configMu.Unlock()
	c.mu.Lock()
	_, exists := c.connections[id]
	c.mu.Unlock()
	if !exists {
		return NewError("NOT_FOUND", "Connection was not loaded.")
	}
	overrides, disabled := copyConnectionOverlay(c.overrides, c.disabled)
	delete(overrides, id)
	if _, native := c.nativeConnections[id]; native {
		disabled[id] = struct{}{}
	} else {
		delete(disabled, id)
	}
	return c.applyInventory(ctx, c.nativeConnections, overrides, disabled)
}

// Features reports installed ancillary implementations rather than Controller's
// static method set. Wrappers with additional native traits extend this result.
func (c *Controller[T]) Features(ctx context.Context) (FeatureSet, error) {
	if err := ctx.Err(); err != nil {
		return FeatureSet{}, c.classify(nil, err)
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return FeatureSet{}, NewError("CANCELLED", "Provider is closed.")
	}
	features := FeatureSet{Traits: []Feature{}}
	if c.cfg.Lifecycle != nil {
		features.Traits = append(features.Traits, FeatureConnections, FeatureConnectionReadiness, FeatureConnectionOverlay)
	}
	updates := c.cfg.DynamicUpdates
	for _, registration := range c.cfg.Registrations {
		if handles(registration, OperationUpdate) {
			updates = true
			break
		}
	}
	if updates {
		features.Traits = append(features.Traits, FeatureBestEffortUpdates)
	}
	return features, nil
}

var _ FeatureNegotiator = (*Controller[struct{}])(nil)

var _ ConnectionOverlayProvider = (*Controller[struct{}])(nil)
