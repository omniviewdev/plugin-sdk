package plugin

import (
	"context"

	"google.golang.org/grpc"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

// Client adapts remote resource operations to domain interfaces. Its method set
// does not imply optional provider support; negotiate Features before using it.
type Client struct{ conn grpc.ClientConnInterface }

// NewClient binds resource operations to an existing gRPC connection.
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
	if err := c.conn.Invoke(
		ctx,
		resourcepb.ResourceProvider_Call_FullMethodName,
		r,
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
		return nil, unsupported("unsupported response wire version")
	}
	return out, nil
}

// Catalog discovers the provider's catalog for the explicit route.
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

// finiteClientOperation retains admission before encoding and validates decoded
// results against that same descriptor, never a newly fetched catalog.
func finiteClientOperation[I, P, O any](
	ctx context.Context,
	c *Client,
	in I,
	encode func(I) (P, error),
	wrap func(P) *resourcepb.Request,
	decode func(*resourcepb.Response) (O, error),
) (O, error) {
	var zero O
	descriptor, err := resourcev2.AdmitOperation(ctx, c, in)
	if err != nil {
		return zero, err
	}
	var actionDescriptor *resourcev2.ActionDescriptor
	if action, ok := any(in).(resourcev2.ActionRequest); ok && action.ExpectedDescriptor != nil {
		actionDescriptor, err = resourcev2.ResolveActionDescriptor(ctx, c, action, false)
		if err != nil { return zero, err }
	}
	p, err := encode(in)
	if err != nil {
		return zero, err
	}
	out, err := c.call(ctx, wrap(p))
	if err != nil {
		return zero, err
	}
	value, err := decode(out)
	if err == nil {
		err = resourcev2.ValidateOperationResult(in, value, descriptor)
	}
	if err == nil {
		if _, ok := any(in).(resourcev2.ActionRequest); ok {
			output, valid := any(value).(resourcev2.Value)
			if !valid { return zero, invalid("unexpected action output") }
			err = resourcev2.ValidateResolvedActionOutput(actionDescriptor, output)
		}
	}
	if err != nil {
		return zero, err
	}
	return value, nil
}

func decodeRecordResponse(out *resourcepb.Response) (resourcev2.Record, error) {
	if out.GetRecord() == nil {
		return resourcev2.Record{}, invalid("unexpected response result")
	}
	return resourcev2.FromProtoRecord(out.GetRecord())
}

func decodePageResponse(out *resourcepb.Response) (resourcev2.SourcePage, error) {
	if out.GetPage() == nil {
		return resourcev2.SourcePage{}, invalid("unexpected response result")
	}
	return resourcev2.FromProtoSourcePage(out.GetPage())
}

func decodeValueResponse(out *resourcepb.Response) (resourcev2.Value, error) {
	if out.GetValue() == nil {
		return resourcev2.Value{}, invalid("unexpected response result")
	}
	return resourcev2.FromProtoValue(out.GetValue())
}

// Get reads an explicit representation and validates its admitted provenance.
func (c *Client) Get(ctx context.Context, in resourcev2.GetRequest) (resourcev2.Record, error) {
	return finiteClientOperation(ctx, c, in, resourcev2.ToProtoGetRequest,
		func(p *resourcepb.GetInput) *resourcepb.Request {
			return &resourcepb.Request{Operation: &resourcepb.Request_Get{Get: p}}
		}, decodeRecordResponse)
}

// Enumerate acquires a bounded source page under the accepted catalog observation.
func (c *Client) Enumerate(ctx context.Context, in resourcev2.EnumerateRequest) (resourcev2.SourcePage, error) {
	return finiteClientOperation(ctx, c, in, resourcev2.ToProtoEnumerateRequest,
		func(p *resourcepb.EnumerateInput) *resourcepb.Request {
			return &resourcepb.Request{Operation: &resourcepb.Request_Enumerate{Enumerate: p}}
		}, decodePageResponse)
}

// NativeQuery executes the provider's native query contract and validates its page.
func (c *Client) NativeQuery(ctx context.Context, in resourcev2.NativeQueryRequest) (resourcev2.SourcePage, error) {
	return finiteClientOperation(ctx, c, in, resourcev2.ToProtoNativeQueryRequest,
		func(p *resourcepb.NativeQueryInput) *resourcepb.Request {
			return &resourcepb.Request{Operation: &resourcepb.Request_NativeQuery{NativeQuery: p}}
		}, decodePageResponse)
}

// Mutate admits the exact requested write verb and validates the returned record.
func (c *Client) Mutate(ctx context.Context, in resourcev2.MutationRequest) (resourcev2.Record, error) {
	return finiteClientOperation(ctx, c, in, resourcev2.ToProtoMutationRequest,
		func(p *resourcepb.MutationInput) *resourcepb.Request {
			return &resourcepb.Request{Operation: &resourcepb.Request_Mutate{Mutate: p}}
		}, decodeRecordResponse)
}

// Action invokes a finite provider action under the accepted catalog observation.
func (c *Client) Action(ctx context.Context, in resourcev2.ActionRequest) (resourcev2.Value, error) {
	return finiteClientOperation(ctx, c, in, resourcev2.ToProtoActionRequest,
		func(p *resourcepb.ActionInput) *resourcepb.Request {
			return &resourcepb.Request{Operation: &resourcepb.Request_Action{Action: p}}
		}, decodeValueResponse)
}
