package logs

import (
	"context"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	logspb "github.com/omniviewdev/plugin-sdk/proto/v1/logs"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protowire"
)

// RegisterLogPluginServer preserves generated lifecycle handlers while admitting
// resource session requests before generated decoding or provider side effects.
func RegisterLogPluginServer(registrar grpc.ServiceRegistrar, server logspb.LogPluginServer) {
	descriptor := logspb.LogPlugin_ServiceDesc
	descriptor.Methods = append([]grpc.MethodDesc(nil), descriptor.Methods...)
	for i := range descriptor.Methods {
		switch descriptor.Methods[i].MethodName {
		case "CreateSession":
			descriptor.Methods[i].Handler = logSessionHandler(false)
		case "CreateResourceSession":
			descriptor.Methods[i].Handler = logSessionHandler(true)
		}
	}
	registrar.RegisterService(&descriptor, server)
}
func logSessionHandler(resource bool) func(any, context.Context, func(any) error, grpc.UnaryServerInterceptor) (any, error) {
	return func(server any, ctx context.Context, decode func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		request := new(logspb.CreateLogSessionRequest)
		var legacy func([]byte) error
		if !resource {
			legacy = func(data []byte) error {
				return resourceplugin.ValidateLegacyUtilityWire(data, []protowire.Number{3, 5, 6, 7, 8}, []protowire.Number{1, 2})
			}
		}
		envelope := resourceplugin.NewUtilityTransportEnvelope(request, legacy)
		if err := decode(envelope); err != nil {
			return nil, err
		}
		if err := envelope.AdmissionError(); err != nil {
			return nil, resourceplugin.ResourceRPCError(err)
		}
		method := "CreateSession"
		if resource {
			method = "CreateResourceSession"
		}
		handler := func(ctx context.Context, message any) (any, error) {
			if resource {
				return server.(logspb.LogPluginServer).CreateResourceSession(ctx, message.(*logspb.CreateLogSessionRequest))
			}
			return server.(logspb.LogPluginServer).CreateSession(ctx, message.(*logspb.CreateLogSessionRequest))
		}
		if interceptor == nil {
			return handler(ctx, request)
		}
		return interceptor(ctx, request, &grpc.UnaryServerInfo{Server: server, FullMethod: "/" + logspb.LogPlugin_ServiceDesc.ServiceName + "/" + method}, handler)
	}
}
