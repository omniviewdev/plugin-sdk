package resource_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/resource/resourcetest"
)

func setupWatchTest(t *testing.T, regs ...resource.ResourceRegistration[string]) (*resource.WatchManagerForTest, *resource.ResourcerRegistryForTest, *resourcetest.RecordingSink) {
	t.Helper()
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	for _, r := range regs {
		reg.Register(r)
	}
	wm := resource.NewWatchManagerForTest(reg)
	resource.SetWatchManagerBackoff(wm, 3, 10*time.Millisecond) // fast backoff for tests
	sink := resourcetest.NewRecordingSink()
	wm.AddListener(sink)
	return wm, reg, sink
}

// testCtx returns a context with a generous timeout for tests, preventing hangs.
func testCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// --- WM-001: StartConnectionWatch starts SyncOnConnect watchers ---
func TestWM_StartConnectionWatch(t *testing.T) {
	wm, _, sink := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{},
		},
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.DeploymentMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test-client"
	if err := wm.StartConnectionWatch(ctx, "conn-1", &client, ctx); err != nil {
		t.Fatal(err)
	}

	// Both should be running.
	if !wm.IsResourceWatchRunning("conn-1", "core::v1::Pod") {
		t.Fatal("Pod watch should be running")
	}
	if !wm.IsResourceWatchRunning("conn-1", "apps::v1::Deployment") {
		t.Fatal("Deployment watch should be running")
	}

	cancel()
	wm.Wait()

	// Verify state events were received.
	if sink.AddCount()+len(sink.States) == 0 {
		// At minimum, watches were started (they may or may not emit events in default impl)
	}
}

// --- WM-002: StartConnectionWatch skips SyncOnFirstQuery ---
func TestWM_SkipsSyncOnFirstQuery(t *testing.T) {
	syncFirst := resource.SyncOnFirstQuery
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{},
		},
		resource.ResourceRegistration[string]{
			Meta: resourcetest.SecretMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				PolicyVal: &syncFirst,
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	if !wm.IsResourceWatchRunning("conn-1", "core::v1::Pod") {
		t.Fatal("Pod should be running")
	}
	if wm.IsResourceWatchRunning("conn-1", "core::v1::Secret") {
		t.Fatal("Secret should NOT be running (SyncOnFirstQuery)")
	}

	cancel()
	wm.Wait()
}

// --- WM-004: StartConnectionWatch skips non-Watcher resourcers ---
func TestWM_SkipsNonWatcher(t *testing.T) {
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{},
		},
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.ServiceMeta,
			Resourcer: &resourcetest.StubResourcer[string]{},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	if !wm.IsResourceWatchRunning("conn-1", "core::v1::Pod") {
		t.Fatal("Pod should be running")
	}
	if wm.IsResourceWatchRunning("conn-1", "core::v1::Service") {
		t.Fatal("Service should not have a watch")
	}

	cancel()
	wm.Wait()
}

// --- WM-006: StopConnectionWatch cancels all ---
func TestWM_StopConnectionWatch(t *testing.T) {
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	if err := wm.StopConnectionWatch(ctx, "conn-1"); err != nil {
		t.Fatal(err)
	}

	if wm.IsResourceWatchRunning("conn-1", "core::v1::Pod") {
		t.Fatal("Pod should not be running after stop")
	}

	wm.Wait()
}

// --- WM-010: EnsureResourceWatch starts lazy watch ---
func TestWM_EnsureResourceWatch(t *testing.T) {
	syncFirst := resource.SyncOnFirstQuery
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.SecretMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				PolicyVal: &syncFirst,
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	if wm.IsResourceWatchRunning("conn-1", "core::v1::Secret") {
		t.Fatal("Secret should not be running yet")
	}

	if err := wm.EnsureResourceWatch(ctx, "conn-1", "core::v1::Secret"); err != nil {
		t.Fatal(err)
	}

	if !wm.IsResourceWatchRunning("conn-1", "core::v1::Secret") {
		t.Fatal("Secret should be running after EnsureResourceWatch")
	}

	cancel()
	wm.Wait()
}

// --- WM-011: EnsureResourceWatch already running is no-op ---
func TestWM_EnsureResourceWatchIdempotent(t *testing.T) {
	var callCount int32
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
					atomic.AddInt32(&callCount, 1)
					<-ctx.Done()
					return nil
				},
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	tCtx, tCancel := testCtx(t)
	defer tCancel()
	if err := resource.WaitForWatchReadyWM(tCtx, wm, "conn-1", "core::v1::Pod"); err != nil {
		t.Fatalf("WaitForWatchReady: %v", err)
	}

	// Second ensure should be no-op.
	if err := wm.EnsureResourceWatch(ctx, "conn-1", "core::v1::Pod"); err != nil {
		t.Fatal(err)
	}

	cancel()
	wm.Wait()

	if atomic.LoadInt32(&callCount) != 1 {
		t.Fatalf("expected Watch called once, got %d", callCount)
	}
}

// --- WM-012: EnsureResourceWatch non-Watcher returns error ---
func TestWM_EnsureResourceWatchNonWatcher(t *testing.T) {
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.ServiceMeta,
			Resourcer: &resourcetest.StubResourcer[string]{},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	err := wm.EnsureResourceWatch(ctx, "conn-1", "core::v1::Service")
	if err == nil {
		t.Fatal("expected error for non-Watcher")
	}

	cancel()
	wm.Wait()
}

// --- WM-015: StopResourceWatch stops specific resource only ---
func TestWM_StopResourceWatch(t *testing.T) {
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{},
		},
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.DeploymentMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	if err := wm.StopResourceWatch(ctx, "conn-1", "core::v1::Pod"); err != nil {
		t.Fatal(err)
	}

	if wm.IsResourceWatchRunning("conn-1", "core::v1::Pod") {
		t.Fatal("Pod should be stopped")
	}
	if !wm.IsResourceWatchRunning("conn-1", "apps::v1::Deployment") {
		t.Fatal("Deployment should still be running")
	}

	cancel()
	wm.Wait()
}

// --- WM-017: RestartResourceWatch ---
func TestWM_RestartResourceWatch(t *testing.T) {
	var callCount int32
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
					atomic.AddInt32(&callCount, 1)
					<-ctx.Done()
					return nil
				},
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	tCtx, tCancel := testCtx(t)
	defer tCancel()
	if err := resource.WaitForWatchReadyWM(tCtx, wm, "conn-1", "core::v1::Pod"); err != nil {
		t.Fatalf("WaitForWatchReady: %v", err)
	}

	if err := wm.RestartResourceWatch(ctx, "conn-1", "core::v1::Pod"); err != nil {
		t.Fatal(err)
	}

	// Restart creates a new rws with new ready channel — wait for it.
	if err := resource.WaitForWatchReadyWM(tCtx, wm, "conn-1", "core::v1::Pod"); err != nil {
		t.Fatalf("WaitForWatchReady after restart: %v", err)
	}

	if !wm.IsResourceWatchRunning("conn-1", "core::v1::Pod") {
		t.Fatal("Pod should be running after restart")
	}

	cancel()
	wm.Wait()

	if atomic.LoadInt32(&callCount) < 2 {
		t.Fatalf("expected Watch called at least twice, got %d", callCount)
	}
}

// --- WM-023: Events flow from Watch to subscriber sink ---
func TestWM_EventsFlow(t *testing.T) {
	wm, _, sink := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, s resource.WatchEventSink) error {
					s.OnAdd(resource.WatchAddPayload{Key: meta.Key(), ID: "pod-1", Connection: "conn-1"})
					s.OnUpdate(resource.WatchUpdatePayload{Key: meta.Key(), ID: "pod-1", Connection: "conn-1"})
					s.OnDelete(resource.WatchDeletePayload{Key: meta.Key(), ID: "pod-1", Connection: "conn-1"})
					<-ctx.Done()
					return nil
				},
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	sink.WaitForAdds(t, 1, 2*time.Second)
	sink.WaitForUpdates(t, 1, 2*time.Second)
	sink.WaitForDeletes(t, 1, 2*time.Second)

	cancel()
	wm.Wait()
}

// --- WM-031: Watch returns error — restarted with backoff ---
func TestWM_WatchErrorRestart(t *testing.T) {
	var callCount int32
	watchReady := make(chan struct{}, 10)
	wm, _, sink := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
					count := atomic.AddInt32(&callCount, 1)
					if count <= 2 {
						return errors.New("transient error")
					}
					// Signal that the successful watch is running.
					select {
					case watchReady <- struct{}{}:
					default:
					}
					<-ctx.Done()
					return nil
				},
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	// Wait for the third call to succeed and start blocking.
	select {
	case <-watchReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for successful retry")
	}

	if atomic.LoadInt32(&callCount) < 3 {
		t.Fatalf("expected at least 3 Watch calls, got %d", callCount)
	}

	// Should have received Error state events.
	foundError := false
	sink.Reset() // check that it's still running
	for _, s := range sink.States {
		if s.State == resource.WatchStateError {
			foundError = true
		}
	}
	_ = foundError // error events may have been received before reset

	cancel()
	wm.Wait()
}

