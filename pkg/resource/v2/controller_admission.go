package resourcev2

import (
	"context"
	"reflect"
	"slices"
)

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
	case OperationCreate, OperationMutationUpdate, OperationDelete:
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
		constraint, ok := intersectConstraint(ac, scopeConstraint(b, ac.Dimension))
		if !ok {
			return nil, false
		}
		out = append(out, constraint)
	}
	return out, true
}

func intersectConstraint(a ScopeConstraint, b *ScopeConstraint) (ScopeConstraint, bool) {
	if b == nil {
		return ScopeConstraint{}, false
	}
	if a.Match == ScopeAny {
		return *b, true
	}
	if b.Match == ScopeAny {
		return a, true
	}
	if a.Values == nil || b.Values == nil {
		return ScopeConstraint{}, false
	}
	values := make([]string, 0)
	for _, value := range *a.Values {
		if slices.Contains(*b.Values, value) {
			values = append(values, value)
		}
	}
	return ScopeConstraint{Dimension: a.Dimension, Match: ScopeOneOf, Values: &values}, true
}

func scopeContains(outer, inner QueryScope) bool {
	if outer == nil {
		return true
	}
	if len(outer) != len(inner) {
		return false
	}
	for _, wanted := range inner {
		allowed := scopeConstraint(outer, wanted.Dimension)
		if allowed == nil || !constraintContains(*allowed, wanted) {
			return false
		}
	}
	return true
}

func scopeConstraint(scope QueryScope, dimension string) *ScopeConstraint {
	for i := range scope {
		if scope[i].Dimension == dimension {
			return &scope[i]
		}
	}
	return nil
}

func constraintContains(allowed, wanted ScopeConstraint) bool {
	if allowed.Match == ScopeAny {
		return true
	}
	if wanted.Match != ScopeOneOf || wanted.Values == nil || allowed.Values == nil {
		return false
	}
	for _, value := range *wanted.Values {
		if !slices.Contains(*allowed.Values, value) {
			return false
		}
	}
	return true
}

// Catalog discovers one explicit route under a generation lease without starting
// clients or requiring an accepted catalog observation. It validates callback
// output, intersects advertised capabilities with installed registrations/scopes,
// and binds the revision to this controller or native generation.
// Callback containers must remain stable during SDK use; the result is not a
// blanket deep copy. Catalog callbacks may reenter WithClient using the supplied
// context, but must not synchronously retire their leased generation.
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
		if err = c.catalogCapabilities(ctx, request.Route, typ); err != nil {
			return Catalog{}, err
		}
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

// catalogCapabilities intersects declared support with installed registration
// scopes without changing the provider-owned capability slice.
func (c *Controller[T]) catalogCapabilities(ctx context.Context, route AccessRoute, typ *ResourceType) error {
	registrations, err := c.registrations(ctx, route, typ.Type)
	if err != nil {
		return err
	}
	caps := make([]Capability, 0, len(typ.Capabilities))
	for _, capability := range typ.Capabilities {
		for _, registration := range registrations {
			candidate, ok, candidateErr := registeredCapability(capability, registration, typ.QueryDimensions)
			if candidateErr != nil {
				return candidateErr
			}
			if !ok {
				continue
			}
			if !slices.ContainsFunc(caps, func(previous Capability) bool {
				return reflect.DeepEqual(previous, candidate)
			}) {
				caps = append(caps, candidate)
			}
		}
	}
	typ.Capabilities = caps
	return nil
}

