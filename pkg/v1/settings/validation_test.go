package settings

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	settingspb "github.com/omniviewdev/plugin-sdk/proto/v1/settings"
	base "github.com/omniviewdev/plugin-sdk/settings"
)

func TestValidationRPCExecutesRegisteredCallbacksWithoutMutation(t *testing.T) {
	provider := base.NewProvider(
		base.ProviderOpts{
			PluginID: "fixture",
			PluginSettings: []base.Category{{ID: "plugin", Settings: map[string]base.Setting{
				"tool": {
					ID:      "tool",
					Type:    base.Text,
					Default: "saved",
					Validator: func(value any) base.ValidationResult {
						if value == "empty-message" {
							return base.ValidationResult{Code: "tool_failed"}
						}
						if value == "bad" {
							return base.ValidationResult{Code: "tool_failed", Message: "Select a working tool."}
						}
						return base.ValidationResult{Valid: true, Code: "tool_ready", Message: "Tool is ready."}
					},
				},
				"secret": {
					ID:        "secret",
					Type:      base.Text,
					Default:   "",
					Sensitive: true,
					Validator: func(value any) base.ValidationResult {
						return base.ValidationResult{Code: "private", Message: fmt.Sprint(value)}
					},
				},
			}}},
		},
	)
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	settingspb.RegisterSettingsPluginServer(server, &SettingsPluginServer{Impl: NewProviderWrapper(provider)})
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
		if serveErr := <-done; serveErr != nil && !errors.Is(serveErr, grpc.ErrServerStopped) {
			t.Errorf("owned validation RPC server: %v", serveErr)
		}
	})
	connection, err := grpc.NewClient(
		"passthrough:///validation",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	waitForSettingsConnection(t, connection)
	client := &Client{client: settingspb.NewSettingsPluginClient(connection)}
	result, err := client.ValidateSetting("tool", "good")
	require.NoError(t, err)
	require.True(t, result.Valid)
	require.Equal(t, "tool_ready", result.Code)
	result, err = client.ValidateSetting("tool", "bad")
	require.NoError(t, err)
	require.False(t, result.Valid)
	require.Equal(t, "tool_failed", result.Code)
	require.Equal(t, "Select a working tool.", result.Message)
	result, err = client.ValidateSetting("tool", "empty-message")
	require.NoError(t, err)
	require.False(t, result.Valid)
	require.Equal(t, "tool_failed", result.Code)
	require.Equal(t, "Value did not pass validation.", result.Message)
	require.ErrorContains(t, client.SetSettings(map[string]any{"tool": "empty-message"}), result.Message)
	value, err := provider.GetString("tool")
	require.NoError(t, err)
	require.Equal(t, "saved", value)
	result, err = client.ValidateSetting("secret", "private-input")
	require.NoError(t, err)
	require.False(t, result.Valid)
	require.NotContains(t, result.Message, "private-input")
	require.NotEqual(t, "private", result.Code)
	require.Error(t, client.SetSettings(map[string]any{"tool": "bad"}))
	value, err = provider.GetString("tool")
	require.NoError(t, err)
	require.Equal(t, "saved", value)
	_, err = client.ValidateSetting("missing", "value")
	require.Error(t, err)
}
