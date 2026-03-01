package resource

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const (
	defaultMaxRetries  = 5
	defaultBaseBackoff = 500 * time.Millisecond
	maxBackoff         = 30 * time.Second
)

// Clock abstracts time operations for testability.
type Clock interface {
	After(d time.Duration) <-chan time.Time
}

// realClock uses real time.
type realClock struct{}

func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// resourceWatchState tracks the lifecycle of a single Watch goroutine.
type resourceWatchState struct {
	running bool
	state   WatchState
	cancel  context.CancelFunc
	retries int
	ready   chan struct{} // closed when goroutine is about to call Watch()
	done    chan struct{} // closed when goroutine exits
}

// connectionWatchState holds all watch state for a single connection.
type connectionWatchState[ClientT any] struct {
	client    *ClientT
	connCtx   context.Context
	resources map[string]*resourceWatchState // by resource key
}

// watchManager manages Watch goroutine lifecycle for all connections and resources.
// It provides WatchEventSink fan-out to multiple listeners.
type watchManager[ClientT any] struct {
	mu       sync.RWMutex
	registry *resourcerRegistry[ClientT]
	watches  map[string]*connectionWatchState[ClientT] // by connection ID

	// Listeners for event fan-out.
	listenersMu sync.RWMutex
	listeners   []WatchEventSink

	maxRetries  int
	baseBackoff time.Duration
	clock       Clock

	// wg tracks all goroutines for clean shutdown.
	wg sync.WaitGroup
}

func newWatchManager[ClientT any](registry *resourcerRegistry[ClientT]) *watchManager[ClientT] {
	return &watchManager[ClientT]{
		registry:    registry,
		watches:     make(map[string]*connectionWatchState[ClientT]),
		maxRetries:  defaultMaxRetries,
		baseBackoff: defaultBaseBackoff,
		clock:       realClock{},
	}
}

// StartConnectionWatch starts watches for all SyncOnConnect-capable resources.
func (m *watchManager[ClientT]) StartConnectionWatch(ctx context.Context, connectionID string, client *ClientT, connCtx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.watches[connectionID]; ok {
		return nil // already started
	}

	cws := &connectionWatchState[ClientT]{
		client:    client,
		connCtx:   connCtx,
		resources: make(map[string]*resourceWatchState),
	}
	m.watches[connectionID] = cws

	// Start watches for SyncOnConnect resources.
	for _, meta := range m.registry.ListWatchable() {
		policy := m.registry.GetSyncPolicy(meta.Key())
		if policy == SyncOnConnect {
			m.startWatchLocked(connectionID, meta.Key(), cws)
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
		return nil, fmt.Errorf("connection %q not found", connectionID)
	}
	summary := &WatchConnectionSummary{
		ConnectionID: connectionID,
		Resources:    make(map[string]WatchState, len(cws.resources)),
	}
	for key, rws := range cws.resources {
		summary.Resources[key] = rws.state
	}
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

// AddListener registers a WatchEventSink for event fan-out.
func (m *watchManager[ClientT]) AddListener(sink WatchEventSink) {
	m.listenersMu.Lock()
	defer m.listenersMu.Unlock()
	m.listeners = append(m.listeners, sink)
}

// RemoveListener unregisters a WatchEventSink.
func (m *watchManager[ClientT]) RemoveListener(sink WatchEventSink) {
	m.listenersMu.Lock()
	defer m.listenersMu.Unlock()
	for i, s := range m.listeners {
		if s == sink {
			m.listeners = append(m.listeners[:i], m.listeners[i+1:]...)
			return
		}
	}
}

// fanOutSink is a WatchEventSink that broadcasts events to all registered listeners.
type fanOutSink[ClientT any] struct {
	mgr *watchManager[ClientT]
}

func (s *fanOutSink[ClientT]) OnAdd(p WatchAddPayload) {
	s.mgr.listenersMu.RLock()
	defer s.mgr.listenersMu.RUnlock()
	for _, l := range s.mgr.listeners {
		l.OnAdd(p)
	}
}

func (s *fanOutSink[ClientT]) OnUpdate(p WatchUpdatePayload) {
	s.mgr.listenersMu.RLock()
	defer s.mgr.listenersMu.RUnlock()
	for _, l := range s.mgr.listeners {
		l.OnUpdate(p)
	}
}

func (s *fanOutSink[ClientT]) OnDelete(p WatchDeletePayload) {
	s.mgr.listenersMu.RLock()
	defer s.mgr.listenersMu.RUnlock()
	for _, l := range s.mgr.listeners {
		l.OnDelete(p)
	}
}

func (s *fanOutSink[ClientT]) OnStateChange(e WatchStateEvent) {
	s.mgr.listenersMu.RLock()
	defer s.mgr.listenersMu.RUnlock()
	for _, l := range s.mgr.listeners {
		l.OnStateChange(e)
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
	rws := &resourceWatchState{
		running: true,
		state:   WatchStateIdle,
		cancel:  cancel,
		retries: 0,
		ready:   make(chan struct{}),
		done:    make(chan struct{}),
	}
	cws.resources[resourceKey] = rws

	sink := &fanOutSink[ClientT]{mgr: m}

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
		// Signal that the goroutine is ready and about to call Watch.
		readyOnce.Do(func() { close(rws.ready) })

		err := m.safeWatch(watcher, resourceCtx, client, meta, sink)

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
) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("watch panic: %v", r)
		}
	}()
	return watcher.Watch(ctx, client, meta, sink)
}

// Wait blocks until all watch goroutines have exited.
func (m *watchManager[ClientT]) Wait() {
	m.wg.Wait()
}
