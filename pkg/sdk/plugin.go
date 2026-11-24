package sdk

import (
	"context"
	"maps"
	"math"
	"os"
	"slices"
	"time"

	"github.com/hashicorp/go-plugin"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	logging "github.com/omniviewdev/plugin-sdk/log"
	pkgsettings "github.com/omniviewdev/plugin-sdk/settings"

	"github.com/omniviewdev/plugin-sdk/pkg/activity"
	"github.com/omniviewdev/plugin-sdk/pkg/config"
	"github.com/omniviewdev/plugin-sdk/pkg/diagnostics"
	"github.com/omniviewdev/plugin-sdk/pkg/profiling"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	rp "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/state"
	"github.com/omniviewdev/plugin-sdk/pkg/telemetry"
	"github.com/omniviewdev/plugin-sdk/pkg/utils"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/hostconnections"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/lifecycle"
	sdksettings "github.com/omniviewdev/plugin-sdk/pkg/v1/settings"
)

// CurrentProtocolVersion is the SDK protocol version that this SDK release
// implements. It is used as the key in go-plugin's VersionedPlugins map so
// that the engine and plugin can negotiate the highest common version.
const CurrentProtocolVersion = 1

// LogLevelEnvironment names the environment variable the host uses to set the
// initial plugin log level (trace, debug, info, warn, error). It is ignored when
// PluginOpts.Debug is set.
const LogLevelEnvironment = "OMNIVIEW_PLUGIN_LOG_LEVEL"

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
	pluginMap       map[string]plugin.Plugin
	Log             logging.Logger
	LogLevel        *logging.LevelController
	runtimeBackend  *logging.HCLBackend
	profiler        *profiling.Service
	hostConnections *hostconnections.Binding
	activities      activity.Binding
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
	// The host passes its plugin log ship level here. Debug mode wins, and an
	// invalid value keeps Info; the warning is emitted once the logger exists.
	invalidEnvLevel := ""
	if envLevel := os.Getenv(LogLevelEnvironment); !opts.Debug && envLevel != "" {
		if parsed, parseErr := logging.ParseLevel(envLevel); parseErr == nil {
			level.Set(parsed)
		} else {
			invalidEnvLevel = envLevel
		}
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
	if invalidEnvLevel != "" {
		sdkLogger.Warnw(context.Background(), "invalid "+LogLevelEnvironment,
			"value", invalidEnvLevel)
	}

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

	if p.hostConnections == nil {
		p.hostConnections = &hostconnections.Binding{}
	}
	if consumer, ok := registration.(interface {
		BindHostConnections(*hostconnections.Binding)
	}); ok {
		consumer.BindHostConnections(p.hostConnections)
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

// GRPCServerFactory builds a server with SDK middleware and the default logger. See GRPCServerFactoryWithLogger.
func GRPCServerFactory(opts []grpc.ServerOption) *grpc.Server {
	return GRPCServerFactoryWithLogger(logging.Default(), opts)
}

// GRPCServerFactoryWithLogger builds a server with resource wire limits, SDK middleware and the keepalive of a plugin's server
// (utils.ServerKeepaliveOptions: pings of a host are allowed every 5 s, with or without a stream, and a host that does not answer
// the plugin's ping within 15 s of its last word is dropped, which ends every stream on its connection). The options in opts come
// after the keepalive, so a plugin that needs other times passes grpc.KeepaliveParams or grpc.KeepaliveEnforcementPolicy and its own
// are kept.
func GRPCServerFactoryWithLogger(log logging.Logger, opts []grpc.ServerOption) *grpc.Server {
	return grpc.NewServer(utils.RegisterServerOpts(append(opts, rp.ServerOptions()...), log)...)
}

// GRPCDialOptions returns SDK context propagation and tracing options using the default logger. It sets no keepalive: see
// HostKeepaliveDialOption.
func GRPCDialOptions() []grpc.DialOption {
	return withClientOpts(nil, logging.Default())
}

// GRPCDialOptionsWithLogger returns SDK context propagation and tracing options using log. It sets no keepalive: see
// HostKeepaliveDialOption.
func GRPCDialOptionsWithLogger(log logging.Logger) []grpc.DialOption {
	return withClientOpts(nil, log)
}

// HostKeepaliveDialOption returns the dial option that sets the keepalive a host uses on its connection to a plugin (utils.HostKeepalive:
// ping after 30 s of silence, with or without a stream, give up after 10 s), so that a plugin that stopped answering without closing
// the connection is found, and every stream of the connection fails at once. A host adds it to go-plugin's ClientConfig.GRPCDialOptions,
// next to the SDK's other options:
//
//	GRPCDialOptions: append(sdk.GRPCDialOptions(), sdk.HostKeepaliveDialOption()),
//
// for the plugins that were built with an SDK that serves the matching policy (every plugin from the release that added it, see
// utils.ServerKeepaliveOptions). A plugin that was built before answers a host that pings every 30 s with a GOAWAY and closes the
// connection, which is why GRPCDialOptions does not carry it.
func HostKeepaliveDialOption() grpc.DialOption {
	return grpc.WithKeepaliveParams(utils.HostKeepalive())
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
//
// A host that stops the plugin with go-plugin's Client.Kill first calls the controller's Shutdown, which stops the server at
// once. The server Serve hands go-plugin holds that one call until the capabilities have ended what they hold (the exec
// lane's sessions, whose streams then receive their typed end) or until capabilityPreStopBound has passed, and then lets it
// run. Once the server has stopped, whichever way it was stopped, the capabilities are told again: a plugin that stops
// itself has its sessions ended too, and a second telling after the first is a no-op.
func (p *Plugin) Serve() { p.serveWith(plugin.Serve) }

// serveWith is Serve with go-plugin's entry point given, so that a test serves the plugin in process, through go-plugin's
// test mode, and still runs everything else that Serve does.
func (p *Plugin) serveWith(serve func(*plugin.ServeConfig)) {
	p.profiler = p.startProfiling()
	if p.hostConnections == nil {
		p.hostConnections = &hostconnections.Binding{}
	}
	defer p.hostConnections.Close()
	defer p.activities.Close()
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
	serve(&plugin.ServeConfig{
		HandshakeConfig: p.meta.GenerateHandshakeConfig(),
		VersionedPlugins: map[int]plugin.PluginSet{
			p.effectiveProtocolVersion(): p.pluginMap,
		},
		GRPCServer: p.grpcServer,
	})
	p.shutdownCapabilities(capabilityShutdownBound)
}

// grpcServer builds the server go-plugin serves the plugin with: the SDK's middleware and wire limits, and the hold on the
// controller's Shutdown that shutdownBeforeStop describes. The interceptor is added before the SDK's own, so it runs first.
func (p *Plugin) grpcServer(opts []grpc.ServerOption) *grpc.Server {
	return GRPCServerFactoryWithLogger(p.Log, append(opts, grpc.ChainUnaryInterceptor(p.shutdownBeforeStop(capabilityPreStopBound))))
}

// controllerShutdownMethod is the method of go-plugin's controller service that stops the plugin's gRPC server. A host's
// Client.Kill calls it first, and when the call has returned waits 2 s for the process to exit before it kills it. The
// controller answers it by stopping the server at once: no stream is ended and no call is waited for.
const controllerShutdownMethod = "/plugin.GRPCController/Shutdown"

// capabilityPreStopBound is how long the plugin holds the controller's Shutdown while its capabilities end what they hold (see
// shutdownBeforeStop). The host is blocked in that call for as long, and then waits its 2 s for the process to exit, so the
// bound is short.
const capabilityPreStopBound = 1500 * time.Millisecond

// capabilityShutdownBound is how long the plugin waits for its capabilities to end what they hold once its server has stopped.
const capabilityShutdownBound = 10 * time.Second

// capabilityShutdown is implemented by a capability that holds work to end when the plugin stops: the exec lane's sessions,
// whose functions are cancelled and given their grace, and whose remote processes are told to go. Shutdown returns when the
// capability has ended it, or when ctx ends, with ctx's error.
type capabilityShutdown interface {
	Shutdown(ctx context.Context) error
}

// shutdownCapabilities tells each capability that holds work, in the order of their names, that the plugin is stopping, and
// waits for each at most as long as bound allows in all: one that does not return is given up on, and the plugin goes on
// stopping.
func (p *Plugin) shutdownCapabilities(bound time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), bound)
	defer cancel()
	for _, name := range slices.Sorted(maps.Keys(p.pluginMap)) {
		capability, ok := p.pluginMap[name].(capabilityShutdown)
		if !ok {
			continue
		}
		if err := capability.Shutdown(ctx); err != nil {
			p.Log.Warn(context.Background(), "capability shutdown did not complete", logging.String("capability", name), logging.Error(err))
		}
	}
}

// shutdownBeforeStop returns the unary interceptor that holds the controller's Shutdown, the call that stops the server, until
// the capabilities have been told that the plugin is stopping and have ended what they hold, or until bound has passed, and then
// lets it run, whatever became of them. A stream that a capability still serves can so receive its last frame, as the exec
// lane's sessions receive their typed end, instead of the broken connection that a stopped server leaves it. No other method is
// touched.
func (p *Plugin) shutdownBeforeStop(bound time.Duration) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if info.FullMethod == controllerShutdownMethod {
			p.shutdownCapabilities(bound)
		}
		return handler(ctx, req)
	}
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
			HostConnections:    p.hostConnections,
			Activities:         &p.activities,
			ProfilingStatus:    p.profilingStatus,
			Diagnostics:        diagnostics.Default().Snapshot,
			StartUnixNano:      diagnostics.Default().StartUnixNano(),
		},
	}
}

// Activities returns the process-scoped reporter. Calls fail until the host binds
// the callback service; reporting never grants access to delegated connections.
func (p *Plugin) Activities() activity.Reporter {
	return &p.activities
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