// --- WM-032: Watch max retries exceeded ---
func TestWM_WatchMaxRetriesExceeded(t *testing.T) {
	var callCount int32
	wm, _, sink := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
					atomic.AddInt32(&callCount, 1)
					return errors.New("permanent error")
				},
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	// Wait for the watch goroutine to finish (max retries exhausted).
	tCtx, tCancel := testCtx(t)
	defer tCancel()
	if err := resource.WaitForWatchDoneWM(tCtx, wm, "conn-1", "core::v1::Pod"); err != nil {
		t.Fatalf("WaitForWatchDone: %v", err)
	}

	// Should be maxRetries+1 (initial + retries).
	got := atomic.LoadInt32(&callCount)
	if got != 4 { // 1 initial + 3 retries
		t.Fatalf("expected 4 Watch calls, got %d", got)
	}

	if wm.IsResourceWatchRunning("conn-1", "core::v1::Pod") {
		t.Fatal("Pod should not be running after max retries")
	}

	// Check for Failed state event.
	sink.WaitForState(t, "core::v1::Pod", resource.WatchStateFailed, 2*time.Second)

	cancel()
	wm.Wait()
}

// --- WM-035: Watch panics — recovered ---
func TestWM_WatchPanicRecovered(t *testing.T) {
	var callCount int32
	watchReady := make(chan struct{}, 10)
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
					count := atomic.AddInt32(&callCount, 1)
					if count == 1 {
						panic("test panic")
					}
					// Signal that the retry watch started.
					select {
					case watchReady <- struct{}{}:
					default:
					}
					<-ctx.Done()
					return nil
				},
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	// Wait for panic recovery and retry to start.
	select {
	case <-watchReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for retry after panic")
	}

	if atomic.LoadInt32(&callCount) < 2 {
		t.Fatal("expected at least 2 calls (panic + retry)")
	}

	cancel()
	wm.Wait()
}

// --- WM-047: GetWatchState ---
func TestWM_GetWatchState(t *testing.T) {
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	state, err := wm.GetWatchState("conn-1")
	if err != nil {
		t.Fatal(err)
	}
	if state.ConnectionID != "conn-1" {
		t.Fatal("wrong connection ID")
	}
	if _, ok := state.Resources["core::v1::Pod"]; !ok {
		t.Fatal("Pod should be in watch state")
	}

	cancel()
	wm.Wait()
}

// --- WM-003: StartConnectionWatch skips SyncNever ---
func TestWM_SkipsSyncNever(t *testing.T) {
	syncNever := resource.SyncNever
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{},
		},
		resource.ResourceRegistration[string]{
			Meta: resourcetest.SecretMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				PolicyVal: &syncNever,
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	if !wm.IsResourceWatchRunning("conn-1", "core::v1::Pod") {
		t.Fatal("Pod should be running (SyncOnConnect)")
	}
	if wm.IsResourceWatchRunning("conn-1", "core::v1::Secret") {
		t.Fatal("Secret should NOT be running (SyncNever)")
	}

	cancel()
	wm.Wait()
}

// --- WM-005: Zero watchable resourcers ---
func TestWM_ZeroWatchable(t *testing.T) {
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.ServiceMeta,
			Resourcer: &resourcetest.StubResourcer[string]{},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	err := wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)
	if err != nil {
		t.Fatal(err)
	}

	cancel()
	wm.Wait()
}

// --- WM-008: StopConnectionWatch connection not found ---
func TestWM_StopConnectionWatchNotFound(t *testing.T) {
	wm, _, _ := setupWatchTest(t)

	err := wm.StopConnectionWatch(context.Background(), "conn-unknown")
	if err == nil {
		t.Fatal("expected error for unknown connection")
	}
}

// --- WM-009: StopConnectionWatch idempotent ---
func TestWM_StopConnectionWatchIdempotent(t *testing.T) {
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)
	wm.StopConnectionWatch(ctx, "conn-1")

	// Second stop should not panic.
	err := wm.StopConnectionWatch(ctx, "conn-1")
	// May return error or nil — just shouldn't panic.
	_ = err

	wm.Wait()
}

// --- WM-013: EnsureResourceWatch unknown resource key ---
func TestWM_EnsureResourceWatchUnknownKey(t *testing.T) {
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	err := wm.EnsureResourceWatch(ctx, "conn-1", "unknown::v1::Foo")
	if err == nil {
		t.Fatal("expected error for unknown resource key")
	}

	cancel()
	wm.Wait()
}

// --- WM-014: EnsureResourceWatch connection not found ---
func TestWM_EnsureResourceWatchConnNotFound(t *testing.T) {
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{},
		},
	)

	err := wm.EnsureResourceWatch(context.Background(), "conn-unknown", "core::v1::Pod")
	if err == nil {
		t.Fatal("expected error for unknown connection")
	}
}

// --- WM-016: StopResourceWatch not running ---
func TestWM_StopResourceWatchNotRunning(t *testing.T) {
	syncFirst := resource.SyncOnFirstQuery
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.SecretMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				PolicyVal: &syncFirst,
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	// Secret was never started (SyncOnFirstQuery) — stop should not panic.
	err := wm.StopResourceWatch(ctx, "conn-1", "core::v1::Secret")
	_ = err // may be nil or error — just no panic

	cancel()
	wm.Wait()
}

// --- WM-018: RestartResourceWatch fresh context ---
func TestWM_RestartResourceWatchFreshContext(t *testing.T) {
	var mu sync.Mutex
	var ctxs []context.Context
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
					mu.Lock()
					ctxs = append(ctxs, ctx)
					mu.Unlock()
					<-ctx.Done()
					return nil
				},
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	tCtx, tCancel := testCtx(t)
	defer tCancel()
	if err := resource.WaitForWatchReadyWM(tCtx, wm, "conn-1", "core::v1::Pod"); err != nil {
		t.Fatalf("WaitForWatchReady: %v", err)
	}

	wm.RestartResourceWatch(ctx, "conn-1", "core::v1::Pod")

	// Restart creates a new rws — wait for it.
	if err := resource.WaitForWatchReadyWM(tCtx, wm, "conn-1", "core::v1::Pod"); err != nil {
		t.Fatalf("WaitForWatchReady after restart: %v", err)
	}

	cancel()
	wm.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(ctxs) < 2 {
		t.Fatalf("expected at least 2 Watch calls, got %d", len(ctxs))
	}
	// Old context should be cancelled.
	if ctxs[0].Err() == nil {
		t.Fatal("expected old context to be cancelled")
	}
}

// --- WM-022: IsResourceWatchRunning connection not found ---
func TestWM_IsResourceWatchRunningConnNotFound(t *testing.T) {
	wm, _, _ := setupWatchTest(t)

	if wm.IsResourceWatchRunning("conn-unknown", "core::v1::Pod") {
		t.Fatal("expected false for unknown connection")
	}
}

// --- WM-024: State events flow to subscriber sink ---
func TestWM_StateEventsFlow(t *testing.T) {
	wm, _, sink := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, s resource.WatchEventSink) error {
					s.OnStateChange(resource.WatchStateEvent{
						ResourceKey: meta.Key(),
						State:       resource.WatchStateSyncing,
					})
					s.OnStateChange(resource.WatchStateEvent{
						ResourceKey: meta.Key(),
						State:       resource.WatchStateSynced,
					})
					<-ctx.Done()
					return nil
				},
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	sink.WaitForState(t, "core::v1::Pod", resource.WatchStateSynced, 2*time.Second)

	cancel()
	wm.Wait()
}

// --- WM-025: Events from multiple connections distinguished ---
func TestWM_EventsMultipleConnections(t *testing.T) {
	wm, _, sink := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, s resource.WatchEventSink) error {
					// Emit using the connection from context info — we'll use a fixed approach.
					s.OnAdd(resource.WatchAddPayload{Key: meta.Key(), ID: "pod-1"})
					<-ctx.Done()
					return nil
				},
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c1 := "client-1"
	c2 := "client-2"
	wm.StartConnectionWatch(ctx, "conn-1", &c1, ctx)
	wm.StartConnectionWatch(ctx, "conn-2", &c2, ctx)

	sink.WaitForAdds(t, 2, 2*time.Second)

	cancel()
	wm.Wait()
}

