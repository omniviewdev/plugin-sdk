package plugin

import (
	"context"

	"google.golang.org/grpc"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

const (
	ancillaryListConnections      = "listConnections"
	ancillaryScopeValues          = "scopeValues"
	ancillaryUpdateConnection     = "updateConnection"
	ancillaryDeleteConnection     = "deleteConnection"
	ancillaryGroups               = "groups"
	ancillaryDefinition           = "definition"
	ancillaryEditorSchemas        = "editorSchemas"
	ancillaryActions              = "actions"
	ancillaryRelationships        = "relationships"
	ancillaryResolveRelationships = "resolveRelationships"
	ancillaryHealth               = "health"
	ancillaryEvents               = "events"
	ancillarySchemaDocument       = "schemaDocument"
)

type ancillaryServer struct {
	resourcepb.UnimplementedResourceAncillaryServer

	owner *server
}

func registerAncillary(s *grpc.Server, owner *server) {
	d := resourcepb.ResourceAncillary_ServiceDesc
	d.Methods = []grpc.MethodDesc{{MethodName: rpcCallMethod, Handler: ancillaryCallHandler}}
	d.Streams = []grpc.StreamDesc{
		{StreamName: "WatchConnections", Handler: watchConnectionsHandler, ServerStreams: true},
		{StreamName: "StreamAction", Handler: streamActionHandler, ServerStreams: true},
	}
	s.RegisterService(&d, &ancillaryServer{owner: owner})
}

//nolint:revive // gRPC requires the server argument before the request context.
func ancillaryCallHandler(
	srv any,
	ctx context.Context,
	dec func(any) error,
	i grpc.UnaryServerInterceptor,
) (any, error) {
	return handleAncillaryCall(ctx, srv, dec, i)
}

func handleAncillaryCall(
	ctx context.Context,
	srv any,
	dec func(any) error,
	i grpc.UnaryServerInterceptor,
) (any, error) {
	r := &resourcepb.AncillaryRequest{}
	e := &envelope{message: r}
	if err := dec(e); err != nil {
		return nil, rpcError(invalid("malformed ancillary request"))
	}
	if e.err != nil {
		return nil, rpcError(e.err)
	}
	f := func(ctx context.Context, v any) (any, error) {
		s, ok := srv.(*ancillaryServer)
		if !ok {
			return nil, rpcError(invalid("invalid ancillary server"))
		}
		ctx, err := incomingSession(ctx, s.owner.settings)
		if err != nil {
			return nil, rpcError(err)
		}
		request, ok := v.(*resourcepb.AncillaryRequest)
		if !ok {
			return nil, rpcError(invalid("invalid ancillary request"))
		}
		if fenceErr := s.owner.checkAncillaryCatalogs(ctx, request); fenceErr != nil {
			return nil, rpcError(s.owner.classify(fenceErr))
		}
		out, err := s.call(ctx, request)
		if fenceErr := s.owner.checkAncillaryCatalogs(ctx, request); fenceErr != nil {
			return nil, rpcError(s.owner.classify(fenceErr))
		}
		err = responseError(out, err)
		return out, rpcError(s.owner.classify(err))
	}
	if i == nil {
		return f(ctx, r)
	}
	return i(
		ctx,
		r,
		&grpc.UnaryServerInfo{Server: srv, FullMethod: resourcepb.ResourceAncillary_Call_FullMethodName},
		f,
	)
}

func (s *ancillaryServer) call(
	ctx context.Context,
	r *resourcepb.AncillaryRequest,
) (*resourcepb.AncillaryResponse, error) {
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
	case nativeConnectionsOperation,
		connectionOverlayOperation,
		ancillaryListConnections,
		ancillaryUpdateConnection,
		ancillaryDeleteConnection:
		return s.connectionCall(ctx, r)
	case ancillaryScopeValues,
		ancillarySchemaDocument,
		ancillaryGroups,
		ancillaryDefinition,
		ancillaryEditorSchemas,
		ancillaryActions:
		return s.presentationCall(ctx, r, route)
	case ancillaryRelationships, ancillaryResolveRelationships, ancillaryHealth, ancillaryEvents:
		return s.relationshipHealthCall(ctx, r, route)
	default:
		return nil, unsupported("unknown ancillary operation")
	}
}
