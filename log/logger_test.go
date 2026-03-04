package log_test

import (
	"context"
	"testing"

	"google.golang.org/grpc/metadata"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/log/logtest"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

func TestLogger_EnrichesPluginContextAndTrace(t *testing.T) {
	sink := &logtest.RecordingSink{}
	level := logging.NewLevelController(logging.LevelTrace)
	log := logging.New(logging.Config{
		Name:    "test",
		Level:   level,
		Backend: logging.NewSinkBackend(sink),
	})

	ctx := context.Background()
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(
		"traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	))
	ctx = types.WithPluginContext(ctx, &types.PluginContext{
		RequestID:   "req-1",
		RequesterID: "ide",
		Connection:  &types.Connection{ID: "conn-1"},
		ResourceContext: &types.ResourceContext{
			Key: "core::v1::Pod",
		},
	})

	log.Info(ctx, "hello", logging.String("custom", "value"))

	records := sink.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}

	m := records[0].ToMap()
	if got := m["request_id"]; got != "req-1" {
		t.Fatalf("request_id = %v", got)
	}
	if got := m["connection_id"]; got != "conn-1" {
		t.Fatalf("connection_id = %v", got)
	}
	if got := m["resource_key"]; got != "core::v1::Pod" {
		t.Fatalf("resource_key = %v", got)
	}
	if got := m["trace_id"]; got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("trace_id = %v", got)
	}
	if got := m["span_id"]; got != "00f067aa0ba902b7" {
		t.Fatalf("span_id = %v", got)
	}
}

func TestLogger_RespectsLevelController(t *testing.T) {
	sink := &logtest.RecordingSink{}
	level := logging.NewLevelController(logging.LevelInfo)
	log := logging.New(logging.Config{
		Name:    "test",
		Level:   level,
		Backend: logging.NewSinkBackend(sink),
	})

	ctx := context.Background()
	log.Debug(ctx, "debug skipped")
	log.Info(ctx, "info emitted")

	records := sink.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 record at info level, got %d", len(records))
	}

	level.Set(logging.LevelDebug)
	log.Debug(ctx, "debug emitted")
	if got := len(sink.Records()); got != 2 {
		t.Fatalf("expected 2 records after lowering level, got %d", got)
	}
}
