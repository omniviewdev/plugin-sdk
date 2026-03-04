package logs

import (
	"bufio"
	"cmp"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/utils/timeutil"
	"github.com/omniviewdev/plugin-sdk/settings"
)

const (
	MaxConcurrentStreamsPerSession = 20
	MaxScannerBuffer               = 1024 * 1024 // 1MB max line length
	scannerInitBuf                 = 4096        // 4KB initial; grows to MaxScannerBuffer on demand
	ReconnectMaxAttempts           = 5
	ReconnectInitialDelay          = 1 * time.Second
	ReconnectMaxDelay              = 30 * time.Second
)

// ---------------------------------------------------------------------------
// sessionState — per-session, mutex-protected state
// ---------------------------------------------------------------------------

type sessionState struct {
	mu      sync.RWMutex
	session LogSession // value type, copy on read

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{} // closed when all goroutines for this session complete

	sourceMu   sync.Mutex
	sourceCtxs map[string]context.CancelFunc

	// sourceWg tracks watchSourceEvents and dynamically spawned streamSource
	// goroutines so orchestrateSession can wait for them before closing done.
	sourceWg sync.WaitGroup

	// sourceSem limits concurrent streaming goroutines per session.
	// Initialized once in orchestrateSession before any sources launch.
	sourceSem chan struct{}

	// Readiness — totalSources set BEFORE goroutines start (no race)
	totalSources     int32        // immutable after set in orchestrateSession
	readySources     atomic.Int32 // number of unique sources that sent their first line
	readied          atomic.Bool  // whether SESSION_READY has been emitted
	readySet         sync.Map     // map[string]struct{} — tracks which source IDs have been counted
	initialSourceIDs map[string]struct{} // snapshot of source IDs at session init; only these gate readiness

	opts      CreateSessionOptions
	pluginCtx *types.PluginContext
}

// snapshot returns a copy of the session for safe reads.
func (s *sessionState) snapshot() LogSession {
	s.mu.RLock()
	cp := s.session
	cp.ActiveSources = slices.Clone(s.session.ActiveSources)
	s.mu.RUnlock()
	return cp
}

// transition performs a CAS on session status. Returns true if the transition
// succeeded (old status matched `from`).
func (s *sessionState) transition(from, to LogSessionStatus) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session.Status != from {
		return false
	}
	s.session.Status = to
	return true
}

// setStatus forces the status to the given value.
func (s *sessionState) setStatus(to LogSessionStatus) {
	s.mu.Lock()
	s.session.Status = to
	s.mu.Unlock()
}

// setSources replaces the active sources list.
func (s *sessionState) setSources(sources []LogSource) {
	s.mu.Lock()
	s.session.ActiveSources = slices.Clone(sources)
	s.mu.Unlock()
}

// addSource appends a source to the active list.
func (s *sessionState) addSource(src LogSource) {
	s.mu.Lock()
	s.session.ActiveSources = append(s.session.ActiveSources, src)
	s.mu.Unlock()
}

// removeSource removes a source from the active list by ID.
func (s *sessionState) removeSource(sourceID string) {
	s.mu.Lock()
	s.session.ActiveSources = slices.DeleteFunc(s.session.ActiveSources, func(src LogSource) bool {
		return src.ID == sourceID
	})
	s.mu.Unlock()
}

// status returns the current session status.
func (s *sessionState) status() LogSessionStatus {
	s.mu.RLock()
	st := s.session.Status
	s.mu.RUnlock()
	return st
}

// ---------------------------------------------------------------------------
// ManagerConfig
// ---------------------------------------------------------------------------

// ManagerConfig configures the Manager.
type ManagerConfig struct {
	Logger    logging.Logger
	Settings  settings.Provider
	Handlers  map[string]Handler
	Resolvers map[string]SourceResolver
	Sink      OutputSink     // optional: nil → ChannelSink created by Stream()
	Clock     timeutil.Clock // optional: nil → timeutil.RealClock
}

// ---------------------------------------------------------------------------
// Manager
// ---------------------------------------------------------------------------

// Manager manages the lifecycle of log sessions within a plugin process.
type Manager struct {
	log      logging.Logger
	settings settings.Provider
	registry *handlerRegistry
	sink     OutputSink
	clock    timeutil.Clock

	mu       sync.RWMutex
	sessions map[string]*sessionState
	out      chan StreamOutput // nil when Sink provided via config
	wg       sync.WaitGroup
}

