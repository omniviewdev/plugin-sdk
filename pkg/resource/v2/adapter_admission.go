package resourcev2

import (
	"strconv"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
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
	budget := protoAdmissionBudget{}
	if err := budget.visit(p.ProtoReflect(), 0); err != nil {
		return err
	}
	if proto.Size(p) > MaxMessageBytes {
		return limited("protobuf message byte limit")
	}
	return nil
}

type protoAdmissionBudget struct {
	nodes, fields, bytes int
}

func (b *protoAdmissionBudget) visit(m protoreflect.Message, depth int) error {
	if !m.IsValid() {
		return invalid("missing protobuf message")
	}
	b.nodes++
	const wrapperDepth = maxDomainDepth*3 + 16
	const wrapperNodes = maxDomainNodes * 4
	if depth > wrapperDepth || b.nodes > wrapperNodes {
		return limited("protobuf complexity limit")
	}
	if len(m.GetUnknown()) != 0 {
		return NewError("UNSUPPORTED", "unknown resource protobuf field")
	}
	var err error
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		err = b.field(fd, v, depth)
		return err == nil
	})
	return err
}

func (b *protoAdmissionBudget) field(fd protoreflect.FieldDescriptor, v protoreflect.Value, depth int) error {
	if !fd.IsList() {
		b.fields++
		if b.fields > maxDomainFields {
			return limited("protobuf field limit")
		}
		return b.scalar(fd, v, depth)
	}
	list := v.List()
	b.fields += list.Len()
	if b.fields > maxDomainFields {
		return limited("protobuf field limit")
	}
	for i := range list.Len() {
		if err := b.scalar(fd, list.Get(i), depth); err != nil {
			return err
		}
	}
	return nil
}

func (b *protoAdmissionBudget) scalar(fd protoreflect.FieldDescriptor, v protoreflect.Value, depth int) error {
	kind := fd.Kind()
	if kind == protoreflect.MessageKind {
		return b.visit(v.Message(), depth+1)
	}
	if kind == protoreflect.StringKind {
		s := v.String()
		b.bytes += len(s)
		if !utf8.ValidString(s) {
			return invalid("invalid protobuf UTF-8")
		}
	}
	if kind == protoreflect.BytesKind {
		b.bytes += len(v.Bytes())
	}
	if b.bytes > MaxMessageBytes {
		return limited("protobuf byte limit")
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
		v.Kind = valueKindNull
	case *resourcepb.Value_BoolValue:
		if x == nil {
			d.fail("missing bool")
			break
		}
		v = Value{Kind: valueKindBool, Bool: x.BoolValue}
	case *resourcepb.Value_StringValue:
		if x == nil {
			d.fail("missing string")
			break
		}
		v = Value{Kind: valueKindString, String: x.StringValue}
	case *resourcepb.Value_Int64Value:
		if x == nil {
			d.fail("missing int64")
			break
		}
		v = Value{Kind: valueKindInt64, Int: strconv.FormatInt(x.Int64Value, 10)}
	case *resourcepb.Value_Uint64Value:
		if x == nil {
			d.fail("missing uint64")
			break
		}
		v = Value{Kind: valueKindUint64, Uint: strconv.FormatUint(x.Uint64Value, 10)}
	case *resourcepb.Value_DoubleValue:
		if x == nil {
			d.fail("missing double")
			break
		}
		v = Value{Kind: valueKindDouble, Double: x.DoubleValue}
	case *resourcepb.Value_TimestampValue:
		v = d.timestampValue(x)
	case *resourcepb.Value_ListValue:
		v = d.listValue(x, depth)
	case *resourcepb.Value_MapValue:
		v = d.mapValue(x, depth)
	default:
		d.fail("unset value selection")
	}
	return v
}

func (d *adapterDecoder) timestampValue(x *resourcepb.Value_TimestampValue) Value {
	if x == nil || x.TimestampValue == nil {
		d.fail("missing timestamp")
		return Value{}
	}
	t := x.TimestampValue
	if t.Seconds < -62135596800 || t.Seconds > 253402300799 || t.Nanos > 999999999 {
		d.fail("timestamp out of range")
		return Value{}
	}
	return TimestampValue(time.Unix(t.Seconds, int64(t.Nanos)))
}

func (d *adapterDecoder) listValue(x *resourcepb.Value_ListValue, depth int) Value {
	if x == nil || x.ListValue == nil {
		d.fail("missing list")
		return Value{}
	}
	values := x.ListValue.Values
	if len(values) > maxDomainNodes-d.nodes {
		d.err = limited("value node limit")
		return Value{}
	}
	v := Value{Kind: valueKindList, List: make([]Value, len(values))}
	for i, p := range values {
		v.List[i] = d.valueAt(p, depth+1)
		if d.err != nil {
			break
		}
	}
	return v
}

func (d *adapterDecoder) mapValue(x *resourcepb.Value_MapValue, depth int) Value {
	if x == nil || x.MapValue == nil {
		d.fail("missing map")
		return Value{}
	}
	entries := x.MapValue.Entries
	if len(entries) > maxDomainNodes-d.nodes {
		d.err = limited("value node limit")
		return Value{}
	}
	keys := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		if e == nil {
			d.fail("missing map entry")
			return Value{}
		}
		if _, ok := keys[e.Key]; ok {
			d.fail("duplicate map key")
			return Value{}
		}
		keys[e.Key] = struct{}{}
	}
	v := Value{Kind: valueKindMap, Map: make(map[string]Value, len(entries))}
	for _, e := range entries {
		v.Map[e.Key] = d.valueAt(e.Value, depth+1)
		if d.err != nil {
			break
		}
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
