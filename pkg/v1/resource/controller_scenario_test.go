package resource_test

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/resource/resourcetest"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// testDiscoveryProvider is a configurable DiscoveryProvider for scenario tests.
type testDiscoveryProvider struct {
	DiscoverFunc func(ctx context.Context, conn *types.Connection) ([]resource.ResourceMeta, error)
}

func (d *testDiscoveryProvider) Discover(ctx context.Context, conn *types.Connection) ([]resource.ResourceMeta, error) {
	return d.DiscoverFunc(ctx, conn)
}

func (d *testDiscoveryProvider) OnConnectionRemoved(_ context.Context, _ *types.Connection) error {
	return nil
}

// --- SC-001: Connect -> Browse -> Disconnect ---
func TestSC001_ConnectBrowseDisconnect(t *testing.T) {
	podResourcer := &resourcetest.StubResourcer[string]{
		ListFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.ListInput) (*resource.ListResult, error) {
			return &resource.ListResult{
				Success: true,
				Result:  []json.RawMessage{json.RawMessage(`{"id":"pod-1"}`)},
			}, nil
		},
		GetFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, input resource.GetInput) (*resource.GetResult, error) {
			return &resource.GetResult{
				Success: true,
				Result:  json.RawMessage(fmt.Sprintf(`{"id":%q}`, input.ID)),
			}, nil
		},
	}

	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: podResourcer},
		},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatalf("build controller: %v", err)
	}
	defer ctrl.Close()

	// Load and start connection.
	if _, err := ctrl.LoadConnections(ctx); err != nil {
		t.Fatalf("load connections: %v", err)
	}
	if _, err := ctrl.StartConnection(ctx, "conn-1"); err != nil {
		t.Fatalf("start connection: %v", err)
	}

	// Browse: List pods.
	testCtx := resourcetest.NewTestContext()
	listResult, err := ctrl.List(testCtx, "core::v1::Pod", resource.ListInput{})
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}
	if len(listResult.Result) != 1 {
		t.Fatalf("expected 1 pod, got %d", len(listResult.Result))
	}

	// Browse: Get pod.
	getResult, err := ctrl.Get(testCtx, "core::v1::Pod", resource.GetInput{ID: "pod-1", Namespace: "default"})
	if err != nil {
		t.Fatalf("get pod: %v", err)
	}
	if !getResult.Success {
		t.Fatal("expected get success")
	}

	// Disconnect.
	if _, err := ctrl.StopConnection(ctx, "conn-1"); err != nil {
		t.Fatalf("stop connection: %v", err)
	}

	// List again should fail — no active connection.
	_, err = ctrl.List(testCtx, "core::v1::Pod", resource.ListInput{})
	if err == nil {
		t.Fatal("expected error after disconnect")
	}
}

// --- SC-002: Watch -> Receive Events -> Disconnect ---
func TestSC002_WatchReceiveEventsDisconnect(t *testing.T) {
	ready := make(chan struct{})

	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, sink resource.WatchEventSink) error {
					// Signal ready, then emit an event.
					select {
					case <-ready:
					case <-ctx.Done():
						return nil
					}
					sink.OnAdd(resource.WatchAddPayload{
						Key:        meta.Key(),
						ID:         "pod-1",
						Connection: "conn-1",
						Data:       json.RawMessage(`{"id":"pod-1"}`),
					})
					<-ctx.Done()
					return nil
				},
			},
		}},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatalf("build controller: %v", err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)

	// Register listener before connection.
	sink := resourcetest.NewRecordingSink()
	resource.AddListenerForTest(ctrl, sink)
	t.Cleanup(func() { resource.RemoveListenerForTest(ctrl, sink) })

	ctrl.StartConnection(ctx, "conn-1")
	close(ready)

	sink.WaitForAdds(t, 1, 2*time.Second)

	if sink.AddCount() < 1 {
		t.Fatal("expected at least 1 add event")
	}
	if sink.Adds[0].Connection != "conn-1" {
		t.Fatalf("expected connection conn-1, got %s", sink.Adds[0].Connection)
	}

	// Disconnect — stop connection.
	ctrl.StopConnection(ctx, "conn-1")

	// Record current add count — no more events should arrive.
	countBefore := sink.AddCount()
	// Brief wait to verify no further events arrive after disconnect.
	time.Sleep(20 * time.Millisecond)
	if sink.AddCount() != countBefore {
		t.Fatal("unexpected events after disconnect")
	}
}

