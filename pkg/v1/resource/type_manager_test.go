package resource_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/resource/resourcetest"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// stubDiscoveryProvider implements DiscoveryProvider for testing.
type stubDiscoveryProvider struct {
	DiscoverFunc           func(ctx context.Context, conn *types.Connection) ([]resource.ResourceMeta, error)
	OnConnectionRemovedFunc func(ctx context.Context, conn *types.Connection) error
	RemoveCalls            int
}

func (s *stubDiscoveryProvider) Discover(ctx context.Context, conn *types.Connection) ([]resource.ResourceMeta, error) {
	if s.DiscoverFunc != nil {
		return s.DiscoverFunc(ctx, conn)
	}
	return nil, nil
}

func (s *stubDiscoveryProvider) OnConnectionRemoved(ctx context.Context, conn *types.Connection) error {
	s.RemoveCalls++
	if s.OnConnectionRemovedFunc != nil {
		return s.OnConnectionRemovedFunc(ctx, conn)
	}
	return nil
}

var crdMeta = resource.ResourceMeta{
	Group: "custom.io", Version: "v1", Kind: "Widget",
	Label: "Widget", Category: "Custom",
}

// --- TM-001: Static types from registry ---
func TestTM_StaticTypes(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.DeploymentMeta, Resourcer: &resourcetest.StubResourcer[string]{}})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.ServiceMeta, Resourcer: &resourcetest.StubResourcer[string]{}})

	mgr := resource.NewTypeManagerForTest(reg, nil, nil)
	types := mgr.GetResourceTypes(context.Background(), "conn-1")
	if len(types) != 3 {
		t.Fatalf("expected 3 types, got %d", len(types))
	}
}

// --- TM-002: Discovered types merged with static ---
func TestTM_DiscoveredMerged(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}})

	dp := &stubDiscoveryProvider{
		DiscoverFunc: func(_ context.Context, _ *types.Connection) ([]resource.ResourceMeta, error) {
			return []resource.ResourceMeta{crdMeta}, nil
		},
	}
	mgr := resource.NewTypeManagerForTest(reg, nil, dp)

	conn := &types.Connection{ID: "conn-1"}
	mgr.DiscoverForConnection(context.Background(), conn)

	rt := mgr.GetResourceTypes(context.Background(), "conn-1")
	if len(rt) != 2 {
		t.Fatalf("expected 2 types (Pod + Widget), got %d", len(rt))
	}
}

// --- TM-003: Discovery disabled ---
func TestTM_DiscoveryDisabled(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}})

	mgr := resource.NewTypeManagerForTest(reg, nil, nil)
	rt := mgr.GetResourceTypes(context.Background(), "conn-1")
	if len(rt) != 1 {
		t.Fatalf("expected 1 type, got %d", len(rt))
	}
}

// --- TM-004: Discovery error graceful degradation ---
func TestTM_DiscoveryError(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}})

	dp := &stubDiscoveryProvider{
		DiscoverFunc: func(_ context.Context, _ *types.Connection) ([]resource.ResourceMeta, error) {
			return nil, errors.New("discovery failed")
		},
	}
	mgr := resource.NewTypeManagerForTest(reg, nil, dp)

	conn := &types.Connection{ID: "conn-1"}
	err := mgr.DiscoverForConnection(context.Background(), conn)
	if err != nil {
		t.Fatal("expected nil error (graceful degradation)")
	}

	rt := mgr.GetResourceTypes(context.Background(), "conn-1")
	if len(rt) != 1 {
		t.Fatalf("expected 1 type (static only), got %d", len(rt))
	}
}

// --- TM-006: GetResourceGroup exists ---
func TestTM_GetResourceGroup(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	groups := []resource.ResourceGroup{{ID: "workloads", Name: "Workloads"}}
	mgr := resource.NewTypeManagerForTest(reg, groups, nil)

	g, err := mgr.GetResourceGroup(context.Background(), "workloads")
	if err != nil {
		t.Fatal(err)
	}
	if g.ID != "workloads" {
		t.Fatalf("expected workloads, got %s", g.ID)
	}
}

// --- TM-007: GetResourceGroup not found ---
func TestTM_GetResourceGroupNotFound(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	groups := []resource.ResourceGroup{{ID: "workloads"}}
	mgr := resource.NewTypeManagerForTest(reg, groups, nil)

	_, err := mgr.GetResourceGroup(context.Background(), "networking")
	if err == nil {
		t.Fatal("expected error for missing group")
	}
}

