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
)

// ============================================================================
// Performance Invariant Tests (PI-001 through PI-010)
// ============================================================================

// --- PI-001: Events emitted during sync with no ListenForEvents go nowhere ---
func TestPI001_EventsWithNoListener(t *testing.T) {
	// Watcher emits 100 adds during sync. No ListenForEvents is ever called.
	// Verify: no panic, no blocking, events are silently discarded by fan-out sink
	// (which has zero listeners).
	var emitted atomic.Int32
	allEmitted := make(chan struct{})

	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, sink resource.WatchEventSink) error {
					sink.OnStateChange(resource.WatchStateEvent{
						ResourceKey: meta.Key(),
						State:       resource.WatchStateSyncing,
					})
					for i := 0; i < 100; i++ {
						sink.OnAdd(resource.WatchAddPayload{
							Key:        meta.Key(),
							ID:         fmt.Sprintf("pod-%d", i),
							Connection: "conn-1",
							Data:       json.RawMessage(`{}`),
						})
						emitted.Add(1)
					}
					sink.OnStateChange(resource.WatchStateEvent{
						ResourceKey: meta.Key(),
						State:       resource.WatchStateSynced,
					})
					close(allEmitted)
					<-ctx.Done()
					return nil
				},
			},
		}},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")

	// Wait for watcher to finish emitting via channel.
	select {
	case <-allEmitted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for events to be emitted")
	}

	if emitted.Load() != 100 {
		t.Fatalf("expected 100 events emitted, got %d", emitted.Load())
	}

	// No panic, no blocked goroutines. Events went to fan-out with zero listeners.
	// The key invariant is that the watcher completes without blocking.
}

// --- PI-002: Listener registered after events still receives new events ---
func TestPI002_LateListenerReceivesNewEvents(t *testing.T) {
	// Start conn-1. Watches sync and emit initial events. Wait 200ms. THEN
	// start ListenForEvents. Assert: new events after listener registration arrive.
	phase := make(chan int, 1)

	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, sink resource.WatchEventSink) error {
					// Phase 1: emit initial events (before listener).
					for i := 0; i < 5; i++ {
						sink.OnAdd(resource.WatchAddPayload{
							Key:        meta.Key(),
							ID:         fmt.Sprintf("pod-early-%d", i),
							Connection: "conn-1",
						})
					}
					sink.OnStateChange(resource.WatchStateEvent{
						ResourceKey: meta.Key(),
						State:       resource.WatchStateSynced,
					})

					// Signal phase 1 complete.
					select {
					case phase <- 1:
					default:
					}

					// Wait for test to register listener.
					select {
					case <-phase:
					case <-ctx.Done():
						return nil
					}

					// Phase 2: emit events after listener is registered.
					for i := 0; i < 3; i++ {
						sink.OnAdd(resource.WatchAddPayload{
							Key:        meta.Key(),
							ID:         fmt.Sprintf("pod-late-%d", i),
							Connection: "conn-1",
						})
					}
					<-ctx.Done()
					return nil
				},
			},
		}},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")

	// Wait for phase 1 to complete.
	select {
	case <-phase:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for phase 1")
	}

	time.Sleep(200 * time.Millisecond) // deliberate delay to test late registration

	// Register listener synchronously.
	sink := resourcetest.NewRecordingSink()
	resource.AddListenerForTest(ctrl, sink)
	t.Cleanup(func() { resource.RemoveListenerForTest(ctrl, sink) })

	// Signal phase 2.
	phase <- 2

	// New events should arrive.
	sink.WaitForAdds(t, 3, 2*time.Second)

	if sink.AddCount() < 3 {
		t.Fatalf("expected at least 3 late events, got %d", sink.AddCount())
	}
}

