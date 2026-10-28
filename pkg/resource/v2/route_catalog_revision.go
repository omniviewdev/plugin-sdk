package resourcev2

import (
	"context"
	"encoding/binary"
	"unicode/utf8"
)

// MaxCatalogRevisionMetadataBytes bounds all catalog admission metadata together.
const MaxCatalogRevisionMetadataBytes = 32 * 1024

type routeCatalogRevision struct {
	provider string
	route    AccessRoute
	revision string
}
type routeCatalogRevisionsKey struct{}

// WithCatalogRevisionForRoute records an explicit provider/route observation for
// a source or relationship target. It takes precedence over the explicit default
// and never discovers or substitutes a current catalog.
func WithCatalogRevisionForRoute(ctx context.Context, provider string, route AccessRoute, revision string) context.Context {
	previous, _ := ctx.Value(routeCatalogRevisionsKey{}).([]routeCatalogRevision)
	entries := make([]routeCatalogRevision, len(previous)+1)
	copy(entries, previous)
	entries[len(previous)] = routeCatalogRevision{provider: provider, route: route, revision: revision}
	return context.WithValue(ctx, routeCatalogRevisionsKey{}, entries)
}

func CatalogRevisionForRouteFromContext(ctx context.Context, provider string, route AccessRoute) (string, bool) {
	if revision, ok := QualifiedCatalogRevisionFromContext(ctx, provider, route); ok {
		return revision, true
	}
	return CatalogRevisionFromContext(ctx)
}

// QualifiedCatalogRevisionFromContext never falls back to a source observation.
func QualifiedCatalogRevisionFromContext(ctx context.Context, provider string, route AccessRoute) (string, bool) {
	entries, _ := ctx.Value(routeCatalogRevisionsKey{}).([]routeCatalogRevision)
	var revision string
	found := false
	for _, entry := range entries {
		if entry.provider == provider && entry.route == route {
			if found {
				return "", true
			}
			revision, found = entry.revision, true
		}
	}
	return revision, found
}

// CheckCatalogRevisionForRoute selects a qualified observation, when supplied,
// otherwise the explicit default observation. Missing or duplicate pins fail closed.
func CheckCatalogRevisionForRoute(ctx context.Context, catalog Catalog, route AccessRoute) error {
	if _, err := catalogRevisionRoutesSize(ctx); err != nil {
		return err
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

// MarshalCatalogRevisionRoutes encodes the bounded SDK-owned metadata frame:
// version byte, uint16 count, then uint16-length UTF-8 provider/route/revision.
func MarshalCatalogRevisionRoutes(ctx context.Context) ([]byte, error) {
	size, err := catalogRevisionRoutesSize(ctx)
	if err != nil {
		return nil, err
	}
	if size == 0 {
		return nil, nil
	}
	entries, _ := ctx.Value(routeCatalogRevisionsKey{}).([]routeCatalogRevision)
	frame := make([]byte, size)
	frame[0] = 1
	binary.BigEndian.PutUint16(frame[1:3], uint16(len(entries)))
	offset := 3
	for _, entry := range entries {
		for _, field := range []string{entry.provider, entry.route.Connection, entry.revision} {
			binary.BigEndian.PutUint16(frame[offset:offset+2], uint16(len(field)))
			offset += 2
			copy(frame[offset:], field)
			offset += len(field)
		}
	}
	return frame, nil
}

// UnmarshalCatalogRevisionRoutes strictly decodes a complete metadata frame.
func UnmarshalCatalogRevisionRoutes(ctx context.Context, frame []byte) (context.Context, error) {
	invalid := func() (context.Context, error) {
		return nil, NewError("RESET_REQUIRED", "Malformed qualified catalog revision metadata.")
	}
	defaultRevision, _ := CatalogRevisionFromContext(ctx)
	if len(frame)+len(defaultRevision)+256 > MaxCatalogRevisionMetadataBytes {
		return nil, NewError("RESOURCE_LIMIT", "Catalog revision metadata exceeds bound.")
	}
	if len(frame) < 3 || frame[0] != 1 {
		return invalid()
	}
	count := int(binary.BigEndian.Uint16(frame[1:3]))
	if count > MaxRelationshipTargets {
		return nil, NewError("RESOURCE_LIMIT", "Too many route catalog observations.")
	}
	if count == 0 {
		return invalid()
	}
	entries := make([]routeCatalogRevision, 0, count)
	offset := 3
	for range count {
		fields := [3]string{}
		for j := range fields {
			if len(frame)-offset < 2 {
				return invalid()
			}
			size := int(binary.BigEndian.Uint16(frame[offset : offset+2]))
			offset += 2
			if size == 0 || size > MaxCatalogRevisionBytes || size > len(frame)-offset || !utf8.Valid(frame[offset:offset+size]) {
				return invalid()
			}
			fields[j] = string(frame[offset : offset+size])
			offset += size
		}
		entry := routeCatalogRevision{provider: fields[0], route: AccessRoute{Connection: fields[1]}, revision: fields[2]}
		for _, previous := range entries {
			if previous.provider == entry.provider && previous.route == entry.route {
				return invalid()
			}
		}
		entries = append(entries, entry)
	}
	if offset != len(frame) {
		return invalid()
	}
	return context.WithValue(ctx, routeCatalogRevisionsKey{}, entries), nil
}

// ClearCatalogRevisionRoutes removes inherited route observations at an RPC boundary.
func ClearCatalogRevisionRoutes(ctx context.Context) context.Context {
	return context.WithValue(ctx, routeCatalogRevisionsKey{}, []routeCatalogRevision(nil))
}

func catalogRevisionRoutesSize(ctx context.Context) (int, error) {
	entries, _ := ctx.Value(routeCatalogRevisionsKey{}).([]routeCatalogRevision)
	if len(entries) == 0 {
		return 0, nil
	}
	if len(entries) > MaxRelationshipTargets {
		return 0, NewError("RESOURCE_LIMIT", "Too many route catalog observations.")
	}
	size := 3
	for i, entry := range entries {
		for _, previous := range entries[:i] {
			if previous.provider == entry.provider && previous.route == entry.route {
				return 0, NewError("RESET_REQUIRED", "Duplicate qualified catalog revision.")
			}
		}
		if entry.provider == "" || entry.route.Connection == "" || len(entry.provider) > MaxCatalogRevisionBytes || len(entry.route.Connection) > MaxCatalogRevisionBytes || !utf8.ValidString(entry.provider) || !utf8.ValidString(entry.route.Connection) {
			return 0, NewError("RESET_REQUIRED", "Invalid qualified catalog route.")
		}
		if err := ValidateCatalogRevision(entry.revision); err != nil {
			return 0, err
		}
		size += 6 + len(entry.provider) + len(entry.route.Connection) + len(entry.revision)
	}
	defaultRevision, _ := CatalogRevisionFromContext(ctx)
	if size+len(defaultRevision)+256 > MaxCatalogRevisionMetadataBytes {
		return 0, NewError("RESOURCE_LIMIT", "Catalog revision metadata exceeds bound.")
	}
	return size, nil
}
