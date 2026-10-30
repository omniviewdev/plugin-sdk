package plugin

import (
	"context"
	"errors"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/encoding"
	grpcproto "google.golang.org/grpc/encoding/proto"
	"google.golang.org/grpc/mem"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
)

// envelope retains admission failure within a single RPC, without global state.
type envelope struct {
	message         proto.Message
	err             error
	legacyExec      bool
	forceStrict     bool
	legacyValidator func([]byte) error
}
type strictCodec struct{ fallback encoding.CodecV2 }

// init installs raw-wire admission before gRPC can decode resource messages.
//
//nolint:gochecknoinits // gRPC's process-wide codec registry must be set before either clients or servers start.
func init() {
	encoding.RegisterCodecV2(strictCodec{fallback: encoding.GetCodecV2(grpcproto.Name)})
}

func (c strictCodec) Name() string { return grpcproto.Name }
func (c strictCodec) Marshal(v any) (mem.BufferSlice, error) {
	if e, ok := v.(*envelope); ok {
		v = e.message
	}
	return c.fallback.Marshal(v)
}

func (c strictCodec) Unmarshal(data mem.BufferSlice, v any) error {
	e, wrapped := v.(*envelope)
	var p proto.Message
	if wrapped {
		p = e.message
	} else {
		p, _ = v.(proto.Message)
	}
	if !wrapped && p != nil && legacyUtilityRequest(p.ProtoReflect().Descriptor()) {
		return c.fallback.Unmarshal(data, v)
	}
	if !strictDecode(p, e, wrapped) {
		return c.fallback.Unmarshal(data, v)
	}
	err := decodeResource(data, p, e, wrapped)
	if err != nil {
		if resourcev2.ErrorCode(err) == "INTERNAL" {
			err = invalid("malformed resource protobuf")
		}
		if wrapped {
			e.err = err
			return nil
		}
		return rpcError(err)
	}
	return nil
}

func strictDecode(p proto.Message, e *envelope, wrapped bool) bool {
	if p == nil {
		return false
	}
	return strictRoot(p.ProtoReflect().Descriptor()) ||
		wrapped && (e.legacyExec || e.forceStrict || e.legacyValidator != nil)
}

func decodeResource(data mem.BufferSlice, p proto.Message, e *envelope, wrapped bool) error {
	if data.Len() > MaxWireBytes {
		return limited("resource message exceeds byte limit")
	}
	buf := data.MaterializeToBuffer(mem.DefaultBufferPool())
	defer buf.Free()
	var err error
	switch {
	case wrapped && e.legacyValidator != nil:
		err = e.legacyValidator(buf.ReadOnlyData())
	case wrapped && e.legacyExec:
		err = ValidateLegacyExecWire(buf.ReadOnlyData())
	default:
		err = ValidateWire(buf.ReadOnlyData(), p.ProtoReflect().Descriptor())
	}
	if err != nil {
		return err
	}
	return proto.Unmarshal(buf.ReadOnlyData(), p)
}

func rpcError(err error) error {
	if err == nil {
		return nil
	}
	var typed *resourcev2.ResourceOperationError
	if !errors.As(err, &typed) || typed == nil {
		if errors.Is(err, context.Canceled) {
			return status.Error(codes.Canceled, "resource call cancelled")
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return status.Error(codes.DeadlineExceeded, "resource call timed out")
		}
		if st, ok := status.FromError(err); ok {
			return status.Error(st.Code(), "resource transport failed")
		}
		err = resourcev2.NewError("PROVIDER_ERROR", "resource operation failed")
	}
	code := resourcev2.ErrorCode(err)
	grpcCode := codes.Internal
	switch code {
	case "INVALID_REQUEST", "INVALID_INPUT", "FILTER_UNKNOWN_FIELD", "FILTER_INVALID_OPERATOR":
		grpcCode = codes.InvalidArgument
	case "UNSUPPORTED":
		grpcCode = codes.Unimplemented
	case "RESOURCE_LIMIT":
		grpcCode = codes.ResourceExhausted
	case "NOT_FOUND":
		grpcCode = codes.NotFound
	case "ALREADY_EXISTS":
		grpcCode = codes.AlreadyExists
	case "UNAUTHORIZED":
		grpcCode = codes.Unauthenticated
	case "FORBIDDEN", "PERMISSION_DENIED":
		grpcCode = codes.PermissionDenied
	case "CONFLICT":
		grpcCode = codes.Aborted
	case "UNAVAILABLE", "CONNECTION_ERROR", "CERTIFICATE_ERROR":
		grpcCode = codes.Unavailable
	case "CONTINUATION_EXPIRED", "RESET_REQUIRED", "PARTIAL_RESULT":
		grpcCode = codes.FailedPrecondition
	case "CANCELLED":
		grpcCode = codes.Canceled
	case "TIMEOUT":
		grpcCode = codes.DeadlineExceeded
	}
	s := status.New(grpcCode, "resource operation failed")
	detailed, e := s.WithDetails(&errdetails.ErrorInfo{Reason: code, Domain: resourceErrorDomain})
	if e != nil {
		return s.Err()
	}
	return detailed.Err()
}

func domainError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, "resource call cancelled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "resource call timed out")
	}
	st, ok := status.FromError(err)
	if !ok {
		return status.Error(codes.Unknown, "resource transport failed")
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.Domain == resourceErrorDomain {
			if !resourcev2.IsErrorCode(info.Reason) {
				return status.Error(codes.Internal, "invalid resource error classification")
			}
			return &resourcev2.ResourceOperationError{Err: err, Code: info.Reason, Message: "resource operation failed"}
		}
	}
	return status.Error(st.Code(), "resource transport failed")
}

func responseError(message proto.Message, err error) error {
	if err != nil {
		return err
	}
	if proto.Size(message) > MaxWireBytes {
		return limited("encoded resource frame exceeds limit")
	}
	return nil
}

func sendResource(stream interface{ SendMsg(any) error }, message proto.Message) error {
	if err := responseError(message, nil); err != nil {
		return err
	}
	return stream.SendMsg(message)
}
