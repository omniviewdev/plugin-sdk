package resourcev2

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
)

func assertGeneratedValueSize(t *testing.T, v Value) {
	t.Helper()
	size, err := ValueWireSize(v)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ToProtoValue(v)
	if err != nil {
		t.Fatal(err)
	}
	if want := proto.Size(p); size != want {
		t.Fatalf("%s size = %d, generated size = %d", v.Kind, size, want)
	}
}

func TestValueSizingWrapperBoundaries(t *testing.T) {
	for _, n := range []int{0, 1, 119, 120, 121, 122, 123, 124, 125, 126, 127, 128, 129, 16375, 16376, 16377, 16378, 16379, 16380, 16381, 16382, 16383, 16384} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			v := Value{Kind: "string", String: strings.Repeat("x", n)}
			assertGeneratedValueSize(t, v)
			assertGeneratedValueSize(t, Value{Kind: "list", List: []Value{v}})
			assertGeneratedValueSize(t, Value{Kind: "map", Map: map[string]Value{"": v}})
			assertGeneratedValueSize(t, Value{Kind: "map", Map: map[string]Value{"雪": v}})
			assertGeneratedValueSize(t, Value{Kind: "map", Map: map[string]Value{strings.Repeat("k", n): {Kind: "null"}}})
		})
	}
	for _, n := range []uint64{0, 1, 63, 64, 127, 128, 8191, 8192, 16383, 16384, math.MaxInt64, math.MaxUint64} {
		assertGeneratedValueSize(t, Value{Kind: "uint64", Uint: strconv.FormatUint(n, 10)})
		if n <= math.MaxInt64 {
			assertGeneratedValueSize(t, Value{Kind: "int64", Int: strconv.FormatInt(int64(n), 10)})
			assertGeneratedValueSize(t, Value{Kind: "int64", Int: strconv.FormatInt(-int64(n), 10)})
		}
	}
}

func TestValueSizingGeneratedTrees(t *testing.T) {
	rng := rand.New(rand.NewPCG(711, 29))
	var generate func(int) Value
	generate = func(depth int) Value {
		kind := rng.IntN(9)
		if depth == 0 {
			kind = rng.IntN(7)
		}
		switch kind {
		case 0:
			return Value{Kind: "null"}
		case 1:
			return Value{Kind: "bool", Bool: rng.IntN(2) == 0}
		case 2:
			return Value{Kind: "string", String: strings.Repeat("雪\x00", rng.IntN(130))}
		case 3:
			return Value{Kind: "int64", Int: strconv.FormatInt(int64(rng.Uint64()), 10)}
		case 4:
			return Value{Kind: "uint64", Uint: strconv.FormatUint(rng.Uint64(), 10)}
		case 5:
			return Value{Kind: "double", Double: math.Copysign(float64(rng.IntN(2)), -1)}
		case 6:
			return Value{Kind: "timestamp", Timestamp: "1969-12-31T23:59:59.000000127Z"}
		case 7:
			v := Value{Kind: "list"}
			for n := rng.IntN(5); n > 0; n-- {
				v.List = append(v.List, generate(depth-1))
			}
			return v
		default:
			v := Value{Kind: "map", Map: make(map[string]Value)}
			for n := rng.IntN(5); n > 0; n-- {
				v.Map[fmt.Sprintf("雪%d", n)] = generate(depth - 1)
			}
			return v
		}
	}
	for range 300 {
		assertGeneratedValueSize(t, generate(5))
	}
}

