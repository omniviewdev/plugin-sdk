package resourcev2

import (
	"context"
	"encoding/hex"
	"maps"
	"reflect"
	"strconv"
	"time"
)

// All metadata state and subscription queues are protected by Controller.mu.
// Published snapshots are immutable; only public boundaries deep-copy values.
type connectionMetadataState[T any] struct {
	snapshot ConnectionMetadataSnapshot
	revision uint64
	slot     *clientSlot[T]
	running  *metadataAcquisition
	pending  bool
	removed  bool
}
type metadataAcquisition struct {
	done chan struct{}
	err  error
}
type metadataWatcher struct {
	events   chan ConnectionMetadataEvent
	overflow chan struct{}
}

func (c *Controller[T]) metadataStateLocked(id string) *connectionMetadataState[T] {
	m := c.metadata[id]
	if m == nil {
		m = &connectionMetadataState[T]{snapshot: ConnectionMetadataSnapshot{ConnectionID: id, Entries: map[string]ConnectionMetadataEntry{}}}
		c.metadata[id] = m
	}
	return m
}
func (c *Controller[T]) metadataPublishLocked(m *connectionMetadataState[T], next ConnectionMetadataSnapshot, removed bool) {
	if reflect.DeepEqual(m.snapshot, next) && m.removed == removed && m.revision != 0 {
		return
	}
	m.revision++
	next.Revision = strconv.FormatUint(m.revision, 10)
	m.snapshot = next
	m.removed = removed
	event := ConnectionMetadataEvent{Snapshot: next, Removed: removed}
	for w := range c.metadataWatchers {
		select {
		case w.events <- event:
		default:
			close(w.overflow)
			delete(c.metadataWatchers, w)
		}
	}
}
func (c *Controller[T]) metadataAdmitLocked(s *clientSlot[T]) {
	if c.cfg.ConnectionMetadata == nil || s.stopping || s.ctx.Err() != nil || c.closed || c.clients[s.connection.ID] != s {
		return
	}
	m := c.metadataStateLocked(s.connection.ID)
	if m.slot == s {
		return
	}
	m.slot = s
	next := ConnectionMetadataSnapshot{ConnectionID: s.connection.ID, Generation: hex.EncodeToString(s.catalogNonce[:]), Revision: m.snapshot.Revision, Active: true, Entries: map[string]ConnectionMetadataEntry{}}
	c.metadataPublishLocked(m, next, false)
	c.metadataAcquireLocked(m)
}
func (c *Controller[T]) metadataStopLocked(s *clientSlot[T]) {
	if c.cfg.ConnectionMetadata == nil {
		return
	}
	m := c.metadata[s.connection.ID]
	if m == nil || m.slot != s {
		return
	}
	m.slot = nil
	m.pending = false
	next := m.snapshot
	next.Active = false
	next.Refreshing = false
	next.Entries = maps.Clone(next.Entries)
	for key, entry := range next.Entries {
		if entry.State == "current" {
			entry.State = "stale"
			next.Entries[key] = entry
		}
	}
	c.metadataPublishLocked(m, next, false)
}
func (c *Controller[T]) metadataInventoryLocked(previous, next map[string]bool) {
	if c.cfg.ConnectionMetadata == nil {
		return
	}
	for id := range previous {
		if next[id] {
			continue
		}
		m := c.metadataStateLocked(id)
		m.slot = nil
		m.pending = false
		snapshot := ConnectionMetadataSnapshot{ConnectionID: id, Revision: m.snapshot.Revision, Entries: map[string]ConnectionMetadataEntry{}}
		c.metadataPublishLocked(m, snapshot, true)
	}
	for id := range next {
		m := c.metadataStateLocked(id)
		if !previous[id] || m.removed {
			c.metadataPublishLocked(m, ConnectionMetadataSnapshot{ConnectionID: id, Revision: m.snapshot.Revision, Entries: map[string]ConnectionMetadataEntry{}}, false)
		}
	}
}

