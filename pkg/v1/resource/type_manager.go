package resource

import (
	"context"
	"fmt"
	"sync"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// typeManager manages static and dynamically discovered resource types.
type typeManager[ClientT any] struct {
	mu         sync.RWMutex
	registry   *resourcerRegistry[ClientT]
	groups     []ResourceGroup
	discovery  DiscoveryProvider
	discovered map[string][]ResourceMeta // per connection ID
}

func newTypeManager[ClientT any](
	registry *resourcerRegistry[ClientT],
	groups []ResourceGroup,
	discovery DiscoveryProvider,
) *typeManager[ClientT] {
	return &typeManager[ClientT]{
		registry:   registry,
		groups:     groups,
		discovery:  discovery,
		discovered: make(map[string][]ResourceMeta),
	}
}

// GetResourceGroups returns all resource groups, including discovered resources.
func (m *typeManager[ClientT]) GetResourceGroups(ctx context.Context, connectionID string) map[string]ResourceGroup {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]ResourceGroup, len(m.groups))
	for _, g := range m.groups {
		result[g.ID] = g
	}
	return result
}

// GetResourceGroup returns a single resource group.
func (m *typeManager[ClientT]) GetResourceGroup(ctx context.Context, id string) (ResourceGroup, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, g := range m.groups {
		if g.ID == id {
			return g, nil
		}
	}
	return ResourceGroup{}, fmt.Errorf("resource group %q not found", id)
}

// GetResourceTypes returns all resource types (static + discovered) for a connection.
func (m *typeManager[ClientT]) GetResourceTypes(ctx context.Context, connectionID string) map[string]ResourceMeta {
	staticMetas := m.registry.ListAll()
	result := make(map[string]ResourceMeta, len(staticMetas))
	for _, meta := range staticMetas {
		result[meta.Key()] = meta
	}

	m.mu.RLock()
	if discovered, ok := m.discovered[connectionID]; ok {
		for _, meta := range discovered {
			result[meta.Key()] = meta
		}
	}
	m.mu.RUnlock()

	return result
}

// GetResourceType returns a single resource type by key.
func (m *typeManager[ClientT]) GetResourceType(ctx context.Context, key string) (*ResourceMeta, error) {
	if meta, ok := m.registry.LookupMeta(key); ok {
		return &meta, nil
	}

	// Check discovered types across all connections.
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, metas := range m.discovered {
		for _, meta := range metas {
			if meta.Key() == key {
				return &meta, nil
			}
		}
	}
	return nil, fmt.Errorf("resource type %q not found", key)
}

// HasResourceType checks whether a resource type exists (static or discovered).
func (m *typeManager[ClientT]) HasResourceType(ctx context.Context, key string) bool {
	if _, ok := m.registry.LookupMeta(key); ok {
		return true
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, metas := range m.discovered {
		for _, meta := range metas {
			if meta.Key() == key {
				return true
			}
		}
	}
	return false
}

// GetResourceDefinition returns the definition for a resource type.
func (m *typeManager[ClientT]) GetResourceDefinition(ctx context.Context, key string) (ResourceDefinition, error) {
	return m.registry.GetDefinition(key), nil
}

// GetResourceCapabilities returns the auto-derived capabilities for a resource type.
func (m *typeManager[ClientT]) GetResourceCapabilities(ctx context.Context, key string) (*ResourceCapabilities, error) {
	caps := m.registry.DeriveCapabilities(key)
	if caps == nil {
		return nil, fmt.Errorf("resource type %q not found", key)
	}
	return caps, nil
}

// DiscoverForConnection runs discovery for a connection and caches the results.
func (m *typeManager[ClientT]) DiscoverForConnection(ctx context.Context, conn *types.Connection) error {
	if m.discovery == nil {
		return nil
	}
	metas, err := m.discovery.Discover(ctx, conn)
	if err != nil {
		// Graceful degradation — don't fail, just skip discovery.
		return nil
	}
	m.mu.Lock()
	m.discovered[conn.ID] = metas
	m.mu.Unlock()
	return nil
}

// OnConnectionRemoved cleans up discovered types for a removed connection.
func (m *typeManager[ClientT]) OnConnectionRemoved(ctx context.Context, conn *types.Connection) error {
	m.mu.Lock()
	delete(m.discovered, conn.ID)
	m.mu.Unlock()

	if m.discovery != nil {
		return m.discovery.OnConnectionRemoved(ctx, conn)
	}
	return nil
}
