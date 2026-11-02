// Package plugin transports resource v2 providers over bounded, validated gRPC messages.
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

// ListConnections returns configured connection snapshots without starting native clients.
func (c *Client) ListConnections(ctx context.Context) ([]types.Connection, error) {
	var out []types.Connection
	err := c.ancillaryJSON(ctx, &resourcepb.AncillaryRequest{Operation: ancillaryListConnections}, &out)
	return out, err
}

// ScopeValues discovers values for one scope dimension on the explicit route.
func (c *Client) ScopeValues(ctx context.Context, route resourcev2.AccessRoute, dimension string) ([]string, error) {
	var out []string
	p, err := ancillaryRoute(route)
	if err != nil {
		return nil, err
	}
	err = c.ancillaryJSON(
		ctx,
		&resourcepb.AncillaryRequest{Operation: ancillaryScopeValues, Route: p, Type: dimension},
		&out,
	)
	return out, err
}

// UpdateConnection updates one configured connection after validating its wire metadata.
func (c *Client) UpdateConnection(ctx context.Context, in types.Connection) (types.Connection, error) {
	var out types.Connection
	if err := resourcev2.ValidateMetadata(in); err != nil {
		return out, err
	}
	// Connection JSON uses the existing public connection serialization contract.
	//nolint:musttag // types.Connection owns its established wire field names.
	data, err := json.Marshal(in)
	if err != nil {
		return out, invalid("invalid connection JSON")
	}
	if err = resourcev2.ValidateJSON(data); err != nil {
		return out, err
	}
	err = c.ancillaryJSON(
		ctx,
		&resourcepb.AncillaryRequest{Operation: ancillaryUpdateConnection, ConnectionJson: data},
		&out,
	)
	return out, err
}

// DeleteConnection removes the configured connection identified by id.
func (c *Client) DeleteConnection(ctx context.Context, id string) error {
	return c.ancillaryJSON(ctx, &resourcepb.AncillaryRequest{Operation: ancillaryDeleteConnection, Id: id}, &struct{}{})
}

// GetResourceGroups returns validated presentation groups for the selected route.
func (c *Client) GetResourceGroups(
	ctx context.Context,
	route resourcev2.AccessRoute,
) ([]resourcev2.ResourceGroup, error) {
	var out []resourcev2.ResourceGroup
	p, err := ancillaryRoute(route)
	if err != nil {
		return out, err
	}
	err = c.ancillaryJSON(ctx, &resourcepb.AncillaryRequest{Operation: ancillaryGroups, Route: p}, &out)
	return out, err
}

// GetResourceDefinition returns the validated presentation definition for one resource type.
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
	err = c.ancillaryJSON(ctx, &resourcepb.AncillaryRequest{Operation: ancillaryDefinition, Route: p, Type: key}, &out)
	return out, err
}

// GetEditorSchemas returns editor schema bindings for the selected route.
func (c *Client) GetEditorSchemas(
	ctx context.Context,
	route resourcev2.AccessRoute,
) ([]resourcev2.EditorSchema, error) {
	p, err := ancillaryRoute(route)
	if err != nil {
		return nil, err
	}
	r, err := c.ancillary(ctx, &resourcepb.AncillaryRequest{Operation: ancillaryEditorSchemas, Route: p})
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
	if err = resourcev2.ValidateEditorSchemaList(out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetActions returns validated action descriptors for one resource type.
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
	err = c.ancillaryJSON(ctx, &resourcepb.AncillaryRequest{Operation: ancillaryActions, Route: p, Type: key}, &out)
	return out, err
}

// GetRelationships returns relationship descriptors for one resource type.
func (c *Client) GetRelationships(
	ctx context.Context,
	route resourcev2.AccessRoute,
	key string,
) ([]resourcev2.RelationshipDescriptor, error) {
	p, err := ancillaryRoute(route)
	if err != nil {
		return nil, err
	}
	r, err := c.ancillary(ctx, &resourcepb.AncillaryRequest{Operation: ancillaryRelationships, Route: p, Type: key})
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

// ResolveRelationships resolves links within the explicitly admitted source and target bindings.
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
		&resourcepb.AncillaryRequest{Operation: ancillaryResolveRelationships, RelationshipResolution: p},
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

// GetHealth returns optional health for the resource body; nil health means no health result.
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
	r, err := c.ancillary(ctx, &resourcepb.AncillaryRequest{Operation: ancillaryHealth, Get: p, Value: v})
	if err != nil {
		return nil, err
	}
	if r.GetHealth() == nil {
		return nil, invalid("unexpected health result")
	}
	if r.GetHealth().Health == nil {
		//nolint:nilnil // Absent optional health is a successful result, not a provider failure.
		return nil, nil
	}
	out, err := resourcev2.FromProtoResourceHealth(r.GetHealth().Health)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetResourceEvents returns resource events within both the requested and transport limits.
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
	r, err := c.ancillary(ctx, &resourcepb.AncillaryRequest{Operation: ancillaryEvents, Get: p, Limit: limit})
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

// GetSchemaDocument fetches the exact schema document reference without substituting another revision.
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
	r, err := c.ancillary(
		ctx,
		&resourcepb.AncillaryRequest{Operation: ancillarySchemaDocument, Route: p, DocumentRef: ref},
	)
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
