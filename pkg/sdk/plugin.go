package sdk

import (
	"context"
	"fmt"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

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
	pkgtypes "github.com/omniviewdev/plugin-sdk/pkg/types"
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

// Serve begins serving the plugin over the given RPC server. This should be called
// after all capabilities have been registered.
//
// The lifecycle service is auto-registered before serving — it reads capabilities
// from the plugin map so plugin authors never need to touch it.
//
// When the OMNIVIEW_DEV environment variable is set to "1", the function will:
//   - Intercept the go-plugin handshake line from stdout to capture the listen address
//   - Write a .devinfo file so the IDE can connect via ReattachConfig
//   - Register a signal handler to clean up .devinfo on graceful shutdown
func (p *Plugin) Serve() {
	startupCtx := p.startupContext()

	// Auto-register the lifecycle service from the current plugin map.
	p.registerLifecycle()
	isDev := os.Getenv("OMNIVIEW_DEV") == "1"
	if !isDev {
		p.serveNormal()
		return
	}

	protoVersion := p.effectiveProtocolVersion()

	// Register cleanup handler for graceful shutdown.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		_ = CleanupDevInfo(p.meta.ID)
		os.Exit(0)
	}()

	// Intercept stdout to capture the go-plugin handshake line.
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		p.Log.Error(startupCtx, "failed to create pipe for dev info capture", logging.Error(err))
		p.serveNormal()
		return
	}

	os.Stdout = w

	// Start serving in a goroutine.
	done := make(chan struct{})
	go func() {
		plugin.Serve(&plugin.ServeConfig{
			HandshakeConfig: p.meta.GenerateHandshakeConfig(),
			VersionedPlugins: map[int]plugin.PluginSet{
				protoVersion: p.pluginMap,
			},
			GRPCServer: func(opts []grpc.ServerOption) *grpc.Server {
				return GRPCServerFactoryWithLogger(p.Log, opts)
			},
		})
		close(done)
	}()

	// Read the handshake line from the pipe.
	buf := make([]byte, 4096)
	n, readErr := r.Read(buf)

	// Restore stdout immediately.
	os.Stdout = origStdout

	if readErr != nil {
		p.Log.Error(startupCtx, "failed to read handshake line", logging.Error(readErr))
	} else {
		handshakeLine := string(buf[:n])
		// Write it to real stdout so go-plugin host can read it.
		fmt.Print(handshakeLine)

		// Parse vite port from env (set by CLI tool or manually).
		vitePort := 0
		if vp := os.Getenv("OMNIVIEW_VITE_PORT"); vp != "" {
			vitePort, _ = strconv.Atoi(vp)
		}

		if err := WriteDevInfo(p.meta.ID, p.meta.Version, strings.TrimSpace(handshakeLine), vitePort); err != nil {
			p.Log.Error(startupCtx, "failed to write devinfo", logging.Error(err))
		} else {
			p.Log.Infow(startupCtx, "wrote .devinfo file",
				"plugin_id", p.meta.ID,
				"handshake", strings.TrimSpace(handshakeLine),
			)
		}
	}

	// Wait for serve to complete.
	<-done
	_ = CleanupDevInfo(p.meta.ID)
}

func (p *Plugin) serveNormal() {
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

func (p *Plugin) startupContext() context.Context {
	return pkgtypes.WithPluginContext(context.Background(), &pkgtypes.PluginContext{
		RequestID:   "plugin-startup",
		RequesterID: "plugin-sdk",
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
