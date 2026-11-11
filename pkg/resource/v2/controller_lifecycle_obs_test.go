package resourcev2

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/log/logtest"
	sdkdiag "github.com/omniviewdev/plugin-sdk/pkg/diagnostics"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

const lifecycleObsSecret = "token=abc123-provider-secret"

type lifecycleObsHarness struct {
	spans *tracetest.InMemoryExporter
	sink  *logtest.RecordingSink
}

func newLifecycleObsHarness(t *testing.T) *lifecycleObsHarness {
	t.Helper()
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
	return &lifecycleObsHarness{spans: spans, sink: sink}
}

func (h *lifecycleObsHarness) span(t *testing.T, name string) tracetest.SpanStub {
	t.Helper()
	for _, span := range h.spans.GetSpans() {
		if span.Name == name {
			return span
		}
	}
	t.Fatalf("span %q not exported; have %v", name, h.spanNames())
	return tracetest.SpanStub{}
}

func (h *lifecycleObsHarness) spanNames() []string {
	var names []string
	for _, span := range h.spans.GetSpans() {
		names = append(names, span.Name)
	}
	return names
}

func lifecycleObsAttr(span tracetest.SpanStub, key string) string {
	for _, attr := range span.Attributes {
		if string(attr.Key) == key {
			return attr.Value.Emit()
		}
	}
	return ""
}

func lifecycleObsHasEvent(span tracetest.SpanStub, name string) bool {
	for _, event := range span.Events {
		if event.Name == name {
			return true
		}
	}
	return false
}

func (h *lifecycleObsHarness) logged(message string) (map[string]any, bool) {
	for _, record := range h.sink.Records() {
		if record.Message != message {
			continue
		}
		fields := map[string]any{}
		for _, field := range record.Fields {
			fields[field.Key] = field.Value
		}
		return fields, true
	}
	return nil, false
}

func (h *lifecycleObsHarness) assertNoSecret(t *testing.T) {
	t.Helper()
	var exported strings.Builder
	for _, span := range h.spans.GetSpans() {
		fmt.Fprint(&exported, span.Name, span.Attributes, span.Events, span.Status)
	}
	for _, record := range h.sink.Records() {
		fmt.Fprint(&exported, record.Message, record.Fields)
	}
	if strings.Contains(exported.String(), "abc123") {
		t.Fatal("provider error text leaked into spans or logs")
	}
}

func lifecycleTransitionTotal(phase, outcome string) float64 {
	metrics, _ := sdkdiag.Default().Snapshot()
	total := 0.0
	for _, metric := range metrics {
		if metric.GetName() != "omniview.plugin.self.sdk.connection.transitions" {
			continue
		}
		for _, point := range metric.GetPoints() {
			matchedPhase, matchedOutcome := false, false
			for _, attr := range point.GetAttrs() {
				matchedPhase = matchedPhase || attr.GetValue() == phase
				matchedOutcome = matchedOutcome || attr.GetValue() == outcome
			}
			if matchedPhase && matchedOutcome {
				total += point.GetValue()
			}
		}
	}
	return total
}

