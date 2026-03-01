package resourcetest

import (
	"sync"
	"testing"
	"time"

	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
)

// RecordingSink is a thread-safe WatchEventSink that records all events for assertions.
// Use NewRecordingSink() to create instances.
type RecordingSink struct {
	mu      sync.Mutex
	changed chan struct{} // closed-and-recreated on each event to wake waiters
	Adds    []resource.WatchAddPayload
	Updates []resource.WatchUpdatePayload
	Deletes []resource.WatchDeletePayload
	States  []resource.WatchStateEvent
}

// NewRecordingSink creates a RecordingSink with its broadcast channel initialized.
func NewRecordingSink() *RecordingSink {
	return &RecordingSink{changed: make(chan struct{})}
}

// broadcast wakes all waiters. Must be called with mu held.
func (s *RecordingSink) broadcast() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *RecordingSink) OnAdd(p resource.WatchAddPayload) {
	s.mu.Lock()
	s.Adds = append(s.Adds, p)
	s.broadcast()
	s.mu.Unlock()
}

func (s *RecordingSink) OnUpdate(p resource.WatchUpdatePayload) {
	s.mu.Lock()
	s.Updates = append(s.Updates, p)
	s.broadcast()
	s.mu.Unlock()
}

func (s *RecordingSink) OnDelete(p resource.WatchDeletePayload) {
	s.mu.Lock()
	s.Deletes = append(s.Deletes, p)
	s.broadcast()
	s.mu.Unlock()
}

func (s *RecordingSink) OnStateChange(e resource.WatchStateEvent) {
	s.mu.Lock()
	s.States = append(s.States, e)
	s.broadcast()
	s.mu.Unlock()
}

// AddCount returns the number of add events recorded.
func (s *RecordingSink) AddCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.Adds)
}

// UpdateCount returns the number of update events recorded.
func (s *RecordingSink) UpdateCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.Updates)
}

// DeleteCount returns the number of delete events recorded.
func (s *RecordingSink) DeleteCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.Deletes)
}

// StateCount returns the number of state events recorded.
func (s *RecordingSink) StateCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.States)
}

// Reset clears all recorded events and wakes any waiters.
func (s *RecordingSink) Reset() {
	s.mu.Lock()
	s.Adds = nil
	s.Updates = nil
	s.Deletes = nil
	s.States = nil
	s.broadcast()
	s.mu.Unlock()
}

// WaitForAdds blocks until at least count add events have been recorded, or the timeout expires.
func (s *RecordingSink) WaitForAdds(t *testing.T, count int, timeout time.Duration) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		s.mu.Lock()
		n := len(s.Adds)
		ch := s.changed
		s.mu.Unlock()
		if n >= count {
			return
		}
		select {
		case <-ch:
		case <-timer.C:
			t.Fatalf("timed out waiting for %d adds, got %d", count, s.AddCount())
		}
	}
}

// WaitForUpdates blocks until at least count update events have been recorded, or the timeout expires.
func (s *RecordingSink) WaitForUpdates(t *testing.T, count int, timeout time.Duration) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		s.mu.Lock()
		n := len(s.Updates)
		ch := s.changed
		s.mu.Unlock()
		if n >= count {
			return
		}
		select {
		case <-ch:
		case <-timer.C:
			t.Fatalf("timed out waiting for %d updates, got %d", count, s.UpdateCount())
		}
	}
}

// WaitForDeletes blocks until at least count delete events have been recorded, or the timeout expires.
func (s *RecordingSink) WaitForDeletes(t *testing.T, count int, timeout time.Duration) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		s.mu.Lock()
		n := len(s.Deletes)
		ch := s.changed
		s.mu.Unlock()
		if n >= count {
			return
		}
		select {
		case <-ch:
		case <-timer.C:
			t.Fatalf("timed out waiting for %d deletes, got %d", count, s.DeleteCount())
		}
	}
}

// WaitForState blocks until a state event matching the given resourceKey and state is recorded.
func (s *RecordingSink) WaitForState(t *testing.T, resourceKey string, state resource.WatchState, timeout time.Duration) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		s.mu.Lock()
		found := false
		for _, e := range s.States {
			if e.ResourceKey == resourceKey && e.State == state {
				found = true
				break
			}
		}
		ch := s.changed
		s.mu.Unlock()
		if found {
			return
		}
		select {
		case <-ch:
		case <-timer.C:
			t.Fatalf("timed out waiting for state %v on %s", state, resourceKey)
		}
	}
}
