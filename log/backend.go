// Package log provides structured runtime logging with context enrichment,
// configurable severity thresholds, and pluggable output backends.
package log

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
)

// Backend emits log records to a concrete logging implementation.
type Backend interface {
	Write(ctx context.Context, record Record) error
	Sync(ctx context.Context) error
}

// HCLBackend emits records to an hclog logger.
type HCLBackend struct {
	mu     sync.RWMutex
	logger hclog.Logger
}

// NewHCLBackend wraps logger, using a null logger when logger is nil.
func NewHCLBackend(logger hclog.Logger) *HCLBackend {
	if logger == nil {
		logger = hclog.NewNullLogger()
	}
	return &HCLBackend{logger: logger}
}

// NewDefaultHCLBackend creates an hclog backend with its default output options.
func NewDefaultHCLBackend(name string, level Level) *HCLBackend {
	return NewHCLBackend(hclog.New(&hclog.LoggerOptions{
		Name:  name,
		Level: toHCLLevel(level),
	}))
}

// Named returns a backend with a child logger, or this backend for an empty name.
func (b *HCLBackend) Named(name string) *HCLBackend {
	if name == "" {
		return b
	}
	b.mu.RLock()
	next := b.logger.Named(name)
	b.mu.RUnlock()
	return &HCLBackend{logger: next}
}

// SetLevel updates the underlying hclog severity threshold.
func (b *HCLBackend) SetLevel(level Level) {
	b.mu.Lock()
	b.logger.SetLevel(toHCLLevel(level))
	b.mu.Unlock()
}

// Write emits a record through hclog; fatal records log errors without exiting.
func (b *HCLBackend) Write(_ context.Context, record Record) error {
	b.mu.RLock()
	logger := b.logger
	b.mu.RUnlock()

	args := make([]any, 0, len(record.Fields)*2+2)
	args = append(args, "timestamp", record.Timestamp.Format(time.RFC3339Nano))
	for _, field := range record.Fields {
		args = append(args, field.Key, field.Value)
	}

	switch record.Level {
	case LevelTrace:
		logger.Trace(record.Message, args...)
	case LevelDebug:
		logger.Debug(record.Message, args...)
	case LevelInfo:
		logger.Info(record.Message, args...)
	case LevelWarn:
		logger.Warn(record.Message, args...)
	case LevelError:
		logger.Error(record.Message, args...)
	case LevelFatal:
		// Intentionally mapped to Error to avoid os.Exit semantics in backend logging.
		logger.Error(record.Message, args...)
	default:
		return fmt.Errorf("unsupported log level: %v", record.Level)
	}

	return nil
}

// Sync returns nil because hclog exposes no flush operation.
func (b *HCLBackend) Sync(_ context.Context) error { return nil }

func toHCLLevel(level Level) hclog.Level {
	switch level {
	case LevelTrace:
		return hclog.Trace
	case LevelDebug:
		return hclog.Debug
	case LevelInfo:
		return hclog.Info
	case LevelWarn:
		return hclog.Warn
	case LevelError, LevelFatal:
		return hclog.Error
	default:
		return hclog.Info
	}
}

// SinkBackend adapts a Sink to Backend.
type SinkBackend struct {
	sink Sink
}

// NewSinkBackend wraps sink; a nil sink discards records.
func NewSinkBackend(sink Sink) *SinkBackend {
	return &SinkBackend{sink: sink}
}

// Write forwards the record to the sink and returns its error.
func (b *SinkBackend) Write(ctx context.Context, record Record) error {
	if b.sink == nil {
		return nil
	}
	return b.sink.Write(ctx, record)
}

// Sync returns nil because Sink exposes no flush operation.
func (b *SinkBackend) Sync(_ context.Context) error { return nil }
