package resource_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/resource/resourcetest"
)

func TestCM_LifecycleAutoConnectNilStates_NoPanic(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		initial types.Connection
		update  types.Connection
	}{
		{
			name: "zero lifecycle",
			initial: types.Connection{
				ID:   "conn-1",
				Name: "Initial",
			},
			update: types.Connection{
				ID:   "conn-1",
				Name: "Updated",
			},
		},
		{
			name: "explicit nil auto_connect",
			initial: types.Connection{
				ID:   "conn-1",
				Name: "Initial",
				Lifecycle: types.ConnectionLifecycle{
					AutoConnect: nil,
				},
			},
			update: types.Connection{
				ID:   "conn-1",
				Name: "Updated",
				Lifecycle: types.ConnectionLifecycle{
					AutoConnect: nil,
				},
			},
		},
		{
			name: "non-nil auto_connect with nil triggers and empty retry",
			initial: types.Connection{
				ID:   "conn-1",
				Name: "Initial",
				Lifecycle: types.ConnectionLifecycle{
					AutoConnect: &types.ConnectionAutoConnect{
						Enabled:  true,
						Triggers: nil,
						Retry:    "",
					},
				},
			},
			update: types.Connection{
				ID:   "conn-1",
				Name: "Updated",
				Lifecycle: types.ConnectionLifecycle{
					AutoConnect: &types.ConnectionAutoConnect{
						Enabled:  true,
						Triggers: nil,
						Retry:    "",
					},
				},
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			cp := &resourcetest.StubConnectionProvider[string]{
				LoadConnectionsFunc: func(_ context.Context) ([]types.Connection, error) {
					return []types.Connection{tc.initial}, nil
				},
			}
			mgr := resource.NewConnectionManagerForTest(ctx, cp)

			require.NotPanics(t, func() {
				_, err := mgr.LoadConnections(ctx)
				require.NoError(t, err)

				_, err = mgr.ListConnections(ctx)
				require.NoError(t, err)

				_, err = mgr.GetConnection(tc.initial.ID)
				require.NoError(t, err)

				_, err = mgr.StartConnection(ctx, tc.initial.ID)
				require.NoError(t, err)

				_, err = mgr.UpdateConnection(ctx, tc.update)
				require.NoError(t, err)

				_, err = mgr.StopConnection(ctx, tc.initial.ID)
				require.NoError(t, err)
			})
		})
	}
}
