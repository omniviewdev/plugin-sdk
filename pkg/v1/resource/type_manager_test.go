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

// --- TM-006: GetResourceGroup exists and has populated resources ---
func TestTM_GetResourceGroup(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	// Pod has Group: "core" — we'll use "core" as the group ID.
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.ServiceMeta, Resourcer: &resourcetest.StubResourcer[string]{}})

	groups := []resource.ResourceGroup{{ID: "core", Name: "Core"}}
	mgr := resource.NewTypeManagerForTest(reg, groups, nil)

	g, err := mgr.GetResourceGroup(context.Background(), "core")
	if err != nil {
		t.Fatal(err)
	}
	if g.ID != "core" {
		t.Fatalf("expected core, got %s", g.ID)
	}
	if g.Resources == nil {
		t.Fatal("expected Resources to be populated")
	}
	if len(g.Resources["v1"]) != 2 {
		t.Fatalf("expected 2 resources in core/v1, got %d", len(g.Resources["v1"]))
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

// --- TM-005: GetResourceGroups returns configured groups with populated resources ---
func TestTM_GetResourceGroups(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	// Register resources whose Group matches the group IDs.
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}})         // Group: "core"
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.DeploymentMeta, Resourcer: &resourcetest.StubResourcer[string]{}})   // Group: "apps"
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.ServiceMeta, Resourcer: &resourcetest.StubResourcer[string]{}})      // Group: "core"

	groups := []resource.ResourceGroup{
		{ID: "core", Name: "Core"},
		{ID: "apps", Name: "Apps"},
	}
	mgr := resource.NewTypeManagerForTest(reg, groups, nil)

	got := mgr.GetResourceGroups(context.Background(), "conn-1")
	if len(got) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(got))
	}

	// Core group should have Pod and Service under v1.
	coreGroup := got["core"]
	if coreGroup.Resources == nil {
		t.Fatal("expected core group to have Resources populated")
	}
	v1Resources := coreGroup.Resources["v1"]
	if len(v1Resources) != 2 {
		t.Fatalf("expected 2 resources in core/v1, got %d", len(v1Resources))
	}

	// Apps group should have Deployment under v1.
	appsGroup := got["apps"]
	if len(appsGroup.Resources["v1"]) != 1 {
		t.Fatalf("expected 1 resource in apps/v1, got %d", len(appsGroup.Resources["v1"]))
	}
	if appsGroup.Resources["v1"][0].Kind != "Deployment" {
		t.Fatalf("expected Deployment, got %s", appsGroup.Resources["v1"][0].Kind)
	}
}

// --- TM-005b: GetResourceGroups creates dynamic groups for unmatched resource groups ---
func TestTM_GetResourceGroupsDynamicGroup(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	// Register a resource whose Group does NOT match any static group.
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}}) // Group: "core"

	groups := []resource.ResourceGroup{
		{ID: "apps", Name: "Apps"}, // No "core" group defined.
	}
	mgr := resource.NewTypeManagerForTest(reg, groups, nil)

	got := mgr.GetResourceGroups(context.Background(), "conn-1")
	// Should have apps (empty) + core (dynamic).
	if len(got) != 2 {
		t.Fatalf("expected 2 groups (apps + dynamic core), got %d", len(got))
	}
	coreGroup, ok := got["core"]
	if !ok {
		t.Fatal("expected dynamic 'core' group to be created")
	}
	if len(coreGroup.Resources["v1"]) != 1 {
		t.Fatalf("expected 1 resource in dynamic core/v1, got %d", len(coreGroup.Resources["v1"]))
	}
}

