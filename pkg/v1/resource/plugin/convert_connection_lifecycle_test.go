package plugin

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	commonpb "github.com/omniviewdev/plugin-sdk/proto/v1/common"
)

func TestConnectionLifecycleProtoRoundTrip(t *testing.T) {
	in := types.Connection{
		ID:   "conn-1",
		Name: "Connection 1",
		Lifecycle: types.ConnectionLifecycle{
			AutoConnect: types.ConnectionAutoConnect{
				Enabled: true,
				Triggers: []types.ConnectionAutoConnectTrigger{
					types.ConnectionAutoConnectTriggerPluginStart,
					types.ConnectionAutoConnectTriggerConnectionDiscovered,
				},
				Retry: types.ConnectionAutoConnectRetryOnChange,
			},
		},
	}

	pb, err := connectionToProto(in)
	require.NoError(t, err)

	out := connectionFromProto(pb)
	require.Equal(t, in.Lifecycle, out.Lifecycle)
}

func TestConnectionLifecycleFromProtoDefaultsRetry(t *testing.T) {
	out := connectionFromProto(nil)
	require.Equal(t, types.ConnectionAutoConnectRetryNone, out.Lifecycle.AutoConnect.Retry)

	out = connectionFromProto(&commonpb.Connection{Id: "conn-1"})
	require.Equal(t, types.ConnectionAutoConnectRetryNone, out.Lifecycle.AutoConnect.Retry)
}
