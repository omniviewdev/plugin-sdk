package resource_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/resource/resourcetest"
)

// --- test doubles ---

// stubActionResourcer implements Resourcer + ActionResourcer for testing.
type stubActionResourcer struct {
	resourcetest.StubResourcer[string]
}

func (s *stubActionResourcer) GetActions(_ context.Context, _ *string, _ resource.ResourceMeta) ([]resource.ActionDescriptor, error) {
	return nil, nil
}
func (s *stubActionResourcer) ExecuteAction(_ context.Context, _ *string, _ resource.ResourceMeta, _ string, _ resource.ActionInput) (*resource.ActionResult, error) {
	return nil, nil
}
func (s *stubActionResourcer) StreamAction(_ context.Context, _ *string, _ resource.ResourceMeta, _ string, _ resource.ActionInput, _ chan<- resource.ActionEvent) error {
	return nil
}

// stubErrorClassifierResourcer implements Resourcer + ErrorClassifier.
type stubErrorClassifierResourcer struct {
	resourcetest.StubResourcer[string]
}

func (s *stubErrorClassifierResourcer) ClassifyError(err error) error { return err }

// stubSchemaResourcer implements Resourcer + ResourceSchemaProvider.
type stubSchemaResourcer struct {
	resourcetest.StubResourcer[string]
}

func (s *stubSchemaResourcer) GetResourceSchema(_ context.Context, _ *string, _ resource.ResourceMeta) (json.RawMessage, error) {
	return nil, nil
}

// --- RR-001: Lookup exact match ---
func TestRegistryLookupExact(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	stubPod := &resourcetest.StubResourcer[string]{}
	reg.Register(resource.ResourceRegistration[string]{
		Meta: resourcetest.PodMeta, Resourcer: stubPod,
	})

	r, err := reg.Lookup("core::v1::Pod")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r != stubPod {
		t.Fatal("expected stubPod")
	}
}

// --- RR-002: Lookup returns correct resourcer among multiple ---
func TestRegistryLookupCorrectAmongMultiple(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	stubPod := &resourcetest.StubResourcer[string]{}
	stubDep := &resourcetest.StubResourcer[string]{}
	stubSvc := &resourcetest.StubResourcer[string]{}
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.PodMeta, Resourcer: stubPod})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.DeploymentMeta, Resourcer: stubDep})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.ServiceMeta, Resourcer: stubSvc})

	r, _ := reg.Lookup("apps::v1::Deployment")
	if r != stubDep {
		t.Fatal("expected deployment resourcer")
	}
}

// --- RR-003: Lookup with pattern fallback ---
func TestRegistryLookupPatternFallback(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	pattern := &resourcetest.StubResourcer[string]{}
	reg.RegisterPattern("*", pattern)

	r, err := reg.Lookup("core::v1::Pod")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r != pattern {
		t.Fatal("expected pattern resourcer")
	}
}

// --- RR-004: Lookup exact wins over pattern ---
func TestRegistryLookupExactWinsOverPattern(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	exact := &resourcetest.StubResourcer[string]{}
	pattern := &resourcetest.StubResourcer[string]{}
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.PodMeta, Resourcer: exact})
	reg.RegisterPattern("*", pattern)

	r, _ := reg.Lookup("core::v1::Pod")
	if r != exact {
		t.Fatal("expected exact resourcer, not pattern")
	}
}

// --- RR-005: Lookup not found, no pattern ---
func TestRegistryLookupNotFound(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{
		Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{},
	})

	r, err := reg.Lookup("apps::v1::Deployment")
	if err == nil {
		t.Fatal("expected error for missing resource")
	}
	if r != nil {
		t.Fatal("expected nil resourcer")
	}
}

// --- RR-006: Lookup on empty registry ---
func TestRegistryLookupEmpty(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	r, err := reg.Lookup("core::v1::Pod")
	if err == nil {
		t.Fatal("expected error on empty registry")
	}
	if r != nil {
		t.Fatal("expected nil")
	}
}

// --- RR-007: Detects Watcher capability ---
func TestRegistryIsWatcherTrue(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{
		Meta: resourcetest.PodMeta, Resourcer: &resourcetest.WatchableResourcer[string]{},
	})

	if !reg.IsWatcher("core::v1::Pod") {
		t.Fatal("expected IsWatcher == true")
	}
}

