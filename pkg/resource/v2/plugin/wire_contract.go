package plugin

import (
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// admitResourceContractVersion rejects incompatible cohorts before descending
// into their changed payload shapes. Utility and callback handshakes retain
// their own independent protocol versions.
func admitResourceContractVersion(data []byte, descriptor protoreflect.MessageDescriptor) error {
	if descriptor.ParentFile().Package() != "omniview.resource.v2" {
		return nil
	}
	field := descriptor.Fields().ByName("version")
	if field == nil {
		return nil
	}
	for len(data) > 0 {
		number, wire, count, err := consumeWireTag(data)
		if err != nil {
			return err
		}
		data = data[count:]
		if wire == protowire.StartGroupType || wire == protowire.EndGroupType {
			return invalid("invalid protobuf wire type")
		}
		if number == field.Number() {
			if wire != protowire.VarintType {
				return invalid("invalid resource wire version field")
			}
			value, count := protowire.ConsumeVarint(data)
			if count < 0 || count != protowire.SizeVarint(value) {
				return invalid("malformed resource wire version")
			}
			if value != uint64(resourcev2.WireVersion) {
				return unsupported("unsupported resource wire version; coordinated SDK cohort required")
			}
			return nil
		}
		count = protowire.ConsumeFieldValue(number, wire, data)
		if count < 0 {
			return invalid("malformed resource protobuf field")
		}
		data = data[count:]
	}
	return unsupported("unsupported resource wire version; coordinated SDK cohort required")
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
