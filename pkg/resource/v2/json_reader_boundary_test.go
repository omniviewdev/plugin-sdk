package resourcev2

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestJSONReaderAdmissionBoundaries(t *testing.T) {
	cases := []string{
		`{"nested":[{"x":0,"\u0078":1}]}`,
		`{"nested":["\ud800"]}`,
		`{"nested":["\udc00"]}`,
		`{"nested":["\ud800\u0041"]}`,
		"{\"nested\":[\"\xff\"]}",
		`{"nested":[]} {}`,
		`{"nested":[1,]}`,
		strings.Repeat("[", maxDomainDepth+1) + "0" + strings.Repeat("]", maxDomainDepth+1),
		"[" + strings.Repeat("0,", maxDomainFields) + "0]",
	}
	for _, source := range cases {
		t.Run(source[:min(len(source), 60)], func(t *testing.T) {
			if err := ValidateJSON([]byte(source)); err == nil {
				t.Fatal("validation accepted malformed or over-budget JSON")
			}
			if value, err := JSONToValue([]byte(source)); err == nil || !reflect.DeepEqual(value, Value{}) {
				t.Fatalf("native decode must return zero on failure: %#v, %v", value, err)
			}
		})
	}
	// Raw validation does not impose native numeric representability.
	if err := ValidateJSON([]byte(`{"n":18446744073709551616,"f":1e400}`)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateJSON([]byte(strings.Repeat(" ", MaxMessageBytes) + "{}")); err == nil {
		t.Fatal("accepted oversized raw message")
	}
}

func TestNativeJSONRawErrorsPrecedeNumericErrors(t *testing.T) {
	deep := strings.Repeat("[", maxDomainDepth+1) + "0" + strings.Repeat("]", maxDomainDepth+1)
	for _, source := range []string{
		`{"n":1e400,"deep":` + deep + `}`,
		`{"n":1e400,"n":0}`,
		`{"n":1e400,"\u006e":0}`,
		`{"n":1e400} {}`,
		`{"n":1e400,"items":[0,]}`,
	} {
		rawErr := ValidateJSON([]byte(source))
		value, err := JSONToValue([]byte(source))
		if rawErr == nil || err == nil || err.Error() != rawErr.Error() {
			t.Fatalf("raw admission must win: raw=%v, native=%v", rawErr, err)
		}
		if !reflect.DeepEqual(value, Value{}) {
			t.Fatalf("failed decode returned partial value: %#v", value)
		}
	}
	for _, source := range []string{
		`{"n":1e400,"next":18446744073709551616}`,
		`{"nested":[1e400],"next":18446744073709551616}`,
	} {
		_, want := nativeJSONNumber(json.Number("1e400"))
		_, err := JSONToValue([]byte(source))
		if err == nil || err.Error() != want.Error() {
			t.Fatalf("first numeric error must win: got=%v, want=%v", err, want)
		}
	}
}

func TestDirectNativeJSONScalarIntent(t *testing.T) {
	value, err := JSONToValue([]byte(`{"":[],"m":{},"i":-9223372036854775808,"u":18446744073709551615,"z":-0.0,"s":"2000-01-01T00:00:00Z","null":null,"pair":"\ud83d\ude00"}`))
	if err != nil {
		t.Fatal(err)
	}
	if value.Map["i"].Int != "-9223372036854775808" || value.Map["u"].Uint != "18446744073709551615" {
		t.Fatal("lost exact integer intent")
	}
	if !math.Signbit(value.Map["z"].Double) || value.Map["s"].Kind != valueKindString || value.Map["pair"].String != "😀" {
		t.Fatal("lost native scalar intent")
	}
	if value.Map[""].List == nil || value.Map["m"].Map == nil || value.Map["null"].Kind != valueKindNull {
		t.Fatal("lost empty container or null intent")
	}
	for _, source := range []string{`{"kind":"null"}`, `{"kind":"int64","value":"1"}`} {
		got, err := JSONToValue([]byte(source))
		if err != nil || got.Kind != valueKindMap {
			t.Fatalf("native input acquired tagged intent: %#v, %v", got, err)
		}
	}
}

func TestDomainRootAdmissionRemainsAtomic(t *testing.T) {
	token := "unchanged"
	original := ResourceReference{Incarnation: &token}
	for _, source := range []string{
		`{"address":null}`,
		`{"address":[]}`,
		`{"address":{},"incarnation":null}`,
		`{"address":{},"\u0061ddress":{}}`,
		`{"address":{"provider":"x","provider":"y"}}`,
		`{"address":{},"Address":{}}`,
		`{"incarnation":"x"}`,
		`[]`,
	} {
		got := original
		if err := json.Unmarshal([]byte(source), &got); err == nil {
			t.Fatalf("accepted invalid domain object: %s", source)
		}
		if !reflect.DeepEqual(got, original) {
			t.Fatalf("failed decode mutated receiver: %#v", got)
		}
	}
}