// --- TM-008: OnConnectionRemoved delegates ---
func TestTM_OnConnectionRemoved(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	dp := &stubDiscoveryProvider{}
	mgr := resource.NewTypeManagerForTest(reg, nil, dp)

	conn := &types.Connection{ID: "conn-1"}
	mgr.OnConnectionRemoved(context.Background(), conn)

	if dp.RemoveCalls != 1 {
		t.Fatalf("expected 1 OnConnectionRemoved call, got %d", dp.RemoveCalls)
	}
}

// --- TM-009: OnConnectionRemoved clears discovered types ---
func TestTM_OnConnectionRemovedClearsDiscovered(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}})

	dp := &stubDiscoveryProvider{
		DiscoverFunc: func(_ context.Context, _ *types.Connection) ([]resource.ResourceMeta, error) {
			return []resource.ResourceMeta{crdMeta}, nil
		},
	}
	mgr := resource.NewTypeManagerForTest(reg, nil, dp)

	conn := &types.Connection{ID: "conn-1"}
	mgr.DiscoverForConnection(context.Background(), conn)

	// Should have 2 types.
	if len(mgr.GetResourceTypes(context.Background(), "conn-1")) != 2 {
		t.Fatal("expected 2 types before removal")
	}

	mgr.OnConnectionRemoved(context.Background(), conn)

	// Should be back to 1 (static only).
	if len(mgr.GetResourceTypes(context.Background(), "conn-1")) != 1 {
		t.Fatal("expected 1 type after removal")
	}
}

// --- TM-010: HasResourceType true ---
func TestTM_HasResourceType(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}})
	mgr := resource.NewTypeManagerForTest(reg, nil, nil)

	if !mgr.HasResourceType(context.Background(), "core::v1::Pod") {
		t.Fatal("expected true")
	}
}

// --- TM-011: HasResourceType false ---
func TestTM_HasResourceTypeFalse(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}})
	mgr := resource.NewTypeManagerForTest(reg, nil, nil)

	if mgr.HasResourceType(context.Background(), "apps::v1::Deployment") {
		t.Fatal("expected false")
	}
}

// --- TM-012: GetResourceType returns full meta ---
func TestTM_GetResourceType(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}})
	mgr := resource.NewTypeManagerForTest(reg, nil, nil)

	meta, err := mgr.GetResourceType(context.Background(), "core::v1::Pod")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Kind != "Pod" {
		t.Fatalf("expected Pod, got %s", meta.Kind)
	}
}

// --- TM-005: GetResourceGroups returns configured groups ---
func TestTM_GetResourceGroups(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	groups := []resource.ResourceGroup{
		{ID: "workloads", Name: "Workloads"},
		{ID: "networking", Name: "Networking"},
	}
	mgr := resource.NewTypeManagerForTest(reg, groups, nil)

	got := mgr.GetResourceGroups(context.Background(), "conn-1")
	if len(got) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(got))
	}
	if _, ok := got["workloads"]; !ok {
		t.Fatal("expected workloads group")
	}
	if _, ok := got["networking"]; !ok {
		t.Fatal("expected networking group")
	}
}

// --- TM-013: GetResourceDefinition precedence chain ---
func TestTM_GetResourceDefinition(t *testing.T) {
	defProvider := resource.ResourceDefinition{IDAccessor: "from-provider"}
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{IDAccessor: "from-default"})
	reg.Register(resource.ResourceRegistration[string]{
		Meta:      resourcetest.PodMeta,
		Resourcer: &resourcetest.WatchableResourcer[string]{DefinitionVal: &defProvider},
	})
	mgr := resource.NewTypeManagerForTest(reg, nil, nil)

	def, err := mgr.GetResourceDefinition(context.Background(), "core::v1::Pod")
	if err != nil {
		t.Fatal(err)
	}
	if def.IDAccessor != "from-provider" {
		t.Fatalf("expected DefinitionProvider to win, got %s", def.IDAccessor)
	}
}

// --- TM-014: DiscoveryProvider returns duplicate key as static type ---
func TestTM_DiscoveryDuplicateKey(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}})

	dp := &stubDiscoveryProvider{
		DiscoverFunc: func(_ context.Context, _ *types.Connection) ([]resource.ResourceMeta, error) {
			// Return a discovered type with the same key as a static type.
			return []resource.ResourceMeta{resourcetest.PodMeta}, nil
		},
	}
	mgr := resource.NewTypeManagerForTest(reg, nil, dp)
	conn := &types.Connection{ID: "conn-1"}
	mgr.DiscoverForConnection(context.Background(), conn)

	rt := mgr.GetResourceTypes(context.Background(), "conn-1")
	// Should be 1 — discovered duplicate merges (overwrites) with static.
	if len(rt) != 1 {
		t.Fatalf("expected 1 type (merged), got %d", len(rt))
	}
}

