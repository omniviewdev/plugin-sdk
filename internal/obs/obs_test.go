package obs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestEndDoesNotExportErrorText(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	_, span := provider.Tracer("test").Start(context.Background(), "test")
	End(span, errors.New("token=abc123"), "PROVIDER_ERROR")
	ended := recorder.Ended()[0]
	if ended.Status().Description != "PROVIDER_ERROR" {
		t.Fatalf("unexpected status: %v", ended.Status())
	}
	if strings.Contains(fmt.Sprint(ended.Attributes(), ended.Events(), ended.Status()), "abc123") {
		t.Fatal("span contains secret")
	}
}
