package resourcev2

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	"google.golang.org/protobuf/proto"
)

func codecToken(s string) *string { return &s }
func codecRecord() Record {
	return Record{
		Reference:      ResourceReference{Address: ResourceAddress{Provider: "provider", Authority: "opaque-authority", Type: "resource/type", Scope: []ScopeEntry{{Dimension: "region", Value: "雪"}}, ID: "opaque/id::%2F"}, Incarnation: codecToken("incarnation")},
		Body:           Value{Kind: "map", Map: map[string]Value{"integer": {Kind: "int64", Int: "9007199254740993"}, "empty": {Kind: "list", List: []Value{}}}},
		Representation: Representation{ID: "native", Revision: "representation-1", Schema: SchemaBinding{State: "absent", Validation: "none", Unvalidated: []string{}}},
		Coverage:       Coverage{Kind: "complete", Paths: []string{}}, SourceRevision: codecToken("object-revision"),
	}
}
func adapterRoundTrip[D any, P proto.Message](t *testing.T, value D, to func(D) (P, error), from func(P) (D, error)) {
	t.Helper()
	p, err := to(value)
	if err != nil {
		t.Fatal(err)
	}
	data, err := proto.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	decoded := p.ProtoReflect().New().Interface()
	if err := proto.Unmarshal(data, decoded); err != nil {
		t.Fatal(err)
	}
	domain, err := from(decoded.(P))
	if err != nil {
		t.Fatal(err)
	}
	again, err := to(domain)
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(p, again) {
		t.Fatalf("unstable domain adapter: %T", p)
	}
}

func TestEveryFiniteDomainAdapter(t *testing.T) {
	r := codecRecord()
	route := AccessRoute{Connection: "route"}
	values := []string{"雪", "region-2", "雪"}
	scope := QueryScope{{Dimension: "region", Match: ScopeOneOf, Values: &values}}
	target := ReadTarget{Route: route, Provider: r.Reference.Address.Provider, Authority: r.Reference.Address.Authority, Type: r.Reference.Address.Type, Scope: scope, Representation: r.Representation.ID}
	typ := ResourceType{Type: target.Type, IdentityDimensions: []Dimension{{Name: "region"}}, QueryDimensions: []Dimension{{Name: "region"}}, Representations: []Representation{r.Representation}, Capabilities: []Capability{{Operation: OperationEnumerate, Scope: scope}, {Operation: OperationUpdate, Scope: scope, Guarantee: BestEffortGuarantee}}}
	catalog := Catalog{Provider: target.Provider, Revision: "catalog-1", Types: []ResourceType{typ}, Coverage: Coverage{Kind: "complete"}, NativeAuthorityHint: &NativeAuthorityHint{Scheme: "fixture.v1", ID: "provider-native-id", Continuity: ContinuityConnectionLocal}}
	adapterRoundTrip(t, r.Body, ToProtoValue, FromProtoValue)
	adapterRoundTrip(t, r.Reference.Address.Scope[0], ToProtoScopeEntry, FromProtoScopeEntry)
	adapterRoundTrip(t, r.Reference.Address, ToProtoResourceAddress, FromProtoResourceAddress)
	adapterRoundTrip(t, route, ToProtoAccessRoute, FromProtoAccessRoute)
	adapterRoundTrip(t, r.Reference, ToProtoResourceReference, FromProtoResourceReference)
	adapterRoundTrip(t, scope[0], ToProtoScopeConstraint, FromProtoScopeConstraint)
	adapterRoundTrip(t, scope, ToProtoQueryScope, FromProtoQueryScope)
	adapterRoundTrip(t, r.Representation.Schema, ToProtoSchemaBinding, FromProtoSchemaBinding)
	adapterRoundTrip(t, r.Representation, ToProtoRepresentation, FromProtoRepresentation)
	adapterRoundTrip(t, typ.IdentityDimensions[0], ToProtoDimension, FromProtoDimension)
	adapterRoundTrip(t, typ.Capabilities[0], ToProtoCapability, FromProtoCapability)
	adapterRoundTrip(t, typ.Capabilities[1], ToProtoCapability, FromProtoCapability)
	adapterRoundTrip(t, typ, ToProtoResourceType, FromProtoResourceType)
	adapterRoundTrip(t, *catalog.NativeAuthorityHint, ToProtoNativeAuthorityHint, FromProtoNativeAuthorityHint)
	adapterRoundTrip(t, catalog, ToProtoCatalog, FromProtoCatalog)
	adapterRoundTrip(t, CatalogRequest{Route: route}, ToProtoCatalogRequest, FromProtoCatalogRequest)
	adapterRoundTrip(t, target, ToProtoReadTarget, FromProtoReadTarget)
	adapterRoundTrip(t, GetRequest{Route: route, Reference: r.Reference, Representation: r.Representation.ID}, ToProtoGetRequest, FromProtoGetRequest)
	adapterRoundTrip(t, EnumerateRequest{Target: target, PageSize: 256, Cursor: codecToken("cursor")}, ToProtoEnumerateRequest, FromProtoEnumerateRequest)
	adapterRoundTrip(t, NativeQueryRequest{Target: target, Language: "native.v1", Query: r.Body, PageSize: 1}, ToProtoNativeQueryRequest, FromProtoNativeQueryRequest)
	adapterRoundTrip(t, r.Coverage, ToProtoCoverage, FromProtoCoverage)
	adapterRoundTrip(t, Count{Kind: "exact", Value: codecToken("18446744073709551615")}, ToProtoCount, FromProtoCount)
	adapterRoundTrip(t, r, ToProtoRecord, FromProtoRecord)
	adapterRoundTrip(t, SourcePage{Records: []Record{r}, Count: Count{Kind: "unknown"}, Coverage: r.Coverage, Consistency: "bestEffort", SourceRevision: codecToken("independent-page-revision")}, ToProtoSourcePage, FromProtoSourcePage)
	adapterRoundTrip(t, MutationRequest{Route: route, Reference: r.Reference, Operation: "patch", Input: r.Body, ExpectedRevision: codecToken("expected")}, ToProtoMutationRequest, FromProtoMutationRequest)
	adapterRoundTrip(t, ActionRequest{Route: route, Reference: r.Reference, Action: "act", Input: r.Body}, ToProtoActionRequest, FromProtoActionRequest)
	reference := SchemaDocumentReference{ID: "schema/document", Revision: "document-1"}
	adapterRoundTrip(t, reference, ToProtoSchemaDocumentReference, FromProtoSchemaDocumentReference)
	adapterRoundTrip(t, SchemaDocument{Reference: reference, Dialect: "https://json-schema.org/draft/2020-12/schema", Document: []byte(`{"$defs":{"x":{"type":"object"}}}`)}, ToProtoSchemaDocument, FromProtoSchemaDocument)
	adapterRoundTrip(t, SchemaBinding{State: "available", Revision: codecToken("schema-1"), Dialect: "https://json-schema.org/draft/2020-12/schema", DocumentRef: &reference, Root: "#/$defs/x", Validation: "none"}, ToProtoSchemaBinding, FromProtoSchemaBinding)
}