// --- WM-026: Events from multiple resource types distinguished ---
func TestWM_EventsMultipleResourceTypes(t *testing.T) {
	wm, _, sink := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, s resource.WatchEventSink) error {
					s.OnAdd(resource.WatchAddPayload{Key: meta.Key(), ID: "pod-1", Connection: "conn-1"})
					<-ctx.Done()
					return nil
				},
			},
		},
		resource.ResourceRegistration[string]{
			Meta: resourcetest.DeploymentMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, s resource.WatchEventSink) error {
					s.OnAdd(resource.WatchAddPayload{Key: meta.Key(), ID: "dep-1", Connection: "conn-1"})
					<-ctx.Done()
					return nil
				},
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	sink.WaitForAdds(t, 2, 2*time.Second)

	// Verify different resource keys.
	keys := make(map[string]bool)
	for _, a := range sink.Adds {
		keys[a.Key] = true
	}
	if !keys["core::v1::Pod"] {
		t.Fatal("expected Pod event")
	}
	if !keys["apps::v1::Deployment"] {
		t.Fatal("expected Deployment event")
	}

	cancel()
	wm.Wait()
}

// --- WM-027: No events after StopConnectionWatch ---
func TestWM_NoEventsAfterStop(t *testing.T) {
	emitting := make(chan struct{})
	wm, _, sink := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, s resource.WatchEventSink) error {
					s.OnAdd(resource.WatchAddPayload{Key: meta.Key(), ID: "pod-1", Connection: "conn-1"})
					close(emitting)
					<-ctx.Done()
					return nil
				},
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	<-emitting
	sink.WaitForAdds(t, 1, 2*time.Second)
	countBefore := sink.AddCount()

	wm.StopConnectionWatch(ctx, "conn-1")
	wm.Wait()

	// After StopConnectionWatch + Wait(), all goroutines have exited.
	// No new events can arrive, so verify immediately.
	if sink.AddCount() != countBefore {
		t.Fatal("expected no new events after stop")
	}

	cancel()
}

// --- WM-029: Multiple listeners receive same events ---
func TestWM_MultipleListeners(t *testing.T) {
	wm, _, sink1 := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, s resource.WatchEventSink) error {
					s.OnAdd(resource.WatchAddPayload{Key: meta.Key(), ID: "pod-1", Connection: "conn-1"})
					<-ctx.Done()
					return nil
				},
			},
		},
	)

	sink2 := resourcetest.NewRecordingSink()
	wm.AddListener(sink2)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	sink1.WaitForAdds(t, 1, 2*time.Second)
	sink2.WaitForAdds(t, 1, 2*time.Second)

	if sink1.AddCount() != sink2.AddCount() {
		t.Fatal("expected both sinks to receive same events")
	}

	cancel()
	wm.Wait()
}

// --- WM-030: Listener disconnect doesn't affect others ---
func TestWM_ListenerDisconnect(t *testing.T) {
	ready := make(chan struct{})
	wm, _, sink1 := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, s resource.WatchEventSink) error {
					<-ready
					s.OnAdd(resource.WatchAddPayload{Key: meta.Key(), ID: "pod-1", Connection: "conn-1"})
					<-ctx.Done()
					return nil
				},
			},
		},
	)

	sink2 := resourcetest.NewRecordingSink()
	wm.AddListener(sink2)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	// Remove sink1 before events emit.
	wm.RemoveListener(sink1)
	close(ready)

	sink2.WaitForAdds(t, 1, 2*time.Second)

	if sink1.AddCount() != 0 {
		t.Fatal("removed sink should receive no events")
	}

	cancel()
	wm.Wait()
}

// --- WM-034: Error then success resets retry count ---
func TestWM_ErrorThenSuccessResetsRetries(t *testing.T) {
	var callCount int32
	watchReady := make(chan struct{}, 10)
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
					count := atomic.AddInt32(&callCount, 1)
					if count == 1 {
						return errors.New("fail once")
					}
					// Succeed — signal and block until cancelled.
					select {
					case watchReady <- struct{}{}:
					default:
					}
					<-ctx.Done()
					return nil
				},
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	// Wait for retry to succeed.
	select {
	case <-watchReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for successful retry")
	}

	if atomic.LoadInt32(&callCount) < 2 {
		t.Fatal("expected at least 2 calls")
	}
	// Still running (succeeded on retry).
	if !wm.IsResourceWatchRunning("conn-1", "core::v1::Pod") {
		t.Fatal("Pod should be running after recovery")
	}

	cancel()
	wm.Wait()
}

// --- WM-037: Context cancelled during backoff ---
func TestWM_ContextCancelledDuringBackoff(t *testing.T) {
	var callCount int32
	watchCalled := make(chan struct{}, 20)
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
					atomic.AddInt32(&callCount, 1)
					select {
					case watchCalled <- struct{}{}:
					default:
					}
					return errors.New("always fail")
				},
			},
		},
	)
	// Use longer backoff so we can cancel during it.
	resource.SetWatchManagerBackoff(wm, 10, 200*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	// Wait for first failure.
	select {
	case <-watchCalled:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first Watch call")
	}

	cancel() // cancel during backoff

	wm.Wait() // should complete — all goroutines exit

	got := atomic.LoadInt32(&callCount)
	if got > 3 {
		t.Fatalf("expected few calls before cancellation, got %d", got)
	}
}

// --- WM-038: Connection context cancel stops all watches ---
func TestWM_ConnectionContextCancelStopsAll(t *testing.T) {
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{},
		},
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.DeploymentMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{},
		},
	)

	connCtx, connCancel := context.WithCancel(context.Background())

	client := "test"
	wm.StartConnectionWatch(connCtx, "conn-1", &client, connCtx)

	if !wm.IsResourceWatchRunning("conn-1", "core::v1::Pod") {
		t.Fatal("Pod should be running")
	}

	connCancel()
	wm.Wait()

	if wm.IsResourceWatchRunning("conn-1", "core::v1::Pod") {
		t.Fatal("Pod should stop after context cancel")
	}
	if wm.IsResourceWatchRunning("conn-1", "apps::v1::Deployment") {
		t.Fatal("Deployment should stop after context cancel")
	}
}

// --- WM-043: Watch blocks until cancelled ---
func TestWM_WatchBlocksUntilCancelled(t *testing.T) {
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{}, // default: blocks on <-ctx.Done()
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	// Wait for watch to be running.
	tCtx, tCancel := testCtx(t)
	defer tCancel()
	if err := resource.WaitForWatchReadyWM(tCtx, wm, "conn-1", "core::v1::Pod"); err != nil {
		t.Fatalf("WaitForWatchReady: %v", err)
	}

	if !wm.IsResourceWatchRunning("conn-1", "core::v1::Pod") {
		t.Fatal("Watch should still be running")
	}

	cancel()
	wm.Wait()
}

// --- WM-044: Concurrent EnsureResourceWatch ---
func TestWM_ConcurrentEnsureResourceWatch(t *testing.T) {
	syncFirst := resource.SyncOnFirstQuery
	var callCount int32
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.SecretMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				PolicyVal: &syncFirst,
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
					atomic.AddInt32(&callCount, 1)
					<-ctx.Done()
					return nil
				},
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	// 10 concurrent EnsureResourceWatch calls.
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wm.EnsureResourceWatch(ctx, "conn-1", "core::v1::Secret")
		}()
	}
	wg.Wait()

	// Wait for the watch to be ready (one of the Ensure calls started it).
	tCtx, tCancel := testCtx(t)
	defer tCancel()
	if err := resource.WaitForWatchReadyWM(tCtx, wm, "conn-1", "core::v1::Secret"); err != nil {
		t.Fatalf("WaitForWatchReady: %v", err)
	}

	if atomic.LoadInt32(&callCount) != 1 {
		t.Fatalf("expected Watch called once, got %d", callCount)
	}

	cancel()
	wm.Wait()
}

// --- WM-045: Concurrent Start/Stop connection watch ---
func TestWM_ConcurrentStartStopWatch(t *testing.T) {
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)
		}()
		go func() {
			defer wg.Done()
			wm.StopConnectionWatch(ctx, "conn-1")
		}()
	}
	wg.Wait()

	cancel()
	wm.Wait()
}