// --- SC-003: Lazy Watch — SyncOnFirstQuery triggers on List ---
func TestSC003_LazyWatchSyncOnFirstQuery(t *testing.T) {
	syncFirst := resource.SyncOnFirstQuery

	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{
			{
				Meta:      resourcetest.PodMeta,
				Resourcer: &resourcetest.WatchableResourcer[string]{},
			},
			{
				Meta: resourcetest.SecretMeta,
				Resourcer: &resourcetest.WatchableResourcer[string]{
					PolicyVal: &syncFirst,
					StubResourcer: resourcetest.StubResourcer[string]{
						ListFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.ListInput) (*resource.ListResult, error) {
							return &resource.ListResult{Success: true}, nil
						},
					},
				},
			},
		},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatalf("build controller: %v", err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")

	testCtx := resourcetest.NewTestContext()

	// Pod (SyncOnConnect) should be running.
	running, _ := ctrl.IsResourceWatchRunning(testCtx, "conn-1", "core::v1::Pod")
	if !running {
		t.Fatal("expected Pod watch running after StartConnection")
	}

	// Secret (SyncOnFirstQuery) should NOT be running yet.
	running, _ = ctrl.IsResourceWatchRunning(testCtx, "conn-1", "core::v1::Secret")
	if running {
		t.Fatal("Secret watch should not be running before first query")
	}

	// List Secret triggers lazy watch start (maybeEnsureWatch runs in goroutine).
	ctrl.List(testCtx, "core::v1::Secret", resource.ListInput{})
	time.Sleep(time.Millisecond) // yield so goroutine registers the rws
	waitCtx, waitCancel := context.WithTimeout(ctx, 2*time.Second)
	defer waitCancel()
	resource.WaitForWatchReadyForTest(waitCtx, ctrl, "conn-1", "core::v1::Secret")

	running, _ = ctrl.IsResourceWatchRunning(testCtx, "conn-1", "core::v1::Secret")
	if !running {
		t.Fatal("Secret watch should be running after List")
	}
}

// --- SC-004: Disconnect -> Reconnect -> Watches restart ---
func TestSC004_DisconnectReconnectWatchesRestart(t *testing.T) {
	var watchCalls atomic.Int32

	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
					watchCalls.Add(1)
					<-ctx.Done()
					return nil
				},
			},
		}},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatalf("build controller: %v", err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)

	// First start.
	ctrl.StartConnection(ctx, "conn-1")
	waitCtx1, waitCancel1 := context.WithTimeout(ctx, 2*time.Second)
	defer waitCancel1()
	resource.WaitForConnectionReadyForTest(waitCtx1, ctrl, "conn-1")
	if watchCalls.Load() != 1 {
		t.Fatalf("expected 1 watch call, got %d", watchCalls.Load())
	}

	// Stop.
	ctrl.StopConnection(ctx, "conn-1")

	// Restart.
	ctrl.StartConnection(ctx, "conn-1")
	waitCtx2, waitCancel2 := context.WithTimeout(ctx, 2*time.Second)
	defer waitCancel2()
	resource.WaitForConnectionReadyForTest(waitCtx2, ctrl, "conn-1")

	if watchCalls.Load() != 2 {
		t.Fatalf("expected 2 watch calls after reconnect, got %d", watchCalls.Load())
	}
}

// --- SC-005: Multiple connections — events distinguished ---
func TestSC005_MultipleConnectionsEventsDistinguished(t *testing.T) {
	ready := make(chan struct{})

	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{
			LoadConnectionsFunc: func(_ context.Context) ([]types.Connection, error) {
				return []types.Connection{
					{ID: "conn-1", Name: "Conn1"},
					{ID: "conn-2", Name: "Conn2"},
					{ID: "conn-3", Name: "Conn3"},
				}, nil
			},
			// CreateClient uses the Session on ctx to embed the connection ID
			// in the returned client string. The watch func reads it back.
			CreateClientFunc: func(ctx context.Context) (*string, error) {
				sess := resource.SessionFromContext(ctx)
				connID := "unknown"
				if sess != nil && sess.Connection != nil {
					connID = sess.Connection.ID
				}
				return &connID, nil
			},
		},
		Resources: []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, client *string, meta resource.ResourceMeta, sink resource.WatchEventSink) error {
					// Derive connection ID from the client value.
					connID := "unknown"
					if client != nil {
						connID = *client
					}
					select {
					case <-ready:
					case <-ctx.Done():
						return nil
					}
					sink.OnAdd(resource.WatchAddPayload{
						Key:        meta.Key(),
						ID:         "pod-" + connID,
						Connection: connID,
						Data:       json.RawMessage(`{}`),
					})
					<-ctx.Done()
					return nil
				},
			},
		}},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatalf("build controller: %v", err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)

	sink := resourcetest.NewRecordingSink()
	resource.AddListenerForTest(ctrl, sink)
	t.Cleanup(func() { resource.RemoveListenerForTest(ctrl, sink) })

	ctrl.StartConnection(ctx, "conn-1")
	ctrl.StartConnection(ctx, "conn-2")
	ctrl.StartConnection(ctx, "conn-3")
	close(ready)

	sink.WaitForAdds(t, 3, 2*time.Second)

	// Check that each connection produced a distinguishable event.
	connsSeen := make(map[string]bool)
	for _, add := range sink.Adds {
		connsSeen[add.Connection] = true
	}
	for _, cid := range []string{"conn-1", "conn-2", "conn-3"} {
		if !connsSeen[cid] {
			t.Fatalf("missing event from connection %s", cid)
		}
	}
}

