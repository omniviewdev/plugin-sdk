// Package hostconnections provides invocation-scoped access to host-owned connections.
package hostconnections

import (
	"context"
	"errors"
	"slices"
	"sync"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	rp "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	pb "github.com/omniviewdev/plugin-sdk/proto/v1/hostconnections"
	"google.golang.org/grpc"
)

const Version uint32 = 1

var ErrHostConnectionsUnavailable = errors.New("host connections service is unavailable")

type ResolveTargetRequest struct {
	Route          resourcev2.AccessRoute
	Type           string
	Scope          resourcev2.QueryScope
	Representation string
}
type ResolvedTarget struct {
	TargetID string
	Target   resourcev2.ReadTarget
}
type Client interface {
	Catalog(context.Context, string, resourcev2.CatalogRequest) (resourcev2.Catalog, error)
	ResolveTarget(context.Context, string, ResolveTargetRequest) (ResolvedTarget, error)
	GetSchemaDocument(context.Context, string, resourcev2.AccessRoute, resourcev2.SchemaDocumentReference) (resourcev2.SchemaDocument, error)
	Get(context.Context, string, string, resourcev2.GetRequest) (resourcev2.Record, error)
	Enumerate(context.Context, string, string, resourcev2.EnumerateRequest) (resourcev2.SourcePage, error)
	Update(context.Context, string, string, resourcev2.MutationRequest) (resourcev2.Record, error)
}
type Service interface {
	Catalog(context.Context, []byte, string, resourcev2.CatalogRequest) (resourcev2.Catalog, error)
	ResolveTarget(context.Context, []byte, string, ResolveTargetRequest) (ResolvedTarget, error)
	GetSchemaDocument(context.Context, []byte, string, resourcev2.AccessRoute, resourcev2.SchemaDocumentReference) (resourcev2.SchemaDocument, error)
	Get(context.Context, []byte, string, string, resourcev2.GetRequest) (resourcev2.Record, error)
	Enumerate(context.Context, []byte, string, string, resourcev2.EnumerateRequest) (resourcev2.SourcePage, error)
	Update(context.Context, []byte, string, string, resourcev2.MutationRequest) (resourcev2.Record, error)
}
type contextKey struct{}

func FromContext(ctx context.Context) (Client, error) {
	if ctx != nil {
		if c, ok := ctx.Value(contextKey{}).(Client); ok && c != nil {
			return c, nil
		}
	}
	return nil, ErrHostConnectionsUnavailable
}

type Binding struct {
	mu    sync.Mutex
	conn  *grpc.ClientConn
	bound bool
}

func (b *Binding) Bind(conn *grpc.ClientConn) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.bound || conn == nil {
		return ErrHostConnectionsUnavailable
	}
	b.conn = conn
	b.bound = true
	return nil
}
func (b *Binding) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn != nil {
		err := b.conn.Close()
		b.conn = nil
		return err
	}
	return nil
}
func (b *Binding) Context(ctx context.Context, owner string, route resourcev2.AccessRoute, token []byte) (context.Context, error) {
	if b == nil || len(token) == 0 || owner == "" || ctx == nil {
		return nil, ErrHostConnectionsUnavailable
	}
	if _, err := resourcev2.ToProtoAccessRoute(route); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn == nil {
		return nil, ErrHostConnectionsUnavailable
	}
	return context.WithValue(ctx, contextKey{}, &client{rpc: pb.NewHostConnectionsClient(b.conn), token: append([]byte(nil), token...), owner: owner, route: route}), nil
}

type client struct {
	rpc   pb.HostConnectionsClient
	token []byte
	owner string
	route resourcev2.AccessRoute
}

