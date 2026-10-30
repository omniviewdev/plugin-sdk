package logs

import (
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/utils/timeutil"
)

func TestReadinessCounterBeyondInt32(t *testing.T) {
	const sourceCount = int64(1<<31) + 1
	ss := &sessionState{
		session:          LogSession{ID: "large-session", Status: LogSessionStatusInitializing},
		totalSources:     sourceCount,
		initialSourceIDs: map[string]struct{}{"last-source": {}},
	}
	ss.readySources.Store(sourceCount - 1)
	m := &Manager{clock: timeutil.RealClock{}}

	m.markSourceReady(ss, "last-source")

	if got := ss.readySources.Load(); got != sourceCount {
		t.Fatalf("ready sources = %d, want %d", got, sourceCount)
	}
	if got := ss.status(); got != LogSessionStatusActive {
		t.Fatalf("status = %v, want active", got)
	}
	m.markSourceReady(ss, "last-source")
	if got := ss.readySources.Load(); got != sourceCount {
		t.Fatalf("duplicate source changed readiness to %d", got)
	}
}