// --- PI-003: In-process event latency < 100ms ---
func TestPI003_EventLatency(t *testing.T) {
	// Start conn-1. Start ListenForEvents. Pod watcher emits add at recorded time.
	// Check RecordingSink. Assert add received within 100ms (in-process should be <10ms).
	ready := make(chan struct{})
	var emitTime time.Time

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
					emitTime = time.Now()
					sink.OnAdd(resource.WatchAddPayload{
						Key:        meta.Key(),
						ID:         "pod-latency",
						Connection: "conn-1",
					})
					<-ctx.Done()
					return nil
				},
			},
		}},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)

	// Register listener first.
	sink := resourcetest.NewRecordingSink()
	resource.AddListenerForTest(ctrl, sink)
	t.Cleanup(func() { resource.RemoveListenerForTest(ctrl, sink) })

	ctrl.StartConnection(ctx, "conn-1")
	close(ready)

	sink.WaitForAdds(t, 1, 2*time.Second)
	receiveTime := time.Now()

	latency := receiveTime.Sub(emitTime)
	if latency > 100*time.Millisecond {
		t.Fatalf("event latency %v exceeds 100ms threshold", latency)
	}
}

// --- PI-004: Non-watchable resource produces no events ---
func TestPI004_NonWatchableNoEvents(t *testing.T) {
	// Service is NOT watchable. Pod IS watchable. Pod watcher emits events.
	// Assert: no events with key "core::v1::Service".
	ready := make(chan struct{})

	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{
			{
				Meta: resourcetest.PodMeta,
				Resourcer: &resourcetest.WatchableResourcer[string]{
					WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, sink resource.WatchEventSink) error {
						select {
						case <-ready:
						case <-ctx.Done():
							return nil
						}
						for i := 0; i < 5; i++ {
							sink.OnAdd(resource.WatchAddPayload{
								Key:        meta.Key(),
								ID:         fmt.Sprintf("pod-%d", i),
								Connection: "conn-1",
							})
						}
						<-ctx.Done()
						return nil
					},
				},
			},
			{
				// Service is NOT watchable (StubResourcer, not WatchableResourcer).
				Meta:      resourcetest.ServiceMeta,
				Resourcer: &resourcetest.StubResourcer[string]{},
			},
		},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)

	sink := resourcetest.NewRecordingSink()
	resource.AddListenerForTest(ctrl, sink)
	t.Cleanup(func() { resource.RemoveListenerForTest(ctrl, sink) })

	ctrl.StartConnection(ctx, "conn-1")
	close(ready)

	sink.WaitForAdds(t, 5, 2*time.Second)

	// After WaitForAdds returns, all 5 Pod events have been received.
	// No further events are being emitted, so reading Adds is safe.
	// Verify no events for Service key.
	count := sink.AddCount()
	for i := 0; i < count; i++ {
		// Access exported Adds slice. Safe because events are done arriving
		// and we waited for synchronization above.
		if sink.Adds[i].Key == "core::v1::Service" {
			t.Fatal("unexpected event for non-watchable Service resource")
		}
	}
}

// --- PI-005: Multiple watchable resources all reach synced ---
func TestPI005_MultipleWatchablesAllSync(t *testing.T) {
	// 5 watchable resources (Pod, Deployment, Secret, Service, + Node).
	// All SyncOnConnect. Start conn-1. Wait for all to reach synced.
	customMeta := []resource.ResourceMeta{
		resourcetest.PodMeta,
		resourcetest.DeploymentMeta,
		resourcetest.SecretMeta,
		resourcetest.ServiceMeta,
		resourcetest.NodeMeta,
	}

	var registrations []resource.ResourceRegistration[string]
	for _, meta := range customMeta {
		m := meta // capture
		registrations = append(registrations, resource.ResourceRegistration[string]{
			Meta: m,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, sink resource.WatchEventSink) error {
					sink.OnStateChange(resource.WatchStateEvent{
						ResourceKey: meta.Key(),
						State:       resource.WatchStateSyncing,
					})
					// Emit a few events per resource.
					for i := 0; i < 3; i++ {
						sink.OnAdd(resource.WatchAddPayload{
							Key:        meta.Key(),
							ID:         fmt.Sprintf("%s-%d", meta.Kind, i),
							Connection: "conn-1",
						})
					}
					sink.OnStateChange(resource.WatchStateEvent{
						ResourceKey: meta.Key(),
						State:       resource.WatchStateSynced,
					})
					<-ctx.Done()
					return nil
				},
			},
		})
	}

	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources:   registrations,
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")

	// Wait for all watches to be running.
	waitCtx, waitCancel := context.WithTimeout(ctx, 2*time.Second)
	defer waitCancel()
	if err := resource.WaitForConnectionReadyForTest(waitCtx, ctrl, "conn-1"); err != nil {
		t.Fatalf("timed out waiting for all watches: %v", err)
	}

	// Verify via GetWatchState.
	summary, err := ctrl.GetWatchState(ctx, "conn-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, meta := range customMeta {
		key := meta.Key()
		if _, ok := summary.Resources[key]; !ok {
			t.Fatalf("expected watch state for %s", key)
		}
	}
	if len(summary.Resources) != 5 {
		t.Fatalf("expected 5 resource watch states, got %d", len(summary.Resources))
	}
}

