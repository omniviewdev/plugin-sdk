package networker

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protowire"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	networkerpb "github.com/omniviewdev/plugin-sdk/proto/v1/networker"
)

func resourceUnsupported() error {
	return resourcev2.NewError("UNSUPPORTED", "Resource forwarding requires StartResourcePortForwardSession.")
}

func resourceInvalidResponse() error {
	return resourcev2.NewError("INVALID_REQUEST", "Resource forwarding returned no resource session.")
}

// RegisterNetworkerPluginServer admits resource requests before generated decoding.
func RegisterNetworkerPluginServer(registrar grpc.ServiceRegistrar, server networkerpb.NetworkerPluginServer) {
	descriptor := networkerpb.NetworkerPlugin_ServiceDesc
	descriptor.Methods = append([]grpc.MethodDesc(nil), descriptor.Methods...)
	for i := range descriptor.Methods {
		switch descriptor.Methods[i].MethodName {
		case "StartPortForwardSession":
			descriptor.Methods[i].Handler = networkSessionHandler(false)
		case "StartResourcePortForwardSession":
			descriptor.Methods[i].Handler = networkSessionHandler(true)
		}
	}
	registrar.RegisterService(&descriptor, server)
}

func networkSessionHandler(
	resource bool,
) func(any, context.Context, func(any) error, grpc.UnaryServerInterceptor) (any, error) {
	return func(server any, ctx context.Context, decode func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		request := new(networkerpb.PortForwardSessionOptions)
		var legacy func([]byte) error
		if !resource {
			legacy = func(data []byte) error {
				return resourceplugin.ValidateLegacyUtilityWire(data, []protowire.Number{10}, nil)
			}
		}
		envelope := resourceplugin.NewUtilityTransportEnvelope(request, legacy)
		if err := decode(envelope); err != nil {
			return nil, err
		}
		if err := envelope.AdmissionError(); err != nil {
			return nil, resourceplugin.ResourceRPCError(err)
		}
		method := "StartPortForwardSession"
		if resource {
			method = "StartResourcePortForwardSession"
		}
		handler := func(ctx context.Context, message any) (any, error) {
			if resource {
				return server.(networkerpb.NetworkerPluginServer).StartResourcePortForwardSession(
					ctx,
					message.(*networkerpb.PortForwardSessionOptions),
				)
			}
			return server.(networkerpb.NetworkerPluginServer).StartPortForwardSession(
				ctx,
				message.(*networkerpb.PortForwardSessionOptions),
			)
		}
		if interceptor == nil {
			return handler(ctx, request)
		}
		return interceptor(
			ctx,
			request,
			&grpc.UnaryServerInfo{
				Server:     server,
				FullMethod: "/" + networkerpb.NetworkerPlugin_ServiceDesc.ServiceName + "/" + method,
			},
			handler,
		)
	}
}
