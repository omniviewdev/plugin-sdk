package resource

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sync"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// ErrConnectionUnchanged is returned by UpdateConnection when the incoming
// connection data is identical to the stored connection. Callers can use
// errors.Is(err, ErrConnectionUnchanged) to detect the no-op case.
var ErrConnectionUnchanged = errors.New("connection unchanged")

type deepCloneVisitKey struct {
	typ reflect.Type
	ptr uintptr
}

func deepCloneMap(in map[string]any) map[string]any {
	return deepCloneMapWithVisited(in, make(map[deepCloneVisitKey]reflect.Value))
}

func deepCloneMapWithVisited(in map[string]any, visited map[deepCloneVisitKey]reflect.Value) map[string]any {
	if in == nil {
		return nil
	}
	rv := reflect.ValueOf(in)
	key := deepCloneVisitKey{typ: rv.Type(), ptr: rv.Pointer()}
	if existing, ok := visited[key]; ok {
		if out, ok := existing.Interface().(map[string]any); ok {
			return out
		}
	}

	out := make(map[string]any, len(in))
	visited[key] = reflect.ValueOf(out)
	for k, v := range in {
		out[k] = deepCloneValueWithVisited(v, visited)
	}
	return out
}

func assignClonedValue(dst reflect.Value, src reflect.Value, visited map[deepCloneVisitKey]reflect.Value) {
	cloned := deepCloneValueWithVisited(src.Interface(), visited)
	cv := reflect.ValueOf(cloned)
	if !cv.IsValid() {
		dst.Set(reflect.Zero(dst.Type()))
		return
	}
	if cv.Type().AssignableTo(dst.Type()) {
		dst.Set(cv)
		return
	}
	if cv.Type().ConvertibleTo(dst.Type()) {
		dst.Set(cv.Convert(dst.Type()))
		return
	}
	dst.Set(src)
}

func deepCloneValue(v any) any {
	return deepCloneValueWithVisited(v, make(map[deepCloneVisitKey]reflect.Value))
}

func deepCloneValueWithVisited(v any, visited map[deepCloneVisitKey]reflect.Value) any {
	if v == nil {
		return nil
	}

	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Map:
		if rv.IsNil() {
			return v
		}
		key := deepCloneVisitKey{typ: rv.Type(), ptr: rv.Pointer()}
		if existing, ok := visited[key]; ok {
			return existing.Interface()
		}
		out := reflect.MakeMapWithSize(rv.Type(), rv.Len())
		visited[key] = out
		iter := rv.MapRange()
		for iter.Next() {
			key := iter.Key()
			srcVal := iter.Value()
			dstVal := reflect.New(rv.Type().Elem()).Elem()
			assignClonedValue(dstVal, srcVal, visited)
			out.SetMapIndex(key, dstVal)
		}
		return out.Interface()
	case reflect.Slice:
		if rv.IsNil() {
			return v
		}
		key := deepCloneVisitKey{typ: rv.Type(), ptr: rv.Pointer()}
		if existing, ok := visited[key]; ok {
			return existing.Interface()
		}
		out := reflect.MakeSlice(rv.Type(), rv.Len(), rv.Len())
		visited[key] = out
		for i := 0; i < rv.Len(); i++ {
			srcVal := rv.Index(i)
			assignClonedValue(out.Index(i), srcVal, visited)
		}
		return out.Interface()
	case reflect.Pointer:
		if rv.IsNil() {
			return v
		}
		key := deepCloneVisitKey{typ: rv.Type(), ptr: rv.Pointer()}
		if existing, ok := visited[key]; ok {
			return existing.Interface()
		}
		dstPtr := reflect.New(rv.Elem().Type())
		visited[key] = dstPtr
		assignClonedValue(dstPtr.Elem(), rv.Elem(), visited)
		return dstPtr.Interface()
	default:
		return v
	}
}

// cloneConnection returns a copy of c with deep-cloned map/slice data so the
// caller and manager cannot share mutable nested state.
func cloneConnection(c types.Connection) types.Connection {
	c.Data = deepCloneMap(c.Data)
	c.Labels = deepCloneMap(c.Labels)
	c.SetSensitiveData(deepCloneMap(c.GetSensitiveData()))
	if c.Lifecycle.AutoConnect != nil {
		auto := *c.Lifecycle.AutoConnect
		auto.Triggers = slices.Clone(auto.Triggers)
		if auto.Retry == "" {
			auto.Retry = types.ConnectionAutoConnectRetryNone
		}
		c.Lifecycle.AutoConnect = &auto
	}
	return c
}