// --- TM-015: DiscoveryProvider returns empty list ---
func TestTM_DiscoveryEmptyList(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}})

	dp := &stubDiscoveryProvider{
		DiscoverFunc: func(_ context.Context, _ *types.Connection) ([]resource.ResourceMeta, error) {
			return []resource.ResourceMeta{}, nil
		},
	}
	mgr := resource.NewTypeManagerForTest(reg, nil, dp)
	conn := &types.Connection{ID: "conn-1"}
	mgr.DiscoverForConnection(context.Background(), conn)

	rt := mgr.GetResourceTypes(context.Background(), "conn-1")
	if len(rt) != 1 {
		t.Fatalf("expected 1 type (static only), got %d", len(rt))
	}
}

// --- TM-016: DiscoverForConnection called multiple times for same connection ---
func TestTM_DiscoveryRefresh(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	callCount := 0
	dp := &stubDiscoveryProvider{
		DiscoverFunc: func(_ context.Context, _ *types.Connection) ([]resource.ResourceMeta, error) {
			callCount++
			if callCount == 1 {
				return []resource.ResourceMeta{crdMeta}, nil
			}
			// Second call discovers an extra type.
			return []resource.ResourceMeta{
				crdMeta,
				{Group: "custom.io", Version: "v1", Kind: "Gadget"},
			}, nil
		},
	}
	mgr := resource.NewTypeManagerForTest(reg, nil, dp)
	conn := &types.Connection{ID: "conn-1"}

	mgr.DiscoverForConnection(context.Background(), conn)
	rt1 := mgr.GetResourceTypes(context.Background(), "conn-1")
	if len(rt1) != 1 {
		t.Fatalf("expected 1 after first discovery, got %d", len(rt1))
	}

	mgr.DiscoverForConnection(context.Background(), conn)
	rt2 := mgr.GetResourceTypes(context.Background(), "conn-1")
	if len(rt2) != 2 {
		t.Fatalf("expected 2 after second discovery, got %d", len(rt2))
	}
}

// --- TM-017: GetResourceTypes for unknown connection ---
func TestTM_GetResourceTypesUnknownConnection(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}})
	mgr := resource.NewTypeManagerForTest(reg, nil, nil)

	// Unknown connection — should still return static types.
	rt := mgr.GetResourceTypes(context.Background(), "unknown-conn")
	if len(rt) != 1 {
		t.Fatalf("expected 1 type (static), got %d", len(rt))
	}
}

// --- TM-019: GetResourceType for discovered type ---
func TestTM_GetResourceTypeDiscovered(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	dp := &stubDiscoveryProvider{
		DiscoverFunc: func(_ context.Context, _ *types.Connection) ([]resource.ResourceMeta, error) {
			return []resource.ResourceMeta{crdMeta}, nil
		},
	}
	mgr := resource.NewTypeManagerForTest(reg, nil, dp)
	conn := &types.Connection{ID: "conn-1"}
	mgr.DiscoverForConnection(context.Background(), conn)

	meta, err := mgr.GetResourceType(context.Background(), crdMeta.Key())
	if err != nil {
		t.Fatal(err)
	}
	if meta.Kind != "Widget" {
		t.Fatalf("expected Widget, got %s", meta.Kind)
	}
}

// --- TM-020: OnConnectionRemoved with nil DiscoveryProvider ---
func TestTM_OnConnectionRemovedNilDiscovery(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	mgr := resource.NewTypeManagerForTest(reg, nil, nil)

	conn := &types.Connection{ID: "conn-1"}
	// Should not panic.
	err := mgr.OnConnectionRemoved(context.Background(), conn)
	if err != nil {
		t.Fatal(err)
	}
}

// --- TM-018: Large number of discovered types (500+) ---
func TestTM_DiscoveryLargeScale(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}})

	var crds []resource.ResourceMeta
	for i := 0; i < 500; i++ {
		crds = append(crds, resource.ResourceMeta{
			Group:    "custom.io",
			Version:  "v1",
			Kind:     fmt.Sprintf("CRD%04d", i),
			Label:    "CRD",
			Category: "Custom",
		})
	}

	dp := &stubDiscoveryProvider{
		DiscoverFunc: func(_ context.Context, _ *types.Connection) ([]resource.ResourceMeta, error) {
			return crds, nil
		},
	}
	mgr := resource.NewTypeManagerForTest(reg, nil, dp)
	conn := &types.Connection{ID: "conn-1"}
	mgr.DiscoverForConnection(context.Background(), conn)

	rt := mgr.GetResourceTypes(context.Background(), "conn-1")
	// 1 static (Pod) + 500 discovered
	if len(rt) != 501 {
		t.Fatalf("expected 501 types, got %d", len(rt))
	}
}
