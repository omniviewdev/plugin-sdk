package plugin

import (
	"encoding/json"
	"math"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
)

// WirePolicy is an implementation-candidate transport ceiling, not predicate policy or an SLO.
const WirePolicy = resourcev2.ResourceWirePolicy

const (
	// MaxWireBytes bounds the entire raw resource protobuf frame before decoding.
	MaxWireBytes = resourcev2.MaxMessageBytes
	// MaxBodyBytes bounds scalar payloads and encoded resource Value documents.
	MaxBodyBytes = resourcev2.MaxDocumentBytes
	// MaxDepth bounds nested protobuf messages, with the root at depth zero.
	MaxDepth = resourcev2.MaxProtoDepth
	// MaxNodes bounds raw message occurrences across one frame, including its root.
	MaxNodes = resourcev2.MaxValueNodes
	// MaxFields bounds raw field occurrences across all messages in one frame.
	MaxFields = resourcev2.MaxWireFields
	// MaxPageSize bounds page records and requested enumeration/query page sizes.
	MaxPageSize = resourcev2.MaxPageRecords
)

const (
	wirePresenceBits        = 64
	wireNanosField          = 2
	wireEntryValueField     = 2
	wireSchemaReasonField   = 9
	wireValueMapField       = 9
	wireSchemaReasonBytes   = 64
	wireNanosPerSecond      = 1000000000
	wireTimestampMinSeconds = -62135596800
	wireTimestampMaxSeconds = 253402300799
	wireDurationMaxSeconds  = 315576000000
	wireDurationMaxNanos    = 999999999
)

func invalid(s string) error     { return resourcev2.NewError("INVALID_REQUEST", s) }
func limited(s string) error     { return resourcev2.NewError("RESOURCE_LIMIT", s) }
func unsupported(s string) error { return resourcev2.NewError("UNSUPPORTED", s) }

// Nodes count raw protobuf message occurrences across the entire frame.
type wireBudget struct{ fields, nodes int }

type wireMessage struct {
	descriptor protoreflect.MessageDescriptor

	seen, selections uint64
	version          uint64

	versionField protoreflect.FieldNumber

	durationSeconds, durationNanos int64

	schemaUnavailable, schemaReason bool

	keys map[wireKey]struct{}

	records, relationshipTargets int
}

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
	state := wireMessage{descriptor: d, versionField: utilityVersionField(d)}
	for len(data) > 0 {
		b.fields++
		if b.fields > MaxFields {
			return limited("resource fields exceed limit")
		}
		number, wire, n, err := consumeWireTag(data)
		if err != nil {
			return err
		}
		data = data[n:]
		f := d.Fields().ByNumber(number)
		if err = state.admitField(f); err != nil {
			return err
		}
		if wire != fieldWireType(f.Kind()) {
			return invalid("wrong protobuf wire type")
		}
		n, err = b.consumeField(data, wire, f, &state, depth)
		if err != nil {
			return err
		}
		data = data[n:]
	}
	return state.finish()
}

func consumeWireTag(data []byte) (protowire.Number, protowire.Type, int, error) {
	number, wire, n := protowire.ConsumeTag(data)
	if n < 0 || number <= 0 || number > protowire.MaxValidNumber {
		return 0, 0, 0, invalid("malformed protobuf tag")
	}
	// ConsumeVarint preserves the encoded tag without signed wire-type conversion.
	tag, _ := protowire.ConsumeVarint(data)
	if n != protowire.SizeVarint(tag) {
		return 0, 0, 0, invalid("noncanonical protobuf tag")
	}
	if wire < protowire.VarintType || wire > protowire.Fixed32Type ||
		wire == protowire.StartGroupType || wire == protowire.EndGroupType {
		return 0, 0, 0, invalid("invalid protobuf wire type")
	}
	return number, wire, n, nil
}

