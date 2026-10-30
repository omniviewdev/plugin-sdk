package plugin

import (
	"context"
	"errors"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	pkgsettings "github.com/omniviewdev/plugin-sdk/settings"
)

// GRPCPlugin exposes a resource provider through HashiCorp's gRPC plugin transport.
type GRPCPlugin struct {
	goplugin.NetRPCUnsupportedPlugin

	Impl             resourcev2.Provider
	SettingsProvider pkgsettings.Provider
}

// GRPCServer registers resource services with the plugin's gRPC server.
func (p *GRPCPlugin) GRPCServer(_ *goplugin.GRPCBroker, s *grpc.Server) error {
	register(s, p.Impl, p.SettingsProvider)
	return nil
}

// GRPCClient constructs the remote resource provider client.
func (p *GRPCPlugin) GRPCClient(_ context.Context, _ *goplugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	return NewClient(c), nil
}

// ServerOptions bounds incoming and outgoing resource frames.
func ServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{grpc.MaxRecvMsgSize(MaxWireBytes), grpc.MaxSendMsgSize(MaxWireBytes)}
}

// RegisterServer registers resource services without a settings provider.
func RegisterServer(s *grpc.Server, p resourcev2.Provider) { register(s, p, nil) }

type server struct {
	resourcepb.UnimplementedResourceProviderServer

	impl     resourcev2.Provider
	settings pkgsettings.Provider
}

func register(s *grpc.Server, p resourcev2.Provider, settings pkgsettings.Provider) {
	desc := resourcepb.ResourceProvider_ServiceDesc
	desc.Methods = []grpc.MethodDesc{{
		MethodName: rpcCallMethod,
		Handler: func(
			srv any,
			ctx context.Context,
			dec func(any) error,
			interceptor grpc.UnaryServerInterceptor,
		) (any, error) {
			return callHandler(ctx, srv, dec, interceptor)
		},
	}}
	owner := &server{impl: p, settings: settings}
	s.RegisterService(&desc, owner)
	registerAncillary(s, owner)
	registerUpdates(s, owner)
}

func callHandler(
	ctx context.Context,
	srv any,
	dec func(any) error,
	interceptor grpc.UnaryServerInterceptor,
) (any, error) {
	req := &resourcepb.Request{}
	e := &envelope{message: req}
	if err := dec(e); err != nil {
		return nil, rpcError(invalid("malformed resource request"))
	}
	if e.err != nil {
		return nil, rpcError(e.err)
	}
	handler := func(ctx context.Context, r any) (any, error) {
		owner, ownerOK := srv.(*server)
		request, requestOK := r.(*resourcepb.Request)
		if !ownerOK || !requestOK {
			return nil, rpcError(invalid("invalid resource handler input"))
		}
		return owner.Call(ctx, request)
	}
	if interceptor == nil {
		return handler(ctx, req)
	}
	return interceptor(
		ctx,
		req,
		&grpc.UnaryServerInfo{Server: srv, FullMethod: resourcepb.ResourceProvider_Call_FullMethodName},
		handler,
	)
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
	case *resourcepb.Request_Get,
		*resourcepb.Request_Enumerate,
		*resourcepb.Request_NativeQuery,
		*resourcepb.Request_Mutate,
		*resourcepb.Request_Action:
		if routeErr := s.checkCatalogRoute(ctx, finiteRoute(r)); routeErr != nil {
			return nil, rpcError(s.classify(routeErr))
		}
	}
	out, err := s.dispatch(ctx, r)
	switch r.Operation.(type) {
	case *resourcepb.Request_Get,
		*resourcepb.Request_Enumerate,
		*resourcepb.Request_NativeQuery,
		*resourcepb.Request_Mutate,
		*resourcepb.Request_Action:
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
	switch op := r.Operation.(type) {
	case *resourcepb.Request_Features:
		return s.dispatchFeatures(ctx)
	case *resourcepb.Request_Catalog:
		return s.dispatchCatalog(ctx, op)
	case *resourcepb.Request_Get:
		return s.dispatchGet(ctx, op)
	case *resourcepb.Request_Enumerate:
		return s.dispatchEnumerate(ctx, op)
	case *resourcepb.Request_NativeQuery:
		return s.dispatchNativeQuery(ctx, op)
	case *resourcepb.Request_Mutate:
		return s.dispatchMutate(ctx, op)
	case *resourcepb.Request_Action:
		return s.dispatchAction(ctx, op)
	case *resourcepb.Request_Connection:
		p, err := s.connection(ctx, op.Connection)
		if err != nil {
			return nil, err
		}
		return &resourcepb.Response{Result: &resourcepb.Response_Connection{Connection: p}}, nil
	default:
		return nil, invalid("missing resource operation")
	}
}

