package resource_test

import (
	"context"
	"encoding/json"
	"testing"

	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/resource/resourcetest"
)

// ============================================================================
// Filter/Query/Capabilities Tests (FQ-012 through FQ-034, registry & controller level)
// ============================================================================

// fqAllInterfacesResourcer is a test type that implements ALL optional interfaces
// (Watcher, FilterableProvider, TextSearchProvider, ActionResourcer,
// ResourceSchemaProvider, ScaleHintProvider) so DeriveCapabilities returns all flags true.
type fqAllInterfacesResourcer struct {
	resourcetest.WatchableResourcer[string]
}

func (r *fqAllInterfacesResourcer) FilterFields(_ context.Context, _ string) ([]resource.FilterField, error) {
	return []resource.FilterField{
		{
			Path:        "metadata.name",
			DisplayName: "Name",
			Description: "Resource name",
			Type:        resource.FilterFieldString,
			Operators:   []resource.FilterOperator{resource.OpEqual, resource.OpContains},
		},
		{
			Path:        "status.phase",
			DisplayName: "Phase",
			Description: "Current phase",
			Type:        resource.FilterFieldEnum,
			Operators:   []resource.FilterOperator{resource.OpEqual, resource.OpIn},
			AllowedValues: []string{"Running", "Pending", "Failed"},
		},
	}, nil
}

func (r *fqAllInterfacesResourcer) Search(_ context.Context, _ *string, _ resource.ResourceMeta, _ string, _ int) (*resource.FindResult, error) {
	return &resource.FindResult{Success: true}, nil
}

func (r *fqAllInterfacesResourcer) GetActions(_ context.Context, _ *string, _ resource.ResourceMeta) ([]resource.ActionDescriptor, error) {
	return []resource.ActionDescriptor{
		{ID: "restart", Label: "Restart", Scope: resource.ActionScopeInstance},
	}, nil
}

func (r *fqAllInterfacesResourcer) ExecuteAction(_ context.Context, _ *string, _ resource.ResourceMeta, _ string, _ resource.ActionInput) (*resource.ActionResult, error) {
	return &resource.ActionResult{Success: true}, nil
}

func (r *fqAllInterfacesResourcer) StreamAction(_ context.Context, _ *string, _ resource.ResourceMeta, _ string, _ resource.ActionInput, _ chan<- resource.ActionEvent) error {
	return nil
}

