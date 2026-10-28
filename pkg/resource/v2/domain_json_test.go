package resourcev2

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func TestPublicDomainJSONRejectsMalformedIdentity(t *testing.T) {
	cases := []struct {
		name, source string
		destination  func() any
	}{
		{"duplicate address field", `{"provider":"p","provider":"p","authority":"a","type":"t","scope":[],"id":"id"}`, func() any { return &ResourceAddress{} }},
		{"unknown address field", `{"provider":"p","authority":"a","type":"t","scope":[],"id":"id","namespace":"x"}`, func() any { return &ResourceAddress{} }},
		{"wrong field case", `{"Provider":"p","authority":"a","type":"t","scope":[],"id":"id"}`, func() any { return &ResourceAddress{} }},
		{"missing scope", `{"provider":"p","authority":"a","type":"t","id":"id"}`, func() any { return &ResourceAddress{} }},
		{"null scope", `{"provider":"p","authority":"a","type":"t","scope":null,"id":"id"}`, func() any { return &ResourceAddress{} }},
		{"object scope", `{"provider":"p","authority":"a","type":"t","scope":{},"id":"id"}`, func() any { return &ResourceAddress{} }},
		{"duplicate dimensions", `{"provider":"p","authority":"a","type":"t","scope":[{"dimension":"x","value":"a"},{"dimension":"x","value":"b"}],"id":"id"}`, func() any { return &ResourceAddress{} }},
		{"unpaired surrogate", `{"provider":"p","authority":"a","type":"t","scope":[],"id":"\ud800"}`, func() any { return &ResourceAddress{} }},
		{"any null values", `{"dimension":"x","match":"any","values":null}`, func() any { return &ScopeConstraint{} }},
		{"any empty values", `{"dimension":"x","match":"any","values":[]}`, func() any { return &ScopeConstraint{} }},
		{"oneOf absent values", `{"dimension":"x","match":"oneOf"}`, func() any { return &ScopeConstraint{} }},
		{"oneOf null values", `{"dimension":"x","match":"oneOf","values":null}`, func() any { return &ScopeConstraint{} }},
		{"oneOf unknown field", `{"dimension":"x","match":"oneOf","values":[],"unknown":0}`, func() any { return &ScopeConstraint{} }},
		{"duplicate nested entry member", `[{"dimension":"x","dimension":"x","match":"any"}]`, func() any { return new(QueryScope) }},
		{"duplicate query dimension", `[{"dimension":"x","match":"any"},{"dimension":"x","match":"any"}]`, func() any { return new(QueryScope) }},
		{"null query scope", `null`, func() any { return new(QueryScope) }},
		{"empty cursor", `{"target":{"route":{"connection":"c"},"provider":"p","authority":"a","type":"t","scope":[],"representation":"native"},"cursor":""}`, func() any { return &EnumerateRequest{} }},
		{"null cursor", `{"target":{"route":{"connection":"c"},"provider":"p","authority":"a","type":"t","scope":[],"representation":"native"},"cursor":null}`, func() any { return &EnumerateRequest{} }},
		{"unknown route field", `{"route":{"connection":"c","principal":"caller"}}`, func() any { return &CatalogRequest{} }},
		{"duplicate schema revision", `{"id":"doc","revision":"r","revision":"r"}`, func() any { return &SchemaDocumentReference{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(tc.source), tc.destination()); ErrorCode(err) != "INVALID_REQUEST" {
				t.Fatalf("class %s: %v", ErrorCode(err), err)
			}
		})
	}
}

