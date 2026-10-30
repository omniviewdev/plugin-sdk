package resourcev2

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	jsonQuoteBytes       = 2
	jsonEscapeBytes      = 6
	jsonShortEscapeBytes = 2
	jsonControlLimit     = 0x20
	jsonFloatBits        = 64
	taggedMemberCount    = 2
	taggedObjectBytes    = len(`{"kind":}`)
	taggedPayloadBytes   = len(`,"value":`)
	jsonContainerBytes   = 2
	taggedBaseNodes      = 2
	taggedChildDepth     = 2
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
		var err error
		i, err = checkJSONStringUnicode(data, i+1)
		if err != nil {
			return err
		}
	}
	return nil
}

func checkJSONStringUnicode(data []byte, i int) (int, error) {
	for i < len(data) && data[i] != '"' {
		if data[i] != '\\' {
			i++
			continue
		}
		i++
		if i >= len(data) {
			return 0, invalid("truncated JSON escape")
		}
		if data[i] != 'u' {
			i++
			continue
		}
		next, err := checkUnicodeEscape(data, i)
		if err != nil {
			return 0, err
		}
		i = next
	}
	return i, nil
}

func checkUnicodeEscape(data []byte, i int) (int, error) {
	const hexDigits = 4
	const hexBase = 16
	const codeUnitBits = 16
	if i+hexDigits >= len(data) {
		return 0, invalid("truncated Unicode escape")
	}
	u, err := strconv.ParseUint(string(data[i+1:i+1+hexDigits]), hexBase, codeUnitBits)
	if err != nil {
		return 0, invalid("invalid Unicode escape")
	}
	i += 1 + hexDigits
	if u >= 0xd800 && u <= 0xdbff {
		if i+jsonEscapeBytes-1 >= len(data) || data[i] != '\\' || data[i+1] != 'u' {
			return 0, invalid("unpaired Unicode surrogate")
		}
		lo, parseErr := strconv.ParseUint(string(data[i+2:i+jsonEscapeBytes]), hexBase, codeUnitBits)
		if parseErr != nil || lo < 0xdc00 || lo > 0xdfff {
			return 0, invalid("unpaired Unicode surrogate")
		}
		return i + jsonEscapeBytes, nil
	}
	if u >= 0xdc00 && u <= 0xdfff {
		return 0, invalid("unpaired Unicode surrogate")
	}
	return i, nil
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
	reader := jsonTreeReader{decoder: d}
	v, err := reader.read(0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); !errors.Is(err, io.EOF) {
		return nil, invalid("trailing JSON")
	}
	return v, nil
}

type jsonTreeReader struct {
	decoder       *json.Decoder
	nodes, fields int
}

func (r *jsonTreeReader) read(depth int) (any, error) {
	r.nodes++
	if depth > maxDomainDepth || r.nodes > maxDomainNodes {
		return nil, limited("JSON complexity limit")
	}
	t, err := r.decoder.Token()
	if err != nil {
		return nil, invalid("invalid JSON")
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return t, nil
	}
	switch delim {
	case '{':
		return r.object(depth)
	case '[':
		return r.array(depth)
	default:
		return nil, invalid("unexpected JSON delimiter")
	}
}

func (r *jsonTreeReader) object(depth int) (any, error) {
	m := map[string]any{}
	for r.decoder.More() {
		r.fields++
		if r.fields > maxDomainFields {
			return nil, limited("JSON field limit")
		}
		key, err := r.decoder.Token()
		if err != nil {
			return nil, invalid("invalid JSON member")
		}
		k, ok := key.(string)
		if !ok {
			return nil, invalid("invalid JSON key")
		}
		if _, exists := m[k]; exists {
			return nil, invalid("duplicate JSON member")
		}
		v, err := r.read(depth + 1)
		if err != nil {
			return nil, err
		}
		m[k] = v
	}
	end, err := r.decoder.Token()
	if err != nil || end != json.Delim('}') {
		return nil, invalid("invalid JSON object")
	}
	return m, nil
}

