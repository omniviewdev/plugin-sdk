package resource

import (
	"context"

	"github.com/omniviewdev/plugin-sdk/pkg/config"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/settings"
)

type sessionKey struct{}

// Session carries per-request metadata through context.Context values.
// Set by the SDK framework before calling into plugin code.
// Replaces the old *PluginContext — Go convention is context.Context first.
type Session struct {
	Connection   *types.Connection
	PluginConfig settings.Provider
	GlobalConfig *config.GlobalConfig
	RequestID    string
	RequesterID  string
}

// WithSession stores a Session in the context.
func WithSession(ctx context.Context, s *Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

// SessionFromContext retrieves the Session from the context.
// Returns nil if no Session is present.
func SessionFromContext(ctx context.Context) *Session {
	s, _ := ctx.Value(sessionKey{}).(*Session)
	return s
}

// ConnectionFromContext is a convenience helper that retrieves the Connection
// from the Session stored in the context. Returns nil if no Session or no
// Connection is present.
func ConnectionFromContext(ctx context.Context) *types.Connection {
	s := SessionFromContext(ctx)
	if s == nil {
		return nil
	}
	return s.Connection
}