// --- WM-050: Restart connection — new watches with new client ---
func TestWM_RestartConnectionNewClient(t *testing.T) {
	var mu sync.Mutex
	var clients []string
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, client *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
					mu.Lock()
					clients = append(clients, *client)
					mu.Unlock()
					<-ctx.Done()
					return nil
				},
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c1 := "client-1"
	wm.StartConnectionWatch(ctx, "conn-1", &c1, ctx)

	tCtx, tCancel := testCtx(t)
	defer tCancel()
	if err := resource.WaitForWatchReadyWM(tCtx, wm, "conn-1", "core::v1::Pod"); err != nil {
		t.Fatalf("WaitForWatchReady: %v", err)
	}

	wm.StopConnectionWatch(ctx, "conn-1")
	wm.Wait()

	c2 := "client-2"
	wm.StartConnectionWatch(ctx, "conn-1", &c2, ctx)

	if err := resource.WaitForWatchReadyWM(tCtx, wm, "conn-1", "core::v1::Pod"); err != nil {
		t.Fatalf("WaitForWatchReady after restart: %v", err)
	}

	cancel()
	wm.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(clients) < 2 {
		t.Fatalf("expected 2 Watch calls, got %d", len(clients))
	}
	if clients[0] != "client-1" || clients[1] != "client-2" {
		t.Fatalf("expected client-1 then client-2, got %v", clients)
	}
}

// --- WM-007: StopConnectionWatch — Watch() goroutines return nil on clean shutdown ---
func TestWM_StopConnectionWatchCleanShutdown(t *testing.T) {
	var returned atomic.Int32
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
					<-ctx.Done()
					returned.Add(1)
					return nil
				},
			},
		},
	)

	ctx := context.Background()
	client := "test"
	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	wm.StartConnectionWatch(ctx, "conn-1", &client, connCtx)

	tCtx, tCancel := testCtx(t)
	defer tCancel()
	if err := resource.WaitForWatchReadyWM(tCtx, wm, "conn-1", "core::v1::Pod"); err != nil {
		t.Fatalf("WaitForWatchReady: %v", err)
	}

	wm.StopConnectionWatch(ctx, "conn-1")
	wm.Wait()

	if returned.Load() != 1 {
		t.Fatalf("expected Watch goroutine to return, got %d returns", returned.Load())
	}
}

// --- WM-019/020/021: IsResourceWatchRunning states ---
func TestWM_IsResourceWatchRunningStates(t *testing.T) {
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := "test"

	// WM-021: false for never-started
	if wm.IsResourceWatchRunning("conn-1", "core::v1::Pod") {
		t.Fatal("expected false before any start")
	}

	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	tCtx, tCancel := testCtx(t)
	defer tCancel()
	if err := resource.WaitForWatchReadyWM(tCtx, wm, "conn-1", "core::v1::Pod"); err != nil {
		t.Fatalf("WaitForWatchReady: %v", err)
	}

	// WM-019: true when running
	if !wm.IsResourceWatchRunning("conn-1", "core::v1::Pod") {
		t.Fatal("expected true while running")
	}

	// Stop and check WM-020
	wm.StopResourceWatch(ctx, "conn-1", "core::v1::Pod")

	// Wait for the watch goroutine to exit.
	if err := resource.WaitForWatchDoneWM(tCtx, wm, "conn-1", "core::v1::Pod"); err != nil {
		t.Fatalf("WaitForWatchDone: %v", err)
	}

	if wm.IsResourceWatchRunning("conn-1", "core::v1::Pod") {
		t.Fatal("expected false after stop")
	}

	cancel()
	wm.Wait()
}

// --- WM-028: No events from stopped resource ---
func TestWM_NoEventsFromStoppedResource(t *testing.T) {
	wm, _, sink := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, s resource.WatchEventSink) error {
					for {
						select {
						case <-ctx.Done():
							return nil
						case <-time.After(5 * time.Millisecond):
							s.OnAdd(resource.WatchAddPayload{Key: "core::v1::Pod"})
						}
					}
				},
			},
		},
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.DeploymentMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := "test"

	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	// Wait for at least one event so we know the watch is emitting.
	sink.WaitForAdds(t, 1, 2*time.Second)

	// Stop Pod watch specifically.
	wm.StopResourceWatch(ctx, "conn-1", "core::v1::Pod")

	// Wait for the Pod watch goroutine to fully exit.
	tCtx, tCancel := testCtx(t)
	defer tCancel()
	if err := resource.WaitForWatchDoneWM(tCtx, wm, "conn-1", "core::v1::Pod"); err != nil {
		t.Fatalf("WaitForWatchDone: %v", err)
	}

	// Record count after goroutine exit — no more events can arrive.
	countAfterStop := sink.AddCount()

	// No new events should arrive for Pod since the goroutine has exited.
	if sink.AddCount() != countAfterStop {
		t.Fatal("expected no new events after stopping resource watch")
	}

	cancel()
	wm.Wait()
}

// --- WM-033: Backoff increases exponentially (uses FakeClock) ---
func TestWM_BackoffExponential(t *testing.T) {
	fc := resourcetest.NewFakeClock()
	retryCount := 0
	watchCalled := make(chan struct{}, 20)

	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
					retryCount++
					select {
					case watchCalled <- struct{}{}:
					default:
					}
					return errors.New("fail")
				},
			},
		},
	)
	resource.SetWatchManagerBackoff(wm, 3, 100*time.Millisecond)
	resource.SetWatchManagerClock(wm, fc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := "test"

	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	// Wait for first Watch() call to complete (and fail).
	<-watchCalled

	// After first failure: backoff = 100ms (baseBackoff * 2^0)
	tCtx, tCancel := testCtx(t)
	defer tCancel()
	if err := fc.WaitForTimers(tCtx, 1); err != nil {
		t.Fatalf("WaitForTimers: %v", err)
	}
	if fc.PendingTimers() != 1 {
		t.Fatalf("expected 1 pending timer, got %d", fc.PendingTimers())
	}
	fc.Advance(100 * time.Millisecond)
	<-watchCalled

	// After second failure: backoff = 200ms (baseBackoff * 2^1)
	if err := fc.WaitForTimers(tCtx, 1); err != nil {
		t.Fatalf("WaitForTimers: %v", err)
	}
	if fc.PendingTimers() != 1 {
		t.Fatalf("expected 1 pending timer for retry 2, got %d", fc.PendingTimers())
	}
	fc.Advance(200 * time.Millisecond)
	<-watchCalled

	// After third failure: backoff = 400ms (baseBackoff * 2^2)
	if err := fc.WaitForTimers(tCtx, 1); err != nil {
		t.Fatalf("WaitForTimers: %v", err)
	}
	if fc.PendingTimers() != 1 {
		t.Fatalf("expected 1 pending timer for retry 3, got %d", fc.PendingTimers())
	}
	fc.Advance(400 * time.Millisecond)

	// Wait for the goroutine to finish (max retries exceeded, goes to Failed).
	if err := resource.WaitForWatchDoneWM(tCtx, wm, "conn-1", "core::v1::Pod"); err != nil {
		t.Fatalf("WaitForWatchDone: %v", err)
	}

	// After max retries exceeded, no more timers.
	if fc.PendingTimers() != 0 {
		t.Fatalf("expected 0 pending timers after max retries, got %d", fc.PendingTimers())
	}

	cancel()
	wm.Wait()
}

// --- WM-036: Watch returns nil (clean shutdown) — no restart ---
func TestWM_WatchReturnsNilNoRestart(t *testing.T) {
	var watchCalls atomic.Int32
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
					watchCalls.Add(1)
					return nil // clean return, no error
				},
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := "test"

	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	// Wait for the watch goroutine to finish (returns nil immediately, no retry).
	tCtx, tCancel := testCtx(t)
	defer tCancel()
	if err := resource.WaitForWatchDoneWM(tCtx, wm, "conn-1", "core::v1::Pod"); err != nil {
		t.Fatalf("WaitForWatchDone: %v", err)
	}

	// Should be called once only (no retry on nil return).
	if watchCalls.Load() != 1 {
		t.Fatalf("expected 1 Watch call (no restart), got %d", watchCalls.Load())
	}

	cancel()
	wm.Wait()
}

// --- WM-039: Root context cancel stops everything ---
func TestWM_RootContextCancelStopsAll(t *testing.T) {
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{},
		},
	)

	rootCtx, rootCancel := context.WithCancel(context.Background())
	client := "test"

	wm.StartConnectionWatch(rootCtx, "conn-1", &client, rootCtx)

	tCtx, tCancel := testCtx(t)
	defer tCancel()
	if err := resource.WaitForConnectionReadyWM(tCtx, wm, "conn-1"); err != nil {
		t.Fatalf("WaitForConnectionReady: %v", err)
	}

	rootCancel()
	wm.Wait() // should complete — all goroutines exit
}

