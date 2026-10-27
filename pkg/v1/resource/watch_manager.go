package resource

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/pkg/utils/timeutil"
)

const (
	defaultMaxRetries  = 5
	defaultBaseBackoff = 500 * time.Millisecond
	maxBackoff         = 30 * time.Second
)

// resourceWatchState tracks the lifecycle of a single Watch goroutine.
type resourceWatchState struct {
	running bool
	state   WatchState
	count   int // last known resource count from OnStateChange
	cancel  context.CancelFunc
	retries int
	ready   chan struct{} // closed when goroutine is about to call Watch()
	done    chan struct{} // closed when goroutine exits
}

const maxPendingWatchStates = 1000

var errWatchStateReplayOverflow = errors.New("watch state replay exceeded 1000 buffered events")

// watchListener owns only its registration's bounded state replay. Data events
// remain direct. No sink callback runs while mu or a manager lock is held.
type watchListener struct {
	sink      WatchEventSink
	ctx       context.Context
	retired   atomic.Bool
	accepted  bool // guarded by listenersMu
	mu        sync.Mutex
	replaying bool
	overflow  bool
	pending   []WatchStateEvent
}

func (l *watchListener) active() bool {
	return !l.retired.Load() && l.ctx.Err() == nil
}

func (l *watchListener) onStateChange(event WatchStateEvent) {
	l.mu.Lock()
	if !l.active() {
		l.mu.Unlock()
		return
	}
	if l.replaying {
		if len(l.pending) < maxPendingWatchStates {
			l.pending = append(l.pending, event)
		} else {
			l.overflow = true
		}
		l.mu.Unlock()
		return
	}
	l.mu.Unlock()
	l.sink.OnStateChange(event)
}

func (l *watchListener) replay() error {
	for {
		l.mu.Lock()
		if err := l.ctx.Err(); err != nil {
			l.mu.Unlock()
			return err
		}
		if l.retired.Load() {
			l.mu.Unlock()
			return context.Canceled
		}
		if l.overflow {
			l.mu.Unlock()
			return errWatchStateReplayOverflow
		}
		pending := l.pending
		l.pending = nil
		if len(pending) == 0 {
			l.replaying = false
			l.mu.Unlock()
			return nil
		}
		l.mu.Unlock()
		for _, event := range pending {
			if !l.active() {
				if err := l.ctx.Err(); err != nil {
					return err
				}
				return context.Canceled
			}
			l.sink.OnStateChange(event)
		}
	}
}

// connectionWatchState holds all watch state for a single connection.
type connectionWatchState[ClientT any] struct {
	client     *ClientT
	connCtx    context.Context
	resources  map[string]*resourceWatchState // by resource key
	watchScope *WatchScope                    // resolved scope for this connection (nil = unscoped)
}

// watchManager manages Watch goroutine lifecycle for all connections and resources.
// It provides WatchEventSink fan-out to multiple listeners.
type watchManager[ClientT any] struct {
	mu       sync.RWMutex
	registry *resourcerRegistry[ClientT]
	watches  map[string]*connectionWatchState[ClientT] // by connection ID
	log      logging.Logger

	// scopeProvider resolves watch scope for connections (optional).
	scopeProvider ScopeProvider[ClientT]

	// Published slices are immutable; a fan-out snapshot never owns the lock
	// while calling a sink. Removal retires a registration, not its in-flight calls.
	listenersMu    sync.RWMutex
	listeners      []*watchListener
	readyListeners int

	maxRetries  int
	baseBackoff time.Duration
	clock       timeutil.Clock

	// Until a listener finishes replay, retain history for rejected/canceled
	// registration retries. Overflow fails closed instead of hiding a gap.
	pendingMu            sync.Mutex
	pendingStateEvents   []WatchStateEvent
	pendingStateOverflow bool

	// wg tracks all goroutines for clean shutdown.
	wg sync.WaitGroup
}

func newWatchManager[ClientT any](log logging.Logger, registry *resourcerRegistry[ClientT]) *watchManager[ClientT] {
	if log == nil {
		log = logging.NewNop()
	}
	return &watchManager[ClientT]{
		registry:    registry,
		watches:     make(map[string]*connectionWatchState[ClientT]),
		log:         log.Named("watch_manager"),
		maxRetries:  defaultMaxRetries,
		baseBackoff: defaultBaseBackoff,
		clock:       timeutil.RealClock{},
	}
}

