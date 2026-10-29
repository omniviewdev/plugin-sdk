package resourcev2

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

const maxPendingUpdateStates = 1000

// updateListener ports the bounded state replay barrier. Data events remain
// advisory direct delivery; this is not a replay log or a continuity protocol.
type updateListener struct {
	ctx       context.Context
	sink      WatchEventSink
	retired   atomic.Bool
	accepted  bool
	mu        sync.Mutex
	dataReady bool
	replaying bool
	overflow  bool
	pending   []WatchStateEvent
	failed    chan error
	callbacks sync.WaitGroup
}

func (l *updateListener) active() bool { return !l.retired.Load() && l.ctx.Err() == nil }
func (l *updateListener) terminalError() error {
	select {
	case err := <-l.failed:
		return err
	default:
	}
	if err := l.ctx.Err(); err != nil {
		return err
	}
	return context.Canceled
}
func (l *updateListener) call(fn func()) { l.dispatch(false, fn) }

// Data received before readiness and state replay completes is dropped. Updates
// are advisory, so this barrier retains no data and makes no continuity claim.
func (l *updateListener) data(fn func()) { l.dispatch(true, fn) }

func (l *updateListener) dispatch(data bool, fn func()) {
	l.mu.Lock()
	if !l.active() || (data && !l.dataReady) {
		l.mu.Unlock()
		return
	}
	l.callbacks.Add(1)
	l.mu.Unlock()
	defer l.callbacks.Done()
	defer func() {
		if recover() != nil {
			l.mu.Lock()
			l.retired.Store(true)
			l.mu.Unlock()
			select {
			case l.failed <- NewError("INTERNAL", "Update listener panicked."):
			default:
			}
		}
	}()
	fn()
}
func (l *updateListener) state(event WatchStateEvent) {
	l.mu.Lock()
	if !l.active() {
		l.mu.Unlock()
		return
	}
	if l.replaying {
		if len(l.pending) < maxPendingUpdateStates {
			l.pending = append(l.pending, event)
		} else {
			l.overflow = true
		}
		l.mu.Unlock()
		return
	}
	l.mu.Unlock()
	l.call(func() { l.sink.OnStateChange(event) })
}
func (l *updateListener) replay() error {
	for {
		l.mu.Lock()
		if !l.active() {
			l.mu.Unlock()
			return l.terminalError()
		}
		if l.overflow {
			l.mu.Unlock()
			return NewError("RESOURCE_LIMIT", "Update state replay exceeded its bounded buffer.")
		}
		pending := l.pending
		l.pending = nil
		if len(pending) == 0 {
			l.replaying = false
			l.dataReady = true
			l.mu.Unlock()
			return nil
		}
		l.mu.Unlock()
		for _, event := range pending {
			if !l.active() {
				return l.terminalError()
			}
			l.call(func() { l.sink.OnStateChange(event) })
		}
	}
}

type updateWatch struct {
	target        ReadTarget
	ctx           context.Context
	cancel        context.CancelFunc
	ready         chan struct{}
	done          chan struct{}
	running       bool
	state         WatchState
	count         Count
	validationErr error
}

type updateManager[T any] struct {
	controller     *Controller[T]
	mu             sync.Mutex
	watches        map[string]map[string]*updateWatch
	stopping       map[string]int
	listenersMu    sync.Mutex
	listeners      []*updateListener
	readyListeners int
	pending        []WatchStateEvent
	overflow       bool
	baseBackoff    time.Duration
	maxRetries     int
}

func newUpdateManager[T any](controller *Controller[T]) *updateManager[T] {
	return &updateManager[T]{controller: controller, watches: make(map[string]map[string]*updateWatch), stopping: make(map[string]int), baseBackoff: 500 * time.Millisecond, maxRetries: 5}
}

