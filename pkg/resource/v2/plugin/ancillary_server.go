package plugin

import (
	"context"
	"encoding/json"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
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
func presentationJSON(value any) (*resourcepb.AncillaryResponse, error) {
	var err error
	switch v := value.(type) {
	case resourcev2.ResourceDefinition:
		err = resourcev2.ValidateResourceDefinition(v)
	case []resourcev2.ResourceGroup:
		err = resourcev2.ValidateResourceGroups(v)
	case []resourcev2.ActionDescriptor:
		err = resourcev2.ValidateActionDescriptors(v)
	default:
		err = resourcev2.ValidateMetadata(value)
	}
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, invalid("invalid presentation JSON")
	}
	if err = resourcev2.ValidateJSON(data); err != nil {
		return nil, err
	}
	return &resourcepb.AncillaryResponse{Version: 2, Result: &resourcepb.AncillaryResponse_Json{Json: data}}, nil
}
func (s *ancillaryServer) call(ctx context.Context, r *resourcepb.AncillaryRequest) (*resourcepb.AncillaryResponse, error) {
	if r.Version != resourcev2.WireVersion {
		return nil, unsupported("unsupported ancillary wire version")
	}
	impl := s.owner.impl
	route := resourcev2.AccessRoute{}
	if r.Route != nil {
		var err error
		route, err = resourcev2.FromProtoAccessRoute(r.Route)
		if err != nil {
			return nil, err
		}
	}
	out := &resourcepb.AncillaryResponse{Version: 2}
	switch r.Operation {
	case "nativeConnections":
		p, ok := impl.(resourcev2.ConnectionOverlayProvider)
		if !ok {
			return nil, unsupported("native connection snapshots unsupported")
		}
		connections, err := p.NativeConnections(ctx)
		if err != nil {
			return nil, err
		}
		return presentationJSON(connections)
	case "applyConnectionOverlay":
		p, ok := impl.(resourcev2.ConnectionOverlayProvider)
		if !ok {
			return nil, unsupported("connection overlays unsupported")
		}
		var overlay resourcev2.ConnectionOverlay
		if err := decodeConnection(r.ConnectionJson, &overlay); err != nil {
			return nil, err
		}
		if err := resourcev2.ValidateConnectionOverlay(overlay); err != nil {
			return nil, err
		}
		if err := p.ApplyConnectionOverlay(ctx, overlay); err != nil {
			return nil, err
		}
		return presentationJSON(struct{}{})
	case "listConnections":
		p, ok := impl.(interface {
			ListConnections(context.Context) ([]types.Connection, error)
		})
		if !ok {
			return nil, unsupported("connection listing unsupported")
		}
		v, err := p.ListConnections(ctx)
		if err != nil {
			return nil, err
		}
		return presentationJSON(v)
	case "updateConnection":
		p, ok := impl.(interface {
			UpdateConnection(context.Context, types.Connection) (types.Connection, error)
		})
		if !ok {
			return nil, unsupported("connection updates unsupported")
		}
		var in types.Connection
		if err := decodeConnection(r.ConnectionJson, &in); err != nil {
			return nil, err
		}
		v, err := p.UpdateConnection(ctx, in)
		if err != nil {
			return nil, err
		}
		return presentationJSON(v)
	case "deleteConnection":
		p, ok := impl.(interface {
			DeleteConnection(context.Context, string) error
		})
		if !ok {
			return nil, unsupported("connection deletion unsupported")
		}
		if r.Id == "" {
			return nil, invalid("missing connection id")
		}
		if err := p.DeleteConnection(ctx, r.Id); err != nil {
			return nil, err
		}
		return presentationJSON(struct{}{})
	case "scopeValues":
		if route.Connection == "" || r.Type == "" {
			return nil, invalid("missing scope discovery target")
		}
		p, ok := impl.(resourcev2.ScopeValuesProvider)
		if !ok {
			return nil, unsupported("scope discovery unsupported")
		}
		v, err := p.ScopeValues(ctx, route, r.Type)
		if err != nil {
			return nil, err
		}
		return presentationJSON(v)
	case "schemaDocument":
		if route.Connection == "" {
			return nil, invalid("missing access route")
		}
		p, ok := impl.(resourcev2.SchemaDocumentProvider)
		if !ok {
			return nil, unsupported("schema documents unsupported")
		}
		ref, err := resourcev2.FromProtoSchemaDocumentReference(r.DocumentRef)
		if err != nil {
			return nil, err
		}
		v, err := p.GetSchemaDocument(ctx, route, ref)
		if err != nil {
			return nil, err
		}
		if v.Reference != ref {
			return nil, invalid("schema document reference mismatch")
		}
		doc, err := resourcev2.ToProtoSchemaDocument(v)
		if err != nil {
			return nil, err
		}
		out.Result = &resourcepb.AncillaryResponse_Document{Document: doc}
	case "groups", "definition":
		if route.Connection == "" {
			return nil, invalid("missing access route")
		}
		p, ok := impl.(resourcev2.PresentationProvider)
		if !ok {
			return nil, unsupported("presentation unsupported")
		}
		if r.Operation == "groups" {
			v, err := p.GetResourceGroups(ctx, route)
			if err != nil {
				return nil, err
			}
			return presentationJSON(v)
		}
		if r.Type == "" {
			return nil, invalid("missing resource type")
		}
		v, err := p.GetResourceDefinition(ctx, route, r.Type)
		if err != nil {
			return nil, err
		}
		return presentationJSON(v)
	case "editorSchemas":
		if route.Connection == "" {
			return nil, invalid("missing access route")
		}
		p, ok := impl.(resourcev2.EditorSchemaProvider)
		if !ok {
			return nil, unsupported("editor schemas unsupported")
		}
		v, err := p.GetEditorSchemas(ctx, route)
		if err != nil {
			return nil, err
		}
		result := &resourcepb.EditorSchemas{}
		for _, item := range v {
			schema, err := resourcev2.ToProtoEditorSchema(item)
			if err != nil {
				return nil, err
			}
			result.Schemas = append(result.Schemas, schema)
		}
		out.Result = &resourcepb.AncillaryResponse_EditorSchemas{EditorSchemas: result}
	case "actions":
		if route.Connection == "" || r.Type == "" {
			return nil, invalid("missing action catalog target")
		}
		p, ok := impl.(resourcev2.ActionCatalog)
		if !ok {
			return nil, unsupported("action catalog unsupported")
		}
		v, err := p.GetActions(ctx, route, r.Type)
		if err != nil {
			return nil, err
		}
		return presentationJSON(v)
	case "relationships":
		if route.Connection == "" || r.Type == "" {
			return nil, invalid("missing relationship target")
		}
		p, ok := impl.(resourcev2.RelationshipProvider)
		if !ok {
			return nil, unsupported("relationships unsupported")
		}
		v, err := p.GetRelationships(ctx, route, r.Type)
		if err != nil {
			return nil, err
		}
		result := &resourcepb.RelationshipDescriptors{}
		for _, item := range v {
			p, err := resourcev2.ToProtoRelationshipDescriptor(item)
			if err != nil {
				return nil, err
			}
			result.Descriptors = append(result.Descriptors, p)
		}
		out.Result = &resourcepb.AncillaryResponse_Relationships{Relationships: result}
	case "resolveRelationships":
		if r.Get != nil {
			return nil, invalid("legacy get field is not valid for relationship resolution")
		}
		in, err := resourcev2.FromProtoRelationshipResolutionRequest(r.RelationshipResolution)
		if err != nil {
			return nil, err
		}
		if err := resourcev2.ValidateRelationshipResolutionRequest(in); err != nil {
			return nil, err
		}
		p, ok := impl.(resourcev2.RelationshipProvider)
		if !ok {
			return nil, unsupported("relationships unsupported")
		}
		v, err := p.ResolveRelationships(ctx, in)
		if err != nil {
			return nil, err
		}
		if err := resourcev2.ValidateRelationshipResolutionResult(in, v); err != nil {
			return nil, err
		}
		result := &resourcepb.ResolvedRelationships{}
		for _, item := range v {
			p, err := resourcev2.ToProtoResolvedRelationship(item)
			if err != nil {
				return nil, err
			}
			result.Relationships = append(result.Relationships, p)
		}
		out.Result = &resourcepb.AncillaryResponse_Resolved{Resolved: result}
	case "health", "events":
		in, err := resourcev2.FromProtoGetRequest(r.Get)
		if err != nil {
			return nil, err
		}
		switch r.Operation {
		case "health":
			p, ok := impl.(resourcev2.HealthProvider)
			if !ok {
				return nil, unsupported("health unsupported")
			}
			body, err := resourcev2.FromProtoValue(r.Value)
			if err != nil {
				return nil, err
			}
			v, err := p.GetHealth(ctx, in, body)
			if err != nil {
				return nil, err
			}
			result := &resourcepb.OptionalHealth{}
			if v != nil {
				result.Health, err = resourcev2.ToProtoResourceHealth(*v)
				if err != nil {
					return nil, err
				}
			}
			out.Result = &resourcepb.AncillaryResponse_Health{Health: result}
		case "events":
			if r.Limit > MaxPageSize {
				return nil, limited("event limit exceeds bound")
			}
			p, ok := impl.(resourcev2.HealthProvider)
			if !ok {
				return nil, unsupported("health unsupported")
			}
			v, err := p.GetResourceEvents(ctx, in, r.Limit)
			if err != nil {
				return nil, err
			}
			if len(v) > MaxPageSize || r.Limit > 0 && len(v) > int(r.Limit) {
				return nil, limited("event count exceeds bound")
			}
			result := &resourcepb.ResourceEvents{}
			for _, item := range v {
				p, err := resourcev2.ToProtoResourceEvent(item)
				if err != nil {
					return nil, err
				}
				result.Events = append(result.Events, p)
			}
			out.Result = &resourcepb.AncillaryResponse_Events{Events: result}
		}
	default:
		return nil, unsupported("unknown ancillary operation")
	}
	return out, nil
}
