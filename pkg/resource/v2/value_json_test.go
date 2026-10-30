package resourcev2

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTaggedJSONExactScalars(t *testing.T) {
	cases := []struct {
		source string
		want   Value
	}{
		{`{"kind":"null"}`, Value{Kind: "null"}},
		{`{"kind":"bool","value":false}`, Value{Kind: "bool"}},
		{`{"kind":"string","value":"雪😀"}`, Value{Kind: "string", String: "雪😀"}},
		{`{"kind":"int64","value":"-9223372036854775808"}`, Value{Kind: "int64", Int: "-9223372036854775808"}},
		{`{"kind":"uint64","value":"18446744073709551615"}`, Value{Kind: "uint64", Uint: "18446744073709551615"}},
		{`{"kind":"double","value":-0}`, Value{Kind: "double", Double: math.Copysign(0, -1)}},
		{
			`{"kind":"timestamp","value":"1969-12-31T23:59:59.999999999Z"}`,
			Value{Kind: "timestamp", Timestamp: "1969-12-31T23:59:59.999999999Z"},
		},
		{
			`{"kind":"timestamp","value":"2000-01-01T01:00:00.123456789+01:00"}`,
			Value{Kind: "timestamp", Timestamp: "2000-01-01T00:00:00.123456789Z"},
		},
		{`{"kind":"list","value":[]}`, Value{Kind: "list", List: []Value{}}},
		{`{"kind":"map","value":{}}`, Value{Kind: "map", Map: map[string]Value{}}},
	}
	for _, tc := range cases {
		t.Run(tc.source, func(t *testing.T) {
			var got Value
			if err := json.Unmarshal([]byte(tc.source), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) || math.Signbit(got.Double) != math.Signbit(tc.want.Double) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var roundtrip Value
			if err := json.Unmarshal(encoded, &roundtrip); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, roundtrip) || math.Signbit(got.Double) != math.Signbit(roundtrip.Double) {
				t.Fatalf("lost scalar intent: %s", encoded)
			}
		})
	}
}

func TestTaggedJSONRejectsAmbiguity(t *testing.T) {
	cases := []string{
		`{"kind":"bool","kind":"bool","value":true}`,
		`{"kind":"bool","value":true,"extra":false}`,
		`{"kind":"bool","value":true,"value":true}`,
		`{"kind":"map","value":{"x":{"kind":"null"},"\u0078":{"kind":"null"}}}`,
		`{"kind":"list","value":[{"kind":"null","extra":0}]}`,
		`{"kind":"map","value":{"x":{"kind":"bad","value":null}}}`,
		`{"kind":"string","value":"\ud800"}`,
		`{"kind":"string","value":"\udc00"}`,
		`{"kind":"string","value":"\ud800\u0041"}`,
		`{"kind":"int64","value":"01"}`,
		`{"kind":"int64","value":"-0"}`,
		`{"kind":"int64","value":"+1"}`,
		`{"kind":"int64","value":"1e3"}`,
		`{"kind":"int64","value":1}`,
		`{"kind":"int64","value":"9223372036854775808"}`,
		`{"kind":"uint64","value":"18446744073709551616"}`,
		`{"kind":"uint64","value":"-1"}`,
		`{"kind":"double","value":1e400}`,
		`{"kind":"double","value":"NaN"}`,
		`{"kind":"timestamp","value":"2000-01-01T00:00:00.0000000001Z"}`,
		`{"kind":"timestamp","value":"2000-01-01T00:00:00,1Z"}`,
		`{"kind":"timestamp","value":"2000-01-01T00:00:00+24:00"}`,
		`{"kind":"timestamp","value":"2000-01-01T00:00:00+00:60"}`,
		`{"kind":"timestamp","value":"0001-01-01T00:00:00+01:00"}`,
		`{"kind":"timestamp","value":"9999-12-31T23:59:59-01:00"}`,
		`{"kind":"timestamp","value":"2000-01-01T00:00:60Z"}`,
		`{"kind":"list","value":null}`,
		`{"kind":"map","value":null}`,
		`{"kind":"null","value":null}`,
		`{"kind":"null","value":false}`,
		`{"kind":"null"} {}`,
	}
	for _, source := range cases {
		t.Run(source, func(t *testing.T) {
			original := Value{Kind: "string", String: "unchanged"}
			got := original
			err := got.UnmarshalJSON([]byte(source))
			if ErrorCode(err) != "INVALID_REQUEST" {
				t.Fatalf("class %s: %v", ErrorCode(err), err)
			}
			if !reflect.DeepEqual(got, original) {
				t.Fatal("failed admission mutated destination")
			}
		})
	}
	source := append([]byte(`{"kind":"string","value":"`), 255)
	source = append(source, []byte(`"}`)...)
	var v Value
	if err := json.Unmarshal(source, &v); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
}