// connectionEqual reports whether two connections are semantically equal
// for client-affecting fields. It skips LastRefresh, Client, and UID.
func connectionEqual(a, b types.Connection) bool {
	if a.Name != b.Name || a.Description != b.Description || a.Avatar != b.Avatar || a.ExpiryTime != b.ExpiryTime {
		return false
	}
	if !reflect.DeepEqual(a.Data, b.Data) {
		return false
	}
	if !reflect.DeepEqual(a.Labels, b.Labels) {
		return false
	}
	if !reflect.DeepEqual(a.GetSensitiveData(), b.GetSensitiveData()) {
		return false
	}
	if !reflect.DeepEqual(a.Lifecycle, b.Lifecycle) {
		return false
	}
	return true
}

// connectionState holds per-connection runtime state.
type connectionState[ClientT any] struct {
	conn   types.Connection
	client *ClientT
	ctx    context.Context
	cancel context.CancelFunc
	gen    int64 // incremented on each UpdateConnection swap
}

// connectionManager manages connections and their typed clients.
// Thread-safe for concurrent access.
type connectionManager[ClientT any] struct {
	mu       sync.RWMutex
	provider ConnectionProvider[ClientT]
	conns    map[string]*connectionState[ClientT] // by connection ID
	loaded   map[string]types.Connection          // all known connections (from LoadConnections)
	rootCtx  context.Context
}

func newConnectionManager[ClientT any](rootCtx context.Context, provider ConnectionProvider[ClientT]) *connectionManager[ClientT] {
	if rootCtx == nil {
		rootCtx = context.Background()
	}
	return &connectionManager[ClientT]{
		provider: provider,
		conns:    make(map[string]*connectionState[ClientT]),
		loaded:   make(map[string]types.Connection),
		rootCtx:  rootCtx,
	}
}

// LoadConnections delegates to the ConnectionProvider and reconciles the cache,
// removing stale entries that are no longer returned by the provider.
func (m *connectionManager[ClientT]) LoadConnections(ctx context.Context) ([]types.Connection, error) {
	conns, err := m.provider.LoadConnections(ctx)
	if err != nil {
		return nil, err
	}
	fresh := make(map[string]types.Connection, len(conns))
	for _, c := range conns {
		fresh[c.ID] = cloneConnection(c)
	}
	m.mu.Lock()
	// Remove stale entries not present in the fresh set.
	for id := range m.loaded {
		if _, ok := fresh[id]; !ok {
			delete(m.loaded, id)
		}
	}
	// Upsert fresh entries.
	for id, c := range fresh {
		m.loaded[id] = c
	}
	m.mu.Unlock()
	return conns, nil
}

// reconcileLoaded merges discovered connections into the loaded map.
// New connections are added, existing ones are updated, and connections
// absent from the fresh set are removed (matching LoadConnections semantics).
// Returns the IDs of connections that were removed or whose spec changed,
// so the caller can run cleanup (stop watches, evict caches, tear down
// live runtimes) via the normal code paths.
func (m *connectionManager[ClientT]) reconcileLoaded(conns []types.Connection) []string {
	fresh := make(map[string]types.Connection, len(conns))
	for _, c := range conns {
		fresh[c.ID] = cloneConnection(c)
	}

	var removed []string

	m.mu.Lock()
	// Detect removed or changed connections by comparing fresh against
	// both m.loaded and m.conns.
	for id := range m.loaded {
		if _, ok := fresh[id]; !ok {
			removed = append(removed, id)
			delete(m.loaded, id)
		}
	}
	for id := range m.conns {
		if _, ok := fresh[id]; !ok {
			// Connection has a live runtime but is no longer in the snapshot.
			if !slices.Contains(removed, id) {
				removed = append(removed, id)
			}
		}
	}
	for id, c := range fresh {
		m.loaded[id] = c
	}
	m.mu.Unlock()

	return removed
}

