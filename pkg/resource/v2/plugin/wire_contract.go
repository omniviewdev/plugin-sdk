package plugin

import (
	"sync/atomic"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// readWireFloor is the oldest resource wire this process reads. A plugin keeps the
// default, the current wire only; a host that dual-reads lowers it once at start
// (SetReadWireFloor) and then sends and expects each peer's own wire (NewClientWire).
var readWireFloor atomic.Uint32

func init() { readWireFloor.Store(resourcev2.WireVersion) }

// SetReadWireFloor makes this process read every resource wire from floor to
// resourcev2.WireVersion. A message is admitted against the descriptor tree as it
// was at the wire its own version field names.
func SetReadWireFloor(floor uint32) error {
	if floor < resourcev2.FloorWireVersion || floor > resourcev2.WireVersion {
		return unsupported("resource wire floor outside what this SDK can read")
	}
	readWireFloor.Store(floor)
	return nil
}

// fieldSince names, by full name, every field of omniview.resource.v2 that does not
// exist before a wire. A reader that admits an earlier wire rejects them as unknown,
// exactly as the reader of that wire always did. Every strict-lane field added after
// wire 3 needs a row here; the descriptor-digest golden test the SDK0 plan asks for is
// what would keep that true.
var fieldSince = map[protoreflect.FullName]uint32{
	"omniview.resource.v2.ResourceType.environment": 4,
}

// admitResourceContractVersion rejects incompatible cohorts before descending
// into their changed payload shapes, and returns the wire the message speaks (0
// for a message that carries no resource wire version). Utility and callback
// handshakes retain their own independent protocol versions.
func admitResourceContractVersion(data []byte, descriptor protoreflect.MessageDescriptor) (uint32, error) {
	if descriptor.ParentFile().Package() != "omniview.resource.v2" {
		return 0, nil
	}
	field := descriptor.Fields().ByName("version")
	if field == nil {
		return 0, nil
	}
	for len(data) > 0 {
		number, wire, count, err := consumeWireTag(data)
		if err != nil {
			return 0, err
		}
		data = data[count:]
		if wire == protowire.StartGroupType || wire == protowire.EndGroupType {
			return 0, invalid("invalid protobuf wire type")
		}
		if number == field.Number() {
			if wire != protowire.VarintType {
				return 0, invalid("invalid resource wire version field")
			}
			value, count := protowire.ConsumeVarint(data)
			if count < 0 || count != protowire.SizeVarint(value) {
				return 0, invalid("malformed resource wire version")
			}
			if value < uint64(readWireFloor.Load()) || value > uint64(resourcev2.WireVersion) {
				return 0, unsupported("unsupported resource wire version; coordinated SDK cohort required")
			}
			return uint32(value), nil
		}
		count = protowire.ConsumeFieldValue(number, wire, data)
		if count < 0 {
			return 0, invalid("malformed resource protobuf field")
		}
		data = data[count:]
	}
	return 0, unsupported("unsupported resource wire version; coordinated SDK cohort required")
}

func (s *wireMessage) requireFields(numbers ...protoreflect.FieldNumber) error {
	for _, number := range numbers {
		field := s.descriptor.Fields().ByNumber(number)
		if field == nil || s.seen&(uint64(1)<<field.Index()) == 0 {
			return invalid("missing required resource protobuf field")
		}
	}
	return nil
}

func (s *wireMessage) finishSchemaContract() error {
	if s.descriptor.ParentFile().Package() != "omniview.resource.v2" {
		return nil
	}
	switch s.descriptor.Name() {
	case "AncillaryRequest":
		if s.resolveSchema {
			return s.requireFields(12)
		}
	case "CatalogRequest":
		return s.requireFields(1)
	case "Representation":
		return s.requireFields(1, 2)
	case "SchemaTarget":
		if s.selections == 0 {
			return invalid("schema request requires exactly one target")
		}
	case "AuxiliarySchemaTarget":
		return s.requireFields(1, 2)
	case "SchemaRequest":
		return s.requireFields(1, 2, 3)
	case "SchemaDocumentRequest":
		return s.requireFields(1, 2)
	}
	return nil
}

func (s *wireMessage) schemaContractBytes(field protoreflect.FieldDescriptor, value []byte) error {
	if s.descriptor.ParentFile().Package() != "omniview.resource.v2" {
		return nil
	}
	switch s.descriptor.Name() {
	case "AncillaryRequest":
		if field.Number() == 2 {
			s.resolveSchema = string(value) == "resolveSchema"
		}
	case "AuxiliarySchemaTarget":
		if field.Number() == 3 && len(value) == 0 {
			return invalid("auxiliary source revision must not be empty")
		}
	}
	return nil
}