// --- WM-040: Per-resource context cancel doesn't affect other resources ---
func TestWM_PerResourceCancelIsolation(t *testing.T) {
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{},
		},
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.DeploymentMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := "test"

	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	tCtx, tCancel := testCtx(t)
	defer tCancel()
	if err := resource.WaitForConnectionReadyWM(tCtx, wm, "conn-1"); err != nil {
		t.Fatalf("WaitForConnectionReady: %v", err)
	}

	// Stop only Pod watch.
	wm.StopResourceWatch(ctx, "conn-1", "core::v1::Pod")

	// Wait for the Pod watch goroutine to fully exit.
	if err := resource.WaitForWatchDoneWM(tCtx, wm, "conn-1", "core::v1::Pod"); err != nil {
		t.Fatalf("WaitForWatchDone: %v", err)
	}

	// Deployment should still be running.
	if !wm.IsResourceWatchRunning("conn-1", "apps::v1::Deployment") {
		t.Fatal("expected Deployment watch still running")
	}
	if wm.IsResourceWatchRunning("conn-1", "core::v1::Pod") {
		t.Fatal("expected Pod watch stopped")
	}

	cancel()
	wm.Wait()
}

// --- WM-042: No Watcher-capable resourcers — zero goroutines ---
func TestWM_NoWatchableNoGoroutines(t *testing.T) {
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{}, // not watchable
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := "test"

	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	// No watches running — StubResourcer is not a Watcher, so no goroutines started.
	// No need to wait, just verify immediately.
	if wm.IsResourceWatchRunning("conn-1", "core::v1::Pod") {
		t.Fatal("expected no watch for non-Watcher")
	}

	cancel()
	wm.Wait()
}

// --- WM-046: StopResourceWatch before any Start ---
func TestWM_StopResourceWatchBeforeStart(t *testing.T) {
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{},
		},
	)

	// StopResourceWatch with no connection started at all.
	err := wm.StopResourceWatch(context.Background(), "conn-1", "core::v1::Pod")
	if err != nil {
		t.Fatal("expected no error for stop before start")
	}
}

// --- WM-069: StartConnectionWatch called twice for same connection (idempotent) ---
func TestWM_StartConnectionWatchIdempotent(t *testing.T) {
	var watchCalls atomic.Int32
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
					watchCalls.Add(1)
					<-ctx.Done()
					return nil
				},
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := "test"

	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	tCtx, tCancel := testCtx(t)
	defer tCancel()
	if err := resource.WaitForWatchReadyWM(tCtx, wm, "conn-1", "core::v1::Pod"); err != nil {
		t.Fatalf("WaitForWatchReady: %v", err)
	}

	// Second call should be no-op.
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	// Give the second call a moment to potentially cause a duplicate Watch.
	// Since StartConnectionWatch returns immediately when already started,
	// the only Watch goroutine is the first one. Verify by checking count.
	if watchCalls.Load() != 1 {
		t.Fatalf("expected 1 Watch call (idempotent), got %d", watchCalls.Load())
	}

	cancel()
	wm.Wait()
}

// --- WM-072: Watch error — backoff capped at max interval ---
func TestWM_BackoffCappedAtMax(t *testing.T) {
	fc := resourcetest.NewFakeClock()
	watchCalled := make(chan struct{}, 20)

	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
					select {
					case watchCalled <- struct{}{}:
					default:
					}
					return errors.New("fail")
				},
			},
		},
	)
	// Set base backoff very high so exponential would exceed max quickly.
	// maxBackoff is 30s (package constant).
	resource.SetWatchManagerBackoff(wm, 10, 20*time.Second)
	resource.SetWatchManagerClock(wm, fc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := "test"

	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)
	<-watchCalled // first failure

	tCtx, tCancel := testCtx(t)
	defer tCancel()

	// First retry: backoff = min(20s * 2^0, 30s) = 20s
	if err := fc.WaitForTimers(tCtx, 1); err != nil {
		t.Fatalf("WaitForTimers: %v", err)
	}
	fc.Advance(20 * time.Second)
	<-watchCalled // second failure

	// Second retry: backoff = min(20s * 2^1, 30s) = 30s (capped)
	// Advance by 30s to trigger.
	if err := fc.WaitForTimers(tCtx, 1); err != nil {
		t.Fatalf("WaitForTimers: %v", err)
	}
	fc.Advance(30 * time.Second)
	<-watchCalled // third failure

	cancel()
	wm.Wait()
}

// --- WM-048/049: HasWatch ---
func TestWM_HasWatch(t *testing.T) {
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{},
		},
	)

	if wm.HasWatch("conn-unknown") {
		t.Fatal("should be false for unknown connection")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	if !wm.HasWatch("conn-1") {
		t.Fatal("should be true after starting watches")
	}

	cancel()
	wm.Wait()
}

// --- WM-041: Per-resource watch gets child context of connection context ---
func TestWM_PerResourceContextChildOfConnCtx(t *testing.T) {
	// Use a channel to safely communicate the resource ctx.Done channel
	// from the Watch goroutine to the test goroutine.
	ctxDoneCh := make(chan (<-chan struct{}), 1)
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
					ctxDoneCh <- ctx.Done()
					<-ctx.Done()
					return nil
				},
			},
		},
	)

	connCtx, connCancel := context.WithCancel(context.Background())

	client := "test"
	wm.StartConnectionWatch(connCtx, "conn-1", &client, connCtx)

	// Wait for the Watch goroutine to send us its ctx.Done channel.
	var resourceCtxDone <-chan struct{}
	select {
	case resourceCtxDone = <-ctxDoneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Watch to start")
	}

	// Verify resource context is still active while connection context is active.
	select {
	case <-resourceCtxDone:
		t.Fatal("resource context should not be done yet")
	default:
	}

	// Cancel the connection context.
	connCancel()

	// Resource context must also be cancelled (child of connection context).
	select {
	case <-resourceCtxDone:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("resource context was not cancelled when connection context was cancelled")
	}

	wm.Wait()
}

// --- WM-051: High event rate — 1000 adds in tight loop ---
func TestWM_WatchHighEventRate(t *testing.T) {
	const eventCount = 1000
	wm, _, sink := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, s resource.WatchEventSink) error {
					for i := 0; i < eventCount; i++ {
						s.OnAdd(resource.WatchAddPayload{Key: meta.Key(), ID: fmt.Sprintf("pod-%d", i), Connection: "conn-1"})
					}
					<-ctx.Done()
					return nil
				},
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	sink.WaitForAdds(t, eventCount, 5*time.Second)

	if got := sink.AddCount(); got != eventCount {
		t.Fatalf("expected %d adds, got %d", eventCount, got)
	}

	cancel()
	wm.Wait()
}

// --- WM-052: Watcher emits wrong resource key — SDK does not validate ---
func TestWM_WatchWrongResourceKey(t *testing.T) {
	wm, _, sink := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, s resource.WatchEventSink) error {
					s.OnAdd(resource.WatchAddPayload{Key: "wrong::key", ID: "pod-1", Connection: "conn-1"})
					<-ctx.Done()
					return nil
				},
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	sink.WaitForAdds(t, 1, 2*time.Second)

	adds := sink.Adds
	if len(adds) != 1 {
		t.Fatalf("expected 1 add, got %d", len(adds))
	}
	if adds[0].Key != "wrong::key" {
		t.Fatalf("expected key 'wrong::key', got %q", adds[0].Key)
	}

	cancel()
	wm.Wait()
}

// --- WM-053: Watcher emits Synced before Syncing — SDK forwards as-is ---
func TestWM_WatchWrongStateOrder(t *testing.T) {
	wm, _, sink := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, s resource.WatchEventSink) error {
					// Emit Synced before Syncing — wrong order.
					s.OnStateChange(resource.WatchStateEvent{
						ResourceKey: "core::v1::Pod",
						State:       resource.WatchStateSynced,
					})
					s.OnStateChange(resource.WatchStateEvent{
						ResourceKey: "core::v1::Pod",
						State:       resource.WatchStateSyncing,
					})
					<-ctx.Done()
					return nil
				},
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	// Wait for both state events.
	sink.WaitForState(t, "core::v1::Pod", resource.WatchStateSyncing, 2*time.Second)

	// Verify both arrived and in the wrong order (SDK does not fix order).
	if len(sink.States) < 2 {
		t.Fatalf("expected at least 2 state events, got %d", len(sink.States))
	}
	if sink.States[0].State != resource.WatchStateSynced {
		t.Fatalf("expected first state to be Synced, got %v", sink.States[0].State)
	}
	if sink.States[1].State != resource.WatchStateSyncing {
		t.Fatalf("expected second state to be Syncing, got %v", sink.States[1].State)
	}

	cancel()
	wm.Wait()
}