func (r *fqAllInterfacesResourcer) GetResourceSchema(_ context.Context, _ *string, _ resource.ResourceMeta) (json.RawMessage, error) {
	return json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`), nil
}

func (r *fqAllInterfacesResourcer) ScaleHint() *resource.ScaleHint {
	return &resource.ScaleHint{
		Level:         resource.ScaleMany,
		ExpectedCount: 1000,
	}
}

// --- FQ-012: DeriveCapabilities auto-derives all flags for full-interface resourcer ---
func TestFQ012_DeriveCapabilitiesAllFlags(t *testing.T) {
	registry := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	registry.Register(resource.ResourceRegistration[string]{
		Meta:      resourcetest.PodMeta,
		Resourcer: &fqAllInterfacesResourcer{},
	})

	caps := registry.DeriveCapabilities(resourcetest.PodMeta.Key())
	if caps == nil {
		t.Fatal("expected non-nil capabilities")
	}

	// CRUD flags (always true for any Resourcer).
	if !caps.CanGet {
		t.Error("CanGet should be true")
	}
	if !caps.CanList {
		t.Error("CanList should be true")
	}
	if !caps.CanFind {
		t.Error("CanFind should be true")
	}
	if !caps.CanCreate {
		t.Error("CanCreate should be true")
	}
	if !caps.CanUpdate {
		t.Error("CanUpdate should be true")
	}
	if !caps.CanDelete {
		t.Error("CanDelete should be true")
	}

	// Extended capabilities.
	if !caps.Watchable {
		t.Error("Watchable should be true (implements Watcher)")
	}
	if !caps.Filterable {
		t.Error("Filterable should be true (implements FilterableProvider)")
	}
	if !caps.Searchable {
		t.Error("Searchable should be true (implements TextSearchProvider)")
	}
	if !caps.HasActions {
		t.Error("HasActions should be true (implements ActionResourcer)")
	}
	if !caps.HasSchema {
		t.Error("HasSchema should be true (implements ResourceSchemaProvider)")
	}
	if caps.Scale == nil {
		t.Fatal("Scale should be non-nil (implements ScaleHintProvider)")
	}
	if caps.Scale.ExpectedCount != 1000 {
		t.Errorf("Scale.ExpectedCount = %d, want 1000", caps.Scale.ExpectedCount)
	}
}

// --- FQ-013: Watchable=true iff Watcher ---
func TestFQ013_WatchableFlag(t *testing.T) {
	registry := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})

	// WatchableResourcer implements Watcher.
	registry.Register(resource.ResourceRegistration[string]{
		Meta:      resourcetest.PodMeta,
		Resourcer: &resourcetest.WatchableResourcer[string]{},
	})
	// StubResourcer does NOT implement Watcher.
	registry.Register(resource.ResourceRegistration[string]{
		Meta:      resourcetest.ServiceMeta,
		Resourcer: &resourcetest.StubResourcer[string]{},
	})

	podCaps := registry.DeriveCapabilities(resourcetest.PodMeta.Key())
	if !podCaps.Watchable {
		t.Error("Pod (WatchableResourcer) should have Watchable=true")
	}

	svcCaps := registry.DeriveCapabilities(resourcetest.ServiceMeta.Key())
	if svcCaps.Watchable {
		t.Error("Service (StubResourcer) should have Watchable=false")
	}
}

// --- FQ-014: Filterable=true iff FilterableProvider ---
func TestFQ014_FilterableFlag(t *testing.T) {
	registry := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})

	// fqAllInterfacesResourcer implements FilterableProvider.
	registry.Register(resource.ResourceRegistration[string]{
		Meta:      resourcetest.PodMeta,
		Resourcer: &fqAllInterfacesResourcer{},
	})
	// StubResourcer does NOT.
	registry.Register(resource.ResourceRegistration[string]{
		Meta:      resourcetest.ServiceMeta,
		Resourcer: &resourcetest.StubResourcer[string]{},
	})

	podCaps := registry.DeriveCapabilities(resourcetest.PodMeta.Key())
	if !podCaps.Filterable {
		t.Error("Pod (fqAllInterfacesResourcer) should have Filterable=true")
	}

	svcCaps := registry.DeriveCapabilities(resourcetest.ServiceMeta.Key())
	if svcCaps.Filterable {
		t.Error("Service (StubResourcer) should have Filterable=false")
	}
}

// --- FQ-015: HasActions=true iff ActionResourcer ---
func TestFQ015_HasActionsFlag(t *testing.T) {
	registry := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})

	registry.Register(resource.ResourceRegistration[string]{
		Meta:      resourcetest.PodMeta,
		Resourcer: &fqAllInterfacesResourcer{},
	})
	registry.Register(resource.ResourceRegistration[string]{
		Meta:      resourcetest.ServiceMeta,
		Resourcer: &resourcetest.StubResourcer[string]{},
	})

	podCaps := registry.DeriveCapabilities(resourcetest.PodMeta.Key())
	if !podCaps.HasActions {
		t.Error("Pod (fqAllInterfacesResourcer) should have HasActions=true")
	}

	svcCaps := registry.DeriveCapabilities(resourcetest.ServiceMeta.Key())
	if svcCaps.HasActions {
		t.Error("Service (StubResourcer) should have HasActions=false")
	}
}

// --- FQ-016: HasSchema=true iff ResourceSchemaProvider ---
func TestFQ016_HasSchemaFlag(t *testing.T) {
	registry := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})

	registry.Register(resource.ResourceRegistration[string]{
		Meta:      resourcetest.PodMeta,
		Resourcer: &fqAllInterfacesResourcer{},
	})
	registry.Register(resource.ResourceRegistration[string]{
		Meta:      resourcetest.ServiceMeta,
		Resourcer: &resourcetest.StubResourcer[string]{},
	})

	podCaps := registry.DeriveCapabilities(resourcetest.PodMeta.Key())
	if !podCaps.HasSchema {
		t.Error("Pod (fqAllInterfacesResourcer) should have HasSchema=true")
	}

	svcCaps := registry.DeriveCapabilities(resourcetest.ServiceMeta.Key())
	if svcCaps.HasSchema {
		t.Error("Service (StubResourcer) should have HasSchema=false")
	}
}

// --- FQ-017: Searchable=true iff TextSearchProvider ---
func TestFQ017_SearchableFlag(t *testing.T) {
	registry := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})

	registry.Register(resource.ResourceRegistration[string]{
		Meta:      resourcetest.PodMeta,
		Resourcer: &fqAllInterfacesResourcer{},
	})
	registry.Register(resource.ResourceRegistration[string]{
		Meta:      resourcetest.ServiceMeta,
		Resourcer: &resourcetest.StubResourcer[string]{},
	})

	podCaps := registry.DeriveCapabilities(resourcetest.PodMeta.Key())
	if !podCaps.Searchable {
		t.Error("Pod (fqAllInterfacesResourcer) should have Searchable=true")
	}

	svcCaps := registry.DeriveCapabilities(resourcetest.ServiceMeta.Key())
	if svcCaps.Searchable {
		t.Error("Service (StubResourcer) should have Searchable=false")
	}
}

// --- FQ-018: NamespaceScoped — ResourceDefinition.NamespaceAccessor set ---
func TestFQ018_NamespaceScoped(t *testing.T) {
	// Verify that when a registration has a NamespaceAccessor, it's present in the definition.
	registry := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})

	namespacedDef := resource.ResourceDefinition{
		IDAccessor:        "metadata.name",
		NamespaceAccessor: "metadata.namespace",
	}
	registry.Register(resource.ResourceRegistration[string]{
		Meta:       resourcetest.PodMeta,
		Resourcer:  &resourcetest.StubResourcer[string]{},
		Definition: &namespacedDef,
	})

	def := registry.GetDefinition(resourcetest.PodMeta.Key())
	if def.NamespaceAccessor == "" {
		t.Error("expected NamespaceAccessor to be set")
	}
	if def.NamespaceAccessor != "metadata.namespace" {
		t.Errorf("NamespaceAccessor = %q, want %q", def.NamespaceAccessor, "metadata.namespace")
	}

	// Cluster-scoped resource has no NamespaceAccessor.
	clusterDef := resource.ResourceDefinition{IDAccessor: "metadata.name"}
	registry.Register(resource.ResourceRegistration[string]{
		Meta:       resourcetest.NodeMeta,
		Resourcer:  &resourcetest.StubResourcer[string]{},
		Definition: &clusterDef,
	})

	nodeDef := registry.GetDefinition(resourcetest.NodeMeta.Key())
	if nodeDef.NamespaceAccessor != "" {
		t.Errorf("Node should have empty NamespaceAccessor, got %q", nodeDef.NamespaceAccessor)
	}
}

// --- FQ-019: ScaleHint populated from ScaleHintProvider ---
func TestFQ019_ScaleHintPopulated(t *testing.T) {
	registry := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	registry.Register(resource.ResourceRegistration[string]{
		Meta:      resourcetest.PodMeta,
		Resourcer: &fqAllInterfacesResourcer{},
	})

	caps := registry.DeriveCapabilities(resourcetest.PodMeta.Key())
	if caps.Scale == nil {
		t.Fatal("Scale should be non-nil for ScaleHintProvider")
	}
	if caps.Scale.ExpectedCount != 1000 {
		t.Errorf("Scale.ExpectedCount = %d, want 1000", caps.Scale.ExpectedCount)
	}
	if caps.Scale.Level != resource.ScaleMany {
		t.Errorf("Scale.Level = %v, want ScaleMany", caps.Scale.Level)
	}
}

// --- FQ-020: ScaleHint nil when not implemented ---
func TestFQ020_ScaleHintNilWhenNotImplemented(t *testing.T) {
	registry := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	registry.Register(resource.ResourceRegistration[string]{
		Meta:      resourcetest.ServiceMeta,
		Resourcer: &resourcetest.StubResourcer[string]{},
	})

	caps := registry.DeriveCapabilities(resourcetest.ServiceMeta.Key())
	if caps.Scale != nil {
		t.Errorf("Scale should be nil for StubResourcer, got %+v", caps.Scale)
	}
}

// --- FQ-021: GetResourceSchema returns JSON from ResourceSchemaProvider ---
func TestFQ021_GetResourceSchemaReturnsJSON(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{{
			Meta:      resourcetest.PodMeta,
			Resourcer: &fqAllInterfacesResourcer{},
		}},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")

	schema, err := ctrl.GetResourceSchema(ctx, "conn-1", resourcetest.PodMeta.Key())
	if err != nil {
		t.Fatal(err)
	}
	if schema == nil {
		t.Fatal("expected non-nil schema")
	}

	// Verify it's valid JSON.
	var parsed map[string]interface{}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if parsed["type"] != "object" {
		t.Errorf("expected schema type=object, got %v", parsed["type"])
	}
}

// --- FQ-022: GetResourceSchema returns nil when not implemented ---
func TestFQ022_GetResourceSchemaNilWhenNotImplemented(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{{
			Meta:      resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{},
		}},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")

	schema, err := ctrl.GetResourceSchema(ctx, "conn-1", resourcetest.PodMeta.Key())
	if err != nil {
		t.Fatal(err)
	}
	if schema != nil {
		t.Fatalf("expected nil schema for StubResourcer, got %s", string(schema))
	}
}

// --- FQ-026: ActionDescriptor with nil ParamsSchema ---
func TestFQ026_ActionDescriptorNilParamsSchema(t *testing.T) {
	// Just create a descriptor with nil ParamsSchema and verify it serializes correctly.
	desc := resource.ActionDescriptor{
		ID:           "restart",
		Label:        "Restart Pod",
		Description:  "Restarts the pod by deleting it",
		Scope:        resource.ActionScopeInstance,
		Streaming:    false,
		ParamsSchema: nil, // intentionally nil
		Dangerous:    true,
	}

	// Verify it can be marshaled to JSON.
	data, err := json.Marshal(desc)
	if err != nil {
		t.Fatalf("failed to marshal ActionDescriptor: %v", err)
	}

	// Verify ParamsSchema is omitted (omitempty tag).
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if _, exists := parsed["paramsSchema"]; exists {
		t.Error("paramsSchema should be omitted when nil")
	}

	// Verify other fields are present.
	if parsed["id"] != "restart" {
		t.Errorf("id = %v, want restart", parsed["id"])
	}
	if parsed["dangerous"] != true {
		t.Error("dangerous should be true")
	}
}

// --- FQ-030: FindInput.Filters=nil equivalent to no filtering ---
func TestFQ030_FindInputFiltersNil(t *testing.T) {
	var capturedInput resource.FindInput
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				FindFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, input resource.FindInput) (*resource.FindResult, error) {
					capturedInput = input
					return &resource.FindResult{
						Success: true,
						Result: []json.RawMessage{
							json.RawMessage(`{"id":"pod-1"}`),
						},
					}, nil
				},
			},
		}},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")

	testCtx := resourcetest.NewTestContext()
	result, err := ctrl.Find(testCtx, resourcetest.PodMeta.Key(), resource.FindInput{
		Filters: nil, // explicitly nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Error("expected success")
	}
	if capturedInput.Filters != nil {
		t.Error("expected nil Filters to pass through as nil")
	}
}

// --- FQ-031: FindInput with empty FilterExpression ---
func TestFQ031_FindInputEmptyFilterExpression(t *testing.T) {
	var capturedInput resource.FindInput
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				FindFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, input resource.FindInput) (*resource.FindResult, error) {
					capturedInput = input
					return &resource.FindResult{Success: true}, nil
				},
			},
		}},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")

	testCtx := resourcetest.NewTestContext()
	result, err := ctrl.Find(testCtx, resourcetest.PodMeta.Key(), resource.FindInput{
		Filters: &resource.FilterExpression{}, // empty expression
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Error("expected success")
	}
	if capturedInput.Filters == nil {
		t.Error("expected non-nil Filters to pass through")
	}
	if len(capturedInput.Filters.Predicates) != 0 {
		t.Error("expected empty predicates")
	}
	if len(capturedInput.Filters.Groups) != 0 {
		t.Error("expected empty groups")
	}
}

// --- FQ-032: GetResourceCapabilities returns correct flags via controller ---
func TestFQ032_GetResourceCapabilitiesViaController(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{
			{
				Meta:      resourcetest.PodMeta,
				Resourcer: &fqAllInterfacesResourcer{},
			},
			{
				Meta:      resourcetest.ServiceMeta,
				Resourcer: &resourcetest.StubResourcer[string]{},
			},
		},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")

	// fqAllInterfacesResourcer — all extended capabilities true.
	podCaps, err := ctrl.GetResourceCapabilities(ctx, resourcetest.PodMeta.Key())
	if err != nil {
		t.Fatal(err)
	}
	if !podCaps.CanGet || !podCaps.CanList || !podCaps.CanFind || !podCaps.CanCreate || !podCaps.CanUpdate || !podCaps.CanDelete {
		t.Error("expected all CRUD flags true for Pod")
	}
	if !podCaps.Watchable {
		t.Error("expected Watchable=true for Pod")
	}
	if !podCaps.Filterable {
		t.Error("expected Filterable=true for Pod")
	}
	if !podCaps.Searchable {
		t.Error("expected Searchable=true for Pod")
	}
	if !podCaps.HasActions {
		t.Error("expected HasActions=true for Pod")
	}
	if !podCaps.HasSchema {
		t.Error("expected HasSchema=true for Pod")
	}
	if podCaps.Scale == nil || podCaps.Scale.ExpectedCount != 1000 {
		t.Error("expected Scale with ExpectedCount=1000 for Pod")
	}

	// StubResourcer — CRUD only, no extended capabilities.
	svcCaps, err := ctrl.GetResourceCapabilities(ctx, resourcetest.ServiceMeta.Key())
	if err != nil {
		t.Fatal(err)
	}
	if !svcCaps.CanGet || !svcCaps.CanList {
		t.Error("expected CRUD flags true for Service")
	}
	if svcCaps.Watchable {
		t.Error("expected Watchable=false for Service")
	}
	if svcCaps.Filterable {
		t.Error("expected Filterable=false for Service")
	}
	if svcCaps.Searchable {
		t.Error("expected Searchable=false for Service")
	}
	if svcCaps.HasActions {
		t.Error("expected HasActions=false for Service")
	}
	if svcCaps.HasSchema {
		t.Error("expected HasSchema=false for Service")
	}
	if svcCaps.Scale != nil {
		t.Error("expected Scale=nil for Service")
	}

	// Unknown resource should error.
	_, err = ctrl.GetResourceCapabilities(ctx, "unknown::v1::Thing")
	if err == nil {
		t.Error("expected error for unknown resource")
	}
}

// --- FQ-033: GetFilterFields returns declared fields ---
func TestFQ033_GetFilterFieldsReturnsFields(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{{
			Meta:      resourcetest.PodMeta,
			Resourcer: &fqAllInterfacesResourcer{},
		}},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")

	fields, err := ctrl.GetFilterFields(ctx, "conn-1", resourcetest.PodMeta.Key())
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 2 {
		t.Fatalf("expected 2 filter fields, got %d", len(fields))
	}

	// Check first field.
	if fields[0].Path != "metadata.name" {
		t.Errorf("field[0].Path = %q, want %q", fields[0].Path, "metadata.name")
	}
	if fields[0].DisplayName != "Name" {
		t.Errorf("field[0].DisplayName = %q, want %q", fields[0].DisplayName, "Name")
	}
	if fields[0].Type != resource.FilterFieldString {
		t.Errorf("field[0].Type = %v, want FilterFieldString", fields[0].Type)
	}
	if len(fields[0].Operators) != 2 {
		t.Errorf("field[0].Operators count = %d, want 2", len(fields[0].Operators))
	}

	// Check second field (enum type with AllowedValues).
	if fields[1].Path != "status.phase" {
		t.Errorf("field[1].Path = %q, want %q", fields[1].Path, "status.phase")
	}
	if fields[1].Type != resource.FilterFieldEnum {
		t.Errorf("field[1].Type = %v, want FilterFieldEnum", fields[1].Type)
	}
	if len(fields[1].AllowedValues) != 3 {
		t.Errorf("field[1].AllowedValues count = %d, want 3", len(fields[1].AllowedValues))
	}
}

// --- FQ-034: GetFilterFields returns nil for non-filterable resource ---
func TestFQ034_GetFilterFieldsNilForNonFilterable(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{{
			Meta:      resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{},
		}},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")

	fields, err := ctrl.GetFilterFields(ctx, "conn-1", resourcetest.PodMeta.Key())
	if err != nil {
		t.Fatal(err)
	}
	if fields != nil {
		t.Fatalf("expected nil filter fields for non-filterable resource, got %v", fields)
	}
}