// StartConnectionWatch starts watches for all SyncOnConnect-capable resources.
// If discoveredTypes is non-nil, resources not in the set are immediately marked
// as WatchStateSkipped (no goroutine spawned). Pass nil to watch all resources.
func (m *watchManager[ClientT]) StartConnectionWatch(ctx context.Context, connectionID string, client *ClientT, connCtx context.Context, discoveredTypes map[string]bool) error {
	// Collect skipped events to emit after releasing the lock (OnStateChange
	// acquires m.mu internally, so we must not hold it during fan-out).
	var skippedEvents []WatchStateEvent

	m.mu.Lock()

	if _, ok := m.watches[connectionID]; ok {
		m.mu.Unlock()
		return nil // already started
	}

	// Resolve watch scope if a ScopeProvider is configured.
	var watchScope *WatchScope
	if m.scopeProvider != nil {
		mode, partitions, err := m.scopeProvider.ResolveScope(connCtx, client)
		if err == nil && mode != ScopeModeAll && len(partitions) > 0 {
			watchScope = &WatchScope{Partitions: partitions}
		}
		if err != nil {
			m.log.Warnw(connCtx, "scope resolution failed, falling back to unscoped",
				"connection_id", connectionID,
				"error", err,
			)
		}
	}

	cws := &connectionWatchState[ClientT]{
		client:     client,
		connCtx:    connCtx,
		resources:  make(map[string]*resourceWatchState),
		watchScope: watchScope,
	}
	m.watches[connectionID] = cws

	// Start watches for SyncOnConnect resources.
	for _, meta := range m.registry.ListWatchable() {
		policy := m.registry.GetSyncPolicy(meta.Key())
		if policy != SyncOnConnect {
			continue
		}

		// If discovery data is available, skip resources not on this connection.
		if discoveredTypes != nil && !discoveredTypes[meta.Key()] {
			rws := &resourceWatchState{
				running: false,
				state:   WatchStateSkipped,
				cancel:  func() {},
				ready:   make(chan struct{}),
				done:    make(chan struct{}),
			}
			close(rws.ready)
			close(rws.done)
			cws.resources[meta.Key()] = rws
			skippedEvents = append(skippedEvents, WatchStateEvent{
				ResourceKey: meta.Key(),
				State:       WatchStateSkipped,
				Message:     "resource type not available on this connection",
			})
			continue
		}

		m.startWatchLocked(connectionID, meta.Key(), cws)
	}

	m.mu.Unlock()

	// Emit skipped state events outside the lock.
	if len(skippedEvents) > 0 {
		sink := &fanOutSink[ClientT]{mgr: m, connectionID: connectionID}
		for _, evt := range skippedEvents {
			sink.OnStateChange(evt)
		}
	}

	return nil
}

// StopConnectionWatch stops all watches for a connection.
func (m *watchManager[ClientT]) StopConnectionWatch(ctx context.Context, connectionID string) error {
	m.mu.Lock()
	cws, ok := m.watches[connectionID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("connection %q not found in watch manager", connectionID)
	}
	// Cancel all per-resource watches.
	for _, rws := range cws.resources {
		if rws.cancel != nil {
			rws.cancel()
		}
		rws.running = false
		rws.state = WatchStateStopped
	}
	delete(m.watches, connectionID)
	m.mu.Unlock()

	return nil
}

// HasWatch returns true if any watches are tracked for a connection.
func (m *watchManager[ClientT]) HasWatch(connectionID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.watches[connectionID]
	return ok
}

// GetWatchState returns a snapshot of all watch states for a connection.
func (m *watchManager[ClientT]) GetWatchState(connectionID string) (*WatchConnectionSummary, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cws, ok := m.watches[connectionID]
	if !ok {
		return nil, &ResourceOperationError{Code: "NOT_FOUND", Title: "Watch not started", Message: "No watch has been started for this connection."}
	}
	summary := &WatchConnectionSummary{
		ConnectionID:   connectionID,
		Resources:      make(map[string]WatchState, len(cws.resources)),
		ResourceCounts: make(map[string]int, len(cws.resources)),
		Scope:          cws.watchScope,
	}
	var nonIdle int
	for key, rws := range cws.resources {
		summary.Resources[key] = rws.state
		if rws.count > 0 {
			summary.ResourceCounts[key] = rws.count
		}
		if rws.state != WatchStateIdle {
			nonIdle++
			m.log.Debugw(context.Background(), "watch state entry",
				"connection_id", connectionID,
				"resource_key", key,
				"state", rws.state,
				"running", rws.running,
				"count", rws.count,
			)
		}
	}
	m.log.Debugw(context.Background(), "watch state summary",
		"connection_id", connectionID,
		"resource_count", len(cws.resources),
		"non_idle_count", nonIdle,
	)
	return summary, nil
}

