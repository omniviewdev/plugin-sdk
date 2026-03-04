package exec

import "github.com/omniviewdev/plugin-sdk/pkg/utils/timeutil"

// ManagerSink returns the Manager's OutputSink for test inspection.
func ManagerSink(m *Manager) OutputSink { return m.sink }

// SetSink replaces the Manager's OutputSink for testing.
func SetSink(m *Manager, s OutputSink) { m.sink = s }

// SetClock replaces the Manager's Clock for testing.
func SetClock(m *Manager, c timeutil.Clock) { m.clock = c }

// WaitDone blocks until all session goroutines have completed.
func WaitDone(m *Manager) { m.wg.Wait() }

// Sessions returns the internal sessions map for test inspection.
func Sessions(m *Manager) map[string]*sessionState { return m.sessions }

// SessionDone returns the done channel for a session state.
func SessionDone(ss *sessionState) <-chan struct{} { return ss.done }