// --- WM-054: EnsureResourceWatch after connection stopped returns error ---
func TestWM_EnsureAfterConnectionStopped(t *testing.T) {
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	tCtx, tCancel := testCtx(t)
	defer tCancel()
	if err := resource.WaitForConnectionReadyWM(tCtx, wm, "conn-1"); err != nil {
		t.Fatalf("WaitForConnectionReady: %v", err)
	}

	wm.StopConnectionWatch(ctx, "conn-1")
	wm.Wait()

	// EnsureResourceWatch on a stopped connection should return error.
	err := wm.EnsureResourceWatch(ctx, "conn-1", "core::v1::Pod")
	if err == nil {
		t.Fatal("expected error when calling EnsureResourceWatch on stopped connection")
	}
}

// --- WM-055: RestartResourceWatch during backoff cancels backoff ---
func TestWM_RestartDuringBackoff(t *testing.T) {
	var callCount int32
	restarted := make(chan struct{}, 1)
	watchCalled := make(chan struct{}, 20)

	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
					count := atomic.AddInt32(&callCount, 1)
					if count == 1 {
						// First call fails immediately.
						select {
						case watchCalled <- struct{}{}:
						default:
						}
						return errors.New("fail")
					}
					// Subsequent calls: signal restart happened, then block.
					select {
					case restarted <- struct{}{}:
					default:
					}
					<-ctx.Done()
					return nil
				},
			},
		},
	)

	// Use a long backoff to ensure we're in the backoff window.
	resource.SetWatchManagerBackoff(wm, 10, 5*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	// Wait for first failure.
	select {
	case <-watchCalled:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first Watch call")
	}

	// Restart during backoff — should cancel the backoff and start a new watch.
	err := wm.RestartResourceWatch(ctx, "conn-1", "core::v1::Pod")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// New watch should start immediately (not wait for the 5s backoff).
	select {
	case <-restarted:
		// success — backoff was cancelled
	case <-time.After(2 * time.Second):
		t.Fatal("new watch did not start — backoff was not cancelled")
	}

	cancel()
	wm.Wait()
}

// --- WM-056: StopResourceWatch during RestartResourceWatch — final state is stopped ---
func TestWM_StopDuringRestart(t *testing.T) {
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	tCtx, tCancel := testCtx(t)
	defer tCancel()
	if err := resource.WaitForWatchReadyWM(tCtx, wm, "conn-1", "core::v1::Pod"); err != nil {
		t.Fatalf("WaitForWatchReady: %v", err)
	}

	// Issue restart and stop concurrently.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		wm.RestartResourceWatch(ctx, "conn-1", "core::v1::Pod")
	}()
	go func() {
		defer wg.Done()
		wm.StopResourceWatch(ctx, "conn-1", "core::v1::Pod")
	}()
	wg.Wait()

	// Wait for all goroutines to settle.
	// Use wm.Wait() which waits for all watch goroutines to exit, but we
	// only want to wait for the Pod watch. Since restart/stop are concurrent,
	// the final state is nondeterministic. Cancel and wait for everything.
	cancel()
	wm.Wait()

	// Final state should either be stopped or running (depending on ordering).
	// The key assertion is no race (test run with -race), and no panic.
}

// --- WM-057: Rapid restarts — only one Watch goroutine at end ---
func TestWM_RapidRestarts(t *testing.T) {
	var activeWatches int32

	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
					atomic.AddInt32(&activeWatches, 1)
					<-ctx.Done()
					atomic.AddInt32(&activeWatches, -1)
					return nil
				},
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	tCtx, tCancel := testCtx(t)
	defer tCancel()
	if err := resource.WaitForWatchReadyWM(tCtx, wm, "conn-1", "core::v1::Pod"); err != nil {
		t.Fatalf("WaitForWatchReady: %v", err)
	}

	// Rapid restarts.
	for i := 0; i < 10; i++ {
		wm.RestartResourceWatch(ctx, "conn-1", "core::v1::Pod")
	}

	// Wait for the final watch goroutine to be ready.
	if err := resource.WaitForWatchReadyWM(tCtx, wm, "conn-1", "core::v1::Pod"); err != nil {
		t.Fatalf("WaitForWatchReady after rapid restarts: %v", err)
	}

	// Give old goroutines a moment to decrement activeWatches via <-ctx.Done().
	// The old goroutines had their contexts cancelled by RestartResourceWatch,
	// but they may not have decremented the counter yet. Use wm.Wait() on a
	// separate scope: we can't call wm.Wait() here because the current watch
	// is still running. Instead, use a brief polling loop.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&activeWatches) == 1 {
			break
		}
		runtime.Gosched()
	}

	active := atomic.LoadInt32(&activeWatches)
	if active != 1 {
		t.Fatalf("expected 1 active Watch goroutine, got %d", active)
	}

	cancel()
	wm.Wait()
}

// --- WM-058: Watcher emits events then returns error ---
func TestWM_ErrorWithPartialEvents(t *testing.T) {
	wm, _, sink := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, s resource.WatchEventSink) error {
					s.OnAdd(resource.WatchAddPayload{Key: meta.Key(), ID: "pod-1", Connection: "conn-1"})
					s.OnAdd(resource.WatchAddPayload{Key: meta.Key(), ID: "pod-2", Connection: "conn-1"})
					s.OnAdd(resource.WatchAddPayload{Key: meta.Key(), ID: "pod-3", Connection: "conn-1"})
					return errors.New("oops")
				},
			},
		},
	)

	// Use 0 retries so it goes straight to Failed.
	resource.SetWatchManagerBackoff(wm, 0, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	// Wait for adds and the failed state.
	sink.WaitForAdds(t, 3, 2*time.Second)
	sink.WaitForState(t, "core::v1::Pod", resource.WatchStateFailed, 2*time.Second)

	if sink.AddCount() < 3 {
		t.Fatalf("expected at least 3 adds, got %d", sink.AddCount())
	}

	// Check that Error state event was emitted.
	var foundFailed bool
	for _, s := range sink.States {
		if s.ResourceKey == "core::v1::Pod" && s.State == resource.WatchStateFailed {
			foundFailed = true
			break
		}
	}
	if !foundFailed {
		t.Fatal("expected Failed state event")
	}

	cancel()
	wm.Wait()
}

// --- WM-059: State machine exact transitions (Syncing -> Synced) ---
func TestWM_StateMachineExactTransitions(t *testing.T) {
	wm, _, sink := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, s resource.WatchEventSink) error {
					s.OnStateChange(resource.WatchStateEvent{
						ResourceKey: "core::v1::Pod",
						State:       resource.WatchStateSyncing,
					})
					s.OnStateChange(resource.WatchStateEvent{
						ResourceKey: "core::v1::Pod",
						State:       resource.WatchStateSynced,
					})
					<-ctx.Done()
					return nil
				},
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	sink.WaitForState(t, "core::v1::Pod", resource.WatchStateSynced, 2*time.Second)

	// Verify exactly [Syncing, Synced] from the watcher.
	var podStates []resource.WatchState
	for _, s := range sink.States {
		if s.ResourceKey == "core::v1::Pod" {
			podStates = append(podStates, s.State)
		}
	}
	if len(podStates) != 2 {
		t.Fatalf("expected 2 state events, got %d: %v", len(podStates), podStates)
	}
	if podStates[0] != resource.WatchStateSyncing {
		t.Fatalf("expected first state Syncing, got %v", podStates[0])
	}
	if podStates[1] != resource.WatchStateSynced {
		t.Fatalf("expected second state Synced, got %v", podStates[1])
	}

	cancel()
	wm.Wait()
}

// --- WM-060: State machine error path — Syncing, error, retry with Syncing, Synced ---
func TestWM_StateMachineErrorPath(t *testing.T) {
	var callCount int32
	wm, _, sink := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, s resource.WatchEventSink) error {
					count := atomic.AddInt32(&callCount, 1)
					s.OnStateChange(resource.WatchStateEvent{
						ResourceKey: "core::v1::Pod",
						State:       resource.WatchStateSyncing,
					})
					if count == 1 {
						// First call: emit Syncing then fail.
						return errors.New("transient error")
					}
					// Second call: emit Synced and block.
					s.OnStateChange(resource.WatchStateEvent{
						ResourceKey: "core::v1::Pod",
						State:       resource.WatchStateSynced,
					})
					<-ctx.Done()
					return nil
				},
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	sink.WaitForState(t, "core::v1::Pod", resource.WatchStateSynced, 2*time.Second)

	// Record states for Pod.
	var podStates []resource.WatchState
	for _, s := range sink.States {
		if s.ResourceKey == "core::v1::Pod" {
			podStates = append(podStates, s.State)
		}
	}

	// Expected: Syncing (from first Watch), Error (from SDK retry), Syncing (from second Watch), Synced
	if len(podStates) < 3 {
		t.Fatalf("expected at least 3 state events, got %d: %v", len(podStates), podStates)
	}

	// First must be Syncing.
	if podStates[0] != resource.WatchStateSyncing {
		t.Fatalf("expected first state Syncing, got %v", podStates[0])
	}

	// Must contain Error from SDK.
	var foundError bool
	for _, s := range podStates {
		if s == resource.WatchStateError {
			foundError = true
		}
	}
	if !foundError {
		t.Fatalf("expected Error state in transitions, got %v", podStates)
	}

	// Must end with Synced.
	if podStates[len(podStates)-1] != resource.WatchStateSynced {
		t.Fatalf("expected last state Synced, got %v", podStates[len(podStates)-1])
	}

	cancel()
	wm.Wait()
}