func (s *server) dispatchFeatures(ctx context.Context) (*resourcepb.Response, error) {
	out := &resourcepb.Response{}
	features, err := resourcev2.DetectFeatures(ctx, s.impl)
	if err != nil {
		return nil, err
	}
	p := &resourcepb.FeatureSet{Traits: make([]string, len(features.Traits))}
	for i, trait := range features.Traits {
		p.Traits[i] = string(trait)
	}
	out.Result = &resourcepb.Response_Features{Features: p}
	return out, nil
}

func (s *server) dispatchCatalog(ctx context.Context, op *resourcepb.Request_Catalog) (*resourcepb.Response, error) {
	out := &resourcepb.Response{}
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
	return out, nil
}

// finiteServerOperation decodes only after the handler checks interface support,
// then retains the admission descriptor through dispatch and result validation.
func finiteServerOperation[P, I, O any](
	ctx context.Context,
	s *server,
	input P,
	decode func(P) (I, error),
	dispatch func(context.Context, I) (O, error),
	encode func(O) (*resourcepb.Response, error),
) (*resourcepb.Response, error) {
	in, err := decode(input)
	if err != nil {
		return nil, err
	}
	descriptor, err := resourcev2.AdmitOperation(ctx, s.impl, in)
	if err != nil {
		return nil, err
	}
	value, err := dispatch(ctx, in)
	if err != nil {
		return nil, err
	}
	if err = resourcev2.ValidateOperationResult(in, value, descriptor); err != nil {
		return nil, err
	}
	return encode(value)
}

func encodeRecordResponse(value resourcev2.Record) (*resourcepb.Response, error) {
	p, err := resourcev2.ToProtoRecord(value)
	if err != nil {
		return nil, err
	}
	return &resourcepb.Response{Result: &resourcepb.Response_Record{Record: p}}, nil
}

func encodePageResponse(value resourcev2.SourcePage) (*resourcepb.Response, error) {
	p, err := resourcev2.ToProtoSourcePage(value)
	if err != nil {
		return nil, err
	}
	return &resourcepb.Response{Result: &resourcepb.Response_Page{Page: p}}, nil
}

func encodeValueResponse(value resourcev2.Value) (*resourcepb.Response, error) {
	p, err := resourcev2.ToProtoValue(value)
	if err != nil {
		return nil, err
	}
	return &resourcepb.Response{Result: &resourcepb.Response_Value{Value: p}}, nil
}

func (s *server) dispatchGet(ctx context.Context, op *resourcepb.Request_Get) (*resourcepb.Response, error) {
	provider, ok := s.impl.(resourcev2.Getter)
	if !ok {
		return nil, unsupported("Get unsupported")
	}
	return finiteServerOperation(ctx, s, op.Get, resourcev2.FromProtoGetRequest,
		provider.Get, encodeRecordResponse)
}

func (s *server) dispatchEnumerate(
	ctx context.Context,
	op *resourcepb.Request_Enumerate,
) (*resourcepb.Response, error) {
	provider, ok := s.impl.(resourcev2.Enumerator)
	if !ok {
		return nil, unsupported("Enumerate unsupported")
	}
	return finiteServerOperation(ctx, s, op.Enumerate, resourcev2.FromProtoEnumerateRequest,
		provider.Enumerate, encodePageResponse)
}

func (s *server) dispatchNativeQuery(
	ctx context.Context,
	op *resourcepb.Request_NativeQuery,
) (*resourcepb.Response, error) {
	provider, ok := s.impl.(resourcev2.NativeQuerier)
	if !ok {
		return nil, unsupported("NativeQuery unsupported")
	}
	return finiteServerOperation(ctx, s, op.NativeQuery, resourcev2.FromProtoNativeQueryRequest,
		provider.NativeQuery, encodePageResponse)
}

func (s *server) dispatchMutate(ctx context.Context, op *resourcepb.Request_Mutate) (*resourcepb.Response, error) {
	provider, ok := s.impl.(resourcev2.Mutator)
	if !ok {
		return nil, unsupported("Mutate unsupported")
	}
	return finiteServerOperation(ctx, s, op.Mutate, resourcev2.FromProtoMutationRequest,
		provider.Mutate, encodeRecordResponse)
}

func (s *server) dispatchAction(ctx context.Context, op *resourcepb.Request_Action) (*resourcepb.Response, error) {
	provider, ok := s.impl.(resourcev2.Actioner)
	if !ok {
		return nil, unsupported("Action unsupported")
	}
	return finiteServerOperation(ctx, s, op.Action, resourcev2.FromProtoActionRequest,
		provider.Action, encodeValueResponse)
}
