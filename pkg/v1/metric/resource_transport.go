package metric

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protowire"

	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	metricpb "github.com/omniviewdev/plugin-sdk/proto/v1/metric"
)

func legacyQueryWire(data []byte) error {
	return resourceplugin.ValidateLegacyUtilityWire(data, []protowire.Number{4, 11, 12, 13, 14}, nil)
}

func legacyStreamWire(data []byte) error {
	return resourceplugin.ValidateLegacyUtilityWire(data, []protowire.Number{6, 9, 10, 11, 12, 13, 15}, nil)
}

// RegisterMetricPluginServer admits resource frames before generated protobuf decode.
func RegisterMetricPluginServer(registrar grpc.ServiceRegistrar, server metricpb.MetricPluginServer) {
	d := metricpb.MetricPlugin_ServiceDesc
	d.Methods = append([]grpc.MethodDesc(nil), d.Methods...)
	d.Streams = append([]grpc.StreamDesc(nil), d.Streams...)
	for i := range d.Methods {
		switch d.Methods[i].MethodName {
		case "Query":
			d.Methods[i].Handler = metricQueryHandler(false)
		case "QueryResource":
			d.Methods[i].Handler = metricQueryHandler(true)
		}
	}
	for i := range d.Streams {
		old := d.Streams[i].Handler
		resource := d.Streams[i].StreamName == "StreamResourceMetrics"
		d.Streams[i].Handler = func(s any, stream grpc.ServerStream) error {
			return old(s, &metricAdmissionStream{ServerStream: stream, resource: resource})
		}
	}
	registrar.RegisterService(&d, server)
}

func metricQueryHandler(
	resource bool,
) func(any, context.Context, func(any) error, grpc.UnaryServerInterceptor) (any, error) {
	return func(
		server any,
		ctx context.Context,
		decode func(any) error,
		interceptor grpc.UnaryServerInterceptor,
	) (any, error) {
		p := new(metricpb.QueryMetricsRequest)
		validator := legacyQueryWire
		if resource {
			validator = nil
		}
		envelope := resourceplugin.NewUtilityTransportEnvelope(p, validator)
		if err := decode(envelope); err != nil {
			return nil, err
		}
		if err := envelope.AdmissionError(); err != nil {
			return nil, resourceplugin.ResourceRPCError(err)
		}
		name := "Query"
		if resource {
			name = "QueryResource"
		}
		impl, ok := server.(metricpb.MetricPluginServer)
		if !ok {
			return nil, resourceplugin.ResourceRPCError(metricError("INTERNAL", "Invalid metric server."))
		}
		handler := func(ctx context.Context, request any) (any, error) {
			query, valid := request.(*metricpb.QueryMetricsRequest)
			if !valid {
				return nil, resourceplugin.ResourceRPCError(metricError("INTERNAL", "Invalid metric request."))
			}
			if resource {
				return impl.QueryResource(ctx, query)
			}
			return impl.Query(ctx, query)
		}
		if interceptor == nil {
			return handler(ctx, p)
		}
		return interceptor(
			ctx,
			p,
			&grpc.UnaryServerInfo{
				Server:     server,
				FullMethod: "/" + metricpb.MetricPlugin_ServiceDesc.ServiceName + "/" + name,
			},
			handler,
		)
	}
}

type metricAdmissionStream struct {
	grpc.ServerStream

	resource bool
}

func (s *metricAdmissionStream) RecvMsg(message any) error {
	p, ok := message.(*metricpb.MetricStreamInput)
	if !ok {
		return s.ServerStream.RecvMsg(message)
	}
	validator := legacyStreamWire
	if s.resource {
		validator = nil
	}
	envelope := resourceplugin.NewUtilityTransportEnvelope(p, validator)
	if err := s.ServerStream.RecvMsg(envelope); err != nil {
		return err
	}
	return resourceplugin.ResourceRPCError(envelope.AdmissionError())
}