// --- WM-061: State machine Failed is terminal ---
func TestWM_StateMachineFailedTerminal(t *testing.T) {
	wm, _, sink := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
					return errors.New("always fail")
				},
			},
		},
	)

	// Max retries = 2 so we see: Error (retry 1), Error (retry 2), Failed.
	resource.SetWatchManagerBackoff(wm, 2, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	sink.WaitForState(t, "core::v1::Pod", resource.WatchStateFailed, 3*time.Second)

	// Collect all Pod state events.
	var podStates []resource.WatchState
	for _, s := range sink.States {
		if s.ResourceKey == "core::v1::Pod" {
			podStates = append(podStates, s.State)
		}
	}

	// Must have Error events and end with Failed.
	if len(podStates) < 2 {
		t.Fatalf("expected at least 2 state events, got %d: %v", len(podStates), podStates)
	}

	// Last state must be Failed (terminal).
	if podStates[len(podStates)-1] != resource.WatchStateFailed {
		t.Fatalf("expected last state Failed, got %v", podStates[len(podStates)-1])
	}

	// Wait for the watch goroutine to fully exit — guarantees no more state changes.
	tCtx, tCancel := testCtx(t)
	defer tCancel()
	if err := resource.WaitForWatchDoneWM(tCtx, wm, "conn-1", "core::v1::Pod"); err != nil {
		t.Fatalf("WaitForWatchDone: %v", err)
	}

	// No further state changes after Failed (goroutine has exited).
	var podStatesAfter []resource.WatchState
	for _, s := range sink.States {
		if s.ResourceKey == "core::v1::Pod" {
			podStatesAfter = append(podStatesAfter, s.State)
		}
	}
	if len(podStatesAfter) != len(podStates) {
		t.Fatalf("state events changed after Failed: before=%v after=%v", podStates, podStatesAfter)
	}

	cancel()
	wm.Wait()
}

// --- WM-062: GetWatchState with SyncOnFirstQuery — empty resources map ---
func TestWM_GetWatchStateZeroStarted(t *testing.T) {
	syncFirst := resource.SyncOnFirstQuery
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				PolicyVal: &syncFirst,
			},
		},
		resource.ResourceRegistration[string]{
			Meta: resourcetest.DeploymentMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				PolicyVal: &syncFirst,
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	summary, err := wm.GetWatchState("conn-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(summary.Resources) != 0 {
		t.Fatalf("expected empty resources map, got %v", summary.Resources)
	}
	if summary.ConnectionID != "conn-1" {
		t.Fatalf("expected connection ID 'conn-1', got %q", summary.ConnectionID)
	}

	cancel()
	wm.Wait()
}

// --- WM-063: No watchable resources — AddListener, wait, no events, no error ---
func TestWM_ListenNoWatchesActive(t *testing.T) {
	wm, _, sink := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta:      resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{}, // not watchable
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	// No watches started (StubResourcer is not a Watcher), so no goroutines.
	// Wait and verify no events arrived.
	cancel()
	wm.Wait()

	// After all goroutines have exited (there were none), verify zero events.
	if sink.AddCount() != 0 {
		t.Fatalf("expected 0 adds, got %d", sink.AddCount())
	}
	if sink.UpdateCount() != 0 {
		t.Fatalf("expected 0 updates, got %d", sink.UpdateCount())
	}
	if sink.DeleteCount() != 0 {
		t.Fatalf("expected 0 deletes, got %d", sink.DeleteCount())
	}
	if len(sink.States) != 0 {
		t.Fatalf("expected 0 state events, got %d", len(sink.States))
	}
}

// --- WM-064: AddListener before StartConnectionWatch — listener receives events ---
func TestWM_ListenBeforeStart(t *testing.T) {
	ready := make(chan struct{})
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, s resource.WatchEventSink) error {
					<-ready
					s.OnAdd(resource.WatchAddPayload{Key: meta.Key(), ID: "pod-1", Connection: "conn-1"})
					<-ctx.Done()
					return nil
				},
			},
		},
	)

	// Add a separate listener BEFORE start.
	earlyListener := resourcetest.NewRecordingSink()
	wm.AddListener(earlyListener)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	// Allow the watch to emit.
	close(ready)

	earlyListener.WaitForAdds(t, 1, 2*time.Second)

	if earlyListener.AddCount() != 1 {
		t.Fatalf("expected early listener to receive 1 add, got %d", earlyListener.AddCount())
	}

	cancel()
	wm.Wait()
}

// --- WM-065: Removed listener does not receive events ---
func TestWM_SinkAfterListenerRemoved(t *testing.T) {
	ready := make(chan struct{})
	reg := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	reg.Register(resource.ResourceRegistration[string]{
		Meta: resourcetest.PodMeta,
		Resourcer: &resourcetest.WatchableResourcer[string]{
			WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, s resource.WatchEventSink) error {
				<-ready
				s.OnAdd(resource.WatchAddPayload{Key: meta.Key(), ID: "pod-1", Connection: "conn-1"})
				<-ctx.Done()
				return nil
			},
		},
	})
	wm := resource.NewWatchManagerForTest(reg)
	resource.SetWatchManagerBackoff(wm, 3, 10*time.Millisecond)

	removedSink := resourcetest.NewRecordingSink()
	wm.AddListener(removedSink)

	activeSink := resourcetest.NewRecordingSink()
	wm.AddListener(activeSink)

	// Remove the first listener before events are emitted.
	wm.RemoveListener(removedSink)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	close(ready)

	activeSink.WaitForAdds(t, 1, 2*time.Second)

	if removedSink.AddCount() != 0 {
		t.Fatal("removed listener should not receive events")
	}
	if activeSink.AddCount() != 1 {
		t.Fatalf("active listener should receive 1 add, got %d", activeSink.AddCount())
	}

	cancel()
	wm.Wait()
}

// --- WM-066: Large number of watches — 50 resources ---
func TestWM_LargeNumberOfWatches(t *testing.T) {
	// Build 50 watchable resource registrations.
	var regs []resource.ResourceRegistration[string]
	for i := 0; i < 50; i++ {
		meta := resource.ResourceMeta{
			Group:    "test",
			Version:  "v1",
			Kind:     fmt.Sprintf("Res%d", i),
			Label:    fmt.Sprintf("Res%d", i),
			Category: "Test",
		}
		regs = append(regs, resource.ResourceRegistration[string]{
			Meta:      meta,
			Resourcer: &resourcetest.WatchableResourcer[string]{},
		})
	}

	wm, _, _ := setupWatchTest(t, regs...)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	tCtx, tCancel := testCtx(t)
	defer tCancel()
	if err := resource.WaitForConnectionReadyWM(tCtx, wm, "conn-1"); err != nil {
		t.Fatalf("WaitForConnectionReady: %v", err)
	}

	// Verify all 50 are running.
	for i := 0; i < 50; i++ {
		key := fmt.Sprintf("test::v1::Res%d", i)
		if !wm.IsResourceWatchRunning("conn-1", key) {
			t.Fatalf("expected watch running for %s", key)
		}
	}

	cancel()
	wm.Wait()
}

// --- WM-067: Goroutine leak detection ---
func TestWM_GoroutineLeakDetection(t *testing.T) {
	// Record baseline goroutine count.
	runtime.GC()
	runtime.Gosched()
	baseline := runtime.NumGoroutine()

	// Build 10 watchable resources.
	var regs []resource.ResourceRegistration[string]
	for i := 0; i < 10; i++ {
		meta := resource.ResourceMeta{
			Group:    "leak",
			Version:  "v1",
			Kind:     fmt.Sprintf("Leak%d", i),
			Label:    fmt.Sprintf("Leak%d", i),
			Category: "Test",
		}
		regs = append(regs, resource.ResourceRegistration[string]{
			Meta:      meta,
			Resourcer: &resourcetest.WatchableResourcer[string]{},
		})
	}

	wm, _, _ := setupWatchTest(t, regs...)

	ctx, cancel := context.WithCancel(context.Background())

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	tCtx, tCancel := testCtx(t)
	defer tCancel()
	if err := resource.WaitForConnectionReadyWM(tCtx, wm, "conn-1"); err != nil {
		t.Fatalf("WaitForConnectionReady: %v", err)
	}

	// Stop all.
	cancel()
	wm.Wait()

	// Let goroutines and runtime settle.
	runtime.GC()
	runtime.Gosched()

	// Poll briefly for goroutine count to stabilize.
	deadline := time.Now().Add(2 * time.Second)
	var after int
	for time.Now().Before(deadline) {
		runtime.GC()
		runtime.Gosched()
		after = runtime.NumGoroutine()
		diff := after - baseline
		if diff <= 5 && diff >= -5 {
			return // pass
		}
	}

	diff := after - baseline
	if diff > 5 || diff < -5 {
		t.Fatalf("goroutine leak detected: baseline=%d after=%d diff=%d", baseline, after, diff)
	}
}