func (r *jsonTreeReader) array(depth int) (any, error) {
	a := []any{}
	for r.decoder.More() {
		r.fields++
		if r.fields > maxDomainFields {
			return nil, limited("JSON field limit")
		}
		v, err := r.read(depth + 1)
		if err != nil {
			return nil, err
		}
		a = append(a, v)
	}
	end, err := r.decoder.Token()
	if err != nil || end != json.Delim(']') {
		return nil, invalid("invalid JSON array")
	}
	return a, nil
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
	if kind == valueKindNull {
		if len(m) != 1 {
			return Value{}, invalid("null value has payload or unknown fields")
		}
		return Value{Kind: valueKindNull}, nil
	}
	if len(m) != taggedMemberCount {
		return Value{}, invalid("tagged value requires kind and value only")
	}
	x, ok := m["value"]
	if !ok {
		return Value{}, invalid("tagged value lacks payload")
	}
	v := Value{Kind: kind}
	switch kind {
	case valueKindBool:
		b, ok := x.(bool)
		if !ok {
			return Value{}, invalid("bool value payload")
		}
		v.Bool = b
	case valueKindString, valueKindInt64, valueKindUint64, valueKindTimestamp:
		return taggedStringValue(kind, x)
	case valueKindDouble:
		n, ok := x.(json.Number)
		if !ok {
			return Value{}, invalid("double value payload")
		}
		f, e := strconv.ParseFloat(string(n), jsonFloatBits)
		if e != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return Value{}, invalid("invalid double")
		}
		v.Double = f
	case valueKindList, valueKindMap:
		return taggedContainerValue(kind, x, depth, nodes)
	default:
		return Value{}, invalid("unknown value kind")
	}
	return v, nil
}

func taggedStringValue(kind string, raw any) (Value, error) {
	s, ok := raw.(string)
	if !ok {
		return Value{}, invalid("string value payload")
	}
	v := Value{Kind: kind}
	switch kind {
	case valueKindString:
		v.String = s
	case valueKindInt64:
		v.Int = s
	case valueKindUint64:
		v.Uint = s
	case valueKindTimestamp:
		t, err := timestamp(s)
		if err != nil {
			return Value{}, err
		}
		v.Timestamp = t.Format(time.RFC3339Nano)
	}
	return v, nil
}

func taggedContainerValue(kind string, raw any, depth int, nodes *int) (Value, error) {
	if kind == valueKindList {
		a, ok := raw.([]any)
		if !ok {
			return Value{}, invalid("list value payload")
		}
		v := Value{Kind: kind, List: make([]Value, 0, len(a))}
		for _, item := range a {
			e, err := taggedValue(item, depth+1, nodes)
			if err != nil {
				return Value{}, err
			}
			v.List = append(v.List, e)
		}
		return v, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return Value{}, invalid("map value payload")
	}
	v := Value{Kind: kind, Map: make(map[string]Value, len(m))}
	for k, item := range m {
		e, err := taggedValue(item, depth+1, nodes)
		if err != nil {
			return Value{}, err
		}
		v.Map[k] = e
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
			switch {
			case c < jsonControlLimit:
				dst = append(dst, '\\', 'u', '0', '0', hex[c>>4], hex[c&15])
			case c == 0xe2 && i+2 < len(s) && s[i+1] == 0x80 && (s[i+2] == 0xa8 || s[i+2] == 0xa9):
				dst = append(dst, '\\', 'u', '2', '0', '2', hex[s[i+2]&15])
				i += 2
			default:
				dst = append(dst, c)
			}
		}
	}
	return append(dst, '"')
}

func appendTagged(dst []byte, v Value) ([]byte, error) {
	dst = append(dst, `{"kind":`...)
	dst = appendJSONString(dst, v.Kind)
	if v.Kind == valueKindNull {
		return append(dst, '}'), nil
	}
	dst = append(dst, `,"value":`...)
	var err error
	switch v.Kind {
	case valueKindBool:
		dst = strconv.AppendBool(dst, v.Bool)
	case valueKindString:
		dst = appendJSONString(dst, v.String)
	case valueKindInt64:
		dst = appendJSONString(dst, v.Int)
	case valueKindUint64:
		dst = appendJSONString(dst, v.Uint)
	case valueKindDouble:
		dst = strconv.AppendFloat(dst, v.Double, 'g', -1, jsonFloatBits)
	case valueKindTimestamp:
		t, _ := timestamp(v.Timestamp)
		dst = append(dst, '"')
		dst = t.AppendFormat(dst, time.RFC3339Nano)
		dst = append(dst, '"')
	case valueKindList:
		dst, err = appendTaggedList(dst, v.List)
	case valueKindMap:
		dst, err = appendTaggedMap(dst, v.Map)
	}
	if err != nil {
		return nil, err
	}
	return appendTaggedEnd(dst)
}

func appendTaggedEnd(dst []byte) ([]byte, error) {
	dst = append(dst, '}')
	if len(dst) > maxDocumentBytes {
		return nil, limited("encoded value byte limit")
	}
	return dst, nil
}

func appendTaggedList(dst []byte, values []Value) ([]byte, error) {
	dst = append(dst, '[')
	for i := range values {
		if i > 0 {
			dst = append(dst, ',')
		}
		var err error
		dst, err = appendTagged(dst, values[i])
		if err != nil {
			return nil, err
		}
	}
	return append(dst, ']'), nil
}

