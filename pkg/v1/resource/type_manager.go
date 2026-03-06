package resource

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// versionStability returns a sort priority for API version strings.
// Higher = more stable. GA > beta > alpha > unknown.
func versionStability(v string) int {
	switch {
	case !strings.Contains(v, "alpha") && !strings.Contains(v, "beta"):
		return 3 // GA: v1, v2
	case strings.Contains(v, "beta"):
		return 2
	case strings.Contains(v, "alpha"):
		return 1
	default:
		return 0
	}
}

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

// GetResourceGroups returns all resource groups with their Resources fields
// populated from the static registry and any discovered types for the connection.
// Deduplication is applied per (group, Kind): static registrations win over
// discovered, and GA versions win over beta/alpha within the same source.
func (m *typeManager[ClientT]) GetResourceGroups(ctx context.Context, connectionID string) map[string]ResourceGroup {
	staticMetas := m.registry.ListAll()

	m.mu.RLock()
	defer m.mu.RUnlock()

	// Build a set of static keys for exact-key dedup against discovered.
	staticKeys := make(map[string]bool, len(staticMetas))
	for _, meta := range staticMetas {
		staticKeys[meta.Key()] = true
	}

	// Collect all candidate metas: static first, then discovered (minus exact key matches).
	type candidate struct {
		meta     ResourceMeta
		isStatic bool
	}
	var candidates []candidate
	for _, meta := range staticMetas {
		candidates = append(candidates, candidate{meta: meta, isStatic: true})
	}
	if discovered, ok := m.discovered[connectionID]; ok {
		for _, meta := range discovered {
			if staticKeys[meta.Key()] {
				continue // exact key match — static already covers it
			}
			candidates = append(candidates, candidate{meta: meta, isStatic: false})
		}
	}

	// Deduplicate by (group, Kind): keep the best version per Kind in each group.
	// "Best" = static wins over discovered; among equal source, higher versionStability wins.
	type dedupKey struct{ group, kind string }
	best := make(map[dedupKey]candidate)
	for _, c := range candidates {
		dk := dedupKey{c.meta.Group, c.meta.Kind}
		existing, exists := best[dk]
		if !exists {
			best[dk] = c
			continue
		}
		// Static always wins over discovered.
		if c.isStatic && !existing.isStatic {
			best[dk] = c
		} else if c.isStatic == existing.isStatic {
			// Same source — prefer more stable version.
			if versionStability(c.meta.Version) > versionStability(existing.meta.Version) {
				best[dk] = c
			}
		}
	}

	// Build result groups.
	result := make(map[string]ResourceGroup, len(m.groups))
	for _, g := range m.groups {
		result[g.ID] = ResourceGroup{
			ID:          g.ID,
			Name:        g.Name,
			Description: g.Description,
			Icon:        g.Icon,
			Resources:   make(map[string][]ResourceMeta),
		}
	}

	for _, c := range best {
		meta := c.meta
		grp, ok := result[meta.Group]
		if !ok {
			grp = ResourceGroup{
				ID:        meta.Group,
				Name:      meta.Group,
				Resources: make(map[string][]ResourceMeta),
			}
		}
		grp.Resources[meta.Version] = append(grp.Resources[meta.Version], meta)
		result[grp.ID] = grp
	}

	return result
}

// GetResourceGroup returns a single resource group with its Resources populated.
// Applies the same (group, Kind) dedup as GetResourceGroups.
func (m *typeManager[ClientT]) GetResourceGroup(ctx context.Context, id string) (ResourceGroup, error) {
	staticMetas := m.registry.ListAll()

	m.mu.RLock()
	defer m.mu.RUnlock()

	// Find the group definition.
	var grp ResourceGroup
	found := false
	for _, g := range m.groups {
		if g.ID == id {
			grp = ResourceGroup{
				ID:          g.ID,
				Name:        g.Name,
				Description: g.Description,
				Icon:        g.Icon,
				Resources:   make(map[string][]ResourceMeta),
			}
			found = true
			break
		}
	}
	if !found {
		return ResourceGroup{}, fmt.Errorf("resource group %q not found", id)
	}

	// Build a set of static keys for this group.
	staticKeys := make(map[string]bool)
	for _, meta := range staticMetas {
		if meta.Group == id {
			staticKeys[meta.Key()] = true
		}
	}

	// Collect candidates for this group: static first, then discovered.
	type candidate struct {
		meta     ResourceMeta
		isStatic bool
	}
	var candidates []candidate
	for _, meta := range staticMetas {
		if meta.Group == id {
			candidates = append(candidates, candidate{meta: meta, isStatic: true})
		}
	}
	for _, discovered := range m.discovered {
		for _, meta := range discovered {
			if meta.Group != id {
				continue
			}
			if staticKeys[meta.Key()] {
				continue
			}
			candidates = append(candidates, candidate{meta: meta, isStatic: false})
		}
	}

	// Deduplicate by Kind.
	best := make(map[string]candidate) // key = Kind
	for _, c := range candidates {
		existing, exists := best[c.meta.Kind]
		if !exists {
			best[c.meta.Kind] = c
			continue
		}
		if c.isStatic && !existing.isStatic {
			best[c.meta.Kind] = c
		} else if c.isStatic == existing.isStatic {
			if versionStability(c.meta.Version) > versionStability(existing.meta.Version) {
				best[c.meta.Kind] = c
			}
		}
	}

	for _, c := range best {
		grp.Resources[c.meta.Version] = append(grp.Resources[c.meta.Version], c.meta)
	}

	return grp, nil
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

// DiscoveredKeySet returns the set of resource keys discovered for a connection.
// Returns nil if no discovery data exists (nil means "watch all" — safe fallback).
func (m *typeManager[ClientT]) DiscoveredKeySet(connectionID string) map[string]bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	metas, ok := m.discovered[connectionID]
	if !ok || len(metas) == 0 {
		return nil
	}
	set := make(map[string]bool, len(metas))
	for _, meta := range metas {
		set[meta.Key()] = true
	}
	return set
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
