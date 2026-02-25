package interceptors

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// UnaryLogging
// ---------------------------------------------------------------------------

func TestUnaryLogging_SuccessPassthrough(t *testing.T) {
	interceptor := UnaryLogging()
	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/LogOK"}

	expected := "result"
	handler := func(_ context.Context, _ interface{}) (interface{}, error) {
		return expected, nil
	}

	resp, err := interceptor(context.Background(), "req", info, handler)

	require.NoError(t, err)
	assert.Equal(t, expected, resp, "response should pass through unchanged")
}

func TestUnaryLogging_ErrorPassthrough(t *testing.T) {
	interceptor := UnaryLogging()
	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/LogFail"}

	handlerErr := status.Error(codes.InvalidArgument, "bad arg")
	handler := func(_ context.Context, _ interface{}) (interface{}, error) {
		return nil, handlerErr
	}

	resp, err := interceptor(context.Background(), "req", info, handler)

	assert.Nil(t, resp)
	require.Error(t, err)
	assert.Equal(t, handlerErr, err, "error should not be swallowed or modified")

	st, _ := status.FromError(err)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestUnaryLogging_HandlerIsCalled(t *testing.T) {
	interceptor := UnaryLogging()
	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/Called"}

	called := false
	handler := func(_ context.Context, _ interface{}) (interface{}, error) {
		called = true
		return nil, nil
	}

	_, _ = interceptor(context.Background(), "req", info, handler)
	assert.True(t, called, "handler should be invoked by the logging interceptor")
}

// ---------------------------------------------------------------------------
// StreamLogging
// ---------------------------------------------------------------------------

func TestStreamLogging_SuccessPassthrough(t *testing.T) {
	interceptor := StreamLogging()
	info := &grpc.StreamServerInfo{FullMethod: "/test.Service/StreamLogOK"}

	handler := func(_ interface{}, _ grpc.ServerStream) error {
		return nil
	}

	err := interceptor(nil, &mockServerStream{ctx: context.Background()}, info, handler)
	assert.NoError(t, err)
}

func TestStreamLogging_ErrorPassthrough(t *testing.T) {
	interceptor := StreamLogging()
	info := &grpc.StreamServerInfo{FullMethod: "/test.Service/StreamLogFail"}

	handlerErr := status.Error(codes.Unavailable, "unavailable")
	handler := func(_ interface{}, _ grpc.ServerStream) error {
		return handlerErr
	}

	err := interceptor(nil, &mockServerStream{ctx: context.Background()}, info, handler)

	require.Error(t, err)
	assert.Equal(t, handlerErr, err, "stream error should pass through unchanged")
}

func TestStreamLogging_HandlerIsCalled(t *testing.T) {
	interceptor := StreamLogging()
	info := &grpc.StreamServerInfo{FullMethod: "/test.Service/StreamCalled"}

	called := false
	handler := func(_ interface{}, _ grpc.ServerStream) error {
		called = true
		return nil
	}

	_ = interceptor(nil, &mockServerStream{ctx: context.Background()}, info, handler)
	assert.True(t, called, "stream handler should be invoked by the logging interceptor")
}
