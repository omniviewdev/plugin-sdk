package settings_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/settings"
	settingspb "github.com/omniviewdev/plugin-sdk/proto/v1/settings"
	base "github.com/omniviewdev/plugin-sdk/settings"
)

func commitClient(
	t *testing.T,
	server settingspb.SettingsPluginServer,
) (*settings.Client, settingspb.SettingsPluginClient) {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	rpc := grpc.NewServer()
	settingspb.RegisterSettingsPluginServer(rpc, server)
	done := make(chan error, 1)
	go func() { done <- rpc.Serve(listener) }()
	t.Cleanup(func() {
		rpc.Stop()
		_ = listener.Close()
		if err := <-done; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Errorf("owned commit server: %v", err)
		}
	})
	conn, err := grpc.NewClient(
		"passthrough:///commit",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	plugin := &settings.SettingsPlugin{}
	client, err := plugin.GRPCClient(context.Background(), nil, conn)
	require.NoError(t, err)
	typed, ok := client.(*settings.Client)
	require.True(t, ok)
	return typed, settingspb.NewSettingsPluginClient(conn)
}

func TestCommitPersistsBeforePublishAndValidatesOnce(t *testing.T) {
	calls := 0
	provider := commitProvider(base.Setting{
		ID: "name", Type: base.Text, Default: "old", RequiresRestart: true,
		Validator: func(any) base.ValidationResult {
			calls++
			return base.ValidationResult{Valid: true}
		},
	})
	client, _ := commitClient(t, &settings.SettingsPluginServer{Impl: settings.NewProviderWrapper(provider)})
	before := calls
	result, err := client.CommitSettings(
		context.Background(), map[string]any{"name": "new"}, func(values map[string]any) error {
			require.Equal(t, "new", values["name"])
			value, readErr := provider.GetSettingValue("name")
			require.NoError(t, readErr)
			require.Equal(t, "old", value)
			return nil
		},
	)
	require.NoError(t, err)
	require.Equal(t, base.CommitResult{Committed: true, Applied: true}, result)
	require.Equal(t, before+1, calls)
	value, err := provider.GetSettingValue("name")
	require.NoError(t, err)
	require.Equal(t, "new", value)
	require.True(t, client.ListSettings()["name"].RequiresRestart)
	failure := errors.New("disk unavailable")
	result, err = client.CommitSettings(
		context.Background(), map[string]any{"name": "rejected"},
		func(map[string]any) error { return failure },
	)
	require.ErrorIs(t, err, failure)
	require.False(t, result.Committed)
	// The following transaction also proves an aborted callback releases serialization.
	result, err = client.CommitSettings(context.Background(), map[string]any{"name": "final"}, nil)
	require.NoError(t, err)
	require.True(t, result.Applied)
}

func TestBaseCommitPersistenceResolvesScopedIDs(t *testing.T) {
	for _, plugin := range []bool{false, true} {
		id := "plugin.name"
		opts := base.ProviderOpts{
			PluginSettings: []base.Category{{
				ID: "plugin", Settings: map[string]base.Setting{
					"name": {ID: "name", Type: base.Text, Default: "old"},
				},
			}},
		}
		if plugin {
			opts.PluginID = "fixture"
			id = "name"
		}
		t.Run(id, func(t *testing.T) {
			provider := base.NewProvider(opts)
			var saved map[string]any
			result, err := provider.CommitSettings(context.Background(), map[string]any{id: "saved"}, func(values map[string]any) error {
				saved = values
				return nil
			})
			require.NoError(t, err)
			require.True(t, result.Applied)
			require.Equal(t, map[string]any{"plugin.name": "saved"}, saved)
			// Scoped storage can hydrate a fresh core provider without changing keys.
			opts.PluginID = ""
			restarted := base.NewProvider(opts)
			require.NoError(t, restarted.SetSettings(saved))
			value, err := restarted.GetString("plugin.name")
			require.NoError(t, err)
			require.Equal(t, "saved", value)
		})
	}
}

func TestPluginCommitPersistencePreservesOpaqueIDsForHydration(t *testing.T) {
	for _, id := range []string{"name", "limits.count", "plugin.name"} {
		t.Run(id, func(t *testing.T) {
			definition := base.Setting{ID: id, Type: base.Text, Default: "old"}
			provider := commitProvider(definition)
			client, _ := commitClient(t, &settings.SettingsPluginServer{Impl: settings.NewProviderWrapper(provider)})
			var saved map[string]any
			result, err := client.CommitSettings(context.Background(), map[string]any{id: "saved"}, func(values map[string]any) error {
				saved = values
				return nil
			})
			require.NoError(t, err)
			require.True(t, result.Applied)
			require.Equal(t, map[string]any{id: "saved"}, saved)
			require.Contains(t, client.ListSettings(), id)

			restarted := commitProvider(definition)
			restartedClient, _ := commitClient(t, &settings.SettingsPluginServer{Impl: settings.NewProviderWrapper(restarted)})
			result, err = restartedClient.CommitSettings(context.Background(), saved, nil)
			require.NoError(t, err)
			require.True(t, result.Applied)
			value, err := restartedClient.GetSettingValue(id)
			require.NoError(t, err)
			require.Equal(t, "saved", value)
		})
	}
}

