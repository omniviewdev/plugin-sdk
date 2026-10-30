package tools

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/pkg/interceptors"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	toolspb "github.com/omniviewdev/plugin-sdk/proto/v1/tools"
)

// connectionEcho returns the connection ID seen by the handler so the test
// proves the connection crosses the gRPC boundary.
func connectionEcho(ctx *types.PluginContext, arguments json.RawMessage) (*Result, error) {
	return JSONResult(map[string]any{
		"connection": ctx.Connection.ID,
		"arguments":  arguments,
	})
}

func newWireClient(t *testing.T, impl Provider) *PluginClient {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer(grpc.ChainUnaryInterceptor(interceptors.UnaryPluginContext()))
	toolspb.RegisterToolsPluginServer(server, &PluginServer{Impl: impl})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, "passthrough:///tools-contract", //nolint:staticcheck // bufconn test dial
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithChainUnaryInterceptor(sdk.ClientPluginContextInterceptor(logging.NewNop())),
		grpc.WithBlock(), //nolint:staticcheck // bufconn test dial
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return &PluginClient{client: toolspb.NewToolsPluginClient(conn), log: logging.NewNop()}
}

func TestWireRoundTrip(t *testing.T) {
	manager, err := NewManager(logging.NewNop(), PluginOpts{Tools: []Tool{
		{
			Descriptor: Descriptor{
				Name:         "echo",
				Title:        "Echo",
				Description:  "echoes the connection",
				InputSchema:  ObjectSchema(map[string]any{"x": map[string]any{"type": "integer"}}, "x"),
				OutputSchema: json.RawMessage(`{"type":"object"}`),
				Scope:        ScopeConnection,
				Annotations:  Annotations{ReadOnly: boolPointer(true), Idempotent: boolPointer(true)},
			},
			Handler: connectionEcho,
		},
		{
			Descriptor: Descriptor{
				Name:        "fail",
				InputSchema: ObjectSchema(nil),
				Scope:       ScopePlugin,
				Annotations: Annotations{Destructive: boolPointer(true)},
			},
			Handler: func(*types.PluginContext, json.RawMessage) (*Result, error) {
				return ErrorResult("nope"), nil
			},
		},
	}})
	require.NoError(t, err)
	manager.BindConnectionContextProvider(toolRegistry(t, nil))
	client := newWireClient(t, manager)

	descriptors, err := client.ListTools(types.NewPluginContext(context.Background(), "test", nil, nil, nil))
	require.NoError(t, err)
	require.Len(t, descriptors, 2)
	require.Equal(t, "echo", descriptors[0].Name)
	require.Equal(t, "Echo", descriptors[0].Title)
	require.Equal(t, ScopeConnection, descriptors[0].Scope)
	require.True(t, descriptors[0].Annotations.Resolve().ReadOnly)
	require.True(t, descriptors[0].Annotations.Resolve().Idempotent)
	require.JSONEq(t, `{"type":"object"}`, string(descriptors[0].OutputSchema))
	require.JSONEq(
		t,
		string(ObjectSchema(map[string]any{"x": map[string]any{"type": "integer"}}, "x")),
		string(descriptors[0].InputSchema),
	)
	require.Equal(t, ScopePlugin, descriptors[1].Scope)
	require.True(t, descriptors[1].Annotations.Resolve().Destructive)
	require.Empty(t, descriptors[1].OutputSchema)

	ctx := types.NewPluginContextWithConnection(context.Background(), "test", nil, nil, &types.Connection{ID: "conn-1"})
	res, err := client.CallTool(
		ctx,
		CallInput{
			Name:      "echo",
			Arguments: json.RawMessage(`{"x":7}`),
			Route:     &resourcev2.AccessRoute{Connection: "conn-1"},
		},
	)
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.JSONEq(t, `{"connection":"conn-1","arguments":{"x":7}}`, string(res.StructuredContent))
	require.Contains(t, res.Content[0].Text, `"connection": "conn-1"`)

	res, err = client.CallTool(
		types.NewPluginContext(context.Background(), "test", nil, nil, nil),
		CallInput{Name: "fail", Arguments: json.RawMessage(`{}`)},
	)
	require.NoError(t, err)
	require.True(t, res.IsError)
	require.Equal(t, "Tool execution failed.", res.Content[0].Text)
	require.Empty(t, res.StructuredContent)
}

func TestWireErrorCodes(t *testing.T) {
	manager, err := NewManager(logging.NewNop(), PluginOpts{Tools: []Tool{validTool("scoped", ScopeConnection)}})
	require.NoError(t, err)
	client := newWireClient(t, manager)

	_, err = client.CallTool(
		types.NewPluginContext(context.Background(), "test", nil, nil, nil),
		CallInput{Name: "scoped", Arguments: json.RawMessage(`{}`)},
	)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))

	_, err = client.CallTool(
		types.NewPluginContext(context.Background(), "test", nil, nil, nil),
		CallInput{Name: "missing", Arguments: json.RawMessage(`{}`)},
	)
	require.ErrorIs(t, err, ErrToolNotFound)
}

func boolPointer(v bool) *bool { return &v }
