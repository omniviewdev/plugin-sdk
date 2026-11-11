package resourcev2

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

func schemaRequestFixture() SchemaRequest {
	return SchemaRequest{
		Route: AccessRoute{Connection: "route"},
		Type:  "resource/type",
		Target: SchemaTarget{Representation: &Representation{
			ID: "document", Revision: "format-1",
		}},
	}
}

func auxiliarySchemaRequestFixture() SchemaRequest {
	request := schemaRequestFixture()
	request.Target = SchemaTarget{Auxiliary: &AuxiliarySchemaTarget{
		Key: "values",
		Reference: ResourceReference{Address: ResourceAddress{
			Provider: "provider", Authority: "authority", Type: request.Type,
			ID: "resource", Scope: []ScopeEntry{{Dimension: "namespace", Value: "team"}},
		}},
	}}
	return request
}

func TestSchemaRequestRequiresOneExactTarget(t *testing.T) {
	for _, request := range []SchemaRequest{schemaRequestFixture(), auxiliarySchemaRequestFixture()} {
		if err := ValidateSchemaRequest(request); err != nil {
			t.Fatal(err)
		}
	}
	maximal := auxiliarySchemaRequestFixture()
	maximal.Target.Auxiliary.Key = strings.Repeat("k", 256)
	maximal.Target.Auxiliary.SourceRevision = codecToken("source-1")
	if err := ValidateSchemaRequest(maximal); err != nil {
		t.Fatalf("bounded exact auxiliary target rejected: %v", err)
	}
	for name, mutate := range map[string]func(*SchemaRequest){
		"missing route":           func(v *SchemaRequest) { v.Route = AccessRoute{} },
		"missing type":            func(v *SchemaRequest) { v.Type = "" },
		"missing target":          func(v *SchemaRequest) { v.Target = SchemaTarget{} },
		"two targets":             func(v *SchemaRequest) { v.Target.Auxiliary = auxiliarySchemaRequestFixture().Target.Auxiliary },
		"missing format id":       func(v *SchemaRequest) { v.Target.Representation.ID = "" },
		"missing format revision": func(v *SchemaRequest) { v.Target.Representation.Revision = "" },
		"invalid format revision": func(v *SchemaRequest) { v.Target.Representation.Revision = "\xff" },
	} {
		t.Run(name, func(t *testing.T) {
			request := schemaRequestFixture()
			mutate(&request)
			if err := ValidateSchemaRequest(request); ErrorCode(err) != "INVALID_REQUEST" {
				t.Fatalf("invalid schema request admitted: %+v %v", request, err)
			}
			if p, err := ToProtoSchemaRequest(request); ErrorCode(err) != "INVALID_REQUEST" || p != nil {
				t.Fatalf("invalid schema request encoded: %v %v", p, err)
			}
		})
	}
	for name, mutate := range map[string]func(*AuxiliarySchemaTarget){
		"missing key":              func(v *AuxiliarySchemaTarget) { v.Key = "" },
		"oversized key":            func(v *AuxiliarySchemaTarget) { v.Key = strings.Repeat("k", 257) },
		"control key":              func(v *AuxiliarySchemaTarget) { v.Key = "a\nb" },
		"invalid utf8 key":         func(v *AuxiliarySchemaTarget) { v.Key = "\xff" },
		"missing provider":         func(v *AuxiliarySchemaTarget) { v.Reference.Address.Provider = "" },
		"missing authority":        func(v *AuxiliarySchemaTarget) { v.Reference.Address.Authority = "" },
		"missing reference type":   func(v *AuxiliarySchemaTarget) { v.Reference.Address.Type = "" },
		"different reference type": func(v *AuxiliarySchemaTarget) { v.Reference.Address.Type = "other/type" },
		"missing resource id":      func(v *AuxiliarySchemaTarget) { v.Reference.Address.ID = "" },
		"empty incarnation":        func(v *AuxiliarySchemaTarget) { v.Reference.Incarnation = codecToken("") },
		"empty source revision":    func(v *AuxiliarySchemaTarget) { v.SourceRevision = codecToken("") },
		"invalid source revision":  func(v *AuxiliarySchemaTarget) { v.SourceRevision = codecToken("\xff") },
	} {
		t.Run(name, func(t *testing.T) {
			request := auxiliarySchemaRequestFixture()
			mutate(request.Target.Auxiliary)
			if err := ValidateSchemaRequest(request); ErrorCode(err) != "INVALID_REQUEST" {
				t.Fatalf("invalid auxiliary schema target admitted: %+v %v", request, err)
			}
		})
	}
	oversized := auxiliarySchemaRequestFixture()
	oversized.Target.Auxiliary.SourceRevision = codecToken(strings.Repeat("s", MaxMessageBytes+1))
	if err := ValidateSchemaRequest(oversized); ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("oversized schema request admitted: %v", err)
	}
}

