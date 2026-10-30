package resourcev2

import (
	"context"
	"errors"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

func (c *Controller[T]) routeContext(
	ctx context.Context,
	route AccessRoute,
) (context.Context, context.CancelFunc, error) {
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
// The callback and context are lease-bound: return before retiring this generation,
// and do not retain native work beyond the lease without explicit session cleanup.
// The supplied profile owns its mutable configuration; editing it changes no
// registry state. A nil callback is invalid; absent Lifecycle is UNSUPPORTED.
// Unknown profiles are NOT_FOUND and configured unstarted profiles are
// CONNECTION_ERROR. Callback success still fails if its lease context was canceled.
func (c *Controller[T]) WithConnection(
	ctx context.Context,
	route AccessRoute,
	fn func(context.Context, types.Connection) error,
) (err error) {
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
// No implicit start, probe, default route, or resource authorization occurs.
// With no Lifecycle the callback receives nil; otherwise the selected generation
// must already be started. A nil callback is invalid. The client is borrowed only
// until callback return, and concurrent callbacks can share it. Honor the supplied
// context and never synchronously retire the same generation while holding its
// lease. A nil callback error does not suppress lease-context cancellation.
func (c *Controller[T]) WithClient(
	ctx context.Context,
	route AccessRoute,
	fn func(context.Context, *T) error,
) (err error) {
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

func (c *Controller[T]) requireLifecycle() error {
	if c.cfg.Lifecycle == nil {
		return NewError("UNSUPPORTED", "Provider does not own connection lifecycle.")
	}
	return nil
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

// StartConnection starts one configured profile or joins its in-flight startup.
// The creator's cancellation cancels startup until construction completes;
// cancellation of another waiter only ends that wait. Accepted generations then
// outlive the initiating request. CreateClient and OnStart must both succeed
// before CONNECTED, which reports lifecycle registration, not network health.
//
// Failure drains and destroys any returned client and retains only safe FAILED
// status. A later explicit call retries with a fresh generation. Unknown IDs are
// NOT_FOUND; lifecycle-less controllers return UNSUPPORTED. Calls may overlap
// readiness inspection, but callbacks must not wait on their own pending start.
func (c *Controller[T]) StartConnection(ctx context.Context, id string) (status types.ConnectionStatus, err error) {
	var r *Registration[T]
	defer c.guard(&r, &err)
	_, err = c.connection(ctx, id)
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
	connection, exists := c.connections[id]
	if !exists {
		c.mu.Unlock()
		return status, NewError("NOT_FOUND", "Connection was removed.")
	}
	s, created, slotErr := c.startSlotLocked(ctx, connection)
	if slotErr != nil {
		c.mu.Unlock()
		return status, slotErr
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

// startSlotLocked publishes one pending generation under mu. stopSlot owns its
// cancellation and teardown; only the creator attaches startup request cancellation.
func (c *Controller[T]) startSlotLocked(
	ctx context.Context,
	connection types.Connection,
) (*clientSlot[T], bool, error) {
	if s := c.clients[connection.ID]; s != nil {
		return s, false, nil
	}
	nonce, err := newCatalogNonce()
	if err != nil {
		return nil, false, err
	}
	s := &clientSlot[T]{connection: connection, ready: make(chan struct{}), catalogNonce: nonce}
	//nolint:gosec // G118: stopSlot cancels and drains the owned generation.
	s.ctx, s.cancel = context.WithCancel(c.ctx)
	session, _ := SessionFromContext(ctx)
	session.Connection = nil
	s.ctx = WithSession(s.ctx, session)
	c.clients[connection.ID] = s
	delete(c.failedStarts, connection.ID)
	return s, true, nil
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
			s.destroyErr = errors.Join(
				s.destroyErr,
				lifecycleCall(func() error { return c.cfg.Lifecycle.DestroyClient(ctx, s.client) }),
			)
		}
		c.mu.Lock()
		if c.clients[s.connection.ID] == s {
			delete(c.clients, s.connection.ID)
		}
		c.mu.Unlock()
	})
	return s.destroyErr
}

// StopConnection cancels a configured profile's generation, stops advisory sources,
// waits for startup and leases, then runs teardown. The profile remains configured;
// an already-stopped profile succeeds and clears retained startup failure status.
// Unknown IDs are NOT_FOUND. The returned profile is an independent snapshot.
//
// After retirement begins, caller cancellation does not skip draining or cleanup.
// This can block on callbacks that ignore cancellation; never call synchronously
// from work holding this generation's lease or from its lifecycle callbacks.
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
// Configured unstarted profiles are DISCONNECTED, construction is PENDING, and
// failed startup remains FAILED until retry, stop, or meaningful replacement.
// Unknown IDs are NOT_FOUND; returned connection configuration is independently
// owned. This is a local readiness snapshot, not a lease or permission grant.
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

// CheckConnection optionally probes a configured profile under an active lease.
// An unstarted profile returns DISCONNECTED without starting it. A nil lifecycle
// CheckConnection callback reports CONNECTED registration without probing network
// health. A supplied callback must return the selected profile's ID; its returned
// profile is copied before exposure. Unknown IDs are NOT_FOUND.
// Cancellation includes the caller and generation; callbacks must honor it.
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

// Close permanently rejects new work, cancels generations and advisory sources,
// drains leases, and destroys clients. Repeated and concurrent calls return the
// same accumulated teardown error. Close has no timeout: callbacks must observe
// cancellation and cleanup must terminate. Never call it synchronously from a
// leased callback or lifecycle callback whose completion Close must await.
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
