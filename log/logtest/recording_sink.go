package logtest

import (
	"context"
	"sync"

	logging "github.com/omniviewdev/plugin-sdk/log"
)

// RecordingSink stores emitted records for assertions.
type RecordingSink struct {
	mu      sync.Mutex
	records []logging.Record
}

func (s *RecordingSink) Write(_ context.Context, record logging.Record) error {
	s.mu.Lock()
	s.records = append(s.records, record)
	s.mu.Unlock()
	return nil
}

func (s *RecordingSink) Records() []logging.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]logging.Record, len(s.records))
	copy(out, s.records)
	return out
}

func (s *RecordingSink) Reset() {
	s.mu.Lock()
	s.records = nil
	s.mu.Unlock()
}
