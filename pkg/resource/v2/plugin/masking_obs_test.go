package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/log/logtest"
)

func TestRPCErrorMaskingIsObservableWithoutProviderText(t *testing.T) {
	spans := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans))
	previousProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	sink := &logtest.RecordingSink{}
	previousLogger := logging.Default()
	logging.SetDefault(logging.New(logging.Config{
		Backend: logging.NewSinkBackend(sink),
		Level:   logging.NewLevelController(logging.LevelDebug),
	}))
	t.Cleanup(func() {
		logging.SetDefault(previousLogger)
		otel.SetTracerProvider(previousProvider)
		_ = provider.Shutdown(context.Background())
	})

	ctx, span := otel.Tracer("test").Start(context.Background(), "test.parent")
	err := rpcErrorContext(ctx, "get", errors.New("token=abc123 provider says no"))
	span.End()
	if err == nil || strings.Contains(err.Error(), "abc123") {
		t.Fatalf("masked error leaked text: %v", err)
	}

	var event bool
	for _, recorded := range spans.GetSpans() {
		for _, e := range recorded.Events {
			if e.Name == "sdk.error_masked" {
				event = true
			}
		}
		if strings.Contains(fmt.Sprint(recorded.Events, recorded.Attributes), "abc123") {
			t.Fatal("span exports provider text")
		}
	}
	if !event {
		t.Fatal("missing sdk.error_masked event")
	}
	var logged bool
	for _, record := range sink.Records() {
		if strings.Contains(fmt.Sprint(record.Fields), "abc123") {
			t.Fatal("log exports provider text")
		}
		if record.Message == "provider error masked" && record.Level == logging.LevelDebug {
			logged = true
		}
	}
	if !logged {
		t.Fatalf("missing provider error masked record: %v", sink.Records())
	}
}