func (m *updateManager[T]) snapshot() []*updateListener {
	m.listenersMu.Lock()
	listeners := m.listeners
	m.listenersMu.Unlock()
	return listeners
}
func (m *updateManager[T]) publishState(watch *updateWatch, event WatchStateEvent) {
	event.Route = watch.target.Route
	event.Type = watch.target.Type
	event.Guarantee = BestEffortGuarantee
	if event.Count.Kind == "" {
		event.Count = Count{Kind: "unknown"}
	}
	m.mu.Lock()
	watch.state = event.State
	watch.count = event.Count
	m.mu.Unlock()
	m.listenersMu.Lock()
	listeners := m.listeners
	if m.readyListeners == 0 {
		if len(m.pending) < maxPendingUpdateStates {
			m.pending = append(m.pending, event)
		} else {
			m.overflow = true
		}
	}
	m.listenersMu.Unlock()
	for _, listener := range listeners {
		listener.state(event)
	}
}
func (m *updateManager[T]) remove(listener *updateListener) {
	m.listenersMu.Lock()
	listener.mu.Lock()
	listener.retired.Store(true)
	listener.pending = nil
	listener.mu.Unlock()
	remaining := make([]*updateListener, 0, len(m.listeners))
	for _, current := range m.listeners {
		if current != listener {
			remaining = append(remaining, current)
		}
	}
	m.listeners = remaining
	if listener.accepted {
		m.readyListeners--
		listener.accepted = false
	}
	m.listenersMu.Unlock()
	// Retirement closes callback admission under listener.mu. Only callbacks
	// admitted before that barrier remain, and none may outlive listener return.
	listener.callbacks.Wait()
}
func (m *updateManager[T]) listen(ctx context.Context, sink WatchEventSink, onReady func() error) error {
	if sink == nil {
		return NewError("INVALID_REQUEST", "Update sink is required.")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.listenersMu.Lock()
	if m.overflow {
		m.listenersMu.Unlock()
		return NewError("RESOURCE_LIMIT", "Update state replay exceeded its bounded buffer.")
	}
	listener := &updateListener{ctx: ctx, sink: sink, replaying: true, pending: append([]WatchStateEvent(nil), m.pending...), failed: make(chan error, 1)}
	m.listeners = append(m.listeners[:len(m.listeners):len(m.listeners)], listener)
	m.listenersMu.Unlock()
	defer m.remove(listener)
	if onReady != nil {
		if err := onReady(); err != nil {
			return err
		}
	}
	if err := listener.replay(); err != nil {
		return err
	}
	m.listenersMu.Lock()
	if !listener.active() {
		m.listenersMu.Unlock()
		return listener.terminalError()
	}
	listener.accepted = true
	m.readyListeners++
	m.pending = nil
	m.overflow = false
	m.listenersMu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-listener.failed:
		return err
	}
}

func copyUpdateRepresentation(representation Representation) Representation {
	schema := &representation.Schema
	schema.Revision = copyToken(schema.Revision)
	schema.UnavailableReason = copyToken(schema.UnavailableReason)
	schema.Document = append(schema.Document[:0:0], schema.Document...)
	schema.Unvalidated = copyStrings(schema.Unvalidated)
	if schema.DocumentRef != nil {
		ref := *schema.DocumentRef
		schema.DocumentRef = &ref
	}
	return representation
}

func copyUpdateDescriptor(descriptor ResourceType) ResourceType {
	descriptor.IdentityDimensions = append([]Dimension(nil), descriptor.IdentityDimensions...)
	descriptor.QueryDimensions = append([]Dimension(nil), descriptor.QueryDimensions...)
	descriptor.Representations = append([]Representation(nil), descriptor.Representations...)
	for i := range descriptor.Representations {
		descriptor.Representations[i] = copyUpdateRepresentation(descriptor.Representations[i])
	}
	descriptor.Capabilities = append([]Capability(nil), descriptor.Capabilities...)
	for i := range descriptor.Capabilities {
		descriptor.Capabilities[i].Scope = cloneReadTarget(ReadTarget{Scope: descriptor.Capabilities[i].Scope}).Scope
	}
	if descriptor.NativeAuthorityHint != nil {
		hint := *descriptor.NativeAuthorityHint
		descriptor.NativeAuthorityHint = &hint
	}
	return descriptor
}