var _ Provider = (*Manager)(nil)

// NewManager creates a new Manager with the given config.
func NewManager(cfg ManagerConfig) *Manager {
	logger := cfg.Logger
	if logger == nil {
		logger = logging.NewNop()
	}

	clk := cfg.Clock
	if clk == nil {
		clk = timeutil.RealClock{}
	}

	return &Manager{
		log:      logger.Named("logs.manager"),
		settings: cfg.Settings,
		registry: newHandlerRegistry(logger, cfg.Handlers, cfg.Resolvers),
		sink:     cfg.Sink,
		clock:    clk,
		sessions: make(map[string]*sessionState),
	}
}

// ---------------------------------------------------------------------------
// Provider interface implementation
// ---------------------------------------------------------------------------

func (m *Manager) GetSupportedResources(_ *types.PluginContext) []Handler {
	return m.registry.AllHandlers()
}

func (m *Manager) CreateSession(
	pluginctx *types.PluginContext,
	opts CreateSessionOptions,
) (*LogSession, error) {
	if pluginctx == nil {
		return nil, fmt.Errorf("plugin context is nil")
	}

	logger := m.log.With(
		logging.String("resource_key", opts.ResourceKey),
		logging.String("resource_id", opts.ResourceID),
	)

	sessionID := uuid.NewString()
	ctx, cancel := context.WithCancel(context.Background())

	// Copy the PluginContext so we don't mutate the caller's shared instance.
	pctxCopy := *pluginctx
	pctxCopy.Context = ctx
	if m.settings != nil {
		pctxCopy.SetSettingsProvider(m.settings)
	}
	pctxCopy.Logger = m.log.With(
		logging.String("session_id", sessionID),
		logging.String("resource_key", opts.ResourceKey),
	)

	session := LogSession{
		ID:          sessionID,
		ResourceKey: opts.ResourceKey,
		ResourceID:  opts.ResourceID,
		Options:     opts.Options,
		Status:      LogSessionStatusConnecting,
		CreatedAt:   m.clock.Now(),
	}

	ss := &sessionState{
		session:    session,
		ctx:        ctx,
		cancel:     cancel,
		done:       make(chan struct{}),
		sourceCtxs: make(map[string]context.CancelFunc),
		opts:       opts,
		pluginCtx:  &pctxCopy,
	}

	m.mu.Lock()
	m.sessions[sessionID] = ss
	m.mu.Unlock()

	m.wg.Add(1)
	go m.orchestrateSession(ss)

	logger.Debugw(ctx, "log session created", "session_id", sessionID)
	snap := ss.snapshot()
	return &snap, nil
}

func (m *Manager) GetSession(_ *types.PluginContext, sessionID string) (*LogSession, error) {
	m.mu.RLock()
	ss, ok := m.sessions[sessionID]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("log session %s not found", sessionID)
	}
	snap := ss.snapshot()
	return &snap, nil
}

func (m *Manager) ListSessions(_ *types.PluginContext) ([]*LogSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sessions := make([]*LogSession, 0, len(m.sessions))
	for _, ss := range m.sessions {
		snap := ss.snapshot()
		sessions = append(sessions, &snap)
	}

	// Sort by ID for deterministic output (map iteration order is random).
	slices.SortFunc(sessions, func(a, b *LogSession) int {
		return cmp.Compare(a.ID, b.ID)
	})

	return sessions, nil
}

func (m *Manager) CloseSession(_ *types.PluginContext, sessionID string) error {
	m.mu.Lock()
	ss, ok := m.sessions[sessionID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("log session %s not found", sessionID)
	}
	delete(m.sessions, sessionID)
	m.mu.Unlock()

	ss.cancel()
	ss.setStatus(LogSessionStatusClosed)

	// Wait for all goroutines for this session to finish, with a safety timeout
	// to prevent blocking forever if a goroutine is stuck.
	select {
	case <-ss.done:
	case <-m.clock.After(10 * time.Second):
		m.log.Warnw(ss.ctx, "CloseSession timed out waiting for goroutines", "session_id", sessionID)
	}
	return nil
}

