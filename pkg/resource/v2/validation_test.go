package resourcev2

import (
	"testing"
)

func TestAddressAndScopeDimensionAdmission(t *testing.T) {
	a := codecRecord().Reference.Address
	dims := []Dimension{{Name: "region"}}
	if err := ValidateAddress(a, dims); err != nil {
		t.Fatal(err)
	}
	bad := a
	bad.Scope = []ScopeEntry{{Dimension: "region", Value: "a"}, {Dimension: "region", Value: "b"}}
	if err := ValidateAddress(bad, dims); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("duplicate dimension: %v", err)
	}
	bad.Scope = []ScopeEntry{}
	if err := ValidateAddress(bad, dims); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("missing dimension: %v", err)
	}
	bad.Scope = []ScopeEntry{{Dimension: "unknown", Value: "a"}}
	if err := ValidateAddress(bad, dims); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("unknown dimension: %v", err)
	}
	bad.Scope = []ScopeEntry{{Dimension: "region", Value: ""}}
	if err := ValidateAddress(bad, dims); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("empty dimension: %v", err)
	}
	if err := ValidateAddress(bad, []Dimension{{Name: "region", AllowEmpty: true}}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateScope(QueryScope{}, dims); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("unresolved scope: %v", err)
	}
	values := []string{}
	if err := ValidateScope(QueryScope{{Dimension: "region", Match: ScopeOneOf, Values: &values}}, dims); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []QueryScope{
		{{Dimension: "region", Match: ScopeOneOf}},
		{{Dimension: "region", Match: ScopeAny, Values: &values}},
		{{Dimension: "region", Match: ScopeAny}, {Dimension: "region", Match: ScopeAny}},
		{{Dimension: "other", Match: ScopeAny}},
	} {
		if err := ValidateScope(scope, dims); ErrorCode(err) != "INVALID_REQUEST" {
			t.Fatalf("scope admitted: %#v %v", scope, err)
		}
	}
}

func TestScopeNormalizationFollowsDuplicateAdmission(t *testing.T) {
	values := []string{"z", "a", "z"}
	scope := QueryScope{{Dimension: "z", Match: ScopeOneOf, Values: &values}, {Dimension: "a", Match: ScopeAny}}
	p, err := ToProtoReadTarget(ReadTarget{Route: AccessRoute{Connection: "route"}, Provider: "provider", Authority: "authority", Type: "resource/type", Representation: "native", Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	if p.Scope.Constraints[0].Dimension != "a" || p.Scope.Constraints[1].GetOneOf().Values[0] != "a" || len(p.Scope.Constraints[1].GetOneOf().Values) != 2 {
		t.Fatal("scope not normalized")
	}
	if values[0] != "z" || len(values) != 3 || scope[0].Dimension != "z" {
		t.Fatal("normalization mutated caller")
	}
	p.Scope.Constraints = append(p.Scope.Constraints, p.Scope.Constraints[0])
	if _, err := FromProtoReadTarget(p); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("duplicate erased before admission: %v", err)
	}
}

func TestOptionalEmptyTokensReject(t *testing.T) {
	r := codecRecord()
	r.Reference.Incarnation = codecToken("")
	if err := ValidateRecord(r); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("empty incarnation: %v", err)
	}
	r = codecRecord()
	r.SourceRevision = codecToken("")
	if err := ValidateRecord(r); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("empty source revision: %v", err)
	}
	p := SourcePage{Records: []Record{}, Count: Count{Kind: "unknown"}, Coverage: Coverage{Kind: "complete"}, Consistency: "bestEffort", NextCursor: codecToken("")}
	if err := ValidateSourcePage(p); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("empty cursor: %v", err)
	}
	r = codecRecord()
	r.Body = Value{Kind: "list", List: []Value{}}
	if err := ValidateRecord(r); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("non-map resource root: %v", err)
	}
}

func TestSchemaDocumentBindingAndLocalRoot(t *testing.T) {
	ref := SchemaDocumentReference{ID: "shared-document", Revision: "doc-1"}
	binding := SchemaBinding{State: "available", Revision: codecToken("schema-1"), Dialect: "json-schema", DocumentRef: &ref, Root: "#/$defs/a~1b/%E9%9B%AA", Validation: "none"}
	if _, err := ToProtoEditorSchema(EditorSchema{Type: "resource/type", FileMatch: "*.json", URI: "omniview://schema", Language: "json", Binding: binding}); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{"https://outside/schema", "#", "#/bad~2escape", "#/%ff", "#/%", "#/space here", "#/bad#fragment"} {
		b := binding
		b.Root = root
		if _, err := ToProtoEditorSchema(EditorSchema{Type: "resource/type", FileMatch: "*.json", URI: "omniview://schema", Language: "json", Binding: b}); ErrorCode(err) != "INVALID_REQUEST" {
			t.Fatalf("invalid root %q: %v", root, err)
		}
	}
	both := binding
	both.Document = []byte(`{}`)
	if _, err := ToProtoEditorSchema(EditorSchema{Type: "resource/type", FileMatch: "*.json", URI: "omniview://schema", Language: "json", Binding: both}); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("ambiguous document binding: %v", err)
	}
	missing := binding
	missing.DocumentRef = nil
	if _, err := ToProtoEditorSchema(EditorSchema{Type: "resource/type", FileMatch: "*.json", URI: "omniview://schema", Language: "json", Binding: missing}); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("missing document binding: %v", err)
	}
	inline := binding
	inline.DocumentRef = nil
	inline.Document = []byte(`{"x":1,"x":2}`)
	if _, err := ToProtoEditorSchema(EditorSchema{Type: "resource/type", FileMatch: "*.json", URI: "omniview://schema", Language: "json", Binding: inline}); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("duplicate schema JSON: %v", err)
	}
	reduced := binding
	reduced.Validation = "reduced"
	if _, err := ToProtoEditorSchema(EditorSchema{Type: "resource/type", FileMatch: "*.json", URI: "omniview://schema", Language: "json", Binding: reduced}); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("reduced validation lacks claims: %v", err)
	}
	reduced.Unvalidated = []string{"external-ref"}
	if _, err := ToProtoEditorSchema(EditorSchema{Type: "resource/type", FileMatch: "*.json", URI: "omniview://schema", Language: "json", Binding: reduced}); err != nil {
		t.Fatal(err)
	}
	full := reduced
	full.Validation = "full"
	if _, err := ToProtoEditorSchema(EditorSchema{Type: "resource/type", FileMatch: "*.json", URI: "omniview://schema", Language: "json", Binding: full}); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("full validation hides limitations: %v", err)
	}
}

