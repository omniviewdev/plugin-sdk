package exectest

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/utils/timeutil"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
)

// Harness is a test DSL wrapping a real exec.Manager with fake terminals.
type Harness struct {
	t       *testing.T
	Manager *exec.Manager
	Output  *RecordingOutput
	ctx     context.Context
	cancel  context.CancelFunc
}

// HarnessOption configures a Harness.
type HarnessOption func(*harnessConfig)

type harnessConfig struct {
	handlers        map[string]exec.Handler
	terminalFactory exec.TerminalFactory
	clock           timeutil.Clock
	logger          hclog.Logger
}

// WithHandler registers a handler with the harness.
func WithHandler(h exec.Handler) HarnessOption {
	return func(c *harnessConfig) {
		c.handlers[h.ID()] = h
	}
}

// WithTerminalFactory overrides the terminal factory.
func WithTerminalFactory(tf exec.TerminalFactory) HarnessOption {
	return func(c *harnessConfig) {
		c.terminalFactory = tf
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

// Mount creates a new Harness. The harness is automatically cleaned up via
// t.Cleanup().
func Mount(t *testing.T, opts ...HarnessOption) *Harness {
	t.Helper()

	cfg := &harnessConfig{
		handlers: make(map[string]exec.Handler),
	}
	for _, o := range opts {
		o(cfg)
	}

	if cfg.terminalFactory == nil {
		cfg.terminalFactory = NewFakeTerminalFactory()
	}
	if cfg.logger == nil {
		cfg.logger = hclog.NewNullLogger()
	}
	if cfg.clock == nil {
		cfg.clock = timeutil.RealClock{}
	}

	output := NewRecordingOutput()
	ctx, cancel := context.WithCancel(context.Background())

	mgr := exec.NewManager(exec.ManagerConfig{
		Logger:          cfg.logger,
		Handlers:        cfg.handlers,
		Sink:            output,
		TerminalFactory: cfg.terminalFactory,
		Clock:           cfg.clock,
	})

	h := &Harness{
		t:       t,
		Manager: mgr,
		Output:  output,
		ctx:     ctx,
		cancel:  cancel,
	}

	t.Cleanup(func() {
		cancel()
		mgr.Close()
	})

	return h
}

// CreateSession creates a session using the harness context.
func (h *Harness) CreateSession(opts exec.SessionOptions) (*exec.Session, error) {
	h.t.Helper()
	pctx := newTestPluginContext(h.ctx)
	return h.Manager.CreateSession(pctx, opts)
}

// CloseSession closes a session by ID.
func (h *Harness) CloseSession(sessionID string) error {
	h.t.Helper()
	pctx := newTestPluginContext(h.ctx)
	return h.Manager.CloseSession(pctx, sessionID)
}

// GetSession retrieves a session by ID.
func (h *Harness) GetSession(sessionID string) (*exec.Session, error) {
	h.t.Helper()
	pctx := newTestPluginContext(h.ctx)
	return h.Manager.GetSession(pctx, sessionID)
}

// AttachSession attaches to a session.
func (h *Harness) AttachSession(sessionID string) (*exec.Session, []byte, error) {
	h.t.Helper()
	pctx := newTestPluginContext(h.ctx)
	return h.Manager.AttachSession(pctx, sessionID)
}

// DetachSession detaches from a session.
func (h *Harness) DetachSession(sessionID string) (*exec.Session, error) {
	h.t.Helper()
	pctx := newTestPluginContext(h.ctx)
	return h.Manager.DetachSession(pctx, sessionID)
}

// WaitForClose blocks until the close signal for a session is emitted.
func (h *Harness) WaitForClose(timeout time.Duration) *exec.StreamOutput {
	return h.Output.WaitForSignal(exec.StreamSignalClose, timeout)
}

// newTestPluginContext builds a minimal PluginContext for tests.
func newTestPluginContext(ctx context.Context) *types.PluginContext {
	return &types.PluginContext{
		Context: ctx,
	}
}

// NoopTTYHandler returns a TTYHandlerFunc that does nothing — the session
// stays open until context cancellation or stopCh signal.
func NoopTTYHandler() exec.TTYHandlerFunc {
	return func(
		_ *types.PluginContext,
		_ exec.SessionOptions,
		_ *os.File,
		_ chan error,
		_ <-chan exec.SessionResizeInput,
	) error {
		return nil
	}
}

// FailingTTYHandler returns a TTYHandlerFunc that immediately returns an error.
func FailingTTYHandler(err error) exec.TTYHandlerFunc {
	return func(
		_ *types.PluginContext,
		_ exec.SessionOptions,
		_ *os.File,
		_ chan error,
		_ <-chan exec.SessionResizeInput,
	) error {
		return err
	}
}

// StopChTTYHandler returns a TTYHandlerFunc that sends the given error on
// the stopCh after a delay, simulating a handler that fails after starting.
// The send is non-blocking so the goroutine never leaks if the session ends early.
func StopChTTYHandler(stopErr error, delay time.Duration) exec.TTYHandlerFunc {
	return func(
		_ *types.PluginContext,
		_ exec.SessionOptions,
		_ *os.File,
		stopCh chan error,
		_ <-chan exec.SessionResizeInput,
	) error {
		go func() {
			time.Sleep(delay)
			select {
			case stopCh <- stopErr:
			default:
			}
		}()
		return nil
	}
}