func (m *updateManager[T]) start(ctx context.Context, target ReadTarget, registration *Registration[T], descriptor ResourceType) error {
	target = cloneReadTarget(target)
	descriptor = copyUpdateDescriptor(descriptor)
	selectedRevision, _ := CatalogRevisionForRouteFromContext(ctx, target.Provider, target.Route)
	if err := ValidateCatalogRevision(selectedRevision); err != nil {
		return err
	}
	// Retain the admitted observation, never a later route-map selection.
	base := WithCatalogRevision(context.WithoutCancel(ctx), selectedRevision)
	leaseCtx, client, release, err := m.controller.lease(base, target.Route)
	if err != nil {
		return err
	}
	if err := m.controller.fenceCatalog(leaseCtx, target.Route); err != nil {
		release()
		return err
	}
	watchCtx, cancel := context.WithCancel(leaseCtx)
	watch := &updateWatch{target: target, ctx: watchCtx, cancel: cancel, ready: make(chan struct{}), done: make(chan struct{}), running: true, state: WatchStateIdle, count: Count{Kind: "unknown"}}
	var representation Representation
	for _, candidate := range descriptor.Representations {
		if candidate.ID == target.Representation {
			representation = candidate
			break
		}
	}
	m.mu.Lock()
	if m.stopping[target.Route.Connection] != 0 {
		m.mu.Unlock()
		cancel()
		release()
		return NewError("CANCELLED", "Connection updates are stopping.")
	}
	resources := m.watches[target.Route.Connection]
	if resources == nil {
		resources = make(map[string]*updateWatch)
		m.watches[target.Route.Connection] = resources
	}
	if existing := resources[target.Type]; existing != nil {
		previous := existing.target
		if previous.Provider != target.Provider || previous.Authority != target.Authority || previous.Type != target.Type || previous.Representation != target.Representation || !scopeContains(previous.Scope, target.Scope) {
			m.mu.Unlock()
			cancel()
			release()
			return NewError("RESET_REQUIRED", "Update target changed; stop the existing resource watch first.")
		}
		if existing.running {
			previousRevision, previousPinned := CatalogRevisionFromContext(existing.ctx)
			requestedRevision, requestedPinned := CatalogRevisionForRouteFromContext(leaseCtx, target.Provider, target.Route)
			if !previousPinned || !requestedPinned || previousRevision != requestedRevision {
				m.mu.Unlock()
				cancel()
				release()
				return NewError("RESET_REQUIRED", "Update catalog revision changed; stop the existing resource watch first.")
			}
			m.mu.Unlock()
			cancel()
			release()
			return nil
		}
		select {
		case <-existing.done:
		default:
			m.mu.Unlock()
			cancel()
			release()
			return NewError("CANCELLED", "Previous resource watch is still stopping.")
		}
	}
	if watchCtx.Err() != nil {
		m.mu.Unlock()
		cancel()
		release()
		return watchCtx.Err()
	}
	resources[target.Type] = watch
	m.mu.Unlock()
	sink := &updateSourceSink[T]{manager: m, watch: watch, descriptor: descriptor}
	go m.run(watchCtx, client, release, watch, registration, representation, sink)
	select {
	case <-watch.ready:
		if err := m.controller.fenceCatalog(leaseCtx, target.Route); err != nil {
			cancel()
			return err
		}
		return nil
	case <-ctx.Done():
		cancel()
		<-watch.done
		return ctx.Err()
	}
}

