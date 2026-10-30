package tools

import (
	"context"
	"errors"

	"github.com/hashicorp/go-plugin"
	logging "github.com/omniviewdev/plugin-sdk/log"
	"google.golang.org/grpc"

	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	toolspb "github.com/omniviewdev/plugin-sdk/proto/v1/tools"
)

const (
	// PluginID identifies the tools capability in the plugin registry.
	PluginID = "tools"
)

// Provider is the interface satisfied by the plugin server and client
// to expose tools to the host.
type Provider interface {
	ListTools(*types.PluginContext) ([]Descriptor, error)
	CallTool(*types.PluginContext, CallInput) (*Result, error)
}

// Plugin exposes a tools provider through the go-plugin gRPC transport.
type Plugin struct {
	plugin.Plugin
	Impl Provider
}

// GRPCServer registers the tools provider with the plugin's gRPC server.
func (p *Plugin) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	toolspb.RegisterToolsPluginServer(s, &PluginServer{Impl: p.Impl})
	return nil
}

// GRPCClient creates a tools client for the plugin's gRPC connection.
func (p *Plugin) GRPCClient(
	_ context.Context,
	_ *plugin.GRPCBroker,
	c *grpc.ClientConn,
) (interface{}, error) {
	return &PluginClient{
		client: toolspb.NewToolsPluginClient(c),
		log:    logging.Default().Named("tools.plugin.client"),
	}, nil
}

// PluginOpts lists the tools a plugin exposes.
type PluginOpts struct {
	Tools []Tool
}

// RegisterPlugin validates the tools and registers the capability.
func RegisterPlugin(p *sdk.Plugin, opts PluginOpts) error {
	if p == nil {
		return errors.New("plugin is nil")
	}

	impl, err := NewManager(p.Log.Named("tools"), opts)
	if err != nil {
		return err
	}

	p.RegisterCapability(PluginID, &Plugin{Impl: impl})
	return nil
}
