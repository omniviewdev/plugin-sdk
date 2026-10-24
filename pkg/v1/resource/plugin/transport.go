package plugin

import (
	"context"
	"errors"
	"unicode/utf8"

	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
	commonpb "github.com/omniviewdev/plugin-sdk/proto/v1/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const requestIDKey = "omniview-request-id"
const requesterIDKey = "omniview-requester-id"

// Metadata is diagnostic only; it does not carry an authenticated host principal.
func outgoingSession(ctx context.Context) context.Context {
	session := resource.SessionFromContext(ctx)
	if session == nil {
		return ctx
	}
	md, _ := metadata.FromOutgoingContext(ctx)
	md = md.Copy()
	md.Set(requestIDKey, session.RequestID)
	md.Set(requesterIDKey, session.RequesterID)
	return metadata.NewOutgoingContext(ctx, md)
}
func incomingSession(ctx context.Context) (*resource.Session, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	session := &resource.Session{}
	for _, field := range []struct {
		key string
		out *string
	}{{requestIDKey, &session.RequestID}, {requesterIDKey, &session.RequesterID}} {
		values := md.Get(field.key)
		if len(values) > 1 || (len(values) == 1 && (len(values[0]) > 256 || !utf8.ValidString(values[0]))) {
			return nil, &resource.ResourceOperationError{Code: "INVALID_REQUEST", Title: "Invalid request", Message: "Invalid diagnostic metadata."}
		}
		if len(values) == 1 {
			*field.out = values[0]
		}
	}
	return session, nil
}

func structuredError(err error) *resource.ResourceOperationError {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return &resource.ResourceOperationError{Err: err, Code: "CANCELLED", Title: "Cancelled", Message: "The operation was cancelled."}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &resource.ResourceOperationError{Err: err, Code: "TIMEOUT", Title: "Timed out", Message: "The operation exceeded its deadline."}
	}
	var roe *resource.ResourceOperationError
	if errors.As(err, &roe) {
		return roe
	}
	code := "PROVIDER_ERROR"
	switch status.Code(err) {
	case codes.Canceled:
		code = "CANCELLED"
	case codes.DeadlineExceeded:
		code = "TIMEOUT"
	case codes.PermissionDenied:
		code = "FORBIDDEN"
	case codes.Unauthenticated:
		code = "UNAUTHORIZED"
	case codes.InvalidArgument:
		code = "INVALID_REQUEST"
	case codes.Unimplemented:
		code = "UNSUPPORTED"
	case codes.NotFound:
		code = "NOT_FOUND"
	case codes.AlreadyExists:
		code = "ALREADY_EXISTS"
	case codes.Aborted:
		code = "CONFLICT"
	case codes.ResourceExhausted:
		code = "RESOURCE_LIMIT"
	case codes.Unavailable:
		code = "UNAVAILABLE"
	case codes.Internal:
		code = "INTERNAL"
	}
	return &resource.ResourceOperationError{Err: err, Code: code, Title: "Provider operation failed", Message: "The provider could not complete the operation."}
}
func grpcCode(code string) codes.Code {
	switch code {
	case "FORBIDDEN", "PERMISSION_DENIED":
		return codes.PermissionDenied
	case "UNAUTHORIZED":
		return codes.Unauthenticated
	case "INVALID_REQUEST", "INVALID_INPUT", "FILTER_UNKNOWN_FIELD", "FILTER_INVALID_OPERATOR":
		return codes.InvalidArgument
	case "UNSUPPORTED":
		return codes.Unimplemented
	case "NOT_FOUND":
		return codes.NotFound
	case "ALREADY_EXISTS":
		return codes.AlreadyExists
	case "CONFLICT":
		return codes.Aborted
	case "CANCELLED":
		return codes.Canceled
	case "TIMEOUT":
		return codes.DeadlineExceeded
	case "RESOURCE_LIMIT":
		return codes.ResourceExhausted
	case "UNAVAILABLE", "CONNECTION_ERROR", "CERTIFICATE_ERROR":
		return codes.Unavailable
	case "CONTINUATION_EXPIRED", "RESET_REQUIRED", "PARTIAL_RESULT":
		return codes.FailedPrecondition
	default:
		return codes.Internal
	}
}
func encodeRPCError(err error) error {
	if err == nil {
		return nil
	}
	roe := structuredError(err)
	st := status.New(grpcCode(roe.Code), "Resource operation failed.")
	withDetails, detailErr := st.WithDetails(resourceErrorToProto(roe))
	if detailErr != nil {
		return st.Err()
	}
	return withDetails.Err()
}
func decodeRPCError(err error) error {
	if err == nil {
		return nil
	}
	if st, ok := status.FromError(err); ok {
		for _, detail := range st.Details() {
			if re, ok := detail.(*commonpb.ResourceError); ok {
				out := resourceErrorFromProto(re)
				out.Err = err
				return out
			}
		}
		// No typed provider response: retain the actual transport status so the
		// host can distinguish process loss from a provider-declared outage.
		return status.Error(st.Code(), "Resource operation failed.")
	}
	return structuredError(err)
}
