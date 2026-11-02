package plugin

import (
	"context"
	"encoding/json"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

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
	return &resourcepb.AncillaryResponse{
		Version: resourcev2.WireVersion,
		Result:  &resourcepb.AncillaryResponse_Json{Json: data},
	}, nil
}

// presentationCall preserves operation-specific target and schema validation.
// The shared ancillaryCallHandler owns admission and response validation.
func (s *ancillaryServer) presentationCall(
	ctx context.Context,
	r *resourcepb.AncillaryRequest,
	route resourcev2.AccessRoute,
) (*resourcepb.AncillaryResponse, error) {
	switch r.Operation {
	case ancillaryScopeValues:
		return s.scopeDiscovery(ctx, r, route)
	case ancillarySchemaDocument:
		return s.schemaDocument(ctx, r, route)
	case ancillaryGroups, ancillaryDefinition:
		return s.resourcePresentation(ctx, r, route)
	case ancillaryEditorSchemas:
		return s.editorSchemas(ctx, route)
	case ancillaryActions:
		return s.actionCatalog(ctx, r, route)
	default:
		return nil, unsupported("unknown ancillary operation")
	}
}

func (s *ancillaryServer) scopeDiscovery(
	ctx context.Context,
	r *resourcepb.AncillaryRequest,
	route resourcev2.AccessRoute,
) (*resourcepb.AncillaryResponse, error) {
	impl := s.owner.impl
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
}

func (s *ancillaryServer) schemaDocument(
	ctx context.Context,
	r *resourcepb.AncillaryRequest,
	route resourcev2.AccessRoute,
) (*resourcepb.AncillaryResponse, error) {
	impl := s.owner.impl
	out := &resourcepb.AncillaryResponse{Version: resourcev2.WireVersion}
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
	return out, nil
}

func (s *ancillaryServer) resourcePresentation(
	ctx context.Context,
	r *resourcepb.AncillaryRequest,
	route resourcev2.AccessRoute,
) (*resourcepb.AncillaryResponse, error) {
	impl := s.owner.impl
	if route.Connection == "" {
		return nil, invalid("missing access route")
	}
	p, ok := impl.(resourcev2.PresentationProvider)
	if !ok {
		return nil, unsupported("presentation unsupported")
	}
	if r.Operation == ancillaryGroups {
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
}

func (s *ancillaryServer) editorSchemas(
	ctx context.Context,
	route resourcev2.AccessRoute,
) (*resourcepb.AncillaryResponse, error) {
	impl := s.owner.impl
	out := &resourcepb.AncillaryResponse{Version: resourcev2.WireVersion}
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
	if err = resourcev2.ValidateEditorSchemaList(v); err != nil {
		return nil, err
	}
	result := &resourcepb.EditorSchemas{}
	for i := range v {
		schema, err := resourcev2.ToProtoEditorSchema(v[i])
		if err != nil {
			return nil, err
		}
		result.Schemas = append(result.Schemas, schema)
	}
	out.Result = &resourcepb.AncillaryResponse_EditorSchemas{EditorSchemas: result}
	return out, nil
}

func (s *ancillaryServer) actionCatalog(
	ctx context.Context,
	r *resourcepb.AncillaryRequest,
	route resourcev2.AccessRoute,
) (*resourcepb.AncillaryResponse, error) {
	impl := s.owner.impl
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
}
