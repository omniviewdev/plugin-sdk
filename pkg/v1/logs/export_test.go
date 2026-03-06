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

// WaitDone blocks until all session goroutines have completed.
func WaitDone(m *Manager) {
	m.wg.Wait()
}

// ExtractTimestampForBench exposes extractTimestamp for benchmarking.
func ExtractTimestampForBench(line []byte) (time.Time, string) {
	return extractTimestamp(line)
}