func (m *Manager) UpdateSessionOptions(
	_ *types.PluginContext,
	sessionID string,
	opts LogSessionOptions,
) (*LogSession, error) {
	m.mu.RLock()
	ss, ok := m.sessions[sessionID]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("log session %s not found", sessionID)
	}

	ss.mu.Lock()
	ss.session.Options = opts
	ss.opts.Options = opts
	ss.mu.Unlock()

	if enabledStr, hasEnabled := opts.Params["enabled_sources"]; hasEnabled {
		m.updateEnabledSources(ss, enabledStr)
	}

	snap := ss.snapshot()
	return &snap, nil
}

func (m *Manager) Stream(ctx context.Context, in chan StreamInput) (chan StreamOutput, error) {
	m.mu.Lock()
	if m.sink == nil {
		m.out = make(chan StreamOutput, 256)
		m.sink = NewChannelSink(ctx, m.out)
	}
	out := m.out
	m.mu.Unlock()

	go m.handleCommands(ctx, in)

	return out, nil
}

// Wait blocks until all session goroutines finish.
func (m *Manager) Wait() {
	m.wg.Wait()
}

// Close cancels all active sessions and waits for their goroutines to finish.
func (m *Manager) Close() {
	m.mu.Lock()
	for id, ss := range m.sessions {
		ss.cancel()
		ss.setStatus(LogSessionStatusClosed)
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	m.wg.Wait()
}

// ---------------------------------------------------------------------------
// Internal: orchestration
// ---------------------------------------------------------------------------

func (m *Manager) orchestrateSession(ss *sessionState) {
	defer m.wg.Done()
	defer func() {
		ss.sourceWg.Wait()
		close(ss.done)
	}()

	// Initialize the session-level concurrency semaphore once, before any
	// source goroutines are launched (applies to fanOutSources, dynamic
	// source events, and re-enabled sources alike).
	ss.sourceSem = make(chan struct{}, MaxConcurrentStreamsPerSession)

	logger := m.log.With(logging.String("session_id", ss.session.ID))

	// Copy opts under the session lock — UpdateSessionOptions writes
	// ss.opts.Options under ss.mu, so reads must be synchronized.
	ss.mu.RLock()
	opts := ss.opts
	ss.mu.RUnlock()

	// Try direct handler first
	handler, hasHandler := m.registry.FindHandler(opts.ResourceKey)

	if hasHandler {
		m.streamFromHandler(ss, handler, opts, logger)
		return
	}

	// Try source resolver
	resolver, hasResolver := m.registry.FindResolver(opts.ResourceKey)
	if !hasResolver {
		logger.Errorw(ss.ctx, "no handler or resolver found for resource", "key", opts.ResourceKey)
		ss.setStatus(LogSessionStatusError)
		m.emitEvent(ss.session.ID, LogStreamEvent{
			Type:      StreamEventStreamError,
			Message:   fmt.Sprintf("no log handler found for resource type %s", opts.ResourceKey),
			Timestamp: m.clock.Now(),
		})
		return
	}

	// Resolve sources
	result, err := resolver(ss.pluginCtx, opts.ResourceData, SourceResolverOptions{
		Watch:  opts.Options.Follow,
		Target: opts.Options.Target,
		Params: opts.Options.Params,
	})
	if err != nil {
		logger.Errorw(ss.ctx, "failed to resolve sources", "error", err)
		ss.setStatus(LogSessionStatusError)
		m.emitEvent(ss.session.ID, LogStreamEvent{
			Type:      StreamEventStreamError,
			Message:   fmt.Sprintf("failed to resolve sources: %v", err),
			Timestamp: m.clock.Now(),
		})
		return
	}

	// Find a handler for the resolved sources
	h, ok := m.registry.AnyHandler()
	if !ok {
		logger.Error(ss.ctx, "no handler available for resolved sources")
		ss.setStatus(LogSessionStatusError)
		m.emitEvent(ss.session.ID, LogStreamEvent{
			Type:      StreamEventStreamError,
			Message:   "no log handler available for resolved sources",
			Timestamp: m.clock.Now(),
		})
		return
	}

	// Set sources and totalSources BEFORE starting goroutines (fixes bug #1, #2)
	ss.setSources(result.Sources)
	total := int32(len(result.Sources))
	// For Follow sessions, only MaxConcurrentStreamsPerSession sources can
	// stream concurrently. Cap totalSources so markSourceReady can reach
	// it; otherwise the session hangs in INITIALIZING forever.
	if opts.Options.Follow && total > MaxConcurrentStreamsPerSession {
		total = MaxConcurrentStreamsPerSession
	}
	ss.totalSources = total
	// Snapshot the initial source IDs so markSourceReady only counts these
	// for the INITIALIZING → ACTIVE transition. Dynamic sources added later
	// via watchSourceEvents won't prematurely satisfy readiness.
	ss.initialSourceIDs = make(map[string]struct{}, len(result.Sources))
	for _, src := range result.Sources {
		ss.initialSourceIDs[src.ID] = struct{}{}
	}
	ss.transition(LogSessionStatusConnecting, LogSessionStatusInitializing)

	// If the resolver returned zero sources, transition directly to ACTIVE
	// because markSourceReady will never be called.
	if len(result.Sources) == 0 {
		ss.transition(LogSessionStatusInitializing, LogSessionStatusActive)
		m.emitEvent(ss.session.ID, LogStreamEvent{
			Type:      StreamEventSessionReady,
			Message:   "No sources to stream (resolved 0 sources)",
			Timestamp: m.clock.Now(),
		})
	}

	// Watch for dynamic source changes BEFORE the blocking fanOutSources call,
	// because fanOutSources blocks until all source goroutines complete (which
	// for Follow=true sessions, only happens when the context is cancelled).
	if result.Events != nil {
		ss.sourceWg.Add(1)
		go func() {
			defer ss.sourceWg.Done()
			m.watchSourceEvents(ss, h, result.Events, logger)
		}()
	}

	m.fanOutSources(ss, h, result.Sources, opts, logger)
}

func (m *Manager) streamFromHandler(ss *sessionState, handler Handler, opts CreateSessionOptions, logger logging.Logger) {
	var sources []LogSource
	if handler.SourceBuilder != nil {
		sources = handler.SourceBuilder(opts.ResourceID, opts.ResourceData, opts.Options)
	} else {
		sources = []LogSource{{ID: opts.ResourceID, Labels: make(map[string]string)}}
	}

	if len(sources) == 0 {
		logger.Warnw(ss.ctx, "source builder returned no sources", "resource_key", opts.ResourceKey)
		ss.setStatus(LogSessionStatusError)
		m.emitEvent(ss.session.ID, LogStreamEvent{
			Type:      StreamEventStreamError,
			Message:   "No log sources found for this resource",
			Timestamp: m.clock.Now(),
		})
		return
	}

	// Set sources and totalSources BEFORE starting goroutines (fixes bug #1, #2)
	ss.setSources(sources)
	total := int32(len(sources))
	if opts.Options.Follow && total > MaxConcurrentStreamsPerSession {
		total = MaxConcurrentStreamsPerSession
	}
	ss.totalSources = total
	ss.initialSourceIDs = make(map[string]struct{}, len(sources))
	for _, src := range sources {
		ss.initialSourceIDs[src.ID] = struct{}{}
	}
	ss.transition(LogSessionStatusConnecting, LogSessionStatusInitializing)

	m.fanOutSources(ss, handler, sources, opts, logger)
}

func (m *Manager) fanOutSources(
	ss *sessionState,
	handler Handler,
	sources []LogSource,
	opts CreateSessionOptions,
	logger logging.Logger,
) {
	var wg sync.WaitGroup
	for _, source := range sources {
		wg.Add(1)
		go func(src LogSource) {
			defer wg.Done()
			m.launchSource(ss, handler, src, opts, logger)
		}(source)
	}
	wg.Wait()
}

// launchSource registers a cancellable context for the source, acquires the
// session-level concurrency semaphore, then runs streamSource. All source
// launches (initial fan-out, dynamic events, re-enabled sources) go through
// this method to enforce MaxConcurrentStreamsPerSession uniformly.
//
// Registering in sourceCtxs BEFORE acquiring the semaphore ensures that a
// SourceRemoved event can cancel a queued source that hasn't started streaming.
func (m *Manager) launchSource(
	ss *sessionState,
	handler Handler,
	source LogSource,
	opts CreateSessionOptions,
	logger logging.Logger,
) {
	// Register a cancellable context so SourceRemoved can cancel queued sources.
	sourceCtx, sourceCancel := context.WithCancel(ss.ctx)
	ss.sourceMu.Lock()
	ss.sourceCtxs[source.ID] = sourceCancel
	ss.sourceMu.Unlock()

	// Acquire semaphore with context check to avoid blocking on shutdown
	// or if this specific source was cancelled while queued.
	select {
	case ss.sourceSem <- struct{}{}:
	case <-sourceCtx.Done():
		ss.sourceMu.Lock()
		delete(ss.sourceCtxs, source.ID)
		ss.sourceMu.Unlock()
		// Count as "ready" so the session doesn't get stuck in INITIALIZING
		// waiting for a source that will never stream.
		m.markSourceReady(ss, source.ID)
		return
	}
	defer func() { <-ss.sourceSem }()

	// If cancelled while waiting, bail out.
	if sourceCtx.Err() != nil {
		ss.sourceMu.Lock()
		delete(ss.sourceCtxs, source.ID)
		ss.sourceMu.Unlock()
		m.markSourceReady(ss, source.ID)
		return
	}

	m.streamSource(sourceCtx, ss, handler, source, opts, logger)
}

// ---------------------------------------------------------------------------
// Internal: per-source streaming
// ---------------------------------------------------------------------------

func (m *Manager) streamSource(
	sourceCtx context.Context,
	ss *sessionState,
	handler Handler,
	source LogSource,
	opts CreateSessionOptions,
	logger logging.Logger,
) {
	logger = logger.With(logging.String("source_id", source.ID))

	// sourceCtx is the cancellable context created by launchSource and
	// registered in ss.sourceCtxs. We reuse it directly so that a
	// SourceRemoved cancelling the entry in sourceCtxs also cancels
	// this streaming goroutine — no independent context needed.

	defer func() {
		ss.sourceMu.Lock()
		delete(ss.sourceCtxs, source.ID)
		ss.sourceMu.Unlock()
	}()

	if opts.Options.IncludeSourceEvents {
		m.emitEvent(ss.session.ID, LogStreamEvent{
			Type:      StreamEventSourceAdded,
			SourceID:  source.ID,
			Message:   fmt.Sprintf("Started streaming from %s", source.ID),
			Timestamp: m.clock.Now(),
		})
	}

	req := LogStreamRequest{
		SourceID:          source.ID,
		Labels:            source.Labels,
		ResourceData:      opts.ResourceData,
		Target:            opts.Options.Target,
		Follow:            opts.Options.Follow,
		IncludePrevious:   opts.Options.IncludePrevious,
		IncludeTimestamps: opts.Options.IncludeTimestamps,
		TailLines:         opts.Options.TailLines,
		SinceSeconds:      opts.Options.SinceSeconds,
		SinceTime:         opts.Options.SinceTime,
		LimitBytes:        opts.Options.LimitBytes,
		Params:            opts.Options.Params,
	}

	m.streamWithReconnect(sourceCtx, ss, handler, source, req, logger)
}

func (m *Manager) streamWithReconnect(
	ctx context.Context,
	ss *sessionState,
	handler Handler,
	source LogSource,
	req LogStreamRequest,
	logger logging.Logger,
) {
	delay := ReconnectInitialDelay

	for attempt := 0; attempt <= ReconnectMaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return
		}

		if attempt > 0 {
			m.emitEvent(ss.session.ID, LogStreamEvent{
				Type:      StreamEventReconnecting,
				SourceID:  source.ID,
				Message:   fmt.Sprintf("Reconnecting (attempt %d/%d)", attempt, ReconnectMaxAttempts),
				Timestamp: m.clock.Now(),
			})

			select {
			case <-ctx.Done():
				return
			case <-m.clock.After(delay):
			}

			delay *= 2
			if delay > ReconnectMaxDelay {
				delay = ReconnectMaxDelay
			}
		}

		// Shallow-copy the PluginContext with the source-scoped context
		// so the handler respects per-source cancellation.
		pctxCopy := *ss.pluginCtx
		pctxCopy.Context = ctx
		pctxCopy.Logger = logger
		reader, err := handler.Handler(&pctxCopy, req)
		if err != nil {
			if ctx.Err() != nil {
				return // context cancelled, stop reconnecting
			}
			logger.Errorw(ctx, "failed to open log stream", "error", err, "attempt", attempt)
			continue
		}
		if reader == nil {
			logger.Errorw(ctx, "handler returned nil reader without error", "source", source.ID, "attempt", attempt)
			continue
		}

		if attempt > 0 {
			m.emitEvent(ss.session.ID, LogStreamEvent{
				Type:      StreamEventReconnected,
				SourceID:  source.ID,
				Message:   "Reconnected",
				Timestamp: m.clock.Now(),
			})
		}

		err = m.readStream(ctx, ss, source, reader)
		reader.Close()

		if ctx.Err() != nil {
			return
		}

		if err == nil || err == io.EOF {
			if !req.Follow {
				return
			}
		}

		logger.Warnw(ctx, "log stream interrupted", "error", err, "source", source.ID)
	}

	m.emitEvent(ss.session.ID, LogStreamEvent{
		Type:      StreamEventStreamError,
		SourceID:  source.ID,
		Message:   fmt.Sprintf("Failed to reconnect after %d attempts", ReconnectMaxAttempts),
		Timestamp: m.clock.Now(),
	})
}

