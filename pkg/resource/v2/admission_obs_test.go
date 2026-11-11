package resourcev2

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
	sdkdiag "github.com/omniviewdev/plugin-sdk/pkg/diagnostics"
)

type admissionObsHarness struct {
	spans *tracetest.InMemoryExporter
	sink  *logtest.RecordingSink
}

func newAdmissionObsHarness(t *testing.T) *admissionObsHarness {
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
	return &admissionObsHarness{spans: spans, sink: sink}
}

func admissionRejectionTotal() float64 {
	metrics, _ := sdkdiag.Default().Snapshot()
	total := 0.0
	for _, metric := range metrics {
		if metric.Name != "omniview.plugin.self.sdk.rejections" {
			continue
		}
		for _, point := range metric.Points {
			total += point.Value
		}
	}
	return total
}

func TestAdmitStreamingActionRejectionIsObservable(t *testing.T) {
	h := newAdmissionObsHarness(t)
	request := ActionRequest{
		Route:     controllerTarget().Route,
		Reference: ResourceReference{Address: controllerAddress()},
		Action:    "stream",
		Input:     Value{Kind: "null"},
	}
	// A plain controller does not implement StreamingActioner.
	controller := newTestController(t, controllerConfig())
	before := admissionRejectionTotal()
	ctx, span := otel.Tracer("test").Start(context.Background(), "test.parent")
	_, err := AdmitStreamingAction(ctx, controller, request)
	span.End()
	assertControllerCode(t, err, "UNSUPPORTED")

	var event bool
	for _, recorded := range h.spans.GetSpans() {
		for _, e := range recorded.Events {
			if e.Name != "sdk.rejected" {
				continue
			}
			attrs := fmt.Sprint(e.Attributes)
			if strings.Contains(attrs, "UNSUPPORTED") && strings.Contains(attrs, "admit_streaming_action") {
				event = true
			}
		}
	}
	if !event {
		t.Fatalf("missing sdk.rejected span event: %v", h.spans.GetSpans())
	}
	var logged bool
	for _, record := range h.sink.Records() {
		if record.Message == "request rejected" && record.Level == logging.LevelDebug {
			logged = true
		}
	}
	if !logged {
		t.Fatalf("missing Debug request rejected record: %v", h.sink.Records())
	}
	if after := admissionRejectionTotal(); after <= before {
		t.Fatalf("rejection metric did not increase: before=%v after=%v", before, after)
	}
}

func TestProviderPanicLogsTypeNotValue(t *testing.T) {
	h := newAdmissionObsHarness(t)
	cfg := controllerConfig()
	cfg.Catalog = func(context.Context, CatalogRequest) (Catalog, error) {
		panic(errors.New("token=abc123"))
	}
	controller := newTestController(t, cfg)
	ctx, span := otel.Tracer("test").Start(context.Background(), "test.parent")
	_, err := controller.Catalog(ctx, CatalogRequest{Route: controllerTarget().Route})
	span.End()
	assertControllerCode(t, err, "INTERNAL")
	var logged bool
	for _, record := range h.sink.Records() {
		if record.Message != "provider handler panicked" {
			continue
		}
		logged = true
		if record.Level != logging.LevelError {
			t.Fatalf("unexpected level %v", record.Level)
		}
		for _, field := range record.Fields {
			if field.Key != "stack" && strings.Contains(fmt.Sprint(field.Value), "abc123") {
				t.Fatalf("panic value leaked in %s", field.Key)
			}
		}
	}
	if !logged {
		t.Fatal("missing provider handler panicked record")
	}
	var event bool
	for _, recorded := range h.spans.GetSpans() {
		for _, e := range recorded.Events {
			event = event || e.Name == "panic_recovered"
		}
	}
	if !event {
		t.Fatal("missing panic_recovered event")
	}
}

func TestInvalidSourcePageWarnsWithoutProviderText(t *testing.T) {
	h := newAdmissionObsHarness(t)
	target := controllerTarget()
	page := SourcePage{Records: make([]Record, 2)}
	err := checkPage(page, ResourceType{}, target, 1)
	if err == nil {
		t.Fatal("expected page rejection")
	}
	var warned bool
	for _, record := range h.sink.Records() {
		if record.Message == "provider source page invalid" && record.Level == logging.LevelWarn {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("missing warn record: %v", h.sink.Records())
	}
}
