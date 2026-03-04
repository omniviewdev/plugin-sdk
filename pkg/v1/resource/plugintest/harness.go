// Package plugintest provides a test harness for plugin authors to mount their
// ResourcePluginConfig and assert behavior (CRUD, watch events, health,
// relationships) without needing gRPC or process spawning.
package plugintest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/resource/resourcetest"
)

// Harness wraps a resource controller and a recording sink, providing a
// fluent test API for plugin authors.
type Harness[ClientT any] struct {
	t          *testing.T
	controller resource.Provider
	sink       *resourcetest.RecordingSink
	cancel     context.CancelFunc
	ctx        context.Context
}

// Mount builds a resource controller from the given config and returns a
// Harness ready for testing. The harness starts a background event listener
// so that watch events are automatically captured by the recording sink.
func Mount[ClientT any](t *testing.T, cfg resource.ResourcePluginConfig[ClientT], opts ...Option[ClientT]) *Harness[ClientT] {
	t.Helper()

	hcfg := &harnessConfig[ClientT]{
		timeout: 10 * time.Second,
	}
	for _, o := range opts {
		o(hcfg)
	}

	parentCtx := hcfg.ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	ctx, cancel := context.WithCancel(parentCtx)

	controller, err := resource.BuildResourceController(ctx, cfg)
	if err != nil {
		cancel()
		t.Fatalf("plugintest.Mount: failed to build controller: %v", err)
	}

	sink := resourcetest.NewRecordingSink()

	// Start background event listener so watch events flow to the sink.
	go func() {
		_ = controller.ListenForEvents(ctx, sink)
	}()

	h := &Harness[ClientT]{
		t:          t,
		controller: controller,
		sink:       sink,
		cancel:     cancel,
		ctx:        ctx,
	}

	t.Cleanup(h.Close)
	return h
}

// --- Connection lifecycle ---

// StartConnection starts a connection and returns its status.
func (h *Harness[ClientT]) StartConnection(id string) types.ConnectionStatus {
	h.t.Helper()
	status, err := h.controller.StartConnection(h.ctx, id)
	if err != nil {
		h.t.Fatalf("StartConnection(%q): %v", id, err)
	}
	return status
}

// StopConnection stops a connection.
func (h *Harness[ClientT]) StopConnection(id string) {
	h.t.Helper()
	_, err := h.controller.StopConnection(h.ctx, id)
	if err != nil {
		h.t.Fatalf("StopConnection(%q): %v", id, err)
	}
}

// LoadConnections loads connections from config.
func (h *Harness[ClientT]) LoadConnections() []types.Connection {
	h.t.Helper()
	conns, err := h.controller.LoadConnections(h.ctx)
	if err != nil {
		h.t.Fatalf("LoadConnections: %v", err)
	}
	return conns
}

// --- CRUD convenience ---

// Get delegates to the controller's Get method.
func (h *Harness[ClientT]) Get(key string, input resource.GetInput) *resource.GetResult {
	h.t.Helper()
	result, err := h.controller.Get(h.ctx, key, input)
	if err != nil {
		h.t.Fatalf("Get(%q): %v", key, err)
	}
	return result
}

// List delegates to the controller's List method.
func (h *Harness[ClientT]) List(key string, input resource.ListInput) *resource.ListResult {
	h.t.Helper()
	result, err := h.controller.List(h.ctx, key, input)
	if err != nil {
		h.t.Fatalf("List(%q): %v", key, err)
	}
	return result
}

// Create delegates to the controller's Create method.
func (h *Harness[ClientT]) Create(key string, input resource.CreateInput) *resource.CreateResult {
	h.t.Helper()
	result, err := h.controller.Create(h.ctx, key, input)
	if err != nil {
		h.t.Fatalf("Create(%q): %v", key, err)
	}
	return result
}

// Update delegates to the controller's Update method.
func (h *Harness[ClientT]) Update(key string, input resource.UpdateInput) *resource.UpdateResult {
	h.t.Helper()
	result, err := h.controller.Update(h.ctx, key, input)
	if err != nil {
		h.t.Fatalf("Update(%q): %v", key, err)
	}
	return result
}

// Delete delegates to the controller's Delete method.
func (h *Harness[ClientT]) Delete(key string, input resource.DeleteInput) *resource.DeleteResult {
	h.t.Helper()
	result, err := h.controller.Delete(h.ctx, key, input)
	if err != nil {
		h.t.Fatalf("Delete(%q): %v", key, err)
	}
	return result
}

// --- Actions ---

// ExecuteAction executes a resource action through the controller.
func (h *Harness[ClientT]) ExecuteAction(key, actionID string, input resource.ActionInput) *resource.ActionResult {
	h.t.Helper()
	result, err := h.controller.ExecuteAction(h.ctx, key, actionID, input)
	if err != nil {
		h.t.Fatalf("ExecuteAction(%q, %q): %v", key, actionID, err)
	}
	return result
}

// --- Watch assertions ---

// WaitForAdds blocks until at least count add events are recorded.
func (h *Harness[ClientT]) WaitForAdds(count int, timeout time.Duration) {
	h.t.Helper()
	h.sink.WaitForAdds(h.t, count, timeout)
}

// WaitForUpdates blocks until at least count update events are recorded.
func (h *Harness[ClientT]) WaitForUpdates(count int, timeout time.Duration) {
	h.t.Helper()
	h.sink.WaitForUpdates(h.t, count, timeout)
}

// WaitForDeletes blocks until at least count delete events are recorded.
func (h *Harness[ClientT]) WaitForDeletes(count int, timeout time.Duration) {
	h.t.Helper()
	h.sink.WaitForDeletes(h.t, count, timeout)
}

// Sink returns the underlying RecordingSink for advanced assertions.
func (h *Harness[ClientT]) Sink() *resourcetest.RecordingSink {
	return h.sink
}

// --- Health & relationships ---

// GetHealth assesses health for a resource from raw data.
func (h *Harness[ClientT]) GetHealth(connID, key string, data json.RawMessage) *resource.ResourceHealth {
	h.t.Helper()
	health, err := h.controller.GetHealth(h.ctx, connID, key, data)
	if err != nil {
		h.t.Fatalf("GetHealth(%q, %q): %v", connID, key, err)
	}
	return health
}

// GetRelationships returns declared relationship descriptors for a resource type.
func (h *Harness[ClientT]) GetRelationships(key string) []resource.RelationshipDescriptor {
	h.t.Helper()
	rels, err := h.controller.GetRelationships(h.ctx, key)
	if err != nil {
		h.t.Fatalf("GetRelationships(%q): %v", key, err)
	}
	return rels
}

// --- Context ---

// Context returns the harness's context, useful for injecting session data.
func (h *Harness[ClientT]) Context() context.Context {
	return h.ctx
}

// Provider returns the underlying resource.Provider for direct access.
func (h *Harness[ClientT]) Provider() resource.Provider {
	return h.controller
}

// --- Cleanup ---

// Close cancels the harness context and shuts down the controller if it
// implements io.Closer (the concrete resourceController does).
func (h *Harness[ClientT]) Close() {
	h.cancel()
	if closer, ok := h.controller.(interface{ Close() }); ok {
		closer.Close()
	}
}