func (m *Manager) readStream(
	ctx context.Context,
	ss *sessionState,
	source LogSource,
	reader io.ReadCloser,
) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, scannerInitBuf), MaxScannerBuffer)

	firstLine := true
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Skip output if paused (bug #12: documented behavior)
		if ss.status() == LogSessionStatusPaused {
			continue
		}

		line := scanner.Bytes()
		ts, content := extractTimestamp(line)

		m.mu.RLock()
		sink := m.sink
		m.mu.RUnlock()
		if sink != nil {
			sink.OnLine(LogLine{
				SessionID: ss.session.ID,
				SourceID:  source.ID,
				Labels:    source.Labels,
				Timestamp: ts,
				Content:   content,
				Origin:    LogLineOriginCurrent,
			})
		}

		if firstLine {
			firstLine = false
			m.markSourceReady(ss, source.ID)
		}
	}

	// If we never received a line, still mark ready so we don't block
	// the ACTIVE transition (the source simply has no output).
	if firstLine {
		m.markSourceReady(ss, source.ID)
	}

	return scanner.Err()
}

// markSourceReady increments the ready source counter for unique sources and,
// when all initial sources have reported, transitions the session to ACTIVE and
// emits SESSION_READY. Reconnects for the same source ID are deduplicated.
//
// Only sources in the initial snapshot (initialSourceIDs) count toward the
// readiness threshold while the session hasn't transitioned to ACTIVE yet.
// Dynamic sources added later are tracked in readySet for deduplication but
// don't affect the INITIALIZING → ACTIVE gate.
func (m *Manager) markSourceReady(ss *sessionState, sourceID string) {
	if _, loaded := ss.readySet.LoadOrStore(sourceID, struct{}{}); loaded {
		return // already counted for this source
	}
	// If the session hasn't reached ACTIVE yet, only count initial sources
	// toward the readiness threshold. Dynamic sources are ignored for gating.
	if !ss.readied.Load() {
		if _, isInitial := ss.initialSourceIDs[sourceID]; !isInitial {
			return
		}
	}
	ready := ss.readySources.Add(1)
	if ready >= ss.totalSources && ss.readied.CompareAndSwap(false, true) {
		ss.transition(LogSessionStatusInitializing, LogSessionStatusActive)
		m.emitEvent(ss.session.ID, LogStreamEvent{
			Type:      StreamEventSessionReady,
			Message:   "All sources connected",
			Timestamp: m.clock.Now(),
		})
	}
}

