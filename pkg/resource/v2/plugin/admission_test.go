package plugin

import (
	"context"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	"google.golang.org/grpc"
	"sync/atomic"
	"testing"
)

type qualifiedProvider struct {
	allowed bool
	escape  bool
	calls   atomic.Int32
}

func testRepresentation() resourcev2.Representation {
	return resourcev2.Representation{ID: "document", Revision: "rep-1", Schema: resourcev2.SchemaBinding{State: "absent", Validation: "none", Unvalidated: []string{}}}
}
func testGet() resourcev2.GetRequest {
	return resourcev2.GetRequest{Route: resourcev2.AccessRoute{Connection: "explicit"}, Reference: resourcev2.ResourceReference{Address: resourcev2.ResourceAddress{Provider: "test", Authority: "authority", Type: "opaque/type", ID: "opaque/id", Scope: []resourcev2.ScopeEntry{}}}, Representation: "document"}
}
func (p *qualifiedProvider) Catalog(context.Context, resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
	caps := []resourcev2.Capability{}
	if p.allowed {
		caps = append(caps, resourcev2.Capability{Operation: resourcev2.OperationGet, Scope: resourcev2.QueryScope{}})
	}
	return resourcev2.Catalog{Provider: "test", Revision: "cat-1", Coverage: resourcev2.Coverage{Kind: "complete", Paths: []string{}}, Types: []resourcev2.ResourceType{{Type: "opaque/type", IdentityDimensions: []resourcev2.Dimension{}, QueryDimensions: []resourcev2.Dimension{}, Representations: []resourcev2.Representation{testRepresentation()}, Capabilities: caps}}}, nil
}
func (p *qualifiedProvider) Get(context.Context, resourcev2.GetRequest) (resourcev2.Record, error) {
	p.calls.Add(1)
	reference := testGet().Reference
	if p.escape {
		reference.Address.Authority = "escaped"
	}
	return resourcev2.Record{Reference: reference, Body: resourcev2.Value{Kind: "map", Map: map[string]resourcev2.Value{}}, Representation: testRepresentation(), Coverage: resourcev2.Coverage{Kind: "complete", Paths: []string{}}}, nil
}
func TestCatalogDeniesDirectProviderDispatch(t *testing.T) {
	p := &qualifiedProvider{}
	s := grpc.NewServer()
	RegisterServer(s, p)
	c := pipe(t, s)
	input, err := resourcev2.ToProtoGetRequest(testGet())
	if err != nil {
		t.Fatal(err)
	}
	var out resourcepb.Response
	err = c.Invoke(context.Background(), resourcepb.ResourceProvider_Call_FullMethodName, &resourcepb.Request{Version: 2, Operation: &resourcepb.Request_Get{Get: input}}, &out)
	if resourcev2.ErrorCode(domainError(err)) != "UNSUPPORTED" {
		t.Fatalf("unexpected class: %v", err)
	}
	if p.calls.Load() != 0 {
		t.Fatal("catalog-denied Get dispatched")
	}
}
func TestServerRejectsEscapedResponse(t *testing.T) {
	p := &qualifiedProvider{allowed: true, escape: true}
	s := grpc.NewServer()
	RegisterServer(s, p)
	c := NewClient(pipe(t, s))
	_, err := c.Get(context.Background(), testGet())
	if resourcev2.ErrorCode(err) != "INTERNAL" {
		t.Fatalf("escaped response accepted: %v", err)
	}
	if p.calls.Load() != 1 {
		t.Fatal("Get not dispatched")
	}
}

// maliciousServer bypasses domain response admission, but not the client's default codec.
type maliciousServer struct {
	resourcepb.UnimplementedResourceProviderServer
	provider *qualifiedProvider
}

func (s *maliciousServer) Call(ctx context.Context, in *resourcepb.Request) (*resourcepb.Response, error) {
	if in.GetCatalog() != nil {
		v, err := s.provider.Catalog(ctx, resourcev2.CatalogRequest{})
		if err != nil {
			return nil, err
		}
		p, err := resourcev2.ToProtoCatalog(v)
		return &resourcepb.Response{Version: 2, Result: &resourcepb.Response_Catalog{Catalog: p}}, err
	}
	v, err := s.provider.Get(ctx, testGet())
	if err != nil {
		return nil, err
	}
	p, err := resourcev2.ToProtoRecord(v)
	return &resourcepb.Response{Version: 2, Result: &resourcepb.Response_Record{Record: p}}, err
}
func TestClientRejectsEscapedResponse(t *testing.T) {
	p := &qualifiedProvider{allowed: true, escape: true}
	s := grpc.NewServer()
	resourcepb.RegisterResourceProviderServer(s, &maliciousServer{provider: p})
	c := NewClient(pipe(t, s))
	_, err := c.Get(context.Background(), testGet())
	if resourcev2.ErrorCode(err) != "INTERNAL" {
		t.Fatalf("client accepted escaped response: %v", err)
	}
}
