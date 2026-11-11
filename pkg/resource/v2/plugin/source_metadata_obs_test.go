package plugin

import (
	"context"
	"testing"

	"google.golang.org/grpc"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/log/logtest"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
)

func sourceMetadataObsSink(t *testing.T) *logtest.RecordingSink {
	t.Helper()
	sink := &logtest.RecordingSink{}
	previous := logging.Default()
	logging.SetDefault(logging.New(logging.Config{
		Backend: logging.NewSinkBackend(sink),
		Level:   logging.NewLevelController(logging.LevelDebug),
	}))
	t.Cleanup(func() { logging.SetDefault(previous) })
	return sink
}

func sourceMetadataObsRecord(sink *logtest.RecordingSink, message string) (logging.Record, bool) {
	for _, record := range sink.Records() {
		if record.Message == message {
			return record, true
		}
	}
	return logging.Record{}, false
}

func TestMetadataWatchRejectionIsLoggedOnServer(t *testing.T) {
	sink := sourceMetadataObsSink(t)
	s := grpc.NewServer()
	RegisterServer(s, &qualifiedProvider{})
	c := NewClient(pipe(t, s))
	err := c.WatchConnectionMetadata(context.Background(), func(resourcev2.ConnectionMetadataEvent) error { return nil })
	if resourcev2.ErrorCode(err) != "UNSUPPORTED" {
		t.Fatalf("expected UNSUPPORTED, got %v", err)
	}
	record, ok := sourceMetadataObsRecord(sink, "request rejected")
	if !ok {
		t.Fatalf("missing request rejected record: %v", sink.Records())
	}
	var operation string
	for _, field := range record.Fields {
		if field.Key == "operation" {
			operation, _ = field.Value.(string)
		}
	}
	if operation != "watch_connection_metadata" {
		t.Fatalf("operation = %q", operation)
	}
}

func TestMetadataWatchLogsOpenAndClose(t *testing.T) {
	sink := sourceMetadataObsSink(t)
	p := &metadataTransportProvider{snapshot: metadataTransportSnapshot()}
	s := grpc.NewServer()
	RegisterServer(s, p)
	c := NewClient(pipe(t, s))
	if err := c.WatchConnectionMetadata(context.Background(), func(resourcev2.ConnectionMetadataEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"metadata watch opened", "metadata watch closed"} {
		if _, ok := sourceMetadataObsRecord(sink, message); !ok {
			t.Fatalf("missing %q record: %v", message, sink.Records())
		}
	}
}

func TestSourceTimeoutsLogClosedFieldsOnly(t *testing.T) {
	sink := sourceMetadataObsSink(t)
	if err := sourceBeginTimedOut(context.Background(), "session-1"); resourcev2.ErrorCode(err) != "TIMEOUT" {
		t.Fatalf("begin timeout code = %q", resourcev2.ErrorCode(err))
	}
	record, ok := sourceMetadataObsRecord(sink, "source begin timed out")
	if !ok || record.Level != logging.LevelWarn {
		t.Fatalf("missing begin timeout warning: %v", sink.Records())
	}
	fields := map[string]any{}
	for _, field := range record.Fields {
		fields[field.Key] = field.Value
	}
	if fields["timeout_ms"] == nil || fields["error_code"] != "TIMEOUT" {
		t.Fatalf("unexpected fields: %v", fields)
	}
}