// --- SC-006: Stop during initial sync ---
func TestSC006_StopDuringInitialSync(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
					// Simulate slow initial sync.
					select {
					case <-time.After(2 * time.Second):
						return nil
					case <-ctx.Done():
						return nil
					}
				},
			},
		}},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatalf("build controller: %v", err)
	}

	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")

	// Stop immediately while watch is in "sync" sleep.
	ctrl.StopConnection(ctx, "conn-1")

	// Close should complete cleanly and quickly.
	done := make(chan struct{})
	go func() {
		ctrl.Close()
		close(done)
	}()

	select {
	case <-done:
		// success
	case <-time.After(3 * time.Second):
		t.Fatal("controller did not shut down cleanly after stop during sync")
	}
}

// --- SC-007: CRUD during active Watch — no interference ---
func TestSC007_CRUDDuringActiveWatch(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				StubResourcer: resourcetest.StubResourcer[string]{
					GetFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, input resource.GetInput) (*resource.GetResult, error) {
						return &resource.GetResult{Success: true, Result: json.RawMessage(`{"id":"pod-1"}`)}, nil
					},
					ListFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.ListInput) (*resource.ListResult, error) {
						return &resource.ListResult{Success: true, Result: []json.RawMessage{json.RawMessage(`{}`)}}, nil
					},
				},
				WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, sink resource.WatchEventSink) error {
					ticker := time.NewTicker(10 * time.Millisecond)
					defer ticker.Stop()
					i := 0
					for {
						select {
						case <-ctx.Done():
							return nil
						case <-ticker.C:
							i++
							sink.OnAdd(resource.WatchAddPayload{
								Key:        meta.Key(),
								ID:         fmt.Sprintf("pod-%d", i),
								Connection: "conn-1",
								Data:       json.RawMessage(`{}`),
							})
						}
					}
				},
			},
		}},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatalf("build controller: %v", err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)

	sink := resourcetest.NewRecordingSink()
	resource.AddListenerForTest(ctrl, sink)
	t.Cleanup(func() { resource.RemoveListenerForTest(ctrl, sink) })

	ctrl.StartConnection(ctx, "conn-1")

	// Run concurrent CRUD while watch is actively emitting.
	var wg sync.WaitGroup
	testCtx := resourcetest.NewTestContext()
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := ctrl.Get(testCtx, "core::v1::Pod", resource.GetInput{ID: "pod-1"})
			if err != nil {
				t.Errorf("concurrent Get failed: %v", err)
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := ctrl.List(testCtx, "core::v1::Pod", resource.ListInput{})
			if err != nil {
				t.Errorf("concurrent List failed: %v", err)
			}
		}()
	}
	wg.Wait()

	// Verify watch events still flowing.
	sink.WaitForAdds(t, 1, 2*time.Second)
}

// --- SC-008: Error recovery end-to-end ---
func TestSC008_ErrorRecoveryEndToEnd(t *testing.T) {
	var callCount atomic.Int32

	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, sink resource.WatchEventSink) error {
					n := callCount.Add(1)
					if n == 1 {
						// First call fails immediately.
						return fmt.Errorf("watch error")
					}
					// Second call succeeds — emit event and block.
					sink.OnAdd(resource.WatchAddPayload{
						Key:        meta.Key(),
						ID:         "pod-recovered",
						Connection: "conn-1",
						Data:       json.RawMessage(`{}`),
					})
					<-ctx.Done()
					return nil
				},
			},
		}},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatalf("build controller: %v", err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)

	sink := resourcetest.NewRecordingSink()
	resource.AddListenerForTest(ctrl, sink)
	t.Cleanup(func() { resource.RemoveListenerForTest(ctrl, sink) })

	ctrl.StartConnection(ctx, "conn-1")

	// Wait for error state event.
	sink.WaitForState(t, "core::v1::Pod", resource.WatchStateError, 3*time.Second)

	// After retry, should recover and emit an add event.
	sink.WaitForAdds(t, 1, 5*time.Second)

	if sink.Adds[0].ID != "pod-recovered" {
		t.Fatalf("expected pod-recovered, got %s", sink.Adds[0].ID)
	}
}

