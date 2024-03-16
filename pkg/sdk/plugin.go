package sdk

import (
	"os"
	"path/filepath"

	"github.com/hashicorp/go-plugin"

	"github.com/omniviewdev/plugin-sdk/pkg/config"
	"github.com/omniviewdev/plugin-sdk/pkg/resource/controllers"
	resource "github.com/omniviewdev/plugin-sdk/pkg/resource/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/resource/services"
	"github.com/omniviewdev/plugin-sdk/pkg/resource/types"
	pkgsettings "github.com/omniviewdev/plugin-sdk/pkg/settings"
)

const DefaultPluginMetaPath = "../plugin.yaml"

// PluginOpts is the options for creating a new plugin.
type PluginOpts struct {
	// ID is the unique identifier for the plugin
	ID string

	// Settings is a list of settings to be used by the plugin
	Settings []interface{}

	// Debug is the debug mode for the plugin
	Debug bool
}

type Plugin struct {
	// settingsProvider is the settings provider for the plugin.
	settingsProvider pkgsettings.Provider

	// pluginMap is the map of plugins we can dispense. Each plugin entry in the map
	// is a plugin capability.
	pluginMap map[string]plugin.Plugin

	// meta holds metadata for the plugin, found inside of the plugin.yaml
	// file in the same directory as the plugin.
	meta config.PluginMeta
}

// NewPlugin creates a new plugin with the given configuration. This should be instantiated
// within your main function for your plugin and passed to the Register* functions to add
// capabilities to the plugin.
func NewPlugin(opts PluginOpts) *Plugin {
	if opts.ID == "" {
		panic("plugin ID cannot be empty")
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}
	path := filepath.Join(homeDir, ".omniview", "plugins", opts.ID)

	// plugin yaml will be in users home
	// create io reader from the file
	file, err := os.Open(filepath.Join(path, "plugin.yaml"))
	if err != nil {
		if os.IsNotExist(err) {
			panic("plugin.yaml not found")
		}
		panic(err)
	}

	// load in the plugin configuration
	meta := config.PluginMeta{}
	if err = meta.Load(file); err != nil {
		panic(err)
	}

	return &Plugin{
		meta:             meta,
		pluginMap:        make(map[string]plugin.Plugin),
		settingsProvider: pkgsettings.NewSettingsProvider(opts.Settings),
	}
}

// registerCapability registers a plugin capability with the plugin system.
func (p *Plugin) registerCapability(capability string, registration plugin.Plugin) {
	if p == nil {
		panic("plugin cannot be nil when registering capability")
	}
	if capability == "" {
		panic("capability cannot be empty when registering capability")
	}
	// just to be safe, not really necessary
	if p.pluginMap == nil {
		p.pluginMap = make(map[string]plugin.Plugin)
	}

	p.pluginMap[capability] = registration
}

// GetPluginMap returns the plugin map for the plugin based on the capabilities that have been
// registered.
func (p *Plugin) GetPluginMap() map[string]plugin.Plugin {
	return p.pluginMap
}

// Serve begins serving the plugin over the given RPC server. This should be called
// after all capabilities have been registered.
func (p *Plugin) Serve() {
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: p.meta.GenerateHandshakeConfig(),
		Plugins:         p.pluginMap,
		GRPCServer:      plugin.DefaultGRPCServer,
	})
}

// RegisterResourcePlugin registers a resource plugin with the given options. Resource plugins are
// plugins that manage resources, such as clouds, Kubernetes clusters, etc.
func RegisterResourcePlugin[ClientT, DiscoveryT, InformerT any](
	p *Plugin,
	opts IResourcePluginOpts[ClientT, DiscoveryT, InformerT],
) {
	if p == nil {
		// improper use of plugin
		panic("plugin cannot be nil")
	}

	metas := make([]types.ResourceMeta, 0, len(opts.GetResourcers()))
	for meta := range opts.GetResourcers() {
		metas = append(metas, meta)
	}

	// check for discovery
	var typeManager services.ResourceTypeManager

	hasDiscovery, err := opts.HasDiscovery()
	if err != nil {
		panic(err)
	}

	if hasDiscovery {
		// dynamic resource plugin
		typeManager = services.NewDynamicResourceTypeManager(
			metas,
			opts.GetDiscoveryClientFactory(),
			opts.GetDiscoveryFunc(),
		)
	} else {
		// static resource plugin
		typeManager = services.NewStaticResourceTypeManager(metas)
	}

	controller := controllers.NewResourceController(
		services.NewResourcerManager[ClientT](),
		services.NewHookManager(),
		services.NewConnectionManager(opts.GetClientFactory()),
		typeManager,
		opts.GetInformerOpts(),
	)

	// Register the resource plugin with the plugin system.
	p.registerCapability("resource", &resource.ResourcePlugin{Impl: controller})
}