func TestPublicDomainJSONReferenceTokens(t *testing.T) {
	base := `{"address":{"provider":"p","authority":"a","type":"opaque/type","scope":[],"id":"opaque::id/%2F雪"}`
	for _, suffix := range []string{`,"incarnation":""}`, `,"incarnation":null}`} {
		var reference ResourceReference
		if err := reference.UnmarshalJSON([]byte(base + suffix)); ErrorCode(err) != "INVALID_REQUEST" {
			t.Fatalf("optional token accepted: %v", err)
		}
	}
	var reference ResourceReference
	if err := reference.UnmarshalJSON([]byte(base + `}`)); err != nil {
		t.Fatal(err)
	}
	if reference.Incarnation != nil || reference.Address.ID != "opaque::id/%2F雪" || reference.Address.Type != "opaque/type" {
		t.Fatal("adapter invented or normalized identity")
	}
}

func TestPublicDomainJSONNormalizesOnlyAfterAdmission(t *testing.T) {
	var scope QueryScope
	if err := scope.UnmarshalJSON([]byte(`[{"dimension":"z","match":"oneOf","values":["b","a","b"]},{"dimension":"a","match":"any"}]`)); err != nil {
		t.Fatal(err)
	}
	if scope[0].Dimension != "a" || scope[1].Values == nil || len(*scope[1].Values) != 2 || (*scope[1].Values)[0] != "a" {
		t.Fatalf("bad normalized scope: %#v", scope)
	}
	var empty ScopeConstraint
	if err := empty.UnmarshalJSON([]byte(`{"dimension":"x","match":"oneOf","values":[]}`)); err != nil {
		t.Fatal(err)
	}
	if empty.Values == nil || *empty.Values == nil || len(*empty.Values) != 0 {
		t.Fatal("empty oneOf lost actual array")
	}
	var request GetRequest
	if err := request.UnmarshalJSON([]byte(`{"route":{"connection":"c"},"reference":{"address":{"provider":"p","authority":"a","type":"t","scope":[],"id":"id"}},"representation":"native"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestMetadataIgnoresHiddenCredentialsAndBoundsVisibleWork(t *testing.T) {
	cycle := map[string]any{}
	cycle["self"] = cycle
	value := struct {
		Visible string         `json:"visible"`
		Hidden  map[string]any `json:"-"`
		private map[string]any
	}{Visible: "visible", Hidden: cycle, private: map[string]any{"time": time.Now(), "cycle": cycle}}
	if err := ValidateMetadata(value); err != nil {
		t.Fatalf("visited hidden credential graph: %v", err)
	}
	if err := ValidateMetadata(map[string]any{"bad": math.Inf(1)}); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("nonfinite metadata: %v", err)
	}
	if err := ValidateMetadata(map[string]any{"cycle": cycle}); ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("unbounded visible graph: %v", err)
	}
	if err := ValidateMetadata(strings.Repeat("<", MaxMessageBytes/6+1)); ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("escaped metadata bytes: %v", err)
	}
}

func TestCanonicalJSONEmitsSelectedEmptyArrays(t *testing.T) {
	address := ResourceAddress{Provider: "p", Authority: "a", Type: "t", ID: "opaque-id"}
	data, err := json.Marshal(address)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"scope":[]`) {
		t.Fatalf("nil identity scope: %s", data)
	}
	var decoded ResourceAddress
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	var scope QueryScope
	data, err = json.Marshal(scope)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "[]" {
		t.Fatalf("nil query scope: %s", data)
	}
	if err := json.Unmarshal(data, &scope); err != nil {
		t.Fatal(err)
	}
	var values []string
	constraint := ScopeConstraint{Dimension: "x", Match: ScopeOneOf, Values: &values}
	data, err = json.Marshal(constraint)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"values":[]`) {
		t.Fatalf("nil selected oneOf: %s", data)
	}
	if err := json.Unmarshal(data, &constraint); err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(ScopeConstraint{Dimension: "x", Match: ScopeAny})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"values"`) {
		t.Fatalf("any has values: %s", data)
	}
	catalog := Catalog{Provider: "p", Revision: "r", Coverage: Coverage{Kind: "complete"}}
	data, err = json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatalf("empty catalog did not roundtrip: %s %v", data, err)
	}
}
