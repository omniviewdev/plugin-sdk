package logs

import (
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/utils/timeutil"
)

// ManagerSink returns the Manager's OutputSink for test inspection.
func ManagerSink(m *Manager) OutputSink {
	return m.sink
}

// SetClock replaces the Manager's Clock for testing.
func SetClock(m *Manager, c timeutil.Clock) {
	m.clock = c
}

// ManagerClosed reports whether the Manager has stopped taking sessions: Close or Wait has
// begun. It is read under the lock they set it under.
func ManagerClosed(m *Manager) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.closed
}

// ExtractTimestampForBench exposes extractTimestamp for benchmarking.
func ExtractTimestampForBench(line []byte) (time.Time, string) {
	return extractTimestamp(line)
}