// StartConnection creates a client for the connection and stores it.
// Returns Connected status. No-op if already started.
func (m *connectionManager[ClientT]) StartConnection(ctx context.Context, connectionID string) (types.ConnectionStatus, error) {
	m.mu.Lock()
	// Already started? Validate health before returning CONNECTED.
	if state, ok := m.conns[connectionID]; ok {
		client := state.client
		conn := cloneConnection(state.conn)
		m.mu.Unlock()

		// Verify the existing connection is still healthy.
		checkStatus, checkErr := m.provider.CheckConnection(ctx, &conn, client)
		if checkErr == nil && checkStatus.Status == types.ConnectionStatusConnected {
			return types.ConnectionStatus{
				Connection: &conn,
				Status:     types.ConnectionStatusConnected,
			}, nil
		}

		// Stale connection — tear down and attempt reconnect.
		_, _ = m.StopConnection(ctx, connectionID)

		// If the connection was removed from loaded while we tore down,
		// return the failed status instead of trying to reconnect.
		m.mu.Lock()
		if _, stillLoaded := m.loaded[connectionID]; !stillLoaded {
			m.mu.Unlock()
			if checkErr != nil {
				return types.ConnectionStatus{}, checkErr
			}
			return checkStatus, nil
		}
		// Fall through to fresh connection below (lock held).
	}

	// Must be a known connection.
	connSnapshot, ok := m.loaded[connectionID]
	if !ok {
		m.mu.Unlock()
		return types.ConnectionStatus{}, fmt.Errorf("connection %q not found", connectionID)
	}
	m.mu.Unlock()

	// Clone before passing to provider — prevents mutation of manager-owned maps.
	connForProvider := cloneConnection(connSnapshot)
	session := &Session{Connection: &connForProvider}
	clientCtx := WithSession(ctx, session)
	client, err := m.provider.CreateClient(clientCtx)
	if err != nil {
		return types.ConnectionStatus{}, fmt.Errorf("create client for %q: %w", connectionID, err)
	}

	connCtx, cancel := context.WithCancel(m.rootCtx)

	m.mu.Lock()
	// Double-check after lock reacquisition (concurrent start).
	if state, ok := m.conns[connectionID]; ok {
		connCopy := cloneConnection(state.conn)
		m.mu.Unlock()
		// Release lock before calling external provider methods.
		cancel()
		if err := m.provider.DestroyClient(clientCtx, client); err != nil {
			return types.ConnectionStatus{}, fmt.Errorf("cleanup error after duplicate start for %q: %w", connectionID, err)
		}
		return types.ConnectionStatus{
			Connection: &connCopy,
			Status:     types.ConnectionStatusConnected,
		}, nil
	}
	// Guard against deletion during CreateClient: if the connection was
	// removed from m.loaded while we were unlocked, don't resurrect it.
	currentConn, stillLoaded := m.loaded[connectionID]
	if !stillLoaded {
		m.mu.Unlock()
		cancel()
		originalErr := fmt.Errorf("connection %q was deleted during start", connectionID)
		if cleanupErr := m.provider.DestroyClient(clientCtx, client); cleanupErr != nil {
			return types.ConnectionStatus{}, fmt.Errorf("original error: %w; cleanup error: %w", originalErr, cleanupErr)
		}
		return types.ConnectionStatus{}, originalErr
	}
	// If the loaded config changed while we were unlocked, the client was
	// built with stale data. Discard the client and let the caller retry.
	if !connectionEqual(connSnapshot, currentConn) {
		m.mu.Unlock()
		cancel()
		originalErr := fmt.Errorf("connection %q config changed during start; retry", connectionID)
		if cleanupErr := m.provider.DestroyClient(clientCtx, client); cleanupErr != nil {
			return types.ConnectionStatus{}, fmt.Errorf("original error: %w; cleanup error: %w", originalErr, cleanupErr)
		}
		return types.ConnectionStatus{}, originalErr
	}
	connSnapshot = currentConn
	m.conns[connectionID] = &connectionState[ClientT]{
		conn:   connSnapshot,
		client: client,
		ctx:    connCtx,
		cancel: cancel,
	}
	m.mu.Unlock()

	connOut := cloneConnection(connSnapshot)
	return types.ConnectionStatus{
		Connection: &connOut,
		Status:     types.ConnectionStatusConnected,
	}, nil
}

