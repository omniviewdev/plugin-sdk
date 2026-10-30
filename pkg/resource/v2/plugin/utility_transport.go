package plugin

import (
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// NewUtilityTransportEnvelope admits a resource utility message before generated
// decoding. A nil legacyValidator selects the strict negotiated descriptor tree;
// a nonnil validator preserves an ordinary utility lane while rejecting resource
// intent or a legacy resource body before that lane can reach native code.
func NewUtilityTransportEnvelope(message proto.Message, legacyValidator func([]byte) error) *TransportEnvelope {
	return &envelope{message: message, forceStrict: true, legacyValidator: legacyValidator}
}

// ValidateLegacyUtilityWire checks only resource intent on an ordinary utility
// lane. It never materializes or converts a legacy Struct into canonical data.
func ValidateLegacyUtilityWire(data []byte, resourceFields, nonemptyFields []protowire.Number) error {
	if len(data) > MaxWireBytes {
		return limited("utility message exceeds resource admission limit")
	}
	fields := 0
	for len(data) > 0 {
		fields++
		if fields > MaxFields {
			return limited("utility fields exceed admission limit")
		}
		number, wire, n := protowire.ConsumeTag(data)
		if n < 0 || number <= 0 {
			return invalid("malformed utility protobuf tag")
		}
		data = data[n:]
		size := protowire.ConsumeFieldValue(number, wire, data)
		if size < 0 {
			return invalid("malformed utility protobuf value")
		}
		for _, field := range resourceFields {
			if number == field {
				return unsupported("resource utility requires its explicit resource RPC")
			}
		}
		for _, field := range nonemptyFields {
			if number == field {
				if wire != protowire.BytesType {
					return invalid("invalid utility resource selector")
				}
				value, n := protowire.ConsumeBytes(data)
				if n < 0 {
					return invalid("malformed utility resource selector")
				}
				if len(value) > 0 {
					return unsupported("resource utility requires its explicit resource RPC")
				}
			}
		}
		data = data[size:]
	}
	return nil
}

func utilityPackage(d protoreflect.MessageDescriptor) bool {
	switch d.ParentFile().Package() {
	case "omniview.sdk.exec.v1",
		"omniview.sdk.logs.v1",
		"omniview.sdk.metric.v1",
		"omniview.sdk.networker.v1",
		"com.omniview.pluginsdk":
		return true
	}
	return false
}

func strictUtilityRoot(d protoreflect.MessageDescriptor) bool {
	if !utilityPackage(d) {
		return false
	}
	switch d.Name() {
	case "SessionOptions", "ResourceSessionResponse",
		"CreateLogSessionRequest", "ResourceLogSessionResponse",
		"QueryMetricsRequest", "MetricStreamInput", "ResourceQueryMetricsResponse", "ResourceMetricStreamOutput",
		"PortForwardSessionOptions", "PortForwardResourceConnection", "PortForwardSession", "ResourcePortForwardSessionResponse", "PortForwardSessionByIdResponse", "PortForwardSessionListResponse":
		return true
	}
	return false
}

func legacyUtilityRequest(d protoreflect.MessageDescriptor) bool {
	if !utilityPackage(d) {
		return false
	}
	switch d.Name() {
	case "SessionOptions",
		"CreateLogSessionRequest",
		"QueryMetricsRequest",
		"MetricStreamInput",
		"PortForwardSessionOptions":
		return true
	}
	return false
}

func utilityVersionField(d protoreflect.MessageDescriptor) protoreflect.FieldNumber {
	if !utilityPackage(d) {
		return 0
	}
	field := d.Fields().ByName("resource_wire_version")
	if field != nil {
		return field.Number()
	}
	return 0
}

// networkSessionResponse identifies only utility response trees that can carry
// a canonical resource connection. Pure static payloads keep their old protobuf
// field semantics; resource-bearing payloads admit the entire bounded tree.
func networkSessionResponse(d protoreflect.MessageDescriptor) bool {
	if !utilityPackage(d) {
		return false
	}
	switch d.Name() {
	case "PortForwardSessionByIdResponse", "PortForwardSessionListResponse", "PortForwardSession":
		return true
	}
	return false
}

func networkResourcePresent(data []byte, d protoreflect.MessageDescriptor) (bool, error) {
	budget := wireBudget{}
	return budget.networkResourcePresent(data, d, 0)
}

func (b *wireBudget) networkResourcePresent(data []byte, d protoreflect.MessageDescriptor, depth int) (bool, error) {
	if depth > 3 {
		return false, invalid("invalid network utility response nesting")
	}
	for len(data) > 0 {
		b.fields++
		if b.fields > MaxFields {
			return false, limited("network utility fields exceed limit")
		}
		number, wire, n := protowire.ConsumeTag(data)
		if n < 0 || number <= 0 {
			return false, invalid("malformed network utility response")
		}
		data = data[n:]
		size := protowire.ConsumeFieldValue(number, wire, data)
		if size < 0 {
			return false, invalid("malformed network utility response")
		}
		resource := d.Name() == "PortForwardSession" && number == 20
		session := (d.Name() == "PortForwardSessionByIdResponse" || d.Name() == "PortForwardSessionListResponse") &&
			number == 1
		if resource || session {
			if wire != protowire.BytesType {
				return false, invalid("invalid network utility response field")
			}
			if resource {
				return true, nil
			}
			value, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return false, invalid("malformed network utility session")
			}
			field := d.Fields().ByNumber(number)
			found, err := b.networkResourcePresent(value, field.Message(), depth+1)
			if err != nil || found {
				return found, err
			}
		}
		data = data[size:]
	}
	return false, nil
}
