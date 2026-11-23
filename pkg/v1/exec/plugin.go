package exec

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	logging "github.com/omniviewdev/plugin-sdk/log"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
	execpb "github.com/omniviewdev/plugin-sdk/proto/v1/exec"
)

// Provider is the interface satisfied by the plugin server and client
// to provide the exec functionality.
type Provider interface {
	// GetSupportedResources returns the supported resource types
	GetSupportedResources(ctx *types.PluginContext) []Handler
	// GetSession returns a session by ID
	GetSession(ctx *types.PluginContext, sessionID string) (*Session, error)
	// ListSessions returns all of the sessions
	ListSessions(ctx *types.PluginContext) ([]*Session, error)
	// CreateSession creates a new session
	CreateSession(ctx *types.PluginContext, opts SessionOptions) (*Session, error)
	// AttachSession attaches a session
	AttachSession(ctx *types.PluginContext, sessionID string) (*Session, []byte, error)
	// DetachSession detaches a session
	DetachSession(ctx *types.PluginContext, sessionID string) (*Session, error)
	// CloseSession closes a session
	CloseSession(ctx *types.PluginContext, sessionID string) error
	// ResizeSession resizes a session
	ResizeSession(ctx *types.PluginContext, sessionID string, rows, cols int32) error
	// Stream starts a new stream to multiplex sessions
	Stream(context.Context, chan StreamInput) (chan StreamOutput, error)
	// Close shuts down the provider, releasing all resources.
	Close()
}

// Plugin exposes the exec lanes of a plugin through HashiCorp's gRPC plugin protocol: the legacy lane through Impl
// (omniview.sdk.exec.v1) and the v2 lane through the targets it was built with (omniview.sdk.exec.v2). The host
// dispenses a zero Plugin to get a client for both. Build a Plugin that serves targets with NewTargetPlugin.
//
// Plugin is comparable, as it was before the v2 lane existed: it holds no mutex, slice or map of its own, and the
// state of the v2 lane (the registry of the targets, their server, the bound connection registry) lives behind one
// pointer. A Plugin that was not built by NewTargetPlugin, such as the zero Plugin the host dispenses, has no v2 lane.
type Plugin struct {
	plugin.Plugin

	// Impl serves the legacy lane. Nil on a plugin that serves only targets.
	Impl Provider

	v2 *targetCapability
}

// targetCapability is the v2 lane of a Plugin: the server of its targets and the connection registry bound to it.
type targetCapability struct {
	mu          sync.Mutex
	server      *serverV2
	connections resourcev2.ConnectionContextProvider
}

// connectionProvider returns the bound connection registry, or nil.
func (c *targetCapability) connectionProvider() resourcev2.ConnectionContextProvider {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connections
}

// NewTargetPlugin returns a Plugin that serves targets on the v2 lane. The targets are validated here, so an invalid
// target (a missing routing key or Exec, a duplicate key, a capability or limit that breaks the contract) is an
// error at construction. No targets is a Plugin with no v2 lane. Set Impl on the result to serve the legacy lane
// beside it; RegisterPlugin does.
func NewTargetPlugin(targets []Target) (*Plugin, error) {
	if len(targets) == 0 {
		return &Plugin{}, nil
	}
	registry, err := newTargetRegistry(targets)
	if err != nil {
		return nil, err
	}
	capability := &targetCapability{}
	capability.server = newServerV2(logging.Default().Named("exec.v2"), registry, capability.connectionProvider)
	return &Plugin{v2: capability}, nil
}

// BindConnectionContextProvider forwards registry binding to the native provider of the legacy lane and keeps it for
// the v2 lane, whose Options runs inside the connection lease.
func (p *Plugin) BindConnectionContextProvider(provider resourcev2.ConnectionContextProvider) {
	if consumer, ok := p.Impl.(resourcev2.ConnectionContextConsumer); ok {
		consumer.BindConnectionContextProvider(provider)
	}
	if p.v2 == nil {
		return
	}
	p.v2.mu.Lock()
	p.v2.connections = provider
	p.v2.mu.Unlock()
}

// GRPCServer registers the exec RPCs on s: the legacy service backed by Impl (unless the plugin serves only
// targets) and the v2 service backed by the targets (when there are any).
func (p *Plugin) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	var v2 *serverV2
	if p.v2 != nil {
		v2 = p.v2.server
	}
	if p.Impl != nil || v2 == nil {
		RegisterExecPluginServer(s, &PluginServer{
			log:  logging.Default().Named("exec.plugin.server"),
			Impl: p.Impl,
		})
	}
	if v2 != nil {
		execv2.RegisterExecServer(s, v2)
	}
	return nil
}

// GRPCClient constructs the host-side exec provider using c. The client holds both lanes; PluginClient.V2 returns
// the v2 one.
func (p *Plugin) GRPCClient(
	_ context.Context,
	_ *plugin.GRPCBroker,
	c *grpc.ClientConn,
) (any, error) {
	return &PluginClient{
		client: execpb.NewExecPluginClient(c),
		log:    logging.Default().Named("exec.plugin.client"),
		v2:     newClientV2(execv2.NewExecClient(c), logging.Default().Named("exec.v2.client")),
	}, nil
}

// RegisterPlugin installs the exec capability after rejecting duplicate handler keys and invalid targets. A plugin
// that supplies Handlers, or neither Handlers nor Targets, serves the legacy lane exactly as before; one that
// supplies Targets serves the v2 lane; one that supplies both serves both.
func RegisterPlugin(
	p *sdk.Plugin,
	opts PluginOpts,
) error {
	if p == nil {
		return errors.New("plugin is nil")
	}

	handlers := make(map[string]Handler, len(opts.Handlers))
	for i := range opts.Handlers {
		handler := &opts.Handlers[i]
		key := handler.Plugin + "/" + handler.Resource
		if _, exists := handlers[key]; exists {
			return fmt.Errorf("duplicate exec handler key %q", key)
		}
		handlers[key] = *handler
	}

	capability, err := NewTargetPlugin(opts.Targets)
	if err != nil {
		return err
	}
	if capability.v2 != nil {
		capability.v2.server.log = p.Log.Named("exec.v2")
	}
	if len(opts.Handlers) > 0 || len(opts.Targets) == 0 {
		capability.Impl = NewManager(ManagerConfig{
			Logger:   p.Log.Named("exec"),
			Settings: p.SettingsProvider,
			Handlers: handlers,
		})
	}
	p.RegisterCapability("exec", capability)
	return nil
}
