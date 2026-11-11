package plugin

import (
	"context"
	"fmt"

	logging "github.com/omniviewdev/plugin-sdk/log"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// recordErrorMasked notes that provider/transport error text was replaced by a
// fixed message before crossing the wire. Only the gRPC code, the resource
// error code and the Go type of the original error are recorded; never its text.
func recordErrorMasked(ctx context.Context, operation string, original error, grpcCode, errorCode string) {
	if operation == "" {
		operation = "unknown"
	}
	goType := fmt.Sprintf("%T", original)
	trace.SpanFromContext(ctx).AddEvent("sdk.error_masked", trace.WithAttributes(
		attribute.String("operation", operation),
		attribute.String("grpc_code", grpcCode),
		attribute.String("error_code", errorCode),
	))
	logging.Default().Debugw(ctx, "provider error masked",
		"operation", operation,
		"grpc_code", grpcCode,
		"error_code", errorCode,
		"error_type", goType,
	)
}

// actionReadinessViolation warns about a streaming action readiness protocol
// violation. reason is a closed SDK-authored token.
func actionReadinessViolation(ctx context.Context, reason string) {
	logging.Default().Warnw(ctx, "action readiness protocol violation", "operation", "stream_action", "reason", reason)
	trace.SpanFromContext(ctx).AddEvent("action.readiness_violation", trace.WithAttributes(attribute.String("reason", reason)))
}

// actionRejected records the action.rejected span event with the resource error
// code and the phase that refused the request.
func actionRejected(_ context.Context, span trace.Span, phase string, err error) {
	span.AddEvent("action.rejected", trace.WithAttributes(
		attribute.String("error_code", resourcev2.ErrorCode(err)),
		attribute.String("action_phase", phase),
	))
}