func TestValueSizingValidationPrecedence(t *testing.T) {
	cases := []struct {
		name  string
		value Value
		want  string
	}{
		{"invalid utf8", Value{Kind: "null", String: "\xff", Bool: true}, "INVALID_REQUEST"},
		{"inactive payload", Value{Kind: "unknown", Bool: true}, "INVALID_REQUEST"},
		{"inactive negative zero", Value{Kind: "null", Double: math.Copysign(0, -1)}, "INVALID_REQUEST"},
		{"inactive empty list", Value{Kind: "null", List: []Value{}}, "INVALID_REQUEST"},
		{"inactive empty map", Value{Kind: "null", Map: map[string]Value{}}, "INVALID_REQUEST"},
		{"integer", Value{Kind: "int64", Int: "-0"}, "INVALID_REQUEST"},
		{"unsigned", Value{Kind: "uint64", Uint: "-1"}, "INVALID_REQUEST"},
		{"double", Value{Kind: "double", Double: math.Inf(1)}, "INVALID_REQUEST"},
		{"timestamp", Value{Kind: "timestamp", Timestamp: "2026-02-30T00:00:00Z"}, "INVALID_REQUEST"},
		{"unknown", Value{Kind: "unknown"}, "INVALID_REQUEST"},
		{"byte before utf8", Value{Kind: "string", String: strings.Repeat("x", maxDocumentBytes) + "\xff"}, "RESOURCE_LIMIT"},
		{"encoded limit", Value{Kind: "string", String: strings.Repeat("x", maxDocumentBytes)}, "RESOURCE_LIMIT"},
		{"semantic before encoded", Value{Kind: "list", List: []Value{{Kind: "string", String: strings.Repeat("x", maxDocumentBytes)}, {Kind: "unknown"}}}, "INVALID_REQUEST"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			validation := ValidateValue(tc.value)
			size, sizing := ValueWireSize(tc.value)
			if validation == nil || sizing == nil || size != 0 {
				t.Fatalf("validation=%v size=%d sizing=%v", validation, size, sizing)
			}
			if ErrorCode(validation) != tc.want || ErrorCode(sizing) != tc.want {
				t.Fatalf("validation=%v sizing=%v want=%s", validation, sizing, tc.want)
			}
		})
	}
}

func TestValueSizingComplexityLimits(t *testing.T) {
	v := Value{Kind: "null"}
	for range maxDomainDepth {
		v = Value{Kind: "list", List: []Value{v}}
	}
	assertGeneratedValueSize(t, v)
	v = Value{Kind: "list", List: []Value{v}}
	if _, err := ValueWireSize(v); ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("depth limit: %v", err)
	}
	values := make([]Value, maxDomainNodes)
	for i := range values {
		values[i].Kind = "null"
	}
	assertGeneratedValueSize(t, Value{Kind: "list", List: values[:maxDomainNodes-1]})
	values[0].Kind = "unknown"
	if _, err := ValueWireSize(Value{Kind: "list", List: values}); ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("node limit must precede child validation: %v", err)
	}
}

func TestRecordSizingGeneratedBodies(t *testing.T) {
	for _, n := range []int{0, 121, 127, 128, 16377, 16383, 16384} {
		r := codecRecord()
		r.Body = Value{Kind: "map", Map: map[string]Value{"payload": {Kind: "string", String: strings.Repeat("x", n)}}}
		size, err := RecordWireSize(r)
		if err != nil {
			t.Fatal(err)
		}
		p, err := ToProtoRecord(r)
		if err != nil {
			t.Fatal(err)
		}
		if size != proto.Size(p) {
			t.Fatalf("record body %d: size=%d generated=%d", n, size, proto.Size(p))
		}
	}
}

func TestValueSizingDocumentBoundaryAndMutation(t *testing.T) {
	// A string at this boundary has a one-byte tag and four-byte length.
	v := Value{Kind: "string", String: strings.Repeat("x", maxDocumentBytes-5)}
	size, err := ValueWireSize(v)
	if err != nil || size != maxDocumentBytes {
		t.Fatalf("exact document boundary: size=%d err=%v", size, err)
	}
	v.String += "x"
	if _, err := ValueWireSize(v); ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("encoded document overflow: %v", err)
	}

	children := map[string]Value{"x": {Kind: "null"}}
	body := Value{Kind: "map", Map: children}
	assertGeneratedValueSize(t, body)
	children["x"] = Value{Kind: "unknown"}
	if _, err := ValueWireSize(body); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("mutated child must be revalidated: %v", err)
	}
	children["x"] = Value{Kind: "string", String: "雪"}
	assertGeneratedValueSize(t, body)
}

func TestRecordSizingPreservesValidationOrder(t *testing.T) {
	r := codecRecord()
	r.Body = Value{Kind: "map", Map: map[string]Value{"x": {Kind: "string", String: strings.Repeat("x", maxDocumentBytes)}}}
	r.Representation.ID = ""
	validation := ValidateRecord(r)
	size, sizing := RecordWireSize(r)
	if validation == nil || sizing == nil || size != 0 {
		t.Fatalf("validation=%v size=%d sizing=%v", validation, size, sizing)
	}
	if ErrorCode(validation) != "RESOURCE_LIMIT" || ErrorCode(sizing) != "RESOURCE_LIMIT" {
		t.Fatalf("body error must precede representation: validation=%v sizing=%v", validation, sizing)
	}
	page := SourcePage{
		Records:     []Record{r},
		Count:       Count{Kind: "invalid"},
		Coverage:    Coverage{Kind: "complete"},
		Consistency: "unknown",
	}
	if err := ValidateSourcePage(page); ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("record error must precede page metadata: %v", err)
	}
}
