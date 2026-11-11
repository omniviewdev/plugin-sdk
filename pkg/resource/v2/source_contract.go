package resourcev2

import (
	"context"
	"reflect"
)

// SourceOperationalContractEqual compares only the selected source contract.
// Both descriptors and the selected request must already be admitted. Provider,
// route, principal, and generation admission remain independently required.
// Presentation, unrelated support, and global revisions do not own the source.
func SourceOperationalContractEqual(previous, current ResourceType, request SourceOpenRequest) bool {
	if previous.Type != request.Target.Type || current.Type != previous.Type ||
		!sameSourceDimensions(previous.IdentityDimensions, current.IdentityDimensions) ||
		!sameSourceDimensions(previous.QueryDimensions, current.QueryDimensions) ||
		!sameSourceAuthorityHint(previous.NativeAuthorityHint, current.NativeAuthorityHint) ||
		!sourceDeclaresBinding(previous, request.Binding.Representation) ||
		!sourceDeclaresBinding(current, request.Binding.Representation) {
		return false
	}
	if request.Mode == SourceModeSnapshotAndUpdates {
		before := SourceGuaranteeForTarget(previous, request.Target)
		return before != "" && before == SourceGuaranteeForTarget(current, request.Target)
	}
	operation := OperationEnumerate
	if request.Acquisition.Kind == SourceAcquisitionNativeQuery {
		operation = OperationNativeQuery
		if !sameSourceNativeQuery(previous, current, request.Acquisition.Language) {
			return false
		}
	}
	if !sourceDeclaresOperation(previous, operation, request.Target.Scope) ||
		!sourceDeclaresOperation(current, operation, request.Target.Scope) {
		return false
	}
	return true
}

// CheckSourceCatalog fences ongoing work against the originally admitted native
// generation and selected operational contract, not an unrelated catalog change.
// Unknown discovery cannot prove withdrawal or authorize unobserved support.
func CheckSourceCatalog(ctx context.Context, catalog Catalog, request SourceOpenRequest, admitted ResourceType) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateCatalog(catalog); err != nil {
		return err
	}
	accepted, _ := CatalogRevisionForRouteFromContext(ctx, request.Target.Provider, request.Target.Route)
	before, err := CatalogBindingEpoch(accepted)
	if err != nil {
		return err
	}
	after, err := CatalogBindingEpoch(catalog.Revision)
	if err != nil {
		return err
	}
	if before != after || catalog.Provider != request.Target.Provider {
		return NewError("RESET_REQUIRED", "Source client generation changed.")
	}
	for _, current := range catalog.Types {
		if current.Type != request.Target.Type {
			continue
		}
		if SourceOperationalContractEqual(admitted, current, request) {
			return nil
		}
		if !sameSourceDimensions(admitted.IdentityDimensions, current.IdentityDimensions) ||
			!sameSourceDimensions(admitted.QueryDimensions, current.QueryDimensions) ||
			current.NativeAuthorityHint != nil && !sameSourceAuthorityHint(admitted.NativeAuthorityHint, current.NativeAuthorityHint) {
			return NewError("RESET_REQUIRED", "Selected source identity changed.")
		}
		for _, format := range current.Representations {
			if format.ID == request.Binding.Representation.ID && format != request.Binding.Representation {
				return NewError("RESET_REQUIRED", "Selected source format changed.")
			}
		}
		if request.Mode == SourceModeSnapshotAndUpdates {
			continuity := SourceGuaranteeForTarget(current, request.Target)
			if continuity != "" && continuity != SourceGuaranteeForTarget(admitted, request.Target) {
				return NewError("RESET_REQUIRED", "Selected source continuity changed.")
			}
		}
		if catalog.Coverage.Kind != "complete" {
			return NewError("UNAVAILABLE", "Selected source support is not fully observed.")
		}
		return NewError("RESET_REQUIRED", "Selected source contract changed.")
	}
	if catalog.Coverage.Kind != "complete" {
		return NewError("UNAVAILABLE", "Selected source support is not fully observed.")
	}
	return NewError("RESET_REQUIRED", "Selected source support was withdrawn.")
}

func sameSourceDimensions(a, b []Dimension) bool {
	if len(a) != len(b) {
		return false
	}
	for _, dimension := range a {
		found := false
		for _, candidate := range b {
			if candidate == dimension {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func sameSourceAuthorityHint(a, b *NativeAuthorityHint) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sourceDeclaresBinding(descriptor ResourceType, binding Representation) bool {
	for _, candidate := range descriptor.Representations {
		if candidate == binding {
			return true
		}
	}
	return false
}

func sourceDeclaresOperation(descriptor ResourceType, operation Operation, scope QueryScope) bool {
	for _, capability := range descriptor.Capabilities {
		if capability.Operation == operation && scopeContains(capability.Scope, scope) {
			return true
		}
	}
	return false
}

func sameSourceNativeQuery(a, b ResourceType, language string) bool {
	if len(a.NativeQueries) == 0 || len(b.NativeQueries) == 0 {
		return len(a.NativeQueries) == 0 && len(b.NativeQueries) == 0
	}
	for _, previous := range a.NativeQueries {
		if previous.Language != language {
			continue
		}
		for _, current := range b.NativeQueries {
			if current.Language == language {
				return reflect.DeepEqual(previous.InputSchema, current.InputSchema)
			}
		}
	}
	return false
}
