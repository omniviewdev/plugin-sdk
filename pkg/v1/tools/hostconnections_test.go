package tools

import (
	"context"
	"encoding/json"
	"testing"

	logging "github.com/omniviewdev/plugin-sdk/log"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/hostconnections"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestReviewedFailuresOverGRPC(t *testing.T) {
	cases := map[string]string{"GRANT_DENIED": "Cross-plugin connection access is not permitted.", "CONNECTION_NOT_STARTED": "The owner connection is not started.", "OWNER_UNAVAILABLE": "The connection owner is unavailable.", "DELEGATION_NOT_ACTIVE": "The delegated tool call is not active.", "CONFLICT": "The object changed during the update.", "TIMEOUT": "The operation timed out.", "UNSUPPORTED": "The operation is not supported."}
	for code, text := range cases {
		t.Run(code, func(t *testing.T) {
			manager, err := NewManager(logging.NewNop(), PluginOpts{Tools: []Tool{{Descriptor: Descriptor{Name: "failure", Scope: ScopePlugin, InputSchema: ObjectSchema(nil)}, Handler: func(*types.PluginContext, json.RawMessage) (*Result, error) {
				return nil, &resourcev2.ResourceOperationError{Code: code, Message: "secret", Err: context.DeadlineExceeded}
			}}}})
			require.NoError(t, err)
			result, err := newWireClient(t, manager).CallTool(&types.PluginContext{Context: context.Background()}, CallInput{Name: "failure", Arguments: json.RawMessage(`{}`)})
			require.NoError(t, err)
			require.True(t, result.IsError)
			require.Equal(t, code, result.ErrorCode)
			require.Equal(t, text, result.Content[0].Text)
			require.Empty(t, result.StructuredContent)
		})
	}
}
func TestOwnerToolRequiresBindingAndToken(t *testing.T) {
	called := false
	tool := Tool{Descriptor: Descriptor{Name: "borrowed", Scope: ScopeConnection, ConnectionOwner: "kubernetes", InputSchema: ObjectSchema(nil)}, Handler: func(*types.PluginContext, json.RawMessage) (*Result, error) {
		called = true
		return TextResult("unexpected"), nil
	}}
	manager, err := NewManager(logging.NewNop(), PluginOpts{Tools: []Tool{tool}})
	require.NoError(t, err)
	ctx := &types.PluginContext{Context: context.Background()}
	route := resourcev2.AccessRoute{Connection: "id"}
	_, err = manager.CallTool(ctx, CallInput{Name: "borrowed", Arguments: json.RawMessage(`{}`), Route: &route, DelegationToken: []byte("token")})
	require.Error(t, err)
	require.False(t, called)
	_, err = manager.CallTool(ctx, CallInput{Name: "borrowed", Arguments: json.RawMessage(`{}`), Route: &route})
	require.Error(t, err)
	require.False(t, called)
	tool.Scope = ScopePlugin
	_, err = NewManager(logging.NewNop(), PluginOpts{Tools: []Tool{tool}})
	require.Error(t, err)
}

func TestOwnerToolDoesNotLeaseLocalRegistry(t *testing.T) {
	conn, err := grpc.NewClient("passthrough:///unused", grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()
	binding := &hostconnections.Binding{}
	require.NoError(t, binding.Bind(conn))
	tool := Tool{Descriptor: Descriptor{Name: "borrowed", Scope: ScopeConnection, ConnectionOwner: "owner", InputSchema: ObjectSchema(nil)}, Handler: func(pc *types.PluginContext, _ json.RawMessage) (*Result, error) {
		_, err := hostconnections.FromContext(pc.Context)
		require.NoError(t, err)
		require.Equal(t, "id", pc.Connection.ID)
		return TextResult("ok"), nil
	}}
	manager, err := NewManager(logging.NewNop(), PluginOpts{Tools: []Tool{tool}})
	require.NoError(t, err)
	manager.BindHostConnections(binding)
	route := resourcev2.AccessRoute{Connection: "id"}
	result, err := manager.CallTool(&types.PluginContext{Context: context.Background()}, CallInput{Name: "borrowed", Arguments: json.RawMessage(`{}`), Route: &route, DelegationToken: []byte("token")})
	require.NoError(t, err)
	require.Equal(t, "ok", result.Content[0].Text)
}