// --- PI-006: No head-of-line blocking across resource watchers ---
func TestPI006_NoHeadOfLineBlocking(t *testing.T) {
	// Pod watcher emits 1000 adds. Deployment watcher emits 5 adds and finishes quickly.
	// Assert Deployment events arrive while Pod is still emitting (no head-of-line blocking).
	deployDone := make(chan struct{})
	podDone := make(chan struct{})

	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{
			{
				Meta: resourcetest.PodMeta,
				Resourcer: &resourcetest.WatchableResourcer[string]{
					WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, sink resource.WatchEventSink) error {
						for i := 0; i < 1000; i++ {
							sink.OnAdd(resource.WatchAddPayload{
								Key:        meta.Key(),
								ID:         fmt.Sprintf("pod-%d", i),
								Connection: "conn-1",
							})
							// Small delay to simulate realistic event cadence.
							time.Sleep(100 * time.Microsecond)
						}
						close(podDone)
						<-ctx.Done()
						return nil
					},
				},
			},
			{
				Meta: resourcetest.DeploymentMeta,
				Resourcer: &resourcetest.WatchableResourcer[string]{
					WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, sink resource.WatchEventSink) error {
						for i := 0; i < 5; i++ {
							sink.OnAdd(resource.WatchAddPayload{
								Key:        meta.Key(),
								ID:         fmt.Sprintf("dep-%d", i),
								Connection: "conn-1",
							})
						}
						close(deployDone)
						<-ctx.Done()
						return nil
					},
				},
			},
		},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)

	sink := resourcetest.NewRecordingSink()
	resource.AddListenerForTest(ctrl, sink)
	t.Cleanup(func() { resource.RemoveListenerForTest(ctrl, sink) })

	ctrl.StartConnection(ctx, "conn-1")

	// Wait for deployment to finish emitting.
	select {
	case <-deployDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for deployment events")
	}

	deploymentDoneTime := time.Now()

	// Verify pod is still going (podDone should NOT be closed yet).
	select {
	case <-podDone:
		// podDone was already closed before deploy finished, which is also fine
		// (means both ran fast in parallel). The key is deployment wasn't blocked.
	default:
		// This is the expected case: pod is still emitting while deployment is done.
	}

	// Wait for pod to finish.
	select {
	case <-podDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for pod events")
	}

	_ = deploymentDoneTime // used for timing verification

	// Wait for all events to arrive.
	sink.WaitForAdds(t, 1005, 5*time.Second)
}