func invalid() error {
	return resourcev2.NewError("INVALID_REQUEST", "Invalid host connection request.")
}
func (c *client) call(ctx context.Context, owner, target string, op any) (*pb.CallResponse, error) {
	if owner != c.owner {
		return nil, invalid()
	}
	r := &pb.CallRequest{OwnerPlugin: owner, DelegationToken: c.token, TargetId: target}
	switch v := op.(type) {
	case *resourcepb.Request:
		r.Operation = &pb.CallRequest_Resource{Resource: v}
	case *pb.ResolveTargetInput:
		r.Operation = &pb.CallRequest_ResolveTarget{ResolveTarget: v}
	case *pb.SchemaDocumentInput:
		r.Operation = &pb.CallRequest_SchemaDocument{SchemaDocument: v}
	default:
		return nil, invalid()
	}
	opt, admission := rp.ResponseAdmission()
	out, err := c.rpc.Call(ctx, r, opt)
	if err != nil {
		return nil, rp.ResourceDomainError(err)
	}
	if err = admission(); err != nil {
		return nil, err
	}
	return out, nil
}
func (c *client) check(route resourcev2.AccessRoute) error {
	if route != c.route {
		return invalid()
	}
	_, err := resourcev2.ToProtoAccessRoute(route)
	return err
}
func (c *client) Catalog(ctx context.Context, owner string, in resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
	var z resourcev2.Catalog
	if err := c.check(in.Route); err != nil {
		return z, err
	}
	p, err := resourcev2.ToProtoCatalogRequest(in)
	if err != nil {
		return z, err
	}
	out, err := c.call(ctx, owner, "", &resourcepb.Request{Version: resourcev2.WireVersion, Operation: &resourcepb.Request_Catalog{Catalog: p}})
	if err != nil {
		return z, err
	}
	r := out.GetResource()
	if r == nil || r.GetVersion() != resourcev2.WireVersion || r.GetCatalog() == nil {
		return z, invalid()
	}
	return resourcev2.FromProtoCatalog(r.GetCatalog())
}
func (c *client) ResolveTarget(ctx context.Context, owner string, in ResolveTargetRequest) (ResolvedTarget, error) {
	var z ResolvedTarget
	if err := c.check(in.Route); err != nil {
		return z, err
	}
	route, err := resourcev2.ToProtoAccessRoute(in.Route)
	if err != nil {
		return z, err
	}
	scope, err := resourcev2.ToProtoQueryScope(in.Scope)
	if err != nil {
		return z, err
	}
	if in.Type == "" || in.Representation == "" {
		return z, invalid()
	}
	out, err := c.call(ctx, owner, "", &pb.ResolveTargetInput{Route: route, Type: in.Type, Scope: scope, Representation: in.Representation})
	if err != nil {
		return z, err
	}
	p := out.GetResolvedTarget()
	if p == nil || p.GetTargetId() == "" {
		return z, invalid()
	}
	t, err := resourcev2.FromProtoReadTarget(p.GetTarget())
	if err != nil {
		return z, err
	}
	if t.Route != c.route || t.Type != in.Type || t.Representation != in.Representation {
		return z, invalid()
	}
	return ResolvedTarget{TargetID: p.GetTargetId(), Target: t}, nil
}
func (c *client) GetSchemaDocument(ctx context.Context, owner string, route resourcev2.AccessRoute, ref resourcev2.SchemaDocumentReference) (resourcev2.SchemaDocument, error) {
	var z resourcev2.SchemaDocument
	if err := c.check(route); err != nil {
		return z, err
	}
	r, err := resourcev2.ToProtoAccessRoute(route)
	if err != nil {
		return z, err
	}
	p, err := resourcev2.ToProtoSchemaDocumentReference(ref)
	if err != nil {
		return z, err
	}
	out, err := c.call(ctx, owner, "", &pb.SchemaDocumentInput{Route: r, Reference: p})
	if err != nil {
		return z, err
	}
	if out.GetSchemaDocument() == nil {
		return z, invalid()
	}
	d, err := resourcev2.FromProtoSchemaDocument(out.GetSchemaDocument())
	if err != nil {
		return z, err
	}
	if d.Reference != ref {
		return z, invalid()
	}
	return d, nil
}

func sameReference(got, requested resourcev2.ResourceReference) bool {
	address, expected := got.Address, requested.Address
	if address.Provider != expected.Provider || address.Authority != expected.Authority ||
		address.Type != expected.Type || address.ID != expected.ID || len(address.Scope) != len(expected.Scope) {
		return false
	}
	for _, entry := range address.Scope {
		if !slices.Contains(expected.Scope, entry) {
			return false
		}
	}
	return requested.Incarnation == nil ||
		(got.Incarnation != nil && *got.Incarnation == *requested.Incarnation)
}

func (c *client) Get(ctx context.Context, owner, target string, in resourcev2.GetRequest) (resourcev2.Record, error) {
	var z resourcev2.Record
	if target == "" {
		return z, invalid()
	}
	if err := c.check(in.Route); err != nil {
		return z, err
	}
	p, err := resourcev2.ToProtoGetRequest(in)
	if err != nil {
		return z, err
	}
	out, err := c.call(ctx, owner, target, &resourcepb.Request{Version: resourcev2.WireVersion, Operation: &resourcepb.Request_Get{Get: p}})
	if err != nil {
		return z, err
	}
	r := out.GetResource()
	if r == nil || r.GetVersion() != resourcev2.WireVersion || r.GetRecord() == nil {
		return z, invalid()
	}
	record, err := resourcev2.FromProtoRecord(r.GetRecord())
	if err != nil {
		return z, err
	}
	if !sameReference(record.Reference, in.Reference) {
		return z, invalid()
	}
	return record, nil
}
func (c *client) Enumerate(ctx context.Context, owner, target string, in resourcev2.EnumerateRequest) (resourcev2.SourcePage, error) {
	var z resourcev2.SourcePage
	if target == "" {
		return z, invalid()
	}
	if err := c.check(in.Target.Route); err != nil {
		return z, err
	}
	p, err := resourcev2.ToProtoEnumerateRequest(in)
	if err != nil {
		return z, err
	}
	out, err := c.call(ctx, owner, target, &resourcepb.Request{Version: resourcev2.WireVersion, Operation: &resourcepb.Request_Enumerate{Enumerate: p}})
	if err != nil {
		return z, err
	}
	r := out.GetResource()
	if r == nil || r.GetVersion() != resourcev2.WireVersion || r.GetPage() == nil {
		return z, invalid()
	}
	return resourcev2.FromProtoSourcePage(r.GetPage())
}
func (c *client) Update(ctx context.Context, owner, target string, in resourcev2.MutationRequest) (resourcev2.Record, error) {
	var z resourcev2.Record
	if target == "" || in.Operation != "update" || in.ExpectedRevision == nil || *in.ExpectedRevision == "" {
		return z, invalid()
	}
	if err := c.check(in.Route); err != nil {
		return z, err
	}
	p, err := resourcev2.ToProtoMutationRequest(in)
	if err != nil {
		return z, err
	}
	out, err := c.call(ctx, owner, target, &resourcepb.Request{Version: resourcev2.WireVersion, Operation: &resourcepb.Request_Mutate{Mutate: p}})
	if err != nil {
		return z, err
	}
	r := out.GetResource()
	if r == nil || r.GetVersion() != resourcev2.WireVersion || r.GetRecord() == nil {
		return z, invalid()
	}
	record, err := resourcev2.FromProtoRecord(r.GetRecord())
	if err != nil {
		return z, err
	}
	if !sameReference(record.Reference, in.Reference) {
		return z, invalid()
	}
	return record, nil
}