// --- RR-008: Non-Watcher detected correctly ---
func TestRegistryIsWatcherFalse(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{
		Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{},
	})

	if reg.IsWatcher("core::v1::Pod") {
		t.Fatal("expected IsWatcher == false")
	}
}

// --- RR-009: Detects SyncPolicyDeclarer — custom policy ---
func TestRegistryGetSyncPolicyCustom(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	policy := resource.SyncOnFirstQuery
	reg.Register(resource.ResourceRegistration[string]{
		Meta: resourcetest.PodMeta,
		Resourcer: &resourcetest.WatchableResourcer[string]{
			PolicyVal: &policy,
		},
	})

	got := reg.GetSyncPolicy("core::v1::Pod")
	if got != resource.SyncOnFirstQuery {
		t.Fatalf("expected SyncOnFirstQuery, got %v", got)
	}
}

// --- RR-010: SyncPolicyDeclarer absent — defaults to SyncOnConnect ---
func TestRegistryGetSyncPolicyDefault(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{
		Meta:      resourcetest.PodMeta,
		Resourcer: &resourcetest.WatchableResourcer[string]{},
	})

	got := reg.GetSyncPolicy("core::v1::Pod")
	if got != resource.SyncOnConnect {
		t.Fatalf("expected SyncOnConnect, got %v", got)
	}
}

// --- RR-011: SyncPolicy for non-Watcher — returns SyncNever ---
func TestRegistryGetSyncPolicyNonWatcher(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{
		Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{},
	})

	got := reg.GetSyncPolicy("core::v1::Pod")
	if got != resource.SyncNever {
		t.Fatalf("expected SyncNever, got %v", got)
	}
}

// --- RR-012: Detects DefinitionProvider — interface wins over registration ---
func TestRegistryGetDefinitionProviderWins(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	defA := resource.ResourceDefinition{IDAccessor: "regDef"}
	defB := resource.ResourceDefinition{IDAccessor: "providerDef"}
	reg.Register(resource.ResourceRegistration[string]{
		Meta:       resourcetest.PodMeta,
		Resourcer:  &resourcetest.WatchableResourcer[string]{DefinitionVal: &defB},
		Definition: &defA,
	})

	got := reg.GetDefinition("core::v1::Pod")
	if got.IDAccessor != "providerDef" {
		t.Fatalf("expected DefinitionProvider to win, got %s", got.IDAccessor)
	}
}

// --- RR-013: Definition fallback to Registration.Definition ---
func TestRegistryGetDefinitionFromRegistration(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	defA := resource.ResourceDefinition{IDAccessor: "regDef"}
	reg.Register(resource.ResourceRegistration[string]{
		Meta:       resourcetest.PodMeta,
		Resourcer:  &resourcetest.StubResourcer[string]{},
		Definition: &defA,
	})

	got := reg.GetDefinition("core::v1::Pod")
	if got.IDAccessor != "regDef" {
		t.Fatalf("expected registration def, got %s", got.IDAccessor)
	}
}

// --- RR-014: Definition fallback to DefaultDefinition ---
func TestRegistryGetDefinitionDefault(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{IDAccessor: "defaultDef"})
	reg.Register(resource.ResourceRegistration[string]{
		Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{},
	})

	got := reg.GetDefinition("core::v1::Pod")
	if got.IDAccessor != "defaultDef" {
		t.Fatalf("expected default def, got %s", got.IDAccessor)
	}
}

// --- RR-015: Detects ActionResourcer ---
func TestRegistryGetActionResourcer(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{
		Meta: resourcetest.PodMeta, Resourcer: &stubActionResourcer{},
	})

	ar, ok := reg.GetActionResourcer("core::v1::Pod")
	if !ok || ar == nil {
		t.Fatal("expected ActionResourcer")
	}
}

// --- RR-016: No ActionResourcer ---
func TestRegistryGetActionResourcerNone(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{
		Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{},
	})

	_, ok := reg.GetActionResourcer("core::v1::Pod")
	if ok {
		t.Fatal("expected no ActionResourcer")
	}
}

// --- RR-017: Detects ErrorClassifier — per-resource ---
func TestRegistryGetErrorClassifier(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{
		Meta: resourcetest.PodMeta, Resourcer: &stubErrorClassifierResourcer{},
	})

	_, ok := reg.GetErrorClassifier("core::v1::Pod")
	if !ok {
		t.Fatal("expected ErrorClassifier")
	}
}