type disconnectCommitServer struct {
	settingspb.UnimplementedSettingsPluginServer
}

func (*disconnectCommitServer) CommitSettings(stream settingspb.SettingsPlugin_CommitSettingsServer) error {
	request, err := stream.Recv()
	if err != nil {
		return err
	}
	prepared := &settingspb.CommitSettingsResponse{
		Phase: &settingspb.CommitSettingsResponse_Prepared{Prepared: request.GetProposal()},
	}
	if err = stream.Send(prepared); err != nil {
		return err
	}
	if _, err = stream.Recv(); err != nil {
		return err
	}
	return status.Error(codes.Unavailable, "disconnected after persistence")
}

func TestCommitReportsDurableDisconnectAsUncertainSuccess(t *testing.T) {
	client, _ := commitClient(t, &disconnectCommitServer{})
	persisted := false
	result, err := client.CommitSettings(
		context.Background(), map[string]any{"name": "new"}, func(map[string]any) error {
			persisted = true
			return nil
		},
	)
	require.NoError(t, err)
	require.True(t, persisted)
	require.Equal(t, base.CommitResult{Committed: true, Uncertain: true}, result)
}

func TestCanceledCommitReleasesProviderWithoutPublishing(t *testing.T) {
	provider := commitProvider(base.Setting{ID: "name", Type: base.Text, Default: "old"})
	client, wire := commitClient(t, &settings.SettingsPluginServer{Impl: settings.NewProviderWrapper(provider)})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stream, err := wire.CommitSettings(ctx)
	require.NoError(t, err)
	values := commitValues(t, "abandoned")
	require.NoError(t, stream.Send(&settingspb.CommitSettingsRequest{
		Phase: &settingspb.CommitSettingsRequest_Proposal{Proposal: values},
	}))
	_, err = stream.Recv()
	require.NoError(t, err)
	cancel()
	result, err := client.CommitSettings(
		context.Background(), map[string]any{"name": "final"}, func(map[string]any) error {
			value, readErr := provider.GetSettingValue("name")
			require.NoError(t, readErr)
			require.Equal(t, "old", value)
			return nil
		},
	)
	require.NoError(t, err)
	require.True(t, result.Applied)
}

func TestRestartMetadataRoundTrip(t *testing.T) {
	setting := base.Setting{
		ID: "name", Type: base.Text, Default: "old", Value: "new",
		RequiresRestart: true, PendingRestart: true,
	}
	decoded := settings.FromProtoSetting(settings.ToProtoSetting(setting))
	require.True(t, decoded.RequiresRestart)
	require.True(t, decoded.PendingRestart)
}

func TestQueuedCanceledCommitReturnsBeforeActiveTransactionEnds(t *testing.T) {
	provider := commitProvider(base.Setting{ID: "name", Type: base.Text, Default: "old"})
	client, wire := commitClient(t, &settings.SettingsPluginServer{Impl: settings.NewProviderWrapper(provider)})
	activeCtx, cancelActive := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelActive()
	active, err := wire.CommitSettings(activeCtx)
	require.NoError(t, err)
	values := commitValues(t, "active")
	require.NoError(t, active.Send(&settingspb.CommitSettingsRequest{
		Phase: &settingspb.CommitSettingsRequest_Proposal{Proposal: values},
	}))
	_, err = active.Recv()
	require.NoError(t, err)
	queuedCtx, cancelQueued := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelQueued()
	persisted := false
	result, err := client.CommitSettings(queuedCtx, map[string]any{"name": "queued"}, func(map[string]any) error {
		persisted = true
		return nil
	})
	require.Error(t, err)
	require.False(t, result.Committed)
	require.False(t, persisted)
	// The active transaction still owns serialization when the queued client returns.
	require.NoError(t, active.Send(&settingspb.CommitSettingsRequest{
		Phase: &settingspb.CommitSettingsRequest_Persisted{Persisted: true},
	}))
	applied, err := active.Recv()
	require.NoError(t, err)
	require.True(t, applied.GetResult().GetApplied())
	result, err = client.CommitSettings(context.Background(), map[string]any{"name": "final"}, func(map[string]any) error {
		value, readErr := provider.GetSettingValue("name")
		require.NoError(t, readErr)
		require.Equal(t, "active", value)
		return nil
	})
	require.NoError(t, err)
	require.True(t, result.Applied)
}

func commitProvider(setting base.Setting) base.Provider {
	return base.NewProvider(base.ProviderOpts{
		PluginID: "fixture",
		PluginSettings: []base.Category{{
			ID: "plugin", Settings: map[string]base.Setting{"name": setting},
		}},
	})
}

func commitValues(t *testing.T, value string) *settingspb.SettingsValues {
	t.Helper()
	encoded, err := settings.ConvertInterfaceToAny(value)
	require.NoError(t, err)
	return &settingspb.SettingsValues{Values: map[string]*anypb.Any{"name": encoded}}
}