func appendTaggedMap(dst []byte, values map[string]Value) ([]byte, error) {
	dst = append(dst, '{')
	keys := make([]string, 0, len(values))
	for k := range values {
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
		dst, err = appendTagged(dst, values[k])
		if err != nil {
			return nil, err
		}
	}
	return append(dst, '}'), nil
}

// MarshalJSON emits deterministic tagged JSON, preserving integers, timestamps, and negative zero.
// Both semantic value budgets and raw tagged-JSON budgets apply before allocation.
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
	if v.Kind != valueKindMap {
		return Value{}, invalid("resource body root must be map")
	}
	return v, nil
}

// ValueFromNative preserves native scalar types without a JSON float roundtrip.
// Unsupported native types fail rather than stringify or silently lose intent.
func ValueFromNative(raw any) (Value, error) {
	converter := nativeValueConverter{}
	v, err := converter.convert(raw, 0)
	if err != nil {
		return Value{}, err
	}
	if err = ValidateValue(v); err != nil {
		return Value{}, err
	}
	return v, nil
}

type nativeValueConverter struct{ nodes, bytes int }

func (c *nativeValueConverter) convert(raw any, depth int) (Value, error) {
	c.nodes++
	if depth > maxDomainDepth || c.nodes > maxDomainNodes {
		return Value{}, limited("native value complexity limit")
	}
	var v Value
	var err error
	switch x := raw.(type) {
	case string:
		c.bytes += len(x)
		v = Value{Kind: valueKindString, String: x}
	case json.Number:
		c.bytes += len(x)
		if c.bytes > maxDocumentBytes {
			return Value{}, limited("native number byte limit")
		}
		v, err = nativeJSONNumber(x)
	case []any:
		v, err = c.list(x, depth)
	case map[string]any:
		v, err = c.mapEntries(x, depth)
	default:
		v, err = nativeScalarValue(raw)
	}
	if err != nil {
		return Value{}, err
	}
	if c.bytes > maxDocumentBytes {
		return Value{}, limited("native value byte limit")
	}
	return v, nil
}

func nativeScalarValue(raw any) (Value, error) {
	switch x := raw.(type) {
	case nil:
		return Value{Kind: valueKindNull}, nil
	case bool:
		return Value{Kind: valueKindBool, Bool: x}, nil
	case int:
		return Value{Kind: valueKindInt64, Int: strconv.FormatInt(int64(x), 10)}, nil
	case int8:
		return Value{Kind: valueKindInt64, Int: strconv.FormatInt(int64(x), 10)}, nil
	case int16:
		return Value{Kind: valueKindInt64, Int: strconv.FormatInt(int64(x), 10)}, nil
	case int32:
		return Value{Kind: valueKindInt64, Int: strconv.FormatInt(int64(x), 10)}, nil
	case int64:
		return Value{Kind: valueKindInt64, Int: strconv.FormatInt(x, 10)}, nil
	case uint:
		return Value{Kind: valueKindUint64, Uint: strconv.FormatUint(uint64(x), 10)}, nil
	case uint8:
		return Value{Kind: valueKindUint64, Uint: strconv.FormatUint(uint64(x), 10)}, nil
	case uint16:
		return Value{Kind: valueKindUint64, Uint: strconv.FormatUint(uint64(x), 10)}, nil
	case uint32:
		return Value{Kind: valueKindUint64, Uint: strconv.FormatUint(uint64(x), 10)}, nil
	case uint64:
		return Value{Kind: valueKindUint64, Uint: strconv.FormatUint(x, 10)}, nil
	case float32:
		return Value{Kind: valueKindDouble, Double: float64(x)}, nil
	case float64:
		return Value{Kind: valueKindDouble, Double: x}, nil
	case time.Time:
		return TimestampValue(x), nil
	default:
		return Value{}, NewError("UNSUPPORTED", "unsupported native value type")
	}
}

func nativeJSONNumber(number json.Number) (Value, error) {
	s := string(number)
	if !json.Valid([]byte(s)) {
		return Value{}, invalid("invalid native JSON number")
	}
	if strings.ContainsAny(s, ".eE") {
		f, err := strconv.ParseFloat(s, jsonFloatBits)
		if err != nil {
			return Value{}, invalid("native double out of range")
		}
		return Value{Kind: valueKindDouble, Double: f}, nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return Value{Kind: valueKindInt64, Int: strconv.FormatInt(n, 10)}, nil
	}
	if n, err := strconv.ParseUint(s, 10, 64); err == nil {
		return Value{Kind: valueKindUint64, Uint: strconv.FormatUint(n, 10)}, nil
	}
	return Value{}, invalid("native integer out of range")
}

func (c *nativeValueConverter) list(values []any, depth int) (Value, error) {
	if len(values) > maxDomainNodes-c.nodes {
		return Value{}, limited("native list limit")
	}
	v := Value{Kind: valueKindList, List: make([]Value, 0, len(values))}
	for _, raw := range values {
		e, err := c.convert(raw, depth+1)
		if err != nil {
			return Value{}, err
		}
		v.List = append(v.List, e)
	}
	return v, nil
}

