package command

import (
	"context"
	"errors"

	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	commandpb "github.com/omniviewdev/plugin-sdk/proto/v1/command"
)

const (
	// PluginID is the go-plugin capability name used to dispense command transport.
	PluginID = "command"
)

type ProviderImpl interface {
	Call(ctx *types.PluginContext, target string, payload []byte) ([]byte, error)
}

// Provider is the interface satisfied by the plugin server and client
// to provide the general functionality.
type Provider interface {
	ProviderImpl
}

// Plugin registers command RPCs or creates a client for a go-plugin connection.
type Plugin struct {
	plugin.Plugin
	Impl Provider
}

// GRPCServer installs command RPCs backed by Impl.
func (p *Plugin) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	commandpb.RegisterCommandServer(s, &PluginServer{Impl: p.Impl})
	return nil
}

// GRPCClient returns a command client using the supplied connection.
func (p *Plugin) GRPCClient(
	_ context.Context,
	_ *plugin.GRPCBroker,
	c *grpc.ClientConn,
) (interface{}, error) {
	return &PluginClient{client: commandpb.NewCommandClient(c)}, nil
}

// RegisterPlugin installs handlers as a command capability; a nil plugin returns an error.
func RegisterPlugin(
	p *sdk.Plugin,
	opts PluginOpts,
) error {
	if p == nil {
		return errors.New("plugin is nil")
	}

	impl := NewManager(
		p.Log.Named("command"),
		p.SettingsProvider,
		opts.Handlers,
	)

	p.RegisterCapability(PluginID, &Plugin{Impl: impl})
	return nil
}