// ---------------------------------------------------------------------------
// Internal: dynamic source events
// ---------------------------------------------------------------------------

func (m *Manager) watchSourceEvents(
	ss *sessionState,
	handler Handler,
	events <-chan SourceEvent,
	logger logging.Logger,
) {
	for {
		select {
		case <-ss.ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}

			switch event.Type {
			case SourceAdded:
				ss.addSource(event.Source)
				m.emitEvent(ss.session.ID, LogStreamEvent{
					Type:      StreamEventSourceAdded,
					SourceID:  event.Source.ID,
					Message:   fmt.Sprintf("Source added: %s", event.Source.ID),
					Timestamp: m.clock.Now(),
				})
				// Read fresh session options under lock so dynamic sources
				// pick up any changes from UpdateSessionOptions.
				ss.mu.RLock()
				currentOpts := ss.opts
				ss.mu.RUnlock()
				ss.sourceWg.Add(1)
				go func() {
					defer ss.sourceWg.Done()
					m.launchSource(ss, handler, event.Source, currentOpts, logger)
				}()

			case SourceRemoved:
				// Cancel the source's streaming goroutine so it stops retrying
				ss.sourceMu.Lock()
				if cancel, ok := ss.sourceCtxs[event.Source.ID]; ok {
					cancel()
				}
				ss.sourceMu.Unlock()

				ss.removeSource(event.Source.ID)
				m.emitEvent(ss.session.ID, LogStreamEvent{
					Type:      StreamEventSourceRemoved,
					SourceID:  event.Source.ID,
					Message:   fmt.Sprintf("Source removed: %s", event.Source.ID),
					Timestamp: m.clock.Now(),
				})
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Internal: commands and events
// ---------------------------------------------------------------------------

func (m *Manager) handleCommands(ctx context.Context, in <-chan StreamInput) {
	for {
		select {
		case <-ctx.Done():
			m.closeAll()
			return
		case input, ok := <-in:
			if !ok {
				// Input channel closed — shut down gracefully.
				m.closeAll()
				return
			}

			m.mu.RLock()
			ss, found := m.sessions[input.SessionID]
			m.mu.RUnlock()

			if !found {
				m.log.Errorw(ctx, "session not found for stream command", "session_id", input.SessionID)
				continue
			}

			switch input.Command {
			case StreamCommandPause:
				ss.setStatus(LogSessionStatusPaused)
			case StreamCommandResume:
				ss.setStatus(LogSessionStatusActive)
			case StreamCommandClose:
				_ = m.CloseSession(nil, input.SessionID)
			}
		}
	}
}

func (m *Manager) emitEvent(sessionID string, event LogStreamEvent) {
	m.mu.RLock()
	sink := m.sink
	m.mu.RUnlock()
	if sink == nil {
		return
	}
	sink.OnEvent(sessionID, event)
}

func (m *Manager) closeAll() {
	m.mu.Lock()
	for _, ss := range m.sessions {
		ss.cancel()
	}
	m.sessions = make(map[string]*sessionState)
	m.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Internal: enabled sources update
// ---------------------------------------------------------------------------

func (m *Manager) updateEnabledSources(ss *sessionState, enabledStr string) {
	enabledSet := make(map[string]bool)
	allEnabled := enabledStr == ""
	if !allEnabled {
		for _, id := range strings.Split(enabledStr, ",") {
			id = strings.TrimSpace(id)
			if id != "" {
				enabledSet[id] = true
			}
		}
	}

	var removedEvents []LogStreamEvent
	ss.sourceMu.Lock()
	for sourceID, cancel := range ss.sourceCtxs {
		if !allEnabled && !enabledSet[sourceID] {
			cancel()
			removedEvents = append(removedEvents, LogStreamEvent{
				Type:      StreamEventSourceRemoved,
				SourceID:  sourceID,
				Message:   fmt.Sprintf("Source disabled: %s", sourceID),
				Timestamp: m.clock.Now(),
			})
		}
	}

	// Find sources to restart
	snap := ss.snapshot()
	var toRestart []LogSource
	for _, src := range snap.ActiveSources {
		shouldBeEnabled := allEnabled || enabledSet[src.ID]
		if shouldBeEnabled {
			if _, hasCtx := ss.sourceCtxs[src.ID]; !hasCtx {
				toRestart = append(toRestart, src)
			}
		}
	}
	ss.sourceMu.Unlock()

	for _, evt := range removedEvents {
		m.emitEvent(ss.session.ID, evt)
	}

	// Copy opts under the session lock for the same reason as orchestrateSession.
	ss.mu.RLock()
	opts := ss.opts
	ss.mu.RUnlock()

	// Find a handler for restarting
	handler, ok := m.registry.FindHandler(opts.ResourceKey)
	if !ok {
		handler, ok = m.registry.AnyHandler()
	}
	if !ok {
		return
	}

	logger := m.log.With(logging.String("session_id", ss.session.ID))
	for _, src := range toRestart {
		ss.sourceWg.Add(1)
		go func(s LogSource) {
			defer ss.sourceWg.Done()
			m.launchSource(ss, handler, s, opts, logger)
		}(src)
		m.emitEvent(ss.session.ID, LogStreamEvent{
			Type:      StreamEventSourceAdded,
			SourceID:  src.ID,
			Message:   fmt.Sprintf("Source re-enabled: %s", src.ID),
			Timestamp: m.clock.Now(),
		})
	}
}
