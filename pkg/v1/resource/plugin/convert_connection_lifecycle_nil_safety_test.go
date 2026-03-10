package plugin

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	commonpb "github.com/omniviewdev/plugin-sdk/proto/v1/common"
)

func TestConnectionToProto_LifecycleNilStates_NoPanic(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name               string
		lifecycle          types.ConnectionLifecycle
		expectLifecycleNil bool
	}{
		{
			name:               "zero lifecycle",
			lifecycle:          types.ConnectionLifecycle{},
			expectLifecycleNil: true,
		},
		{
			name: "explicit nil auto_connect",
			lifecycle: types.ConnectionLifecycle{
				AutoConnect: nil,
			},
			expectLifecycleNil: true,
		},
		{
			name: "auto_connect with nil triggers and empty retry",
			lifecycle: types.ConnectionLifecycle{
				AutoConnect: &types.ConnectionAutoConnect{
					Enabled:  true,
					Triggers: nil,
					Retry:    "",
				},
			},
			expectLifecycleNil: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.NotPanics(t, func() {
				pb, err := connectionToProto(types.Connection{
					ID:        "conn-1",
					Name:      "Connection 1",
					Lifecycle: tc.lifecycle,
				})
				require.NoError(t, err)

				if tc.expectLifecycleNil {
					require.Nil(t, pb.GetLifecycle())
					return
				}

				require.NotNil(t, pb.GetLifecycle())
				require.NotNil(t, pb.GetLifecycle().GetAutoConnect())
				require.Equal(
					t,
					commonpb.ConnectionAutoConnectRetry_CONNECTION_AUTO_CONNECT_RETRY_NONE,
					pb.GetLifecycle().GetAutoConnect().GetRetry(),
				)
				require.Empty(t, pb.GetLifecycle().GetAutoConnect().GetTriggers())
			})
		})
	}
}

func TestConnectionFromProto_LifecycleNilStates_NoPanic(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name             string
		pb               *commonpb.Connection
		expectAutoNil    bool
		expectRetryValue types.ConnectionAutoConnectRetry
	}{
		{
			name:          "nil proto",
			pb:            nil,
			expectAutoNil: true,
		},
		{
			name: "nil lifecycle",
			pb: &commonpb.Connection{
				Id: "conn-1",
			},
			expectAutoNil: true,
		},
		{
			name: "nil auto_connect",
			pb: &commonpb.Connection{
				Id: "conn-1",
				Lifecycle: &commonpb.ConnectionLifecycle{
					AutoConnect: nil,
				},
			},
			expectAutoNil: true,
		},
		{
			name: "auto_connect with unspecified retry",
			pb: &commonpb.Connection{
				Id: "conn-1",
				Lifecycle: &commonpb.ConnectionLifecycle{
					AutoConnect: &commonpb.ConnectionAutoConnect{
						Enabled: true,
						Retry:   commonpb.ConnectionAutoConnectRetry_CONNECTION_AUTO_CONNECT_RETRY_UNSPECIFIED,
					},
				},
			},
			expectAutoNil:    false,
			expectRetryValue: types.ConnectionAutoConnectRetryNone,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.NotPanics(t, func() {
				out := connectionFromProto(tc.pb)
				if tc.expectAutoNil {
					require.Nil(t, out.Lifecycle.AutoConnect)
					return
				}
				require.NotNil(t, out.Lifecycle.AutoConnect)
				require.Equal(t, tc.expectRetryValue, out.Lifecycle.AutoConnect.Retry)
			})
		})
	}
}
