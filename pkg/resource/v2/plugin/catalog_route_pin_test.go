package plugin

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

type distinctRouteCatalogProvider struct{ relationshipBoundaryProvider }

func (p *distinctRouteCatalogProvider) Catalog(
	ctx context.Context,
	in resourcev2.CatalogRequest,
) (resourcev2.Catalog, error) {
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
			ctx := relationshipRoutePinContext(context.Background(), t, c, request, mode)
			result, err := c.ResolveRelationships(ctx, request)
			if mode == "valid" {
				if err != nil || len(result) != 1 || p.calls.Load() != 1 {
					t.Fatalf(
						"distinct accepted route epochs failed: result=%v calls=%d err=%v",
						result,
						p.calls.Load(),
						err,
					)
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
		outgoing := metadata.NewOutgoingContext(
			context.Background(),
			metadata.MD{catalogRevisionMetadata: {"cat-1"}, catalogRouteRevisionsMetadata: values},
		)
		var response resourcepb.AncillaryResponse
		err = conn.Invoke(
			outgoing,
			resourcepb.ResourceAncillary_Call_FullMethodName,
			&resourcepb.AncillaryRequest{Version: 2, Operation: "resolveRelationships", RelationshipResolution: wire},
			&response,
		)
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
		//nolint:fatcontext // Accumulate all nine observations to exercise the aggregate limit.
		ctx = appendLargeRouteObservation(ctx, i)
	}
	if _, err := outgoingSession(ctx); resourcev2.ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("aggregate context: %v", err)
	}
}

func relationshipRoutePinContext(
	ctx context.Context,
	t *testing.T,
	p resourcev2.Provider,
	request resourcev2.RelationshipResolutionRequest,
	mode string,
) context.Context {
	t.Helper()
	ctx = acceptedCatalogContext(ctx, t, p, request.Source.Route)
	if mode == "missing" {
		return ctx
	}
	target := request.Targets[0]
	catalog, err := p.Catalog(ctx, resourcev2.CatalogRequest{Route: target.Route})
	if err != nil {
		t.Fatal(err)
	}
	revision := catalog.Revision
	if mode == "wrong" {
		revision = "wrong-observation"
	}
	return resourcev2.WithCatalogRevisionForRoute(ctx, target.Provider, target.Route, revision)
}

func appendLargeRouteObservation(ctx context.Context, index int) context.Context {
	return resourcev2.WithCatalogRevisionForRoute(
		ctx,
		"test",
		resourcev2.AccessRoute{Connection: strconv.Itoa(index)},
		strings.Repeat("x", resourcev2.MaxCatalogRevisionBytes),
	)
}
