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

// RetainedOutputLimit and RetainedOutputTTL are the bounds of the output a manager keeps for the first
// attach to a session that ended before anyone attached.
const (
	RetainedOutputLimit = retainedOutputLimit
	RetainedOutputTTL   = retainedOutputTTL
)

// RetainedSessions returns the number of ended sessions whose output waits for a first attach.
func RetainedSessions(m *Manager) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.retained)
}

// BufferTotal returns the number of bytes the manager has read from a live session's terminal,
// which is where a test that writes to a terminal by hand can see that the manager has taken it.
func BufferTotal(m *Manager, id string) (uint64, bool) {
	m.mu.RLock()
	ss, exists := m.sessions[id]
	m.mu.RUnlock()
	if !exists {
		return 0, false
	}
	return ss.buffer.Total(), true
}

// MarkEnd writes mark to a terminal the way the manager does when the process behind a session is over, and
// reports whether the terminal can take a mark at all. It returns when the write does.
func MarkEnd(t Terminal, mark []byte) (supported bool, err error) {
	terminal, ok := t.(EndMarker)
	if !ok {
		return false, nil
	}
	return true, terminal.MarkEnd(mark)
}

// ScanMark runs the output of a terminal, in the chunks it came in, through the scanner of the end mark as the
// reader of a session does, and returns what the reader delivers and whether it reached the mark.
func ScanMark(mark []byte, chunks ...[]byte) (delivered []byte, found bool) {
	var scanner markScanner
	for _, chunk := range chunks {
		out, reached := scanner.feed(mark, chunk)
		delivered = append(delivered, out...)
		if reached {
			return delivered, true
		}
	}
	return delivered, false
}
