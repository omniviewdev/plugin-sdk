package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewConnection_Defaults(t *testing.T) {
	conn, err := NewConnection(ConnectionOpts{
		ID:   "conn-1",
		Name: "Connection 1",
	})
	require.NoError(t, err)
	require.Equal(t, ConnectionDefaultExpiryTime, conn.ExpiryTime)
	require.Nil(t, conn.Lifecycle.AutoConnect)
}

func TestNewConnection_NameDefaultsToID(t *testing.T) {
	conn, err := NewConnection(ConnectionOpts{
		ID: "conn-1",
	})
	require.NoError(t, err)
	require.Equal(t, "conn-1", conn.Name)
}

func TestNewConnection_KeepExplicitRetry(t *testing.T) {
	conn, err := NewConnection(ConnectionOpts{
		ID:   "conn-1",
		Name: "Connection 1",
		Lifecycle: ConnectionLifecycle{
			AutoConnect: &ConnectionAutoConnect{
				Enabled: true,
				Retry:   ConnectionAutoConnectRetryOnChange,
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, ConnectionAutoConnectRetryOnChange, conn.Lifecycle.AutoConnect.Retry)
}

func TestNewConnection_InvalidLifecycleValues(t *testing.T) {
	_, err := NewConnection(ConnectionOpts{
		ID:   "conn-1",
		Name: "Connection 1",
		Lifecycle: ConnectionLifecycle{
			AutoConnect: &ConnectionAutoConnect{
				Retry: ConnectionAutoConnectRetry("INVALID"),
			},
		},
	})
	require.Error(t, err)

	_, err = NewConnection(ConnectionOpts{
		ID:   "conn-2",
		Name: "Connection 2",
		Lifecycle: ConnectionLifecycle{
			AutoConnect: &ConnectionAutoConnect{
				Retry:    ConnectionAutoConnectRetryNone,
				Triggers: []ConnectionAutoConnectTrigger{"INVALID_TRIGGER"},
			},
		},
	})
	require.Error(t, err)
}
