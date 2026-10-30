package plugin

import (
	"context"
	"testing"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
)

func acceptedCatalogContext(
	ctx context.Context,
	t *testing.T,
	p resourcev2.Provider,
	route resourcev2.AccessRoute,
) context.Context {
	t.Helper()
	catalog, err := p.Catalog(ctx, resourcev2.CatalogRequest{Route: route})
	if err != nil {
		t.Fatal(err)
	}
	return resourcev2.WithCatalogRevision(ctx, catalog.Revision)
}

func acceptedWireContext(
	ctx context.Context,
	t *testing.T,
	p resourcev2.Provider,
	route resourcev2.AccessRoute,
) context.Context {
	t.Helper()
	ctx, err := outgoingSession(acceptedCatalogContext(ctx, t, p, route))
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func acceptedRelationshipContext(
	ctx context.Context,
	t *testing.T,
	p resourcev2.Provider,
	request resourcev2.RelationshipResolutionRequest,
) context.Context {
	t.Helper()
	ctx = acceptedCatalogContext(ctx, t, p, request.Source.Route)
	seen := map[struct {
		provider string
		route    resourcev2.AccessRoute
	}]bool{}
	for _, target := range request.Targets {
		key := struct {
			provider string
			route    resourcev2.AccessRoute
		}{target.Provider, target.Route}
		if seen[key] {
			continue
		}
		seen[key] = true
		//nolint:fatcontext // Each target observation must remain in the accumulated context.
		ctx = acceptedQualifiedCatalogContext(ctx, t, p, target.Provider, target.Route)
	}
	return ctx
}

func acceptedRelationshipWireContext(
	ctx context.Context,
	t *testing.T,
	p resourcev2.Provider,
	request resourcev2.RelationshipResolutionRequest,
) context.Context {
	t.Helper()
	ctx, err := outgoingSession(acceptedRelationshipContext(ctx, t, p, request))
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

// acceptedQualifiedCatalogContext records one route's actual catalog observation.
func acceptedQualifiedCatalogContext(
	ctx context.Context,
	t *testing.T,
	p resourcev2.Provider,
	provider string,
	route resourcev2.AccessRoute,
) context.Context {
	t.Helper()
	catalog, err := p.Catalog(ctx, resourcev2.CatalogRequest{Route: route})
	if err != nil {
		t.Fatal(err)
	}
	return resourcev2.WithCatalogRevisionForRoute(ctx, provider, route, catalog.Revision)
}