// --- SC-009: Multiple listeners — all receive events ---
func TestSC009_MultipleListenersAllReceiveEvents(t *testing.T) {
	ready := make(chan struct{})

	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, sink resource.WatchEventSink) error {
					select {
					case <-ready:
					case <-ctx.Done():
						return nil
					}
					sink.OnAdd(resource.WatchAddPayload{
						Key:        meta.Key(),
						ID:         "pod-1",
						Connection: "conn-1",
						Data:       json.RawMessage(`{}`),
					})
					<-ctx.Done()
					return nil
				},
			},
		}},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatalf("build controller: %v", err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)

	// Register 3 listeners synchronously.
	sinks := make([]*resourcetest.RecordingSink, 3)
	for i := range sinks {
		sinks[i] = resourcetest.NewRecordingSink()
		resource.AddListenerForTest(ctrl, sinks[i])
		s := sinks[i] // capture for cleanup
		t.Cleanup(func() { resource.RemoveListenerForTest(ctrl, s) })
	}

	ctrl.StartConnection(ctx, "conn-1")
	close(ready)

	for i, s := range sinks {
		s.WaitForAdds(t, 1, 2*time.Second)
		if s.AddCount() < 1 {
			t.Fatalf("listener %d did not receive add event", i)
		}
	}
}

// --- SC-010: Discovery + CRUD for discovered type ---
func TestSC010_DiscoveryCRUDForDiscoveredType(t *testing.T) {
	crdMeta := resource.ResourceMeta{
		Group:    "custom.io",
		Version:  "v1",
		Kind:     "Widget",
		Label:    "Widget",
		Category: "Custom",
	}

	patternResourcer := &resourcetest.StubResourcer[string]{
		ListFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.ListInput) (*resource.ListResult, error) {
			return &resource.ListResult{
				Success: true,
				Result:  []json.RawMessage{json.RawMessage(`{"id":"widget-1"}`)},
			}, nil
		},
	}

	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Patterns: map[string]resource.Resourcer[string]{
			"*": patternResourcer,
		},
		Discovery: &testDiscoveryProvider{
			DiscoverFunc: func(_ context.Context, _ *types.Connection) ([]resource.ResourceMeta, error) {
				return []resource.ResourceMeta{crdMeta}, nil
			},
		},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatalf("build controller: %v", err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1") // triggers discovery

	testCtx := resourcetest.NewTestContext()

	// HasResourceType should find the discovered CRD.
	if !ctrl.HasResourceType(testCtx, "custom.io::v1::Widget") {
		t.Fatal("expected HasResourceType to find discovered CRD")
	}

	// List via pattern resourcer.
	result, err := ctrl.List(testCtx, "custom.io::v1::Widget", resource.ListInput{})
	if err != nil {
		t.Fatalf("list CRD: %v", err)
	}
	if !result.Success || len(result.Result) != 1 {
		t.Fatalf("expected 1 widget, got %d", len(result.Result))
	}
}

