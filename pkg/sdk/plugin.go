package sdk

import (
	"context"
	"maps"
	"slices"

	"github.com/hashicorp/go-plugin"
	logging "github.com/omniviewdev/plugin-sdk/log"
	pkgsettings "github.com/omniviewdev/plugin-sdk/settings"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	"github.com/omniviewdev/plugin-sdk/pkg/config"
	"github.com/omniviewdev/plugin-sdk/pkg/resource"
	rp "github.com/omniviewdev/plugin-sdk/pkg/resource/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/resource/services"
	"github.com/omniviewdev/plugin-sdk/pkg/resource/types"
	"github.com/omniviewdev/plugin-sdk/pkg/state"
	"github.com/omniviewdev/plugin-sdk/pkg/telemetry"
	"github.com/omniviewdev/plugin-sdk/pkg/utils"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/lifecycle"
	sdksettings "github.com/omniviewdev/plugin-sdk/pkg/v1/settings"
)

// CurrentProtocolVersion is the SDK protocol version that this SDK release
// implements. It is used as the key in go-plugin's VersionedPlugins map so
// that the engine and plugin can negotiate the highest common version.
const CurrentProtocolVersion = 1

// PluginOpts is the options for creating a new plugin.
type PluginOpts struct {
	// ID is the unique identifier for the plugin
	ID string

	// Settings is a list of settings to be used by the plugin
	Settings []pkgsettings.Setting

	// Debug is the debug mode for the plugin
	Debug bool
}

type Plugin struct {
	// settingsProvider is the settings provider for the plugin.
	SettingsProvider pkgsettings.Provider
	// pluginMap is the map of plugins we can dispense. Each plugin entry in the map
	// is a plugin capability.
	pluginMap      map[string]plugin.Plugin
	Log            logging.Logger
	LogLevel       *logging.LevelController
	runtimeBackend *logging.HCLBackend
	// meta holds metadata for the plugin, found inside of the plugin.yaml
	// file in the same directory as the plugin.
	meta config.PluginMeta
}

// NewPlugin creates a new plugin with the given configuration. This should be instantiated
// within your main function for your plugin and passed to the Register* functions to add
// capabilities to the plugin.
func NewPlugin(opts PluginOpts) *Plugin {
	path, err := state.OpenPluginRoot(opts.ID)
	if err != nil {
		panic(err)
	}
	defer path.Close()
	if err := state.RejectSymlink(path, "plugin.yaml"); err != nil {
		panic(err)
	}
	file, err := path.Open("plugin.yaml")
	if err != nil {
		panic(err)
	}
	defer file.Close()

	// load in the plugin configuration
	meta := config.PluginMeta{}
	if err = meta.Load(file); err != nil {
		panic(err)
	}
	if meta.ID != opts.ID {
		panic("plugin.yaml ID differs from requested plugin ID")
	}

	// convert the plugin settings to a map
	settings := make(map[string]pkgsettings.Setting)
	for _, setting := range opts.Settings {
		settings[setting.ID] = setting
	}

	level := logging.NewLevelController(logging.LevelInfo)
	if opts.Debug {
		level.Set(logging.LevelDebug)
	}

	runtimeBackend := logging.NewDefaultHCLBackend(meta.ID, level.Level()).Named("runtime")
	sdkLogger := logging.New(logging.Config{
		Name:    "plugin",
		Level:   level,
		Backend: runtimeBackend,
		Fields: []logging.Field{
			logging.String("plugin_id", opts.ID),
		},
	})
	logging.SetDefault(sdkLogger)

	// init the settings provider
	settingsProvider := pkgsettings.NewProvider(pkgsettings.ProviderOpts{
		Logger:   zap.S(),
		PluginID: opts.ID,
		PluginSettings: []pkgsettings.Category{
			{
				ID:       "plugin",
				Settings: settings,
			},
		},
	})

	// setup the settings plugin by default
	plugins := map[string]plugin.Plugin{
		"settings": &sdksettings.SettingsPlugin{
			Impl: sdksettings.NewProviderWrapper(settingsProvider),
		},
	}

	return &Plugin{
		Log:              sdkLogger,
		LogLevel:         level,
		runtimeBackend:   runtimeBackend,
		meta:             meta,
		pluginMap:        plugins,
		SettingsProvider: settingsProvider,
	}
}