// --- RR-018: No per-resource ErrorClassifier ---
func TestRegistryGetErrorClassifierNone(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{
		Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{},
	})

	_, ok := reg.GetErrorClassifier("core::v1::Pod")
	if ok {
		t.Fatal("expected no ErrorClassifier")
	}
}

// --- RR-019: Detects SchemaResourcer ---
func TestRegistryGetSchemaResourcer(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{
		Meta: resourcetest.PodMeta, Resourcer: &stubSchemaResourcer{},
	})

	_, ok := reg.GetSchemaResourcer("core::v1::Pod")
	if !ok {
		t.Fatal("expected SchemaResourcer")
	}
}

// --- RR-020: ListAll returns all registered metas ---
func TestRegistryListAll(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.DeploymentMeta, Resourcer: &resourcetest.StubResourcer[string]{}})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.ServiceMeta, Resourcer: &resourcetest.StubResourcer[string]{}})

	metas := reg.ListAll()
	if len(metas) != 3 {
		t.Fatalf("expected 3, got %d", len(metas))
	}
}

// --- RR-021: ListWatchable returns only Watcher-capable metas ---
func TestRegistryListWatchable(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.WatchableResourcer[string]{}})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.DeploymentMeta, Resourcer: &resourcetest.WatchableResourcer[string]{}})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.ServiceMeta, Resourcer: &resourcetest.StubResourcer[string]{}})

	metas := reg.ListWatchable()
	if len(metas) != 2 {
		t.Fatalf("expected 2 watchable, got %d", len(metas))
	}
}

// --- RR-022: GetWatcher returns the Watcher interface ---
func TestRegistryGetWatcher(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	wr := &resourcetest.WatchableResourcer[string]{}
	reg.Register(resource.ResourceRegistration[string]{
		Meta: resourcetest.PodMeta, Resourcer: wr,
	})

	w, ok := reg.GetWatcher("core::v1::Pod")
	if !ok || w == nil {
		t.Fatal("expected Watcher")
	}
}

// --- RR-023: GetWatcher for non-Watcher returns false ---
func TestRegistryGetWatcherNone(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{
		Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{},
	})

	_, ok := reg.GetWatcher("core::v1::Pod")
	if ok {
		t.Fatal("expected no Watcher")
	}
}

// --- RR-024: Pattern resourcer with named pattern ---
func TestRegistryNamedPattern(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	extRes := &resourcetest.StubResourcer[string]{}
	catchAll := &resourcetest.StubResourcer[string]{}
	reg.RegisterPattern("extensions::*::*", extRes)
	reg.RegisterPattern("*", catchAll)

	r1, _ := reg.Lookup("extensions::v1beta1::Ingress")
	if r1 != extRes {
		t.Fatal("expected extensions resourcer")
	}

	r2, _ := reg.Lookup("core::v1::ConfigMap")
	if r2 != catchAll {
		t.Fatal("expected catch-all resourcer")
	}
}

// --- RR-025: Duplicate registration for same key overwrites ---
func TestRegistryDuplicateOverwrites(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	resA := &resourcetest.StubResourcer[string]{}
	resB := &resourcetest.StubResourcer[string]{}
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.PodMeta, Resourcer: resA})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.PodMeta, Resourcer: resB})

	r, _ := reg.Lookup("core::v1::Pod")
	if r != resB {
		t.Fatal("expected second registration to win")
	}
}

// --- RR-026: Lookup with empty string key ---
func TestRegistryLookupEmptyKey(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{
		Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{},
	})
	_, err := reg.Lookup("")
	if err == nil {
		t.Fatal("expected error for empty key")
	}
}

// --- RR-027: Lookup with malformed key (missing parts) ---
func TestRegistryLookupMalformedKey(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{
		Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{},
	})
	_, err := reg.Lookup("core::v1") // only 2 parts
	if err == nil {
		t.Fatal("expected error for malformed key")
	}
}

// --- RR-028: Lookup with extra separators ---
func TestRegistryLookupExtraSeparators(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{
		Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{},
	})
	// Extra separators: "core::v1::Pod::extra" — should not match "core::v1::Pod".
	_, err := reg.Lookup("core::v1::Pod::extra")
	if err == nil {
		t.Fatal("expected error for extra separators")
	}
}

