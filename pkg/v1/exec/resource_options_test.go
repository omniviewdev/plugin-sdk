package exec

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	execpb "github.com/omniviewdev/plugin-sdk/proto/v1/exec"
)

func exactExecBody() *resourcev2.Value {
	return &resourcev2.Value{Kind: "map", Map: map[string]resourcev2.Value{
		"intMin":      {Kind: "int64", Int: "-9223372036854775808"},
		"wideUint":    {Kind: "uint64", Uint: "18446744073709551615"},
		"beforeEpoch": {Kind: "timestamp", Timestamp: "1969-12-31T23:59:59.999999999Z"},
		"nested": {
			Kind: "list",
			List: []resourcev2.Value{
				{Kind: "double", Double: math.Copysign(0, -1)},
				{Kind: "null"},
				{Kind: "map", Map: map[string]resourcev2.Value{}},
			},
		},
	}}
}

func exactExecOptions() SessionOptions {
	return SessionOptions{
		ResourcePlugin: "provider",
		ResourceKey:    "opaque/type::/%雪",
		ResourceData:   exactExecBody(),
		Command:        []string{"shell"},
		Params:         map[string]string{"container": "actual"},
		Labels:         map[string]string{"diagnostic": "not-a-grant"},
	}
}

func TestExecOptionsCanonicalBinaryRoundTrip(t *testing.T) {
	input := exactExecOptions()
	wire, err := input.ToProto()
	if err != nil {
		t.Fatal(err)
	}
	if wire.ResourceWireVersion != ResourceSessionWireVersion ||
		wire.ProtoReflect().Descriptor().Fields().ByName("resource_data").Number() != 9 {
		t.Fatal("resource payload did not select explicit canonical wire")
	}
	encoded, err := proto.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	decoded := new(execpb.SessionOptions)
	if err = proto.Unmarshal(encoded, decoded); err != nil {
		t.Fatal(err)
	}
	output, err := NewSessionOptionsFromProto(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(input, *output) {
		t.Fatalf("resource options changed: %#v", output)
	}
	if !math.Signbit(output.ResourceData.Map["nested"].List[0].Double) {
		t.Fatal("negative double zero changed")
	}
	plain := SessionOptions{Command: []string{"local-shell"}}
	plainWire, err := plain.ToProto()
	if err != nil {
		t.Fatal(err)
	}
	if plainWire.ResourceWireVersion != 0 || plainWire.ResourceData != nil {
		t.Fatal("plain terminal acquired resource intent")
	}
	plainBack, err := NewSessionOptionsFromProto(plainWire)
	if err != nil || plainBack.HasResourceIntent() {
		t.Fatalf("plain terminal path changed: %v %v", plainBack, err)
	}
}

func TestExecOptionsUseActualReferenceWithoutFabrication(t *testing.T) {
	ref := resourcev2.ResourceReference{
		Address: resourcev2.ResourceAddress{
			Provider:  "provider",
			Authority: "host-issued",
			Type:      "opaque/type",
			Scope:     []resourcev2.ScopeEntry{},
			ID:        "opaque::/%雪",
		},
	}
	input := SessionOptions{Reference: &ref, Route: &resourcev2.AccessRoute{Connection: "explicit"}}
	normalized, err := NormalizeResourceSessionOptions(input)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.ResourcePlugin != "provider" || normalized.ResourceKey != "opaque/type" ||
		normalized.Reference != input.Reference ||
		normalized.ResourceData != nil {
		t.Fatal("normalization fabricated or replaced canonical input")
	}
	if err := ValidateResourceSessionOptions(
		SessionOptions{ResourcePlugin: "provider", ResourceKey: "type"},
	); err != nil {
		t.Fatal("opaque utility selector incorrectly requires a body or resource Get")
	}
	for _, bad := range []SessionOptions{
		{Reference: &ref},
		{Reference: &ref, Route: &resourcev2.AccessRoute{Connection: "explicit"}, ResourcePlugin: "wrong"},
		{Reference: &ref, Route: &resourcev2.AccessRoute{Connection: "explicit"}, ResourceKey: "wrong"},
		{ResourcePlugin: "provider"},
		{ResourcePlugin: "provider", ResourceKey: "type", ResourceData: &resourcev2.Value{Kind: "uint64", Uint: "1"}},
	} {
		if err := ValidateResourceSessionOptions(bad); resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
			t.Fatalf("bad resource options accepted: %#v %v", bad, err)
		}
	}
}

