package logtest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	logs "github.com/omniviewdev/plugin-sdk/pkg/v1/logs"
)

// Harness provides a fluent test DSL for testing the logs Manager.
// It wraps a real Manager with TestLogSources wired in.
type Harness struct {
	t       *testing.T
	manager *logs.Manager
	output  *RecordingOutput
	sources map[string]*TestLogSource
	ctx     context.Context
	cancel  context.CancelFunc
}

// HarnessOption configures the Harness.
type HarnessOption func(*harnessConfig)

type harnessConfig struct {
	sources   map[string]*TestLogSource
	resolvers map[string]logs.SourceResolver
}

// WithSource adds a named test source to the harness.
// The source ID is used as both the handler key and source ID.
func WithSource(sourceID string, src *TestLogSource) HarnessOption {
	return func(c *harnessConfig) {
		c.sources[sourceID] = src
	}
}

// WithResolver adds a source resolver for a resource key.
func WithResolver(resourceKey string, resolver logs.SourceResolver) HarnessOption {
	return func(c *harnessConfig) {
		c.resolvers[resourceKey] = resolver
	}
}

// Mount creates a new Harness with a real Manager and TestLogSources wired in.
// The harness is cleaned up automatically via t.Cleanup.
func Mount(t *testing.T, opts ...HarnessOption) *Harness {
	t.Helper()

	cfg := &harnessConfig{
		sources:   make(map[string]*TestLogSource),
		resolvers: make(map[string]logs.SourceResolver),
	}
	for _, opt := range opts {
		opt(cfg)
	}

	// Build handlers map from sources
	handlers := make(map[string]logs.Handler)
	for sourceID, src := range cfg.sources {
		if src == nil {
			continue
		}
		handlers["test/"+sourceID] = logs.Handler{
			Plugin:        "test",
			Resource:      sourceID,
			Handler:       src.HandlerFunc(),
			SourceBuilder: SingleSourceBuilder(sourceID, map[string]string{"source": sourceID}),
		}
	}

	output := NewRecordingOutput()
	ctx, cancel := context.WithCancel(context.Background())

	manager := logs.NewManager(logs.ManagerConfig{
		Logger:    hclog.NewNullLogger(),
		Handlers:  handlers,
		Resolvers: cfg.resolvers,
		Sink:      output, // inject directly — no channel, no consumer goroutine
	})

	h := &Harness{
		t:       t,
		manager: manager,
		output:  output,
		sources: cfg.sources,
		ctx:     ctx,
		cancel:  cancel,
	}

	t.Cleanup(func() {
		h.Close()
	})

	return h
}

// CreateSession creates a log session for the given resource key.
func (h *Harness) CreateSession(resourceKey string, opts ...SessionOption) *logs.LogSession {
	h.t.Helper()

	cfg := sessionConfig{
		resourceID:   "test-resource",
		resourceData: map[string]interface{}{},
		options:      logs.LogSessionOptions{Follow: true, IncludeSourceEvents: true},
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	pctx := newTestPluginContext(h.ctx)
	session, err := h.manager.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey:  resourceKey,
		ResourceID:   cfg.resourceID,
		ResourceData: cfg.resourceData,
		Options:      cfg.options,
	})
	if err != nil {
		h.t.Fatalf("CreateSession failed: %v", err)
	}

	// Record the initial session status
	h.output.RecordStatus(session.Status)
	return session
}

// GetSession retrieves the current state of a session.
func (h *Harness) GetSession(sessionID string) *logs.LogSession {
	h.t.Helper()
	pctx := newTestPluginContext(h.ctx)
	session, err := h.manager.GetSession(pctx, sessionID)
	if err != nil {
		h.t.Fatalf("GetSession failed: %v", err)
	}
	return session
}

// CloseSession closes the given session.
func (h *Harness) CloseSession(sessionID string) {
	h.t.Helper()
	pctx := newTestPluginContext(h.ctx)
	if err := h.manager.CloseSession(pctx, sessionID); err != nil {
		h.t.Fatalf("CloseSession failed: %v", err)
	}
}

// FeedLine sends a line to the named source.
func (h *Harness) FeedLine(sourceID string, line string) {
	h.t.Helper()
	src, ok := h.sources[sourceID]
	if !ok {
		h.t.Fatalf("source %q not found in harness", sourceID)
	}
	src.Lines <- line
}

// CloseSource closes the Lines channel for the named source, ending its stream.
func (h *Harness) CloseSource(sourceID string) {
	h.t.Helper()
	src, ok := h.sources[sourceID]
	if !ok {
		h.t.Fatalf("source %q not found in harness", sourceID)
	}
	close(src.Lines)
}

// WaitForLines waits until at least count lines have been received.
func (h *Harness) WaitForLines(count int, timeout time.Duration) []logs.LogLine {
	h.t.Helper()
	return h.output.WaitForLines(count, timeout)
}

// WaitForEvent waits until an event of the given type is received.
func (h *Harness) WaitForEvent(eventType logs.LogStreamEventType, timeout time.Duration) *logs.LogStreamEvent {
	h.t.Helper()
	return h.output.WaitForEvent(eventType, timeout)
}

// WaitForStatus waits until the given status has been recorded.
func (h *Harness) WaitForStatus(status logs.LogSessionStatus, timeout time.Duration) bool {
	h.t.Helper()
	return h.output.WaitForStatus(status, timeout)
}

// Output returns the recording output for direct inspection.
func (h *Harness) Output() *RecordingOutput {
	return h.output
}

// Manager returns the underlying Manager for direct access.
func (h *Harness) Manager() *logs.Manager {
	return h.manager
}

// Close shuts down the harness by closing all sessions and waiting for goroutines.
func (h *Harness) Close() {
	h.cancel()
	h.manager.Close()
}

// SessionOption configures session creation in the harness.
type SessionOption func(*sessionConfig)

type sessionConfig struct {
	resourceID   string
	resourceData map[string]interface{}
	options      logs.LogSessionOptions
}

// WithResourceID sets the resource ID for session creation.
func WithResourceID(id string) SessionOption {
	return func(c *sessionConfig) { c.resourceID = id }
}

// WithResourceData sets the resource data for session creation.
func WithResourceData(data map[string]interface{}) SessionOption {
	return func(c *sessionConfig) { c.resourceData = data }
}

// WithSessionOptions sets the session options.
func WithSessionOptions(opts logs.LogSessionOptions) SessionOption {
	return func(c *sessionConfig) { c.options = opts }
}

func newTestPluginContext(ctx context.Context) *types.PluginContext {
	return types.NewPluginContext(ctx, "log", nil, nil, nil)
}

// AssertEventTypes checks that all expected event types have been recorded, in order.
func (h *Harness) AssertEventTypes(expected ...logs.LogStreamEventType) {
	h.t.Helper()
	events := h.output.Events()
	if len(events) < len(expected) {
		h.t.Fatalf("expected %d events, got %d: %v", len(expected), len(events), eventTypeNames(events))
	}
	for i, want := range expected {
		if events[i].Type != want {
			h.t.Errorf("event[%d]: expected type %d, got %d", i, want, events[i].Type)
		}
	}
}

// ContextWithCancelFrom returns a PluginContext using the harness's context.
func ContextWithCancelFrom(h *Harness) *types.PluginContext {
	return newTestPluginContext(h.ctx)
}

func eventTypeNames(events []logs.LogStreamEvent) []string {
	names := make([]string, len(events))
	for i, e := range events {
		names[i] = fmt.Sprintf("%d", e.Type)
	}
	return names
}
