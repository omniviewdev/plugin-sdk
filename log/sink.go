package log

import "context"

// Sink receives normalized log records.
type Sink interface {
	Write(ctx context.Context, record Record) error
}

// MultiSink fans out writes to multiple sinks.
type MultiSink struct {
	sinks []Sink
}

func NewMultiSink(sinks ...Sink) *MultiSink {
	filtered := make([]Sink, 0, len(sinks))
	for _, s := range sinks {
		if s != nil {
			filtered = append(filtered, s)
		}
	}
	return &MultiSink{sinks: filtered}
}

func (m *MultiSink) Write(ctx context.Context, record Record) error {
	for _, sink := range m.sinks {
		if err := sink.Write(ctx, record); err != nil {
			return err
		}
	}
	return nil
}
