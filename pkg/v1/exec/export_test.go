package exec

import (
	"maps"

	"github.com/omniviewdev/plugin-sdk/pkg/utils/timeutil"
)

// ManagerSink returns the Manager's OutputSink for test inspection.
func ManagerSink(m *Manager) OutputSink {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sink
}

// SetSink replaces the Manager's OutputSink for testing.
func SetSink(m *Manager, s OutputSink) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sink = s
}

// SetClock replaces the Manager's Clock for testing.
func SetClock(m *Manager, c timeutil.Clock) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clock = c
}

// Sessions returns a defensive copy of the internal sessions map for test inspection.
func Sessions(m *Manager) map[string]*sessionState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return maps.Clone(m.sessions)
}

// ManagerClosed reports whether the Manager has begun to close: it admits no more sessions. It
// is read under the lock Close sets it under, so a test that sees true also sees every
// session Close cancelled with it.
func ManagerClosed(m *Manager) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.closed
}

// SessionDone returns the done channel for a session state.
func SessionDone(ss *sessionState) <-chan struct{} { return ss.done }
