package log

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	grpcmetadata "google.golang.org/grpc/metadata"
)

func TestExtractTraceIDs_PrefersOTelSpanContext(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	defer tp.Shutdown(context.Background())
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	ctx, span := tp.Tracer("test").Start(context.Background(), "test-op")
	defer span.End()

	traceID, spanID := extractTraceIDs(ctx)
	assert.Equal(t, span.SpanContext().TraceID().String(), traceID)
	assert.Equal(t, span.SpanContext().SpanID().String(), spanID)
}

func TestExtractTraceIDs_FallsBackToMetadata(t *testing.T) {
	md := grpcmetadata.Pairs("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	ctx := grpcmetadata.NewIncomingContext(context.Background(), md)

	traceID, spanID := extractTraceIDs(ctx)
	assert.Equal(t, "0af7651916cd43dd8448eb211c80319c", traceID)
	assert.Equal(t, "b7ad6b7169203331", spanID)
}

func TestExtractTraceIDs_OTelSpanOverridesMetadata(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	defer tp.Shutdown(context.Background())

	ctx, span := tp.Tracer("test").Start(context.Background(), "test-op")
	defer span.End()

	md := grpcmetadata.Pairs("traceparent", "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01")
	ctx = grpcmetadata.NewIncomingContext(ctx, md)

	traceID, spanID := extractTraceIDs(ctx)
	assert.Equal(t, span.SpanContext().TraceID().String(), traceID)
	assert.Equal(t, span.SpanContext().SpanID().String(), spanID)
}

func TestExtractTraceIDs_NoTraceContext(t *testing.T) {
	traceID, spanID := extractTraceIDs(context.Background())
	assert.Empty(t, traceID)
	assert.Empty(t, spanID)
}

func TestExtractTraceIDs_NoopSpanIgnored(t *testing.T) {
	ctx := trace.ContextWithSpanContext(context.Background(), trace.SpanContext{})
	md := grpcmetadata.Pairs("x-trace-id", "abc123", "x-span-id", "def456")
	ctx = grpcmetadata.NewIncomingContext(ctx, md)

	traceID, spanID := extractTraceIDs(ctx)
	assert.Equal(t, "abc123", traceID)
	assert.Equal(t, "def456", spanID)
}
