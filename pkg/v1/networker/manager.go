package networker

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/utils/timeutil"
	"github.com/omniviewdev/plugin-sdk/settings"
)

const defaultCloseTimeout = 10 * time.Second
const readyTimeout = 30 * time.Second

// Sentinel causes for context cancellation — forwarders can inspect these
// via context.Cause(ctx) to distinguish intentional stops from failures.
var (
	ErrSessionClosed  = errors.New("session closed")
	ErrManagerStopped = errors.New("manager stopped")
)

// isIntentionalStop returns true if err is a sentinel indicating a
// deliberate session/manager shutdown rather than an unexpected failure.
func isIntentionalStop(err error) bool {
	return err != nil && (errors.Is(err, ErrSessionClosed) || errors.Is(err, ErrManagerStopped))
}

// Manager manages the lifecycle of networker actions, such as port forwarding sessions.
type Manager struct {
	log              logging.Logger
	settingsProvider settings.Provider
	portChecker      PortChecker
	clock            timeutil.Clock
	closeTimeout     time.Duration

	resourceForwarders map[string]ResourceForwarder
	staticForwarders   map[string]StaticForwarder

	mu       sync.RWMutex
	sessions map[string]*sessionEntry
	wg       sync.WaitGroup // tracks monitor goroutines
	stopped  bool           // set by StopAll to reject new sessions
}

var _ Provider = (*Manager)(nil)

// NewManager creates a new Manager with the given config and plugin opts.
func NewManager(cfg ManagerConfig, opts PluginOpts) *Manager {
	logger := cfg.Logger
	if logger == nil {
		logger = logging.NewNop()
	}

	clk := cfg.Clock
	if clk == nil {
		clk = timeutil.RealClock{}
	}

	pc := cfg.PortChecker
	if pc == nil {
		pc = RealPortChecker{}
	}

	closeTimeout := cfg.CloseTimeout
	if closeTimeout == 0 {
		closeTimeout = defaultCloseTimeout
	}

	// Defensive copy of forwarder maps to prevent external mutation.
	rf := maps.Clone(opts.ResourceForwarders)
	sf := maps.Clone(opts.StaticForwarders)

	return &Manager{
		log:                logger.Named("networker.manager"),
		settingsProvider:   cfg.Settings,
		portChecker:        pc,
		clock:              clk,
		closeTimeout:       closeTimeout,
		resourceForwarders: rf,
		staticForwarders:   sf,
		sessions:           make(map[string]*sessionEntry),
	}
}

// ---------------------------------------------------------------------------
// Provider implementation
// ---------------------------------------------------------------------------

func (m *Manager) GetSupportedPortForwardTargets(_ *types.PluginContext) ([]string, error) {
	return slices.Collect(maps.Keys(m.resourceForwarders)), nil
}

func (m *Manager) GetPortForwardSession(
	_ *types.PluginContext,
	sessionID string,
) (*PortForwardSession, error) {
	m.mu.RLock()
	entry, ok := m.sessions[sessionID]
	m.mu.RUnlock()

	if !ok {
		return nil, NewSessionNotFoundError(sessionID)
	}

	snap := entry.snapshot()
	return &snap, nil
}

func (m *Manager) ListPortForwardSessions(_ *types.PluginContext) ([]*PortForwardSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sessions := make([]*PortForwardSession, 0, len(m.sessions))
	for _, entry := range m.sessions {
		snap := entry.snapshot()
		sessions = append(sessions, &snap)
	}
	return sessions, nil
}

func (m *Manager) FindPortForwardSessions(
	_ *types.PluginContext,
	req FindPortForwardSessionRequest,
) ([]*PortForwardSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sessions := make([]*PortForwardSession, 0)
	for _, entry := range m.sessions {
		snap := entry.snapshot()

		passesResourceCheck := req.ResourceID == ""
		passesConnectionCheck := req.ConnectionID == ""

		var resource PortForwardResourceConnection
		switch c := snap.Connection.(type) {
		case PortForwardResourceConnection:
			resource = c
		case *PortForwardResourceConnection:
			if c != nil {
				resource = *c
			}
		}
		if resource.ResourceKey != "" || resource.ResourceID != "" || resource.ConnectionID != "" {
			if req.ResourceID != "" {
				passesResourceCheck = resource.ResourceID == req.ResourceID
			}
			if req.ConnectionID != "" {
				passesConnectionCheck = resource.ConnectionID == req.ConnectionID
			}
		}

		if passesResourceCheck && passesConnectionCheck {
			sessions = append(sessions, &snap)
		}
	}

	return sessions, nil
}

