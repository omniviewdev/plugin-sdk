package sdk

import (
	"errors"

	"github.com/omniviewdev/plugin-sdk/pkg/resource/factories"
	"github.com/omniviewdev/plugin-sdk/pkg/resource/types"
	pkgtypes "github.com/omniviewdev/plugin-sdk/pkg/types"
)

type IResourcePluginOpts[CT, DT, IT any] interface {
	HasDiscovery() (bool, error)
	GetClientFactory() factories.ResourceClientFactory[CT]
	GetResourcers() map[types.ResourceMeta]types.Resourcer[CT]
	GetResourceGroups() []types.ResourceGroup
	GetDiscoveryClientFactory() factories.ResourceDiscoveryClientFactory[DT]
	GetDiscoveryFunc() func(*pkgtypes.PluginContext, *DT) ([]types.ResourceMeta, error)
	HasInformer() bool
	GetInformerOpts() *types.InformerOptions[CT, IT]
	GetLayoutOpts() *types.LayoutOpts
	GetLoadConnectionFunc() func(*pkgtypes.PluginContext) ([]pkgtypes.Connection, error)
}

// DynamicResourcePluginOpts is a set of options for configuring a dynamic resource plugin. A dynamic resource
// plugin must consist of a discovery client factory, a discovery function, a client factory, and a set of
// resourcers that the plugin will manage.
type ResourcePluginOpts[ClientT, DiscoveryClientT, InformerT any] struct {
	// LoadConnectionFunc is a function that will be called to load the possible connections
	// for the resource plugin. This should be used to load the possible connections available based on either
	// settings within the ide (by pulling it off of the plugin context) or by using system defaults.
	//
	// TODO - move this and the client factory to the parent plugin opts so that multiple capabilities can
	// use the same client and connection loaders
	LoadConnectionFunc func(*pkgtypes.PluginContext) ([]pkgtypes.Connection, error)

	// ClientFactory is the factory for creating a new resource client to interact with a backend.
	//
	// TODO - move this and the load connection func to the parent plugin opts so that multiple capabilities can
	// use the same client
	ClientFactory factories.ResourceClientFactory[ClientT]

	// DiscoveryClientFactory is the factory for creating a new discovery client to interact with a backend.
	DiscoveryClientFactory factories.ResourceDiscoveryClientFactory[DiscoveryClientT]

	// DiscoveryFunc is the function that will be called to discover resources.
	DiscoveryFunc func(*pkgtypes.PluginContext, *DiscoveryClientT) ([]types.ResourceMeta, error)

	// Resourcers is a map of resource metadata to resourcers that the plugin will manage.
	Resourcers map[types.ResourceMeta]types.Resourcer[ClientT]

	// ResourceGroups is an optional array of resource groups to provide more details about the groups that
	// contain the resources. Is is highly recommended to provide this information to enrich the UI.
	ResourceGroups []types.ResourceGroup

	// InformerOpts allows for an additional set of custom options for setting up informers on the
	// resource backend.
	InformerOpts *types.InformerOptions[ClientT, InformerT]

	// LayoutOpts allows for customizing the layout within the UI
	LayoutOpts *types.LayoutOpts
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

func (opts ResourcePluginOpts[CT, DT, IT]) GetResourceGroups() []types.ResourceGroup {
	return opts.ResourceGroups
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

func (opts ResourcePluginOpts[CT, DT, IT]) GetInformerOpts() *types.InformerOptions[CT, IT] {
	return opts.InformerOpts
}

func (opts ResourcePluginOpts[CT, DT, IT]) GetLayoutOpts() *types.LayoutOpts {
	return opts.LayoutOpts
}

func (opts ResourcePluginOpts[ClientT, DiscoveryClientT, InformerT]) GetLoadConnectionFunc() func(*pkgtypes.PluginContext) (
	[]pkgtypes.Connection,
	error,
) {
	return opts.LoadConnectionFunc
}
