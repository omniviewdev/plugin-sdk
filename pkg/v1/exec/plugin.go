package exec

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/go-plugin"
	logging "github.com/omniviewdev/plugin-sdk/log"
	"google.golang.org/grpc"

	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	execpb "github.com/omniviewdev/plugin-sdk/proto/v1/exec"
)

// Provider is the interface satisfied by the plugin server and client
// to provide the exec functionality.
type Provider interface {
	// GetSupportedResources returns the supported resource types
	GetSupportedResources(ctx *types.PluginContext) []Handler
	// GetSession returns a session by ID
	GetSession(ctx *types.PluginContext, sessionID string) (*Session, error)
	// ListSessions returns all of the sessions
	ListSessions(ctx *types.PluginContext) ([]*Session, error)
	// CreateSession creates a new session
	CreateSession(ctx *types.PluginContext, opts SessionOptions) (*Session, error)
	// AttachSession attaches a session
	AttachSession(ctx *types.PluginContext, sessionID string) (*Session, []byte, error)
	// DetachSession detaches a session
	DetachSession(ctx *types.PluginContext, sessionID string) (*Session, error)
	// CloseSession closes a session
	CloseSession(ctx *types.PluginContext, sessionID string) error
	// ResizeSession resizes a session
	ResizeSession(ctx *types.PluginContext, sessionID string, rows, cols int32) error
	// Stream starts a new stream to multiplex sessions
	Stream(context.Context, chan StreamInput) (chan StreamOutput, error)
	// Close shuts down the provider, releasing all resources.
	Close()
}

type Plugin struct {
	plugin.Plugin
	Impl Provider
}

func (p *Plugin) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	execpb.RegisterExecPluginServer(s, &PluginServer{
		log:  logging.Default().Named("exec.plugin.server"),
		Impl: p.Impl,
	})
	return nil
}

func (p *Plugin) GRPCClient(
	_ context.Context,
	_ *plugin.GRPCBroker,
	c *grpc.ClientConn,
) (interface{}, error) {
	return &PluginClient{
		client: execpb.NewExecPluginClient(c),
		log:    logging.Default().Named("exec.plugin.client"),
	}, nil
}

func RegisterPlugin(
	p *sdk.Plugin,
	opts PluginOpts,
) error {
	if p == nil {
		return errors.New("plugin is nil")
	}

	handlers := make(map[string]Handler, len(opts.Handlers))
	for _, handler := range opts.Handlers {
		key := handler.Plugin + "/" + handler.Resource
		if _, exists := handlers[key]; exists {
			return fmt.Errorf("duplicate exec handler key %q", key)
		}
		handlers[key] = handler
	}

	impl := NewManager(ManagerConfig{
		Logger:   p.Log.Named("exec"),
		Settings: p.SettingsProvider,
		Handlers: handlers,
	})

	p.RegisterCapability("exec", &Plugin{Impl: impl})
	return nil
}
