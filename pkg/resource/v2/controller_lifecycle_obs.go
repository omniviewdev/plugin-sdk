package resourcev2

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/omniviewdev/plugin-sdk/internal/obs"
	logging "github.com/omniviewdev/plugin-sdk/log"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// lifecycleGenerations numbers client generations started by this process. It is
// a diagnostic counter only and never leaves the process as an identifier.
var lifecycleGenerations atomic.Uint64

func lifecycleLogger() logging.Logger { return logging.Default() }

// lifecycleOutcome maps an error to the closed transition outcome vocabulary.
func lifecycleOutcome(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "failure"
	}
}

// lifecycleStart opens a sdk.connection.<phase> span; the returned finisher records
// the transition metric and ends the span.
func lifecycleStart(ctx context.Context, phase, connectionID string) (context.Context, trace.Span, func(error)) {
	return lifecycleStartNamed(ctx, phase, phase, connectionID)
}

// lifecycleStartNamed is lifecycleStart with a span suffix distinct from the metric phase.
func lifecycleStartNamed(ctx context.Context, suffix, phase, connectionID string) (context.Context, trace.Span, func(error)) {
	ctx, span := obs.Tracer().Start(ctx, "sdk.connection."+suffix, trace.WithAttributes(attribute.String("connection_id", connectionID)))
	return ctx, span, func(err error) {
		obs.ConnectionTransition(phase, lifecycleOutcome(err))
		obs.End(span, err, ErrorCode(err))
	}
}

// sourceResetRequired records that a source session must resynchronize. The reason
// is a closed value; no resource or provider text is included.
func sourceResetRequired(ctx context.Context, reason string) {
	trace.SpanFromContext(ctx).AddEvent("source.reset_required", trace.WithAttributes(attribute.String("reason", reason)))
	lifecycleLogger().Infow(ctx, "source reset required", "reason", reason, "error_code", "RESET_REQUIRED")
}

// sourceTransportEnded logs a source transport that ended before its terminal frame.
// The phase is the tracker's frame phase number; the transport error text is never logged.
func sourceTransportEnded(phase uint8) {
	lifecycleLogger().Infow(context.Background(), "source transport ended before completion", "reason", "transport_ended", "error_code", "RESET_REQUIRED", "phase", phase)
}
