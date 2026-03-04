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
	return true
}

// connectionState holds per-connection runtime state.
type connectionState[ClientT any] struct {
	conn   types.Connection
	client *ClientT
	ctx    context.Context
	cancel context.CancelFunc
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
		fresh[c.ID] = c
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

// StartConnection creates a client for the connection and stores it.
// Returns Connected status. No-op if already started.
func (m *connectionManager[ClientT]) StartConnection(ctx context.Context, connectionID string) (types.ConnectionStatus, error) {
	m.mu.Lock()
	// Already started?
	if state, ok := m.conns[connectionID]; ok {
		connCopy := state.conn
		m.mu.Unlock()
		return types.ConnectionStatus{
			Connection: &connCopy,
			Status:     types.ConnectionStatusConnected,
		}, nil
	}

	// Must be a known connection.
	conn, ok := m.loaded[connectionID]
	if !ok {
		m.mu.Unlock()
		return types.ConnectionStatus{}, fmt.Errorf("connection %q not found", connectionID)
	}
	m.mu.Unlock()

	// Attach session to ctx for CreateClient.
	clientCtx := WithSession(ctx, &Session{Connection: &conn})
	client, err := m.provider.CreateClient(clientCtx)
	if err != nil {
		return types.ConnectionStatus{}, fmt.Errorf("create client for %q: %w", connectionID, err)
	}

	connCtx, cancel := context.WithCancel(m.rootCtx)

	m.mu.Lock()
	// Double-check after lock reacquisition (concurrent start).
	if state, ok := m.conns[connectionID]; ok {
		cancel()
		_ = m.provider.DestroyClient(ctx, client)
		connCopy := state.conn
		m.mu.Unlock()
		return types.ConnectionStatus{
			Connection: &connCopy,
			Status:     types.ConnectionStatusConnected,
		}, nil
	}
	// Guard against deletion during CreateClient: if the connection was
	// removed from m.loaded while we were unlocked, don't resurrect it.
	currentConn, stillLoaded := m.loaded[connectionID]
	if !stillLoaded {
		cancel()
		_ = m.provider.DestroyClient(ctx, client)
		m.mu.Unlock()
		return types.ConnectionStatus{}, fmt.Errorf("connection %q was deleted during start", connectionID)
	}
	// Use the latest loaded config (may have been updated while unlocked).
	conn = currentConn
	m.conns[connectionID] = &connectionState[ClientT]{
		conn:   conn,
		client: client,
		ctx:    connCtx,
		cancel: cancel,
	}
	m.mu.Unlock()

	return types.ConnectionStatus{
		Connection: &conn,
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

	// Best-effort client cleanup.
	destroyCtx := WithSession(ctx, &Session{Connection: &state.conn})
	err := m.provider.DestroyClient(destroyCtx, state.client)
	return state.conn, err
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
		return state.conn, nil
	}
	// Check loaded connections.
	if conn, ok := m.loaded[connectionID]; ok {
		return conn, nil
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
		conns = append(conns, conn)
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
	conn := state.conn
	m.mu.RUnlock()
	return m.provider.CheckConnection(ctx, &conn, client)
}

// UpdateConnection updates the stored connection data.
// If the connection is active, restarts the client with the new connection data.
// Returns ErrConnectionUnchanged if the incoming data matches the stored connection.
func (m *connectionManager[ClientT]) UpdateConnection(ctx context.Context, conn types.Connection) (types.Connection, error) {
	m.mu.Lock()

	// Check whether the connection data actually changed.
	if old, ok := m.loaded[conn.ID]; ok && connectionEqual(old, conn) {
		m.mu.Unlock()
		return conn, ErrConnectionUnchanged
	}

	m.loaded[conn.ID] = conn

	state, isActive := m.conns[conn.ID]
	if !isActive {
		m.mu.Unlock()
		return conn, nil
	}

	oldClient := state.client
	oldCancel := state.cancel
	m.mu.Unlock()

	// Create a new client with the updated connection data.
	clientCtx := WithSession(ctx, &Session{Connection: &conn})
	newClient, err := m.provider.CreateClient(clientCtx)
	if err != nil {
		return conn, fmt.Errorf("recreate client for %q: %w", conn.ID, err)
	}

	newCtx, newCancel := context.WithCancel(m.rootCtx)

	m.mu.Lock()
	// Re-verify the connection wasn't stopped/deleted while we were unlocked.
	currentState, stillActive := m.conns[conn.ID]
	if !stillActive || currentState != state {
		// Connection was removed or replaced — discard the new client.
		newCancel()
		_ = m.provider.DestroyClient(ctx, newClient)
		m.mu.Unlock()
		return conn, fmt.Errorf("connection %q was modified during update", conn.ID)
	}
	state.conn = conn
	state.client = newClient
	state.ctx = newCtx
	state.cancel = newCancel
	m.mu.Unlock()

	// Clean up old resources after successful swap.
	oldCancel()
	_ = m.provider.DestroyClient(ctx, oldClient)

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
		_ = m.provider.DestroyClient(ctx, state.client)
	}
	return nil
}

// ActiveConnectionIDs returns IDs of all connections with active clients.
func (m *connectionManager[ClientT]) ActiveConnectionIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return slices.Collect(maps.Keys(m.conns))
}
