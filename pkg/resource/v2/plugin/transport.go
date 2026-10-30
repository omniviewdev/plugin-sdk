package plugin

import (
	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/mem"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	resourceErrorDomain = "omniview.resource.v2"
	rpcCallMethod       = "Call"
)

// TransportEnvelope preserves admission errors before generated protobuf decoding.
type TransportEnvelope = envelope

// NewTransportEnvelope wraps a message for bounded raw-wire admission before decoding.
func NewTransportEnvelope(message proto.Message, legacyExec bool) *TransportEnvelope {
	return &envelope{message: message, legacyExec: legacyExec}
}

// AdmissionError returns the raw-wire admission failure retained during decoding.
func (e *envelope) AdmissionError() error { return e.err }

// ResourceRPCError converts a domain error into a public-safe gRPC status.
func ResourceRPCError(err error) error { return rpcError(err) }

// ResourceDomainError restores a classified resource error from a gRPC status.
func ResourceDomainError(err error) error { return domainError(err) }

func strictRoot(d protoreflect.MessageDescriptor) bool {
	namespace := d.ParentFile().Package()
	return namespace == resourceErrorDomain || namespace == "omniview.sdk.tools.v2" || strictUtilityRoot(d)
}

// ValidateLegacyExecWire detects resource intent without discarding reserved fields.
// Nonresource utility messages retain the generated protobuf admission policy.
func ValidateLegacyExecWire(data []byte) error {
	for len(data) > 0 {
		n, t, k := protowire.ConsumeTag(data)
		if k < 0 {
			return invalid("malformed exec protobuf")
		}
		data = data[k:]
		size := protowire.ConsumeFieldValue(n, t, data)
		if size < 0 {
			return invalid("malformed exec protobuf")
		}
		if n >= 6 && n <= 12 {
			if n == 8 || n >= 9 {
				return unsupported("resource sessions require CreateResourceSession")
			}
			if t != protowire.BytesType {
				return invalid("invalid resource selector")
			}
			value, _ := protowire.ConsumeBytes(data)
			if len(value) > 0 {
				return unsupported("resource sessions require CreateResourceSession")
			}
		}
		data = data[size:]
	}
	return nil
}

// ResponseAdmission retains response admission failures within one unary call.
func ResponseAdmission() (grpc.CallOption, func() error) {
	c := &responseAdmissionCodec{codec: encoding.GetCodecV2("proto")}
	return grpc.ForceCodecV2(c), func() error { return c.err }
}

type responseAdmissionCodec struct {
	codec encoding.CodecV2
	err   error
}

func (c *responseAdmissionCodec) Name() string                           { return "proto" }
func (c *responseAdmissionCodec) Marshal(v any) (mem.BufferSlice, error) { return c.codec.Marshal(v) }

func (c *responseAdmissionCodec) Unmarshal(data mem.BufferSlice, v any) error {
	p, ok := v.(proto.Message)
	if !ok {
		return c.codec.Unmarshal(data, v)
	}
	e := NewTransportEnvelope(p, false)
	if err := c.codec.Unmarshal(data, e); err != nil {
		return err
	}
	c.err = e.AdmissionError()
	return nil
}