func registeredCapability[T any](
	capability Capability,
	registration Registration[T],
	dimensions []Dimension,
) (Capability, bool, error) {
	if !handles(registration, capability.Operation) {
		return Capability{}, false, nil
	}
	if registration.Scope != nil {
		if err := ValidateScope(registration.Scope, dimensions); err != nil {
			return Capability{}, false, err
		}
	}
	scope, ok := intersectScope(capability.Scope, registration.Scope)
	return Capability{Operation: capability.Operation, Scope: scope, Guarantee: capability.Guarantee}, ok, nil
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
		constraint := scopeConstraint(scope, entry.Dimension)
		if constraint == nil || constraint.Match == ScopeAny {
			continue
		}
		if constraint.Values == nil || !slices.Contains(*constraint.Values, entry.Value) {
			return false
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
	if target.representation != "" && !declaresRepresentation(descriptor, target.representation) {
		return ResourceType{}, nil, NewError("UNSUPPORTED", "Representation is not declared by the route catalog.")
	}
	for _, capability := range descriptor.Capabilities {
		if capability.Operation == target.operation && scopeContains(capability.Scope, scope) {
			return *descriptor, scope, nil
		}
	}
	return ResourceType{}, nil, NewError("UNSUPPORTED", "Operation is not declared for this type and scope.")
}

func declaresRepresentation(descriptor *ResourceType, id string) bool {
	for i := range descriptor.Representations {
		if descriptor.Representations[i].ID == id {
			return true
		}
	}
	return false
}

// mutationOperation keeps mutation update distinct from advisory watch delivery.
func mutationOperation(verb string) Operation {
	switch verb {
	case "create":
		return OperationCreate
	case "update":
		return OperationMutationUpdate
	case "delete":
		return OperationDelete
	default:
		return ""
	}
}

func describeOperation(request any) (operationTarget, error) {
	switch request := request.(type) {
	case GetRequest:
		a := request.Reference.Address
		validationErr := ValidateGetRequest(request)
		return operationTarget{
			route:          request.Route,
			provider:       a.Provider,
			typ:            a.Type,
			representation: request.Representation,
			address:        &a,
			operation:      OperationGet,
		}, validationErr
	case EnumerateRequest:
		t := request.Target
		validationErr := ValidateEnumerateRequest(request)
		return operationTarget{
			route:          t.Route,
			provider:       t.Provider,
			typ:            t.Type,
			representation: t.Representation,
			scope:          t.Scope,
			operation:      OperationEnumerate,
		}, validationErr
	case NativeQueryRequest:
		t := request.Target
		validationErr := ValidateNativeQueryRequest(request)
		return operationTarget{
			route:          t.Route,
			provider:       t.Provider,
			typ:            t.Type,
			representation: t.Representation,
			scope:          t.Scope,
			operation:      OperationNativeQuery,
		}, validationErr
	case MutationRequest:
		a := request.Reference.Address
		validationErr := ValidateMutationRequest(request)
		return operationTarget{
			route:     request.Route,
			provider:  a.Provider,
			typ:       a.Type,
			address:   &a,
			operation: mutationOperation(request.Operation),
		}, validationErr
	case ActionRequest:
		a := request.Reference.Address
		validationErr := ValidateActionRequest(request)
		return operationTarget{
			route:     request.Route,
			provider:  a.Provider,
			typ:       a.Type,
			address:   &a,
			operation: OperationAction,
		}, validationErr
	case ReadTarget:
		validationErr := ValidateEnumerateRequest(EnumerateRequest{Target: request})
		return operationTarget{
			route:          request.Route,
			provider:       request.Provider,
			typ:            request.Type,
			representation: request.Representation,
			scope:          request.Scope,
			operation:      OperationUpdate,
		}, validationErr
	default:
		return operationTarget{}, NewError("UNSUPPORTED", "Unsupported finite resource operation.")
	}
}

// AdmitOperation is shared by controller and transport admission. Direct
// providers must declare the route/type/scope and implement the optional method.
// Requests are domain values, not generated transport messages or pointers.
// Use a nonnil provider and context. Finite request values, not pointers, are
// accepted; unsupported shapes or missing optional traits return UNSUPPORTED.
// This calls Catalog and requires the exact accepted route observation. It does
// not invoke the operation, retain a lease after return, or authorize the caller.
// Providers/callers must still fence dispatch and results against retirement.
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
	case OperationCreate, OperationMutationUpdate, OperationDelete:
		_, supported = provider.(Mutator)
	case OperationAction:
		_, supported = provider.(Actioner)
	case OperationUpdate:
		// Advisory targets use AdmitBestEffortTarget, not finite admission.
		supported = false
	}
	if !supported {
		return descriptor, NewError("UNSUPPORTED", "Operation handler is not implemented.")
	}
	return admitProviderTarget(ctx, provider, target)
}

