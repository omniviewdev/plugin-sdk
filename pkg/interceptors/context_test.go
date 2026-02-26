package interceptors

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	grpcmd "google.golang.org/grpc/metadata"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// helper: build an incoming context with the given metadata pairs.
func ctxWithMD(pairs ...string) context.Context {
	md := grpcmd.Pairs(pairs...)
	return grpcmd.NewIncomingContext(context.Background(), md)
}

// ---------------------------------------------------------------------------
// Connection data round-trip tests
// ---------------------------------------------------------------------------

func TestConnectionDataRoundTrip(t *testing.T) {
	data := map[string]any{"foo": "bar", "count": float64(42)}
	encoded, err := json.Marshal(data)
	require.NoError(t, err)

	ctx := ctxWithMD(
		mdKeyRequestID, "req-1",
		mdKeyRequesterID, "user-1",
		mdKeyConnectionID, "conn-1",
		mdKeyConnectionData, string(encoded),
		mdKeyProtocolVersion, "2",
	)

	ctx, err = useServerPluginContext(ctx)
	require.NoError(t, err)

	pc := types.PluginContextFromContext(ctx)
	require.NotNil(t, pc)
	assert.Equal(t, "req-1", pc.RequestID)
	assert.Equal(t, "user-1", pc.RequesterID)
	require.NotNil(t, pc.Connection)
	assert.Equal(t, "conn-1", pc.Connection.ID)
	require.NotNil(t, pc.Connection.Data)
	assert.Equal(t, "bar", pc.Connection.Data["foo"])
	assert.Equal(t, float64(42), pc.Connection.Data["count"])
}

func TestConnectionDataWithKubeconfig(t *testing.T) {
	data := map[string]any{"kubeconfig": "/home/user/.kube/config"}
	encoded, err := json.Marshal(data)
	require.NoError(t, err)

	ctx := ctxWithMD(
		mdKeyRequestID, "req-2",
		mdKeyConnectionID, "ctx-kube",
		mdKeyConnectionData, string(encoded),
	)

	ctx, err = useServerPluginContext(ctx)
	require.NoError(t, err)

	pc := types.PluginContextFromContext(ctx)
	require.NotNil(t, pc)
	require.NotNil(t, pc.Connection)
	require.NotNil(t, pc.Connection.Data)
	assert.Equal(t, "/home/user/.kube/config", pc.Connection.Data["kubeconfig"])
}

func TestNilConnectionData(t *testing.T) {
	ctx := ctxWithMD(
		mdKeyRequestID, "req-3",
		mdKeyConnectionID, "conn-nodata",
	)

	ctx, err := useServerPluginContext(ctx)
	require.NoError(t, err)

	pc := types.PluginContextFromContext(ctx)
	require.NotNil(t, pc)
	require.NotNil(t, pc.Connection)
	assert.Equal(t, "conn-nodata", pc.Connection.ID)
	assert.Nil(t, pc.Connection.Data)
}

func TestNoConnection(t *testing.T) {
	ctx := ctxWithMD(
		mdKeyRequestID, "req-4",
		mdKeyRequesterID, "user-4",
	)

	ctx, err := useServerPluginContext(ctx)
	require.NoError(t, err)

	pc := types.PluginContextFromContext(ctx)
	require.NotNil(t, pc)
	assert.Equal(t, "req-4", pc.RequestID)
	assert.Nil(t, pc.Connection)
}

func TestEmptyRequestID(t *testing.T) {
	// No metadata at all — should return original context with no error.
	ctx, err := useServerPluginContext(context.Background())
	require.NoError(t, err)

	pc := types.PluginContextFromContext(ctx)
	assert.Nil(t, pc)
}

func TestInvalidConnectionData(t *testing.T) {
	ctx := ctxWithMD(
		mdKeyRequestID, "req-5",
		mdKeyConnectionID, "conn-bad",
		mdKeyConnectionData, "%%%not-json%%%",
	)

	_, err := useServerPluginContext(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode connection data")
}

// ---------------------------------------------------------------------------
// UnaryPluginContext interceptor tests
// ---------------------------------------------------------------------------

func TestUnaryPluginContext_ValidMetadata(t *testing.T) {
	interceptor := UnaryPluginContext()
	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/CtxOK"}

	ctx := ctxWithMD(
		mdKeyRequestID, "req-123",
		mdKeyRequesterID, "user-456",
		mdKeyProtocolVersion, "2",
	)

	var captured context.Context
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		captured = ctx
		return "ok", nil
	}

	resp, err := interceptor(ctx, "req", info, handler)

	require.NoError(t, err)
	assert.Equal(t, "ok", resp)

	extracted := types.PluginContextFromContext(captured)
	require.NotNil(t, extracted)
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

	resp, err := interceptor(context.Background(), "req", info, handler)

	require.NoError(t, err)
	assert.Equal(t, "ok", resp)
	assert.True(t, called)
}

// ---------------------------------------------------------------------------
// StreamPluginContext interceptor tests
// ---------------------------------------------------------------------------

func TestStreamPluginContext_ValidMetadata(t *testing.T) {
	interceptor := StreamPluginContext()
	info := &grpc.StreamServerInfo{FullMethod: "/test.Service/StreamCtxOK"}

	data := map[string]any{"kubeconfig": "/path/to/kubeconfig"}
	encoded, _ := json.Marshal(data)

	ctx := ctxWithMD(
		mdKeyRequestID, "stream-req-1",
		mdKeyRequesterID, "stream-user-1",
		mdKeyConnectionID, "conn-stream",
		mdKeyConnectionData, string(encoded),
	)
	ss := &mockServerStream{ctx: ctx}

	var capturedCtx context.Context
	handler := func(_ interface{}, stream grpc.ServerStream) error {
		capturedCtx = stream.Context()
		return nil
	}

	err := interceptor(nil, ss, info, handler)

	require.NoError(t, err)
	extracted := types.PluginContextFromContext(capturedCtx)
	require.NotNil(t, extracted)
	assert.Equal(t, "stream-req-1", extracted.RequestID)
	assert.Equal(t, "stream-user-1", extracted.RequesterID)
	require.NotNil(t, extracted.Connection)
	assert.Equal(t, "/path/to/kubeconfig", extracted.Connection.Data["kubeconfig"])
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
	assert.True(t, called)
}

func TestStreamPluginContext_WrappedStreamOverridesContext(t *testing.T) {
	interceptor := StreamPluginContext()
	info := &grpc.StreamServerInfo{FullMethod: "/test.Service/StreamWrapped"}

	ctx := ctxWithMD(
		mdKeyRequestID, "wrapped-req",
		mdKeyRequesterID, "wrapped-user",
	)
	ss := &mockServerStream{ctx: ctx}

	handler := func(_ interface{}, stream grpc.ServerStream) error {
		extracted := types.PluginContextFromContext(stream.Context())
		require.NotNil(t, extracted)
		assert.Equal(t, "wrapped-req", extracted.RequestID)
		return nil
	}

	err := interceptor(nil, ss, info, handler)
	require.NoError(t, err)
}
