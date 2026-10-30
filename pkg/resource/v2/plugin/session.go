package plugin

import (
	"context"
	"unicode/utf8"

	"google.golang.org/grpc/metadata"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	pkgsettings "github.com/omniviewdev/plugin-sdk/settings"
)

const (
	requestIDMetadata             = "omniview-request-id-bin"
	requesterIDMetadata           = "omniview-requester-id-bin"
	maxDiagnosticIDBytes          = 256
	catalogRevisionMetadata       = "omniview-resource-catalog-revision-bin"
	catalogRouteRevisionsMetadata = "omniview-resource-catalog-route-revisions-bin"
)

func diagnostic(md metadata.MD, key string) (string, error) {
	values := md.Get(key)
	if len(values) > 1 {
		return "", invalid("duplicate diagnostic metadata")
	}
	if len(values) == 0 {
		return "", nil
	}
	v := values[0]
	if len(v) > maxDiagnosticIDBytes || !utf8.ValidString(v) {
		return "", invalid("invalid diagnostic metadata")
	}
	return v, nil
}

// Diagnostic identifiers are correlation data, never authenticated principal data.
func outgoingSession(ctx context.Context) (context.Context, error) {
	session, _ := resourcev2.SessionFromContext(ctx)
	md, _ := metadata.FromOutgoingContext(ctx)
	pins := md.Get(catalogRevisionMetadata)
	if len(pins) > 1 {
		return nil, resourcev2.NewError("RESET_REQUIRED", "Duplicate catalog revision metadata.")
	}
	if len(pins) == 1 {
		if err := resourcev2.ValidateCatalogRevision(pins[0]); err != nil {
			return nil, err
		}
	}
	if revision, ok := resourcev2.CatalogRevisionFromContext(ctx); ok {
		if err := resourcev2.ValidateCatalogRevision(revision); err != nil {
			return nil, err
		}
		if len(pins) != 0 {
			return nil, resourcev2.NewError("RESET_REQUIRED", "Duplicate catalog revision metadata.")
		}
		md = md.Copy()
		md.Set(catalogRevisionMetadata, revision)
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	ctx, err := outgoingCatalogRoutes(ctx, md)
	if err != nil {
		return nil, err
	}
	md, _ = metadata.FromOutgoingContext(ctx)
	if _, err := diagnostic(md, requestIDMetadata); err != nil {
		return nil, err
	}
	if _, err := diagnostic(md, requesterIDMetadata); err != nil {
		return nil, err
	}
	if session.RequestID == "" && session.RequesterID == "" {
		return ctx, nil
	}
	md = md.Copy()
	if err := attachDiagnostic(md, requestIDMetadata, session.RequestID); err != nil {
		return nil, err
	}
	if err := attachDiagnostic(md, requesterIDMetadata, session.RequesterID); err != nil {
		return nil, err
	}
	return metadata.NewOutgoingContext(ctx, md), nil
}

func attachDiagnostic(md metadata.MD, key, value string) error {
	if value == "" {
		return nil
	}
	if len(md.Get(key)) != 0 {
		return invalid("duplicate diagnostic metadata")
	}
	if len(value) > maxDiagnosticIDBytes || !utf8.ValidString(value) {
		return invalid("invalid diagnostic metadata")
	}
	md.Set(key, value)
	return nil
}

func incomingSession(ctx context.Context, settings pkgsettings.Provider) (context.Context, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	// Wire admission must not inherit an interceptor's unrelated catalog context.
	ctx = resourcev2.WithCatalogRevision(ctx, "")
	pins := md.Get(catalogRevisionMetadata)
	if len(pins) > 1 {
		return nil, resourcev2.NewError("RESET_REQUIRED", "Duplicate catalog revision metadata.")
	}
	if len(pins) == 1 {
		if err := resourcev2.ValidateCatalogRevision(pins[0]); err != nil {
			return nil, err
		}
		ctx = resourcev2.WithCatalogRevision(ctx, pins[0])
	}
	ctx, err := incomingCatalogRoutes(ctx, md)
	if err != nil {
		return nil, err
	}
	requestID, err := diagnostic(md, requestIDMetadata)
	if err != nil {
		return nil, err
	}
	requesterID, err := diagnostic(md, requesterIDMetadata)
	if err != nil {
		return nil, err
	}
	session, _ := resourcev2.SessionFromContext(ctx)
	if session.RequestID == "" {
		session.RequestID = requestID
	}
	if session.RequesterID == "" {
		session.RequesterID = requesterID
	}
	if settings != nil {
		session.PluginConfig = settings
	}
	return resourcev2.WithSession(ctx, session), nil
}

// OutgoingSessionContext serializes explicit catalog admission and diagnostic
// context for resource-bearing utility RPCs. It never discovers a catalog.
func OutgoingSessionContext(ctx context.Context) (context.Context, error) {
	return outgoingSession(ctx)
}

// IncomingSessionContext validates shared resource metadata for utility RPCs.
// A missing pin is permitted here; reference-bearing dispatch must require it.
func IncomingSessionContext(ctx context.Context) (context.Context, error) {
	return incomingSession(ctx, nil)
}
