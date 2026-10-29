package resourcev2

import (
	"strconv"
	"time"
	"unicode/utf8"

	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Protobuf-tree depth includes wrappers; semantic Value depth does not.
// JSON-tree admission remains separate in JSON codecs.
// admitProto protects direct callers of the handwritten adapters. It cannot
// recover duplicate singular fields lost by generated decoding: transports must
// run ValidateWire before decoding, not rely on this semantic guard alone.
func admitProto(p proto.Message) error {
	if p == nil || !p.ProtoReflect().IsValid() {
		return invalid("missing protobuf message")
	}
	nodes, fields, bytes := 0, 0, 0
	var visit func(protoreflect.Message, int) error
	visit = func(m protoreflect.Message, depth int) error {
		if !m.IsValid() {
			return invalid("missing protobuf message")
		}
		nodes++
		if depth > maxDomainDepth*3+16 || nodes > maxDomainNodes*4 {
			return limited("protobuf complexity limit")
		}
		if len(m.GetUnknown()) != 0 {
			return NewError("UNSUPPORTED", "unknown resource protobuf field")
		}
		var err error
		m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
			check := func(v protoreflect.Value) error {
				switch fd.Kind() {
				case protoreflect.MessageKind:
					return visit(v.Message(), depth+1)
				case protoreflect.StringKind:
					s := v.String()
					bytes += len(s)
					if !utf8.ValidString(s) {
						return invalid("invalid protobuf UTF-8")
					}
				case protoreflect.BytesKind:
					bytes += len(v.Bytes())
				}
				if bytes > 32<<20 {
					return limited("protobuf byte limit")
				}
				return nil
			}
			if fd.IsList() {
				list := v.List()
				fields += list.Len()
				if fields > maxDomainFields {
					err = limited("protobuf field limit")
					return false
				}
				for i := range list.Len() {
					if err = check(list.Get(i)); err != nil {
						return false
					}
				}
			} else {
				fields++
				if fields > maxDomainFields {
					err = limited("protobuf field limit")
					return false
				}
				err = check(v)
			}
			return err == nil
		})
		return err
	}
	if err := visit(p.ProtoReflect(), 0); err != nil {
		return err
	}
	if proto.Size(p) > MaxMessageBytes {
		return limited("protobuf message byte limit")
	}
	return nil
}

// adapterDecoder records the first failure. Admission bounds the complete graph
// before these explicit, allocating conversions run.
type adapterDecoder struct {
	err   error
	nodes int
}

func (d *adapterDecoder) fail(detail string) {
	if d.err == nil {
		d.err = invalid(detail)
	}
}
func (d *adapterDecoder) value(p *resourcepb.Value) Value { return d.valueAt(p, 0) }
func (d *adapterDecoder) valueAt(p *resourcepb.Value, depth int) Value {
	if d.err != nil {
		return Value{}
	}
	d.nodes++
	if depth > maxDomainDepth || d.nodes > maxDomainNodes {
		d.err = limited("value complexity limit")
		return Value{}
	}
	if p == nil {
		d.fail("missing value")
		return Value{}
	}
	var v Value
	switch x := p.Selection.(type) {
	case *resourcepb.Value_NullValue:
		if x == nil || x.NullValue == nil {
			d.fail("missing null marker")
			break
		}
		v.Kind = "null"
	case *resourcepb.Value_BoolValue:
		if x == nil {
			d.fail("missing bool")
			break
		}
		v = Value{Kind: "bool", Bool: x.BoolValue}
	case *resourcepb.Value_StringValue:
		if x == nil {
			d.fail("missing string")
			break
		}
		v = Value{Kind: "string", String: x.StringValue}
	case *resourcepb.Value_Int64Value:
		if x == nil {
			d.fail("missing int64")
			break
		}
		v = Value{Kind: "int64", Int: strconv.FormatInt(x.Int64Value, 10)}
	case *resourcepb.Value_Uint64Value:
		if x == nil {
			d.fail("missing uint64")
			break
		}
		v = Value{Kind: "uint64", Uint: strconv.FormatUint(x.Uint64Value, 10)}
	case *resourcepb.Value_DoubleValue:
		if x == nil {
			d.fail("missing double")
			break
		}
		v = Value{Kind: "double", Double: x.DoubleValue}
	case *resourcepb.Value_TimestampValue:
		if x == nil || x.TimestampValue == nil {
			d.fail("missing timestamp")
			break
		}
		t := x.TimestampValue
		if t.Seconds < -62135596800 || t.Seconds > 253402300799 || t.Nanos > 999999999 {
			d.fail("timestamp out of range")
			break
		}
		v = TimestampValue(time.Unix(t.Seconds, int64(t.Nanos)))
	case *resourcepb.Value_ListValue:
		if x == nil || x.ListValue == nil {
			d.fail("missing list")
			break
		}
		if len(x.ListValue.Values) > maxDomainNodes-d.nodes {
			d.err = limited("value node limit")
			break
		}
		v = Value{Kind: "list", List: make([]Value, len(x.ListValue.Values))}
		for i, p := range x.ListValue.Values {
			v.List[i] = d.valueAt(p, depth+1)
			if d.err != nil {
				break
			}
		}
	case *resourcepb.Value_MapValue:
		if x == nil || x.MapValue == nil {
			d.fail("missing map")
			break
		}
		entries := x.MapValue.Entries
		if len(entries) > maxDomainNodes-d.nodes {
			d.err = limited("value node limit")
			break
		}
		keys := make(map[string]struct{}, len(entries))
		for _, e := range entries {
			if e == nil {
				d.fail("missing map entry")
				break
			}
			if _, ok := keys[e.Key]; ok {
				d.fail("duplicate map key")
				break
			}
			keys[e.Key] = struct{}{}
		}
		if d.err != nil {
			break
		}
		v = Value{Kind: "map", Map: make(map[string]Value, len(entries))}
		for _, e := range entries {
			v.Map[e.Key] = d.valueAt(e.Value, depth+1)
			if d.err != nil {
				break
			}
		}
	default:
		d.fail("unset value selection")
	}
	return v
}

func toProto[D any, P proto.Message](v D, validate func(D) error, encode func(D) P) (P, error) {
	var zero P
	if err := admission(v); err != nil {
		return zero, err
	}
	if err := validate(v); err != nil {
		return zero, err
	}
	p := encode(v)
	if proto.Size(p) > MaxMessageBytes {
		return zero, limited("encoded message byte limit")
	}
	return p, nil
}
func fromProto[D any, P proto.Message](p P, decode func(*adapterDecoder, P) D, validate func(D) error) (D, error) {
	var zero D
	if err := admitProto(p); err != nil {
		return zero, err
	}
	d := &adapterDecoder{}
	v := decode(d, p)
	if d.err != nil {
		return zero, d.err
	}
	if err := validate(v); err != nil {
		return zero, err
	}
	return v, nil
}
