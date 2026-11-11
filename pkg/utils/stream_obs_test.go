package utils

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/log/logtest"
)

func installTestTracer(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	return exporter
}

func recordingLogger() (logging.Logger, *logtest.RecordingSink) {
	sink := &logtest.RecordingSink{}
	return logging.New(logging.Config{
		Name:    "test",
		Level:   logging.NewLevelController(logging.LevelTrace),
		Backend: logging.NewSinkBackend(sink),
	}), sink
}

func TestBeginStreamEmitsOpenSpanAndCloseLog(t *testing.T) {
	exporter := installTestTracer(t)
	log, sink := recordingLogger()

	ctx, stream := BeginStream(context.Background(), log, "exec", "exec_stream")
	if !trace.SpanFromContext(ctx).SpanContext().IsValid() {
		t.Fatal("open span is not in the returned context")
	}
	stream.OpenDone()
	stream.Close(ctx, status.Error(codes.Unavailable, "token=abc123 provider detail"))

	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Name != "sdk.exec.open" {
		t.Fatalf("unexpected spans: %+v", spans)
	}
	var sawClose bool
	for _, record := range sink.Records() {
		if strings.Contains(fmt.Sprint(record.Fields), "abc123") || strings.Contains(record.Message, "abc123") {
			t.Fatalf("log leaks error text: %+v", record)
		}
		if record.Message == "stream closed" {
			sawClose = true
			fields := record.ToMap()
			if fields["grpc_code"] != "Unavailable" || fields["operation"] != "exec_stream" {
				t.Fatalf("unexpected close fields: %v", fields)
			}
		}
	}
	if !sawClose {
		t.Fatal("missing stream closed log")
	}
	if strings.Contains(fmt.Sprint(spans[0].Attributes, spans[0].Events, spans[0].Status), "abc123") {
		t.Fatal("span leaks error text")
	}
}

func TestBeginStreamOpenFailedClassifiesWithoutText(t *testing.T) {
	exporter := installTestTracer(t)
	log, sink := recordingLogger()

	ctx, stream := BeginStream(context.Background(), log, "logs", "log_stream")
	stream.OpenFailed(ctx, errors.New("token=abc123"))
	stream.Close(ctx, nil)

	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Name != "sdk.logs.open" {
		t.Fatalf("unexpected spans: %+v", spans)
	}
	if spans[0].Status.Description != "other" {
		t.Fatalf("status = %q, want classification", spans[0].Status.Description)
	}
	for _, record := range sink.Records() {
		if strings.Contains(fmt.Sprint(record.Fields), "abc123") {
			t.Fatalf("log leaks error text: %+v", record)
		}
	}
}

func TestStreamingMethodFilterSkipsLongLivedStreams(t *testing.T) {
	filter := StreamingMethodFilter()
	for _, method := range []string{
		"/omniview.metric.MetricPlugin/StreamResourceMetrics",
		"/omniview.resource.v2.ResourceAncillary/WatchConnectionMetadata",
		"/omniview.settings.SettingsPlugin/CommitSettings",
		"/omniview.resource.v2.ResourceAncillary/WatchConnections",
		"/omniview.metric.MetricPlugin/StreamMetrics",
	} {
		if filter(&stats.RPCTagInfo{FullMethodName: method}) {
			t.Fatalf("%s should not get an otelgrpc span", method)
		}
	}
	if !filter(&stats.RPCTagInfo{FullMethodName: "/omniview.metric.MetricPlugin/Query"}) {
		t.Fatal("unary methods must stay traced")
	}
}