// StopConnection destroys the client and cancels the connection context.
func (m *connectionManager[ClientT]) StopConnection(ctx context.Context, connectionID string) (types.Connection, error) {
	m.mu.Lock()
	state, ok := m.conns[connectionID]
	if !ok {
		m.mu.Unlock()
		return types.Connection{}, fmt.Errorf("connection %q not started", connectionID)
	}
	delete(m.conns, connectionID)
	m.mu.Unlock()

	// Cancel context first (stops all watches).
	state.cancel()

	// Best-effort client cleanup. Clone to prevent provider from mutating manager state.
	connForDestroy := cloneConnection(state.conn)
	destroyCtx := WithSession(ctx, &Session{Connection: &connForDestroy})
	err := m.provider.DestroyClient(destroyCtx, state.client)
	return cloneConnection(state.conn), err
}

// GetClient returns the active client for a connection.
func (m *connectionManager[ClientT]) GetClient(connectionID string) (*ClientT, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	state, ok := m.conns[connectionID]
	if !ok {
		return nil, fmt.Errorf("connection %q not started", connectionID)
	}
	return state.client, nil
}

// GetConnectionCtx returns the context for a connection.
func (m *connectionManager[ClientT]) GetConnectionCtx(connectionID string) (context.Context, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	state, ok := m.conns[connectionID]
	if !ok {
		return nil, fmt.Errorf("connection %q not started", connectionID)
	}
	return state.ctx, nil
}

// GetConnection returns the connection metadata.
func (m *connectionManager[ClientT]) GetConnection(connectionID string) (types.Connection, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Check active connections first.
	if state, ok := m.conns[connectionID]; ok {
		return cloneConnection(state.conn), nil
	}
	// Check loaded connections.
	if conn, ok := m.loaded[connectionID]; ok {
		return cloneConnection(conn), nil
	}
	return types.Connection{}, fmt.Errorf("connection %q not found", connectionID)
}

// IsStarted returns whether a connection has an active client.
func (m *connectionManager[ClientT]) IsStarted(connectionID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.conns[connectionID]
	return ok
}

// ListConnections returns all known connections with their runtime state.
func (m *connectionManager[ClientT]) ListConnections(ctx context.Context) ([]types.Connection, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	conns := make([]types.Connection, 0, len(m.loaded))
	for _, conn := range m.loaded {
		conns = append(conns, cloneConnection(conn))
	}
	return conns, nil
}

// GetNamespaces delegates to the ConnectionProvider for the given connection's client.
func (m *connectionManager[ClientT]) GetNamespaces(ctx context.Context, connectionID string) ([]string, error) {
	m.mu.RLock()
	state, ok := m.conns[connectionID]
	if !ok {
		m.mu.RUnlock()
		return nil, fmt.Errorf("connection %q not started", connectionID)
	}
	client := state.client
	m.mu.RUnlock()
	return m.provider.GetNamespaces(ctx, client)
}

// CheckConnection delegates to the ConnectionProvider.
func (m *connectionManager[ClientT]) CheckConnection(ctx context.Context, connectionID string) (types.ConnectionStatus, error) {
	m.mu.RLock()
	state, ok := m.conns[connectionID]
	if !ok {
		m.mu.RUnlock()
		return types.ConnectionStatus{}, fmt.Errorf("connection %q not started", connectionID)
	}
	client := state.client
	conn := cloneConnection(state.conn)
	m.mu.RUnlock()
	return m.provider.CheckConnection(ctx, &conn, client)
}

