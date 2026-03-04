package networker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/go-hclog"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/utils/timeutil"
	"github.com/omniviewdev/plugin-sdk/settings"
)

const defaultCloseTimeout = 10 * time.Second
const readyTimeout = 30 * time.Second

// Manager manages the lifecycle of networker actions, such as port forwarding sessions.
type Manager struct {
	log              hclog.Logger
	settingsProvider settings.Provider
	portChecker      PortChecker
	clock            timeutil.Clock
	closeTimeout     time.Duration

	resourceForwarders map[string]ResourceForwarder
	staticForwarders   map[string]StaticForwarder


	mu       sync.RWMutex
	sessions map[string]*sessionEntry
	wg       sync.WaitGroup // tracks monitor goroutines
}

var _ Provider = (*Manager)(nil)

// NewManager creates a new Manager with the given config and plugin opts.
func NewManager(cfg ManagerConfig, opts PluginOpts) *Manager {
	logger := cfg.Logger
	if logger == nil {
		logger = hclog.NewNullLogger()
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
	rf := make(map[string]ResourceForwarder, len(opts.ResourceForwarders))
	for k, v := range opts.ResourceForwarders {
		rf[k] = v
	}
	sf := make(map[string]StaticForwarder, len(opts.StaticForwarders))
	for k, v := range opts.StaticForwarders {
		sf[k] = v
	}

	return &Manager{
		log:                logger.Named("NetworkerManager"),
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
	resources := make([]string, 0, len(m.resourceForwarders))
	for rt := range m.resourceForwarders {
		resources = append(resources, rt)
	}
	return resources, nil
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

		if resource, ok := snap.Connection.(PortForwardResourceConnection); ok {
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
	if pluginctx == nil {
		pluginctx = &types.PluginContext{Context: context.Background()}
	}
	if pluginctx.Context == nil {
		pluginctx.Context = context.Background()
	}

	logger := m.log.With("connection_type", opts.ConnectionType)

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

	// Shallow-copy PluginContext
	pctxCopy := *pluginctx
	ctx, cancel := context.WithCancel(pluginctx.Context)
	pctxCopy.Context = ctx
	if m.settingsProvider != nil {
		pctxCopy.SetSettingsProvider(m.settingsProvider)
	}

	sessionID := uuid.NewString()

	var result *ForwarderResult

	switch opts.ConnectionType {
	case PortForwardConnectionTypeResource:
		result, err = m.handleResourceForward(ctx, &pctxCopy, opts)
	case PortForwardConnectionTypeStatic:
		result, err = m.handleStaticForward(ctx, &pctxCopy, opts)
	default:
		cancel()
		return nil, NewInvalidConnectionTypeError(string(opts.ConnectionType))
	}

	if err != nil {
		cancel()
		return nil, NewForwarderFailedError(sessionID, err)
	}

	if result == nil {
		cancel()
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
				cancel()
				if !ok {
					return nil, NewForwarderFailedError(sessionID, fmt.Errorf("forwarder error channel closed before ready"))
				}
				return nil, NewForwarderFailedError(sessionID, err)
			case <-ctx.Done():
				cancel()
				return nil, NewForwarderFailedError(sessionID, fmt.Errorf("context cancelled while waiting for tunnel"))
			case <-m.clock.After(readyTimeout):
				cancel()
				return nil, NewForwarderFailedError(sessionID, fmt.Errorf("timed out waiting for tunnel to be ready"))
			}
		}
	}

	now := m.clock.Now()
	newSession := PortForwardSession{
		CreatedAt:      now,
		UpdatedAt:      now,
		Labels:         opts.Labels,
		Connection:     opts.Connection,
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
		cancel:  cancel,
	}

	m.mu.Lock()
	if _, exists := m.sessions[sessionID]; exists {
		m.mu.Unlock()
		cancel()
		return nil, NewForwarderFailedError(sessionID, fmt.Errorf("duplicate session ID %q", sessionID))
	}
	m.sessions[sessionID] = entry
	m.mu.Unlock()

	logger.Debug("port forward session started", "session_id", sessionID)

	// Monitor for post-start failures.
	if result.ErrCh != nil {
		m.wg.Add(1)
		go m.monitorSession(entry, result.ErrCh)
	}

	snap := entry.snapshot()
	return &snap, nil
}

func (m *Manager) handleResourceForward(
	ctx context.Context,
	pctx *types.PluginContext,
	opts PortForwardSessionOptions,
) (*ForwarderResult, error) {
	resource, ok := opts.Connection.(PortForwardResourceConnection)
	if !ok {
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
	static, ok := opts.Connection.(PortForwardStaticConnection)
	if !ok {
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

	err, ok := <-errCh
	if !ok {
		// Channel closed cleanly — transition to STOPPED.
		if transErr := entry.transition(SessionStateStopped); transErr != nil {
			m.log.Debug("monitor: transition to STOPPED failed", "session_id", entry.session.ID, "error", transErr)
		}
		return
	}

	// Fatal error — transition to FAILED.
	m.log.Error("port forward session failed", "session_id", entry.session.ID, "error", err)
	if transErr := entry.transition(SessionStateFailed); transErr != nil {
		m.log.Debug("monitor: transition to FAILED failed", "session_id", entry.session.ID, "error", transErr)
	}
}

func (m *Manager) ClosePortForwardSession(
	_ *types.PluginContext,
	sessionID string,
) (*PortForwardSession, error) {
	m.mu.Lock()
	entry, ok := m.sessions[sessionID]
	if !ok {
		m.mu.Unlock()
		return nil, NewSessionNotFoundError(sessionID)
	}
	delete(m.sessions, sessionID)
	m.mu.Unlock()

	entry.cancel()
	_ = entry.transition(SessionStateStopped)

	snap := entry.snapshot()
	return &snap, nil
}

// StopAll cancels all active sessions and waits for monitor goroutines
// to complete within the configured timeout.
func (m *Manager) StopAll() {
	m.mu.Lock()
	for id, entry := range m.sessions {
		entry.cancel()
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
		m.log.Warn("StopAll timed out waiting for monitor goroutines")
	}
}