func (m *Manager) StartPortForwardSession(
	pluginctx *types.PluginContext,
	opts PortForwardSessionOptions,
) (*PortForwardSession, error) {
	// Read from pluginctx without mutating it.
	if pluginctx == nil {
		pluginctx = &types.PluginContext{}
	}
	// Port-forward sessions are long-lived and must outlive the initiating
	// RPC call. Use WithoutCancel to detach from the RPC deadline while
	// preserving any request-scoped values (e.g. middleware metadata).
	baseCtx := context.Background()
	if pluginctx.Context != nil {
		baseCtx = context.WithoutCancel(pluginctx.Context)
	}

	logger := m.log.With(logging.String("connection_type", string(opts.ConnectionType)))

	// Validate protocol early — reject unknown values instead of silently
	// coercing them to TCP in the proto conversion layer.
	if !opts.Protocol.Valid() {
		return nil, fmt.Errorf("invalid port forward protocol: %q", opts.Protocol)
	}

	// Resolve port
	var err error
	if opts.LocalPort == 0 {
		opts.LocalPort, err = m.portChecker.FindFreePort()
		if err != nil {
			return nil, err
		}
	} else if m.portChecker.IsPortUnavailable(opts.LocalPort) {
		return nil, NewPortUnavailableError(opts.LocalPort)
	}

	// Shallow-copy PluginContext — never mutate the caller's instance.
	pctxCopy := *pluginctx
	ctx, cancel := context.WithCancelCause(baseCtx)
	pctxCopy.Context = ctx
	if m.settingsProvider != nil {
		pctxCopy.SetSettingsProvider(m.settingsProvider)
	}

	sessionID := uuid.NewString()
	pctxCopy.Logger = m.log.With(
		logging.String("session", sessionID),
	)

	// Reject new sessions if the manager is shutting down, before starting
	// any external forwarder work that may have side effects.
	m.mu.RLock()
	shuttingDown := m.stopped
	m.mu.RUnlock()
	if shuttingDown {
		cancel(nil)
		return nil, NewManagerShuttingDownError(sessionID)
	}

	var result *ForwarderResult

	switch opts.ConnectionType {
	case PortForwardConnectionTypeResource:
		result, err = m.handleResourceForward(ctx, &pctxCopy, opts)
	case PortForwardConnectionTypeStatic:
		result, err = m.handleStaticForward(ctx, &pctxCopy, opts)
	default:
		cancel(nil)
		return nil, NewInvalidConnectionTypeError(string(opts.ConnectionType))
	}

	if err != nil {
		cancel(nil)
		// Preserve already-typed NetworkerErrors; wrap others.
		var nerr *NetworkerError
		if errors.As(err, &nerr) {
			return nil, err
		}
		return nil, NewForwarderFailedError(sessionID, err)
	}

	if result == nil {
		cancel(nil)
		return nil, NewForwarderFailedError(sessionID, fmt.Errorf("forwarder returned nil result"))
	}

	// Use session ID from the forwarder if it provided one, otherwise use ours.
	if result.SessionID != "" {
		sessionID = result.SessionID
	}

	// Wait for the forwarder to report ready (or fail).
	// Prefer ready over errCh with a non-blocking check first, since both
	// may be signalled simultaneously (e.g. forwarder closes ready then errCh).
	if result.Ready != nil {
		select {
		case <-result.Ready:
			// tunnel established
		default:
			// Not ready yet — wait on all channels.
			select {
			case <-result.Ready:
				// tunnel established
			case err, ok := <-result.ErrCh:
				cancel(nil)
				if !ok {
					return nil, NewForwarderFailedError(sessionID, fmt.Errorf("forwarder error channel closed before ready"))
				}
				if err == nil {
					return nil, NewForwarderFailedError(sessionID, fmt.Errorf("forwarder error channel returned nil error"))
				}
				return nil, NewForwarderFailedError(sessionID, err)
			case <-ctx.Done():
				cancel(nil)
				return nil, NewForwarderFailedError(sessionID, ctx.Err())
			case <-m.clock.After(readyTimeout):
				cancel(nil)
				return nil, NewForwarderFailedError(sessionID, fmt.Errorf("timed out waiting for tunnel to be ready"))
			}
		}
	}

	// Defensive copy of labels to prevent external mutation.
	labels := maps.Clone(opts.Labels)

	now := m.clock.Now()
	newSession := PortForwardSession{
		CreatedAt:      now,
		UpdatedAt:      now,
		Labels:         labels,
		Connection:     cloneConnection(opts.Connection),
		ID:             sessionID,
		Protocol:       opts.Protocol,
		State:          SessionStateActive,
		ConnectionType: opts.ConnectionType,
		Encryption:     opts.Encryption,
		LocalPort:      opts.LocalPort,
		RemotePort:     opts.RemotePort,
	}

	entry := &sessionEntry{
		session: newSession,
		ctx:     ctx,
		cancel:  cancel,
	}

	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		cancel(nil)
		return nil, NewManagerShuttingDownError(sessionID)
	}
	if _, exists := m.sessions[sessionID]; exists {
		m.mu.Unlock()
		cancel(nil)
		return nil, NewForwarderFailedError(sessionID, fmt.Errorf("duplicate session ID %q", sessionID))
	}
	m.sessions[sessionID] = entry
	// Start monitor inside the lock to prevent races with StopAll's wg.Wait().
	if result.ErrCh != nil {
		m.wg.Add(1)
		go m.monitorSession(entry, result.ErrCh)
	}
	m.mu.Unlock()

	logger.Debugw(ctx, "port forward session started", "session_id", sessionID)

	snap := entry.snapshot()
	return &snap, nil
}