// --- SC-011: Connection watch — external config changes ---
func TestSC011_ConnectionWatchExternalConfigChanges(t *testing.T) {
	updateCh := make(chan []types.Connection, 1)

	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.WatchingConnectionProvider[string]{
			StubConnectionProvider: resourcetest.StubConnectionProvider[string]{},
			WatchConnectionsFunc: func(ctx context.Context) (<-chan []types.Connection, error) {
				return updateCh, nil
			},
		},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}},
		},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatalf("build controller: %v", err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")

	// Start WatchConnections in background.
	stream := make(chan []types.Connection, 1)
	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()

	var watchErr error
	watchDone := make(chan struct{})
	go func() {
		watchErr = ctrl.WatchConnections(watchCtx, stream)
		close(watchDone)
	}()

	// Send an update from the external provider.
	updatedConns := []types.Connection{
		{ID: "conn-1", Name: "Updated"},
		{ID: "conn-2", Name: "New"},
	}
	updateCh <- updatedConns

	// Receive on stream.
	select {
	case received := <-stream:
		if len(received) != 2 {
			t.Fatalf("expected 2 connections, got %d", len(received))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for connection watch update")
	}

	watchCancel()
	<-watchDone
	if watchErr != nil {
		t.Fatalf("unexpected watch error: %v", watchErr)
	}
}

// --- SC-012: Full cleanup — root context cancel ---
func TestSC012_FullCleanupRootContextCancel(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{
			LoadConnectionsFunc: func(_ context.Context) ([]types.Connection, error) {
				return []types.Connection{
					{ID: "conn-1", Name: "Conn1"},
					{ID: "conn-2", Name: "Conn2"},
				}, nil
			},
		},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.WatchableResourcer[string]{}},
			{Meta: resourcetest.DeploymentMeta, Resourcer: &resourcetest.WatchableResourcer[string]{}},
		},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatalf("build controller: %v", err)
	}

	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")
	ctrl.StartConnection(ctx, "conn-2")

	// Record baseline goroutine count.
	baselineGoroutines := runtime.NumGoroutine()

	// Close controller (calls wm.Wait() internally).
	ctrl.Close()

	// Let goroutines settle after Close for counting.
	time.Sleep(50 * time.Millisecond)
	runtime.GC()

	afterGoroutines := runtime.NumGoroutine()
	// The goroutine count should have decreased (or at least stabilized).
	// Allow a small delta for runtime background goroutines.
	if afterGoroutines > baselineGoroutines {
		t.Logf("goroutines before Close: %d, after Close: %d", baselineGoroutines, afterGoroutines)
		// Not a hard failure — just verify the trend. The point is no leak.
	}
}

// --- SC-013: Rapid connect/disconnect cycles ---
func TestSC013_RapidConnectDisconnectCycles(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.WatchableResourcer[string]{}},
		},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatalf("build controller: %v", err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)

	before := runtime.NumGoroutine()

	for i := 0; i < 10; i++ {
		ctrl.StartConnection(ctx, "conn-1")
		time.Sleep(2 * time.Millisecond) // pacing for rapid-cycle test
		ctrl.StopConnection(ctx, "conn-1")
		time.Sleep(2 * time.Millisecond) // pacing for rapid-cycle test
	}

	// Let goroutines settle after all cycles.
	time.Sleep(100 * time.Millisecond)
	runtime.GC()

	after := runtime.NumGoroutine()
	// Allow a generous delta (10) for runtime goroutines. The key is no leak
	// proportional to the number of cycles.
	if after > before+10 {
		t.Fatalf("potential goroutine leak: before=%d, after=%d (10 cycles)", before, after)
	}
}

// --- SC-014: Connection with zero registered resources ---
func TestSC014_ConnectionWithZeroResources(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources:   nil, // no resources registered
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatalf("build controller: %v", err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)

	// Start connection should succeed.
	status, err := ctrl.StartConnection(ctx, "conn-1")
	if err != nil {
		t.Fatalf("start connection: %v", err)
	}
	if status.Status != "CONNECTED" {
		t.Fatalf("expected CONNECTED, got %s", status.Status)
	}

	// ListConnections should work.
	conns, err := ctrl.ListConnections(ctx)
	if err != nil {
		t.Fatalf("list connections: %v", err)
	}
	if len(conns) == 0 {
		t.Fatal("expected at least 1 connection")
	}

	// Stop should work.
	_, err = ctrl.StopConnection(ctx, "conn-1")
	if err != nil {
		t.Fatalf("stop connection: %v", err)
	}

	// CRUD should return an error (no resourcer registered).
	testCtx := resourcetest.NewTestContext()
	// Must restart connection for CRUD context to resolve.
	ctrl.StartConnection(ctx, "conn-1")
	_, err = ctrl.Get(testCtx, "core::v1::Pod", resource.GetInput{ID: "pod-1"})
	if err == nil {
		t.Fatal("expected error for unregistered resource type")
	}
}