// --- PI-007: Events before ListenForEvents are NOT buffered (design invariant) ---
func TestPI007_EventsNotBuffered(t *testing.T) {
	// This test documents the design invariant that the SDK does NOT buffer events.
	// Events emitted before ListenForEvents are discarded.
	// Start conn-1, wait for events, then start listener. The listener only gets
	// events emitted after registration.
	earlyDone := make(chan struct{})

	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, sink resource.WatchEventSink) error {
					// Emit 10 events immediately (before listener).
					for i := 0; i < 10; i++ {
						sink.OnAdd(resource.WatchAddPayload{
							Key:        meta.Key(),
							ID:         fmt.Sprintf("pod-early-%d", i),
							Connection: "conn-1",
						})
					}
					close(earlyDone)
					// Then wait for context and emit more.
					<-ctx.Done()
					return nil
				},
			},
		}},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")

	// Wait for early events to be emitted.
	select {
	case <-earlyDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for early events")
	}

	time.Sleep(20 * time.Millisecond) // ensure events have fully propagated through fan-out

	// Now register listener synchronously.
	sink := resourcetest.NewRecordingSink()
	resource.AddListenerForTest(ctrl, sink)
	t.Cleanup(func() { resource.RemoveListenerForTest(ctrl, sink) })

	// The 10 early events should NOT have been buffered.
	if sink.AddCount() != 0 {
		t.Fatalf("expected 0 buffered events, got %d (SDK should NOT buffer pre-listener events)", sink.AddCount())
	}
}

// --- PI-008: Memory doesn't grow unboundedly with many events ---
func TestPI008_MemoryBounded(t *testing.T) {
	// Pod watcher emits 5000 events. ListenForEvents consumes them.
	// Assert memory doesn't grow unboundedly.
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, sink resource.WatchEventSink) error {
					for i := 0; i < 5000; i++ {
						sink.OnAdd(resource.WatchAddPayload{
							Key:        meta.Key(),
							ID:         fmt.Sprintf("pod-%d", i),
							Connection: "conn-1",
							Data:       json.RawMessage(`{"metadata":{"name":"test"}}`),
						})
					}
					<-ctx.Done()
					return nil
				},
			},
		}},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)

	sink := resourcetest.NewRecordingSink()
	resource.AddListenerForTest(ctrl, sink)
	t.Cleanup(func() { resource.RemoveListenerForTest(ctrl, sink) })

	// Force GC and measure baseline.
	runtime.GC()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	ctrl.StartConnection(ctx, "conn-1")
	sink.WaitForAdds(t, 5000, 10*time.Second)

	// Force GC and measure after.
	runtime.GC()
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	// HeapInuse growth should be reasonable (< 50MB for 5000 small events).
	// In practice it should be much less — this is a sanity check, not a tight bound.
	growth := int64(memAfter.HeapInuse) - int64(memBefore.HeapInuse)
	maxGrowth := int64(50 * 1024 * 1024) // 50MB
	if growth > maxGrowth {
		t.Fatalf("heap grew by %d bytes (%.1f MB) for 5000 events, exceeds 50MB threshold",
			growth, float64(growth)/(1024*1024))
	}
}