// --- TM-005c: GetResourceGroups includes discovered resources ---
func TestTM_GetResourceGroupsWithDiscovered(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}})

	dp := &stubDiscoveryProvider{
		DiscoverFunc: func(_ context.Context, _ *types.Connection) ([]resource.ResourceMeta, error) {
			return []resource.ResourceMeta{crdMeta}, nil // Group: "custom.io"
		},
	}
	groups := []resource.ResourceGroup{
		{ID: "core", Name: "Core"},
	}
	mgr := resource.NewTypeManagerForTest(reg, groups, dp)

	conn := &types.Connection{ID: "conn-1"}
	mgr.DiscoverForConnection(context.Background(), conn)

	got := mgr.GetResourceGroups(context.Background(), "conn-1")
	// Should have core (with Pod) + custom.io (dynamic, with Widget).
	if len(got) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(got))
	}

	customGroup, ok := got["custom.io"]
	if !ok {
		t.Fatal("expected dynamic 'custom.io' group for discovered CRD")
	}
	if len(customGroup.Resources["v1"]) != 1 {
		t.Fatalf("expected 1 discovered resource, got %d", len(customGroup.Resources["v1"]))
	}
	if customGroup.Resources["v1"][0].Kind != "Widget" {
		t.Fatalf("expected Widget, got %s", customGroup.Resources["v1"][0].Kind)
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

// --- TM-021: GetResourceGroups deduplicates by Kind across versions ---
func TestTM_GetResourceGroupsDeduplicatesByKind(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})

	// Register HPA under three versions in the same group.
	hpaV1 := resource.ResourceMeta{Group: "autoscaling", Version: "v1", Kind: "HorizontalPodAutoscaler", Label: "HPA"}
	hpaV2 := resource.ResourceMeta{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler", Label: "HPA"}
	hpaV2beta2 := resource.ResourceMeta{Group: "autoscaling", Version: "v2beta2", Kind: "HorizontalPodAutoscaler", Label: "HPA"}

	reg.Register(resource.ResourceRegistration[string]{Meta: hpaV1, Resourcer: &resourcetest.StubResourcer[string]{}})
	reg.Register(resource.ResourceRegistration[string]{Meta: hpaV2, Resourcer: &resourcetest.StubResourcer[string]{}})
	reg.Register(resource.ResourceRegistration[string]{Meta: hpaV2beta2, Resourcer: &resourcetest.StubResourcer[string]{}})

	groups := []resource.ResourceGroup{{ID: "autoscaling", Name: "Autoscaling"}}
	mgr := resource.NewTypeManagerForTest(reg, groups, nil)

	got := mgr.GetResourceGroups(context.Background(), "conn-1")
	asGroup := got["autoscaling"]

	// Count total HPA entries across all versions — should be exactly 1.
	totalHPA := 0
	for _, metas := range asGroup.Resources {
		for _, m := range metas {
			if m.Kind == "HorizontalPodAutoscaler" {
				totalHPA++
			}
		}
	}
	if totalHPA != 1 {
		t.Fatalf("expected 1 HPA (deduped), got %d", totalHPA)
	}

	// The winner should be the most stable GA version (v2 > v1 because higher number,
	// but our heuristic only distinguishes GA vs beta vs alpha — v1 and v2 are both GA).
	// Either v1 or v2 is acceptable; v2beta2 must NOT be chosen.
	for ver := range asGroup.Resources {
		if ver == "v2beta2" {
			t.Fatal("v2beta2 should not be chosen over GA versions")
		}
	}
}

// --- TM-022: GetResourceGroups prefers static over discovered ---
func TestTM_GetResourceGroupsStaticWinsOverDiscovered(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	staticPod := resource.ResourceMeta{Group: "core", Version: "v1", Kind: "Pod", Label: "Pod", Category: "Workloads"}
	reg.Register(resource.ResourceRegistration[string]{Meta: staticPod, Resourcer: &resourcetest.StubResourcer[string]{}})

	dp := &stubDiscoveryProvider{
		DiscoverFunc: func(_ context.Context, _ *types.Connection) ([]resource.ResourceMeta, error) {
			// Discovered Pod with same key — should be deduped against static.
			return []resource.ResourceMeta{
				{Group: "core", Version: "v1", Kind: "Pod"},
			}, nil
		},
	}
	groups := []resource.ResourceGroup{{ID: "core", Name: "Core"}}
	mgr := resource.NewTypeManagerForTest(reg, groups, dp)

	conn := &types.Connection{ID: "conn-1"}
	mgr.DiscoverForConnection(context.Background(), conn)

	got := mgr.GetResourceGroups(context.Background(), "conn-1")
	coreGroup := got["core"]

	totalPod := 0
	for _, metas := range coreGroup.Resources {
		for _, m := range metas {
			if m.Kind == "Pod" {
				totalPod++
			}
		}
	}
	if totalPod != 1 {
		t.Fatalf("expected 1 Pod (static wins), got %d", totalPod)
	}

	// Verify it kept the static version (which has Label set).
	for _, metas := range coreGroup.Resources {
		for _, m := range metas {
			if m.Kind == "Pod" && m.Label != "Pod" {
				t.Fatalf("expected static Pod (Label=Pod), got Label=%s", m.Label)
			}
		}
	}
}

// --- TM-023: GetResourceGroups deduplicates discovered versions for same Kind ---
func TestTM_GetResourceGroupsDiscoveredMultiVersion(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})

	dp := &stubDiscoveryProvider{
		DiscoverFunc: func(_ context.Context, _ *types.Connection) ([]resource.ResourceMeta, error) {
			return []resource.ResourceMeta{
				{Group: "autoscaling", Version: "v1", Kind: "HorizontalPodAutoscaler"},
				{Group: "autoscaling", Version: "v2", Kind: "HorizontalPodAutoscaler"},
				{Group: "autoscaling", Version: "v2beta2", Kind: "HorizontalPodAutoscaler"},
			}, nil
		},
	}
	groups := []resource.ResourceGroup{{ID: "autoscaling", Name: "Autoscaling"}}
	mgr := resource.NewTypeManagerForTest(reg, groups, dp)

	conn := &types.Connection{ID: "conn-1"}
	mgr.DiscoverForConnection(context.Background(), conn)

	got := mgr.GetResourceGroups(context.Background(), "conn-1")
	asGroup := got["autoscaling"]

	totalHPA := 0
	for _, metas := range asGroup.Resources {
		for _, m := range metas {
			if m.Kind == "HorizontalPodAutoscaler" {
				totalHPA++
			}
		}
	}
	if totalHPA != 1 {
		t.Fatalf("expected 1 HPA (deduped), got %d", totalHPA)
	}
}