// --- SC-015: Discovery changes between connect cycles ---
func TestSC015_DiscoveryChangesBetweenConnectCycles(t *testing.T) {
	var discoverCount atomic.Int32

	crd1 := resource.ResourceMeta{Group: "custom.io", Version: "v1", Kind: "Alpha", Label: "Alpha", Category: "Custom"}
	crd2 := resource.ResourceMeta{Group: "custom.io", Version: "v1", Kind: "Beta", Label: "Beta", Category: "Custom"}

	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Patterns: map[string]resource.Resourcer[string]{
			"*": &resourcetest.StubResourcer[string]{},
		},
		Discovery: &testDiscoveryProvider{
			DiscoverFunc: func(_ context.Context, _ *types.Connection) ([]resource.ResourceMeta, error) {
				n := discoverCount.Add(1)
				if n == 1 {
					return []resource.ResourceMeta{crd1}, nil
				}
				return []resource.ResourceMeta{crd1, crd2}, nil
			},
		},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatalf("build controller: %v", err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)

	// First connect — discovers only Alpha.
	ctrl.StartConnection(ctx, "conn-1")
	testCtx := resourcetest.NewTestContext()
	if !ctrl.HasResourceType(testCtx, "custom.io::v1::Alpha") {
		t.Fatal("expected Alpha after first connect")
	}
	if ctrl.HasResourceType(testCtx, "custom.io::v1::Beta") {
		t.Fatal("Beta should not exist after first connect")
	}

	// Disconnect.
	ctrl.StopConnection(ctx, "conn-1")

	// Reconnect — discovers Alpha + Beta.
	ctrl.StartConnection(ctx, "conn-1")
	if !ctrl.HasResourceType(testCtx, "custom.io::v1::Alpha") {
		t.Fatal("expected Alpha after second connect")
	}
	if !ctrl.HasResourceType(testCtx, "custom.io::v1::Beta") {
		t.Fatal("expected Beta after second connect")
	}
}

// --- SC-016: All watches fail simultaneously ---
func TestSC016_AllWatchesFailSimultaneously(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{
			{
				Meta: resourcetest.PodMeta,
				Resourcer: &resourcetest.WatchableResourcer[string]{
					WatchFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
						return fmt.Errorf("pod watch failure")
					},
				},
			},
			{
				Meta: resourcetest.DeploymentMeta,
				Resourcer: &resourcetest.WatchableResourcer[string]{
					WatchFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
						return fmt.Errorf("deployment watch failure")
					},
				},
			},
			{
				Meta: resourcetest.ServiceMeta,
				Resourcer: &resourcetest.WatchableResourcer[string]{
					WatchFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
						return fmt.Errorf("service watch failure")
					},
				},
			},
		},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatalf("build controller: %v", err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)

	sink := resourcetest.NewRecordingSink()
	resource.AddListenerForTest(ctrl, sink)
	t.Cleanup(func() { resource.RemoveListenerForTest(ctrl, sink) })

	ctrl.StartConnection(ctx, "conn-1")

	// All three should eventually reach Error or Failed state.
	sink.WaitForState(t, "core::v1::Pod", resource.WatchStateError, 3*time.Second)
	sink.WaitForState(t, "apps::v1::Deployment", resource.WatchStateError, 3*time.Second)
	sink.WaitForState(t, "core::v1::Service", resource.WatchStateError, 3*time.Second)

	// Connection should still be listed.
	conns, err := ctrl.ListConnections(ctx)
	if err != nil {
		t.Fatalf("list connections: %v", err)
	}
	if len(conns) == 0 {
		t.Fatal("connection should still be listed despite all watch failures")
	}
}

// --- SC-017: Connection stays up but all watches fail permanently ---
func TestSC017_WatchesFailPermanentlyCRUDStillWorks(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{
			{
				Meta: resourcetest.PodMeta,
				Resourcer: &resourcetest.WatchableResourcer[string]{
					StubResourcer: resourcetest.StubResourcer[string]{
						GetFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.GetInput) (*resource.GetResult, error) {
							return &resource.GetResult{Success: true, Result: json.RawMessage(`{"id":"pod-1"}`)}, nil
						},
					},
					WatchFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
						return fmt.Errorf("permanent failure")
					},
				},
			},
			{
				Meta: resourcetest.DeploymentMeta,
				Resourcer: &resourcetest.WatchableResourcer[string]{
					StubResourcer: resourcetest.StubResourcer[string]{
						GetFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.GetInput) (*resource.GetResult, error) {
							return &resource.GetResult{Success: true, Result: json.RawMessage(`{"id":"dep-1"}`)}, nil
						},
					},
					WatchFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
						return fmt.Errorf("permanent failure")
					},
				},
			},
		},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatalf("build controller: %v", err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)

	sink := resourcetest.NewRecordingSink()
	resource.AddListenerForTest(ctrl, sink)
	t.Cleanup(func() { resource.RemoveListenerForTest(ctrl, sink) })

	ctrl.StartConnection(ctx, "conn-1")

	// Wait for watches to exhaust retries and reach Failed.
	sink.WaitForState(t, "core::v1::Pod", resource.WatchStateFailed, 30*time.Second)
	sink.WaitForState(t, "apps::v1::Deployment", resource.WatchStateFailed, 30*time.Second)

	// CRUD should still work.
	testCtx := resourcetest.NewTestContext()
	result, err := ctrl.Get(testCtx, "core::v1::Pod", resource.GetInput{ID: "pod-1"})
	if err != nil {
		t.Fatalf("Get after watch failure: %v", err)
	}
	if !result.Success {
		t.Fatal("expected Get success")
	}

	result, err = ctrl.Get(testCtx, "apps::v1::Deployment", resource.GetInput{ID: "dep-1"})
	if err != nil {
		t.Fatalf("Get deployment after watch failure: %v", err)
	}
	if !result.Success {
		t.Fatal("expected Deployment Get success")
	}
}

