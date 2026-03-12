package utils

import (
	logging "github.com/omniviewdev/plugin-sdk/log"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"

	"github.com/omniviewdev/plugin-sdk/pkg/interceptors"
)

// RegisterServerOpts returns a list of gRPC server options with the necessary interceptors
// for the plugin SDK.
func RegisterServerOpts(opts []grpc.ServerOption, log logging.Logger) []grpc.ServerOption {
	unaryinterceptors, streaminterceptors := NewServerInterceptors(log)

	opts = append(opts, grpc.StatsHandler(otelgrpc.NewServerHandler()))
	opts = append(opts, grpc.ChainUnaryInterceptor(unaryinterceptors...))
	opts = append(opts, grpc.ChainStreamInterceptor(streaminterceptors...))

	return opts
}

// NewServerInterceptors returns the default unary and stream interceptor chains
// for the plugin SDK gRPC server.
func NewServerInterceptors(log logging.Logger) ([]grpc.UnaryServerInterceptor, []grpc.StreamServerInterceptor) {
	return interceptors.DefaultUnaryInterceptors(log), interceptors.DefaultStreamInterceptors(log)
}