// EnsureResourceWatch starts a watch for a specific resource if not already running.
func (m *watchManager[ClientT]) EnsureResourceWatch(ctx context.Context, connectionID string, resourceKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cws, ok := m.watches[connectionID]
	if !ok {
		return fmt.Errorf("connection %q not found in watch manager", connectionID)
	}

	// Already running?
	if rws, ok := cws.resources[resourceKey]; ok && rws.running {
		return nil
	}

	// Verify the resource is watchable.
	if !m.registry.IsWatcher(resourceKey) {
		return fmt.Errorf("resource %q does not support watching", resourceKey)
	}

	m.startWatchLocked(connectionID, resourceKey, cws)
	return nil
}

// StopResourceWatch stops the watch for a specific resource.
func (m *watchManager[ClientT]) StopResourceWatch(ctx context.Context, connectionID string, resourceKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cws, ok := m.watches[connectionID]
	if !ok {
		return nil // not an error if connection doesn't exist
	}
	rws, ok := cws.resources[resourceKey]
	if !ok || !rws.running {
		return nil // not running, no-op
	}
	if rws.cancel != nil {
		rws.cancel()
	}
	rws.running = false
	rws.state = WatchStateStopped
	return nil
}

// RestartResourceWatch stops and restarts a watch for a specific resource.
func (m *watchManager[ClientT]) RestartResourceWatch(ctx context.Context, connectionID string, resourceKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cws, ok := m.watches[connectionID]
	if !ok {
		return fmt.Errorf("connection %q not found", connectionID)
	}

	// Stop existing.
	if rws, ok := cws.resources[resourceKey]; ok && rws.running {
		if rws.cancel != nil {
			rws.cancel()
		}
	}

	if !m.registry.IsWatcher(resourceKey) {
		return fmt.Errorf("resource %q does not support watching", resourceKey)
	}

	m.startWatchLocked(connectionID, resourceKey, cws)
	return nil
}

// IsResourceWatchRunning returns whether a watch is running for a specific resource.
func (m *watchManager[ClientT]) IsResourceWatchRunning(connectionID string, resourceKey string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cws, ok := m.watches[connectionID]
	if !ok {
		return false
	}
	rws, ok := cws.resources[resourceKey]
	if !ok {
		return false
	}
	return rws.running
}