// registerCapability registers a plugin capability with the plugin system.
func (p *Plugin) RegisterCapability(capability string, registration plugin.Plugin) {
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

func (p *Plugin) GetMeta() config.PluginMeta {
	return p.meta
}

func (p *Plugin) GetPluginID() string {
	return p.meta.ID
}

func GRPCServerFactory(opts []grpc.ServerOption) *grpc.Server {
	return GRPCServerFactoryWithLogger(logging.Default(), opts)
}

func GRPCServerFactoryWithLogger(log logging.Logger, opts []grpc.ServerOption) *grpc.Server {
	return grpc.NewServer(utils.RegisterServerOpts(opts, log)...)
}

func GRPCDialOptions() []grpc.DialOption {
	return withClientOpts(nil, logging.Default())
}

func GRPCDialOptionsWithLogger(log logging.Logger) []grpc.DialOption {
	return withClientOpts(nil, log)
}

// effectiveProtocolVersion returns the SDK protocol version to use.
// It prefers meta.SDKProtocolVersion when set, falling back to CurrentProtocolVersion.
func (p *Plugin) effectiveProtocolVersion() int {
	if p.meta.SDKProtocolVersion > 0 {
		return p.meta.SDKProtocolVersion
	}
	return CurrentProtocolVersion
}

func (p *Plugin) SetLogLevel(level logging.Level) {
	if p == nil || p.LogLevel == nil {
		return
	}
	p.LogLevel.Set(level)
	if p.runtimeBackend != nil {
		p.runtimeBackend.SetLevel(level)
	}
}

// Serve begins serving the plugin after all capabilities have been registered.
// It auto-registers lifecycle RPC and leaves handshake transport to go-plugin.
// The host owns development process supervision; the SDK never advertises or
// removes legacy .devinfo records.
func (p *Plugin) Serve() {
	telemetryProvider, err := telemetry.InitFromEnv()
	if err != nil {
		p.Log.Warn(context.Background(), "telemetry init failed, continuing without",
			logging.Error(err))
	}
	if telemetryProvider != nil {
		defer telemetryProvider.Shutdown(context.Background())
	}

	p.registerLifecycle()
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: p.meta.GenerateHandshakeConfig(),
		VersionedPlugins: map[int]plugin.PluginSet{
			p.effectiveProtocolVersion(): p.pluginMap,
		},
		GRPCServer: func(opts []grpc.ServerOption) *grpc.Server {
			return GRPCServerFactoryWithLogger(p.Log, opts)
		},
	})
}

// registerLifecycle auto-registers the PluginLifecycle gRPC service.
// It reads the current plugin map to build the capabilities list.
func (p *Plugin) registerLifecycle() {
	caps := slices.Sorted(maps.Keys(p.pluginMap))

	p.pluginMap["lifecycle"] = &lifecycle.Plugin{
		Impl: &lifecycle.Server{
			PluginID:           p.meta.ID,
			Version:            p.meta.Version,
			SDKProtocolVersion: int32(p.effectiveProtocolVersion()),
			Capabilities:       caps,
		},
	}
}

// RegisterResourcePlugin registers a resource plugin with the given options. Resource plugins are
// plugins that manage resources, such as clouds, Kubernetes clusters, etc.
func RegisterResourcePlugin[ClientT any](
	p *Plugin,
	opts ResourcePluginOpts[ClientT],
) {
	if p == nil {
		// improper use of plugin
		panic("plugin cannot be nil")
	}

	metas := make([]types.ResourceMeta, 0, len(opts.Resourcers))
	for meta := range opts.Resourcers {
		metas = append(metas, meta)
	}

	// check for discovery
	var typeManager services.ResourceTypeManager
	if opts.DiscoveryProvider != nil {
		// dynamic resource plugin
		typeManager = services.NewDynamicResourceTypeManager(
			metas,
			opts.ResourceGroups,
			opts.ResourceDefinitions,
			opts.DefaultResourceDefinition,
			opts.DiscoveryProvider,
		)
	} else {
		// static resource plugin
		typeManager = services.NewStaticResourceTypeManager(
			metas,
			opts.ResourceGroups,
			opts.ResourceDefinitions,
			opts.DefaultResourceDefinition,
		)
	}

	// create the layouts
	layoutManager := services.NewLayoutManager(opts.LayoutOpts)
	if opts.LayoutOpts != nil {
		// generate from the metas
		layoutManager.GenerateLayoutFromMetas(metas)
	}

	// create the resourcer
	resourcer := services.NewResourcerManager[ClientT]()
	if err := resourcer.RegisterResourcersFromMap(opts.Resourcers); err != nil {
		panic(err)
	}

	if len(opts.PatternResourcers) > 0 {
		if err := resourcer.RegisterPatternResourcersFromMap(opts.PatternResourcers); err != nil {
			panic(err)
		}
	}

	controller := resource.NewResourceController(
		resourcer,
		services.NewConnectionManager(
			opts.CreateClient,
			opts.RefreshClient,
			opts.StartClient,
			opts.StopClient,
			opts.LoadConnectionFunc,
			opts.WatchConnectionsFunc,
			opts.CheckConnectionFunc,
			opts.LoadConnectionNamespacesFunc,
		),
		typeManager,
		layoutManager,
		opts.CreateInformerFunc,
		opts.SyncPolicies,
		opts.SchemaFunc,
		opts.ErrorClassifier,
	)

	// Register the resource plugin with the plugin system.
	p.RegisterCapability("resource", &rp.ResourcePlugin{
		Impl:             controller,
		SettingsProvider: p.SettingsProvider,
	})
}
