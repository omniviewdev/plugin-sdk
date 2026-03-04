package interceptors

import (
	"context"
	"runtime/debug"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// UnaryPanicRecovery returns a unary server interceptor that recovers from panics,
// logs the stack trace, and returns a codes.Internal gRPC error.
func UnaryPanicRecovery(log logging.Logger) grpc.UnaryServerInterceptor {
	if log == nil {
		log = logging.Default()
	}
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (resp interface{}, err error) {
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				log.Errorw(ctx, "panic recovered in unary rpc",
					"method", info.FullMethod,
					"panic", r,
					"stack", string(stack),
				)
				err = status.Errorf(codes.Internal, "panic in %s: %v", info.FullMethod, r)
			}
		}()
		return handler(ctx, req)
	}
}

// StreamPanicRecovery returns a stream server interceptor that recovers from panics,
// logs the stack trace, and returns a codes.Internal gRPC error.
func StreamPanicRecovery(log logging.Logger) grpc.StreamServerInterceptor {
	if log == nil {
		log = logging.Default()
	}
	return func(
		srv interface{},
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) (err error) {
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				log.Errorw(ss.Context(), "panic recovered in stream rpc",
					"method", info.FullMethod,
					"panic", r,
					"stack", string(stack),
				)
				err = status.Errorf(codes.Internal, "panic in %s: %v", info.FullMethod, r)
			}
		}()
		return handler(srv, ss)
	}
}