func TestConnectionLifecycleSpansAndTransitions(t *testing.T) {
	h := newLifecycleObsHarness(t)
	cfg := controllerConfig()
	cfg.Lifecycle = controllerLifecycle(&atomic.Int32{})
	c := newTestController(t, cfg)
	if _, err := c.LoadConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	startBefore := lifecycleTransitionTotal("start", "success")
	stopBefore := lifecycleTransitionTotal("stop", "success")
	if _, err := c.StartConnection(context.Background(), "explicit"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.StopConnection(context.Background(), "explicit"); err != nil {
		t.Fatal(err)
	}
	start := h.span(t, "sdk.connection.start")
	if lifecycleObsAttr(start, "connection_id") != "explicit" || lifecycleObsAttr(start, "outcome") != "success" || lifecycleObsAttr(start, "generation") == "" {
		t.Fatalf("unexpected start span attributes: %v", start.Attributes)
	}
	if !lifecycleObsHasEvent(start, "create_client") {
		t.Fatalf("missing create_client event: %v", start.Events)
	}
	h.span(t, "sdk.connection.stop")
	h.span(t, "sdk.connection.destroy")
	if lifecycleTransitionTotal("start", "success") != startBefore+1 || lifecycleTransitionTotal("stop", "success") != stopBefore+1 {
		t.Fatal("connection transition metric did not advance")
	}
}

func TestConnectionStartFailureIsClassifiedAndRedacted(t *testing.T) {
	h := newLifecycleObsHarness(t)
	cfg := controllerConfig()
	lifecycle := controllerLifecycle(&atomic.Int32{})
	lifecycle.CreateClient = func(context.Context, types.Connection) (*string, error) {
		return nil, errors.New(lifecycleObsSecret)
	}
	cfg.Lifecycle = lifecycle
	c := newTestController(t, cfg)
	if _, err := c.LoadConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	failedBefore := lifecycleTransitionTotal("start", "failure")
	if _, err := c.StartConnection(context.Background(), "explicit"); err == nil {
		t.Fatal("start unexpectedly succeeded")
	}
	start := h.span(t, "sdk.connection.start")
	if lifecycleObsAttr(start, "outcome") != "failure" || lifecycleObsAttr(start, "error_code") == "" {
		t.Fatalf("start failure not classified: %v", start.Attributes)
	}
	fields, ok := h.logged("connection start failed")
	if !ok || fields["error_code"] == nil {
		t.Fatalf("missing connection start failed warning: %v", h.sink.Records())
	}
	if lifecycleTransitionTotal("start", "failure") != failedBefore+1 {
		t.Fatal("failed start transition not counted")
	}
	h.assertNoSecret(t)
}

func TestConnectionStartPanicIsLogged(t *testing.T) {
	h := newLifecycleObsHarness(t)
	cfg := controllerConfig()
	lifecycle := controllerLifecycle(&atomic.Int32{})
	lifecycle.CreateClient = func(context.Context, types.Connection) (*string, error) {
		panic(lifecycleObsSecret)
	}
	cfg.Lifecycle = lifecycle
	c := newTestController(t, cfg)
	if _, err := c.LoadConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.StartConnection(context.Background(), "explicit"); err == nil {
		t.Fatal("start unexpectedly succeeded")
	}
	if !lifecycleObsHasEvent(h.span(t, "sdk.connection.start"), "panic_recovered") {
		t.Fatal("panic_recovered event missing")
	}
	if _, ok := h.logged("connection start handler panicked"); !ok {
		t.Fatal("panic log missing")
	}
	h.assertNoSecret(t)
}

func TestMetadataRefreshFailureAndRejectAreObservable(t *testing.T) {
	h := newLifecycleObsHarness(t)
	c := metadataTestController(t, func(context.Context, types.Connection, *string) (ConnectionMetadataUpdate, error) {
		return ConnectionMetadataUpdate{}, errors.New(lifecycleObsSecret)
	})
	if _, err := c.RefreshConnectionMetadata(context.Background(), "explicit"); ErrorCode(err) != "CONNECTION_ERROR" {
		t.Fatalf("unstarted refresh code = %q (%v)", ErrorCode(err), err)
	}
	if fields, ok := h.logged("request rejected"); !ok || fields["operation"] != "refresh_connection_metadata" {
		t.Fatalf("missing rejected log: %v", h.sink.Records())
	}
	if _, err := c.StartConnection(context.Background(), "explicit"); err != nil {
		t.Fatal(err)
	}
	metadataWaitIdle(t, c, "explicit")
	refresh := h.span(t, "sdk.connection.metadata.refresh")
	if lifecycleObsAttr(refresh, "outcome") != "failure" {
		t.Fatalf("refresh span outcome = %v", refresh.Attributes)
	}
	if _, ok := h.logged("metadata refresh failed"); !ok {
		t.Fatal("metadata refresh warning missing")
	}
	h.assertNoSecret(t)
}

func TestSourceOpenSpanCoversHandshake(t *testing.T) {
	h := newLifecycleObsHarness(t)
	cfg := controllerConfig()
	revision := "revision"
	cfg.Registrations = []Registration[string]{{Type: "opaque/type", Enumerate: func(context.Context, *string, EnumerateRequest) (SourcePage, error) {
		return SourcePage{Records: []Record{controllerRecord()}, Count: Count{Kind: "unknown"}, Coverage: Coverage{Kind: "complete", Paths: []string{}}, Consistency: ConsistencyPointInTime, SourceRevision: &revision}, nil
	}}}
	c := newTestController(t, cfg)
	if err := c.OpenSource(acceptedControllerContext(context.Background(), t, c), controllerSourceOpen(SourceModeSnapshot), &recordingSourceEmitter{}); err != nil {
		t.Fatal(err)
	}
	span := h.span(t, "sdk.source.open")
	if lifecycleObsAttr(span, "snapshot_items") != "1" || lifecycleObsAttr(span, "pages") != "1" || lifecycleObsAttr(span, "outcome") != "success" {
		t.Fatalf("unexpected source span: %v", span.Attributes)
	}
}

func TestSourceBindingChangeIsReportedAsReset(t *testing.T) {
	h := newLifecycleObsHarness(t)
	cfg := controllerConfig()
	cfg.Registrations = []Registration[string]{{Type: "opaque/type", Enumerate: func(context.Context, *string, EnumerateRequest) (SourcePage, error) {
		return SourcePage{}, nil
	}}}
	c := newTestController(t, cfg)
	request := controllerSourceOpen(SourceModeSnapshot)
	request.Binding.Representation.Revision = "stale"
	err := c.OpenSource(acceptedControllerContext(context.Background(), t, c), request, &recordingSourceEmitter{})
	assertControllerCode(t, err, "RESET_REQUIRED")
	span := h.span(t, "sdk.source.open")
	if lifecycleObsAttr(span, "outcome") != "failure" || !lifecycleObsHasEvent(span, "source.reset_required") {
		t.Fatalf("reset not recorded on span: %v %v", span.Attributes, span.Events)
	}
	if fields, ok := h.logged("source reset required"); !ok || fields["reason"] != "binding_changed" {
		t.Fatalf("missing reset log: %v", h.sink.Records())
	}
}

func TestSourceTransportLossIsLoggedWithoutErrorText(t *testing.T) {
	h := newLifecycleObsHarness(t)
	tracker := NewSourcePhaseTracker(controllerSourceOpen(SourceModeSnapshot), ContinuityBestEffort)
	err := tracker.TransportError(errors.New(lifecycleObsSecret))
	assertControllerCode(t, err, "RESET_REQUIRED")
	fields, ok := h.logged("source transport ended before completion")
	if !ok || fields["reason"] != "transport_ended" {
		t.Fatalf("missing transport loss log: %v", h.sink.Records())
	}
	h.assertNoSecret(t)
}
