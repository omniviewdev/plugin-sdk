package resourcev2

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// checkUnicode prevents encoding/json from silently replacing malformed UTF-8
// and unpaired UTF-16 surrogate escapes with U+FFFD.
func checkUnicode(data []byte) error {
	if !utf8.Valid(data) {
		return invalid("invalid UTF-8 JSON")
	}
	for i := 0; i < len(data); i++ {
		if data[i] != '"' {
			continue
		}
		i++
		for i < len(data) && data[i] != '"' {
			if data[i] != '\\' {
				i++
				continue
			}
			i++
			if i >= len(data) {
				return invalid("truncated JSON escape")
			}
			if data[i] != 'u' {
				i++
				continue
			}
			if i+4 >= len(data) {
				return invalid("truncated Unicode escape")
			}
			u, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
			if err != nil {
				return invalid("invalid Unicode escape")
			}
			i += 5
			if u >= 0xd800 && u <= 0xdbff {
				if i+5 >= len(data) || data[i] != '\\' || data[i+1] != 'u' {
					return invalid("unpaired Unicode surrogate")
				}
				lo, e := strconv.ParseUint(string(data[i+2:i+6]), 16, 16)
				if e != nil || lo < 0xdc00 || lo > 0xdfff {
					return invalid("unpaired Unicode surrogate")
				}
				i += 6
			} else if u >= 0xdc00 && u <= 0xdfff {
				return invalid("unpaired Unicode surrogate")
			}
		}
	}
	return nil
}
func parseJSON(data []byte) (any, error) { return parseJSONWithLimit(data, MaxDocumentBytes) }
func parseJSONWithLimit(data []byte, byteLimit int) (any, error) {
	if len(data) > byteLimit {
		return nil, limited("JSON byte limit")
	}
	if err := checkUnicode(data); err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	nodes, fields := 0, 0
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		nodes++
		if depth > maxDomainDepth || nodes > maxDomainNodes {
			return nil, limited("JSON complexity limit")
		}
		t, err := d.Token()
		if err != nil {
			return nil, invalid("invalid JSON")
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return t, nil
		}
		switch delim {
		case '{':
			m := map[string]any{}
			for d.More() {
				fields++
				if fields > maxDomainFields {
					return nil, limited("JSON field limit")
				}
				key, e := d.Token()
				if e != nil {
					return nil, invalid("invalid JSON member")
				}
				k, ok := key.(string)
				if !ok {
					return nil, invalid("invalid JSON key")
				}
				if _, exists := m[k]; exists {
					return nil, invalid("duplicate JSON member")
				}
				v, e := read(depth + 1)
				if e != nil {
					return nil, e
				}
				m[k] = v
			}
			end, e := d.Token()
			if e != nil || end != json.Delim('}') {
				return nil, invalid("invalid JSON object")
			}
			return m, nil
		case '[':
			a := []any{}
			for d.More() {
				fields++
				if fields > maxDomainFields {
					return nil, limited("JSON field limit")
				}
				v, e := read(depth + 1)
				if e != nil {
					return nil, e
				}
				a = append(a, v)
			}
			end, e := d.Token()
			if e != nil || end != json.Delim(']') {
				return nil, invalid("invalid JSON array")
			}
			return a, nil
		default:
			return nil, invalid("unexpected JSON delimiter")
		}
	}
	v, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, invalid("trailing JSON")
	}
	return v, nil
}

// ValidateJSON applies the raw message ceiling and exact structural budgets.
// Tagged values, native resource JSON and schema documents independently use
// the smaller body ceiling before their semantic admission.
func ValidateJSON(data []byte) error { _, err := parseJSONWithLimit(data, MaxMessageBytes); return err }

