package networker

import (
	"context"
	"errors"

	"github.com/hashicorp/go-plugin"
	logging "github.com/omniviewdev/plugin-sdk/log"
	"google.golang.org/grpc"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	networkerpb "github.com/omniviewdev/plugin-sdk/proto/v1/networker"
)

const (
	PluginID = "networker"
)

type PortForwardProvider interface {
	GetSupportedPortForwardTargets(*types.PluginContext) ([]string, error)
	GetPortForwardSession(*types.PluginContext, string) (*PortForwardSession, error)
	ListPortForwardSessions(*types.PluginContext) ([]*PortForwardSession, error)
	FindPortForwardSessions(
		*types.PluginContext,
		FindPortForwardSessionRequest,
	) ([]*PortForwardSession, error)
	StartPortForwardSession(
		*types.PluginContext,
		PortForwardSessionOptions,
	) (*PortForwardSession, error)
	ClosePortForwardSession(*types.PluginContext, string) (*PortForwardSession, error)
	StopAll()
}

// ResourcePortForwardProvider opts into lossless resource forwarding through the
// existing forwarding engine. Resource connections use this method even when
// ResourceData is nil. Older peers return UNSUPPORTED; clients never retry the
// legacy static-only StartPortForwardSession RPC.
type ResourcePortForwardProvider interface {
	StartResourcePortForwardSession(*types.PluginContext, PortForwardSessionOptions) (*PortForwardSession, error)
}

// Provider is the interface satisfied by the plugin server and client
// to provide the networker functionality.
type Provider interface {
	PortForwardProvider
}

type Plugin struct {
	plugin.Plugin
	Impl Provider
}

// BindConnectionContextProvider delegates registry binding to capable providers.
func (p *Plugin) BindConnectionContextProvider(provider resourcev2.ConnectionContextProvider) {
	if consumer, ok := p.Impl.(resourcev2.ConnectionContextConsumer); ok {
		consumer.BindConnectionContextProvider(provider)
	}
}

func (p *Plugin) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	RegisterNetworkerPluginServer(s, &PluginServer{Impl: p.Impl})
	return nil
}

func (p *Plugin) GRPCClient(
	_ context.Context,
	_ *plugin.GRPCBroker,
	c *grpc.ClientConn,
) (interface{}, error) {
	return &PluginClient{
		client: networkerpb.NewNetworkerPluginClient(c),
		log:    logging.Default().Named("networker.plugin.client"),
	}, nil
}

func RegisterPlugin(
	p *sdk.Plugin,
	opts PluginOpts,
) error {
	if p == nil {
		return errors.New("plugin is nil")
	}

	impl := NewManager(ManagerConfig{
		Logger:   p.Log.Named("networker"),
		Settings: p.SettingsProvider,
	}, opts)

	p.RegisterCapability(PluginID, &Plugin{Impl: impl})
	return nil
}
