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
	require.Equal(t, ConnectionAutoConnectRetryNone, conn.Lifecycle.AutoConnect.Retry)
}

func TestNewConnection_NameDefaultsToID(t *testing.T) {
	conn, err := NewConnection(ConnectionOpts{
		ID: "conn-1",
	})
	require.NoError(t, err)
	require.Equal(t, "conn-1", conn.Name)
}