func TestTaggedJSONNestedDeterministicOrder(t *testing.T) {
	v := Value{Kind: "map", Map: map[string]Value{
		"z": {Kind: "list", List: []Value{{Kind: "null"}, {Kind: "uint64", Uint: "18446744073709551615"}}},
		"a": {Kind: "map", Map: map[string]Value{"b": {Kind: "bool", Bool: true}}},
	}}
	encoded, err := v.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"kind":"map","value":{"a":{"kind":"map","value":{"b":{"kind":"bool","value":true}}},"z":{"kind":"list","value":[{"kind":"null"},{"kind":"uint64","value":"18446744073709551615"}]}}}`
	if string(encoded) != want {
		t.Fatalf("got %s", encoded)
	}
}

func TestNativeJSONNumericPolicy(t *testing.T) {
	v, err := JSONToValue(
		[]byte(
			`{"signed":9007199254740993,"min":-9223372036854775808,"unsigned":18446744073709551615,"fraction":1.25,"exponent":1e0,"timestamp":"2000-01-01T00:00:00Z"}`,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	if v.Map["signed"].Int != "9007199254740993" || v.Map["min"].Int != "-9223372036854775808" ||
		v.Map["unsigned"].Uint != "18446744073709551615" {
		t.Fatalf("integer token rounded: %#v", v.Map)
	}
	if v.Map["fraction"].Kind != "double" || v.Map["exponent"].Kind != "double" || v.Map["timestamp"].Kind != "string" {
		t.Fatal("native numeric/string intent changed")
	}
	for _, source := range []string{`[]`, `null`, `{"x":18446744073709551616}`, `{"x":-9223372036854775809}`, `{"x":1e400}`, `{"x":0,"x":0}`, `{"x":"\ud800"}`} {
		if _, err := JSONToValue([]byte(source)); ErrorCode(err) != "INVALID_REQUEST" {
			t.Fatalf("accepted %s: %v", source, err)
		}
	}
}

func TestNativeReciprocalKeepsTypes(t *testing.T) {
	instant := time.Unix(-1, 999999999).UTC()
	input := map[string]any{
		"i":    int64(math.MinInt64),
		"u":    uint64(math.MaxUint64),
		"d":    math.Copysign(0, -1),
		"t":    instant,
		"list": []any{nil, true, "x"},
	}
	v, err := ValueFromNative(input)
	if err != nil {
		t.Fatal(err)
	}
	got, err := NativeFromValue(v)
	if err != nil {
		t.Fatal(err)
	}
	gotMap, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("native map changed type: %T", got)
	}
	gotDouble, ok := gotMap["d"].(float64)
	if !ok {
		t.Fatalf("native double changed type: %T", gotMap["d"])
	}
	if !reflect.DeepEqual(input, got) || !math.Signbit(gotDouble) {
		t.Fatalf("native types changed: %#v", got)
	}
	gotList, ok := gotMap["list"].([]any)
	if !ok {
		t.Fatalf("native list changed type: %T", gotMap["list"])
	}
	gotList[2] = "changed"
	if v.Map["list"].List[2].String != "x" {
		t.Fatal("native output shares mutable containers")
	}
	if _, conversionErr := ValueFromNative(map[any]any{1: "x"}); ErrorCode(conversionErr) != "UNSUPPORTED" {
		t.Fatalf("unsupported native type: %v", conversionErr)
	}
}

func TestJSONAndValueBounds(t *testing.T) {
	if err := ValidateJSON([]byte(strings.Repeat(" ", MaxMessageBytes+1))); ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("JSON frame byte limit: %v", err)
	}
	v := Value{Kind: "null"}
	for range MaxValueDepth + 1 {
		v = Value{Kind: "list", List: []Value{v}}
	}
	if err := ValidateValue(v); ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("depth limit: %v", err)
	}
	v = Value{Kind: "string", String: strings.Repeat("x", MaxDocumentBytes)}
	if err := ValidateValue(v); ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("encoded body byte limit: %v", err)
	}
	for _, v := range []Value{{Kind: "double", Double: math.Inf(1)}, {Kind: "double", Double: math.NaN()}, {Kind: "null", String: "inactive"}, {Kind: "null", Double: math.Copysign(0, -1)}, {Kind: "string", String: string([]byte{255})}} {
		if err := ValidateValue(v); ErrorCode(err) != "INVALID_REQUEST" {
			t.Fatalf("invalid value admitted: %#v %v", v, err)
		}
	}
}

func TestTaggedJSONRawStructuralBounds(t *testing.T) {
	v := Value{Kind: "null"}
	for range 20 {
		v = Value{Kind: "list", List: []Value{v}}
	}
	encoded, err := v.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var got Value
	if decodeErr := got.UnmarshalJSON(encoded); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	for range 12 {
		v = Value{Kind: "list", List: []Value{v}}
	}
	if _, marshalErr := v.MarshalJSON(); ErrorCode(marshalErr) != "RESOURCE_LIMIT" {
		t.Fatalf("raw wrapper depth not bounded before output: %v", marshalErr)
	}
	raw := strings.Repeat(`{"kind":"list","value":[`, 32) + `{"kind":"null"}` + strings.Repeat(`]}`, 32)
	if decodeErr := got.UnmarshalJSON([]byte(raw)); ErrorCode(decodeErr) != "RESOURCE_LIMIT" {
		t.Fatalf("raw input depth: %v", decodeErr)
	}
	// Every null element contributes an object and its kind scalar; the list
	// wrapper contributes three raw nodes. No scaled node allowance is permitted.
	leaves := (MaxValueNodes - 3) / 2
	v = Value{Kind: "list", List: make([]Value, leaves)}
	for i := range v.List {
		v.List[i] = Value{Kind: "null"}
	}
	encoded, err = v.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if decodeErr := got.UnmarshalJSON(encoded); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	v.List = append(v.List, Value{Kind: "null"})
	if _, marshalErr := v.MarshalJSON(); ErrorCode(marshalErr) != "RESOURCE_LIMIT" {
		t.Fatalf("raw output nodes: %v", marshalErr)
	}
}

func TestTaggedAndNativeBodyByteLimitsStaySeparate(t *testing.T) {
	source := []byte(strings.Repeat(" ", MaxDocumentBytes+1))
	var value Value
	if err := value.UnmarshalJSON(source); ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("tagged body byte limit: %v", err)
	}
	if _, err := JSONToValue(source); ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("native body byte limit: %v", err)
	}
}

func TestExactEncodedBodyBoundary(t *testing.T) {
	text := strings.Repeat("x", MaxDocumentBytes-4)
	valid := Value{Kind: "string", String: text[:len(text)-1]}
	size, err := ValueWireSize(valid)
	if err != nil {
		t.Fatal(err)
	}
	if size != MaxDocumentBytes {
		t.Fatalf("boundary size %d", size)
	}
	validationErr := ValidateValue(Value{Kind: "string", String: text})
	if ErrorCode(validationErr) != "RESOURCE_LIMIT" {
		t.Fatalf("over-boundary protobuf value: %v", validationErr)
	}
	jsonValue := Value{Kind: "string", String: text[:MaxDocumentBytes-28]}
	data, err := jsonValue.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != MaxDocumentBytes {
		t.Fatalf("tagged JSON boundary size %d", len(data))
	}
}