func (s *wireMessage) admitField(f protoreflect.FieldDescriptor) error {
	if f == nil {
		return unsupported("unknown resource protobuf field")
	}
	if f.Index() >= wirePresenceBits {
		return unsupported("resource descriptor exceeds admitted field table")
	}
	if err := s.countRepeated(f.Number()); err != nil {
		return err
	}
	mask := uint64(1) << f.Index()
	if !f.IsList() && !f.IsMap() && s.seen&mask != 0 {
		if metricCatalogPin(s.descriptor, f.Number()) {
			return resourcev2.NewError("RESET_REQUIRED", "Duplicate metric catalog admission pin.")
		}
		return invalid("duplicate singular protobuf field")
	}
	s.seen |= mask
	o := f.ContainingOneof()
	if o == nil {
		return nil
	}
	if o.Index() >= wirePresenceBits {
		return unsupported("resource descriptor exceeds admitted oneof table")
	}
	mask = uint64(1) << o.Index()
	if s.selections&mask != 0 {
		return invalid("duplicate protobuf oneof selection")
	}
	s.selections |= mask
	return nil
}

func (s *wireMessage) countRepeated(number protoreflect.FieldNumber) error {
	if s.descriptor.Name() == "SourcePage" && number == 1 {
		s.records++
		if s.records > MaxPageSize {
			return limited("page records exceed limit")
		}
	}
	if s.descriptor.Name() == "RelationshipResolutionInput" && number == 2 {
		s.relationshipTargets++
		if s.relationshipTargets > resourcev2.MaxRelationshipTargets {
			return limited("relationship target bindings exceed limit")
		}
	}
	return nil
}

func fieldWireType(kind protoreflect.Kind) protowire.Type {
	switch kind {
	case protoreflect.StringKind, protoreflect.BytesKind, protoreflect.MessageKind:
		return protowire.BytesType
	case protoreflect.DoubleKind, protoreflect.Fixed64Kind, protoreflect.Sfixed64Kind:
		return protowire.Fixed64Type
	case protoreflect.FloatKind, protoreflect.Fixed32Kind, protoreflect.Sfixed32Kind:
		return protowire.Fixed32Type
	case protoreflect.BoolKind, protoreflect.EnumKind, protoreflect.Int32Kind,
		protoreflect.Sint32Kind, protoreflect.Uint32Kind, protoreflect.Int64Kind,
		protoreflect.Sint64Kind, protoreflect.Uint64Kind, protoreflect.GroupKind:
		return protowire.VarintType
	default:
		return protowire.VarintType
	}
}

func (b *wireBudget) consumeField(
	data []byte, wire protowire.Type, f protoreflect.FieldDescriptor, s *wireMessage, depth int,
) (int, error) {
	switch wire {
	case protowire.VarintType:
		v, n := protowire.ConsumeVarint(data)
		if n < 0 || n != protowire.SizeVarint(v) {
			return 0, invalid("malformed protobuf varint")
		}
		return n, s.validateVarint(f, v)
	case protowire.Fixed64Type:
		v, n := protowire.ConsumeFixed64(data)
		if n < 0 {
			return 0, invalid("truncated fixed64")
		}
		if f.Kind() == protoreflect.DoubleKind &&
			(math.IsNaN(math.Float64frombits(v)) || math.IsInf(math.Float64frombits(v), 0)) {
			return 0, invalid("nonfinite double")
		}
		return n, nil
	case protowire.Fixed32Type:
		_, n := protowire.ConsumeFixed32(data)
		if n < 0 {
			return 0, invalid("truncated fixed32")
		}
		return n, nil
	case protowire.BytesType:
		return b.consumeBytes(data, f, s, depth)
	case protowire.StartGroupType, protowire.EndGroupType:
		return 0, invalid("invalid protobuf wire type")
	default:
		return 0, invalid("invalid protobuf wire type")
	}
}

// signedWireValue interprets a protobuf int64 varint's two's-complement bits.
func signedWireValue(v uint64) int64 {
	return int64(v) // #nosec G115 -- Protobuf int64 varints require two's-complement bit reinterpretation.
}