func TestCatalogHintCoverageAndGuarantees(t *testing.T) {
	c := Catalog{Provider: "p", Revision: "r", Coverage: Coverage{Kind: "complete"}, Types: []ResourceType{}}
	for _, coverage := range []Coverage{{Kind: "complete"}, {Kind: "partial", Reason: "selected discovery"}, {Kind: "unknown", Reason: "discovery coverage unknown"}} {
		c.Coverage = coverage
		if err := ValidateCatalog(c); err != nil {
			t.Fatal(err)
		}
	}
	c.Coverage = Coverage{Kind: "unavailable", Reason: "failed"}
	if err := ValidateCatalog(c); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("failure disguised as catalog: %v", err)
	}
	c.Coverage = Coverage{Kind: "complete"}
	c.Types = []ResourceType{{Type: "cluster/pod", NativeAuthorityHint: &NativeAuthorityHint{Scheme: "fixture.v1", ID: "id", Continuity: ContinuityConnectionLocal}}, {Type: "localHelm/release"}}
	if err := ValidateCatalog(c); err != nil {
		t.Fatal(err)
	}
	c.Types[0].NativeAuthorityHint.Continuity = "authoritative"
	if err := ValidateCatalog(c); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("unknown hint continuity: %v", err)
	}
	c = Catalog{Provider: "p", Revision: "r", Coverage: Coverage{Kind: "complete"}, Types: []ResourceType{{Type: "resource/type", Capabilities: []Capability{{Operation: OperationUpdate}}}}}
	if _, err := ToProtoCatalog(c); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("missing update guarantee: %v", err)
	}
	c.Types[0].Capabilities = []Capability{{Operation: OperationGet, Guarantee: BestEffortGuarantee}}
	if _, err := ToProtoCatalog(c); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("finite operation update guarantee: %v", err)
	}
}

func TestUnavailableSchemaKeepsKnownProvenance(t *testing.T) {
	binding := SchemaBinding{State: "unavailable", Revision: codecToken("known-schema-revision"), Dialect: "known-dialect", Root: "#/known~1root", DocumentRef: &SchemaDocumentReference{ID: "document", Revision: "known-document-revision"}, Validation: "none"}
	adapterRoundTrip(t, EditorSchema{Type: "resource/type", FileMatch: "*.json", URI: "omniview://schema", Language: "json", Binding: binding}, ToProtoEditorSchema, FromProtoEditorSchema)
	binding.Document = []byte(`{}`)
	if _, err := ToProtoEditorSchema(EditorSchema{Type: "resource/type", FileMatch: "*.json", URI: "omniview://schema", Language: "json", Binding: binding}); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("unavailable inline document: %v", err)
	}
	binding.Document = nil
	binding.State = "absent"
	if _, err := ToProtoEditorSchema(EditorSchema{Type: "resource/type", FileMatch: "*.json", URI: "omniview://schema", Language: "json", Binding: binding}); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("absent schema has fabricated provenance: %v", err)
	}
}

func TestDefaultRepresentationIsExplicitAndDeclared(t *testing.T) {
	representation := codecRecord().Representation
	typ := ResourceType{Type: "resource/type", Representations: []Representation{representation}, DefaultRepresentation: representation.ID}
	catalog := Catalog{Provider: "provider", Revision: "catalog-1", Coverage: Coverage{Kind: "complete"}, Types: []ResourceType{typ}}
	adapterRoundTrip(t, catalog, ToProtoCatalog, FromProtoCatalog)
	typ.DefaultRepresentation = "undeclared"
	catalog.Types[0] = typ
	if _, err := ToProtoCatalog(catalog); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("unknown default representation: %v", err)
	}
	typ.DefaultRepresentation = ""
	catalog.Types[0] = typ
	p, err := ToProtoCatalog(catalog)
	if err != nil {
		t.Fatal(err)
	}
	got, err := FromProtoCatalog(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Types[0].DefaultRepresentation != "" {
		t.Fatal("adapter invented first representation as default")
	}
}