func (m *Manager) handleResourceForward(
	ctx context.Context,
	pctx *types.PluginContext,
	opts PortForwardSessionOptions,
) (*ForwarderResult, error) {
	var resource PortForwardResourceConnection
	switch c := opts.Connection.(type) {
	case PortForwardResourceConnection:
		resource = c
	case *PortForwardResourceConnection:
		if c == nil {
			return nil, NewInvalidConnectionTypeError("resource connection is nil")
		}
		resource = *c
	default:
		return nil, NewInvalidConnectionTypeError("connection is not a resource")
	}

	forwarder, ok := m.resourceForwarders[resource.ResourceKey]
	if !ok {
		return nil, NewNoHandlerFoundError(resource.ResourceKey)
	}

	handlerOpts := ResourcePortForwardHandlerOpts{
		Options:  opts,
		Resource: resource,
	}

	return forwarder.ForwardResource(ctx, pctx, handlerOpts)
}

func (m *Manager) handleStaticForward(
	ctx context.Context,
	pctx *types.PluginContext,
	opts PortForwardSessionOptions,
) (*ForwarderResult, error) {
	var static PortForwardStaticConnection
	switch c := opts.Connection.(type) {
	case PortForwardStaticConnection:
		static = c
	case *PortForwardStaticConnection:
		if c == nil {
			return nil, NewInvalidConnectionTypeError("static connection is nil")
		}
		static = *c
	default:
		return nil, NewInvalidConnectionTypeError("connection is not static")
	}

	forwarder, ok := m.staticForwarders[static.Address]
	if !ok {
		return nil, NewNoHandlerFoundError(static.Address)
	}

	handlerOpts := StaticPortForwardHandlerOpts{
		Options: opts,
		Static:  static,
	}

	return forwarder.ForwardStatic(ctx, pctx, handlerOpts)
}

// monitorSession watches for post-start failures from a forwarder's ErrCh.
func (m *Manager) monitorSession(entry *sessionEntry, errCh <-chan error) {
	defer m.wg.Done()

	select {
	case <-entry.ctx.Done():
		// Session context cancelled (e.g. via ClosePortForwardSession or StopAll).
		if transErr := entry.transition(SessionStateStopped); transErr != nil {
			m.log.Debugw(entry.ctx, "monitor: transition to STOPPED failed", "session_id", entry.session.ID, "error", transErr)
		}
		return
	case err, ok := <-errCh:
		if !ok || err == nil {
			// Channel closed or nil error — clean stop.
			if transErr := entry.transition(SessionStateStopped); transErr != nil {
				m.log.Debugw(entry.ctx, "monitor: transition to STOPPED failed", "session_id", entry.session.ID, "error", transErr)
			}
			return
		}

		// Intentional shutdown sentinels — treat as clean stop, not failure.
		if isIntentionalStop(err) || isIntentionalStop(context.Cause(entry.ctx)) {
			m.log.Debugw(entry.ctx, "monitor: intentional stop", "session_id", entry.session.ID, "cause", err)
			if transErr := entry.transition(SessionStateStopped); transErr != nil {
				m.log.Debugw(entry.ctx, "monitor: transition to STOPPED failed", "session_id", entry.session.ID, "error", transErr)
			}
			return
		}

		// Fatal error — transition to FAILED.
		m.log.Errorw(entry.ctx, "port forward session failed", "session_id", entry.session.ID, "error", err)
		if transErr := entry.transition(SessionStateFailed); transErr != nil {
			m.log.Debugw(entry.ctx, "monitor: transition to FAILED failed", "session_id", entry.session.ID, "error", transErr)
		}
	}
}

func (m *Manager) ClosePortForwardSession(
	_ *types.PluginContext,
	sessionID string,
) (*PortForwardSession, error) {
	m.log.Debugw(context.Background(), "closing port forward session", "session_id", sessionID)

	m.mu.Lock()
	entry, ok := m.sessions[sessionID]
	if !ok {
		m.mu.Unlock()
		return nil, NewSessionNotFoundError(sessionID)
	}
	delete(m.sessions, sessionID)
	m.mu.Unlock()

	entry.cancel(ErrSessionClosed)
	_ = entry.transition(SessionStateStopped)

	snap := entry.snapshot()
	return &snap, nil
}

// StopAll cancels all active sessions and waits for monitor goroutines
// to complete within the configured timeout.
func (m *Manager) StopAll() {
	m.mu.Lock()
	m.log.Debugw(context.Background(), "stopping all port forward sessions", "session_count", len(m.sessions))
	m.stopped = true
	for id, entry := range m.sessions {
		entry.cancel(ErrManagerStopped)
		_ = entry.transition(SessionStateStopped)
		delete(m.sessions, id)
	}
	m.mu.Unlock()

	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-m.clock.After(m.closeTimeout):
		m.log.Warn(context.Background(), "StopAll timed out waiting for monitor goroutines")
	}
}