func taggedValue(raw any, depth int, nodes *int) (Value, error) {
	*nodes++
	if depth > maxDomainDepth || *nodes > maxDomainNodes {
		return Value{}, limited("value complexity limit")
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return Value{}, invalid("tagged value must be object")
	}
	kind, ok := m["kind"].(string)
	if !ok {
		return Value{}, invalid("value kind must be string")
	}
	if kind == "null" {
		if len(m) != 1 {
			return Value{}, invalid("null value has payload or unknown fields")
		}
		return Value{Kind: "null"}, nil
	}
	if len(m) != 2 {
		return Value{}, invalid("tagged value requires kind and value only")
	}
	x, ok := m["value"]
	if !ok {
		return Value{}, invalid("tagged value lacks payload")
	}
	v := Value{Kind: kind}
	switch kind {
	case "bool":
		b, ok := x.(bool)
		if !ok {
			return Value{}, invalid("bool value payload")
		}
		v.Bool = b
	case "string", "int64", "uint64", "timestamp":
		s, ok := x.(string)
		if !ok {
			return Value{}, invalid("string value payload")
		}
		switch kind {
		case "string":
			v.String = s
		case "int64":
			v.Int = s
		case "uint64":
			v.Uint = s
		case "timestamp":
			t, e := timestamp(s)
			if e != nil {
				return Value{}, e
			}
			v.Timestamp = t.Format(time.RFC3339Nano)
		}
	case "double":
		n, ok := x.(json.Number)
		if !ok {
			return Value{}, invalid("double value payload")
		}
		f, e := strconv.ParseFloat(string(n), 64)
		if e != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return Value{}, invalid("invalid double")
		}
		v.Double = f
	case "list":
		a, ok := x.([]any)
		if !ok {
			return Value{}, invalid("list value payload")
		}
		v.List = make([]Value, 0, len(a))
		for _, raw := range a {
			e, err := taggedValue(raw, depth+1, nodes)
			if err != nil {
				return Value{}, err
			}
			v.List = append(v.List, e)
		}
	case "map":
		m, ok := x.(map[string]any)
		if !ok {
			return Value{}, invalid("map value payload")
		}
		v.Map = make(map[string]Value, len(m))
		for k, raw := range m {
			e, err := taggedValue(raw, depth+1, nodes)
			if err != nil {
				return Value{}, err
			}
			v.Map[k] = e
		}
	default:
		return Value{}, invalid("unknown value kind")
	}
	return v, nil
}

// UnmarshalJSON admits the strict tagged encoding without changing the receiver on failure.
// Duplicate members, malformed Unicode, and missing or inactive payloads are rejected.
func (v *Value) UnmarshalJSON(data []byte) error {
	if v == nil {
		return invalid("nil value receiver")
	}
	raw, err := parseJSON(data)
	if err != nil {
		return err
	}
	nodes := 0
	out, err := taggedValue(raw, 0, &nodes)
	if err != nil {
		return err
	}
	if err = ValidateValue(out); err != nil {
		return err
	}
	*v = out
	return nil
}

func appendJSONString(dst []byte, s string) []byte {
	const hex = "0123456789abcdef"
	dst = append(dst, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"', '\\':
			dst = append(dst, '\\', c)
		case '\b':
			dst = append(dst, '\\', 'b')
		case '\f':
			dst = append(dst, '\\', 'f')
		case '\n':
			dst = append(dst, '\\', 'n')
		case '\r':
			dst = append(dst, '\\', 'r')
		case '\t':
			dst = append(dst, '\\', 't')
		case '<', '>', '&':
			dst = append(dst, '\\', 'u', '0', '0', hex[c>>4], hex[c&15])
		default:
			if c < 0x20 {
				dst = append(dst, '\\', 'u', '0', '0', hex[c>>4], hex[c&15])
			} else if c == 0xe2 && i+2 < len(s) && s[i+1] == 0x80 && (s[i+2] == 0xa8 || s[i+2] == 0xa9) {
				dst = append(dst, '\\', 'u', '2', '0', '2', hex[s[i+2]&15])
				i += 2
			} else {
				dst = append(dst, c)
			}
		}
	}
	return append(dst, '"')
}