func (s *wireMessage) validateVarint(f protoreflect.FieldDescriptor, v uint64) error {
	number := f.Number()
	if s.versionField != 0 && number == s.versionField {
		s.version = v
	}
	if f.Kind() == protoreflect.BoolKind && v > 1 {
		return invalid("invalid protobuf boolean")
	}
	if f.Kind() == protoreflect.Uint32Kind && v > math.MaxUint32 {
		return invalid("protobuf integer overflow")
	}
	if (f.Kind() == protoreflect.Int32Kind || f.Kind() == protoreflect.EnumKind) &&
		v > math.MaxInt32 && v < math.MaxUint64-math.MaxInt32 {
		return invalid("protobuf integer overflow")
	}
	if err := s.validateTimestamp(number, v); err != nil {
		return err
	}
	if s.descriptor.FullName() == "google.protobuf.Duration" {
		switch int(number) {
		case 1:
			s.durationSeconds = signedWireValue(v)
		case wireNanosField:
			s.durationNanos = signedWireValue(v)
		}
	}
	if (s.descriptor.Name() == "EnumerateInput" && number == 2 ||
		s.descriptor.Name() == "NativeQueryInput" && number == 4) && v > MaxPageSize {
		return limited("page size exceeds limit")
	}
	return nil
}

func (s *wireMessage) validateTimestamp(number protoreflect.FieldNumber, v uint64) error {
	if s.descriptor.Name() == "TimestampValue" {
		if number == wireNanosField && v >= wireNanosPerSecond {
			return invalid("invalid timestamp nanos")
		}
		if number == 1 {
			seconds := protowire.DecodeZigZag(v)
			if seconds < wireTimestampMinSeconds || seconds > wireTimestampMaxSeconds {
				return invalid("timestamp outside RFC3339 range")
			}
		}
	}
	if s.descriptor.FullName() == "google.protobuf.Timestamp" {
		seconds := signedWireValue(v)
		if (number == 1 && (seconds < wireTimestampMinSeconds || seconds > wireTimestampMaxSeconds)) ||
			(number == wireNanosField && v >= wireNanosPerSecond) {
			return invalid("invalid session timestamp")
		}
	}
	return nil
}

func metricCatalogPin(d protoreflect.MessageDescriptor, number protoreflect.FieldNumber) bool {
	return utilityPackage(d) && d.Name() == utilityMetricStreamInputName && number == 15
}

func (b *wireBudget) consumeBytes(
	data []byte, f protoreflect.FieldDescriptor, s *wireMessage, depth int,
) (int, error) {
	size, prefix := protowire.ConsumeVarint(data)
	if prefix < 0 || prefix != protowire.SizeVarint(size) {
		return 0, invalid("malformed protobuf length")
	}
	v, n := protowire.ConsumeBytes(data)
	if n < 0 {
		return 0, invalid("malformed protobuf length")
	}
	if err := s.validateBytes(f, v); err != nil {
		return 0, err
	}
	if f.Kind() != protoreflect.MessageKind {
		return n, nil
	}
	if err := b.message(v, f.Message(), depth+1); err != nil {
		return 0, err
	}
	return n, s.validateChild(f, v)
}

func (s *wireMessage) validateBytes(f protoreflect.FieldDescriptor, v []byte) error {
	if metricCatalogPin(s.descriptor, f.Number()) {
		if len(v) > resourcev2.MaxCatalogRevisionBytes || !utf8.Valid(v) {
			return resourcev2.NewError("RESET_REQUIRED", "Invalid metric catalog admission pin.")
		}
	}
	if s.descriptor.FullName() == "omniview.resource.v2.SchemaBinding" {
		if err := s.schemaBytes(f.Number(), v); err != nil {
			return err
		}
	}
	if s.descriptor.FullName() == "omniview.resource.v2.ActionInput" && f.Name() == "expected_descriptor_json" {
		if len(v) == 0 || len(v) > MaxBodyBytes {
			return invalid("invalid expected action descriptor")
		}
		var descriptor resourcev2.ActionDescriptor
		if err := json.Unmarshal(v, &descriptor); err != nil {
			return invalid("invalid expected action descriptor")
		}
	}
	if len(v) > MaxBodyBytes && (f.Kind() != protoreflect.MessageKind || f.Message().Name() == "Value") {
		return limited("resource document exceeds byte limit")
	}
	if f.Kind() == protoreflect.StringKind && !utf8.Valid(v) {
		return invalid("invalid protobuf UTF-8")
	}
	return nil
}