func invokeUpdate[T any](ctx context.Context, client *T, target ReadTarget, registration *Registration[T], representation Representation, sink WatchEventSink) (err error) {
	defer func() {
		if recover() != nil {
			err = NewError("INTERNAL", "Provider update handler panicked.")
		}
	}()
	return registration.Watch(ctx, client, cloneReadTarget(target), copyUpdateRepresentation(representation), sink)
}
func (m *updateManager[T]) run(ctx context.Context, client *T, release func(), watch *updateWatch, registration *Registration[T], representation Representation, sink WatchEventSink) {
	defer close(watch.done)
	defer release()
	defer watch.cancel()
	close(watch.ready)
	for attempt := 0; ; attempt++ {
		if ctx.Err() != nil {
			m.finish(watch, WatchStateStopped, nil)
			return
		}
		if err := m.controller.fenceCatalog(ctx, watch.target.Route); err != nil {
			m.finish(watch, WatchStateFailed, err)
			return
		}
		err := invokeUpdate(ctx, client, watch.target, registration, representation, sink)
		m.mu.Lock()
		validationErr := watch.validationErr
		m.mu.Unlock()
		if validationErr != nil {
			m.finish(watch, WatchStateFailed, validationErr)
			return
		}
		if ctx.Err() != nil || err == nil {
			m.finish(watch, WatchStateStopped, nil)
			return
		}
		err = m.controller.classify(registration, err)
		if attempt >= m.maxRetries {
			m.finish(watch, WatchStateFailed, err)
			return
		}
		m.publishState(watch, WatchStateEvent{State: WatchStateError, Count: Count{Kind: "unknown"}, Error: updateError(err), Message: "Native update failed; retrying with bounded backoff."})
		backoff := m.baseBackoff * time.Duration(1<<attempt)
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			m.finish(watch, WatchStateStopped, nil)
			return
		case <-timer.C:
		}
	}
}
func updateError(err error) *ResourceOperationError {
	if err == nil {
		return nil
	}
	var typed *ResourceOperationError
	if errors.As(err, &typed) {
		return typed
	}
	return &ResourceOperationError{Code: ErrorCode(err), Message: "Native update source failed.", Err: err}
}
func (m *updateManager[T]) finish(watch *updateWatch, state WatchState, err error) {
	m.mu.Lock()
	watch.running = false
	m.mu.Unlock()
	m.publishState(watch, WatchStateEvent{State: state, Count: Count{Kind: "unknown"}, Error: updateError(err)})
}
func (m *updateManager[T]) stopResource(connection, typ string) error {
	m.mu.Lock()
	watch := m.watches[connection][typ]
	if watch != nil {
		watch.cancel()
	}
	m.mu.Unlock()
	if watch != nil {
		<-watch.done
		m.mu.Lock()
		if m.watches[connection][typ] == watch {
			delete(m.watches[connection], typ)
			if len(m.watches[connection]) == 0 {
				delete(m.watches, connection)
			}
		}
		m.mu.Unlock()
	}
	return nil
}
func (m *updateManager[T]) stopRoute(connection string) error {
	m.mu.Lock()
	m.stopping[connection]++
	resources := m.watches[connection]
	watches := make([]*updateWatch, 0, len(resources))
	for _, watch := range resources {
		watch.cancel()
		watches = append(watches, watch)
	}
	m.mu.Unlock()
	for _, watch := range watches {
		<-watch.done
	}
	m.mu.Lock()
	for _, watch := range watches {
		if m.watches[connection][watch.target.Type] == watch {
			delete(m.watches[connection], watch.target.Type)
		}
	}
	if len(m.watches[connection]) == 0 {
		delete(m.watches, connection)
	}
	m.stopping[connection]--
	if m.stopping[connection] == 0 {
		delete(m.stopping, connection)
	}
	m.mu.Unlock()
	return nil
}
func (m *updateManager[T]) stopAll() {
	m.mu.Lock()
	watches := make([]*updateWatch, 0)
	for _, resources := range m.watches {
		for _, watch := range resources {
			watch.cancel()
			watches = append(watches, watch)
		}
	}
	m.mu.Unlock()
	for _, watch := range watches {
		<-watch.done
	}
}

type updateSourceSink[T any] struct {
	manager    *updateManager[T]
	watch      *updateWatch
	descriptor ResourceType
}

func (s *updateSourceSink[T]) reject(err error) {
	s.manager.mu.Lock()
	if s.watch.validationErr == nil {
		s.watch.validationErr = err
	}
	s.watch.cancel()
	s.manager.mu.Unlock()
}
func (s *updateSourceSink[T]) record(record Record) error {
	target := s.watch.target
	return checkRecord(record, s.descriptor, target.Provider, target.Authority, target.Representation, nil, target.Scope)
}
func (s *updateSourceSink[T]) admitted() bool {
	if s.watch.ctx.Err() != nil {
		return false
	}
	if err := s.manager.controller.fenceCatalog(s.watch.ctx, s.watch.target.Route); err != nil {
		s.reject(err)
		return false
	}
	return true
}

