package plugin

import (
	"context"
	"errors"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/encoding"
	_ "google.golang.org/grpc/encoding/proto"
	"google.golang.org/grpc/mem"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// envelope retains admission failure within a single RPC, without global state.
type envelope struct {
	message    proto.Message
	err        error
	legacyExec bool
}
type strictCodec struct{ fallback encoding.CodecV2 }

func init()                        { encoding.RegisterCodecV2(strictCodec{fallback: encoding.GetCodecV2("proto")}) }
func (c strictCodec) Name() string { return "proto" }
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
	if !wrapped && p != nil && p.ProtoReflect().Descriptor().FullName() == "omniview.sdk.exec.v1.SessionOptions" {
		return c.fallback.Unmarshal(data, v)
	}
	if p == nil || (!strictRoot(p.ProtoReflect().Descriptor()) && !(wrapped && e.legacyExec)) {
		return c.fallback.Unmarshal(data, v)
	}
	var err error
	if data.Len() > MaxWireBytes {
		err = limited("resource message exceeds byte limit")
	} else {
		buf := data.MaterializeToBuffer(mem.DefaultBufferPool())
		defer buf.Free()
		if wrapped && e.legacyExec {
			err = ValidateLegacyExecWire(buf.ReadOnlyData())
		} else {
			err = ValidateWire(buf.ReadOnlyData(), p.ProtoReflect().Descriptor())
		}
		if err == nil {
			err = proto.Unmarshal(buf.ReadOnlyData(), p)
		}
	}
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
	detailed, e := s.WithDetails(&errdetails.ErrorInfo{Reason: code, Domain: "omniview.resource.v2"})
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
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.Domain == "omniview.resource.v2" {
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