// --- TM-024: GetResourceGroup applies same dedup ---
func TestTM_GetResourceGroupDedup(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	hpaV1 := resource.ResourceMeta{Group: "autoscaling", Version: "v1", Kind: "HPA"}
	hpaV2beta1 := resource.ResourceMeta{Group: "autoscaling", Version: "v2beta1", Kind: "HPA"}
	reg.Register(resource.ResourceRegistration[string]{Meta: hpaV1, Resourcer: &resourcetest.StubResourcer[string]{}})
	reg.Register(resource.ResourceRegistration[string]{Meta: hpaV2beta1, Resourcer: &resourcetest.StubResourcer[string]{}})

	groups := []resource.ResourceGroup{{ID: "autoscaling", Name: "Autoscaling"}}
	mgr := resource.NewTypeManagerForTest(reg, groups, nil)

	grp, err := mgr.GetResourceGroup(context.Background(), "autoscaling")
	if err != nil {
		t.Fatal(err)
	}

	totalHPA := 0
	for _, metas := range grp.Resources {
		for _, m := range metas {
			if m.Kind == "HPA" {
				totalHPA++
			}
		}
	}
	if totalHPA != 1 {
		t.Fatalf("expected 1 HPA (deduped), got %d", totalHPA)
	}

	// GA v1 should win over v2beta1.
	if _, ok := grp.Resources["v2beta1"]; ok {
		t.Fatal("v2beta1 should not be chosen over GA v1")
	}
}

// --- TM-025: CRD groups create dynamic groups and are not deduped ---
func TestTM_GetResourceGroupsDynamicCRDGroup(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})

	dp := &stubDiscoveryProvider{
		DiscoverFunc: func(_ context.Context, _ *types.Connection) ([]resource.ResourceMeta, error) {
			return []resource.ResourceMeta{
				{Group: "stable.example.com", Version: "v1", Kind: "Widget"},
				{Group: "stable.example.com", Version: "v1", Kind: "Gadget"},
			}, nil
		},
	}
	mgr := resource.NewTypeManagerForTest(reg, nil, dp)

	conn := &types.Connection{ID: "conn-1"}
	mgr.DiscoverForConnection(context.Background(), conn)

	got := mgr.GetResourceGroups(context.Background(), "conn-1")
	crdGroup, ok := got["stable.example.com"]
	if !ok {
		t.Fatal("expected dynamic group for CRD")
	}
	if len(crdGroup.Resources["v1"]) != 2 {
		t.Fatalf("expected 2 CRD resources, got %d", len(crdGroup.Resources["v1"]))
	}
}

// --- TM-026: DiscoveredKeySet returns correct keys after discovery ---
func TestTM_DiscoveredKeySet_AfterDiscovery(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}})

	dp := &stubDiscoveryProvider{
		DiscoverFunc: func(_ context.Context, _ *types.Connection) ([]resource.ResourceMeta, error) {
			return []resource.ResourceMeta{
				resourcetest.PodMeta,
				resourcetest.DeploymentMeta,
				crdMeta,
			}, nil
		},
	}
	mgr := resource.NewTypeManagerForTest(reg, nil, dp)
	conn := &types.Connection{ID: "conn-1"}
	mgr.DiscoverForConnection(context.Background(), conn)

	keySet := resource.DiscoveredKeySetForTest(mgr, "conn-1")
	if keySet == nil {
		t.Fatal("expected non-nil key set after discovery")
	}
	if len(keySet) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(keySet))
	}
	if !keySet[resourcetest.PodMeta.Key()] {
		t.Fatal("expected Pod key in set")
	}
	if !keySet[resourcetest.DeploymentMeta.Key()] {
		t.Fatal("expected Deployment key in set")
	}
	if !keySet[crdMeta.Key()] {
		t.Fatal("expected Widget key in set")
	}
}

// --- TM-027: DiscoveredKeySet returns nil when no discovery has been performed ---
func TestTM_DiscoveredKeySet_NoDiscovery(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}})

	mgr := resource.NewTypeManagerForTest(reg, nil, nil)

	keySet := resource.DiscoveredKeySetForTest(mgr, "conn-1")
	if keySet != nil {
		t.Fatalf("expected nil key set (no discovery), got %v", keySet)
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