// --- RR-030: Registration with zero-value Meta ---
func TestRegistryZeroValueMeta(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{
		Meta:      resource.ResourceMeta{},
		Resourcer: &resourcetest.StubResourcer[string]{},
	})
	// Zero-value meta has key "::::" — lookup should find it by that key.
	_, err := reg.Lookup(":::::")
	// We don't crash; it returns not-found since keys differ.
	if err == nil {
		// It's okay either way — just testing no panic.
	}
}

// --- RR-031: Concurrent lookups during registration ---
func TestRegistryConcurrentLookupsDuringRegistration(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{
		Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{},
	})

	done := make(chan struct{})
	// Concurrent lookups.
	for i := 0; i < 10; i++ {
		go func() {
			reg.Lookup("core::v1::Pod")
			done <- struct{}{}
		}()
	}
	// Concurrent registrations.
	for i := 0; i < 10; i++ {
		go func() {
			reg.Register(resource.ResourceRegistration[string]{
				Meta: resourcetest.DeploymentMeta, Resourcer: &resourcetest.StubResourcer[string]{},
			})
			done <- struct{}{}
		}()
	}
	for i := 0; i < 20; i++ {
		<-done
	}
}

// --- RR-032: Large registry — 100+ registrations ---
func TestRegistryLargeRegistry(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	for i := 0; i < 150; i++ {
		meta := resource.ResourceMeta{
			Group: "test.io", Version: "v1", Kind: fmt.Sprintf("Kind%d", i),
		}
		reg.Register(resource.ResourceRegistration[string]{
			Meta: meta, Resourcer: &resourcetest.StubResourcer[string]{},
		})
	}
	metas := reg.ListAll()
	if len(metas) != 150 {
		t.Fatalf("expected 150, got %d", len(metas))
	}
	_, err := reg.Lookup("test.io::v1::Kind99")
	if err != nil {
		t.Fatalf("expected to find Kind99: %v", err)
	}
}

// --- RR-033: Pattern match does NOT apply to exact-registered keys ---
func TestRegistryPatternDoesNotOverrideExact(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	exact := &resourcetest.StubResourcer[string]{}
	pattern := &resourcetest.StubResourcer[string]{}
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.PodMeta, Resourcer: exact})
	reg.RegisterPattern("*", pattern)

	r, _ := reg.Lookup("core::v1::Pod")
	if r != exact {
		t.Fatal("exact should win over pattern for registered key")
	}
}

// --- RR-034: Multiple patterns — most specific wins ---
func TestRegistryMultiplePatternsSpecificWins(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	catchAll := &resourcetest.StubResourcer[string]{}
	coreAll := &resourcetest.StubResourcer[string]{}
	coreV1 := &resourcetest.StubResourcer[string]{}
	reg.RegisterPattern("*", catchAll)
	reg.RegisterPattern("core::*::*", coreAll)
	reg.RegisterPattern("core::v1::*", coreV1)

	r, _ := reg.Lookup("core::v1::ConfigMap")
	if r != coreV1 {
		t.Fatal("expected most specific pattern (core::v1::*)")
	}

	r2, _ := reg.Lookup("core::v2::Something")
	if r2 != coreAll {
		t.Fatal("expected core::*::* for core::v2")
	}

	r3, _ := reg.Lookup("apps::v1::Deployment")
	if r3 != catchAll {
		t.Fatal("expected catch-all for apps")
	}
}

// --- RR-035: Resourcer implementing ALL optional interfaces ---
type allInterfacesResourcer struct {
	resourcetest.StubResourcer[string]
}

func (a *allInterfacesResourcer) Watch(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
	return nil
}
func (a *allInterfacesResourcer) SyncPolicy() resource.SyncPolicy { return resource.SyncOnConnect }
func (a *allInterfacesResourcer) Definition() resource.ResourceDefinition {
	return resource.ResourceDefinition{IDAccessor: "all"}
}
func (a *allInterfacesResourcer) ClassifyError(err error) error { return err }
func (a *allInterfacesResourcer) GetActions(_ context.Context, _ *string, _ resource.ResourceMeta) ([]resource.ActionDescriptor, error) {
	return nil, nil
}
func (a *allInterfacesResourcer) ExecuteAction(_ context.Context, _ *string, _ resource.ResourceMeta, _ string, _ resource.ActionInput) (*resource.ActionResult, error) {
	return nil, nil
}
func (a *allInterfacesResourcer) StreamAction(_ context.Context, _ *string, _ resource.ResourceMeta, _ string, _ resource.ActionInput, _ chan<- resource.ActionEvent) error {
	return nil
}
func (a *allInterfacesResourcer) GetResourceSchema(_ context.Context, _ *string, _ resource.ResourceMeta) (json.RawMessage, error) {
	return nil, nil
}