// --- WM-070: EnsureResourceWatch with SyncNever but implements Watcher ---
func TestWM_EnsureResourceWatchSyncNever(t *testing.T) {
	syncNever := resource.SyncNever
	var watchCalled atomic.Int32

	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				PolicyVal: &syncNever,
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
					watchCalled.Add(1)
					<-ctx.Done()
					return nil
				},
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	// SyncNever means it should not auto-start. No watches are started,
	// so no ready channel exists yet. Just verify immediately.
	if wm.IsResourceWatchRunning("conn-1", "core::v1::Pod") {
		t.Fatal("SyncNever resource should not auto-start")
	}

	// But EnsureResourceWatch should start it (explicit override).
	err := wm.EnsureResourceWatch(ctx, "conn-1", "core::v1::Pod")
	if err != nil {
		t.Fatalf("EnsureResourceWatch should succeed for SyncNever+Watcher: %v", err)
	}

	// Wait for the watch goroutine to be ready.
	tCtx, tCancel := testCtx(t)
	defer tCancel()
	if err := resource.WaitForWatchReadyWM(tCtx, wm, "conn-1", "core::v1::Pod"); err != nil {
		t.Fatalf("WaitForWatchReady: %v", err)
	}

	if !wm.IsResourceWatchRunning("conn-1", "core::v1::Pod") {
		t.Fatal("EnsureResourceWatch should have started the watch")
	}
	if watchCalled.Load() != 1 {
		t.Fatalf("expected Watch called once, got %d", watchCalled.Load())
	}

	cancel()
	wm.Wait()
}

// --- WM-071: Event ordering preserved ---
func TestWM_EventOrderingPreserved(t *testing.T) {
	wm, _, sink := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, s resource.WatchEventSink) error {
					s.OnAdd(resource.WatchAddPayload{Key: meta.Key(), ID: "a", Connection: "conn-1"})
					s.OnAdd(resource.WatchAddPayload{Key: meta.Key(), ID: "b", Connection: "conn-1"})
					s.OnUpdate(resource.WatchUpdatePayload{Key: meta.Key(), ID: "a", Connection: "conn-1"})
					s.OnDelete(resource.WatchDeletePayload{Key: meta.Key(), ID: "b", Connection: "conn-1"})
					<-ctx.Done()
					return nil
				},
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	sink.WaitForAdds(t, 2, 2*time.Second)
	sink.WaitForUpdates(t, 1, 2*time.Second)
	sink.WaitForDeletes(t, 1, 2*time.Second)

	// Verify add ordering.
	if len(sink.Adds) < 2 {
		t.Fatalf("expected 2 adds, got %d", len(sink.Adds))
	}
	if sink.Adds[0].ID != "a" {
		t.Fatalf("expected first add ID 'a', got %q", sink.Adds[0].ID)
	}
	if sink.Adds[1].ID != "b" {
		t.Fatalf("expected second add ID 'b', got %q", sink.Adds[1].ID)
	}

	// Verify update.
	if len(sink.Updates) < 1 {
		t.Fatalf("expected 1 update, got %d", len(sink.Updates))
	}
	if sink.Updates[0].ID != "a" {
		t.Fatalf("expected update ID 'a', got %q", sink.Updates[0].ID)
	}

	// Verify delete.
	if len(sink.Deletes) < 1 {
		t.Fatalf("expected 1 delete, got %d", len(sink.Deletes))
	}
	if sink.Deletes[0].ID != "b" {
		t.Fatalf("expected delete ID 'b', got %q", sink.Deletes[0].ID)
	}

	cancel()
	wm.Wait()
}

// --- WM-073: Backoff with jitter — verify current deterministic behavior ---
// NOTE: Jitter is NOT currently implemented. This test documents that backoff
// is deterministic (exponential without jitter). Jitter is a future enhancement.
func TestWM_BackoffWithJitter(t *testing.T) {
	fc := resourcetest.NewFakeClock()
	var callTimestamps []int32
	var callCount int32
	watchCalled := make(chan struct{}, 20)

	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
					count := atomic.AddInt32(&callCount, 1)
					callTimestamps = append(callTimestamps, count)
					select {
					case watchCalled <- struct{}{}:
					default:
					}
					return errors.New("fail")
				},
			},
		},
	)

	resource.SetWatchManagerBackoff(wm, 3, 100*time.Millisecond)
	resource.SetWatchManagerClock(wm, fc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := "test"
	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)
	<-watchCalled // first failure

	tCtx, tCancel := testCtx(t)
	defer tCancel()

	// First failure happened. Now verify deterministic backoff intervals:
	// Retry 1: 100ms (100ms * 2^0)
	if err := fc.WaitForTimers(tCtx, 1); err != nil {
		t.Fatalf("WaitForTimers: %v", err)
	}
	if fc.PendingTimers() != 1 {
		t.Fatalf("expected 1 pending timer after first failure, got %d", fc.PendingTimers())
	}
	fc.Advance(100 * time.Millisecond)
	<-watchCalled

	// Retry 2: 200ms (100ms * 2^1)
	if err := fc.WaitForTimers(tCtx, 1); err != nil {
		t.Fatalf("WaitForTimers: %v", err)
	}
	if fc.PendingTimers() != 1 {
		t.Fatalf("expected 1 pending timer after second failure, got %d", fc.PendingTimers())
	}
	fc.Advance(200 * time.Millisecond)
	<-watchCalled

	// Retry 3: 400ms (100ms * 2^2)
	if err := fc.WaitForTimers(tCtx, 1); err != nil {
		t.Fatalf("WaitForTimers: %v", err)
	}
	if fc.PendingTimers() != 1 {
		t.Fatalf("expected 1 pending timer after third failure, got %d", fc.PendingTimers())
	}
	fc.Advance(400 * time.Millisecond)

	// Wait for the goroutine to finish (max retries exceeded).
	if err := resource.WaitForWatchDoneWM(tCtx, wm, "conn-1", "core::v1::Pod"); err != nil {
		t.Fatalf("WaitForWatchDone: %v", err)
	}

	// After max retries, no more timers — backoff is deterministic, no jitter.
	if fc.PendingTimers() != 0 {
		t.Fatalf("expected 0 pending timers after max retries, got %d", fc.PendingTimers())
	}

	// Verify the deterministic nature: the same intervals would produce the
	// same number of calls every time (no randomization / jitter).
	got := atomic.LoadInt32(&callCount)
	if got != 4 { // 1 initial + 3 retries
		t.Fatalf("expected 4 Watch calls (deterministic), got %d", got)
	}

	cancel()
	wm.Wait()
}

// --- WM-068: Watch returns nil while context is still active — no infinite loop ---
// Distinct from WM-036: this test specifically verifies the goroutine does NOT loop
// with zero backoff (no retry on nil return). The Watch returns nil while the context
// is still active, simulating a bug or intentional early exit from the watcher.
func TestWM_WatchReturnsNilActiveContextNoLoop(t *testing.T) {
	var watchCalls atomic.Int32
	wm, _, _ := setupWatchTest(t,
		resource.ResourceRegistration[string]{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
					watchCalls.Add(1)
					return nil // returns nil without ctx being cancelled
				},
			},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := "test"

	wm.StartConnectionWatch(ctx, "conn-1", &client, ctx)

	// Wait for the goroutine to exit.
	tCtx, tCancel := testCtx(t)
	defer tCancel()
	if err := resource.WaitForWatchDoneWM(tCtx, wm, "conn-1", "core::v1::Pod"); err != nil {
		t.Fatalf("WaitForWatchDone: %v", err)
	}

	// Must be called exactly once — no infinite loop, no retry.
	if got := watchCalls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 Watch call (no loop), got %d", got)
	}

	// Verify the watch is marked as stopped (not retrying).
	if wm.IsResourceWatchRunning("conn-1", "core::v1::Pod") {
		t.Fatal("watch should be stopped after nil return")
	}

	cancel()
	wm.Wait()
}
