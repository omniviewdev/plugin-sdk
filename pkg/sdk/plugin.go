package sdk

import (
	"context"
	"maps"
	"math"
	"os"
	"slices"

	"github.com/hashicorp/go-plugin"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	logging "github.com/omniviewdev/plugin-sdk/log"
	pkgsettings "github.com/omniviewdev/plugin-sdk/settings"

	"github.com/omniviewdev/plugin-sdk/pkg/config"
	"github.com/omniviewdev/plugin-sdk/pkg/profiling"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	rp "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
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

// Plugin owns capability registration and process logging. Finish registration
// before serving; registration and metadata access are not synchronized.
type Plugin struct {
	// settingsProvider is the settings provider for the plugin.
	SettingsProvider pkgsettings.Provider
	// pluginMap is the map of plugins we can dispense. Each plugin entry in the map
	// is a plugin capability.
	pluginMap      map[string]plugin.Plugin
	Log            logging.Logger
	LogLevel       *logging.LevelController
	runtimeBackend *logging.HCLBackend
	profiler       *profiling.Service
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
	if pathErr := state.RejectSymlink(path, "plugin.yaml"); pathErr != nil {
		panic(pathErr)
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
	for i := range opts.Settings {
		settings[opts.Settings[i].ID] = opts.Settings[i]
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

// RegisterCapability installs or replaces a transport and binds utility consumers
// to the resource provider's sole configured connection registry.
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
	p.bindConnectionContexts()
}

func (p *Plugin) bindConnectionContexts() {
	resource, ok := p.pluginMap["resource"].(*rp.GRPCPlugin)
	if !ok {
		return
	}
	provider, ok := resource.Impl.(resourcev2.ConnectionContextProvider)
	if !ok {
		return
	}
	for _, capability := range p.pluginMap {
		if consumer, ok := capability.(resourcev2.ConnectionContextConsumer); ok {
			consumer.BindConnectionContextProvider(provider)
		}
	}
}

// GetPluginMap returns the plugin map for the plugin based on the capabilities that have been
// registered.
func (p *Plugin) GetPluginMap() map[string]plugin.Plugin {
	return p.pluginMap
}

// GetMeta returns metadata by value; nested containers remain shared and must not be mutated.
func (p *Plugin) GetMeta() config.PluginMeta {
	return p.meta
}

// GetPluginID returns the ID loaded from the installed plugin manifest.
func (p *Plugin) GetPluginID() string {
	return p.meta.ID
}

// GRPCServerFactory builds a server with SDK middleware and the default logger.
func GRPCServerFactory(opts []grpc.ServerOption) *grpc.Server {
	return GRPCServerFactoryWithLogger(logging.Default(), opts)
}

// GRPCServerFactoryWithLogger builds a server with resource wire limits and SDK middleware.
func GRPCServerFactoryWithLogger(log logging.Logger, opts []grpc.ServerOption) *grpc.Server {
	return grpc.NewServer(utils.RegisterServerOpts(append(opts, rp.ServerOptions()...), log)...)
}

// GRPCDialOptions returns SDK context propagation and tracing options using the default logger.
func GRPCDialOptions() []grpc.DialOption {
	return withClientOpts(nil, logging.Default())
}

// GRPCDialOptionsWithLogger returns SDK context propagation and tracing options using log.
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

// SetLogLevel changes both SDK and runtime logging; nil plugins or controllers are ignored.
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
// It panics if the effective SDK protocol version exceeds the signed 32-bit
// lifecycle wire range, rather than advertising a wrapped version. Nonpositive
// manifest versions continue to use CurrentProtocolVersion.
func (p *Plugin) Serve() {
	p.profiler = p.startProfiling()
	defer p.profiler.Close()

	telemetryProvider, err := telemetry.InitFromEnv()
	if err != nil {
		p.Log.Warn(context.Background(), "telemetry init failed, continuing without",
			logging.Error(err))
	}
	if telemetryProvider != nil {
		defer func() {
			if shutdownErr := telemetryProvider.Shutdown(context.Background()); shutdownErr != nil {
				p.Log.Warn(context.Background(), "telemetry shutdown failed", logging.Error(shutdownErr))
			}
		}()
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
	version := p.effectiveProtocolVersion()
	if version < math.MinInt32 || version > math.MaxInt32 {
		panic("SDK protocol version exceeds lifecycle wire range")
	}

	p.pluginMap["lifecycle"] = &lifecycle.Plugin{
		Impl: &lifecycle.Server{
			PluginID:           p.meta.ID,
			Version:            p.meta.Version,
			SDKProtocolVersion: int32(version),
			Capabilities:       caps,
			ProfilingStatus:    p.profilingStatus,
		},
	}
}

// RegisterResourcePlugin registers the resource-v2 provider. The catalog is
// required; finite operations are independently optional and catalog-qualified.
// Resource wire negotiation does not change unrelated utility protocol versions.
func RegisterResourcePlugin(p *Plugin, provider resourcev2.Provider) {
	if p == nil || provider == nil {
		panic("plugin and resource provider must not be nil")
	}
	p.RegisterCapability("resource", &rp.GRPCPlugin{
		Impl:             provider,
		SettingsProvider: p.SettingsProvider,
	})
}

// startProfiling requires an explicit host opt-in and always chooses a fresh port.
func (p *Plugin) startProfiling() *profiling.Service {
	service := profiling.New(profiling.Config{
		Enabled: os.Getenv("OMNIVIEW_PLUGIN_PPROF_ENABLED") == "true",
		Port:    0,
	}, "environment")
	if err := service.Start(); err != nil {
		p.Log.Warn(context.Background(), "profiling init failed, continuing without", logging.Error(err))
	}
	return service
}

func (p *Plugin) profilingStatus() profiling.Status {
	if p.profiler == nil {
		return profiling.Status{}
	}
	return p.profiler.GetStatus()
}
