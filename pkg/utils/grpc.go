package utils

import (
	"google.golang.org/grpc"

	"github.com/omniviewdev/plugin-sdk/pkg/interceptors"
)

// RegisterServerOpts returns a list of gRPC server options with the necessary interceptors
// for the plugin SDK.
func RegisterServerOpts(opts []grpc.ServerOption) []grpc.ServerOption {
	unaryinterceptors, streaminterceptors := NewServerInterceptors()

	opts = append(opts, grpc.ChainUnaryInterceptor(unaryinterceptors...))
	opts = append(opts, grpc.ChainStreamInterceptor(streaminterceptors...))

	return opts
}

// NewServerInterceptors returns the default unary and stream interceptor chains
// for the plugin SDK gRPC server.
func NewServerInterceptors() ([]grpc.UnaryServerInterceptor, []grpc.StreamServerInterceptor) {
	return interceptors.DefaultUnaryInterceptors(), interceptors.DefaultStreamInterceptors()
}
