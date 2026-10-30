package metric

import (
	"context"
	"errors"

	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	logging "github.com/omniviewdev/plugin-sdk/log"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	metricpb "github.com/omniviewdev/plugin-sdk/proto/v1/metric"
)

// Provider is the interface satisfied by the plugin server and client
// to provide metric querying functionality.
type Provider interface {
	GetSupportedResources(ctx *types.PluginContext) (*ProviderInfo, []Handler)
	Query(ctx *types.PluginContext, req QueryRequest) (*QueryResponse, error)
	StreamMetrics(ctx context.Context, in chan StreamInput) (chan StreamOutput, error)
}

// ResourceQueryProvider accepts lossless resource queries on the explicit v2 RPC.
type ResourceQueryProvider interface {
	QueryResource(*types.PluginContext, QueryRequest) (*QueryResponse, error)
}

// ResourceStreamProvider accepts mixed resource and nonresource v2 frames.
type ResourceStreamProvider interface {
	StreamResourceMetrics(context.Context, chan StreamInput) (chan StreamOutput, error)
}

// Plugin implements the hashicorp go-plugin interfaces for metric capability.
type Plugin struct {
	plugin.Plugin
	Impl Provider
}

func (p *Plugin) BindConnectionContextProvider(provider resourcev2.ConnectionContextProvider) {
	if consumer, ok := p.Impl.(resourcev2.ConnectionContextConsumer); ok {
		consumer.BindConnectionContextProvider(provider)
	}
}

func (p *Plugin) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	RegisterMetricPluginServer(s, &PluginServer{Impl: p.Impl})
	return nil
}

func (p *Plugin) GRPCClient(
	_ context.Context,
	_ *plugin.GRPCBroker,
	c *grpc.ClientConn,
) (interface{}, error) {
	return &PluginClient{
		client: metricpb.NewMetricPluginClient(c),
		log:    logging.Default().Named("metric.plugin.client"),
	}, nil
}

// RegisterPlugin registers the metric capability with the plugin system.
func RegisterPlugin(
	p *sdk.Plugin,
	opts PluginOpts,
) error {
	if p == nil {
		return errors.New("plugin is nil")
	}

	handlers := make(map[string]Handler)
	for key, handler := range opts.Handlers {
		handlers[key] = handler
	}

	impl := NewManager(
		p.Log.Named("metric"),
		p.SettingsProvider,
		opts.ProviderInfo,
		handlers,
		opts.QueryFunc,
		opts.StreamFunc,
	)

	p.RegisterCapability("metric", &Plugin{Impl: impl})
	return nil
}
