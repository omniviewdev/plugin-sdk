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
	require.NotNil(t, conn.Data)
	require.NotNil(t, conn.Labels)
	require.NotNil(t, conn.GetSensitiveData())
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

func TestNewConnection_ExplicitNilAutoConnect(t *testing.T) {
	conn, err := NewConnection(ConnectionOpts{
		ID:   "conn-1",
		Name: "Connection 1",
		Lifecycle: ConnectionLifecycle{
			AutoConnect: nil,
		},
	})
	require.NoError(t, err)
	require.Nil(t, conn.Lifecycle.AutoConnect)
}

func TestNewConnection_NilTriggersAndEmptyRetry(t *testing.T) {
	conn, err := NewConnection(ConnectionOpts{
		ID:   "conn-1",
		Name: "Connection 1",
		Lifecycle: ConnectionLifecycle{
			AutoConnect: &ConnectionAutoConnect{
				Enabled:  true,
				Triggers: nil,
				Retry:    "",
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, conn.Lifecycle.AutoConnect)
	require.Nil(t, conn.Lifecycle.AutoConnect.Triggers)
	require.Equal(t, ConnectionAutoConnectRetryNone, conn.Lifecycle.AutoConnect.Retry)
}