// AdmitStreamingAction admits the independent streaming action interface without
// requiring a finite Action method or fabricating finite action support.
// It requires a nonnil provider/context and an accepted route catalog observation.
// It calls Catalog but not StreamAction, acquires no lasting lease, and grants no
// authority. Admission success cannot replace a result or generation fence.
func AdmitStreamingAction(
	ctx context.Context,
	provider Provider,
	request ActionRequest,
) (descriptor ResourceType, err error) {
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
	if err = ctx.Err(); err != nil {
		return ResourceType{}, &ResourceOperationError{Code: ErrorCode(err), Err: err}
	}
	if err = ValidateCatalog(catalog); err != nil {
		return ResourceType{}, err
	}
	if err = CheckCatalogRevisionForRoute(ctx, catalog, target.route); err != nil {
		return ResourceType{}, err
	}
	descriptor, _, err := admitTarget(catalog, target)
	return descriptor, err
}

// ValidateOperationResult binds finite provider output to its admitted request
// and catalog descriptor. Native source cursors remain opaque source tokens.
// Pass matching domain request/result values, not pointers; nil or mismatched
// result types fail validation. This checks shapes and binding, not authorization,
// current catalog equality, side-effect success, or source continuity.
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

func (c *Controller[T]) resolve(
	ctx context.Context,
	route AccessRoute,
	provider, typ, representation string,
	scope QueryScope,
	address *ResourceAddress,
	op Operation,
) (*Registration[T], ResourceType, error) {
	revision, _ := CatalogRevisionForRouteFromContext(ctx, provider, route)
	if err := ValidateCatalogRevision(revision); err != nil {
		return nil, ResourceType{}, err
	}
	catalog, err := c.Catalog(ctx, CatalogRequest{Route: route})
	if err != nil {
		return nil, ResourceType{}, err
	}
	if err = CheckCatalogRevisionForRoute(ctx, catalog, route); err != nil {
		return nil, ResourceType{}, err
	}
	descriptor, scope, err := admitTarget(
		catalog,
		operationTarget{
			route:          route,
			provider:       provider,
			typ:            typ,
			representation: representation,
			scope:          scope,
			address:        address,
			operation:      op,
		},
	)
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
	if a.Provider != b.Provider || a.Authority != b.Authority || a.Type != b.Type || a.ID != b.ID ||
		len(a.Scope) != len(b.Scope) {
		return false
	}
	for _, ae := range a.Scope {
		if !slices.Contains(b.Scope, ae) {
			return false
		}
	}
	return true
}

func checkRecord(
	record Record,
	descriptor ResourceType,
	provider, authority, representation string,
	reference *ResourceReference,
	scope QueryScope,
) error {
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
		if reference.Incarnation != nil &&
			(record.Reference.Incarnation == nil || *reference.Incarnation != *record.Reference.Incarnation) {
			return NewError("RESET_REQUIRED", "Resource incarnation changed.")
		}
	}
	if scope != nil && !identityWithinQueryScope(a, scope) {
		return NewError("INTERNAL", "Provider response escaped requested identity scope.")
	}
	if representation != "" && record.Representation.ID != representation {
		return NewError("INTERNAL", "Provider response changed requested representation.")
	}
	for i := range descriptor.Representations {
		if reflect.DeepEqual(descriptor.Representations[i], record.Representation) {
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
	for i := range page.Records {
		record := &page.Records[i]
		if err := checkRecord(
			*record,
			descriptor,
			target.Provider,
			target.Authority,
			target.Representation,
			nil,
			target.Scope,
		); err != nil {
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

// AdmitCatalogOperation uses an already acquired catalog without another
// discovery RPC. A ReadTarget requests advisory update capability admission.
// It validates domain request values against declared support only; it neither
// checks a provider's optional Go traits nor an accepted/current revision pair.
// No native work, lasting lease, or access authorization occurs. Keep catalog
// containers stable while using the returned descriptor, which can share them.
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
// A nonnil provider/context and accepted route catalog observation are required.
// It calls Catalog without starting a source or retaining a lease; callers must
// separately fence native startup, retries, and delivery. It grants no authority.
func AdmitBestEffortTarget(
	ctx context.Context,
	provider Provider,
	target ReadTarget,
) (descriptor ResourceType, err error) {
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
