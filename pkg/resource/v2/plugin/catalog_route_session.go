package plugin

import (
	"context"

	"google.golang.org/grpc/metadata"

	"github.com/omniviewdev/plugin-sdk/internal/catalogadmission"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
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
		if _, err := unmarshalCatalogRevisionRoutes(validation, []byte(frames[0])); err != nil {
			return nil, err
		}
	}
	frame, err := marshalCatalogRevisionRoutes(validation)
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
		return catalogadmission.Clear(ctx), nil
	}
	return unmarshalCatalogRevisionRoutes(ctx, []byte(frames[0]))
}

// Qualified observations cross RPC only through this SDK-private frame.
func catalogAdmissionLimits() catalogadmission.Limits {
	return catalogadmission.Limits{
		RevisionBytes: resourcev2.MaxCatalogRevisionBytes,
		MetadataBytes: resourcev2.MaxCatalogRevisionMetadataBytes,
		Observations:  resourcev2.MaxRelationshipTargets,
	}
}

func marshalCatalogRevisionRoutes(ctx context.Context) ([]byte, error) {
	defaultRevision, _ := resourcev2.CatalogRevisionFromContext(ctx)
	frame, violation := catalogadmission.Marshal(ctx, defaultRevision, catalogAdmissionLimits())
	if violation != nil {
		return nil, resourcev2.NewError(violation.Code, violation.Message)
	}
	return frame, nil
}

func unmarshalCatalogRevisionRoutes(ctx context.Context, frame []byte) (context.Context, error) {
	defaultRevision, _ := resourcev2.CatalogRevisionFromContext(ctx)
	result, violation := catalogadmission.Unmarshal(ctx, frame, defaultRevision, catalogAdmissionLimits())
	if violation != nil {
		return nil, resourcev2.NewError(violation.Code, violation.Message)
	}
	return result, nil
}