func TestProtoScalarBoundariesAndSize(t *testing.T) {
	cases := []Value{
		{Kind: "null"}, {Kind: "bool"}, {Kind: "bool", Bool: true}, {Kind: "string", String: ""},
		{Kind: "int64", Int: "-9223372036854775808"}, {Kind: "int64", Int: "9223372036854775807"}, {Kind: "uint64", Uint: "18446744073709551615"},
		{Kind: "double", Double: math.Copysign(0, -1)},
		TimestampValue(time.Unix(-1, 999999999)), TimestampValue(time.Unix(0, 0)),
		{Kind: "timestamp", Timestamp: "0001-01-01T00:00:00Z"}, {Kind: "timestamp", Timestamp: "9999-12-31T23:59:59.999999999Z"},
		{Kind: "list", List: []Value{}}, {Kind: "map", Map: map[string]Value{"": {Kind: "null"}, "x": {Kind: "string", String: "雪"}}},
	}
	for _, v := range cases {
		p, err := ToProtoValue(v)
		if err != nil {
			t.Fatal(err)
		}
		size, err := ValueWireSize(v)
		if err != nil {
			t.Fatal(err)
		}
		if size != proto.Size(p) {
			t.Fatalf("size for %s: got %d want %d", v.Kind, size, proto.Size(p))
		}
		got, err := FromProtoValue(p)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(v, got) || math.Signbit(v.Double) != math.Signbit(got.Double) {
			t.Fatalf("scalar changed: %#v -> %#v", v, got)
		}
	}
	p, err := ToProtoRecord(codecRecord())
	if err != nil {
		t.Fatal(err)
	}
	size, err := RecordWireSize(codecRecord())
	if err != nil {
		t.Fatal(err)
	}
	if size != proto.Size(p) {
		t.Fatalf("record size got %d want %d", size, proto.Size(p))
	}
}

