package interceptors

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// UnaryPanicRecovery
// ---------------------------------------------------------------------------

func TestUnaryPanicRecovery_PanicIsRecovered(t *testing.T) {
	interceptor := UnaryPanicRecovery()
	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/PanicMethod"}

	handler := func(_ context.Context, _ interface{}) (interface{}, error) {
		panic("something went wrong")
	}

	resp, err := interceptor(context.Background(), "req", info, handler)

	require.Error(t, err, "should return an error when handler panics")
	assert.Nil(t, resp, "response should be nil on panic")

	st, ok := status.FromError(err)
	require.True(t, ok, "error should be a gRPC status")
	assert.Equal(t, codes.Internal, st.Code(), "error code should be Internal")
	assert.Contains(t, st.Message(), "panic in /test.Service/PanicMethod", "error message should mention the method")
	assert.Contains(t, st.Message(), "something went wrong", "error message should contain the panic value")
}

func TestUnaryPanicRecovery_SuccessPassthrough(t *testing.T) {
	interceptor := UnaryPanicRecovery()
	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/OK"}

	expected := "hello"
	handler := func(_ context.Context, _ interface{}) (interface{}, error) {
		return expected, nil
	}

	resp, err := interceptor(context.Background(), "req", info, handler)

	require.NoError(t, err)
	assert.Equal(t, expected, resp, "response should pass through unmodified")
}

func TestUnaryPanicRecovery_ErrorPassthrough(t *testing.T) {
	interceptor := UnaryPanicRecovery()
	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/Fail"}

	handlerErr := status.Error(codes.NotFound, "not found")
	handler := func(_ context.Context, _ interface{}) (interface{}, error) {
		return nil, handlerErr
	}

	resp, err := interceptor(context.Background(), "req", info, handler)

	assert.Nil(t, resp)
	require.Error(t, err)
	assert.Equal(t, handlerErr, err, "error should pass through unmodified")

	st, _ := status.FromError(err)
	assert.Equal(t, codes.NotFound, st.Code(), "error code should remain NotFound")
}

// ---------------------------------------------------------------------------
// StreamPanicRecovery
// ---------------------------------------------------------------------------

func TestStreamPanicRecovery_PanicIsRecovered(t *testing.T) {
	interceptor := StreamPanicRecovery()
	info := &grpc.StreamServerInfo{FullMethod: "/test.Service/StreamPanic"}

	handler := func(_ interface{}, _ grpc.ServerStream) error {
		panic("stream boom")
	}

	err := interceptor(nil, &mockServerStream{ctx: context.Background()}, info, handler)

	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok, "error should be a gRPC status")
	assert.Equal(t, codes.Internal, st.Code())
	assert.Contains(t, st.Message(), "panic in /test.Service/StreamPanic")
	assert.Contains(t, st.Message(), "stream boom")
}

func TestStreamPanicRecovery_SuccessPassthrough(t *testing.T) {
	interceptor := StreamPanicRecovery()
	info := &grpc.StreamServerInfo{FullMethod: "/test.Service/StreamOK"}

	handler := func(_ interface{}, _ grpc.ServerStream) error {
		return nil
	}

	err := interceptor(nil, &mockServerStream{ctx: context.Background()}, info, handler)
	assert.NoError(t, err)
}

func TestStreamPanicRecovery_ErrorPassthrough(t *testing.T) {
	interceptor := StreamPanicRecovery()
	info := &grpc.StreamServerInfo{FullMethod: "/test.Service/StreamFail"}

	handlerErr := errors.New("stream handler error")
	handler := func(_ interface{}, _ grpc.ServerStream) error {
		return handlerErr
	}

	err := interceptor(nil, &mockServerStream{ctx: context.Background()}, info, handler)

	require.Error(t, err)
	assert.Equal(t, handlerErr, err, "error should pass through unmodified")
}
