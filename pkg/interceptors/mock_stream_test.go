package interceptors

import (
	"context"

	"google.golang.org/grpc/metadata"
)

// mockServerStream is a minimal grpc.ServerStream implementation for testing.
type mockServerStream struct {
	ctx context.Context
}

func (m *mockServerStream) SetHeader(metadata.MD) error  { return nil }
func (m *mockServerStream) SendHeader(metadata.MD) error  { return nil }
func (m *mockServerStream) SetTrailer(metadata.MD)        {}
func (m *mockServerStream) Context() context.Context       { return m.ctx }
func (m *mockServerStream) SendMsg(interface{}) error      { return nil }
func (m *mockServerStream) RecvMsg(interface{}) error      { return nil }
