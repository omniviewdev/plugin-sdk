package interceptors

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	grpcmd "google.golang.org/grpc/metadata"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// helper: build a context with the given plugin_context metadata value.
func ctxWithPluginContextMD(serialized string) context.Context {
	md := grpcmd.Pairs("plugin_context", serialized)
	return grpcmd.NewIncomingContext(context.Background(), md)
}

// ---------------------------------------------------------------------------
// UnaryPluginContext
// ---------------------------------------------------------------------------

func TestUnaryPluginContext_ValidMetadata(t *testing.T) {
	interceptor := UnaryPluginContext()
	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/CtxOK"}

	// Build a real PluginContext and serialize it.
	pc := &types.PluginContext{
		RequestID:   "req-123",
		RequesterID: "user-456",
	}
	serialized, err := types.SerializePluginContext(pc)
	require.NoError(t, err)

	ctx := ctxWithPluginContextMD(serialized)

	var captured context.Context
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		captured = ctx
		return "ok", nil
	}

	resp, err := interceptor(ctx, "req", info, handler)

	require.NoError(t, err)
	assert.Equal(t, "ok", resp)

	// The handler's context should contain the deserialized PluginContext.
	extracted := types.PluginContextFromContext(captured)
	require.NotNil(t, extracted, "PluginContext should be present in handler context")
	assert.Equal(t, "req-123", extracted.RequestID)
	assert.Equal(t, "user-456", extracted.RequesterID)
}

func TestUnaryPluginContext_NoMetadata(t *testing.T) {
	interceptor := UnaryPluginContext()
	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/NoMD"}

	called := false
	handler := func(_ context.Context, _ interface{}) (interface{}, error) {
		called = true
		return "ok", nil
	}

	// Plain context with no gRPC metadata at all.
	resp, err := interceptor(context.Background(), "req", info, handler)

	require.NoError(t, err)
	assert.Equal(t, "ok", resp)
	assert.True(t, called, "handler should still be called when metadata is absent")
}

func TestUnaryPluginContext_InvalidMetadata(t *testing.T) {
	interceptor := UnaryPluginContext()
	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/BadMD"}

	ctx := ctxWithPluginContextMD("%%%not-json%%%")

	called := false
	handler := func(_ context.Context, _ interface{}) (interface{}, error) {
		called = true
		return "ok", nil
	}

	// Should gracefully degrade: log the error but still call the handler.
	resp, err := interceptor(ctx, "req", info, handler)

	require.NoError(t, err)
	assert.Equal(t, "ok", resp)
	assert.True(t, called, "handler should still be called even with invalid metadata")
}

// ---------------------------------------------------------------------------
// StreamPluginContext
// ---------------------------------------------------------------------------

func TestStreamPluginContext_ValidMetadata(t *testing.T) {
	interceptor := StreamPluginContext()
	info := &grpc.StreamServerInfo{FullMethod: "/test.Service/StreamCtxOK"}

	pc := &types.PluginContext{
		RequestID:   "stream-req-1",
		RequesterID: "stream-user-1",
	}
	serialized, err := types.SerializePluginContext(pc)
	require.NoError(t, err)

	ctx := ctxWithPluginContextMD(serialized)
	ss := &mockServerStream{ctx: ctx}

	var capturedCtx context.Context
	handler := func(_ interface{}, stream grpc.ServerStream) error {
		capturedCtx = stream.Context()
		return nil
	}

	err = interceptor(nil, ss, info, handler)

	require.NoError(t, err)
	extracted := types.PluginContextFromContext(capturedCtx)
	require.NotNil(t, extracted, "PluginContext should be present in stream context")
	assert.Equal(t, "stream-req-1", extracted.RequestID)
	assert.Equal(t, "stream-user-1", extracted.RequesterID)
}

func TestStreamPluginContext_NoMetadata(t *testing.T) {
	interceptor := StreamPluginContext()
	info := &grpc.StreamServerInfo{FullMethod: "/test.Service/StreamNoMD"}

	ss := &mockServerStream{ctx: context.Background()}

	called := false
	handler := func(_ interface{}, _ grpc.ServerStream) error {
		called = true
		return nil
	}

	err := interceptor(nil, ss, info, handler)
	require.NoError(t, err)
	assert.True(t, called, "handler should still be called when metadata is absent")
}

func TestStreamPluginContext_InvalidMetadata(t *testing.T) {
	interceptor := StreamPluginContext()
	info := &grpc.StreamServerInfo{FullMethod: "/test.Service/StreamBadMD"}

	ctx := ctxWithPluginContextMD("{invalid json")
	ss := &mockServerStream{ctx: ctx}

	called := false
	handler := func(_ interface{}, _ grpc.ServerStream) error {
		called = true
		return nil
	}

	err := interceptor(nil, ss, info, handler)
	require.NoError(t, err)
	assert.True(t, called, "handler should still be called even with invalid metadata")
}

func TestStreamPluginContext_WrappedStreamOverridesContext(t *testing.T) {
	interceptor := StreamPluginContext()
	info := &grpc.StreamServerInfo{FullMethod: "/test.Service/StreamWrapped"}

	pc := &types.PluginContext{
		RequestID: "wrapped-req",
	}
	serialized, err := types.SerializePluginContext(pc)
	require.NoError(t, err)

	ctx := ctxWithPluginContextMD(serialized)
	ss := &mockServerStream{ctx: ctx}

	handler := func(_ interface{}, stream grpc.ServerStream) error {
		// The stream passed to the handler should be a wrappedServerStream,
		// not the original mock. Verify context has the plugin context.
		extracted := types.PluginContextFromContext(stream.Context())
		require.NotNil(t, extracted)
		assert.Equal(t, "wrapped-req", extracted.RequestID)
		return nil
	}

	err = interceptor(nil, ss, info, handler)
	require.NoError(t, err)
}
