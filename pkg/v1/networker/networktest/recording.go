package networktest

import (
	"slices"
	"sync"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/networker"
)

// SessionStateChange records a state transition.
type SessionStateChange struct {
	SessionID string
	State     networker.SessionState
	Timestamp time.Time
}

// RecordingObserver tracks session state changes for assertions.
type RecordingObserver struct {
	mu      sync.Mutex
	changes []SessionStateChange
	notify  chan struct{}
}

// NewRecordingObserver creates a new RecordingObserver.
func NewRecordingObserver() *RecordingObserver {
	return &RecordingObserver{
		notify: make(chan struct{}),
	}
}

// Record adds a state change to the log.
func (r *RecordingObserver) Record(sessionID string, state networker.SessionState) {
	r.mu.Lock()
	r.changes = append(r.changes, SessionStateChange{
		SessionID: sessionID,
		State:     state,
		Timestamp: time.Now(),
	})
	close(r.notify)
	r.notify = make(chan struct{})
	r.mu.Unlock()
}

// Changes returns a copy of all recorded changes.
func (r *RecordingObserver) Changes() []SessionStateChange {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.changes)
}

// WaitForState blocks until a change with the given state for the given session is recorded.
func (r *RecordingObserver) WaitForState(sessionID string, state networker.SessionState, timeout time.Duration) *SessionStateChange {
	deadline := time.After(timeout)
	for {
		r.mu.Lock()
		for i := range r.changes {
			if r.changes[i].SessionID == sessionID && r.changes[i].State == state {
				c := r.changes[i]
				r.mu.Unlock()
				return &c
			}
		}
		ch := r.notify
		r.mu.Unlock()

		select {
		case <-ch:
			continue
		case <-deadline:
			return nil
		}
	}
}
