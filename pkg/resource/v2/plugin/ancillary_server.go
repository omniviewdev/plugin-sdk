package plugin

import (
	"context"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	"google.golang.org/grpc"
)

type ancillaryServer struct {
	resourcepb.UnimplementedResourceAncillaryServer
	owner *server
}

func registerAncillary(s *grpc.Server, owner *server) {
	d := resourcepb.ResourceAncillary_ServiceDesc
	d.Methods = []grpc.MethodDesc{{MethodName: "Call", Handler: ancillaryCallHandler}}
	d.Streams = []grpc.StreamDesc{{StreamName: "WatchConnections", Handler: watchConnectionsHandler, ServerStreams: true}, {StreamName: "StreamAction", Handler: streamActionHandler, ServerStreams: true}}
	s.RegisterService(&d, &ancillaryServer{owner: owner})
}
func ancillaryCallHandler(srv interface{}, ctx context.Context, dec func(interface{}) error, i grpc.UnaryServerInterceptor) (interface{}, error) {
	r := &resourcepb.AncillaryRequest{}
	e := &envelope{message: r}
	if err := dec(e); err != nil {
		return nil, rpcError(invalid("malformed ancillary request"))
	}
	if e.err != nil {
		return nil, rpcError(e.err)
	}
	f := func(ctx context.Context, v interface{}) (interface{}, error) {
		s := srv.(*ancillaryServer)
		ctx, err := incomingSession(ctx, s.owner.settings)
		if err != nil {
			return nil, rpcError(err)
		}
		request := v.(*resourcepb.AncillaryRequest)
		if err := s.owner.checkAncillaryCatalogs(ctx, request); err != nil {
			return nil, rpcError(s.owner.classify(err))
		}
		out, err := s.call(ctx, v.(*resourcepb.AncillaryRequest))
		if fenceErr := s.owner.checkAncillaryCatalogs(ctx, request); fenceErr != nil {
			return nil, rpcError(s.owner.classify(fenceErr))
		}
		err = responseError(out, err)
		return out, rpcError(s.owner.classify(err))
	}
	if i == nil {
		return f(ctx, r)
	}
	return i(ctx, r, &grpc.UnaryServerInfo{Server: srv, FullMethod: resourcepb.ResourceAncillary_Call_FullMethodName}, f)
}
func (s *ancillaryServer) call(ctx context.Context, r *resourcepb.AncillaryRequest) (*resourcepb.AncillaryResponse, error) {
	if r.Version != resourcev2.WireVersion {
		return nil, unsupported("unsupported ancillary wire version")
	}
	route := resourcev2.AccessRoute{}
	if r.Route != nil {
		var err error
		route, err = resourcev2.FromProtoAccessRoute(r.Route)
		if err != nil {
			return nil, err
		}
	}
	switch r.Operation {
	case "nativeConnections", "applyConnectionOverlay", "listConnections", "updateConnection", "deleteConnection":
		return s.connectionCall(ctx, r)
	case "scopeValues", "schemaDocument", "groups", "definition", "editorSchemas", "actions":
		return s.presentationCall(ctx, r, route)
	case "relationships", "resolveRelationships", "health", "events":
		return s.relationshipHealthCall(ctx, r, route)
	default:
		return nil, unsupported("unknown ancillary operation")
	}
}