func (c *Controller[T]) GetConnectionMetadata(ctx context.Context, id string) (ConnectionMetadataSnapshot, error) {
	if c.cfg.ConnectionMetadata == nil {
		return ConnectionMetadataSnapshot{}, NewError("UNSUPPORTED", "Connection metadata is unavailable.")
	}
	if _, err := c.connection(ctx, id); err != nil {
		return ConnectionMetadataSnapshot{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.connections[id]; !exists {
		return ConnectionMetadataSnapshot{}, NewError("NOT_FOUND", "Connection was removed.")
	}
	if c.closed {
		return ConnectionMetadataSnapshot{}, NewError("CANCELLED", "Provider is closed.")
	}
	m := c.metadataStateLocked(id)
	if m.revision == 0 {
		c.metadataPublishLocked(m, m.snapshot, false)
	}
	return CloneConnectionMetadataSnapshot(m.snapshot), nil
}

func (c *Controller[T]) RefreshConnectionMetadata(ctx context.Context, id string) (ConnectionMetadataSnapshot, error) {
	if _, err := c.GetConnectionMetadata(ctx, id); err != nil {
		return ConnectionMetadataSnapshot{}, err
	}
	c.mu.Lock()
	m := c.metadata[id]
	if m == nil || m.slot == nil || m.slot.stopping || c.closed || c.changing[id] {
		c.mu.Unlock()
		return ConnectionMetadataSnapshot{}, NewError("CONNECTION_ERROR", "Connection is not started.")
	}
	job := c.metadataAcquireLocked(m)
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return ConnectionMetadataSnapshot{}, ctx.Err()
	case <-job.done:
	}
	if job.err != nil {
		return ConnectionMetadataSnapshot{}, job.err
	}
	return c.GetConnectionMetadata(ctx, id)
}

// InvalidateConnectionMetadata coalesces discovery invalidations through the
// same acquisition coordinator. An in-flight observation gets one follow-up pass.
func (c *Controller[T]) InvalidateConnectionMetadata(ctx context.Context, id string) error {
	if _, err := c.GetConnectionMetadata(ctx, id); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.metadata[id]
	if m == nil || m.slot == nil || m.slot.stopping || c.closed {
		return NewError("CONNECTION_ERROR", "Connection is not started.")
	}
	if m.running != nil {
		m.pending = true
	} else {
		c.metadataAcquireLocked(m)
	}
	return nil
}

func (c *Controller[T]) metadataAcquireLocked(m *connectionMetadataState[T]) *metadataAcquisition {
	if m.running != nil {
		return m.running
	}
	job := &metadataAcquisition{done: make(chan struct{})}
	m.running = job
	next := m.snapshot
	next.Refreshing = true
	c.metadataPublishLocked(m, next, false)
	slot := m.slot
	slot.users.Add(1)
	go c.metadataObserve(m, slot, job)
	return job
}
func (c *Controller[T]) metadataObserve(m *connectionMetadataState[T], slot *clientSlot[T], job *metadataAcquisition) {
	defer slot.users.Done()
	ctx, cancel := context.WithTimeout(slot.ctx, 30*time.Second)
	defer cancel()
	var update ConnectionMetadataUpdate
	var err error
	func() {
		defer func() {
			if recover() != nil {
				err = NewError("INTERNAL", "Connection metadata observation failed.")
			}
		}()
		update, err = c.cfg.ConnectionMetadata(ctx, cloneConnection(slot.connection), slot.client)
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			update, err = cloneMetadataUpdate(update)
		}
	}()
	c.mu.Lock()
	defer c.mu.Unlock()
	if m.running != job {
		job.err = NewError("CANCELLED", "Metadata acquisition retired.")
		close(job.done)
		return
	}
	m.running = nil
	if m.slot != slot || slot.stopping || slot.ctx.Err() != nil || c.closed || c.clients[slot.connection.ID] != slot {
		job.err = NewError("CANCELLED", "Metadata acquisition retired.")
		if m.slot == slot && slot.ctx.Err() != nil {
			c.metadataStopLocked(slot)
		}
		close(job.done)
		// A new generation may have joined while its predecessor drained.
		if m.slot != nil && m.slot != slot && !m.slot.stopping && m.slot.ctx.Err() == nil && !c.closed {
			c.metadataAcquireLocked(m)
		}
		return
	}
	next := m.snapshot
	next.Refreshing = false
	next.Entries = maps.Clone(next.Entries)
	if err == nil {
		next.LastRefreshError = nil
		for key, observation := range update.Set {
			if observation.State == "success" {
				observed := observation.ObservedAt.UTC()
				next.Entries[key] = ConnectionMetadataEntry{State: "current", Value: observation.Value, ObservedAt: &observed}
			} else {
				old, exists := next.Entries[key]
				if exists && old.State != "unavailable" {
					old.State = "stale"
					old.Error = observation.Error
					next.Entries[key] = old
				} else {
					next.Entries[key] = ConnectionMetadataEntry{State: "unavailable", Error: observation.Error}
				}
			}
		}
		for _, key := range update.Remove {
			delete(next.Entries, key)
		}
		err = validateMetadataSnapshotAdmission(next)
	}
	if err != nil {
		next = m.snapshot
		next.Refreshing = false
		next.LastRefreshError = &ConnectionMetadataError{Code: "OBSERVATION_FAILED", Message: "Connection metadata could not be refreshed."}
		next.Entries = maps.Clone(next.Entries)
		for key, entry := range next.Entries {
			if entry.State != "unavailable" {
				entry.State = "stale"
				next.Entries[key] = entry
			}
		}
	}
	c.metadataPublishLocked(m, next, false)
	close(job.done)
	if m.pending {
		m.pending = false
		c.metadataAcquireLocked(m)
	}
}

