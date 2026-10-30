package exec

import (
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// ResourceSessionWireVersion selects the reviewed canonical resource Value wire.
const ResourceSessionWireVersion = resourcev2.WireVersion

// ResourceSessionProvider adds explicit resource-scoped creation while reusing
// ordinary exec session IDs and attach/output/resize/close lifecycle. The body
// and opaque utility selectors are input, never authorization. Existing routing
// does not require a resource Get grant or an invented canonical reference.
type ResourceSessionProvider interface {
	CreateResourceSession(*types.PluginContext, SessionOptions) (*Session, error)
}

// HasResourceIntent prevents resource requests from falling back to an older
// terminal method, including selectors without a body and optional C01 inputs.
func (o SessionOptions) HasResourceIntent() bool {
	return o.ResourceData != nil || o.ResourcePlugin != "" || o.ResourceKey != "" || o.Reference != nil ||
		o.Route != nil
}

func validateResourceSessionResult(session *Session, requestedID string) error {
	if session == nil || session.ID == "" || (requestedID != "" && session.ID != requestedID) {
		return resourcev2.NewError(
			"PROVIDER_ERROR",
			"Resource exec response changed or omitted the requested session ID.",
		)
	}
	return resourcev2.ValidateMetadata(session)
}
