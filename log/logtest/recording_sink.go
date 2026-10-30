// Package logtest provides an in-memory log sink for assertions.
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

// Write stores a record with a copied field slice; field values remain shared.
func (s *RecordingSink) Write(_ context.Context, record logging.Record) error {
	// Copy the field slice to isolate subsequent slice edits by the caller.
	record.Fields = slices.Clone(record.Fields)
	s.mu.Lock()
	s.records = append(s.records, record)
	s.mu.Unlock()
	return nil
}

// Records returns snapshots with independent field slices and shared field values.
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

// Reset clears all stored records.
func (s *RecordingSink) Reset() {
	s.mu.Lock()
	s.records = nil
	s.mu.Unlock()
}