func TestSchemaRequestStrictJSONAdmission(t *testing.T) {
	const representation = `{"route":{"connection":"route"},"type":"resource/type","target":{"representation":{"id":"document","revision":"format-1"}}}`
	const auxiliary = `{"route":{"connection":"route"},"type":"resource/type","target":{"auxiliary":{"key":"values","reference":{"address":{"provider":"provider","authority":"authority","type":"resource/type","id":"resource","scope":[]}},"sourceRevision":"source-1"}}}`
	for _, input := range []string{representation, auxiliary} {
		var request SchemaRequest
		if err := json.Unmarshal([]byte(input), &request); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		var decoded SchemaRequest
		if err := json.Unmarshal(encoded, &decoded); err != nil || !reflect.DeepEqual(request, decoded) {
			t.Fatalf("schema request JSON changed: %s %+v %v", encoded, decoded, err)
		}
	}
	for _, input := range []string{
		`null`, `[]`, `{}`,
		strings.Replace(representation, `"route":`, `"unknown":{},"route":`, 1),
		strings.Replace(representation, `"type":"resource/type"`, `"type":"resource/type","type":"resource/type"`, 1),
		strings.Replace(representation, `"target":{`, `"target":{"unknown":{},`, 1),
		strings.Replace(representation, `"representation":{`, `"representation":null,"representation":{`, 1),
		strings.Replace(representation, `"revision":"format-1"`, `"revision":"format-1","revision":"format-1"`, 1),
		strings.Replace(representation, `"revision":"format-1"`, `"revision":"format-1","schema":{"state":"absent"}`, 1),
		strings.Replace(representation, `"revision":"format-1"`, `"revision":""`, 1),
		strings.Replace(representation, `{"connection":"route"}`, `null`, 1),
		strings.Replace(representation, `"target":{"representation":{"id":"document","revision":"format-1"}}`, `"target":{}`, 1),
		strings.Replace(auxiliary, `"sourceRevision":"source-1"`, `"sourceRevision":null`, 1),
		strings.Replace(auxiliary, `"sourceRevision":"source-1"`, `"sourceRevision":""`, 1),
		strings.Replace(auxiliary, `"key":"values"`, `"key":"values","key":"other"`, 1),
		strings.Replace(auxiliary, `"reference":{`, `"reference":{"unknown":true,`, 1),
		strings.Replace(auxiliary, `"id":"resource"`, `"id":"resource","unknown":true`, 1),
		strings.Replace(auxiliary, `"target":{"auxiliary":`, `"target":{"representation":{"id":"document","revision":"format-1"},"auxiliary":`, 1),
	} {
		before := schemaRequestFixture()
		request := before
		if err := json.Unmarshal([]byte(input), &request); ErrorCode(err) != "INVALID_REQUEST" {
			t.Fatalf("invalid schema request JSON admitted: %s %v", input, err)
		}
		if !reflect.DeepEqual(request, before) {
			t.Fatalf("failed admission changed destination: %+v", request)
		}
	}
	for _, target := range []SchemaTarget{{}, {
		Representation: &Representation{ID: "document", Revision: "format-1"},
		Auxiliary:      auxiliarySchemaRequestFixture().Target.Auxiliary,
	}} {
		if _, err := json.Marshal(target); ErrorCode(err) != "INVALID_REQUEST" {
			t.Fatalf("ambiguous target JSON encoded: %v", err)
		}
	}
}