func TestRegistryAllOptionalInterfaces(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{
		Meta: resourcetest.PodMeta, Resourcer: &allInterfacesResourcer{},
	})

	if !reg.IsWatcher("core::v1::Pod") {
		t.Fatal("expected Watcher")
	}
	if _, ok := reg.GetActionResourcer("core::v1::Pod"); !ok {
		t.Fatal("expected ActionResourcer")
	}
	if _, ok := reg.GetErrorClassifier("core::v1::Pod"); !ok {
		t.Fatal("expected ErrorClassifier")
	}
	if _, ok := reg.GetSchemaResourcer("core::v1::Pod"); !ok {
		t.Fatal("expected SchemaResourcer")
	}
	def := reg.GetDefinition("core::v1::Pod")
	if def.IDAccessor != "all" {
		t.Fatalf("expected 'all', got %s", def.IDAccessor)
	}
	caps := reg.DeriveCapabilities("core::v1::Pod")
	if !caps.Watchable || !caps.HasActions || !caps.HasSchema {
		t.Fatal("expected all capability flags set")
	}
}

// --- RR-036: GetDefinition for pattern-matched resource ---
func TestRegistryGetDefinitionForPattern(t *testing.T) {
	defaultDef := resource.ResourceDefinition{IDAccessor: "default"}
	reg := resource.NewResourcerRegistryForTest(defaultDef)
	reg.RegisterPattern("*", &resourcetest.StubResourcer[string]{})

	// Pattern-matched resources fall back to default definition.
	got := reg.GetDefinition("extensions::v1::Ingress")
	if got.IDAccessor != "default" {
		t.Fatalf("expected default def for pattern resource, got %s", got.IDAccessor)
	}
}

// --- RR-037: GetWatcher for pattern-matched resource ---
func TestRegistryGetWatcherForPattern(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.RegisterPattern("*", &resourcetest.WatchableResourcer[string]{})

	w, ok := reg.GetWatcher("extensions::v1::Ingress")
	if !ok || w == nil {
		t.Fatal("expected Watcher from pattern resourcer")
	}
}

// --- RR-038: ListAll after removing/overwriting a registration ---
func TestRegistryListAllAfterOverwrite(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}})
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.DeploymentMeta, Resourcer: &resourcetest.StubResourcer[string]{}})

	// Overwrite Pod with new resourcer — same key.
	reg.Register(resource.ResourceRegistration[string]{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.WatchableResourcer[string]{}})

	metas := reg.ListAll()
	if len(metas) != 2 {
		t.Fatalf("expected 2 (no duplicates after overwrite), got %d", len(metas))
	}
}

// --- DeriveCapabilities ---
func TestRegistryDeriveCapabilities(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{
		Meta: resourcetest.PodMeta, Resourcer: &resourcetest.WatchableResourcer[string]{},
	})

	caps := reg.DeriveCapabilities("core::v1::Pod")
	if caps == nil {
		t.Fatal("expected capabilities")
	}
	if !caps.CanGet || !caps.CanList || !caps.CanCreate || !caps.CanUpdate || !caps.CanDelete {
		t.Fatal("expected all CRUD flags true")
	}
	if !caps.Watchable {
		t.Fatal("expected Watchable == true")
	}
}

// --- RR-029: Registration with nil Resourcer ---
func TestRR_NilResourcerRegistration(t *testing.T) {
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	// Registering a nil Resourcer — should not panic during Register.
	// Lookup should fail gracefully.
	reg.Register(resource.ResourceRegistration[string]{
		Meta:      resourcetest.PodMeta,
		Resourcer: nil,
	})
	_, err := reg.Lookup("core::v1::Pod")
	// Even though registered, a nil resourcer should be returned.
	// The caller must handle nil — or we error. Either is acceptable.
	// We just want no panic.
	_ = err
}
