package plugin

import (
	"context"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"google.golang.org/grpc/metadata"
)

func outgoingCatalogRoutes(ctx context.Context, md metadata.MD) (context.Context, error) {
	validation := ctx
	if pins := md.Get(catalogRevisionMetadata); len(pins) == 1 {
		validation = resourcev2.WithCatalogRevision(validation, pins[0])
	}
	frames := md.Get(catalogRouteRevisionsMetadata)
	if len(frames) > 1 {
		return nil, resourcev2.NewError("RESET_REQUIRED", "Duplicate route catalog revision metadata.")
	}
	if len(frames) == 1 {
		if _, err := resourcev2.UnmarshalCatalogRevisionRoutes(validation, []byte(frames[0])); err != nil {
			return nil, err
		}
	}
	frame, err := resourcev2.MarshalCatalogRevisionRoutes(validation)
	if err != nil {
		return nil, err
	}
	if len(frame) != 0 {
		if len(frames) != 0 {
			return nil, resourcev2.NewError("RESET_REQUIRED", "Duplicate route catalog revision metadata.")
		}
		md = md.Copy()
		md.Set(catalogRouteRevisionsMetadata, string(frame))
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	return ctx, nil
}

func incomingCatalogRoutes(ctx context.Context, md metadata.MD) (context.Context, error) {
	frames := md.Get(catalogRouteRevisionsMetadata)
	if len(frames) > 1 {
		return nil, resourcev2.NewError("RESET_REQUIRED", "Duplicate route catalog revision metadata.")
	}
	if len(frames) == 0 {
		return resourcev2.ClearCatalogRevisionRoutes(ctx), nil
	}
	return resourcev2.UnmarshalCatalogRevisionRoutes(ctx, []byte(frames[0]))
}