// WaitForWatchReady blocks until the watch goroutine for the given resource
// has started and is about to call Watch(), or ctx is cancelled.
func (m *watchManager[ClientT]) WaitForWatchReady(ctx context.Context, connID, key string) error {
	m.mu.RLock()
	cws, ok := m.watches[connID]
	if !ok {
		m.mu.RUnlock()
		return fmt.Errorf("connection %q not found", connID)
	}
	rws, ok := cws.resources[key]
	if !ok {
		m.mu.RUnlock()
		return fmt.Errorf("resource %q not found for connection %q", key, connID)
	}
	ready := rws.ready
	m.mu.RUnlock()

	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WaitForWatchDone blocks until the watch goroutine for the given resource has exited,
// or ctx is cancelled.
func (m *watchManager[ClientT]) WaitForWatchDone(ctx context.Context, connID, key string) error {
	m.mu.RLock()
	cws, ok := m.watches[connID]
	if !ok {
		m.mu.RUnlock()
		return fmt.Errorf("connection %q not found", connID)
	}
	rws, ok := cws.resources[key]
	if !ok {
		m.mu.RUnlock()
		return fmt.Errorf("resource %q not found for connection %q", key, connID)
	}
	done := rws.done
	m.mu.RUnlock()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WaitForConnectionReady blocks until all watch goroutines for a connection
// have started, or ctx is cancelled.
func (m *watchManager[ClientT]) WaitForConnectionReady(ctx context.Context, connID string) error {
	m.mu.RLock()
	cws, ok := m.watches[connID]
	if !ok {
		m.mu.RUnlock()
		return fmt.Errorf("connection %q not found", connID)
	}
	var readyChans []<-chan struct{}
	for _, rws := range cws.resources {
		readyChans = append(readyChans, rws.ready)
	}
	m.mu.RUnlock()

	for _, ch := range readyChans {
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// AddListener installs a registration before readiness and orders its buffered
// states before live states. The returned identity owns that registration only.
func (m *watchManager[ClientT]) AddListener(ctx context.Context, sink WatchEventSink, onReady func() error) (*watchListener, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.listenersMu.Lock()
	m.pendingMu.Lock()
	if m.pendingStateOverflow {
		m.pendingMu.Unlock()
		m.listenersMu.Unlock()
		return nil, errWatchStateReplayOverflow
	}
	listener := &watchListener{sink: sink, ctx: ctx, replaying: true, pending: slices.Clone(m.pendingStateEvents)}
	m.pendingMu.Unlock()
	m.listeners = append(m.listeners[:len(m.listeners):len(m.listeners)], listener)
	m.listenersMu.Unlock()

	if onReady != nil {
		if err := onReady(); err != nil {
			m.removeListener(listener, nil)
			return nil, err
		}
	}
	if err := listener.replay(); err != nil {
		m.removeListener(listener, nil)
		return nil, err
	}
	m.listenersMu.Lock()
	if !listener.active() {
		m.listenersMu.Unlock()
		m.removeListener(listener, nil)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, context.Canceled
	}
	listener.accepted = true
	m.readyListeners++
	m.pendingMu.Lock()
	m.pendingStateEvents = nil
	m.pendingStateOverflow = false
	m.pendingMu.Unlock()
	m.listenersMu.Unlock()
	return listener, nil
}

// RemoveListener retires registrations for sink without joining callbacks
// already selected for delivery. A sink must own those callbacks' lifetime.
func (m *watchManager[ClientT]) RemoveListener(sink WatchEventSink) {
	m.removeListener(nil, sink)
}

func (m *watchManager[ClientT]) removeListener(target *watchListener, sink WatchEventSink) {
	m.listenersMu.Lock()
	defer m.listenersMu.Unlock()
	count := 0
	for _, listener := range m.listeners {
		if listener == target || target == nil && listener.sink == sink {
			count++
		}
	}
	if count == 0 {
		return
	}
	remaining := make([]*watchListener, 0, len(m.listeners)-count)
	for _, listener := range m.listeners {
		if listener == target || target == nil && listener.sink == sink {
			listener.retired.Store(true)
			if listener.accepted {
				m.readyListeners--
			}
			listener.mu.Lock()
			listener.pending = nil
			listener.mu.Unlock()
		} else {
			remaining = append(remaining, listener)
		}
	}
	m.listeners = remaining
}

func (m *watchManager[ClientT]) listenerSnapshot() []*watchListener {
	m.listenersMu.RLock()
	listeners := m.listeners
	m.listenersMu.RUnlock()
	return listeners
}

// fanOutSink is a WatchEventSink that broadcasts events to all registered
// listeners. It enriches every event with the connectionID before fan-out,
// since plugin Watch functions don't know (or need to know) the connection.
type fanOutSink[ClientT any] struct {
	mgr          *watchManager[ClientT]
	connectionID string
}

func (s *fanOutSink[ClientT]) OnAdd(p WatchAddPayload) {
	p.Connection = s.connectionID
	for _, listener := range s.mgr.listenerSnapshot() {
		if listener.active() {
			listener.sink.OnAdd(p)
		}
	}
}

func (s *fanOutSink[ClientT]) OnUpdate(p WatchUpdatePayload) {
	p.Connection = s.connectionID
	for _, listener := range s.mgr.listenerSnapshot() {
		if listener.active() {
			listener.sink.OnUpdate(p)
		}
	}
}

func (s *fanOutSink[ClientT]) OnDelete(p WatchDeletePayload) {
	p.Connection = s.connectionID
	for _, listener := range s.mgr.listenerSnapshot() {
		if listener.active() {
			listener.sink.OnDelete(p)
		}
	}
}

func (s *fanOutSink[ClientT]) OnStateChange(e WatchStateEvent) {
	e.Connection = s.connectionID

	s.mgr.log.Debugw(context.Background(), "watch state event",
		"connection_id", s.connectionID,
		"resource_key", e.ResourceKey,
		"state", e.State,
		"count", e.ResourceCount,
	)

	// Track state and count for GetWatchState snapshots.
	s.mgr.mu.Lock()
	if cws, ok := s.mgr.watches[s.connectionID]; ok {
		if rws, ok := cws.resources[e.ResourceKey]; ok {
			rws.state = e.State
			if e.ResourceCount > 0 {
				rws.count = e.ResourceCount
			}
		}
	}
	s.mgr.mu.Unlock()

	// Snapshot and buffer under the same registration barrier. Do not clear
	// retry history until some registration has successfully completed replay.
	s.mgr.listenersMu.RLock()
	listeners := s.mgr.listeners
	if s.mgr.readyListeners == 0 {
		s.mgr.pendingMu.Lock()
		if len(s.mgr.pendingStateEvents) < maxPendingWatchStates {
			s.mgr.pendingStateEvents = append(s.mgr.pendingStateEvents, e)
		} else {
			s.mgr.pendingStateOverflow = true
		}
		s.mgr.pendingMu.Unlock()
	}
	s.mgr.listenersMu.RUnlock()
	for _, listener := range listeners {
		listener.onStateChange(e)
	}
}

// startWatchLocked starts a Watch goroutine for a resource.
// Must be called with m.mu held.
func (m *watchManager[ClientT]) startWatchLocked(connectionID string, resourceKey string, cws *connectionWatchState[ClientT]) {
	watcher, ok := m.registry.GetWatcher(resourceKey)
	if !ok {
		return
	}
	meta, _ := m.registry.LookupMeta(resourceKey)

	resourceCtx, cancel := context.WithCancel(cws.connCtx)
	if cws.watchScope != nil {
		resourceCtx = WithWatchScope(resourceCtx, cws.watchScope)
	}
	rws := &resourceWatchState{
		running: true,
		state:   WatchStateIdle,
		cancel:  cancel,
		retries: 0,
		ready:   make(chan struct{}),
		done:    make(chan struct{}),
	}
	cws.resources[resourceKey] = rws

	sink := &fanOutSink[ClientT]{mgr: m, connectionID: connectionID}

	m.wg.Add(1)
	go m.runWatch(connectionID, resourceKey, watcher, cws.client, meta, resourceCtx, cancel, rws, sink)
}

// runWatch is the goroutine that manages the lifecycle of a single Watch call.
// It handles error recovery with exponential backoff.
func (m *watchManager[ClientT]) runWatch(
	connectionID string,
	resourceKey string,
	watcher Watcher[ClientT],
	client *ClientT,
	meta ResourceMeta,
	resourceCtx context.Context,
	cancel context.CancelFunc,
	rws *resourceWatchState,
	sink WatchEventSink,
) {
	defer m.wg.Done()
	defer cancel()
	defer close(rws.done)

	readyOnce := sync.Once{}

	for {
		err := m.safeWatch(watcher, resourceCtx, client, meta, sink, func() {
			// Signal readiness at call-entry time for Watch().
			readyOnce.Do(func() { close(rws.ready) })
		})

		// Check if context was cancelled (clean shutdown).
		if resourceCtx.Err() != nil {
			m.mu.Lock()
			rws.running = false
			rws.state = WatchStateStopped
			m.mu.Unlock()
			return
		}

		// Watch returned without error while context is still active.
		if err == nil {
			m.mu.Lock()
			rws.running = false
			rws.state = WatchStateStopped
			m.mu.Unlock()
			return
		}

		// Watch returned an error — attempt retry.
		m.mu.Lock()
		rws.retries++
		if rws.retries > m.maxRetries {
			rws.running = false
			rws.state = WatchStateFailed
			m.mu.Unlock()
			sink.OnStateChange(WatchStateEvent{
				ResourceKey: resourceKey,
				State:       WatchStateFailed,
				Error:       err,
				Message:     fmt.Sprintf("max retries (%d) exceeded: %v", m.maxRetries, err),
			})
			return
		}
		currentRetry := rws.retries
		rws.state = WatchStateError
		m.mu.Unlock()

		sink.OnStateChange(WatchStateEvent{
			ResourceKey: resourceKey,
			State:       WatchStateError,
			Error:       err,
			Message:     fmt.Sprintf("watch error (retry %d/%d): %v", currentRetry, m.maxRetries, err),
		})

		// Exponential backoff.
		backoff := m.baseBackoff * time.Duration(1<<(currentRetry-1))
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
		select {
		case <-resourceCtx.Done():
			m.mu.Lock()
			rws.running = false
			rws.state = WatchStateStopped
			m.mu.Unlock()
			return
		case <-m.clock.After(backoff):
		}
	}
}

// safeWatch calls Watch with panic recovery.
func (m *watchManager[ClientT]) safeWatch(
	watcher Watcher[ClientT],
	ctx context.Context,
	client *ClientT,
	meta ResourceMeta,
	sink WatchEventSink,
	onStart func(),
) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("watch panic: %v", r)
		}
	}()
	if onStart != nil {
		onStart()
	}
	return watcher.Watch(ctx, client, meta, sink)
}

// Wait blocks until all watch goroutines have exited.
func (m *watchManager[ClientT]) Wait() {
	m.wg.Wait()
}