// UpdateConnection updates the stored connection data.
// If the connection is active, restarts the client with the new connection data.
// Returns ErrConnectionUnchanged if the incoming data matches the stored connection.
func (m *connectionManager[ClientT]) UpdateConnection(ctx context.Context, conn types.Connection) (types.Connection, error) {
	normalizedConn := cloneConnection(conn)

	m.mu.Lock()
	state, isActive := m.conns[conn.ID]

	// Check whether the connection data actually changed.
	if old, ok := m.loaded[conn.ID]; ok && connectionEqual(old, normalizedConn) {
		// If there is no active runtime state, or the active runtime state already
		// matches the normalized desired state, this update is a no-op.
		if !isActive || connectionEqual(state.conn, normalizedConn) {
			m.mu.Unlock()
			return conn, ErrConnectionUnchanged
		}
	}

	stored := normalizedConn

	if !isActive {
		// Not active — update loaded config immediately.
		m.loaded[conn.ID] = stored
		m.mu.Unlock()
		return conn, nil
	}

	origState := state
	oldConn := cloneConnection(state.conn)
	oldClient := state.client
	oldCancel := state.cancel
	capturedGen := state.gen
	m.mu.Unlock()

	// Create a new client with the updated connection data.
	// Clone to prevent provider from mutating manager-owned maps.
	connForProvider := cloneConnection(stored)
	clientCtx := WithSession(ctx, &Session{Connection: &connForProvider})
	newClient, err := m.provider.CreateClient(clientCtx)
	if err != nil {
		// Don't update m.loaded so a retry with the same data won't be
		// short-circuited by ErrConnectionUnchanged.
		return conn, fmt.Errorf("recreate client for %q: %w", conn.ID, err)
	}

	newCtx, newCancel := context.WithCancel(m.rootCtx)

	m.mu.Lock()
	// Re-verify the connection wasn't stopped/deleted or concurrently updated
	// while we were unlocked. Pointer equality catches delete+recreate with
	// the same gen; generation comparison catches in-place mutations.
	currentState, stillActive := m.conns[conn.ID]
	if !stillActive || currentState != origState || currentState.gen != capturedGen {
		// Connection was removed, replaced, or updated by another goroutine.
		// Release the lock before calling external provider methods to avoid
		// blocking other operations or risking deadlocks.
		m.mu.Unlock()
		newCancel()
		originalErr := fmt.Errorf("connection %q was modified during update", conn.ID)
		if cleanupErr := m.provider.DestroyClient(clientCtx, newClient); cleanupErr != nil {
			return conn, fmt.Errorf("original error: %w; cleanup error: %w", originalErr, cleanupErr)
		}
		return conn, originalErr
	}
	// Commit the loaded config only after successful client recreation.
	m.loaded[conn.ID] = stored
	state.conn = stored
	state.client = newClient
	state.ctx = newCtx
	state.cancel = newCancel
	state.gen++
	m.mu.Unlock()

	// Clean up old resources after successful swap.
	oldCancel()
	oldDestroyCtx := WithSession(ctx, &Session{Connection: &oldConn})
	if err := m.provider.DestroyClient(oldDestroyCtx, oldClient); err != nil {
		return conn, err
	}

	return conn, nil
}

// RefreshClient refreshes credentials for an active connection without
// recreating the client. Requires the ConnectionProvider to implement
// ClientRefresher[ClientT].
func (m *connectionManager[ClientT]) RefreshClient(ctx context.Context, connectionID string) error {
	refresher, ok := m.provider.(ClientRefresher[ClientT])
	if !ok {
		return fmt.Errorf("connection provider does not support client refresh")
	}

	m.mu.RLock()
	state, started := m.conns[connectionID]
	if !started {
		m.mu.RUnlock()
		return fmt.Errorf("connection %q not started", connectionID)
	}
	client := state.client
	m.mu.RUnlock()

	return refresher.RefreshClient(ctx, client)
}

// DeleteConnection stops the connection if running and removes it.
func (m *connectionManager[ClientT]) DeleteConnection(ctx context.Context, connectionID string) error {
	m.mu.Lock()
	state, isActive := m.conns[connectionID]
	delete(m.conns, connectionID)
	delete(m.loaded, connectionID)
	m.mu.Unlock()

	if isActive {
		state.cancel()
		connForDestroy := cloneConnection(state.conn)
		destroyCtx := WithSession(ctx, &Session{Connection: &connForDestroy})
		if err := m.provider.DestroyClient(destroyCtx, state.client); err != nil {
			return fmt.Errorf("destroy client for %q: %w", connectionID, err)
		}
	}
	return nil
}

// ActiveConnectionIDs returns IDs of all connections with active clients.
func (m *connectionManager[ClientT]) ActiveConnectionIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return slices.Collect(maps.Keys(m.conns))
}
