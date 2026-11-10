package plugin

import (
	"context"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	grpcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Action streams are excluded from the gRPC stats handlers. Propagate only
// W3C traceparent explicitly, not arbitrary baggage or tracestate.
func actionTraceOutgoing(ctx context.Context) context.Context {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	md, _ := metadata.FromOutgoingContext(ctx)
	md = md.Copy()
	md.Delete("traceparent")
	if parent := carrier.Get("traceparent"); parent != "" {
		md.Set("traceparent", parent)
	}
	return metadata.NewOutgoingContext(ctx, md)
}

func actionTraceIncoming(ctx context.Context) context.Context {
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get("traceparent")
	if len(values) != 1 {
		return ctx
	}
	return propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": values[0]})
}

func startActionTrace(ctx context.Context, name string, kind trace.SpanKind, in resourcev2.ActionRequest) (context.Context, trace.Span) {
	address := in.Reference.Address
	if in.TypeTarget != nil {
		address.Provider = in.TypeTarget.Provider
		address.Type = in.TypeTarget.Type
	}
	return otel.Tracer("github.com/omniviewdev/plugin-sdk/resource/v2").Start(ctx, name,
		trace.WithSpanKind(kind), trace.WithAttributes(
			attribute.String("omniview.action.id", in.Action),
			attribute.String("omniview.plugin.id", address.Provider),
			attribute.String("omniview.connection.id", in.Route.Connection),
			attribute.String("omniview.resource.type", address.Type),
			attribute.String("omniview.resource.id", address.ID),
		))
}

func actionTraceErrorCode(err error) string {
	if err == nil {
		return ""
	}
	classified := domainError(err)
	if code := resourcev2.ErrorCode(classified); code != "INTERNAL" {
		return code
	}
	switch status.Code(err) {
	case grpcodes.Canceled:
		return "CANCELLED"
	case grpcodes.DeadlineExceeded:
		return "TIMEOUT"
	}
	return resourcev2.ErrorCode(err)
}

func finishActionTrace(span trace.Span, stage string, err error) {
	outcome := "success"
	if err != nil {
		outcome = "failure"
		span.SetAttributes(attribute.String("omniview.error.code", actionTraceErrorCode(err)))
		span.SetStatus(codes.Error, "action stream failed")
	}
	span.SetAttributes(attribute.String("omniview.action.stage", stage), attribute.String("omniview.action.outcome", outcome))
	span.End()
}