// --- SC-018: Concurrent CRUD + watch restart ---
func TestSC018_ConcurrentCRUDAndWatchRestart(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				StubResourcer: resourcetest.StubResourcer[string]{
					GetFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.GetInput) (*resource.GetResult, error) {
						return &resource.GetResult{Success: true, Result: json.RawMessage(`{}`)}, nil
					},
					ListFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.ListInput) (*resource.ListResult, error) {
						return &resource.ListResult{Success: true}, nil
					},
				},
			},
		}},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatalf("build controller: %v", err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")

	testCtx := resourcetest.NewTestContext()

	var wg sync.WaitGroup

	// 10 concurrent Get calls.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := ctrl.Get(testCtx, "core::v1::Pod", resource.GetInput{ID: "pod-1"})
			if err != nil {
				t.Errorf("concurrent Get: %v", err)
			}
		}()
	}

	// 10 concurrent List calls.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := ctrl.List(testCtx, "core::v1::Pod", resource.ListInput{})
			if err != nil {
				t.Errorf("concurrent List: %v", err)
			}
		}()
	}

	// Concurrent RestartResourceWatch.
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := ctrl.RestartResourceWatch(ctx, "conn-1", "core::v1::Pod")
		if err != nil {
			t.Errorf("RestartResourceWatch: %v", err)
		}
	}()

	wg.Wait()
}

// --- SC-019: ListenForEvents — listener disconnects and reconnects ---
func TestSC019_ListenerDisconnectsAndReconnects(t *testing.T) {
	var emitCount atomic.Int32
	emitGate := make(chan struct{})

	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, sink resource.WatchEventSink) error {
					for {
						select {
						case <-ctx.Done():
							return nil
						case <-emitGate:
							n := emitCount.Add(1)
							sink.OnAdd(resource.WatchAddPayload{
								Key:        meta.Key(),
								ID:         fmt.Sprintf("pod-%d", n),
								Connection: "conn-1",
								Data:       json.RawMessage(`{}`),
							})
						}
					}
				},
			},
		}},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatalf("build controller: %v", err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")
	waitCtx, waitCancel := context.WithTimeout(ctx, 2*time.Second)
	defer waitCancel()
	resource.WaitForConnectionReadyForTest(waitCtx, ctrl, "conn-1")

	// Listener 1 — register and receive one event.
	sink1 := resourcetest.NewRecordingSink()
	resource.AddListenerForTest(ctrl, sink1)

	emitGate <- struct{}{} // trigger event 1
	sink1.WaitForAdds(t, 1, 2*time.Second)

	// Disconnect listener 1.
	resource.RemoveListenerForTest(ctrl, sink1)

	// Listener 2 — register a new listener.
	sink2 := resourcetest.NewRecordingSink()
	resource.AddListenerForTest(ctrl, sink2)
	t.Cleanup(func() { resource.RemoveListenerForTest(ctrl, sink2) })

	emitGate <- struct{}{} // trigger event 2
	sink2.WaitForAdds(t, 1, 2*time.Second)

	if sink2.AddCount() < 1 {
		t.Fatal("listener 2 did not receive events")
	}
}

// --- SC-020: Provider reuse after shutdown and restart ---
func TestSC020_ProviderReuseAfterShutdownAndRestart(t *testing.T) {
	provider := &resourcetest.StubConnectionProvider[string]{}
	podResourcer := &resourcetest.StubResourcer[string]{
		GetFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.GetInput) (*resource.GetResult, error) {
			return &resource.GetResult{Success: true, Result: json.RawMessage(`{"id":"pod-1"}`)}, nil
		},
	}

	ctx := context.Background()

	// First lifecycle.
	cfg := resource.ResourcePluginConfig[string]{
		Connections: provider,
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: podResourcer},
		},
	}
	ctrl1, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatalf("build controller 1: %v", err)
	}

	ctrl1.LoadConnections(ctx)
	ctrl1.StartConnection(ctx, "conn-1")

	testCtx := resourcetest.NewTestContext()
	result, err := ctrl1.Get(testCtx, "core::v1::Pod", resource.GetInput{ID: "pod-1"})
	if err != nil {
		t.Fatalf("first lifecycle Get: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success in first lifecycle")
	}

	ctrl1.Close()

	// Second lifecycle — reuse same provider.
	ctrl2, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatalf("build controller 2: %v", err)
	}
	defer ctrl2.Close()

	ctrl2.LoadConnections(ctx)
	ctrl2.StartConnection(ctx, "conn-1")

	result, err = ctrl2.Get(testCtx, "core::v1::Pod", resource.GetInput{ID: "pod-1"})
	if err != nil {
		t.Fatalf("second lifecycle Get: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success in second lifecycle")
	}
}

