package resourcev2

import (
	"context"
	"testing"
)

func TestSourceOperationalContractIgnoresUnselectedMetadata(t *testing.T) {
	request := controllerSourceOpen(SourceModeSnapshotAndUpdates)
	for _, tc := range []struct {
		name      string
		change    func(*ResourceType)
		unchanged bool
	}{
		{"presentation", func(r *ResourceType) { r.DisplayName = "new"; r.Description = "new" }, true},
		{"unselected format", func(r *ResourceType) {
			r.Representations = append(r.Representations, Representation{ID: "other", Revision: "2"})
		}, true},
		{"default selection", func(r *ResourceType) { r.DefaultRepresentation = "other"; r.ModelRepresentation = "other" }, true},
		{"unselected capability", func(r *ResourceType) {
			r.Capabilities = []Capability{{Operation: OperationSourceUpdates, Scope: QueryScope{}, Guarantee: ContinuityBestEffort}}
		}, true},
		{"selected format", func(r *ResourceType) { r.Representations[0].Revision = "2" }, false},
		{"selected withdrawal", func(r *ResourceType) { r.Capabilities = nil }, false},
		{"identity dimensions", func(r *ResourceType) { r.IdentityDimensions = []Dimension{{Name: "region"}} }, false},
		{"query dimensions", func(r *ResourceType) { r.QueryDimensions = []Dimension{{Name: "region"}} }, false},
		{"native authority", func(r *ResourceType) {
			r.NativeAuthorityHint = &NativeAuthorityHint{Scheme: "native.v1", ID: "changed", Continuity: ContinuityStableNative}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := controllerConfig().Catalog(context.Background(), CatalogRequest{Route: request.Target.Route})
			if err != nil {
				t.Fatal(err)
			}
			after, err := controllerConfig().Catalog(context.Background(), CatalogRequest{Route: request.Target.Route})
			if err != nil {
				t.Fatal(err)
			}
			tc.change(&after.Types[0])
			if got := SourceOperationalContractEqual(before.Types[0], after.Types[0], request); got != tc.unchanged {
				t.Fatalf("contract unchanged=%v, want %v", got, tc.unchanged)
			}
		})
	}
}

func TestSourceCatalogPreservesOriginalNativeGeneration(t *testing.T) {
	request := controllerSourceOpen(SourceModeSnapshot)
	catalog, err := controllerConfig().Catalog(context.Background(), CatalogRequest{Route: request.Target.Route})
	if err != nil {
		t.Fatal(err)
	}
	nonce := [32]byte{1}
	catalog.Revision = boundCatalogRevision(nonce, "support-1")
	ctx := WithCatalogRevisionForRoute(context.Background(), catalog.Provider, request.Target.Route, catalog.Revision)
	admitted := catalog.Types[0]
	catalog.Revision = boundCatalogRevision(nonce, "unrelated-support-2")
	if err := CheckSourceCatalog(ctx, catalog, request, admitted); err != nil {
		t.Fatalf("unrelated support revision retired source: %v", err)
	}
	catalog.Revision = boundCatalogRevision([32]byte{2}, "unrelated-support-2")
	if err := CheckSourceCatalog(ctx, catalog, request, admitted); ErrorCode(err) != "RESET_REQUIRED" {
		t.Fatalf("replacement generation reused old source: %v", err)
	}
}

func TestSourceCatalogUnknownAbsenceCannotProveWithdrawal(t *testing.T) {
	request := controllerSourceOpen(SourceModeSnapshot)
	catalog, err := controllerConfig().Catalog(context.Background(), CatalogRequest{Route: request.Target.Route})
	if err != nil {
		t.Fatal(err)
	}
	catalog.Revision = boundCatalogRevision([32]byte{1}, "support-1")
	ctx := WithCatalogRevision(context.Background(), catalog.Revision)
	admitted := catalog.Types[0]
	catalog.Types = nil
	catalog.Coverage = Coverage{Kind: "partial", Reason: "Discovery incomplete."}
	if err := CheckSourceCatalog(ctx, catalog, request, admitted); ErrorCode(err) != "UNAVAILABLE" {
		t.Fatalf("partial absence claimed withdrawal: %v", err)
	}
	catalog.Types = []ResourceType{admitted}
	catalog.Types[0].NativeAuthorityHint = &NativeAuthorityHint{Scheme: "native.v1", ID: "retargeted", Continuity: ContinuityStableNative}
	if err := CheckSourceCatalog(ctx, catalog, request, admitted); ErrorCode(err) != "RESET_REQUIRED" {
		t.Fatalf("partial discovery concealed native retargeting: %v", err)
	}
	catalog.Types[0] = admitted
	catalog.Types[0].Representations = []Representation{{ID: request.Binding.Representation.ID, Revision: "different"}}
	if err := CheckSourceCatalog(ctx, catalog, request, admitted); ErrorCode(err) != "RESET_REQUIRED" {
		t.Fatalf("partial discovery concealed changed selected format: %v", err)
	}
	catalog.Types = nil
	catalog.Coverage = Coverage{Kind: "complete"}
	if err := CheckSourceCatalog(ctx, catalog, request, admitted); ErrorCode(err) != "RESET_REQUIRED" {
		t.Fatalf("complete withdrawal kept selected source: %v", err)
	}
}

func TestSourceOperationalContractPreservesOpaqueNativeQuery(t *testing.T) {
	request := controllerSourceOpen(SourceModeSnapshot)
	query := Value{Kind: "map", Map: map[string]Value{}}
	request.Acquisition = SourceAcquisition{Kind: SourceAcquisitionNativeQuery, Language: "opaque-native", Query: &query}
	catalog, err := controllerConfig().Catalog(context.Background(), CatalogRequest{Route: request.Target.Route})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := catalog.Types[0]
	if !SourceOperationalContractEqual(descriptor, descriptor, request) {
		t.Fatal("unchanged opaque native query contract was rejected")
	}
	constrained := descriptor
	constrained.NativeQueries = []NativeQueryDescriptor{{Language: "opaque-native", InputSchema: Schema{}}}
	if SourceOperationalContractEqual(descriptor, constrained, request) {
		t.Fatal("new query constraint inherited the opaque admission")
	}
}
