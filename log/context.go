package log

import (
	"context"
	"strings"

	grpcmetadata "google.golang.org/grpc/metadata"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

func enrichFromContext(ctx context.Context) []Field {
	fields := make([]Field, 0, 8)

	if pc := types.PluginContextFromContext(ctx); pc != nil {
		if pc.RequestID != "" {
			fields = append(fields, String("request_id", pc.RequestID))
		}
		if pc.RequesterID != "" {
			fields = append(fields, String("requester_id", pc.RequesterID))
		}
		if pc.Connection != nil && pc.Connection.ID != "" {
			fields = append(fields, String("connection_id", pc.Connection.ID))
		}
		if pc.ResourceContext != nil && pc.ResourceContext.Key != "" {
			fields = append(fields, String("resource_key", pc.ResourceContext.Key))
		}
	}

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

func extractTraceIDs(ctx context.Context) (string, string) {
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
