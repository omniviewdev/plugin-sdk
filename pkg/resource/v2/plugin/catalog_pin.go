package plugin

import (
	"context"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

// checkCatalogRoute compares the caller's observation without acquiring a new pin.
func (s *server) checkCatalogRoute(ctx context.Context, route *resourcepb.AccessRoute) error {
	in, err := resourcev2.FromProtoAccessRoute(route)
	if err != nil {
		return err
	}
	catalog, err := s.impl.Catalog(ctx, resourcev2.CatalogRequest{Route: in})
	if err != nil {
		return err
	}
	return resourcev2.CheckCatalogRevisionForRoute(ctx, catalog, in)
}

func finiteRoute(r *resourcepb.Request) *resourcepb.AccessRoute {
	switch op := r.Operation.(type) {
	case *resourcepb.Request_Get:
		return op.Get.GetRoute()
	case *resourcepb.Request_Enumerate:
		return op.Enumerate.GetTarget().GetRoute()
	case *resourcepb.Request_NativeQuery:
		return op.NativeQuery.GetTarget().GetRoute()
	case *resourcepb.Request_Mutate:
		return op.Mutate.GetRoute()
	case *resourcepb.Request_Action:
		return op.Action.GetRoute()
	}
	return nil
}

func ancillaryRoutes(r *resourcepb.AncillaryRequest) []*resourcepb.AccessRoute {
	switch r.Operation {
	case "scopeValues", "schemaDocument", "groups", "definition", "editorSchemas", "actions", "relationships":
		return []*resourcepb.AccessRoute{r.Route}
	case "health", "events":
		return []*resourcepb.AccessRoute{r.Get.GetRoute()}
	}
	return nil
}

func (s *server) checkCatalogRoutes(ctx context.Context, routes []*resourcepb.AccessRoute) error {
	for _, route := range routes {
		if err := s.checkCatalogRoute(ctx, route); err != nil {
			return err
		}
	}
	return nil
}

func updateRoutes(r *resourcepb.UpdateRequest) []*resourcepb.AccessRoute {
	switch r.Operation {
	case "startConnection":
		routes := make([]*resourcepb.AccessRoute, 0, len(r.Targets))
		for _, target := range r.Targets {
			routes = append(routes, target.GetRoute())
		}
		return routes
	case "ensure", "restart":
		return []*resourcepb.AccessRoute{r.Target.GetRoute()}
	case "stopConnection", "has", "state", "stopResource", "running":
		return []*resourcepb.AccessRoute{r.Route}
	}
	return nil
}

func (s *server) checkAncillaryCatalogs(ctx context.Context, request *resourcepb.AncillaryRequest) error {
	if request.Operation == "resolveRelationships" {
		in, err := resourcev2.FromProtoRelationshipResolutionRequest(request.RelationshipResolution)
		if err != nil {
			return err
		}
		sourceProvider := in.Source.Reference.Address.Provider
		for _, target := range in.Targets {
			if target.Route != in.Source.Route || target.Provider != sourceProvider {
				revision, ok := resourcev2.QualifiedCatalogRevisionFromContext(ctx, target.Provider, target.Route)
				if !ok {
					return resourcev2.NewError(
						"RESET_REQUIRED",
						"An explicit accepted catalog revision is required for each relationship target route.",
					)
				}
				if err := resourcev2.ValidateCatalogRevision(revision); err != nil {
					return err
				}
			}
			catalog, err := s.impl.Catalog(ctx, resourcev2.CatalogRequest{Route: target.Route})
			if err != nil {
				return err
			}
			if catalog.Provider != target.Provider {
				return resourcev2.NewError("RESET_REQUIRED", "The accepted target catalog provider no longer matches.")
			}
			if err := resourcev2.CheckCatalogRevisionForRoute(ctx, catalog, target.Route); err != nil {
				return err
			}
		}
		return s.checkCatalogRoute(ctx, request.RelationshipResolution.GetSource().GetRoute())
	}
	return s.checkCatalogRoutes(ctx, ancillaryRoutes(request))
}
