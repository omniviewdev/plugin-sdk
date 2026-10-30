package resourcev2

import (
	"encoding/json"
	"testing"
)

func queryDescriptorFixture() ResourceType {
	return ResourceType{
		Type:                  "resource/type",
		IdentityDimensions:    []Dimension{},
		QueryDimensions:       []Dimension{},
		Representations:       []Representation{codecRecord().Representation},
		DefaultRepresentation: "native",
		ModelRepresentation:   "native",
		Capabilities:          []Capability{},
		NativeQueries: []NativeQueryDescriptor{
			{
				Language:    "selector.v1",
				Description: "Select native objects by labels.",
				InputSchema: Schema{
					Properties: map[string]SchemaProperty{"selector": {Type: SchemaString}},
					Required:   []string{"selector"},
				},
			},
		},
	}
}

func TestNativeQueryMetadataRoundTrip(t *testing.T) {
	descriptor := queryDescriptorFixture()
	adapterRoundTrip(
		t,
		Catalog{
			Provider: "provider",
			Revision: "catalog-1",
			Types:    []ResourceType{descriptor},
			Coverage: Coverage{Kind: "complete"},
		},
		ToProtoCatalog,
		FromProtoCatalog,
	)
	data, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ResourceType
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ModelRepresentation != "native" ||
		decoded.NativeQueries[0].Description != descriptor.NativeQueries[0].Description {
		t.Fatal("lost discovery metadata")
	}
	for _, mutate := range []func(*ResourceType){
		func(v *ResourceType) { v.ModelRepresentation = "missing" },
		func(v *ResourceType) { v.DefaultRepresentation = "missing" },
		func(v *ResourceType) { v.NativeQueries = append(v.NativeQueries, v.NativeQueries[0]) },
	} {
		value := queryDescriptorFixture()
		mutate(&value)
		if err := validateType(value); err == nil {
			t.Fatal("invalid discovery metadata admitted")
		}
	}
}

func TestNativeQueryDeclaredLanguageAndInput(t *testing.T) {
	descriptor := queryDescriptorFixture()
	request := NativeQueryRequest{
		Language: "selector.v1",
		Query:    Value{Kind: "map", Map: map[string]Value{"selector": {Kind: "string", String: "app=api"}}},
	}
	if err := ValidateNativeQueryInput(descriptor, request); err != nil {
		t.Fatal(err)
	}
	request.Language = "unknown"
	if ErrorCode(ValidateNativeQueryInput(descriptor, request)) != "UNSUPPORTED" {
		t.Fatal("undeclared language admitted")
	}
	request.Language = "selector.v1"
	request.Query.Map["selector"] = Value{Kind: "int64", Int: "9007199254740993"}
	if err := ValidateNativeQueryInput(descriptor, request); err == nil {
		t.Fatal("invalid query input admitted")
	}
	if request.Query.Map["selector"].Int != "9007199254740993" {
		t.Fatal("query coerced")
	}
}

func TestNativeQueryDescriptorRejectsDuplicateSchemaFields(t *testing.T) {
	for _, data := range []string{
		`{"language":"x","language":"y","inputSchema":{"type":"object"}}`,
		`{"language":"x","inputSchema":{"type":"object","properties":{"x":{"type":"string","type":"integer"}}}}`,
		`{"language":"x","inputSchema":{"type":"object","additionalProperties":false}}`,
	} {
		var descriptor NativeQueryDescriptor
		if err := json.Unmarshal([]byte(data), &descriptor); err == nil {
			t.Fatalf("invalid descriptor admitted: %s", data)
		}
	}
}