func (s *updateSourceSink[T]) OnAdd(payload WatchAddPayload) {
	if !s.admitted() {
		return
	}
	if err := s.record(payload.Record); err != nil {
		s.reject(err)
		return
	}
	payload.Route, payload.Guarantee = s.watch.target.Route, BestEffortGuarantee
	for _, listener := range s.manager.snapshot() {
		listener.data(func() { listener.sink.OnAdd(payload) })
	}
}
func (s *updateSourceSink[T]) OnUpdate(payload WatchUpdatePayload) {
	if !s.admitted() {
		return
	}
	if err := s.record(payload.Record); err != nil {
		s.reject(err)
		return
	}
	payload.Route, payload.Guarantee = s.watch.target.Route, BestEffortGuarantee
	for _, listener := range s.manager.snapshot() {
		listener.data(func() { listener.sink.OnUpdate(payload) })
	}
}
func (s *updateSourceSink[T]) OnDelete(payload WatchDeletePayload) {
	if !s.admitted() {
		return
	}
	if err := validateReference(payload.Reference); err != nil {
		s.reject(err)
		return
	}
	a, target := payload.Reference.Address, s.watch.target
	if a.Provider != target.Provider || a.Authority != target.Authority || a.Type != target.Type {
		s.reject(NewError("INTERNAL", "Update deletion changed target identity."))
		return
	}
	if err := ValidateAddress(a, s.descriptor.IdentityDimensions); err != nil {
		s.reject(err)
		return
	}
	if !identityWithinQueryScope(a, target.Scope) {
		s.reject(NewError("INTERNAL", "Update deletion escaped target scope."))
		return
	}
	if payload.LastKnown != nil {
		if err := checkRecord(*payload.LastKnown, s.descriptor, target.Provider, target.Authority, target.Representation, &payload.Reference, target.Scope); err != nil {
			s.reject(err)
			return
		}
	}
	payload.Route, payload.Guarantee = target.Route, BestEffortGuarantee
	for _, listener := range s.manager.snapshot() {
		listener.data(func() { listener.sink.OnDelete(payload) })
	}
}
func (s *updateSourceSink[T]) OnStateChange(event WatchStateEvent) {
	if !s.admitted() {
		return
	}
	if event.State < WatchStateIdle || event.State > WatchStateSkipped {
		s.reject(NewError("INTERNAL", "Native update emitted an unknown state."))
		return
	}
	if event.Count.Kind == "" {
		event.Count = Count{Kind: "unknown"}
	}
	if err := validateCount(event.Count); err != nil {
		s.reject(err)
		return
	}
	s.manager.publishState(s.watch, event)
}

// StartConnectionWatch admits every supplied target before starting sources whose
// registration uses SyncOnConnect. Nil/empty targets are a no-op. Each target
// needs its accepted catalog observation; no connection starts implicitly.
// If starting a later source fails, earlier sources are not rolled back.
// Success means source registration, not native readiness or gap-free continuity.
// Retained targets/descriptors are copied. Sources outlive this request after
// acceptance and end on explicit stop, generation retirement, or controller close.
func (c *Controller[T]) StartConnectionWatch(ctx context.Context, targets []ReadTarget) (err error) {
	var registration *Registration[T]
	defer c.guard(&registration, &err)
	// Admit every target before starting any native source.
	type admitted struct {
		target       ReadTarget
		registration *Registration[T]
		descriptor   ResourceType
		ctx          context.Context
	}
	operations := make([]admitted, 0, len(targets))
	for _, target := range targets {
		if err := ValidateEnumerateRequest(EnumerateRequest{Target: target}); err != nil {
			return err
		}
		targetCtx, _, release, err := c.lease(ctx, target.Route)
		if err != nil {
			return err
		}
		defer release()
		r, descriptor, err := c.resolve(targetCtx, target.Route, target.Provider, target.Type, target.Representation, target.Scope, nil, OperationUpdate)
		if err != nil {
			return err
		}
		operations = append(operations, admitted{target, r, descriptor, targetCtx})
	}
	for _, operation := range operations {
		if operation.registration.SyncPolicy == SyncOnConnect {
			if err := c.updates.start(operation.ctx, operation.target, operation.registration, operation.descriptor); err != nil {
				return err
			}
		}
	}
	return nil
}

