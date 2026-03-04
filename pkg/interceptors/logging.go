package interceptors

import (
	"context"
	"time"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// UnaryLogging returns a unary interceptor that logs each RPC call with method, duration, and error.
func UnaryLogging(log logging.Logger) grpc.UnaryServerInterceptor {
	if log == nil {
		log = logging.Default()
	}
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		duration := time.Since(start)
		if err != nil {
			st, _ := status.FromError(err)
			log.Errorw(ctx, "rpc call completed with error",
				"method", info.FullMethod,
				"duration", duration.String(),
				"grpc_code", st.Code().String(),
				"error", err,
			)
		} else {
			log.Debugw(ctx, "rpc call completed",
				"method", info.FullMethod,
				"duration", duration.String(),
			)
		}
		return resp, err
	}
}

// StreamLogging returns a stream interceptor that logs stream open/close with method and duration.
func StreamLogging(log logging.Logger) grpc.StreamServerInterceptor {
	if log == nil {
		log = logging.Default()
	}
	return func(
		srv interface{},
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		start := time.Now()
		log.Debugw(ss.Context(), "stream opened", "method", info.FullMethod)
		err := handler(srv, ss)
		duration := time.Since(start)
		if err != nil {
			st, _ := status.FromError(err)
			log.Errorw(ss.Context(), "stream closed with error",
				"method", info.FullMethod,
				"duration", duration.String(),
				"grpc_code", st.Code().String(),
				"error", err,
			)
		} else {
			log.Debugw(ss.Context(), "stream closed",
				"method", info.FullMethod,
				"duration", duration.String(),
			)
		}
		return err
	}
}
