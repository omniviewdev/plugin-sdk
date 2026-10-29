package resourcev2

import "context"

// Get acquires one canonical record using an explicit representation and accepted
// route catalog observation. It leases the native client, requires both catalog
// capability and a matching Get registration, then fences the catalog and validates
// result identity/provenance before success. It never starts a connection.
// Callback inputs/results follow Registration's ownership and concurrency rules.
// Cancellation or a retired admission can reject a callback's otherwise successful
// result; a nonnil error means the returned record is not accepted.
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

// Enumerate acquires one bounded source page for an admitted target, not a host
// query result or snapshot/live handoff. It leases an already-started generation
// when Lifecycle is configured and requires an exact catalog capability and
// callback. Page size, identity, membership, representation, and catalog fences
// apply before success. Source cursors retain native semantics.
// Inputs/results follow Registration's ownership rules; ignore the page on error.
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

// NativeQuery invokes one admitted provider-native query and validates its bounded
// source page. Native query support is independent of enumeration; it is not a
// universal query planner. Catalog capability and a matching callback are both
// required. The call leases the generation without implicitly starting it and
// rejects stale catalog or generation results.
// Inputs/results follow Registration's ownership rules; ignore the page on error.
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

// Mutate dispatches only the requested create, update, or delete verb after exact
// catalog and registration admission. One Mutate callback does not advertise all
// verbs. The call leases the native generation and validates returned identity
// after the catalog fence. It never starts clients or grants resource authority.
// Inputs/results follow Registration's ownership rules. An error can occur after
// the native write, so it does not prove the side effect failed or permit blind
// replay; the returned record is not accepted on error.
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
	r, descriptor, err = c.resolve(ctx, request.Route, a.Provider, a.Type, "", nil, &a, mutationOperation(request.Operation))
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

// Action dispatches a finite action under exact catalog/registration admission and
// a native lease, then fences the catalog and validates the returned Value.
// StreamingActioner is independent and is not invoked here. Inputs/results follow
// Registration's ownership rules. An error can follow a native side effect and is
// not evidence that replay is safe; ignore the returned Value on error.
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
