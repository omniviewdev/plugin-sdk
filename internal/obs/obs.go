// Package obs provides bounded SDK tracing and diagnostics.
package obs

import (
	"context"
	"errors"
	"strings"
	"sync"

	logging "github.com/omniviewdev/plugin-sdk/log"
	sdkdiag "github.com/omniviewdev/plugin-sdk/pkg/diagnostics"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/status"
)

const TracerName = "omniview.plugin.sdk"

func Tracer() trace.Tracer { return otel.Tracer(TracerName) }

func ErrorCode(err error, resourceCode string) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	if resourceCode != "" {
		return resourceCode
	}
	if s, ok := status.FromError(err); ok {
		return s.Code().String()
	}
	return "other"
}

func End(span trace.Span, err error, resourceCode string) {
	outcome := "success"
	if err != nil {
		outcome = "failure"
		if errors.Is(err, context.Canceled) {
			outcome = "canceled"
		}
		code := ErrorCode(err, resourceCode)
		span.SetAttributes(attribute.String("error_code", code))
		span.SetStatus(codes.Error, code)
	}
	span.SetAttributes(attribute.String("outcome", outcome))
	span.End()
}

var (
	rejections, _  = sdkdiag.Default().Counter("omniview.plugin.self.sdk.rejections", "1")
	streams, _     = sdkdiag.Default().Gauge("omniview.plugin.self.sdk.streams", "1")
	transitions, _ = sdkdiag.Default().Counter("omniview.plugin.self.sdk.connection.transitions", "1")
	streamMu       sync.Mutex
	streamCounts   = map[string]int{}
)

func metricValue(value string) string {
	value = strings.ToLower(value)
	if !sdkdiag.ValidValue(value) {
		return "other"
	}
	return value
}
func attr(key sdkdiag.Key, value string) sdkdiag.Attr {
	return sdkdiag.Attr{Key: key, Value: metricValue(value)}
}

func Reject(ctx context.Context, log logging.Logger, operation, code string, kv ...any) {
	trace.SpanFromContext(ctx).AddEvent("sdk.rejected", trace.WithAttributes(attribute.String("operation", operation), attribute.String("error_code", code)))
	if log == nil {
		log = logging.Default()
	}
	fields := append([]any{"operation", operation, "error_code", code}, kv...)
	log.Debugw(ctx, "request rejected", fields...)
	rejections.Add(1, attr(sdkdiag.KeyOperation, operation), attr(sdkdiag.KeyReason, code))
}

func StreamOpened(op string) func() {
	switch op {
	case "source", "watch_connections", "watch_metadata", "stream_action", "log_stream", "exec_stream", "metric_stream":
	default:
		op = "other"
	}
	streamMu.Lock()
	streamCounts[op]++
	streams.Set(float64(streamCounts[op]), attr(sdkdiag.KeyOperation, op))
	streamMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			streamMu.Lock()
			defer streamMu.Unlock()
			streamCounts[op]--
			streams.Set(float64(streamCounts[op]), attr(sdkdiag.KeyOperation, op))
		})
	}
}

func ConnectionTransition(phase, outcome string) {
	transitions.Add(1, attr(sdkdiag.KeyPhase, phase), attr(sdkdiag.KeyOutcome, outcome))
}