func appendTagged(dst []byte, v Value) ([]byte, error) {
	dst = append(dst, `{"kind":`...)
	dst = appendJSONString(dst, v.Kind)
	if v.Kind == "null" {
		return append(dst, '}'), nil
	}
	dst = append(dst, `,"value":`...)
	switch v.Kind {
	case "bool":
		dst = strconv.AppendBool(dst, v.Bool)
	case "string":
		dst = appendJSONString(dst, v.String)
	case "int64":
		dst = appendJSONString(dst, v.Int)
	case "uint64":
		dst = appendJSONString(dst, v.Uint)
	case "double":
		dst = strconv.AppendFloat(dst, v.Double, 'g', -1, 64)
	case "timestamp":
		t, _ := timestamp(v.Timestamp)
		dst = append(dst, '"')
		dst = t.AppendFormat(dst, time.RFC3339Nano)
		dst = append(dst, '"')
	case "list":
		dst = append(dst, '[')
		for i, x := range v.List {
			if i > 0 {
				dst = append(dst, ',')
			}
			var err error
			dst, err = appendTagged(dst, x)
			if err != nil {
				return nil, err
			}
		}
		dst = append(dst, ']')
	case "map":
		dst = append(dst, '{')
		keys := make([]string, 0, len(v.Map))
		for k := range v.Map {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i, k := range keys {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = appendJSONString(dst, k)
			dst = append(dst, ':')
			var err error
			dst, err = appendTagged(dst, v.Map[k])
			if err != nil {
				return nil, err
			}
		}
		dst = append(dst, '}')
	}
	dst = append(dst, '}')
	if len(dst) > maxDocumentBytes {
		return nil, limited("encoded value byte limit")
	}
	return dst, nil
}

func (v Value) MarshalJSON() ([]byte, error) {
	if err := ValidateValue(v); err != nil {
		return nil, err
	}
	budget := jsonValueBudget{}
	size, err := taggedJSONSize(v, 0, &budget)
	if err != nil {
		return nil, err
	}
	return appendTagged(make([]byte, 0, size), v)
}

// JSONToValue admits a native JSON resource document. Integral tokens retain
// exact signed/unsigned intent; only fractional/exponent tokens become doubles.
// Native JSON strings never acquire timestamp intent.
func JSONToValue(data []byte) (Value, error) {
	raw, err := parseJSON(data)
	if err != nil {
		return Value{}, err
	}
	v, err := ValueFromNative(raw)
	if err != nil {
		return Value{}, err
	}
	if v.Kind != "map" {
		return Value{}, invalid("resource body root must be map")
	}
	return v, nil
}

// ValueFromNative preserves native scalar types without a JSON float roundtrip.
// Unsupported native types fail rather than stringify or silently lose intent.
func ValueFromNative(raw any) (Value, error) {
	nodes, bytes := 0, 0
	var convert func(any, int) (Value, error)
	convert = func(raw any, depth int) (Value, error) {
		nodes++
		if depth > maxDomainDepth || nodes > maxDomainNodes {
			return Value{}, limited("native value complexity limit")
		}
		var v Value
		switch x := raw.(type) {
		case nil:
			v.Kind = "null"
		case bool:
			v = Value{Kind: "bool", Bool: x}
		case string:
			bytes += len(x)
			v = Value{Kind: "string", String: x}
		case int:
			v = Value{Kind: "int64", Int: strconv.FormatInt(int64(x), 10)}
		case int8:
			v = Value{Kind: "int64", Int: strconv.FormatInt(int64(x), 10)}
		case int16:
			v = Value{Kind: "int64", Int: strconv.FormatInt(int64(x), 10)}
		case int32:
			v = Value{Kind: "int64", Int: strconv.FormatInt(int64(x), 10)}
		case int64:
			v = Value{Kind: "int64", Int: strconv.FormatInt(x, 10)}
		case uint:
			v = Value{Kind: "uint64", Uint: strconv.FormatUint(uint64(x), 10)}
		case uint8:
			v = Value{Kind: "uint64", Uint: strconv.FormatUint(uint64(x), 10)}
		case uint16:
			v = Value{Kind: "uint64", Uint: strconv.FormatUint(uint64(x), 10)}
		case uint32:
			v = Value{Kind: "uint64", Uint: strconv.FormatUint(uint64(x), 10)}
		case uint64:
			v = Value{Kind: "uint64", Uint: strconv.FormatUint(x, 10)}
		case float32:
			v = Value{Kind: "double", Double: float64(x)}
		case float64:
			v = Value{Kind: "double", Double: x}
		case time.Time:
			v = TimestampValue(x)
		case json.Number:
			s := string(x)
			bytes += len(s)
			if bytes > maxDocumentBytes {
				return Value{}, limited("native number byte limit")
			}
			if !json.Valid([]byte(s)) {
				return Value{}, invalid("invalid native JSON number")
			}
			if strings.ContainsAny(s, ".eE") {
				f, e := strconv.ParseFloat(s, 64)
				if e != nil {
					return Value{}, invalid("native double out of range")
				}
				v = Value{Kind: "double", Double: f}
			} else {
				if n, e := strconv.ParseInt(s, 10, 64); e == nil {
					v = Value{Kind: "int64", Int: strconv.FormatInt(n, 10)}
				} else if n, e := strconv.ParseUint(s, 10, 64); e == nil {
					v = Value{Kind: "uint64", Uint: strconv.FormatUint(n, 10)}
				} else {
					return Value{}, invalid("native integer out of range")
				}
			}
		case []any:
			if len(x) > maxDomainNodes-nodes {
				return Value{}, limited("native list limit")
			}
			v = Value{Kind: "list", List: make([]Value, 0, len(x))}
			for _, raw := range x {
				e, err := convert(raw, depth+1)
				if err != nil {
					return Value{}, err
				}
				v.List = append(v.List, e)
			}
		case map[string]any:
			if len(x) > maxDomainNodes-nodes {
				return Value{}, limited("native map limit")
			}
			v = Value{Kind: "map", Map: make(map[string]Value, len(x))}
			for k, raw := range x {
				bytes += len(k)
				if bytes > maxDocumentBytes {
					return Value{}, limited("native value byte limit")
				}
				if err := text(k, false); err != nil {
					return Value{}, err
				}
				e, err := convert(raw, depth+1)
				if err != nil {
					return Value{}, err
				}
				v.Map[k] = e
			}
		default:
			return Value{}, NewError("UNSUPPORTED", "unsupported native value type")
		}
		if bytes > maxDocumentBytes {
			return Value{}, limited("native value byte limit")
		}
		return v, nil
	}
	v, err := convert(raw, 0)
	if err != nil {
		return Value{}, err
	}
	if err = ValidateValue(v); err != nil {
		return Value{}, err
	}
	return v, nil
}

// NativeFromValue returns exact native scalar types and independent containers.
// Consumers with a narrower native domain must reject unsupported scalar types;
// they must not silently convert uint64 or timestamps through JSON numbers.
func NativeFromValue(v Value) (any, error) {
	if err := ValidateValue(v); err != nil {
		return nil, err
	}
	var convert func(Value) any
	convert = func(v Value) any {
		switch v.Kind {
		case "null":
			return nil
		case "bool":
			return v.Bool
		case "string":
			return v.String
		case "int64":
			n, _ := strconv.ParseInt(v.Int, 10, 64)
			return n
		case "uint64":
			n, _ := strconv.ParseUint(v.Uint, 10, 64)
			return n
		case "double":
			return v.Double
		case "timestamp":
			t, _ := timestamp(v.Timestamp)
			return t
		case "list":
			out := make([]any, len(v.List))
			for i, x := range v.List {
				out[i] = convert(x)
			}
			return out
		case "map":
			out := make(map[string]any, len(v.Map))
			for k, x := range v.Map {
				out[k] = convert(x)
			}
			return out
		}
		panic("validated value has no native mapping")
	}
	return convert(v), nil
}

type jsonValueBudget struct{ nodes, fields int }

func quotedJSONSize(s string) int {
	size := 2
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"', '\\', '\b', '\f', '\n', '\r', '\t':
			size += 2
		case '<', '>', '&':
			size += 6
		default:
			if c < 0x20 {
				size += 6
			} else if c == 0xe2 && i+2 < len(s) && s[i+1] == 0x80 && (s[i+2] == 0xa8 || s[i+2] == 0xa9) {
				size += 6
				i += 2
			} else {
				size++
			}
		}
	}
	return size
}

