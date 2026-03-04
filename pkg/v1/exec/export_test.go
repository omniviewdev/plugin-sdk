package exec

import "github.com/omniviewdev/plugin-sdk/pkg/utils/timeutil"

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

// WaitDone blocks until all session goroutines have completed.
func WaitDone(m *Manager) { m.wg.Wait() }

// Sessions returns a defensive copy of the internal sessions map for test inspection.
func Sessions(m *Manager) map[string]*sessionState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp := make(map[string]*sessionState, len(m.sessions))
	for k, v := range m.sessions {
		cp[k] = v
	}
	return cp
}

// SessionDone returns the done channel for a session state.
func SessionDone(ss *sessionState) <-chan struct{} { return ss.done }
