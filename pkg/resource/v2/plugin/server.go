package plugin

import (
	"context"
	"errors"
	goplugin "github.com/hashicorp/go-plugin"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	pkgsettings "github.com/omniviewdev/plugin-sdk/settings"
	"google.golang.org/grpc"
)

type GRPCPlugin struct {
	goplugin.NetRPCUnsupportedPlugin
	Impl             resourcev2.Provider
	SettingsProvider pkgsettings.Provider
}

func (p *GRPCPlugin) GRPCServer(_ *goplugin.GRPCBroker, s *grpc.Server) error {
	register(s, p.Impl, p.SettingsProvider)
	return nil
}
func (p *GRPCPlugin) GRPCClient(_ context.Context, _ *goplugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	return NewClient(c), nil
}
func ServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{grpc.MaxRecvMsgSize(MaxWireBytes), grpc.MaxSendMsgSize(MaxWireBytes)}
}
func RegisterServer(s *grpc.Server, p resourcev2.Provider) { register(s, p, nil) }

type server struct {
	resourcepb.UnimplementedResourceProviderServer
	impl     resourcev2.Provider
	settings pkgsettings.Provider
}

func register(s *grpc.Server, p resourcev2.Provider, settings pkgsettings.Provider) {
	desc := resourcepb.ResourceProvider_ServiceDesc
	desc.Methods = []grpc.MethodDesc{{MethodName: "Call", Handler: callHandler}}
	owner := &server{impl: p, settings: settings}
	s.RegisterService(&desc, owner)
	registerAncillary(s, owner)
	registerUpdates(s, owner)
}
func callHandler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	req := &resourcepb.Request{}
	e := &envelope{message: req}
	if err := dec(e); err != nil {
		return nil, rpcError(invalid("malformed resource request"))
	}
	if e.err != nil {
		return nil, rpcError(e.err)
	}
	handler := func(ctx context.Context, r interface{}) (interface{}, error) {
		return srv.(*server).Call(ctx, r.(*resourcepb.Request))
	}
	if interceptor == nil {
		return handler(ctx, req)
	}
	return interceptor(ctx, req, &grpc.UnaryServerInfo{Server: srv, FullMethod: resourcepb.ResourceProvider_Call_FullMethodName}, handler)
}
func (s *server) Call(ctx context.Context, r *resourcepb.Request) (*resourcepb.Response, error) {
	if r.GetVersion() != resourcev2.WireVersion {
		return nil, rpcError(unsupported("unsupported resource wire version"))
	}
	var err error
	ctx, err = incomingSession(ctx, s.settings)
	if err != nil {
		return nil, rpcError(err)
	}
	switch r.Operation.(type) {
	case *resourcepb.Request_Get, *resourcepb.Request_Enumerate, *resourcepb.Request_NativeQuery, *resourcepb.Request_Mutate, *resourcepb.Request_Action:
		if err := s.checkCatalogRoute(ctx, finiteRoute(r)); err != nil {
			return nil, rpcError(s.classify(err))
		}
	}
	out, err := s.dispatch(ctx, r)
	switch r.Operation.(type) {
	case *resourcepb.Request_Get, *resourcepb.Request_Enumerate, *resourcepb.Request_NativeQuery, *resourcepb.Request_Mutate, *resourcepb.Request_Action:
		if fenceErr := s.checkCatalogRoute(ctx, finiteRoute(r)); fenceErr != nil {
			return nil, rpcError(s.classify(fenceErr))
		}
	}
	if err != nil {
		return nil, rpcError(s.classify(err))
	}
	out.Version = resourcev2.WireVersion
	if err = responseError(out, nil); err != nil {
		return nil, rpcError(err)
	}
	return out, nil
}
func (s *server) classify(err error) (out error) {
	out = err
	defer func() {
		if recover() != nil {
			out = resourcev2.NewError("INTERNAL", "resource error classification failed")
		}
	}()
	if err == nil {
		return nil
	}
	var typed *resourcev2.ResourceOperationError
	if errors.As(err, &typed) {
		return err
	}
	if c, ok := s.impl.(resourcev2.ErrorClassifier); ok {
		if classified := c.ClassifyError(err); classified != nil {
			return classified
		}
	}
	return err
}
func (s *server) dispatch(ctx context.Context, r *resourcepb.Request) (*resourcepb.Response, error) {
	out := &resourcepb.Response{}
	switch op := r.Operation.(type) {
	case *resourcepb.Request_Features:
		features, err := resourcev2.DetectFeatures(ctx, s.impl)
		if err != nil {
			return nil, err
		}
		p := &resourcepb.FeatureSet{Traits: make([]string, len(features.Traits))}
		for i, trait := range features.Traits {
			p.Traits[i] = string(trait)
		}
		out.Result = &resourcepb.Response_Features{Features: p}
	case *resourcepb.Request_Catalog:
		in, err := resourcev2.FromProtoCatalogRequest(op.Catalog)
		if err != nil {
			return nil, err
		}
		value, err := s.impl.Catalog(ctx, in)
		if err != nil {
			return nil, err
		}
		p, err := resourcev2.ToProtoCatalog(value)
		if err != nil {
			return nil, err
		}
		out.Result = &resourcepb.Response_Catalog{Catalog: p}
	case *resourcepb.Request_Get:
		provider, ok := s.impl.(resourcev2.Getter)
		if !ok {
			return nil, unsupported("Get unsupported")
		}
		in, err := resourcev2.FromProtoGetRequest(op.Get)
		if err != nil {
			return nil, err
		}
		descriptor, err := resourcev2.AdmitOperation(ctx, s.impl, in)
		if err != nil {
			return nil, err
		}
		value, err := provider.Get(ctx, in)
		if err != nil {
			return nil, err
		}
		if err = resourcev2.ValidateOperationResult(in, value, descriptor); err != nil {
			return nil, err
		}
		p, err := resourcev2.ToProtoRecord(value)
		if err != nil {
			return nil, err
		}
		out.Result = &resourcepb.Response_Record{Record: p}
	case *resourcepb.Request_Enumerate:
		provider, ok := s.impl.(resourcev2.Enumerator)
		if !ok {
			return nil, unsupported("Enumerate unsupported")
		}
		in, err := resourcev2.FromProtoEnumerateRequest(op.Enumerate)
		if err != nil {
			return nil, err
		}
		descriptor, err := resourcev2.AdmitOperation(ctx, s.impl, in)
		if err != nil {
			return nil, err
		}
		value, err := provider.Enumerate(ctx, in)
		if err != nil {
			return nil, err
		}
		if err = resourcev2.ValidateOperationResult(in, value, descriptor); err != nil {
			return nil, err
		}
		p, err := resourcev2.ToProtoSourcePage(value)
		if err != nil {
			return nil, err
		}
		out.Result = &resourcepb.Response_Page{Page: p}
	case *resourcepb.Request_NativeQuery:
		provider, ok := s.impl.(resourcev2.NativeQuerier)
		if !ok {
			return nil, unsupported("NativeQuery unsupported")
		}
		in, err := resourcev2.FromProtoNativeQueryRequest(op.NativeQuery)
		if err != nil {
			return nil, err
		}
		descriptor, err := resourcev2.AdmitOperation(ctx, s.impl, in)
		if err != nil {
			return nil, err
		}
		value, err := provider.NativeQuery(ctx, in)
		if err != nil {
			return nil, err
		}
		if err = resourcev2.ValidateOperationResult(in, value, descriptor); err != nil {
			return nil, err
		}
		p, err := resourcev2.ToProtoSourcePage(value)
		if err != nil {
			return nil, err
		}
		out.Result = &resourcepb.Response_Page{Page: p}
	case *resourcepb.Request_Mutate:
		provider, ok := s.impl.(resourcev2.Mutator)
		if !ok {
			return nil, unsupported("Mutate unsupported")
		}
		in, err := resourcev2.FromProtoMutationRequest(op.Mutate)
		if err != nil {
			return nil, err
		}
		descriptor, err := resourcev2.AdmitOperation(ctx, s.impl, in)
		if err != nil {
			return nil, err
		}
		value, err := provider.Mutate(ctx, in)
		if err != nil {
			return nil, err
		}
		if err = resourcev2.ValidateOperationResult(in, value, descriptor); err != nil {
			return nil, err
		}
		p, err := resourcev2.ToProtoRecord(value)
		if err != nil {
			return nil, err
		}
		out.Result = &resourcepb.Response_Record{Record: p}
	case *resourcepb.Request_Action:
		provider, ok := s.impl.(resourcev2.Actioner)
		if !ok {
			return nil, unsupported("Action unsupported")
		}
		in, err := resourcev2.FromProtoActionRequest(op.Action)
		if err != nil {
			return nil, err
		}
		descriptor, err := resourcev2.AdmitOperation(ctx, s.impl, in)
		if err != nil {
			return nil, err
		}
		value, err := provider.Action(ctx, in)
		if err != nil {
			return nil, err
		}
		if err = resourcev2.ValidateOperationResult(in, value, descriptor); err != nil {
			return nil, err
		}
		p, err := resourcev2.ToProtoValue(value)
		if err != nil {
			return nil, err
		}
		out.Result = &resourcepb.Response_Value{Value: p}
	case *resourcepb.Request_Connection:
		p, err := s.connection(ctx, op.Connection)
		if err != nil {
			return nil, err
		}
		out.Result = &resourcepb.Response_Connection{Connection: p}
	default:
		return nil, invalid("missing resource operation")
	}
	return out, nil
}