func taggedJSONSize(v Value, depth int, budget *jsonValueBudget) (int, error) {
	budget.nodes += 2
	budget.fields++
	if v.Kind != "null" {
		budget.nodes++
		budget.fields++
	}
	if depth+1 > MaxValueDepth || budget.nodes > MaxValueNodes || budget.fields > MaxWireFields {
		return 0, limited("tagged JSON structural limit")
	}
	size := 9 + quotedJSONSize(v.Kind)
	if v.Kind == "null" {
		return size, nil
	}
	size += 9
	switch v.Kind {
	case "bool":
		if v.Bool {
			size += 4
		} else {
			size += 5
		}
	case "string":
		size += quotedJSONSize(v.String)
	case "int64":
		size += quotedJSONSize(v.Int)
	case "uint64":
		size += quotedJSONSize(v.Uint)
	case "double":
		var buffer [32]byte
		size += len(strconv.AppendFloat(buffer[:0], v.Double, 'g', -1, 64))
	case "timestamp":
		t, _ := timestamp(v.Timestamp)
		var buffer [40]byte
		size += 2 + len(t.AppendFormat(buffer[:0], time.RFC3339Nano))
	case "list":
		budget.fields += len(v.List)
		size += 2
		for i, x := range v.List {
			n, err := taggedJSONSize(x, depth+2, budget)
			if err != nil {
				return 0, err
			}
			size += n
			if i > 0 {
				size++
			}
			if size > MaxDocumentBytes {
				return 0, limited("encoded JSON byte limit")
			}
		}
	case "map":
		budget.fields += len(v.Map)
		size += 2
		i := 0
		for key, x := range v.Map {
			n, err := taggedJSONSize(x, depth+2, budget)
			if err != nil {
				return 0, err
			}
			size += quotedJSONSize(key) + 1 + n
			if i > 0 {
				size++
			}
			i++
			if size > MaxDocumentBytes {
				return 0, limited("encoded JSON byte limit")
			}
		}
	}
	if size > MaxDocumentBytes || budget.fields > MaxWireFields {
		return 0, limited("encoded JSON limit")
	}
	return size, nil
}
