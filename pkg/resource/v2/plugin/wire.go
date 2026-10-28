package plugin

import (
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
	"math"
	"unicode/utf8"
)

// WirePolicy is an implementation-candidate transport ceiling, not predicate policy or an SLO.
const WirePolicy = resourcev2.ResourceWirePolicy
const (
	MaxWireBytes = resourcev2.MaxMessageBytes
	MaxBodyBytes = resourcev2.MaxDocumentBytes
	MaxDepth     = resourcev2.MaxValueDepth
	MaxNodes     = resourcev2.MaxValueNodes
	MaxFields    = resourcev2.MaxWireFields
	MaxPageSize  = resourcev2.MaxPageRecords
)

func invalid(s string) error     { return resourcev2.NewError("INVALID_REQUEST", s) }
func limited(s string) error     { return resourcev2.NewError("RESOURCE_LIMIT", s) }
func unsupported(s string) error { return resourcev2.NewError("UNSUPPORTED", s) }

// Nodes count raw protobuf message occurrences across the entire frame.
type wireBudget struct{ fields, nodes int }

// ValidateWire admits only the exact negotiated resource descriptor tree, before decoding.
func ValidateWire(data []byte, descriptor protoreflect.MessageDescriptor) error {
	if len(data) > MaxWireBytes {
		return limited("resource message exceeds byte limit")
	}
	if !strictRoot(descriptor) {
		return unsupported("not a resource v2 message")
	}
	if networkSessionResponse(descriptor) {
		resource, err := networkResourcePresent(data, descriptor)
		if err != nil || !resource {
			return err
		}
	}
	b := wireBudget{}
	return b.message(data, descriptor, 0)
}
func (b *wireBudget) message(data []byte, d protoreflect.MessageDescriptor, depth int) error {
	if depth > MaxDepth {
		return limited("resource nesting exceeds limit")
	}
	b.nodes++
	if b.nodes > MaxNodes {
		return limited("resource nodes exceed limit")
	}
	var seen, selections uint64
	var version uint64
	versionField := utilityVersionField(d)
	var durationSeconds, durationNanos int64
	var keys map[wireKey]struct{}
	records := 0
	relationshipTargets := 0
	for len(data) > 0 {
		b.fields++
		if b.fields > MaxFields {
			return limited("resource fields exceed limit")
		}
		number, wire, n := protowire.ConsumeTag(data)
		if n < 0 || number <= 0 || number > protowire.MaxValidNumber {
			return invalid("malformed protobuf tag")
		}
		if n != protowire.SizeVarint(uint64(number)<<3|uint64(wire)) {
			return invalid("noncanonical protobuf tag")
		}
		if wire == protowire.StartGroupType || wire == protowire.EndGroupType || wire > protowire.Fixed32Type {
			return invalid("invalid protobuf wire type")
		}
		data = data[n:]
		f := d.Fields().ByNumber(number)
		if f == nil {
			return unsupported("unknown resource protobuf field")
		}
		if f.Index() >= 64 {
			return unsupported("resource descriptor exceeds admitted field table")
		}
		if d.Name() == "SourcePage" && number == 1 {
			records++
			if records > MaxPageSize {
				return limited("page records exceed limit")
			}
		}
		if d.Name() == "RelationshipResolutionInput" && number == 2 {
			relationshipTargets++
			if relationshipTargets > resourcev2.MaxRelationshipTargets {
				return limited("relationship target bindings exceed limit")
			}
		}
		mask := uint64(1) << f.Index()
		if !f.IsList() && !f.IsMap() && seen&mask != 0 {
			if utilityPackage(d) && d.Name() == "MetricStreamInput" && number == 15 {
				return resourcev2.NewError("RESET_REQUIRED", "Duplicate metric catalog admission pin.")
			}
			return invalid("duplicate singular protobuf field")
		}
		seen |= mask
		if o := f.ContainingOneof(); o != nil {
			if o.Index() >= 64 {
				return unsupported("resource descriptor exceeds admitted oneof table")
			}
			mask := uint64(1) << o.Index()
			if selections&mask != 0 {
				return invalid("duplicate protobuf oneof selection")
			}
			selections |= mask
		}
		expected := protowire.VarintType
		switch f.Kind() {
		case protoreflect.StringKind, protoreflect.BytesKind, protoreflect.MessageKind:
			expected = protowire.BytesType
		case protoreflect.DoubleKind, protoreflect.Fixed64Kind, protoreflect.Sfixed64Kind:
			expected = protowire.Fixed64Type
		case protoreflect.FloatKind, protoreflect.Fixed32Kind, protoreflect.Sfixed32Kind:
			expected = protowire.Fixed32Type
		}
		if wire != expected {
			return invalid("wrong protobuf wire type")
		}
		switch wire {
		case protowire.VarintType:
			v, m := protowire.ConsumeVarint(data)
			if m < 0 || m != protowire.SizeVarint(v) {
				return invalid("malformed protobuf varint")
			}
			data = data[m:]
			if versionField != 0 && number == versionField {
				version = v
			}
			if f.Kind() == protoreflect.BoolKind && v > 1 {
				return invalid("invalid protobuf boolean")
			}
			if f.Kind() == protoreflect.Uint32Kind && v > math.MaxUint32 {
				return invalid("protobuf integer overflow")
			}
			if (f.Kind() == protoreflect.Int32Kind || f.Kind() == protoreflect.EnumKind) && int64(v) != int64(int32(v)) {
				return invalid("protobuf integer overflow")
			}
			if d.Name() == "TimestampValue" && number == 2 && v >= 1000000000 {
				return invalid("invalid timestamp nanos")
			}
			if d.Name() == "TimestampValue" && number == 1 {
				seconds := protowire.DecodeZigZag(v)
				if seconds < -62135596800 || seconds > 253402300799 {
					return invalid("timestamp outside RFC3339 range")
				}
			}
			if d.FullName() == "google.protobuf.Timestamp" && ((number == 1 && (int64(v) < -62135596800 || int64(v) > 253402300799)) || (number == 2 && v >= 1000000000)) {
				return invalid("invalid session timestamp")
			}
			if d.FullName() == "google.protobuf.Duration" {
				if number == 1 {
					durationSeconds = int64(v)
				} else if number == 2 {
					durationNanos = int64(v)
				}
			}
			if (d.Name() == "EnumerateInput" && number == 2 || d.Name() == "NativeQueryInput" && number == 4) && v > MaxPageSize {
				return limited("page size exceeds limit")
			}
		case protowire.Fixed64Type:
			v, m := protowire.ConsumeFixed64(data)
			if m < 0 {
				return invalid("truncated fixed64")
			}
			data = data[m:]
			if f.Kind() == protoreflect.DoubleKind && (math.IsNaN(math.Float64frombits(v)) || math.IsInf(math.Float64frombits(v), 0)) {
				return invalid("nonfinite double")
			}
		case protowire.Fixed32Type:
			_, m := protowire.ConsumeFixed32(data)
			if m < 0 {
				return invalid("truncated fixed32")
			}
			data = data[m:]
		case protowire.BytesType:
			size, prefix := protowire.ConsumeVarint(data)
			if prefix < 0 || prefix != protowire.SizeVarint(size) {
				return invalid("malformed protobuf length")
			}
			v, m := protowire.ConsumeBytes(data)
			if m < 0 {
				return invalid("malformed protobuf length")
			}
			data = data[m:]
			if utilityPackage(d) && d.Name() == "MetricStreamInput" && number == 15 {
				if len(v) > resourcev2.MaxCatalogRevisionBytes || !utf8.Valid(v) {
					return resourcev2.NewError("RESET_REQUIRED", "Invalid metric catalog admission pin.")
				}
			}
			if len(v) > MaxBodyBytes && (f.Kind() != protoreflect.MessageKind || f.Message().Name() == "Value") {
				return limited("resource document exceeds byte limit")
			}
			if f.Kind() == protoreflect.StringKind && !utf8.Valid(v) {
				return invalid("invalid protobuf UTF-8")
			}
			if f.Kind() == protoreflect.MessageKind {
				if err := b.message(v, f.Message(), depth+1); err != nil {
					return err
				}
				if f.IsMap() && duplicateWireKey(&keys, number, rawEntryKey(v)) {
					return invalid("duplicate protobuf map key")
				}
				if d.Name() == "Record" && number == 2 {
					kind, _, _ := protowire.ConsumeTag(v)
					if kind != 9 {
						return invalid("resource body must be a map")
					}
				}
				if d.Name() == "MapValue" && duplicateWireKey(&keys, number, rawEntryKey(v)) {
					return invalid("duplicate map key")
				}
				if (d.Name() == "ResourceAddress" && number == 4 || d.Name() == "QueryScope" && number == 1) && duplicateWireKey(&keys, number, rawEntryKey(v)) {
					return invalid("duplicate scope dimension")
				}
				if (d.Name() == "ResourceMetadata" && number == 1 || d.Name() == "WatchConnectionSummary" && (number == 2 || number == 3)) && duplicateWireKey(&keys, number, rawEntryKey(v)) {
					return invalid("duplicate metadata map key")
				}
				if d.Name() == "RelationshipDescriptor" && (number == 8 || number == 9) && duplicateWireKey(&keys, 0, rawEntryKey(v)) {
					return invalid("duplicate relationship scope dimension")
				}
			}
		}
	}
	if versionField != 0 && version != 2 {
		return unsupported("resource utility wire version must be 2")
	}
	if d.FullName() == "google.protobuf.Duration" && (durationSeconds < -315576000000 || durationSeconds > 315576000000 || durationNanos < -999999999 || durationNanos > 999999999 || durationSeconds > 0 && durationNanos < 0 || durationSeconds < 0 && durationNanos > 0) {
		return invalid("invalid utility duration")
	}
	if d.Name() == "Value" && selections == 0 {
		return invalid("missing value selection")
	}
	if d.Name() == "ScopeConstraint" && selections == 0 {
		return invalid("missing scope selection")
	}
	if d.Name() == "ValueEntry" && seen&(uint64(1)<<d.Fields().ByNumber(2).Index()) == 0 {
		return invalid("missing map entry value")
	}
	return nil
}

type wireKey struct {
	field protoreflect.FieldNumber
	key   string
}

func duplicateWireKey(keys *map[wireKey]struct{}, field protoreflect.FieldNumber, key string) bool {
	if *keys == nil {
		*keys = make(map[wireKey]struct{})
	}
	k := wireKey{field: field, key: key}
	if _, ok := (*keys)[k]; ok {
		return true
	}
	(*keys)[k] = struct{}{}
	return false
}
func rawEntryKey(data []byte) string {
	for len(data) > 0 {
		n, t, k := protowire.ConsumeTag(data)
		data = data[k:]
		if n == 1 {
			v, _ := protowire.ConsumeBytes(data)
			return string(v)
		}
		k = protowire.ConsumeFieldValue(n, t, data)
		data = data[k:]
	}
	return ""
}
