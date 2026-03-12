package log

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/trace"
	grpcmetadata "google.golang.org/grpc/metadata"
)

func enrichFromContext(ctx context.Context) []Field {
	var fields []Field

	// Registered enricher (PluginContext fields injected via types.init()).
	if h, ok := contextEnricher.Load().(*enricherHolder); ok && h != nil && h.fn != nil {
		fields = append(fields, h.fn(ctx)...)
	}

	// Trace IDs from gRPC metadata.
	traceID, spanID := extractTraceIDs(ctx)
	if traceID != "" {
		fields = append(fields, String("trace_id", traceID))
	}
	if spanID != "" {
		fields = append(fields, String("span_id", spanID))
	}

	return dedupeFields(fields)
}

func dedupeFields(in []Field) []Field {
	if len(in) <= 1 {
		return in
	}
	idx := make(map[string]int, len(in))
	out := make([]Field, 0, len(in))
	for _, f := range in {
		if at, ok := idx[f.Key]; ok {
			out[at] = f
			continue
		}
		idx[f.Key] = len(out)
		out = append(out, f)
	}
	return out
}

// extractTraceIDs returns a (traceID, spanID) pair from ctx using the
// following precedence:
//
//  1. OTel span context — used when a real (non-noop) TracerProvider is
//     configured. A zero/noop SpanContext is ignored.
//  2. gRPC incoming metadata — checked via traceFromMD.
//  3. gRPC outgoing metadata — same check, covers client-side contexts.
//
// Returns ("", "") when no trace information is available.
func extractTraceIDs(ctx context.Context) (string, string) {
	// Prefer OTel span context (active when TracerProvider is configured).
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		return sc.TraceID().String(), sc.SpanID().String()
	}

	// Fall back to gRPC metadata (existing behavior).
	if md, ok := grpcmetadata.FromIncomingContext(ctx); ok {
		if traceID, spanID := traceFromMD(md); traceID != "" || spanID != "" {
			return traceID, spanID
		}
	}
	if md, ok := grpcmetadata.FromOutgoingContext(ctx); ok {
		if traceID, spanID := traceFromMD(md); traceID != "" || spanID != "" {
			return traceID, spanID
		}
	}
	return "", ""
}

// traceFromMD extracts trace and span IDs from gRPC metadata headers.
// Supported formats in precedence order:
//
//  1. W3C "traceparent" header (e.g. "00-<traceID>-<spanID>-<flags>")
//  2. Custom "x-trace-id" / "x-span-id" headers
//  3. Fallback "trace_id" / "span_id" headers
func traceFromMD(md grpcmetadata.MD) (string, string) {
	if tp := firstMD(md, "traceparent"); tp != "" {
		parts := strings.Split(tp, "-")
		if len(parts) == 4 {
			return parts[1], parts[2]
		}
	}
	traceID := firstMD(md, "x-trace-id")
	spanID := firstMD(md, "x-span-id")
	if traceID == "" {
		traceID = firstMD(md, "trace_id")
	}
	if spanID == "" {
		spanID = firstMD(md, "span_id")
	}
	return traceID, spanID
}

func firstMD(md grpcmetadata.MD, key string) string {
	vals := md.Get(key)
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}
