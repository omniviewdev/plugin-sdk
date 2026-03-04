package networker

import (
	"maps"

	"github.com/omniviewdev/plugin-sdk/pkg/utils/timeutil"
)

// SetClock replaces the Manager's Clock for testing.
func SetClock(m *Manager, c timeutil.Clock) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clock = c
}

// WaitDone blocks until all monitor goroutines have completed.
func WaitDone(m *Manager) { m.wg.Wait() }

// Sessions returns a defensive copy of the internal sessions map for test inspection.
func Sessions(m *Manager) map[string]*sessionEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return maps.Clone(m.sessions)
}

// EntryState returns the current state of a session entry.
func EntryState(e *sessionEntry) SessionState {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.session.State
}
