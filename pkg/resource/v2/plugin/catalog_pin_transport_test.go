package plugin

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

func TestCatalogPinSessionRoundTrip(t *testing.T) {
	ctx := resourcev2.WithCatalogRevision(context.Background(), "catalog/日本語\x00revision")
	outgoing, err := outgoingSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	md, _ := metadata.FromOutgoingContext(outgoing)
	incoming, err := incomingSession(metadata.NewIncomingContext(context.Background(), md), nil)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := resourcev2.CatalogRevisionFromContext(incoming)
	if !ok || got != "catalog/日本語\x00revision" {
		t.Fatalf("pin lost: %q", got)
	}
}

func TestCatalogPinTypedDuplicateFailsClosed(t *testing.T) {
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(catalogRevisionMetadata, "same"))
	ctx = resourcev2.WithCatalogRevision(ctx, "same")
	if _, err := outgoingSession(ctx); resourcev2.ErrorCode(err) != "RESET_REQUIRED" {
		t.Fatalf("duplicate: %v", err)
	}
}

func TestCatalogPinRawMalformedAndMissingFailClosed(t *testing.T) {
	for _, values := range [][]string{nil, {"same", "same"}, {""}, {string([]byte{255})}, {strings.Repeat("x", 4097)}} {
		p := &qualifiedProvider{allowed: true}
		s := grpc.NewServer()
		RegisterServer(s, p)
		conn := pipe(t, s)
		ctx := metadata.NewOutgoingContext(context.Background(), metadata.MD{catalogRevisionMetadata: values})
		var out resourcepb.Response
		err := conn.Invoke(
			ctx,
			resourcepb.ResourceProvider_Call_FullMethodName,
			&resourcepb.Request{
				Version: 2,
				Operation: &resourcepb.Request_Get{
					Get: &resourcepb.GetInput{Route: &resourcepb.AccessRoute{Connection: "explicit"}},
				},
			},
			&out,
		)
		if resourcev2.ErrorCode(domainError(err)) != "RESET_REQUIRED" {
			t.Fatalf("metadata %q: %v", values, err)
		}
		if p.calls.Load() != 0 {
			t.Fatal("invalid pin dispatched native resource Get")
		}
	}
}

func TestCatalogDiscoveryDoesNotRequirePin(t *testing.T) {
	p := &countProvider{}
	s := grpc.NewServer()
	RegisterServer(s, p)
	_, err := NewClient(
		pipe(t, s),
	).Catalog(context.Background(), resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: "explicit"}})
	if err != nil {
		t.Fatal(err)
	}
}

type changingCatalogProvider struct {
	qualifiedProvider
	revision string
}

func (p *changingCatalogProvider) Catalog(
	ctx context.Context,
	in resourcev2.CatalogRequest,
) (resourcev2.Catalog, error) {
	catalog, err := p.qualifiedProvider.Catalog(ctx, in)
	catalog.Revision = p.revision
	return catalog, err
}

func (p *changingCatalogProvider) Get(ctx context.Context, in resourcev2.GetRequest) (resourcev2.Record, error) {
	result, err := p.qualifiedProvider.Get(ctx, in)
	p.revision = "changed"
	return result, err
}

func TestDirectProviderCatalogChangeRejectsResult(t *testing.T) {
	p := &changingCatalogProvider{qualifiedProvider: qualifiedProvider{allowed: true}, revision: "accepted"}
	s := grpc.NewServer()
	RegisterServer(s, p)
	c := NewClient(pipe(t, s))
	ctx := acceptedCatalogContext(t, context.Background(), c, testGet().Route)
	_, err := c.Get(ctx, testGet())
	if resourcev2.ErrorCode(err) != "RESET_REQUIRED" {
		t.Fatalf("changed catalog result escaped: %v", err)
	}
	if p.calls.Load() != 1 {
		t.Fatal("finite operation did not run exactly once")
	}
}

func TestDirectProviderStaleCatalogDoesNotDispatch(t *testing.T) {
	p := &changingCatalogProvider{qualifiedProvider: qualifiedProvider{allowed: true}, revision: "accepted"}
	s := grpc.NewServer()
	RegisterServer(s, p)
	c := NewClient(pipe(t, s))
	ctx := acceptedCatalogContext(t, context.Background(), c, testGet().Route)
	p.revision = "changed"
	_, err := c.Get(ctx, testGet())
	if resourcev2.ErrorCode(err) != "RESET_REQUIRED" {
		t.Fatalf("stale catalog dispatched: %v", err)
	}
	if p.calls.Load() != 0 {
		t.Fatal("stale finite operation dispatched")
	}
}
