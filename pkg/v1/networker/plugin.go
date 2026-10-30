package networker

import (
	"context"
	"errors"

	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	logging "github.com/omniviewdev/plugin-sdk/log"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	networkerpb "github.com/omniviewdev/plugin-sdk/proto/v1/networker"
)

const (
	// PluginID is the capability name used to register and discover networker plugins.
	PluginID = "networker"
)

// PortForwardProvider manages forwarding sessions and reports supported resource targets.
//nolint:iface // Both exported contracts retain provider source compatibility and identical method membership.
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

// Provider embeds the session-management contract shared by local managers and
// remote clients. Resource forwarding is an optional [ResourcePortForwardProvider].
//nolint:iface // Keep this exported contract distinct without removing any PortForwardProvider methods.
type Provider interface {
	PortForwardProvider
}

// Plugin connects a forwarding provider to the HashiCorp gRPC plugin protocol.
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

// GRPCServer registers the provider with resource-aware request admission.
func (p *Plugin) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	RegisterNetworkerPluginServer(s, &PluginServer{Impl: p.Impl})
	return nil
}

// GRPCClient creates a forwarding client for the supplied plugin connection.
func (p *Plugin) GRPCClient(
	_ context.Context,
	_ *plugin.GRPCBroker,
	c *grpc.ClientConn,
) (any, error) {
	return &PluginClient{
		client: networkerpb.NewNetworkerPluginClient(c),
		log:    logging.Default().Named("networker.plugin.client"),
	}, nil
}

// RegisterPlugin constructs a manager and registers the networker capability.
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