func TestSchemaRequestProtoAdmissionAndOwnership(t *testing.T) {
	adapterRoundTrip(t, schemaRequestFixture(), ToProtoSchemaRequest, FromProtoSchemaRequest)
	request := auxiliarySchemaRequestFixture()
	request.Target.Auxiliary.SourceRevision = codecToken("source-1")
	request.Target.Auxiliary.Reference.Incarnation = codecToken("incarnation-1")
	adapterRoundTrip(t, request, ToProtoSchemaRequest, FromProtoSchemaRequest)
	encoded, err := ToProtoSchemaRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	request.Target.Auxiliary.Reference.Address.Scope[0].Value = "changed"
	*request.Target.Auxiliary.SourceRevision = "changed"
	*request.Target.Auxiliary.Reference.Incarnation = "changed"
	wire := encoded.GetTarget().GetAuxiliary()
	if wire.Reference.Address.Scope[0].Value != "team" || *wire.SourceRevision != "source-1" ||
		*wire.Reference.Incarnation != "incarnation-1" {
		t.Fatal("encoding retained borrowed auxiliary storage")
	}
	decoded, err := FromProtoSchemaRequest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	wire.Reference.Address.Scope[0].Value = "wire-change"
	*wire.SourceRevision = "wire-change"
	*wire.Reference.Incarnation = "wire-change"
	if decoded.Target.Auxiliary.Reference.Address.Scope[0].Value != "team" ||
		*decoded.Target.Auxiliary.SourceRevision != "source-1" ||
		*decoded.Target.Auxiliary.Reference.Incarnation != "incarnation-1" {
		t.Fatal("decoding retained borrowed auxiliary storage")
	}
	for name, mutate := range map[string]func(*resourcepb.SchemaRequest){
		"missing route":      func(p *resourcepb.SchemaRequest) { p.Route = nil },
		"missing type":       func(p *resourcepb.SchemaRequest) { p.Type = "" },
		"missing target":     func(p *resourcepb.SchemaRequest) { p.Target = nil },
		"unset target":       func(p *resourcepb.SchemaRequest) { p.Target.Selection = nil },
		"nil representation": func(p *resourcepb.SchemaRequest) { p.Target.Selection = &resourcepb.SchemaTarget_Representation{} },
		"missing revision":   func(p *resourcepb.SchemaRequest) { p.Target.GetRepresentation().Revision = "" },
		"missing auxiliary reference": func(p *resourcepb.SchemaRequest) {
			p.Target.Selection = &resourcepb.SchemaTarget_Auxiliary{Auxiliary: &resourcepb.AuxiliarySchemaTarget{Key: "values"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := ToProtoSchemaRequest(schemaRequestFixture())
			if err != nil {
				t.Fatal(err)
			}
			mutate(p)
			if _, err := FromProtoSchemaRequest(p); ErrorCode(err) != "INVALID_REQUEST" {
				t.Fatalf("invalid protobuf schema request admitted: %v", err)
			}
		})
	}
	unknown, err := ToProtoSchemaRequest(schemaRequestFixture())
	if err != nil {
		t.Fatal(err)
	}
	unknown.ProtoReflect().SetUnknown([]byte{0x78, 1})
	if _, err := FromProtoSchemaRequest(unknown); err == nil {
		t.Fatal("unknown protobuf schema request field admitted")
	}
	if _, err := FromProtoSchemaRequest(nil); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("nil schema request admitted: %v", err)
	}
}

func TestSchemaDocumentRequestAdmission(t *testing.T) {
	request := SchemaDocumentRequest{
		Request:   auxiliarySchemaRequestFixture(),
		Reference: SchemaDocumentReference{ID: "shared-document", Revision: "document-1"},
	}
	if err := ValidateSchemaDocumentRequest(request); err != nil {
		t.Fatal(err)
	}
	adapterRoundTrip(t, request, ToProtoSchemaDocumentRequest, FromProtoSchemaDocumentRequest)
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var decoded SchemaDocumentRequest
	if err := json.Unmarshal(data, &decoded); err != nil || !reflect.DeepEqual(request, decoded) {
		t.Fatalf("document request JSON changed: %+v %v", decoded, err)
	}
	for _, input := range []string{
		`{}`, `null`,
		`{"request":null,"reference":{"id":"shared-document","revision":"document-1"}}`,
		strings.Replace(string(data), `"reference":{"id":"shared-document"`, `"reference":{"unknown":true,"id":"shared-document"`, 1),
		strings.Replace(string(data), `"revision":"document-1"`, `"revision":"document-1","revision":"document-2"`, 1),
		strings.Replace(string(data), `"request":`, `"unknown":{},"request":`, 1),
		strings.Replace(string(data), `"revision":"document-1"`, `"revision":""`, 1),
	} {
		if err := json.Unmarshal([]byte(input), &decoded); ErrorCode(err) != "INVALID_REQUEST" {
			t.Fatalf("invalid document request JSON admitted: %s %v", input, err)
		}
	}
	for name, mutate := range map[string]func(*SchemaDocumentRequest){
		"missing target":            func(v *SchemaDocumentRequest) { v.Request.Target = SchemaTarget{} },
		"missing document id":       func(v *SchemaDocumentRequest) { v.Reference.ID = "" },
		"missing document revision": func(v *SchemaDocumentRequest) { v.Reference.Revision = "" },
	} {
		t.Run(name, func(t *testing.T) {
			value := request
			mutate(&value)
			if err := ValidateSchemaDocumentRequest(value); ErrorCode(err) != "INVALID_REQUEST" {
				t.Fatalf("invalid exact document request admitted: %v", err)
			}
			if p, err := ToProtoSchemaDocumentRequest(value); ErrorCode(err) != "INVALID_REQUEST" || p != nil {
				t.Fatalf("invalid exact document request encoded: %v %v", p, err)
			}
		})
	}
	for _, p := range []*resourcepb.SchemaDocumentRequest{
		nil, {}, {Reference: &resourcepb.SchemaDocumentReference{Id: "document", Revision: "revision"}},
	} {
		if _, err := FromProtoSchemaDocumentRequest(p); ErrorCode(err) != "INVALID_REQUEST" {
			t.Fatalf("missing target document request admitted: %v", err)
		}
	}
	p, err := ToProtoSchemaDocumentRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	p.Reference = nil
	if _, err := FromProtoSchemaDocumentRequest(p); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("missing exact document reference admitted: %v", err)
	}
}