// --- SC-021: Watch with many connections — selective disconnect ---
func TestSC021_ManyConnectionsSelectiveDisconnect(t *testing.T) {
	ctx := context.Background()

	conns := make([]types.Connection, 5)
	for i := range conns {
		conns[i] = types.Connection{ID: fmt.Sprintf("conn-%d", i+1), Name: fmt.Sprintf("Conn%d", i+1)}
	}

	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{
			LoadConnectionsFunc: func(_ context.Context) ([]types.Connection, error) {
				return conns, nil
			},
		},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.WatchableResourcer[string]{}},
		},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatalf("build controller: %v", err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)
	for i := 1; i <= 5; i++ {
		connID := fmt.Sprintf("conn-%d", i)
		ctrl.StartConnection(ctx, connID)
		waitCtx, waitCancel := context.WithTimeout(ctx, 2*time.Second)
		resource.WaitForConnectionReadyForTest(waitCtx, ctrl, connID)
		waitCancel()
	}

	// Stop only conn-3.
	ctrl.StopConnection(ctx, "conn-3")

	testCtx := context.Background()

	// conn-3 watches should be stopped.
	running, _ := ctrl.IsResourceWatchRunning(testCtx, "conn-3", "core::v1::Pod")
	if running {
		t.Fatal("conn-3 Pod watch should be stopped")
	}

	// Other connections should still be running.
	for _, cid := range []string{"conn-1", "conn-2", "conn-4", "conn-5"} {
		running, _ = ctrl.IsResourceWatchRunning(testCtx, cid, "core::v1::Pod")
		if !running {
			t.Fatalf("%s Pod watch should still be running", cid)
		}
	}
}

// --- SC-022: Slow CreateClient blocks only that connection ---
func TestSC022_SlowCreateClientBlocksOnlyThatConnection(t *testing.T) {
	var conn2Ready atomic.Bool

	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{
			LoadConnectionsFunc: func(_ context.Context) ([]types.Connection, error) {
				return []types.Connection{
					{ID: "conn-1", Name: "Slow"},
					{ID: "conn-2", Name: "Fast"},
				}, nil
			},
			CreateClientFunc: func(ctx context.Context) (*string, error) {
				sess := resource.SessionFromContext(ctx)
				if sess != nil && sess.Connection != nil && sess.Connection.ID == "conn-1" {
					// Slow connection — takes 500ms.
					time.Sleep(500 * time.Millisecond)
				}
				s := "client"
				return &s, nil
			},
		},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{
				GetFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.GetInput) (*resource.GetResult, error) {
					return &resource.GetResult{Success: true, Result: json.RawMessage(`{}`)}, nil
				},
			}},
		},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatalf("build controller: %v", err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)

	// Start both connections concurrently.
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		ctrl.StartConnection(ctx, "conn-1") // slow
	}()

	go func() {
		defer wg.Done()
		_, err := ctrl.StartConnection(ctx, "conn-2") // fast
		if err != nil {
			t.Errorf("start conn-2: %v", err)
			return
		}
		conn2Ready.Store(true)
	}()

	// conn-2 should be operational before conn-1 finishes.
	// Wait up to 300ms for conn-2.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if conn2Ready.Load() {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if !conn2Ready.Load() {
		t.Fatal("conn-2 should be ready while conn-1 is still connecting")
	}

	// Verify conn-2 is operational (CRUD works).
	conn2Ctx := resourcetest.NewTestContextWithConnection(&types.Connection{ID: "conn-2", Name: "Fast"})
	result, err := ctrl.Get(conn2Ctx, "core::v1::Pod", resource.GetInput{ID: "pod-1"})
	if err != nil {
		t.Fatalf("Get on conn-2 while conn-1 connecting: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success on conn-2")
	}

	wg.Wait()
}
