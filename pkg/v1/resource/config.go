package resource

import (
	"fmt"
	"reflect"

	logging "github.com/omniviewdev/plugin-sdk/log"
)

// ResourcePluginConfig holds all configuration for a resource plugin.
// Passed to RegisterResourcePlugin to register the resource capability.
type ResourcePluginConfig[ClientT any] struct {
	// Logger is an optional logger for the resource controller subsystem.
	// nil → logging.NewNop().
	Logger logging.Logger

	// Connections is the required connection provider.
	// All plugins must supply this.
	Connections ConnectionProvider[ClientT]

	// Resources is the list of resource registrations.
	// Each entry binds a resource type to its Resourcer implementation.
	// At least one of Resources or Patterns must be non-empty.
	Resources []ResourceRegistration[ClientT]

	// Patterns is a map of wildcard patterns to fallback Resourcers.
	// The key "*" matches any resource type not registered in Resources.
	// Used for dynamic resource types like CRDs.
	Patterns map[string]Resourcer[ClientT]

	// Groups defines the resource groups for sidebar organization.
	Groups []ResourceGroup

	// DefaultDefinition is the fallback resource definition used when
	// neither the Resourcer (via DefinitionProvider) nor the
	// ResourceRegistration.Definition provides one.
	DefaultDefinition ResourceDefinition

	// Discovery is an optional provider for dynamic resource type discovery.
	// nil means static types only.
	Discovery DiscoveryProvider

	// ErrorClassifier is an optional plugin-wide error classifier.
	// nil means no classification (raw errors pass through).
	// Per-resource ErrorClassifier on a Resourcer takes precedence.
	ErrorClassifier ErrorClassifier

	// Watch support: auto-detected on Resourcer implementations via
	// Watcher[ClientT] type assertion. No config field needed.

	// Schemas: if ConnectionProvider also implements SchemaProvider[ClientT],
	// the SDK auto-detects and wires it. No separate field needed.
}

// Validate checks documented invariants and returns a descriptive error
// if the config is invalid. Uses reflect-based checks to catch typed-nil
// interfaces (e.g., (*T)(nil)) that pass simple == nil comparisons.
func (c ResourcePluginConfig[ClientT]) Validate() error {
	if isNilInterface(c.Connections) {
		return fmt.Errorf("ResourcePluginConfig: Connections provider is required")
	}
	if len(c.Resources) == 0 && len(c.Patterns) == 0 {
		return fmt.Errorf("ResourcePluginConfig: at least one of Resources or Patterns must be non-empty")
	}
	for i, reg := range c.Resources {
		if isNilInterface(reg.Resourcer) {
			return fmt.Errorf("ResourcePluginConfig: Resources[%d] (%s) has nil Resourcer", i, reg.Meta.Key())
		}
	}
	for pattern, res := range c.Patterns {
		if isNilInterface(res) {
			return fmt.Errorf("ResourcePluginConfig: Patterns[%q] has nil Resourcer", pattern)
		}
	}
	return nil
}

// isNilInterface returns true if v is nil or a typed-nil (non-nil interface
// wrapping a nil pointer/map/slice/chan/func).
func isNilInterface(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
		return rv.IsNil()
	}
	return false
}

// ResourceRegistration binds a resource type to its implementation.
// Co-locates metadata, resourcer, and definition.
type ResourceRegistration[ClientT any] struct {
	// Meta identifies the resource type (group, version, kind).
	Meta ResourceMeta

	// Resourcer is the implementation that handles CRUD for this resource type.
	// May also implement optional interfaces (Watcher, ActionResourcer,
	// DefinitionProvider, etc.) which are detected via type assertion.
	Resourcer Resourcer[ClientT]

	// Definition is an optional fallback resource definition (column defs, ID accessor, etc.).
	// Precedence: if the Resourcer implements DefinitionProvider, that wins.
	// Otherwise, this Definition is used. If both are nil, the config's
	// DefaultDefinition applies.
	Definition *ResourceDefinition
}