func (c *nativeValueConverter) mapEntries(values map[string]any, depth int) (Value, error) {
	if len(values) > maxDomainNodes-c.nodes {
		return Value{}, limited("native map limit")
	}
	v := Value{Kind: valueKindMap, Map: make(map[string]Value, len(values))}
	for k, raw := range values {
		c.bytes += len(k)
		if c.bytes > maxDocumentBytes {
			return Value{}, limited("native value byte limit")
		}
		if err := text(k, false); err != nil {
			return Value{}, err
		}
		e, err := c.convert(raw, depth+1)
		if err != nil {
			return Value{}, err
		}
		v.Map[k] = e
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
		case valueKindNull:
			return nil
		case valueKindBool:
			return v.Bool
		case valueKindString:
			return v.String
		case valueKindInt64:
			n, _ := strconv.ParseInt(v.Int, 10, 64)
			return n
		case valueKindUint64:
			n, _ := strconv.ParseUint(v.Uint, 10, 64)
			return n
		case valueKindDouble:
			return v.Double
		case valueKindTimestamp:
			t, _ := timestamp(v.Timestamp)
			return t
		case valueKindList:
			out := make([]any, len(v.List))
			for i := range v.List {
				out[i] = convert(v.List[i])
			}
			return out
		case valueKindMap:
			out := make(map[string]any, len(v.Map))
			for k := range v.Map {
				out[k] = convert(v.Map[k])
			}
			return out
		}
		panic("validated value has no native mapping")
	}
	return convert(v), nil
}

type jsonValueBudget struct{ nodes, fields int }

func quotedJSONSize(s string) int {
	size := jsonQuoteBytes
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"', '\\', '\b', '\f', '\n', '\r', '\t':
			size += jsonShortEscapeBytes
		case '<', '>', '&':
			size += jsonEscapeBytes
		default:
			switch {
			case c < jsonControlLimit:
				size += jsonEscapeBytes
			case c == 0xe2 && i+2 < len(s) && s[i+1] == 0x80 && (s[i+2] == 0xa8 || s[i+2] == 0xa9):
				size += jsonEscapeBytes
				i += 2
			default:
				size++
			}
		}
	}
	return size
}

func taggedJSONSize(v Value, depth int, budget *jsonValueBudget) (int, error) {
	budget.nodes += taggedBaseNodes
	budget.fields++
	if v.Kind != valueKindNull {
		budget.nodes++
		budget.fields++
	}
	if depth+1 > MaxValueDepth || budget.nodes > MaxValueNodes || budget.fields > MaxWireFields {
		return 0, limited("tagged JSON structural limit")
	}
	size := taggedObjectBytes + quotedJSONSize(v.Kind)
	if v.Kind == valueKindNull {
		return size, nil
	}
	size += taggedPayloadBytes
	switch v.Kind {
	case valueKindBool:
		if v.Bool {
			size += len("true")
		} else {
			size += len("false")
		}
	case valueKindString:
		size += quotedJSONSize(v.String)
	case valueKindInt64:
		size += quotedJSONSize(v.Int)
	case valueKindUint64:
		size += quotedJSONSize(v.Uint)
	case valueKindDouble:
		var buffer [32]byte
		size += len(strconv.AppendFloat(buffer[:0], v.Double, 'g', -1, jsonFloatBits))
	case valueKindTimestamp:
		t, _ := timestamp(v.Timestamp)
		var buffer [40]byte
		size += jsonQuoteBytes + len(t.AppendFormat(buffer[:0], time.RFC3339Nano))
	case valueKindList:
		n, err := taggedListJSONSize(v.List, depth, size, budget)
		if err != nil {
			return 0, err
		}
		size = n
	case valueKindMap:
		n, err := taggedMapJSONSize(v.Map, depth, size, budget)
		if err != nil {
			return 0, err
		}
		size = n
	}
	if size > MaxDocumentBytes || budget.fields > MaxWireFields {
		return 0, limited("encoded JSON limit")
	}
	return size, nil
}

func taggedListJSONSize(values []Value, depth, size int, budget *jsonValueBudget) (int, error) {
	budget.fields += len(values)
	size += jsonContainerBytes
	for i := range values {
		n, err := taggedJSONSize(values[i], depth+taggedChildDepth, budget)
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
	return size, nil
}

func taggedMapJSONSize(values map[string]Value, depth, size int, budget *jsonValueBudget) (int, error) {
	budget.fields += len(values)
	size += jsonContainerBytes
	i := 0
	for key := range values {
		n, err := taggedJSONSize(values[key], depth+taggedChildDepth, budget)
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
	return size, nil
}
