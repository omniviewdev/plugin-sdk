package logs

import (
	"context"
	"errors"

	"github.com/hashicorp/go-plugin"
	logging "github.com/omniviewdev/plugin-sdk/log"
	"google.golang.org/grpc"

	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	logspb "github.com/omniviewdev/plugin-sdk/proto/v1/logs"
)

// Provider is the interface satisfied by the plugin server and client
// to provide log viewing functionality.
type Provider interface {
	GetSupportedResources(ctx *types.PluginContext) []Handler
	CreateSession(ctx *types.PluginContext, opts CreateSessionOptions) (*LogSession, error)
	GetSession(ctx *types.PluginContext, sessionID string) (*LogSession, error)
	ListSessions(ctx *types.PluginContext) ([]*LogSession, error)
	CloseSession(ctx *types.PluginContext, sessionID string) error
	UpdateSessionOptions(ctx *types.PluginContext, sessionID string, opts LogSessionOptions) (*LogSession, error)
	Stream(ctx context.Context, in chan StreamInput) (chan StreamOutput, error)
}

// Plugin implements the hashicorp go-plugin interfaces for log capability.
type Plugin struct {
	plugin.Plugin
	Impl Provider
}

func (p *Plugin) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	logspb.RegisterLogPluginServer(s, &PluginServer{log: logging.Default().Named("logs.plugin.server"), Impl: p.Impl})
	return nil
}

func (p *Plugin) GRPCClient(
	_ context.Context,
	_ *plugin.GRPCBroker,
	c *grpc.ClientConn,
) (interface{}, error) {
	return &PluginClient{
		client: logspb.NewLogPluginClient(c),
		log:    logging.Default().Named("logs.plugin.client"),
	}, nil
}

// RegisterPlugin registers the log capability with the plugin system.
func RegisterPlugin(
	p *sdk.Plugin,
	opts PluginOpts,
) error {
	if p == nil {
		return errors.New("plugin is nil")
	}

	handlers := make(map[string]Handler)
	for key, handler := range opts.Handlers {
		handlers[handler.Plugin+"/"+key] = handler
	}

	resolvers := make(map[string]SourceResolver)
	for key, resolver := range opts.SourceResolvers {
		resolvers[key] = resolver
	}

	loggerForManager := p.Log
	if loggerForManager == nil {
		loggerForManager = logging.NewNop()
	}

	impl := NewManager(ManagerConfig{
		Logger:    loggerForManager.Named("logs"),
		Settings:  p.SettingsProvider,
		Handlers:  handlers,
		Resolvers: resolvers,
	})

	p.RegisterCapability("log", &Plugin{Impl: impl})
	return nil
}
