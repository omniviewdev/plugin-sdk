package plugin

import (
	"context"
	"encoding/json"

	"google.golang.org/grpc"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

func (c *Client) ancillary(
	ctx context.Context,
	in *resourcepb.AncillaryRequest,
) (*resourcepb.AncillaryResponse, error) {
	var err error
	ctx, err = outgoingSession(ctx)
	if err != nil {
		return nil, err
	}
	in.Version = resourcev2.WireVersion
	out := &resourcepb.AncillaryResponse{}
	e := &envelope{message: out}
	if err := c.conn.Invoke(
		ctx,
		resourcepb.ResourceAncillary_Call_FullMethodName,
		in,
		e,
		grpc.MaxCallRecvMsgSize(MaxWireBytes),
		grpc.MaxCallSendMsgSize(MaxWireBytes),
	); err != nil {
		return nil, domainError(err)
	}
	if e.err != nil {
		return nil, e.err
	}
	if out.Version != resourcev2.WireVersion {
		return nil, unsupported("unsupported ancillary response version")
	}
	return out, nil
}

func (c *Client) ancillaryJSON(ctx context.Context, in *resourcepb.AncillaryRequest, out any) error {
	r, err := c.ancillary(ctx, in)
	if err != nil {
		return err
	}
	p, ok := r.Result.(*resourcepb.AncillaryResponse_Json)
	if !ok {
		return invalid("unexpected ancillary result")
	}
	if err = decodeConnection(p.Json, out); err != nil {
		return err
	}
	switch v := out.(type) {
	case *resourcev2.ResourceDefinition:
		return resourcev2.ValidateResourceDefinition(*v)
	case *[]resourcev2.ResourceGroup:
		return resourcev2.ValidateResourceGroups(*v)
	case *[]resourcev2.ActionDescriptor:
		return resourcev2.ValidateActionDescriptors(*v)
	}
	return nil
}

func ancillaryRoute(route resourcev2.AccessRoute) (*resourcepb.AccessRoute, error) {
	return resourcev2.ToProtoAccessRoute(route)
}

func (c *Client) ListConnections(ctx context.Context) ([]types.Connection, error) {
	var out []types.Connection
	err := c.ancillaryJSON(ctx, &resourcepb.AncillaryRequest{Operation: "listConnections"}, &out)
	return out, err
}

func (c *Client) ScopeValues(ctx context.Context, route resourcev2.AccessRoute, dimension string) ([]string, error) {
	var out []string
	p, err := ancillaryRoute(route)
	if err != nil {
		return nil, err
	}
	err = c.ancillaryJSON(ctx, &resourcepb.AncillaryRequest{Operation: "scopeValues", Route: p, Type: dimension}, &out)
	return out, err
}

func (c *Client) UpdateConnection(ctx context.Context, in types.Connection) (types.Connection, error) {
	var out types.Connection
	if err := resourcev2.ValidateMetadata(in); err != nil {
		return out, err
	}
	data, err := json.Marshal(in)
	if err != nil {
		return out, invalid("invalid connection JSON")
	}
	if err = resourcev2.ValidateJSON(data); err != nil {
		return out, err
	}
	err = c.ancillaryJSON(ctx, &resourcepb.AncillaryRequest{Operation: "updateConnection", ConnectionJson: data}, &out)
	return out, err
}

func (c *Client) DeleteConnection(ctx context.Context, id string) error {
	return c.ancillaryJSON(ctx, &resourcepb.AncillaryRequest{Operation: "deleteConnection", Id: id}, &struct{}{})
}

func (c *Client) GetResourceGroups(
	ctx context.Context,
	route resourcev2.AccessRoute,
) ([]resourcev2.ResourceGroup, error) {
	var out []resourcev2.ResourceGroup
	p, err := ancillaryRoute(route)
	if err != nil {
		return out, err
	}
	err = c.ancillaryJSON(ctx, &resourcepb.AncillaryRequest{Operation: "groups", Route: p}, &out)
	return out, err
}

func (c *Client) GetResourceDefinition(
	ctx context.Context,
	route resourcev2.AccessRoute,
	key string,
) (resourcev2.ResourceDefinition, error) {
	var out resourcev2.ResourceDefinition
	p, err := ancillaryRoute(route)
	if err != nil {
		return out, err
	}
	err = c.ancillaryJSON(ctx, &resourcepb.AncillaryRequest{Operation: "definition", Route: p, Type: key}, &out)
	return out, err
}

func (c *Client) GetEditorSchemas(
	ctx context.Context,
	route resourcev2.AccessRoute,
) ([]resourcev2.EditorSchema, error) {
	p, err := ancillaryRoute(route)
	if err != nil {
		return nil, err
	}
	r, err := c.ancillary(ctx, &resourcepb.AncillaryRequest{Operation: "editorSchemas", Route: p})
	if err != nil {
		return nil, err
	}
	if r.GetEditorSchemas() == nil {
		return nil, invalid("unexpected editor schemas result")
	}
	out := []resourcev2.EditorSchema{}
	for _, item := range r.GetEditorSchemas().Schemas {
		schema, err := resourcev2.FromProtoEditorSchema(item)
		if err != nil {
			return nil, err
		}
		out = append(out, schema)
	}
	return out, nil
}

func (c *Client) GetActions(
	ctx context.Context,
	route resourcev2.AccessRoute,
	key string,
) ([]resourcev2.ActionDescriptor, error) {
	var out []resourcev2.ActionDescriptor
	p, err := ancillaryRoute(route)
	if err != nil {
		return out, err
	}
	err = c.ancillaryJSON(ctx, &resourcepb.AncillaryRequest{Operation: "actions", Route: p, Type: key}, &out)
	return out, err
}

func (c *Client) GetRelationships(
	ctx context.Context,
	route resourcev2.AccessRoute,
	key string,
) ([]resourcev2.RelationshipDescriptor, error) {
	p, err := ancillaryRoute(route)
	if err != nil {
		return nil, err
	}
	r, err := c.ancillary(ctx, &resourcepb.AncillaryRequest{Operation: "relationships", Route: p, Type: key})
	if err != nil {
		return nil, err
	}
	if r.GetRelationships() == nil {
		return nil, invalid("unexpected relationship result")
	}
	out := []resourcev2.RelationshipDescriptor{}
	for _, item := range r.GetRelationships().Descriptors {
		v, err := resourcev2.FromProtoRelationshipDescriptor(item)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (c *Client) ResolveRelationships(
	ctx context.Context,
	in resourcev2.RelationshipResolutionRequest,
) ([]resourcev2.ResolvedRelationship, error) {
	p, err := resourcev2.ToProtoRelationshipResolutionRequest(in)
	if err != nil {
		return nil, err
	}
	r, err := c.ancillary(
		ctx,
		&resourcepb.AncillaryRequest{Operation: "resolveRelationships", RelationshipResolution: p},
	)
	if err != nil {
		return nil, err
	}
	if r.GetResolved() == nil {
		return nil, invalid("unexpected resolved relationship result")
	}
	out := []resourcev2.ResolvedRelationship{}
	for _, item := range r.GetResolved().Relationships {
		v, err := resourcev2.FromProtoResolvedRelationship(item)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := resourcev2.ValidateRelationshipResolutionResult(in, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) GetHealth(
	ctx context.Context,
	in resourcev2.GetRequest,
	body resourcev2.Value,
) (*resourcev2.ResourceHealth, error) {
	p, err := resourcev2.ToProtoGetRequest(in)
	if err != nil {
		return nil, err
	}
	v, err := resourcev2.ToProtoValue(body)
	if err != nil {
		return nil, err
	}
	r, err := c.ancillary(ctx, &resourcepb.AncillaryRequest{Operation: "health", Get: p, Value: v})
	if err != nil {
		return nil, err
	}
	if r.GetHealth() == nil {
		return nil, invalid("unexpected health result")
	}
	if r.GetHealth().Health == nil {
		return nil, nil
	}
	out, err := resourcev2.FromProtoResourceHealth(r.GetHealth().Health)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetResourceEvents(
	ctx context.Context,
	in resourcev2.GetRequest,
	limit uint32,
) ([]resourcev2.ResourceEvent, error) {
	if limit > MaxPageSize {
		return nil, limited("event limit exceeds bound")
	}
	p, err := resourcev2.ToProtoGetRequest(in)
	if err != nil {
		return nil, err
	}
	r, err := c.ancillary(ctx, &resourcepb.AncillaryRequest{Operation: "events", Get: p, Limit: limit})
	if err != nil {
		return nil, err
	}
	if r.GetEvents() == nil {
		return nil, invalid("unexpected event result")
	}
	if len(r.GetEvents().Events) > MaxPageSize || limit > 0 && len(r.GetEvents().Events) > int(limit) {
		return nil, limited("event count exceeds bound")
	}
	out := []resourcev2.ResourceEvent{}
	for _, item := range r.GetEvents().Events {
		v, err := resourcev2.FromProtoResourceEvent(item)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (c *Client) GetSchemaDocument(
	ctx context.Context,
	route resourcev2.AccessRoute,
	reference resourcev2.SchemaDocumentReference,
) (resourcev2.SchemaDocument, error) {
	p, err := ancillaryRoute(route)
	if err != nil {
		return resourcev2.SchemaDocument{}, err
	}
	ref, err := resourcev2.ToProtoSchemaDocumentReference(reference)
	if err != nil {
		return resourcev2.SchemaDocument{}, err
	}
	r, err := c.ancillary(ctx, &resourcepb.AncillaryRequest{Operation: "schemaDocument", Route: p, DocumentRef: ref})
	if err != nil {
		return resourcev2.SchemaDocument{}, err
	}
	if r.GetDocument() == nil {
		return resourcev2.SchemaDocument{}, invalid("unexpected schema document result")
	}
	out, err := resourcev2.FromProtoSchemaDocument(r.GetDocument())
	if err != nil {
		return resourcev2.SchemaDocument{}, err
	}
	if out.Reference != reference {
		return resourcev2.SchemaDocument{}, invalid("schema document reference mismatch")
	}
	return out, nil
}