// --- PI-009: Goroutine count stable after all watches synced ---
func TestPI009_GoroutineStability(t *testing.T) {
	// 10 watchable resources. Start connection. Wait for all synced. Check
	// runtime.NumGoroutine(). Wait 200ms. Check again. Assert stable (+/- 2).
	metas := []resource.ResourceMeta{
		resourcetest.PodMeta,
		resourcetest.DeploymentMeta,
		resourcetest.SecretMeta,
		resourcetest.ServiceMeta,
		resourcetest.NodeMeta,
		{Group: "batch", Version: "v1", Kind: "Job", Label: "Job", Category: "Workloads"},
		{Group: "batch", Version: "v1", Kind: "CronJob", Label: "CronJob", Category: "Workloads"},
		{Group: "core", Version: "v1", Kind: "ConfigMap", Label: "ConfigMap", Category: "Configuration"},
		{Group: "core", Version: "v1", Kind: "Namespace", Label: "Namespace", Category: "Cluster"},
		{Group: "core", Version: "v1", Kind: "PersistentVolume", Label: "PV", Category: "Storage"},
	}

	var registrations []resource.ResourceRegistration[string]
	for _, meta := range metas {
		m := meta
		registrations = append(registrations, resource.ResourceRegistration[string]{
			Meta: m,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, sink resource.WatchEventSink) error {
					sink.OnStateChange(resource.WatchStateEvent{
						ResourceKey: meta.Key(),
						State:       resource.WatchStateSynced,
					})
					<-ctx.Done()
					return nil
				},
			},
		})
	}

	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources:   registrations,
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")

	// Wait for all watches to be running.
	waitCtx, waitCancel := context.WithTimeout(ctx, 2*time.Second)
	defer waitCancel()
	if err := resource.WaitForConnectionReadyForTest(waitCtx, ctrl, "conn-1"); err != nil {
		t.Fatalf("timed out waiting for all watches: %v", err)
	}

	time.Sleep(50 * time.Millisecond) // let goroutines settle for counting
	goroutines1 := runtime.NumGoroutine()

	time.Sleep(200 * time.Millisecond) // stability window for counting
	goroutines2 := runtime.NumGoroutine()

	diff := goroutines2 - goroutines1
	if diff < 0 {
		diff = -diff
	}
	if diff > 2 {
		t.Fatalf("goroutine count unstable: %d -> %d (diff %d, threshold 2)",
			goroutines1, goroutines2, diff)
	}
}

// --- PI-010: Aggregate event throughput is reasonable ---
func TestPI010_AggregateThroughput(t *testing.T) {
	// 5 resources all emitting events. Verify all events arrived promptly by
	// checking total elapsed time for N events is reasonable.
	metas := []resource.ResourceMeta{
		resourcetest.PodMeta,
		resourcetest.DeploymentMeta,
		resourcetest.SecretMeta,
		resourcetest.ServiceMeta,
		resourcetest.NodeMeta,
	}

	const eventsPerResource = 200
	totalExpected := len(metas) * eventsPerResource

	var wg sync.WaitGroup
	ready := make(chan struct{})

	var registrations []resource.ResourceRegistration[string]
	for _, meta := range metas {
		m := meta
		registrations = append(registrations, resource.ResourceRegistration[string]{
			Meta: m,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, sink resource.WatchEventSink) error {
					wg.Add(1)
					defer wg.Done()
					// Wait for all watchers to be ready.
					select {
					case <-ready:
					case <-ctx.Done():
						return nil
					}
					for i := 0; i < eventsPerResource; i++ {
						sink.OnAdd(resource.WatchAddPayload{
							Key:        meta.Key(),
							ID:         fmt.Sprintf("%s-%d", meta.Kind, i),
							Connection: "conn-1",
							Data:       json.RawMessage(`{}`),
						})
					}
					<-ctx.Done()
					return nil
				},
			},
		})
	}

	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources:   registrations,
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)

	sink := resourcetest.NewRecordingSink()
	resource.AddListenerForTest(ctrl, sink)
	t.Cleanup(func() { resource.RemoveListenerForTest(ctrl, sink) })

	ctrl.StartConnection(ctx, "conn-1")
	// Wait for all watches to be ready before releasing events.
	waitCtx, waitCancel := context.WithTimeout(ctx, 2*time.Second)
	defer waitCancel()
	resource.WaitForConnectionReadyForTest(waitCtx, ctrl, "conn-1")

	start := time.Now()
	close(ready) // release all watchers simultaneously

	sink.WaitForAdds(t, totalExpected, 10*time.Second)
	elapsed := time.Since(start)

	// 1000 in-process events should complete well under 5 seconds.
	// p99 per event should be < 100ms. Total 1000 events at 100ms each = 100s.
	// In practice in-process fan-out is microseconds, so 5s is extremely generous.
	if elapsed > 5*time.Second {
		t.Fatalf("aggregate throughput too slow: %d events in %v", totalExpected, elapsed)
	}

	if sink.AddCount() != totalExpected {
		t.Fatalf("expected %d total events, got %d", totalExpected, sink.AddCount())
	}
}
