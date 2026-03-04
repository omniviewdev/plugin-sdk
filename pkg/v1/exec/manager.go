package exec

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/utils/timeutil"
	"github.com/omniviewdev/plugin-sdk/settings"
)

const (
	DefaultOutputBufferSize = 1000000
	DefaultStreamBufferSize = 4096
	InitialRows             = 27
	InitialCols             = 72
	ResizeTimeout           = 500 * time.Millisecond
)

// ---------------------------------------------------------------------------
// ManagerConfig
// ---------------------------------------------------------------------------

// ManagerConfig configures the Manager.
type ManagerConfig struct {
	Logger          hclog.Logger
	Settings        settings.Provider
	Handlers        map[string]Handler
	Sink            OutputSink      // nil → ChannelSink created by Stream()
	TerminalFactory TerminalFactory // nil → NewRealTerminalFactory()
	Clock           timeutil.Clock  // nil → timeutil.RealClock
}

// ---------------------------------------------------------------------------
// Manager
// ---------------------------------------------------------------------------

// Manager manages the lifecycle of terminal sessions within a plugin process.
type Manager struct {
	log              hclog.Logger
	settingsProvider settings.Provider
	handlers         map[string]Handler
	sink             OutputSink
	terminalFactory  TerminalFactory
	clock            timeutil.Clock

	mu       sync.RWMutex
	sessions map[string]*sessionState
	out      chan StreamOutput // nil when Sink provided via config
	resize   chan StreamResize
	wg       sync.WaitGroup // tracks per-session closer goroutines
}

var _ Provider = (*Manager)(nil)

// NewManager creates a new Manager with the given config.
func NewManager(cfg ManagerConfig) *Manager {
	logger := cfg.Logger
	if logger == nil {
		logger = hclog.NewNullLogger()
	}

	clk := cfg.Clock
	if clk == nil {
		clk = timeutil.RealClock{}
	}

	tf := cfg.TerminalFactory
	if tf == nil {
		tf = NewRealTerminalFactory()
	}

	handlers := cfg.Handlers
	if handlers == nil {
		handlers = make(map[string]Handler)
	}

	return &Manager{
		log:              logger.Named("ExecManager"),
		settingsProvider: cfg.Settings,
		handlers:         handlers,
		sink:             cfg.Sink,
		terminalFactory:  tf,
		clock:            clk,
		sessions:         make(map[string]*sessionState),
		resize:           make(chan StreamResize),
	}
}