func TestExecOptionsRejectLegacyAndUnsupportedProtoBeforeBody(t *testing.T) {
	legacy := new(execpb.SessionOptions)
	legacy.ProtoReflect().SetUnknown(protowire.AppendBytes(protowire.AppendTag(nil, 8, protowire.BytesType), nil))
	if _, err := NewSessionOptionsFromProto(legacy); resourcev2.ErrorCode(err) != "UNSUPPORTED" {
		t.Fatalf("legacy Struct field accepted: %v", err)
	}
	unsupported := &execpb.SessionOptions{
		ResourcePlugin:      "p",
		ResourceKey:         "t",
		ResourceWireVersion: 3,
		ResourceData:        &resourcepb.Value{},
	}
	if _, err := NewSessionOptionsFromProto(unsupported); resourcev2.ErrorCode(err) != "UNSUPPORTED" {
		t.Fatalf("version did not reject before malformed body conversion: %v", err)
	}
	oldSelectors := &execpb.SessionOptions{ResourcePlugin: "p", ResourceKey: "t"}
	if _, err := NewSessionOptionsFromProto(oldSelectors); resourcev2.ErrorCode(err) != "UNSUPPORTED" {
		t.Fatalf("unversioned resource intent accepted: %v", err)
	}
	unknown := new(execpb.SessionOptions)
	unknown.ProtoReflect().SetUnknown(protowire.AppendVarint(protowire.AppendTag(nil, 20, protowire.VarintType), 1))
	if plain, err := NewSessionOptionsFromProto(unknown); err != nil || plain.HasResourceIntent() {
		t.Fatalf("unrelated nonresource utility policy changed: %v %v", plain, err)
	}
}

func TestExecOptionsStrictJSONPreservesExactPayload(t *testing.T) {
	source := `{"resource_plugin":"p","resource_key":"type","resource_data":{"kind":"map","value":{"negativeZero":{"kind":"double","value":-0},"wide":{"kind":"uint64","value":"18446744073709551615"}}}}`
	var input SessionOptions
	if err := json.Unmarshal([]byte(source), &input); err != nil {
		t.Fatal(err)
	}
	if !math.Signbit(input.ResourceData.Map["negativeZero"].Double) ||
		input.ResourceData.Map["wide"].Uint != "18446744073709551615" {
		t.Fatal("JSON coerced canonical scalar intent")
	}
	for _, source := range []string{
		`{"resource_plugin":"p","resource_plugin":"p","resource_key":"t"}`,
		`{"resource_plugin":"p","resource_key":"t","resource_data":null}`,
		`{"resource_plugin":"p","resource_key":"t","reference":null}`,
		`{"resource_plugin":"p","resource_key":"t","route":null}`,
		`{"RESOURCE_PLUGIN":"p","resource_key":"t"}`,
		`{"resource_plugin":"p","resource_key":"t","resource_data":{"metadata":{"name":"legacy-native-map"}}}`,
		`{"id":"\ud800"}`,
		`{"unknown":"value"}`,
	} {
		var out SessionOptions
		if err := json.Unmarshal([]byte(source), &out); resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
			t.Fatalf("malformed exec JSON admitted: %s: %v", source, err)
		}
	}
}

func TestExecOptionsPreflightAggregateFrame(t *testing.T) {
	body := &resourcev2.Value{
		Kind: "map",
		Map:  map[string]resourcev2.Value{"payload": {Kind: "string", String: strings.Repeat("b", 15<<20)}},
	}
	opts := SessionOptions{
		ResourcePlugin: "p",
		ResourceKey:    "type",
		Params:         map[string]string{"large": strings.Repeat("m", 18<<20)},
		ResourceData:   body,
	}
	if _, err := opts.ToProto(); resourcev2.ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("aggregate frame cap lost: %v", err)
	}
}