// WatchConnectionMetadata subscribes before replaying current snapshots. A slow
// sink receives RESET_REQUIRED rather than silently losing an ordered tombstone.
func (c *Controller[T]) WatchConnectionMetadata(ctx context.Context, sink ConnectionMetadataSink) error {
	if c.cfg.ConnectionMetadata == nil {
		return NewError("UNSUPPORTED", "Connection metadata is unavailable.")
	}
	if sink == nil {
		return NewError("INVALID_REQUEST", "Metadata sink is required.")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	w := &metadataWatcher{events: make(chan ConnectionMetadataEvent, 64), overflow: make(chan struct{})}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return NewError("CANCELLED", "Provider is closed.")
	}
	initial := make([]ConnectionMetadataEvent, 0, len(c.connections))
	for id := range c.connections {
		m := c.metadataStateLocked(id)
		if m.revision == 0 {
			c.metadataPublishLocked(m, m.snapshot, false)
		}
		initial = append(initial, ConnectionMetadataEvent{Snapshot: m.snapshot})
	}
	for _, m := range c.metadata {
		if m.removed {
			initial = append(initial, ConnectionMetadataEvent{Snapshot: m.snapshot, Removed: true})
		}
	}
	c.metadataWatchers[w] = struct{}{}
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.metadataWatchers, w); c.mu.Unlock() }()
	for _, event := range initial {
		if err := sink(ConnectionMetadataEvent{Snapshot: CloneConnectionMetadataSnapshot(event.Snapshot), Removed: event.Removed}); err != nil {
			return err
		}
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.ctx.Done():
			return c.ctx.Err()
		case <-w.overflow:
			return NewError("RESET_REQUIRED", "Metadata subscription must resynchronize.")
		case event := <-w.events:
			select {
			case <-w.overflow:
				return NewError("RESET_REQUIRED", "Metadata subscription must resynchronize.")
			default:
			}
			if err := sink(ConnectionMetadataEvent{Snapshot: CloneConnectionMetadataSnapshot(event.Snapshot), Removed: event.Removed}); err != nil {
				return err
			}
		}
	}
}

var _ ConnectionMetadataProvider = (*Controller[struct{}])(nil)
