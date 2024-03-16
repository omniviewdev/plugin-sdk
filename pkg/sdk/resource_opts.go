package sdk

import (
	"errors"

	"github.com/omniviewdev/plugin-sdk/pkg/resource/factories"
	"github.com/omniviewdev/plugin-sdk/pkg/resource/services"
	"github.com/omniviewdev/plugin-sdk/pkg/resource/types"
	pkgtypes "github.com/omniviewdev/plugin-sdk/pkg/types"
)

type IResourcePluginOpts[CT, DT, IT any] interface {
	HasDiscovery() (bool, error)
	GetClientFactory() factories.ResourceClientFactory[CT]
	GetResourcers() map[types.ResourceMeta]types.Resourcer[CT]
	GetDiscoveryClientFactory() factories.ResourceDiscoveryClientFactory[DT]
	GetDiscoveryFunc() func(*pkgtypes.PluginContext, *DT) ([]types.ResourceMeta, error)
	HasInformer() bool
	GetInformerOpts() *services.InformerOptions[CT, IT]
}

// DynamicResourcePluginOpts is a set of options for configuring a dynamic resource plugin. A dynamic resource
// plugin must consist of a discovery client factory, a discovery function, a client factory, and a set of
// resourcers that the plugin will manage.
type ResourcePluginOpts[ClientT, DiscoveryClientT, InformerT any] struct {
	// ClientFactory is the factory for creating a new resource client to interact with a backend.
	ClientFactory factories.ResourceClientFactory[ClientT]

	// DiscoveryClientFactory is the factory for creating a new discovery client to interact with a backend.
	DiscoveryClientFactory factories.ResourceDiscoveryClientFactory[DiscoveryClientT]

	// DiscoveryFunc is the function that will be called to discover resources.
	DiscoveryFunc func(*pkgtypes.PluginContext, *DiscoveryClientT) ([]types.ResourceMeta, error)

	// Resourcers is a map of resource metadata to resourcers that the plugin will manage.
	Resourcers map[types.ResourceMeta]types.Resourcer[ClientT]

	// InformerOpts allows for an additional set of custom options for setting up informers on the
	// resource backend.
	InformerOpts *services.InformerOptions[ClientT, InformerT]
}

func (opts ResourcePluginOpts[CT, DT, IT]) HasDiscovery() (bool, error) {
	switch {
	case opts.DiscoveryClientFactory != nil && opts.DiscoveryFunc != nil:
		// valid discovery
		return true, nil
	case opts.DiscoveryClientFactory != nil && opts.DiscoveryFunc == nil:
		// invalid discovery: discovery client factory is set but discovery function is not
		return false, errors.New("discovery client factory is set but discovery function is not")
	case opts.DiscoveryClientFactory == nil && opts.DiscoveryFunc != nil:
		// invalid discovery: discovery function is set but discovery client factory is not
		return false, errors.New("discovery function is set but discovery client factory is not")
	default:
		// no discovery
		return false, nil
	}
}

func (opts ResourcePluginOpts[CT, DT, IT]) GetClientFactory() factories.ResourceClientFactory[CT] {
	return opts.ClientFactory
}

func (opts ResourcePluginOpts[CT, DT, IT]) GetResourcers() map[types.ResourceMeta]types.Resourcer[CT] {
	return opts.Resourcers
}

func (opts ResourcePluginOpts[CT, DT, IT]) GetDiscoveryClientFactory() factories.ResourceDiscoveryClientFactory[DT] {
	if has, err := opts.HasDiscovery(); has && err == nil {
		return opts.DiscoveryClientFactory
	}
	return nil
}

func (opts ResourcePluginOpts[CT, DT, IT]) GetDiscoveryFunc() func(*pkgtypes.PluginContext, *DT) (
	[]types.ResourceMeta,
	error,
) {
	if has, err := opts.HasDiscovery(); has && err == nil {
		return opts.DiscoveryFunc
	}
	return nil
}

func (opts ResourcePluginOpts[CT, DT, IT]) HasInformer() bool {
	return opts.InformerOpts != nil
}

func (opts ResourcePluginOpts[CT, DT, IT]) GetInformerOpts() *services.InformerOptions[CT, IT] {
	return opts.InformerOpts
}