// EnsureResourceWatch admits and starts or reuses one advisory resource source.
// It requires exact update capability, a Watch callback, and an accepted catalog
// observation; SyncNever disables support. A running source can cover a narrower
// requested scope only with matching identity, representation, and revision.
// Changed targets/revisions require explicit stop and fresh admission.
//
// Success registers the source, not native readiness or replay continuity.
// Admission copies retained target/descriptor containers. Accepted sources outlive
// request completion, retain their generation lease, and recheck admission before
// native retries. Explicit stop or generation retirement cancels and drains them.
func (c *Controller[T]) EnsureResourceWatch(ctx context.Context, target ReadTarget) (err error) {
	var registration *Registration[T]
	defer c.guard(&registration, &err)
	if err := ValidateEnumerateRequest(EnumerateRequest{Target: target}); err != nil {
		return err
	}
	ctx, _, release, err := c.lease(ctx, target.Route)
	if err != nil {
		return err
	}
	defer release()
	registration, descriptor, err := c.resolve(ctx, target.Route, target.Provider, target.Type, target.Representation, target.Scope, nil, OperationUpdate)
	if err != nil {
		return err
	}
	if registration.SyncPolicy == SyncNever {
		return NewError("UNSUPPORTED", "Resource updates are disabled by sync policy.")
	}
	return c.updates.start(ctx, target, registration, descriptor)
}

// StopConnectionWatch cancels and drains every advisory source on an explicit
// route without stopping its client. It holds a generation lease and checks the
// accepted catalog observation before and after stopping; missing watches are a
// no-op. Cancellation after retirement begins does not skip draining.
// Do not call synchronously from a source callback or sink delivery being drained.
func (c *Controller[T]) StopConnectionWatch(ctx context.Context, route AccessRoute) error {
	if route.Connection == "" {
		return NewError("INVALID_REQUEST", "An explicit connection route is required.")
	}
	ctx, _, release, err := c.lease(ctx, route)
	if err != nil {
		return err
	}
	defer release()
	if err := c.fenceCatalog(ctx, route); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return c.classify(nil, err)
	}
	if err := c.updates.stopRoute(route.Connection); err != nil {
		return err
	}
	return c.fenceCatalog(ctx, route)
}

// StopResourceWatch cancels and drains the route/type source without stopping its
// client. Missing sources are a no-op. Exact route, type, generation lease, and
// accepted catalog observation are required; final admission can fail after the
// source has already stopped. Do not call synchronously from that source or sink.
func (c *Controller[T]) StopResourceWatch(ctx context.Context, route AccessRoute, typ string) error {
	if route.Connection == "" || typ == "" {
		return NewError("INVALID_REQUEST", "An explicit route and resource type are required.")
	}
	ctx, _, release, err := c.lease(ctx, route)
	if err != nil {
		return err
	}
	defer release()
	if err := c.fenceCatalog(ctx, route); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return c.classify(nil, err)
	}
	if err := c.updates.stopResource(route.Connection, typ); err != nil {
		return err
	}
	return c.fenceCatalog(ctx, route)
}

// RestartResourceWatch admits the target, stops its route/type source, then
// ensures a new source. It is not an atomic handoff: a failed restart may leave
// no running source, and no replay or gap-free delivery is promised.
// StopResourceWatch and EnsureResourceWatch lifetime/reentrancy rules apply.
func (c *Controller[T]) RestartResourceWatch(ctx context.Context, target ReadTarget) error {
	if _, err := AdmitBestEffortTarget(ctx, c, target); err != nil {
		return err
	}
	if err := c.StopResourceWatch(ctx, target.Route, target.Type); err != nil {
		return err
	}
	return c.EnsureResourceWatch(ctx, target)
}

// HasWatch reports whether any source entry exists for an admitted route,
// including completed entries retained for state inspection. It does not mean a
// native source is currently running or healthy. Invalid routes, cancellation,
// lease failures, and stale/missing catalog observations all return false.
// Use IsResourceWatchRunning or GetWatchState when that distinction matters.
func (c *Controller[T]) HasWatch(ctx context.Context, route AccessRoute) bool {
	if ctx.Err() != nil || route.Connection == "" {
		return false
	}
	ctx, _, release, err := c.lease(ctx, route)
	if err != nil {
		return false
	}
	defer release()
	if err := c.fenceCatalog(ctx, route); err != nil {
		return false
	}
	c.updates.mu.Lock()
	running := len(c.updates.watches[route.Connection]) != 0
	c.updates.mu.Unlock()
	return c.fenceCatalog(ctx, route) == nil && running
}

