package interceptors

import (
	"google.golang.org/grpc"

	logging "github.com/omniviewdev/plugin-sdk/log"
)

// DefaultUnaryInterceptors returns the standard interceptor chain for unary RPCs.
// Order: recovery (outermost) → logging → context (innermost, closest to handler).
func DefaultUnaryInterceptors(log logging.Logger) []grpc.UnaryServerInterceptor {
	return []grpc.UnaryServerInterceptor{
		UnaryPanicRecovery(log),
		UnaryLogging(log),
		UnaryPluginContext(),
	}
}

// DefaultStreamInterceptors returns the standard interceptor chain for stream RPCs.
// Order: recovery (outermost) → logging → context (innermost, closest to handler).
func DefaultStreamInterceptors(log logging.Logger) []grpc.StreamServerInterceptor {
	return []grpc.StreamServerInterceptor{
		StreamPanicRecovery(log),
		StreamLogging(log),
		StreamPluginContext(),
	}
}
