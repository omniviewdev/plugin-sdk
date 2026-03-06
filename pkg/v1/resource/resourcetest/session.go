package resourcetest

import (
	"context"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
)

// NewTestSession creates a Session with sensible test defaults.
// The connection ID defaults to "conn-1" and the connection name to "Test".
func NewTestSession() *resource.Session {
	return &resource.Session{
		Connection: &types.Connection{
			ID:   "conn-1",
			Name: "Test",
		},
		RequestID:   "req-test-001",
		RequesterID: "test-user",
	}
}

// NewTestContext returns a context.Background with a test Session attached.
func NewTestContext() context.Context {
	return resource.WithSession(context.Background(), NewTestSession())
}

// NewTestContextWithConnection returns a context with a Session carrying the given connection.
func NewTestContextWithConnection(conn *types.Connection) context.Context {
	s := NewTestSession()
	s.Connection = conn
	return resource.WithSession(context.Background(), s)
}
