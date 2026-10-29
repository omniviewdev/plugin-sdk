package resourcev2

import (
	"context"

	"github.com/omniviewdev/plugin-sdk/internal/catalogadmission"
)

// MaxCatalogRevisionMetadataBytes bounds all catalog admission metadata together.
const MaxCatalogRevisionMetadataBytes = 32 * 1024

// WithCatalogRevisionForRoute records an explicit provider/route observation for
// a source or relationship target. It takes precedence over the explicit default
// and never discovers or substitutes a current catalog. Duplicate route entries
// remain ambiguous and fail admission, even when their revisions are equal.
func WithCatalogRevisionForRoute(ctx context.Context, provider string, route AccessRoute, revision string) context.Context {
	return catalogadmission.Append(ctx, catalogadmission.Observation{Provider: provider, Connection: route.Connection, Revision: revision})
}

// CatalogRevisionForRouteFromContext selects the qualified observation first,
// falling back only when that provider/route has no qualified entry.
func CatalogRevisionForRouteFromContext(ctx context.Context, provider string, route AccessRoute) (string, bool) {
	if revision, ok := QualifiedCatalogRevisionFromContext(ctx, provider, route); ok {
		return revision, true
	}
	return CatalogRevisionFromContext(ctx)
}

// QualifiedCatalogRevisionFromContext never falls back to a source observation.
// Duplicate entries return an empty but present revision so admission fails closed.
func QualifiedCatalogRevisionFromContext(ctx context.Context, provider string, route AccessRoute) (string, bool) {
	return catalogadmission.Qualified(ctx, provider, route.Connection)
}

// CheckCatalogRevisionForRoute selects a qualified observation, when supplied,
// otherwise the explicit default observation. Missing or duplicate pins fail closed.
// Observation equality does not establish authorization or native client ownership.
func CheckCatalogRevisionForRoute(ctx context.Context, catalog Catalog, route AccessRoute) error {
	defaultRevision, _ := CatalogRevisionFromContext(ctx)
	_, violation := catalogadmission.EncodedSize(ctx, defaultRevision, catalogadmission.Limits{
		RevisionBytes: MaxCatalogRevisionBytes,
		MetadataBytes: MaxCatalogRevisionMetadataBytes,
		Observations:  MaxRelationshipTargets,
	})
	if violation != nil {
		return NewError(violation.Code, violation.Message)
	}
	revision, _ := CatalogRevisionForRouteFromContext(ctx, catalog.Provider, route)
	if err := ValidateCatalogRevision(revision); err != nil {
		return err
	}
	if revision != catalog.Revision {
		return NewError("RESET_REQUIRED", "The accepted catalog revision is no longer current.")
	}
	return nil
}