// Close cancels all active sessions and waits for their goroutines to finish.
func (m *Manager) Close() {
	m.mu.Lock()
	for _, ss := range m.sessions {
		ss.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

func (m *Manager) GetSupportedResources(_ *types.PluginContext) []Handler {
	resources := make([]Handler, 0, len(m.handlers))
	for _, handler := range m.handlers {
		resources = append(resources, handler)
	}
	return resources
}

// Stream creates a new stream to multiplex sessions.
func (m *Manager) Stream(ctx context.Context, in chan StreamInput) (chan StreamOutput, error) {
	if m.sink == nil {
		m.out = make(chan StreamOutput, DefaultStreamBufferSize)
		m.sink = NewChannelSink(ctx, m.out)
	}

	go m.handleStreamInput(ctx, in)

	return m.out, nil
}

func (m *Manager) handleStreamInput(ctx context.Context, in chan StreamInput) {
	for {
		select {
		case <-ctx.Done():
			m.Close()
			return

		case input, ok := <-in:
			if !ok {
				return
			}
			logger := m.log.With("session", input.SessionID)
			logger.Debug("received stream input")

			m.mu.RLock()
			ss, exists := m.sessions[input.SessionID]
			m.mu.RUnlock()
			if !exists {
				logger.Error("session not found")
				continue
			}

			if err := m.writeToSession(ss, input.Data); err != nil {
				logger.Error("error writing to session", "error", err)
			}

		case resize := <-m.resize:
			logger := m.log.With("session", resize.SessionID)
			logger.Debug("received stream resize")

			m.mu.RLock()
			ss, exists := m.sessions[resize.SessionID]
			m.mu.RUnlock()
			if !exists {
				logger.Error("session not found")
				continue
			}

			if ss.ttyResize {
				if err := ss.terminal.Resize(resize.Rows, resize.Cols); err != nil {
					logger.Error("failed to resize terminal", "error", err)
				}
			} else {
				select {
				case ss.resize <- SessionResizeInput{Cols: int32(resize.Cols), Rows: int32(resize.Rows)}:
				case <-m.clock.After(ResizeTimeout):
					logger.Error("timeout resizing session")
				}
			}
		}
	}
}

// GetSession returns a session by ID.
func (m *Manager) GetSession(_ *types.PluginContext, sessionID string) (*Session, error) {
	m.mu.RLock()
	ss, exists := m.sessions[sessionID]
	m.mu.RUnlock()

	if !exists {
		return nil, NewSessionNotFoundError(sessionID)
	}
	snap := ss.snapshot()
	return &snap, nil
}

// ListSessions returns a list of details for all active sessions.
func (m *Manager) ListSessions(_ *types.PluginContext) ([]*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sessions := make([]*Session, 0, len(m.sessions))
	for _, ss := range m.sessions {
		snap := ss.snapshot()
		sessions = append(sessions, &snap)
	}
	return sessions, nil
}

// CreateSession creates a new terminal session with a given command.
func (m *Manager) CreateSession(
	pluginctx *types.PluginContext,
	opts SessionOptions,
) (*Session, error) {
	logger := m.log.With(
		"resource", opts.ResourceKey,
		"params", opts.Params,
		"labels", opts.Labels,
	)

	// Look up handler
	handlerKey := opts.ResourcePlugin + "/" + opts.ResourceKey
	handler, ok := m.handlers[handlerKey]
	if !ok {
		available := make([]string, 0, len(m.handlers))
		for k := range m.handlers {
			available = append(available, k)
		}
		return nil, NewHandlerNotFoundError(handlerKey, available)
	}

	opts.ID = ensureSessionID(opts.ID)

	m.mu.Lock()
	if _, exists := m.sessions[opts.ID]; exists {
		m.mu.Unlock()
		return nil, NewSessionExistsError(opts.ID)
	}
	m.mu.Unlock()

	// Shallow-copy PluginContext to avoid mutating the caller's shared instance.
	pctxCopy := *pluginctx
	ctx, cancel := context.WithCancel(pluginctx.Context)
	pctxCopy.Context = ctx
	if m.settingsProvider != nil {
		pctxCopy.SetSettingsProvider(m.settingsProvider)
	}

	// Create terminal via factory
	terminal, err := m.terminalFactory()
	if err != nil {
		cancel()
		return nil, NewTerminalError(opts.ID, err)
	}

	resizeChan := make(chan SessionResizeInput)
	stopChan := make(chan error, 1)

	// Call the handler with the TTY side
	if err = handler.TTYHandler(&pctxCopy, opts, terminal.SlaveFd(), stopChan, resizeChan); err != nil {
		cancel()
		terminal.Close()
		return nil, NewTerminalError(opts.ID, err)
	}

	ss := &sessionState{
		session: Session{
			ID:        opts.ID,
			Labels:    opts.Labels,
			Params:    opts.Params,
			Command:   opts.Command,
			Attached:  false,
			CreatedAt: m.clock.Now(),
		},
		ctx:       ctx,
		cancel:    cancel,
		terminal:  terminal,
		buffer:    NewOutputBuffer(DefaultOutputBufferSize),
		ttyResize: !handler.HandlesResize,
		resize:    resizeChan,
		stopChan:  stopChan,
		done:      make(chan struct{}),
	}

	m.mu.Lock()
	m.sessions[opts.ID] = ss
	m.mu.Unlock()

	logger.Debug("session created", "session", opts.ID)

	// Start goroutines tracked by the sessionState's WaitGroup.
	ss.wg.Add(2)
	go m.handleStream(ss)
	go m.handleSignals(ss)

	// Manager-level goroutine waits for session to finish and cleans up.
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ss.wg.Wait()
		close(ss.done)
	}()

	snap := ss.snapshot()
	return &snap, nil
}

func (m *Manager) handleSignals(ss *sessionState) {
	defer ss.wg.Done()

	logger := m.log.With("session", ss.session.ID)

	select {
	case err := <-ss.stopChan:
		logger.Debug("stop channel received, stopping read stream handling")
		m.cleanupSession(ss, err)

	case <-ss.ctx.Done():
		logger.Debug("context cancelled, stopping read stream handling")
		m.cleanupSession(ss, nil)
	}
}

func (m *Manager) handleStream(ss *sessionState) {
	defer ss.wg.Done()

	logger := m.log.With("session", ss.session.ID)
	masterFd := ss.terminal.MasterFd()

	for {
		buf := make([]byte, DefaultStreamBufferSize)
		read, err := masterFd.Read(buf)
		if err != nil {
			if err != io.EOF {
				logger.Error("error reading from terminal", "error", err)
			} else {
				logger.Debug("EOF reached on terminal")
			}
			// On read error or EOF, cancel so handleSignals fires cleanup.
			ss.cancel()
			return
		}

		if read > 0 {
			output := StreamOutput{
				SessionID: ss.session.ID,
				Target:    StreamTargetStdOut,
				Data:      buf[:read],
			}
			if m.sink != nil {
				m.sink.OnOutput(output)
			}
			ss.buffer.Append(buf[:read])
		}
	}
}

// cleanupSession removes a session from the map, closes the terminal, and
// emits the appropriate signals via the sink.
func (m *Manager) cleanupSession(ss *sessionState, handlerErr error) {
	sessionID := ss.session.ID

	// Close terminal
	ss.terminal.Close()

	// Remove from map
	m.mu.Lock()
	delete(m.sessions, sessionID)
	m.mu.Unlock()

	// Cancel the context to unblock handleStream if it hasn't returned yet.
	ss.cancel()

	// Emit structured error if the handler sent one.
	if handlerErr != nil {
		var streamErr *StreamError
		var execErr *ExecError
		if errors.As(handlerErr, &execErr) {
			streamErr = &StreamError{
				Title:         execErr.Title,
				Message:       execErr.Message,
				Suggestion:    execErr.Suggestion,
				Retryable:     execErr.Retryable,
				RetryCommands: execErr.RetryCommands,
			}
		} else {
			streamErr = &StreamError{
				Title:      "Session failed",
				Message:    handlerErr.Error(),
				Suggestion: "The exec session terminated unexpectedly.",
				Retryable:  true,
			}
		}
		if m.sink != nil {
			m.sink.OnOutput(StreamOutput{
				SessionID: sessionID,
				Target:    StreamTargetStdErr,
				Data:      []byte(handlerErr.Error()),
				Signal:    StreamSignalError,
				Error:     streamErr,
			})
		}
	}

	// Signal close
	if m.sink != nil {
		m.sink.OnOutput(StreamOutput{
			SessionID: sessionID,
			Target:    StreamTargetStdOut,
			Data:      []byte("\nSession closed\n"),
			Signal:    StreamSignalClose,
		})
	}
}

func (m *Manager) writeToSession(ss *sessionState, data []byte) error {
	if ss == nil {
		return errors.New("session is nil")
	}
	if ss.terminal == nil {
		return NewSessionClosedError(ss.session.ID)
	}
	if _, err := ss.terminal.MasterFd().Write(data); err != nil {
		return NewTerminalError(ss.session.ID, err)
	}
	return nil
}

// AttachSession marks a session as attached and returns its current output buffer.
func (m *Manager) AttachSession(
	_ *types.PluginContext,
	sessionID string,
) (*Session, []byte, error) {
	m.mu.RLock()
	ss, exists := m.sessions[sessionID]
	m.mu.RUnlock()

	if !exists {
		return nil, nil, NewSessionNotFoundError(sessionID)
	}

	ss.setAttached(true)
	m.log.Debug("session attached", "session", sessionID)

	snap := ss.snapshot()
	return &snap, ss.getBufferData(), nil
}

// DetachSession marks a session as not attached, stopping output broadcast.
func (m *Manager) DetachSession(_ *types.PluginContext, sessionID string) (*Session, error) {
	m.mu.RLock()
	ss, exists := m.sessions[sessionID]
	m.mu.RUnlock()

	if !exists {
		return nil, NewSessionNotFoundError(sessionID)
	}

	ss.setAttached(false)
	m.log.Debug("session detached", "session", sessionID)

	snap := ss.snapshot()
	return &snap, nil
}

// CloseSession cancels the session's context, triggering cleanup.
func (m *Manager) CloseSession(_ *types.PluginContext, sessionID string) error {
	m.mu.RLock()
	ss, exists := m.sessions[sessionID]
	m.mu.RUnlock()

	if !exists {
		return NewSessionNotFoundError(sessionID)
	}

	ss.cancel()
	m.log.Debug("session terminated", "session", sessionID)

	// Wait for session goroutines to complete with a safety timeout.
	select {
	case <-ss.waitDone():
	case <-time.After(10 * time.Second):
		m.log.Warn("CloseSession timed out waiting for goroutines", "session_id", sessionID)
	}
	return nil
}

// ResizeSession resizes a session.
func (m *Manager) ResizeSession(
	_ *types.PluginContext,
	sessionID string,
	rows, cols int32,
) error {
	m.resize <- StreamResize{
		SessionID: sessionID,
		Rows:      uint16(rows),
		Cols:      uint16(cols),
	}
	return nil
}

// emitOutput sends output through the sink if available.
func (m *Manager) emitOutput(output StreamOutput) {
	if m.sink == nil {
		return
	}
	m.sink.OnOutput(output)
}
