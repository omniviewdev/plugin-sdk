package hostconnections

import (
	"context"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	rp "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	pb "github.com/omniviewdev/plugin-sdk/proto/v1/hostconnections"
	"google.golang.org/grpc"
)

type server struct {
	pb.UnimplementedHostConnectionsServer
	service Service
}

// RegisterServer installs a bounded, strictly decoded callback service.
func RegisterServer(reg grpc.ServiceRegistrar, service Service) {
	desc := pb.HostConnections_ServiceDesc
	desc.Methods = []grpc.MethodDesc{{MethodName: "Call", Handler: func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		req := &pb.CallRequest{}
		envelope := rp.NewTransportEnvelope(req, false)
		if err := dec(envelope); err != nil {
			return nil, rp.ResourceRPCError(invalid())
		}
		if err := envelope.AdmissionError(); err != nil {
			return nil, rp.ResourceRPCError(err)
		}
		handler := func(ctx context.Context, request any) (any, error) {
			return srv.(*server).Call(ctx, request.(*pb.CallRequest))
		}
		if interceptor == nil {
			return handler(ctx, req)
		}
		return interceptor(ctx, req, &grpc.UnaryServerInfo{Server: srv, FullMethod: pb.HostConnections_Call_FullMethodName}, handler)
	}}}
	reg.RegisterService(&desc, &server{service: service})
}
func (s *server) Call(ctx context.Context, req *pb.CallRequest) (out *pb.CallResponse, err error) {
	defer func() {
		if err != nil {
			err = rp.ResourceRPCError(err)
		}
	}()
	if s.service == nil || req == nil || req.GetOwnerPlugin() == "" || len(req.GetDelegationToken()) == 0 {
		return nil, invalid()
	}
	token, owner, target := req.GetDelegationToken(), req.GetOwnerPlugin(), req.GetTargetId()
	switch op := req.Operation.(type) {
	case *pb.CallRequest_ResolveTarget:
		if target != "" || op.ResolveTarget == nil || op.ResolveTarget.GetType() == "" || op.ResolveTarget.GetRepresentation() == "" {
			return nil, invalid()
		}
		p := op.ResolveTarget
		route, e := resourcev2.FromProtoAccessRoute(p.GetRoute())
		if e != nil {
			return nil, e
		}
		scope, e := resourcev2.FromProtoQueryScope(p.GetScope())
		if e != nil {
			return nil, e
		}
		result, e := s.service.ResolveTarget(ctx, token, owner, ResolveTargetRequest{Route: route, Type: p.GetType(), Scope: scope, Representation: p.GetRepresentation()})
		if e != nil {
			return nil, e
		}
		if result.TargetID == "" || result.Target.Route != route || result.Target.Type != p.GetType() || result.Target.Representation != p.GetRepresentation() {
			return nil, invalid()
		}
		t, e := resourcev2.ToProtoReadTarget(result.Target)
		if e != nil {
			return nil, e
		}
		return &pb.CallResponse{Result: &pb.CallResponse_ResolvedTarget{ResolvedTarget: &pb.ResolvedTarget{TargetId: result.TargetID, Target: t}}}, nil
	case *pb.CallRequest_SchemaDocument:
		if target != "" || op.SchemaDocument == nil {
			return nil, invalid()
		}
		route, e := resourcev2.FromProtoAccessRoute(op.SchemaDocument.GetRoute())
		if e != nil {
			return nil, e
		}
		ref, e := resourcev2.FromProtoSchemaDocumentReference(op.SchemaDocument.GetReference())
		if e != nil {
			return nil, e
		}
		d, e := s.service.GetSchemaDocument(ctx, token, owner, route, ref)
		if e != nil {
			return nil, e
		}
		if d.Reference != ref {
			return nil, invalid()
		}
		p, e := resourcev2.ToProtoSchemaDocument(d)
		if e != nil {
			return nil, e
		}
		return &pb.CallResponse{Result: &pb.CallResponse_SchemaDocument{SchemaDocument: p}}, nil
	case *pb.CallRequest_Resource:
		if op.Resource == nil || op.Resource.GetVersion() != resourcev2.WireVersion {
			return nil, invalid()
		}
		r := &resourcepb.Response{Version: resourcev2.WireVersion}
		switch v := op.Resource.Operation.(type) {
		case *resourcepb.Request_Catalog:
			if target != "" {
				return nil, invalid()
			}
			in, e := resourcev2.FromProtoCatalogRequest(v.Catalog)
			if e != nil {
				return nil, e
			}
			result, e := s.service.Catalog(ctx, token, owner, in)
			if e != nil {
				return nil, e
			}
			p, e := resourcev2.ToProtoCatalog(result)
			if e != nil {
				return nil, e
			}
			r.Result = &resourcepb.Response_Catalog{Catalog: p}
		case *resourcepb.Request_Get:
			if target == "" {
				return nil, invalid()
			}
			in, e := resourcev2.FromProtoGetRequest(v.Get)
			if e != nil {
				return nil, e
			}
			result, e := s.service.Get(ctx, token, owner, target, in)
			if e != nil {
				return nil, e
			}
			p, e := resourcev2.ToProtoRecord(result)
			if e != nil {
				return nil, e
			}
			r.Result = &resourcepb.Response_Record{Record: p}
		case *resourcepb.Request_Enumerate:
			if target == "" {
				return nil, invalid()
			}
			in, e := resourcev2.FromProtoEnumerateRequest(v.Enumerate)
			if e != nil {
				return nil, e
			}
			result, e := s.service.Enumerate(ctx, token, owner, target, in)
			if e != nil {
				return nil, e
			}
			p, e := resourcev2.ToProtoSourcePage(result)
			if e != nil {
				return nil, e
			}
			r.Result = &resourcepb.Response_Page{Page: p}
		case *resourcepb.Request_Mutate:
			if target == "" {
				return nil, invalid()
			}
			in, e := resourcev2.FromProtoMutationRequest(v.Mutate)
			if e != nil {
				return nil, e
			}
			if in.Operation != "update" || in.ExpectedRevision == nil || *in.ExpectedRevision == "" {
				return nil, invalid()
			}
			result, e := s.service.Update(ctx, token, owner, target, in)
			if e != nil {
				return nil, e
			}
			p, e := resourcev2.ToProtoRecord(result)
			if e != nil {
				return nil, e
			}
			r.Result = &resourcepb.Response_Record{Record: p}
		default:
			return nil, invalid()
		}
		return &pb.CallResponse{Result: &pb.CallResponse_Resource{Resource: r}}, nil
	default:
		return nil, invalid()
	}
}