func TestProtoAdmissionRejectsInvalidValues(t *testing.T) {
	null := &resourcepb.Value{Selection: &resourcepb.Value_NullValue{NullValue: &resourcepb.NullValue{}}}
	cases := []*resourcepb.Value{
		nil, {},
		{Selection: &resourcepb.Value_NullValue{}},
		{Selection: &resourcepb.Value_ListValue{}},
		{Selection: &resourcepb.Value_MapValue{}},
		{Selection: &resourcepb.Value_ListValue{ListValue: &resourcepb.ListValue{Values: []*resourcepb.Value{nil}}}},
		{Selection: &resourcepb.Value_MapValue{MapValue: &resourcepb.MapValue{Entries: []*resourcepb.ValueEntry{{Key: "same", Value: null}, {Key: "same", Value: null}}}}},
		{Selection: &resourcepb.Value_TimestampValue{TimestampValue: &resourcepb.TimestampValue{Seconds: -62135596801}}},
		{Selection: &resourcepb.Value_TimestampValue{TimestampValue: &resourcepb.TimestampValue{Seconds: 253402300800}}},
		{Selection: &resourcepb.Value_TimestampValue{TimestampValue: &resourcepb.TimestampValue{Nanos: 1000000000}}},
		{Selection: &resourcepb.Value_DoubleValue{DoubleValue: math.Inf(1)}},
		{Selection: &resourcepb.Value_StringValue{StringValue: string([]byte{255})}},
	}
	for _, p := range cases {
		if _, err := FromProtoValue(p); ErrorCode(err) != "INVALID_REQUEST" {
			t.Fatalf("invalid proto admitted: %T %v", p, err)
		}
	}
	unknown := proto.Clone(null).(*resourcepb.Value)
	unknown.ProtoReflect().SetUnknown([]byte{120, 1})
	if _, err := FromProtoValue(unknown); ErrorCode(err) != "UNSUPPORTED" {
		t.Fatalf("unknown field class: %v", err)
	}
	duplicate := &resourcepb.ResourceAddress{Provider: "p", Authority: "a", Type: "t", Id: "id", Scope: []*resourcepb.ScopeEntry{{Dimension: "x", Value: "a"}, {Dimension: "x", Value: "b"}}}
	if _, err := FromProtoResourceAddress(duplicate); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("duplicate dimension: %v", err)
	}
}

func TestAdaptersDoNotShareTransportStorage(t *testing.T) {
	v := codecRecord()
	p, err := ToProtoRecord(v)
	if err != nil {
		t.Fatal(err)
	}
	*p.Reference.Incarnation = "transport-only"
	if *v.Reference.Incarnation != "incarnation" {
		t.Fatal("ToProto shared optional storage")
	}
	got, err := FromProtoRecord(p)
	if err != nil {
		t.Fatal(err)
	}
	*p.Reference.Incarnation = "changed-again"
	p.Reference.Address.Scope[0].Value = "changed"
	if *got.Reference.Incarnation != "transport-only" || got.Reference.Address.Scope[0].Value != "雪" {
		t.Fatal("FromProto shared transport storage")
	}
}

func TestFullResourceBodiesAndAggregatePages(t *testing.T) {
	r := codecRecord()
	r.Body = Value{Kind: "map", Map: map[string]Value{"large": {Kind: "string", String: strings.Repeat("x", 2<<20)}}}
	if err := ValidateRecord(r); err != nil {
		t.Fatalf("multi-MiB Get rejected by predicate budget: %v", err)
	}
	page := SourcePage{Records: []Record{r}, Count: Count{Kind: "unknown"}, Coverage: Coverage{Kind: "complete"}, Consistency: "bestEffort"}
	if err := ValidateSourcePage(page); err != nil {
		t.Fatal(err)
	}
	r.Body.Map["large"] = Value{Kind: "string", String: strings.Repeat("x", 12<<20)}
	page.Records = []Record{r, r, r}
	if err := ValidateSourcePage(page); ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("aggregate page budget: %v", err)
	}
	page.Records = make([]Record, MaxPageRecords+1)
	if err := ValidateSourcePage(page); ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("page record budget: %v", err)
	}
}
