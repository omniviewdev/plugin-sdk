package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/grpc"
)

type traceFailureProvider struct {
	streamProvider
	ready      bool
	eventError bool
}

func (p *traceFailureProvider) StreamAction(ctx context.Context, _ resourcev2.ActionRequest, out chan<- resourcev2.ActionEvent) error {
	if p.ready {
		select {
		case out <- resourcev2.ActionEvent{Type: "ready"}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if p.eventError {
		select {
		case out <- resourcev2.ActionEvent{Type: "error", Error: &resourcev2.ResourceOperationError{Code: "UNAVAILABLE", Message: "event-secret-message"}}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return errors.New("native-secret-error")
}

func (*traceFailureProvider) ClassifyError(error) error {
	return resourcev2.NewError("UNAVAILABLE", "classified-secret-message")
}

func TestActionStreamTracePropagationAndRedaction(t *testing.T) {
	for _, tc := range []struct {
		name, stage       string
		ready, eventError bool
	}{
		{"before readiness", "pre_ready", false, false},
		{"after readiness", "post_ready", true, false},
		{"error event", "post_ready", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			previous := otel.GetTracerProvider()
			otel.SetTracerProvider(provider)
			t.Cleanup(func() { otel.SetTracerProvider(previous); _ = provider.Shutdown(context.Background()) })

			s := grpc.NewServer()
			RegisterServer(s, &traceFailureProvider{ready: tc.ready, eventError: tc.eventError})
			client := NewClient(pipe(t, s))
			request := actionInput()
			request.Input.Map["secret"] = resourcev2.Value{Kind: "string", String: "input-secret-value"}
			ctx := acceptedCatalogContext(context.Background(), t, client, request.Route)
			out := make(chan resourcev2.ActionEvent, 4)
			err := client.StreamAction(ctx, request, out)
			if tc.eventError {
				if err != nil {
					t.Fatalf("error event changed stream result: %v", err)
				}
				if len(out) != 2 {
					t.Fatalf("expected readiness and error events, got %d", len(out))
				}
				<-out // readiness
				event := <-out
				if event.Type != "error" || resourcev2.ErrorCode(event.Error) != "UNAVAILABLE" {
					t.Fatalf("error event changed: %#v", event)
				}
			} else if resourcev2.ErrorCode(err) != "UNAVAILABLE" {
				t.Fatalf("error classification: %v", err)
			}
			var clientSpan, serverSpan sdktrace.ReadOnlySpan
			for _, span := range recorder.Ended() {
				switch span.Name() {
				case "resource.action.stream.client":
					clientSpan = span
				case "resource.action.stream.server":
					serverSpan = span
				}
			}
			if clientSpan == nil || serverSpan == nil {
				t.Fatal("missing action stream spans")
			}
			if serverSpan.Parent().SpanID() != clientSpan.SpanContext().SpanID() || serverSpan.SpanContext().TraceID() != clientSpan.SpanContext().TraceID() {
				t.Fatal("server span is not linked to client span")
			}
			for _, span := range []sdktrace.ReadOnlySpan{clientSpan, serverSpan} {
				attributes := fmt.Sprint(span.Attributes())
				events := fmt.Sprint(span.Events())
				if !strings.Contains(attributes, tc.stage) || !strings.Contains(attributes, "UNAVAILABLE") {
					t.Fatalf("missing stage or classification: %s", attributes)
				}
				if span.Status().Code != codes.Error || !strings.Contains(attributes, "failure") {
					t.Fatalf("missing failure status: %v %s", span.Status(), attributes)
				}
				for _, secret := range []string{"native-secret-error", "classified-secret-message", "event-secret-message", "input-secret-value"} {
					if strings.Contains(attributes+events+span.Status().Description, secret) {
						t.Fatalf("trace leaked %q", secret)
					}
				}
			}
			eventName := "action.provider.failure"
			if tc.eventError {
				eventName = "action.provider.error"
			}
			if !strings.Contains(fmt.Sprint(serverSpan.Events()), eventName) {
				t.Fatal("missing classified provider failure")
			}
		})
	}
}
