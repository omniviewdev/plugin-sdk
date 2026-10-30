package exec

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	execpb "github.com/omniviewdev/plugin-sdk/proto/v1/exec"
)

// RegisterExecPluginServer preserves all generated handlers except session admission.
func RegisterExecPluginServer(registrar grpc.ServiceRegistrar, server execpb.ExecPluginServer) {
	descriptor := execpb.ExecPlugin_ServiceDesc
	descriptor.Methods = append([]grpc.MethodDesc(nil), descriptor.Methods...)
	for i := range descriptor.Methods {
		switch descriptor.Methods[i].MethodName {
		case "CreateSession":
			descriptor.Methods[i].Handler = execSessionHandler(false)
		case "CreateResourceSession":
			descriptor.Methods[i].Handler = execSessionHandler(true)
		}
	}
	registrar.RegisterService(&descriptor, server)
}

func execSessionHandler(
	resource bool,
) func(any, context.Context, func(any) error, grpc.UnaryServerInterceptor) (any, error) {
	return func(server any, ctx context.Context, decode func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		request := new(execpb.SessionOptions)
		envelope := resourceplugin.NewTransportEnvelope(request, !resource)
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
			provider, ok := server.(execpb.ExecPluginServer)
			if !ok {
				return nil, status.Error(codes.Internal, "invalid exec server")
			}
			options, ok := message.(*execpb.SessionOptions)
			if !ok {
				return nil, status.Error(codes.Internal, "invalid exec session request")
			}
			if resource {
				return provider.CreateResourceSession(ctx, options)
			}
			return provider.CreateSession(ctx, options)
		}
		if interceptor == nil {
			return handler(ctx, request)
		}
		return interceptor(
			ctx,
			request,
			&grpc.UnaryServerInfo{
				Server:     server,
				FullMethod: "/" + execpb.ExecPlugin_ServiceDesc.ServiceName + "/" + method,
			},
			handler,
		)
	}
}
