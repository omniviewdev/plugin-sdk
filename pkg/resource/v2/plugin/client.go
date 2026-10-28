package plugin

import (
	"context"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	"google.golang.org/grpc"
)

type Client struct{ conn grpc.ClientConnInterface }

func NewClient(c grpc.ClientConnInterface) *Client { return &Client{conn: c} }
func (c *Client) call(ctx context.Context, r *resourcepb.Request) (*resourcepb.Response, error) {
	var err error
	ctx, err = outgoingSession(ctx)
	if err != nil {
		return nil, err
	}
	r.Version = resourcev2.WireVersion
	out := &resourcepb.Response{}
	e := &envelope{message: out}
	if err := c.conn.Invoke(ctx, resourcepb.ResourceProvider_Call_FullMethodName, r, e, grpc.MaxCallRecvMsgSize(MaxWireBytes), grpc.MaxCallSendMsgSize(MaxWireBytes)); err != nil {
		return nil, domainError(err)
	}
	if e.err != nil {
		return nil, e.err
	}
	if out.Version != resourcev2.WireVersion {
		return nil, unsupported("unsupported response wire version")
	}
	return out, nil
}
func (c *Client) Catalog(ctx context.Context, in resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
	p, err := resourcev2.ToProtoCatalogRequest(in)
	if err != nil {
		return resourcev2.Catalog{}, err
	}
	out, err := c.call(ctx, &resourcepb.Request{Operation: &resourcepb.Request_Catalog{Catalog: p}})
	if err != nil {
		return resourcev2.Catalog{}, err
	}
	if out.GetCatalog() == nil {
		return resourcev2.Catalog{}, invalid("unexpected response result")
	}
	return resourcev2.FromProtoCatalog(out.GetCatalog())
}
func (c *Client) Get(ctx context.Context, in resourcev2.GetRequest) (resourcev2.Record, error) {
	descriptor, err := resourcev2.AdmitOperation(ctx, c, in)
	if err != nil {
		return resourcev2.Record{}, err
	}
	p, err := resourcev2.ToProtoGetRequest(in)
	if err != nil {
		return resourcev2.Record{}, err
	}
	out, err := c.call(ctx, &resourcepb.Request{Operation: &resourcepb.Request_Get{Get: p}})
	if err != nil {
		return resourcev2.Record{}, err
	}
	if out.GetRecord() == nil {
		return resourcev2.Record{}, invalid("unexpected response result")
	}
	value, err := resourcev2.FromProtoRecord(out.GetRecord())
	if err == nil {
		err = resourcev2.ValidateOperationResult(in, value, descriptor)
	}
	if err != nil {
		return resourcev2.Record{}, err
	}
	return value, nil
}
func (c *Client) Enumerate(ctx context.Context, in resourcev2.EnumerateRequest) (resourcev2.SourcePage, error) {
	descriptor, err := resourcev2.AdmitOperation(ctx, c, in)
	if err != nil {
		return resourcev2.SourcePage{}, err
	}
	p, err := resourcev2.ToProtoEnumerateRequest(in)
	if err != nil {
		return resourcev2.SourcePage{}, err
	}
	out, err := c.call(ctx, &resourcepb.Request{Operation: &resourcepb.Request_Enumerate{Enumerate: p}})
	if err != nil {
		return resourcev2.SourcePage{}, err
	}
	if out.GetPage() == nil {
		return resourcev2.SourcePage{}, invalid("unexpected response result")
	}
	value, err := resourcev2.FromProtoSourcePage(out.GetPage())
	if err == nil {
		err = resourcev2.ValidateOperationResult(in, value, descriptor)
	}
	if err != nil {
		return resourcev2.SourcePage{}, err
	}
	return value, nil
}
func (c *Client) NativeQuery(ctx context.Context, in resourcev2.NativeQueryRequest) (resourcev2.SourcePage, error) {
	descriptor, err := resourcev2.AdmitOperation(ctx, c, in)
	if err != nil {
		return resourcev2.SourcePage{}, err
	}
	p, err := resourcev2.ToProtoNativeQueryRequest(in)
	if err != nil {
		return resourcev2.SourcePage{}, err
	}
	out, err := c.call(ctx, &resourcepb.Request{Operation: &resourcepb.Request_NativeQuery{NativeQuery: p}})
	if err != nil {
		return resourcev2.SourcePage{}, err
	}
	if out.GetPage() == nil {
		return resourcev2.SourcePage{}, invalid("unexpected response result")
	}
	value, err := resourcev2.FromProtoSourcePage(out.GetPage())
	if err == nil {
		err = resourcev2.ValidateOperationResult(in, value, descriptor)
	}
	if err != nil {
		return resourcev2.SourcePage{}, err
	}
	return value, nil
}
func (c *Client) Mutate(ctx context.Context, in resourcev2.MutationRequest) (resourcev2.Record, error) {
	descriptor, err := resourcev2.AdmitOperation(ctx, c, in)
	if err != nil {
		return resourcev2.Record{}, err
	}
	p, err := resourcev2.ToProtoMutationRequest(in)
	if err != nil {
		return resourcev2.Record{}, err
	}
	out, err := c.call(ctx, &resourcepb.Request{Operation: &resourcepb.Request_Mutate{Mutate: p}})
	if err != nil {
		return resourcev2.Record{}, err
	}
	if out.GetRecord() == nil {
		return resourcev2.Record{}, invalid("unexpected response result")
	}
	value, err := resourcev2.FromProtoRecord(out.GetRecord())
	if err == nil {
		err = resourcev2.ValidateOperationResult(in, value, descriptor)
	}
	if err != nil {
		return resourcev2.Record{}, err
	}
	return value, nil
}
func (c *Client) Action(ctx context.Context, in resourcev2.ActionRequest) (resourcev2.Value, error) {
	descriptor, err := resourcev2.AdmitOperation(ctx, c, in)
	if err != nil {
		return resourcev2.Value{}, err
	}
	p, err := resourcev2.ToProtoActionRequest(in)
	if err != nil {
		return resourcev2.Value{}, err
	}
	out, err := c.call(ctx, &resourcepb.Request{Operation: &resourcepb.Request_Action{Action: p}})
	if err != nil {
		return resourcev2.Value{}, err
	}
	if out.GetValue() == nil {
		return resourcev2.Value{}, invalid("unexpected response result")
	}
	value, err := resourcev2.FromProtoValue(out.GetValue())
	if err == nil {
		err = resourcev2.ValidateOperationResult(in, value, descriptor)
	}
	if err != nil {
		return resourcev2.Value{}, err
	}
	return value, nil
}
