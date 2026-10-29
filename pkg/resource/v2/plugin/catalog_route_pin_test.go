package plugin

import (
	"context"
	"fmt"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"strings"
	"testing"
)

type distinctRouteCatalogProvider struct{ relationshipBoundaryProvider }

func (p *distinctRouteCatalogProvider) Catalog(ctx context.Context, in resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
	catalog, err := p.ancillaryFixtureProvider.Catalog(ctx, in)
	catalog.Revision = "accepted-epoch/" + in.Route.Connection
	return catalog, err
}

func TestActualGRPCDistinctRelationshipRouteCatalogPins(t *testing.T) {
	for _, mode := range []string{"valid", "missing", "wrong"} {
		t.Run(mode, func(t *testing.T) {
			p := &distinctRouteCatalogProvider{}
			s := grpc.NewServer()
			RegisterServer(s, p)
			c := NewClient(pipe(t, s))
			request := relationshipFixtureRequest()
			ctx := acceptedCatalogContext(t, context.Background(), c, request.Source.Route)
			if mode != "missing" {
				target := request.Targets[0]
				catalog, err := c.Catalog(ctx, resourcev2.CatalogRequest{Route: target.Route})
				if err != nil {
					t.Fatal(err)
				}
				revision := catalog.Revision
				if mode == "wrong" {
					revision = "wrong-observation"
				}
				ctx = resourcev2.WithCatalogRevisionForRoute(ctx, target.Provider, target.Route, revision)
			}
			result, err := c.ResolveRelationships(ctx, request)
			if mode == "valid" {
				if err != nil || len(result) != 1 || p.calls.Load() != 1 {
					t.Fatalf("distinct accepted route epochs failed: result=%v calls=%d err=%v", result, p.calls.Load(), err)
				}
			} else if resourcev2.ErrorCode(err) != "RESET_REQUIRED" || p.calls.Load() != 0 {
				t.Fatalf("%s target observation dispatched: calls=%d err=%v", mode, p.calls.Load(), err)
			}
		})
	}
}

func TestQualifiedCatalogMetadataRejectsMalformedBeforeDispatch(t *testing.T) {
	request := relationshipFixtureRequest()
	target := request.Targets[0]
	ctx := resourcev2.WithCatalogRevisionForRoute(context.Background(), target.Provider, target.Route, "accepted")
	frame, err := marshalCatalogRevisionRoutes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, values := range [][]string{{string(frame), string(frame)}, {string(append(append([]byte(nil), frame...), 0))}, {""}, {string([]byte{1, 0, 1, 255})}} {
		p := &relationshipBoundaryProvider{}
		s := grpc.NewServer()
		RegisterServer(s, p)
		conn := pipe(t, s)
		wire, err := resourcev2.ToProtoRelationshipResolutionRequest(request)
		if err != nil {
			t.Fatal(err)
		}
		outgoing := metadata.NewOutgoingContext(context.Background(), metadata.MD{catalogRevisionMetadata: {"cat-1"}, catalogRouteRevisionsMetadata: values})
		var response resourcepb.AncillaryResponse
		err = conn.Invoke(outgoing, resourcepb.ResourceAncillary_Call_FullMethodName, &resourcepb.AncillaryRequest{Version: 2, Operation: "resolveRelationships", RelationshipResolution: wire}, &response)
		if resourcev2.ErrorCode(domainError(err)) != "RESET_REQUIRED" || p.calls.Load() != 0 {
			t.Fatalf("malformed route metadata dispatched: calls=%d err=%v", p.calls.Load(), err)
		}
	}
}

func TestQualifiedCatalogContextRejectsDuplicatesAndAggregateLimit(t *testing.T) {
	route := resourcev2.AccessRoute{Connection: "target"}
	ctx := resourcev2.WithCatalogRevisionForRoute(context.Background(), "test", route, "revision")
	ctx = resourcev2.WithCatalogRevisionForRoute(ctx, "test", route, "revision")
	if _, err := outgoingSession(ctx); resourcev2.ErrorCode(err) != "RESET_REQUIRED" {
		t.Fatalf("duplicate context: %v", err)
	}
	ctx = context.Background()
	for i := range 9 {
		ctx = resourcev2.WithCatalogRevisionForRoute(ctx, "test", resourcev2.AccessRoute{Connection: fmt.Sprint(i)}, strings.Repeat("x", resourcev2.MaxCatalogRevisionBytes))
	}
	if _, err := outgoingSession(ctx); resourcev2.ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("aggregate context: %v", err)
	}
}
