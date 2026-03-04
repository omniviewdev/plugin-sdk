package networktest

import (
	"context"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/utils/timeutil"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/networker"
)

// Harness is a test DSL wrapping a real networker.Manager.
type Harness struct {
	t       *testing.T
	Manager *networker.Manager
	ctx     context.Context
	cancel  context.CancelFunc
}

// HarnessOption configures a Harness.
type HarnessOption func(*harnessConfig)

type harnessConfig struct {
	resourceForwarders map[string]networker.ResourceForwarder
	staticForwarders   map[string]networker.StaticForwarder
	portChecker        networker.PortChecker
	clock              timeutil.Clock
	logger             hclog.Logger
}

// WithResourceForwarder registers a resource forwarder.
func WithResourceForwarder(key string, f networker.ResourceForwarder) HarnessOption {
	return func(c *harnessConfig) {
		c.resourceForwarders[key] = f
	}
}

// WithStaticForwarder registers a static forwarder.
func WithStaticForwarder(key string, f networker.StaticForwarder) HarnessOption {
	return func(c *harnessConfig) {
		c.staticForwarders[key] = f
	}
}

// WithPortChecker overrides the port checker.
func WithPortChecker(pc networker.PortChecker) HarnessOption {
	return func(c *harnessConfig) {
		c.portChecker = pc
	}
}

// WithClock injects a custom clock.
func WithClock(clk timeutil.Clock) HarnessOption {
	return func(c *harnessConfig) {
		c.clock = clk
	}
}

// WithLogger sets a custom logger.
func WithLogger(l hclog.Logger) HarnessOption {
	return func(c *harnessConfig) {
		c.logger = l
	}
}

// Mount creates a new Harness. Cleaned up via t.Cleanup().
func Mount(t *testing.T, opts ...HarnessOption) *Harness {
	t.Helper()

	cfg := &harnessConfig{
		resourceForwarders: make(map[string]networker.ResourceForwarder),
		staticForwarders:   make(map[string]networker.StaticForwarder),
	}
	for _, o := range opts {
		o(cfg)
	}

	if cfg.portChecker == nil {
		cfg.portChecker = NewFakePortChecker(10000)
	}
	if cfg.logger == nil {
		cfg.logger = hclog.NewNullLogger()
	}
	if cfg.clock == nil {
		cfg.clock = timeutil.RealClock{}
	}

	ctx, cancel := context.WithCancel(context.Background())

	mgr := networker.NewManager(networker.ManagerConfig{
		Logger:      cfg.logger,
		PortChecker: cfg.portChecker,
		Clock:       cfg.clock,
	}, networker.PluginOpts{
		ResourceForwarders: cfg.resourceForwarders,
		StaticForwarders:   cfg.staticForwarders,
	})

	h := &Harness{
		t:       t,
		Manager: mgr,
		ctx:     ctx,
		cancel:  cancel,
	}

	t.Cleanup(func() {
		cancel()
		mgr.StopAll()
	})

	return h
}

// StartSession is a convenience method to start a port forward session.
func (h *Harness) StartSession(opts networker.PortForwardSessionOptions) (*networker.PortForwardSession, error) {
	h.t.Helper()
	pctx := newTestPluginContext(h.ctx)
	return h.Manager.StartPortForwardSession(pctx, opts)
}

// CloseSession closes a port forward session by ID.
func (h *Harness) CloseSession(sessionID string) (*networker.PortForwardSession, error) {
	h.t.Helper()
	pctx := newTestPluginContext(h.ctx)
	return h.Manager.ClosePortForwardSession(pctx, sessionID)
}

// GetSession retrieves a session by ID.
func (h *Harness) GetSession(sessionID string) (*networker.PortForwardSession, error) {
	h.t.Helper()
	pctx := newTestPluginContext(h.ctx)
	return h.Manager.GetPortForwardSession(pctx, sessionID)
}

// ListSessions returns all sessions.
func (h *Harness) ListSessions() ([]*networker.PortForwardSession, error) {
	h.t.Helper()
	pctx := newTestPluginContext(h.ctx)
	return h.Manager.ListPortForwardSessions(pctx)
}

func newTestPluginContext(ctx context.Context) *types.PluginContext {
	return &types.PluginContext{
		Context: ctx,
	}
}

// TestPluginCtx returns a minimal PluginContext for use in tests outside the harness.
func TestPluginCtx() *types.PluginContext {
	return &types.PluginContext{
		Context: context.Background(),
	}
}