func (s *wireMessage) schemaBytes(number protoreflect.FieldNumber, v []byte) error {
	switch int(number) {
	case 1:
		s.schemaUnavailable = string(v) == "unavailable"
	case wireSchemaReasonField:
		s.schemaReason = true
		if len(v) > wireSchemaReasonBytes {
			return invalid("invalid schema unavailable reason")
		}
		return resourcev2.ValidateSchemaUnavailableReason(string(v))
	}
	return nil
}

func (s *wireMessage) validateChild(f protoreflect.FieldDescriptor, v []byte) error {
	number := f.Number()
	if f.IsMap() && duplicateWireKey(&s.keys, number, rawEntryKey(v)) {
		return invalid("duplicate protobuf map key")
	}
	if s.descriptor.Name() == "Record" && number == 2 {
		kind, _, _ := protowire.ConsumeTag(v)
		if kind != wireValueMapField {
			return invalid("resource body must be a map")
		}
	}
	return s.validateDomainKey(number, v)
}

func (s *wireMessage) validateDomainKey(number protoreflect.FieldNumber, v []byte) error {
	name := s.descriptor.Name()
	var message string
	keyField := number
	switch {
	case name == "MapValue":
		message = "duplicate map key"
	case name == "ResourceAddress" && number == 4 || name == "QueryScope" && number == 1:
		message = "duplicate scope dimension"
	case name == "ResourceMetadata" && number == 1 ||
		name == "WatchConnectionSummary" && (number == 2 || number == 3):
		message = "duplicate metadata map key"
	case name == "RelationshipDescriptor" && (number == 8 || number == 9):
		message = "duplicate relationship scope dimension"
		keyField = 0
	default:
		return nil
	}
	if duplicateWireKey(&s.keys, keyField, rawEntryKey(v)) {
		return invalid(message)
	}
	return nil
}

func (s *wireMessage) finish() error {
	if s.descriptor.Name() == "ActionInput" {
		reference := s.descriptor.Fields().ByName("reference")
		typeTarget := s.descriptor.Fields().ByName("type_target")
		if reference == nil || typeTarget == nil {
			return unsupported("action target descriptor is incomplete")
		}
		hasReference := s.seen&(uint64(1)<<reference.Index()) != 0
		hasTypeTarget := s.seen&(uint64(1)<<typeTarget.Index()) != 0
		if hasReference == hasTypeTarget {
			return invalid("action requires exactly one target")
		}
	}
	if s.schemaReason && !s.schemaUnavailable {
		return invalid("schema unavailable reason requires unavailable state")
	}
	if s.versionField != 0 && s.version != 2 {
		return unsupported("resource utility wire version must be 2")
	}
	if s.descriptor.FullName() == "google.protobuf.Duration" && !s.validDuration() {
		return invalid("invalid utility duration")
	}
	if s.descriptor.Name() == "Value" && s.selections == 0 {
		return invalid("missing value selection")
	}
	if s.descriptor.Name() == "ScopeConstraint" && s.selections == 0 {
		return invalid("missing scope selection")
	}
	if s.descriptor.Name() == "ValueEntry" &&
		s.seen&(uint64(1)<<s.descriptor.Fields().ByNumber(wireEntryValueField).Index()) == 0 {
		return invalid("missing map entry value")
	}
	return nil
}

func (s *wireMessage) validDuration() bool {
	return s.durationSeconds >= -wireDurationMaxSeconds && s.durationSeconds <= wireDurationMaxSeconds &&
		s.durationNanos >= -wireDurationMaxNanos && s.durationNanos <= wireDurationMaxNanos &&
		(s.durationSeconds <= 0 || s.durationNanos >= 0) &&
		(s.durationSeconds >= 0 || s.durationNanos <= 0)
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
