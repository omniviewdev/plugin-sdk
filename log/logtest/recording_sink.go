package logtest

import (
	"context"
	"slices"
	"sync"

	logging "github.com/omniviewdev/plugin-sdk/log"
)

// RecordingSink stores emitted records for assertions.
type RecordingSink struct {
	mu      sync.Mutex
	records []logging.Record
}

func (s *RecordingSink) Write(_ context.Context, record logging.Record) error {
	// Deep-copy Fields to prevent the caller from mutating our snapshot.
	record.Fields = slices.Clone(record.Fields)
	s.mu.Lock()
	s.records = append(s.records, record)
	s.mu.Unlock()
	return nil
}

func (s *RecordingSink) Records() []logging.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]logging.Record, len(s.records))
	for i, r := range s.records {
		r.Fields = slices.Clone(r.Fields)
		out[i] = r
	}
	return out
}

func (s *RecordingSink) Reset() {
	s.mu.Lock()
	s.records = nil
	s.mu.Unlock()
}
