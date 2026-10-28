package plugin

import (
	"context"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"testing"
)

func acceptedCatalogContext(t *testing.T, ctx context.Context, p resourcev2.Provider, route resourcev2.AccessRoute) context.Context {
	t.Helper()
	catalog, err := p.Catalog(ctx, resourcev2.CatalogRequest{Route: route})
	if err != nil {
		t.Fatal(err)
	}
	return resourcev2.WithCatalogRevision(ctx, catalog.Revision)
}

func acceptedWireContext(t *testing.T, ctx context.Context, p resourcev2.Provider, route resourcev2.AccessRoute) context.Context {
	t.Helper()
	ctx, err := outgoingSession(acceptedCatalogContext(t, ctx, p, route))
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func acceptedRelationshipContext(t *testing.T, ctx context.Context, p resourcev2.Provider, request resourcev2.RelationshipResolutionRequest) context.Context {
	t.Helper()
	ctx = acceptedCatalogContext(t, ctx, p, request.Source.Route)
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
		catalog, err := p.Catalog(ctx, resourcev2.CatalogRequest{Route: target.Route})
		if err != nil {
			t.Fatal(err)
		}
		ctx = resourcev2.WithCatalogRevisionForRoute(ctx, target.Provider, target.Route, catalog.Revision)
	}
	return ctx
}

func acceptedRelationshipWireContext(t *testing.T, ctx context.Context, p resourcev2.Provider, request resourcev2.RelationshipResolutionRequest) context.Context {
	t.Helper()
	ctx, err := outgoingSession(acceptedRelationshipContext(t, ctx, p, request))
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}