// IsResourceWatchRunning snapshots the advisory manager's running flag for one
// explicit route/type under a generation lease and accepted catalog observation.
// Missing entries return false, nil; acquisition/admission errors remain errors.
// Running means manager activity, not native readiness, health, or continuity.
func (c *Controller[T]) IsResourceWatchRunning(ctx context.Context, route AccessRoute, typ string) (bool, error) {
	if route.Connection == "" || typ == "" {
		return false, NewError("INVALID_REQUEST", "An explicit route and resource type are required.")
	}
	ctx, _, release, err := c.lease(ctx, route)
	if err != nil {
		return false, err
	}
	defer release()
	if err := c.fenceCatalog(ctx, route); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, c.classify(nil, err)
	}
	c.updates.mu.Lock()
	watch := c.updates.watches[route.Connection][typ]
	running := watch != nil && watch.running
	c.updates.mu.Unlock()
	if err := c.fenceCatalog(ctx, route); err != nil {
		return false, err
	}
	return running, nil
}

// GetWatchState returns an independently owned advisory summary under a
// generation lease and accepted catalog observation. A route with no retained
// source entries returns nil, NOT_FOUND. Counts remain native observations:
// unknown is not zero, and BestEffortGuarantee does not promise replay continuity.
func (c *Controller[T]) GetWatchState(ctx context.Context, route AccessRoute) (*WatchConnectionSummary, error) {
	if route.Connection == "" {
		return nil, NewError("INVALID_REQUEST", "An explicit connection route is required.")
	}
	ctx, _, release, err := c.lease(ctx, route)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := c.fenceCatalog(ctx, route); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, c.classify(nil, err)
	}
	c.updates.mu.Lock()
	resources := c.updates.watches[route.Connection]
	if resources == nil {
		c.updates.mu.Unlock()
		return nil, NewError("NOT_FOUND", "Connection has no registered updates.")
	}
	summary := &WatchConnectionSummary{Route: route, Resources: make(map[string]WatchState, len(resources)), ResourceCounts: make(map[string]Count, len(resources)), Guarantee: BestEffortGuarantee}
	for typ, watch := range resources {
		summary.Resources[typ] = watch.state
		summary.ResourceCounts[typ] = watch.count
	}
	c.updates.mu.Unlock()
	if err := c.fenceCatalog(ctx, route); err != nil {
		return nil, err
	}
	return summary, nil
}

// ListenForEvents registers a listener until caller/controller cancellation or a
// listener failure. A nil sink is invalid; nil onReady accepts without a callback.
// It does not start sources. onReady must succeed before state replay and data
// delivery; pre-ready advisory data is dropped, not buffered for replay.
//
// Sink callbacks can run concurrently and must return promptly. Cancellation
// retires this listener and waits for its admitted callbacks, not the underlying
// generation-owned sources. Do not synchronously stop a delivering source from
// its sink callback or wait there for this listener to return.
// Framework code owns delivery/completion; the sink must not close its channels.
func (c *Controller[T]) ListenForEvents(ctx context.Context, sink WatchEventSink, onReady func() error) (err error) {
	var registration *Registration[T]
	defer c.guard(&registration, &err)
	features, err := c.Features(ctx)
	if err != nil {
		return err
	}
	if !features.Has(FeatureBestEffortUpdates) {
		return NewError("UNSUPPORTED", "Best-effort updates are not installed.")
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	defer func() { stop(); cancel() }()
	return c.updates.listen(ctx, sink, onReady)
}

var _ BestEffortUpdateProvider = (*Controller[struct{}])(nil)

func (c *Controller[T]) ensureOnFirstQuery(ctx context.Context, target ReadTarget) error {
	registrations, err := c.registrations(ctx, target.Route, target.Type)
	if err != nil {
		return err
	}
	for _, registration := range registrations {
		if registration.Watch == nil || registration.SyncPolicy != SyncOnFirstQuery || !scopeContains(registration.Scope, target.Scope) {
			continue
		}
		err := c.EnsureResourceWatch(ctx, target)
		// A finite query does not imply that the catalog declared live support.
		if ErrorCode(err) == "UNSUPPORTED" {
			return nil
		}
		return err
	}
	return nil
}
