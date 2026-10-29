package resourcev2

import (
	"context"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// WithUtilityConnection binds native utility creation/query to the sole active
// connection registry. Metadata selects; it cannot supply a configured profile.
// An actual canonical reference additionally requires the exact host-admitted
// catalog observation. Opaque nonreference utility inputs do not invent one.
// Explicit routes require a selected configured connection even without a reference.
//
// A nil callback is invalid. With no selected connection and no reference or
// route, the callback runs directly and receives pc unchanged, possibly nil.
// A selected connection requires a nonnil provider; a canonical reference also
// requires that provider to implement Provider for revision checks before and
// after the callback. This helper neither performs Get nor authorizes the target.
//
// A nil pc.Context uses Background for registry admission. On the admitted path,
// the callback receives a shallow PluginContext copy with the leased context and
// actual configured profile. Other PluginContext fields retain their ownership.
// Honor the lease context and do not synchronously retire the same generation.
// The callback's lease ends on return; accepted long-lived sessions need their
// own cancellation/retirement cleanup rather than retaining this request lease.
func WithUtilityConnection(pc *types.PluginContext, provider ConnectionContextProvider, reference *ResourceReference, route *AccessRoute, fn func(*types.PluginContext) error) error {
	if fn == nil {
		return NewError("INVALID_REQUEST", "Utility callback is required.")
	}
	ctx := context.Background()
	if pc != nil && pc.Context != nil {
		ctx = pc.Context
	}
	if pc == nil || pc.Connection == nil {
		if reference != nil {
			return NewError("RESET_REQUIRED", "Canonical utility reference requires its admitted connection.")
		}
		if route != nil {
			return NewError("INVALID_REQUEST", "Utility route requires an actual selected connection.")
		}
		return fn(pc)
	}
	selected := AccessRoute{Connection: pc.Connection.ID}
	if route != nil && route.Connection != selected.Connection {
		return NewError("INVALID_REQUEST", "Utility route conflicts with selected connection.")
	}
	if provider == nil {
		return NewError("UNSUPPORTED", "Configured utility connection context is unavailable.")
	}
	catalogProvider, hasCatalog := provider.(Provider)
	if reference != nil && !hasCatalog {
		return NewError("UNSUPPORTED", "Canonical utility catalog admission is unavailable.")
	}
	return provider.WithConnection(ctx, selected, func(bound context.Context, connection types.Connection) error {
		check := func() error {
			if reference == nil {
				return nil
			}
			catalog, err := catalogProvider.Catalog(bound, CatalogRequest{Route: selected})
			if err != nil {
				return err
			}
			return CheckCatalogRevisionForRoute(bound, catalog, selected)
		}
		if err := check(); err != nil {
			return err
		}
		copy := *pc
		copy.Context = bound
		copy.Connection = &connection
		if err := fn(&copy); err != nil {
			return err
		}
		if err := bound.Err(); err != nil {
			return err
		}
		return check()
	})
}
