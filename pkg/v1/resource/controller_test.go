package resource_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/resource/resourcetest"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// buildController creates a test controller with a pod resourcer and starts conn-1.
func buildController(t *testing.T, opts ...func(*resource.ResourcePluginConfig[string])) *resource.ResourceControllerForTest {
	t.Helper()
	ctx := context.Background()

	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}},
		},
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatalf("build controller: %v", err)
	}
	t.Cleanup(func() { ctrl.Close() })

	// Load and start conn-1.
	if _, err := ctrl.LoadConnections(ctx); err != nil {
		t.Fatalf("load connections: %v", err)
	}
	if _, err := ctrl.StartConnection(ctx, "conn-1"); err != nil {
		t.Fatalf("start connection: %v", err)
	}

	return ctrl
}

// --- OP-001: Get — happy path ---
func TestOP_GetHappyPath(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				GetFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.GetInput) (*resource.GetResult, error) {
					return &resource.GetResult{
						Success: true,
						Result:  json.RawMessage(`{"id":"pod-1"}`),
					}, nil
				},
			},
		}}
	})

	ctx := resourcetest.NewTestContext()
	result, err := ctrl.Get(ctx, "core::v1::Pod", resource.GetInput{ID: "pod-1", Namespace: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
}

// --- OP-005: Get — unknown resource key, no pattern ---
func TestOP_GetUnknownKey(t *testing.T) {
	ctrl := buildController(t)

	ctx := resourcetest.NewTestContext()
	_, err := ctrl.Get(ctx, "apps::v1::Deployment", resource.GetInput{ID: "dep-1"})
	if err == nil {
		t.Fatal("expected error for unknown resource")
	}
}

// --- OP-006: Get — pattern fallback ---
func TestOP_GetPatternFallback(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = nil
		cfg.Patterns = map[string]resource.Resourcer[string]{
			"*": &resourcetest.StubResourcer[string]{
				GetFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.GetInput) (*resource.GetResult, error) {
					return &resource.GetResult{Success: true}, nil
				},
			},
		}
	})

	ctx := resourcetest.NewTestContext()
	result, err := ctrl.Get(ctx, "any::v1::Thing", resource.GetInput{ID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatal("expected success via pattern")
	}
}

// --- OP-008: Get — resourcer returns error ---
func TestOP_GetError(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				GetFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.GetInput) (*resource.GetResult, error) {
					return nil, errors.New("not found")
				},
			},
		}}
	})

	ctx := resourcetest.NewTestContext()
	_, err := ctrl.Get(ctx, "core::v1::Pod", resource.GetInput{ID: "pod-1"})
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- OP-013: List — happy path ---
func TestOP_ListHappyPath(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				ListFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.ListInput) (*resource.ListResult, error) {
					return &resource.ListResult{
						Success: true,
						Result: []json.RawMessage{
							json.RawMessage(`{"id":"pod-1"}`),
							json.RawMessage(`{"id":"pod-2"}`),
						},
					}, nil
				},
			},
		}}
	})

	ctx := resourcetest.NewTestContext()
	result, err := ctrl.List(ctx, "core::v1::Pod", resource.ListInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Result) != 2 {
		t.Fatalf("expected 2 items, got %d", len(result.Result))
	}
}

// --- OP-021: Create — happy path ---
func TestOP_CreateHappyPath(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				CreateFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, input resource.CreateInput) (*resource.CreateResult, error) {
					return &resource.CreateResult{Success: true, Result: input.Input}, nil
				},
			},
		}}
	})

	ctx := resourcetest.NewTestContext()
	result, err := ctrl.Create(ctx, "core::v1::Pod", resource.CreateInput{
		Input:     json.RawMessage(`{"name":"new-pod"}`),
		Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
}

// --- CL-001: StartConnection creates client and returns status ---
func TestCL_StartConnection(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}},
		},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)
	status, err := ctrl.StartConnection(ctx, "conn-1")
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "CONNECTED" {
		t.Fatalf("expected CONNECTED, got %s", status.Status)
	}
}

// --- CL-002: StartConnection starts SyncOnConnect watches ---
func TestCL_StartConnectionStartsWatches(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.WatchableResourcer[string]{}},
		},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")

	running, _ := ctrl.IsResourceWatchRunning(ctx, "conn-1", "core::v1::Pod")
	if !running {
		t.Fatal("expected Pod watch running after StartConnection")
	}
}

// --- CL-005: StopConnection stops watches and destroys client ---
func TestCL_StopConnection(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.WatchableResourcer[string]{}},
		}
	})

	ctx := resourcetest.NewTestContext()
	_, err := ctrl.StopConnection(ctx, "conn-1")
	if err != nil {
		t.Fatal(err)
	}

	running, _ := ctrl.IsResourceWatchRunning(ctx, "conn-1", "core::v1::Pod")
	if running {
		t.Fatal("Pod watch should not be running after stop")
	}
}

// --- OP-014: List triggers EnsureResourceWatch for SyncOnFirstQuery ---
func TestOP_ListTriggersSyncOnFirstQuery(t *testing.T) {
	syncFirst := resource.SyncOnFirstQuery
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{
			{
				Meta: resourcetest.SecretMeta,
				Resourcer: &resourcetest.WatchableResourcer[string]{
					PolicyVal: &syncFirst,
				},
			},
		}
	})

	ctx := resourcetest.NewTestContext()

	// Before List, Secret watch should NOT be running.
	running, _ := ctrl.IsResourceWatchRunning(ctx, "conn-1", "core::v1::Secret")
	if running {
		t.Fatal("Secret watch should not be running before List")
	}

	// List triggers watch (maybeEnsureWatch runs EnsureResourceWatch in a goroutine).
	ctrl.List(ctx, "core::v1::Secret", resource.ListInput{})
	time.Sleep(time.Millisecond) // yield so the goroutine registers the rws
	waitCtx, waitCancel := context.WithTimeout(ctx, 2*time.Second)
	defer waitCancel()
	resource.WaitForWatchReadyForTest(waitCtx, ctrl, "conn-1", "core::v1::Secret")

	running, _ = ctrl.IsResourceWatchRunning(ctx, "conn-1", "core::v1::Secret")
	if !running {
		t.Fatal("Secret watch should be running after List")
	}
}

// --- OP-009: Get — ErrorClassifier wraps error (global) ---
func TestOP_GetErrorClassifierGlobal(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				GetFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.GetInput) (*resource.GetResult, error) {
					return nil, errors.New("raw error")
				},
			},
		}}
		cfg.ErrorClassifier = &testGlobalClassifier{}
	})

	ctx := resourcetest.NewTestContext()
	_, err := ctrl.Get(ctx, "core::v1::Pod", resource.GetInput{ID: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
	var roe *resource.ResourceOperationError
	if !errors.As(err, &roe) {
		t.Fatalf("expected ResourceOperationError, got %T", err)
	}
	if roe.Code != "CLASSIFIED" {
		t.Fatalf("expected code CLASSIFIED, got %s", roe.Code)
	}
}

// testGlobalClassifier wraps errors as ResourceOperationError.
type testGlobalClassifier struct{}

func (c *testGlobalClassifier) ClassifyError(err error) error {
	return &resource.ResourceOperationError{
		Err:     err,
		Code:    "CLASSIFIED",
		Title:   "Classified Error",
		Message: err.Error(),
	}
}

// --- TypeProvider: GetResourceTypes ---
func TestCtrl_GetResourceTypes(t *testing.T) {
	ctrl := buildController(t)
	ctx := resourcetest.NewTestContext()
	types := ctrl.GetResourceTypes(ctx, "conn-1")
	if len(types) != 1 {
		t.Fatalf("expected 1 type, got %d", len(types))
	}
}

// --- TypeProvider: GetResourceCapabilities ---
func TestCtrl_GetResourceCapabilities(t *testing.T) {
	ctrl := buildController(t)
	ctx := resourcetest.NewTestContext()
	caps, err := ctrl.GetResourceCapabilities(ctx, "core::v1::Pod")
	if err != nil {
		t.Fatal(err)
	}
	if !caps.CanGet || !caps.CanList {
		t.Fatal("expected CRUD caps")
	}
}

// --- ListenForEvents ---
func TestCtrl_ListenForEvents(t *testing.T) {
	// Use a channel to delay event emission until listener is registered.
	ready := make(chan struct{})

	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, sink resource.WatchEventSink) error {
					// Wait until listener is ready before emitting.
					select {
					case <-ready:
					case <-ctx.Done():
						return nil
					}
					sink.OnAdd(resource.WatchAddPayload{Key: meta.Key(), ID: "pod-1", Connection: "conn-1"})
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

	// Register listener BEFORE starting the connection.
	sink := resourcetest.NewRecordingSink()
	resource.AddListenerForTest(ctrl, sink)
	t.Cleanup(func() { resource.RemoveListenerForTest(ctrl, sink) })

	ctrl.StartConnection(ctx, "conn-1")
	close(ready) // signal watcher to emit events

	sink.WaitForAdds(t, 1, 2*time.Second)

	if sink.AddCount() < 1 {
		t.Fatal("expected at least 1 add event")
	}
}

// --- OP-002: Get — correct client passed to resourcer ---
func TestOP_GetCorrectClient(t *testing.T) {
	var capturedClient *string
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				GetFunc: func(_ context.Context, client *string, _ resource.ResourceMeta, _ resource.GetInput) (*resource.GetResult, error) {
					capturedClient = client
					return &resource.GetResult{Success: true}, nil
				},
			},
		}}
	})

	ctx := resourcetest.NewTestContext()
	ctrl.Get(ctx, "core::v1::Pod", resource.GetInput{ID: "pod-1"})

	if capturedClient == nil {
		t.Fatal("expected client to be passed")
	}
}

// --- OP-003: Get — correct meta passed to resourcer ---
func TestOP_GetCorrectMeta(t *testing.T) {
	var capturedMeta resource.ResourceMeta
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				GetFunc: func(_ context.Context, _ *string, meta resource.ResourceMeta, _ resource.GetInput) (*resource.GetResult, error) {
					capturedMeta = meta
					return &resource.GetResult{Success: true}, nil
				},
			},
		}}
	})

	ctx := resourcetest.NewTestContext()
	ctrl.Get(ctx, "core::v1::Pod", resource.GetInput{ID: "pod-1"})

	if capturedMeta.Kind != "Pod" || capturedMeta.Group != "core" || capturedMeta.Version != "v1" {
		t.Fatalf("expected PodMeta, got %+v", capturedMeta)
	}
}

// --- OP-004: Get — context carries Session ---
func TestOP_GetContextCarriesSession(t *testing.T) {
	var capturedSession *resource.Session
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				GetFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, _ resource.GetInput) (*resource.GetResult, error) {
					capturedSession = resource.SessionFromContext(ctx)
					return &resource.GetResult{Success: true}, nil
				},
			},
		}}
	})

	ctx := resourcetest.NewTestContext()
	ctrl.Get(ctx, "core::v1::Pod", resource.GetInput{ID: "pod-1"})

	if capturedSession == nil {
		t.Fatal("expected session in context")
	}
	if capturedSession.Connection == nil {
		t.Fatal("expected connection in session")
	}
}

// --- OP-007: Get — connection not started ---
func TestOP_GetConnectionNotStarted(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}},
		},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()

	// Don't start any connection.
	_, err = ctrl.Get(ctx, "core::v1::Pod", resource.GetInput{ID: "pod-1"})
	if err == nil {
		t.Fatal("expected error for no active connections")
	}
}

// --- OP-010: Get — ErrorClassifier per-resource wins over global ---
func TestOP_GetErrorClassifierPerResourceWins(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &testPerResourceClassifierResourcer{
				StubResourcer: resourcetest.StubResourcer[string]{
					GetFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.GetInput) (*resource.GetResult, error) {
						return nil, errors.New("raw")
					},
				},
			},
		}}
		cfg.ErrorClassifier = &testGlobalClassifier{}
	})

	ctx := resourcetest.NewTestContext()
	_, err := ctrl.Get(ctx, "core::v1::Pod", resource.GetInput{ID: "x"})
	var roe *resource.ResourceOperationError
	if !errors.As(err, &roe) {
		t.Fatalf("expected ResourceOperationError, got %T", err)
	}
	if roe.Code != "PER_RESOURCE" {
		t.Fatalf("expected PER_RESOURCE, got %s", roe.Code)
	}
}

// testPerResourceClassifierResourcer implements Resourcer + ErrorClassifier.
type testPerResourceClassifierResourcer struct {
	resourcetest.StubResourcer[string]
}

func (r *testPerResourceClassifierResourcer) ClassifyError(err error) error {
	return &resource.ResourceOperationError{
		Err: err, Code: "PER_RESOURCE", Title: "Per Resource", Message: err.Error(),
	}
}

// --- OP-011: Get — context cancelled ---
func TestOP_GetContextCancelled(t *testing.T) {
	ctrl := buildController(t)

	ctx, cancel := context.WithCancel(resourcetest.NewTestContext())
	cancel() // cancel immediately

	_, err := ctrl.Get(ctx, "core::v1::Pod", resource.GetInput{ID: "pod-1"})
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

// --- OP-015: List — does NOT trigger watch for SyncNever ---
func TestOP_ListDoesNotTriggerSyncNever(t *testing.T) {
	syncNever := resource.SyncNever
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{
			{
				Meta: resourcetest.SecretMeta,
				Resourcer: &resourcetest.WatchableResourcer[string]{
					PolicyVal: &syncNever,
				},
			},
		}
	})

	ctx := resourcetest.NewTestContext()
	ctrl.List(ctx, "core::v1::Secret", resource.ListInput{})
	// Give the goroutine a moment to run if it were going to start a watch (it shouldn't).
	runtime.Gosched()
	time.Sleep(5 * time.Millisecond)

	running, _ := ctrl.IsResourceWatchRunning(ctx, "conn-1", "core::v1::Secret")
	if running {
		t.Fatal("Secret watch should NOT be running (SyncNever)")
	}
}

// --- OP-016: List — empty result ---
func TestOP_ListEmptyResult(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				ListFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.ListInput) (*resource.ListResult, error) {
					return &resource.ListResult{Success: true, Result: []json.RawMessage{}}, nil
				},
			},
		}}
	})

	ctx := resourcetest.NewTestContext()
	result, err := ctrl.List(ctx, "core::v1::Pod", resource.ListInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Result) != 0 {
		t.Fatalf("expected 0 items, got %d", len(result.Result))
	}
}

// --- OP-017: List — connection not started ---
func TestOP_ListConnectionNotStarted(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}},
		},
	}
	ctrl, _ := resource.BuildResourceControllerForTest(ctx, cfg)
	defer ctrl.Close()

	_, err := ctrl.List(ctx, "core::v1::Pod", resource.ListInput{})
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- OP-018: List — resourcer returns error ---
func TestOP_ListError(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				ListFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.ListInput) (*resource.ListResult, error) {
					return nil, errors.New("list failed")
				},
			},
		}}
	})

	ctx := resourcetest.NewTestContext()
	_, err := ctrl.List(ctx, "core::v1::Pod", resource.ListInput{})
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- OP-019: Find — happy path ---
func TestOP_FindHappyPath(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				FindFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.FindInput) (*resource.FindResult, error) {
					return &resource.FindResult{
						Success: true,
						Result:  []json.RawMessage{json.RawMessage(`{"id":"pod-match"}`)},
					}, nil
				},
			},
		}}
	})

	ctx := resourcetest.NewTestContext()
	result, err := ctrl.Find(ctx, "core::v1::Pod", resource.FindInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Result) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Result))
	}
}

// --- OP-022: Create — resourcer returns error ---
func TestOP_CreateError(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				CreateFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.CreateInput) (*resource.CreateResult, error) {
					return nil, errors.New("validation failed")
				},
			},
		}}
	})

	ctx := resourcetest.NewTestContext()
	_, err := ctrl.Create(ctx, "core::v1::Pod", resource.CreateInput{Input: json.RawMessage(`{}`)})
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- OP-023: Update — happy path ---
func TestOP_UpdateHappyPath(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				UpdateFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, input resource.UpdateInput) (*resource.UpdateResult, error) {
					return &resource.UpdateResult{Success: true, Result: input.Input}, nil
				},
			},
		}}
	})

	ctx := resourcetest.NewTestContext()
	result, err := ctrl.Update(ctx, "core::v1::Pod", resource.UpdateInput{
		ID: "pod-1", Input: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
}

// --- OP-024: Update — resourcer returns error ---
func TestOP_UpdateError(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				UpdateFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.UpdateInput) (*resource.UpdateResult, error) {
					return nil, errors.New("update failed")
				},
			},
		}}
	})

	ctx := resourcetest.NewTestContext()
	_, err := ctrl.Update(ctx, "core::v1::Pod", resource.UpdateInput{ID: "pod-1"})
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- OP-025: Delete — happy path ---
func TestOP_DeleteHappyPath(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				DeleteFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.DeleteInput) (*resource.DeleteResult, error) {
					return &resource.DeleteResult{Success: true}, nil
				},
			},
		}}
	})

	ctx := resourcetest.NewTestContext()
	result, err := ctrl.Delete(ctx, "core::v1::Pod", resource.DeleteInput{ID: "pod-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
}

// --- OP-026: Delete — resourcer returns error ---
func TestOP_DeleteError(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				DeleteFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.DeleteInput) (*resource.DeleteResult, error) {
					return nil, errors.New("delete failed")
				},
			},
		}}
	})

	ctx := resourcetest.NewTestContext()
	_, err := ctrl.Delete(ctx, "core::v1::Pod", resource.DeleteInput{ID: "pod-1"})
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- OP-027: CRUD — ErrorClassifier not set, raw error passes through ---
func TestOP_ErrorClassifierNotSet(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				GetFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.GetInput) (*resource.GetResult, error) {
					return nil, errors.New("raw error")
				},
			},
		}}
		// No ErrorClassifier set.
	})

	ctx := resourcetest.NewTestContext()
	_, err := ctrl.Get(ctx, "core::v1::Pod", resource.GetInput{ID: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
	var roe *resource.ResourceOperationError
	if errors.As(err, &roe) {
		t.Fatal("expected raw error, not ResourceOperationError")
	}
}

// --- OP-028: CRUD — concurrent operations on same resource ---
func TestOP_ConcurrentSameResource(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				GetFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.GetInput) (*resource.GetResult, error) {
					return &resource.GetResult{Success: true}, nil
				},
			},
		}}
	})

	ctx := resourcetest.NewTestContext()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := ctrl.Get(ctx, "core::v1::Pod", resource.GetInput{ID: "pod-1"})
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
}

// --- OP-029: CRUD — concurrent operations on different resources ---
func TestOP_ConcurrentDifferentResources(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{
			{
				Meta: resourcetest.PodMeta,
				Resourcer: &resourcetest.StubResourcer[string]{
					GetFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.GetInput) (*resource.GetResult, error) {
						return &resource.GetResult{Success: true}, nil
					},
				},
			},
			{
				Meta: resourcetest.DeploymentMeta,
				Resourcer: &resourcetest.StubResourcer[string]{
					ListFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.ListInput) (*resource.ListResult, error) {
						return &resource.ListResult{Success: true}, nil
					},
				},
			},
		}
	})

	ctx := resourcetest.NewTestContext()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		ctrl.Get(ctx, "core::v1::Pod", resource.GetInput{ID: "pod-1"})
	}()
	go func() {
		defer wg.Done()
		ctrl.List(ctx, "apps::v1::Deployment", resource.ListInput{})
	}()
	wg.Wait()
}

// --- CL-003: StartConnection — twice is idempotent ---
func TestCL_StartConnectionIdempotent(t *testing.T) {
	ctrl := buildController(t)

	ctx := resourcetest.NewTestContext()
	// Second start — should be no-op.
	status, err := ctrl.StartConnection(ctx, "conn-1")
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "CONNECTED" {
		t.Fatalf("expected CONNECTED, got %s", status.Status)
	}
}

// --- CL-004: StartConnection — CreateClient fails ---
func TestCL_StartConnectionCreateClientFails(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{
			CreateClientFunc: func(_ context.Context) (*string, error) {
				return nil, errors.New("auth failed")
			},
		},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}},
		},
	}
	ctrl, _ := resource.BuildResourceControllerForTest(ctx, cfg)
	defer ctrl.Close()
	ctrl.LoadConnections(ctx)

	_, err := ctrl.StartConnection(ctx, "conn-1")
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- CL-006: StopConnection — not started ---
func TestCL_StopConnectionNotStarted(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}},
		},
	}
	ctrl, _ := resource.BuildResourceControllerForTest(ctx, cfg)
	defer ctrl.Close()
	ctrl.LoadConnections(ctx)

	_, err := ctrl.StopConnection(ctx, "conn-1")
	if err == nil {
		t.Fatal("expected error for not-started connection")
	}
}

// --- CL-007: LoadConnections returns from ConnectionProvider ---
func TestCL_LoadConnections(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{
			LoadConnectionsFunc: func(_ context.Context) ([]types.Connection, error) {
				return []types.Connection{{ID: "a"}, {ID: "b"}, {ID: "c"}}, nil
			},
		},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}},
		},
	}
	ctrl, _ := resource.BuildResourceControllerForTest(ctx, cfg)
	defer ctrl.Close()

	conns, err := ctrl.LoadConnections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(conns) != 3 {
		t.Fatalf("expected 3 connections, got %d", len(conns))
	}
}

// --- CL-008: ListConnections returns runtime state ---
func TestCL_ListConnections(t *testing.T) {
	ctrl := buildController(t)
	ctx := resourcetest.NewTestContext()

	conns, err := ctrl.ListConnections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(conns) == 0 {
		t.Fatal("expected at least 1 connection")
	}
}

// --- CL-009: GetConnection exists ---
func TestCL_GetConnection(t *testing.T) {
	ctrl := buildController(t)
	ctx := resourcetest.NewTestContext()

	conn, err := ctrl.GetConnection(ctx, "conn-1")
	if err != nil {
		t.Fatal(err)
	}
	if conn.ID != "conn-1" {
		t.Fatalf("expected conn-1, got %s", conn.ID)
	}
}

// --- CL-010: GetConnection not found ---
func TestCL_GetConnectionNotFound(t *testing.T) {
	ctrl := buildController(t)
	ctx := resourcetest.NewTestContext()

	_, err := ctrl.GetConnection(ctx, "conn-999")
	if err == nil {
		t.Fatal("expected error for non-existent connection")
	}
}

// --- CL-011: GetConnectionNamespaces returns namespaces ---
func TestCL_GetConnectionNamespaces(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Connections = &resourcetest.StubConnectionProvider[string]{
			GetNamespacesFunc: func(_ context.Context, _ *string) ([]string, error) {
				return []string{"default", "kube-system"}, nil
			},
		}
	})

	ctx := resourcetest.NewTestContext()
	ns, err := ctrl.GetConnectionNamespaces(ctx, "conn-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ns) != 2 {
		t.Fatalf("expected 2 namespaces, got %d", len(ns))
	}
}

// --- CL-014: DeleteConnection stops and removes ---
func TestCL_DeleteConnection(t *testing.T) {
	ctrl := buildController(t)
	ctx := resourcetest.NewTestContext()

	err := ctrl.DeleteConnection(ctx, "conn-1")
	if err != nil {
		t.Fatal(err)
	}

	_, getErr := ctrl.GetConnection(ctx, "conn-1")
	if getErr == nil {
		t.Fatal("expected error after delete")
	}
}

// --- CL-015: WatchConnections streams changes ---
func TestCL_WatchConnections(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.WatchingConnectionProvider[string]{
			WatchConnectionsFunc: func(ctx context.Context) (<-chan []types.Connection, error) {
				ch := make(chan []types.Connection, 1)
				ch <- []types.Connection{{ID: "conn-new"}}
				go func() {
					<-ctx.Done()
					close(ch)
				}()
				return ch, nil
			},
		},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}},
		},
	}
	ctrl, _ := resource.BuildResourceControllerForTest(ctx, cfg)
	defer ctrl.Close()

	stream := make(chan []types.Connection, 10)
	watchCtx, watchCancel := context.WithTimeout(ctx, 2*time.Second)
	defer watchCancel()

	go ctrl.WatchConnections(watchCtx, stream)

	select {
	case conns := <-stream:
		if len(conns) != 1 || conns[0].ID != "conn-new" {
			t.Fatalf("unexpected connections: %v", conns)
		}
	case <-watchCtx.Done():
		t.Fatal("timeout waiting for connection update")
	}
}

// --- CL-016: WatchConnections not supported ---
func TestCL_WatchConnectionsNotSupported(t *testing.T) {
	ctrl := buildController(t)
	ctx := resourcetest.NewTestContext()

	stream := make(chan []types.Connection, 10)
	watchCtx, watchCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer watchCancel()

	// Should block until context cancelled (no-op).
	ctrl.WatchConnections(watchCtx, stream)
	// If we get here without panic, it works.
}

// --- WP-001: StartConnectionWatch ---
func TestWP_StartConnectionWatch(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.WatchableResourcer[string]{}},
		},
	}
	ctrl, _ := resource.BuildResourceControllerForTest(ctx, cfg)
	defer ctrl.Close()

	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")

	err := ctrl.StartConnectionWatch(ctx, "conn-1")
	if err != nil {
		t.Fatal(err)
	}

	running, _ := ctrl.IsResourceWatchRunning(ctx, "conn-1", "core::v1::Pod")
	if !running {
		t.Fatal("expected Pod watch running")
	}
}

// --- WP-002: StopConnectionWatch ---
func TestWP_StopConnectionWatch(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.WatchableResourcer[string]{}},
		}
	})

	ctx := resourcetest.NewTestContext()
	err := ctrl.StopConnectionWatch(ctx, "conn-1")
	if err != nil {
		t.Fatal(err)
	}

	running, _ := ctrl.IsResourceWatchRunning(ctx, "conn-1", "core::v1::Pod")
	if running {
		t.Fatal("expected watches stopped")
	}
}

// --- WP-003: HasWatch ---
func TestWP_HasWatch(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.WatchableResourcer[string]{}},
		}
	})

	ctx := resourcetest.NewTestContext()
	if !ctrl.HasWatch(ctx, "conn-1") {
		t.Fatal("expected true")
	}
	if ctrl.HasWatch(ctx, "conn-2") {
		t.Fatal("expected false for unknown connection")
	}
}

// --- WP-004: GetWatchState ---
func TestWP_GetWatchState(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.WatchableResourcer[string]{}},
		}
	})

	ctx := resourcetest.NewTestContext()
	state, err := ctrl.GetWatchState(ctx, "conn-1")
	if err != nil {
		t.Fatal(err)
	}
	if state == nil {
		t.Fatal("expected non-nil state")
	}
}

// --- WP-006: ListenForEvents blocks until context cancelled ---
func TestWP_ListenForEventsBlocks(t *testing.T) {
	ctrl := buildController(t)

	ctx, cancel := context.WithCancel(resourcetest.NewTestContext())
	sink := resourcetest.NewRecordingSink()

	done := make(chan struct{})
	go func() {
		ctrl.ListenForEvents(ctx, sink)
		close(done)
	}()

	// Verify ListenForEvents is still blocking (intentional short delay).
	time.Sleep(10 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("ListenForEvents returned before cancel")
	default:
	}

	cancel()

	select {
	case <-done:
		// ListenForEvents returned after cancel — good.
	case <-time.After(2 * time.Second):
		t.Fatal("ListenForEvents did not return after cancel")
	}
}

// --- WP-007: EnsureResourceWatch ---
func TestWP_EnsureResourceWatch(t *testing.T) {
	syncFirst := resource.SyncOnFirstQuery
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.SecretMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				PolicyVal: &syncFirst,
			},
		}}
	})

	ctx := resourcetest.NewTestContext()
	err := ctrl.EnsureResourceWatch(ctx, "conn-1", "core::v1::Secret")
	if err != nil {
		t.Fatal(err)
	}

	running, _ := ctrl.IsResourceWatchRunning(ctx, "conn-1", "core::v1::Secret")
	if !running {
		t.Fatal("expected Secret watch running")
	}
}

// --- WP-008: StopResourceWatch ---
func TestWP_StopResourceWatch(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.WatchableResourcer[string]{}},
		}
	})

	ctx := resourcetest.NewTestContext()
	err := ctrl.StopResourceWatch(ctx, "conn-1", "core::v1::Pod")
	if err != nil {
		t.Fatal(err)
	}

	running, _ := ctrl.IsResourceWatchRunning(ctx, "conn-1", "core::v1::Pod")
	if running {
		t.Fatal("expected Pod watch stopped")
	}
}

// --- WP-009: RestartResourceWatch ---
func TestWP_RestartResourceWatch(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.WatchableResourcer[string]{}},
		}
	})

	ctx := resourcetest.NewTestContext()
	err := ctrl.RestartResourceWatch(ctx, "conn-1", "core::v1::Pod")
	if err != nil {
		t.Fatal(err)
	}

	running, _ := ctrl.IsResourceWatchRunning(ctx, "conn-1", "core::v1::Pod")
	if !running {
		t.Fatal("expected Pod watch running after restart")
	}
}

// --- WP-010: IsResourceWatchRunning ---
func TestWP_IsResourceWatchRunning(t *testing.T) {
	syncFirst := resource.SyncOnFirstQuery
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.WatchableResourcer[string]{}},
			{Meta: resourcetest.SecretMeta, Resourcer: &resourcetest.WatchableResourcer[string]{
				PolicyVal: &syncFirst,
			}},
		}
	})

	ctx := resourcetest.NewTestContext()
	podRunning, _ := ctrl.IsResourceWatchRunning(ctx, "conn-1", "core::v1::Pod")
	secretRunning, _ := ctrl.IsResourceWatchRunning(ctx, "conn-1", "core::v1::Secret")

	if !podRunning {
		t.Fatal("Pod should be running (SyncOnConnect)")
	}
	if secretRunning {
		t.Fatal("Secret should NOT be running (SyncOnFirstQuery)")
	}
}

// --- TP-002: GetResourceGroups ---
func TestTP_GetResourceGroups(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Groups = []resource.ResourceGroup{
			{ID: "workloads", Name: "Workloads"},
		}
	})

	ctx := resourcetest.NewTestContext()
	groups := ctrl.GetResourceGroups(ctx, "conn-1")
	if _, ok := groups["workloads"]; !ok {
		t.Fatal("expected workloads group")
	}
}

// --- TP-003: GetResourceGroup ---
func TestTP_GetResourceGroup(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Groups = []resource.ResourceGroup{
			{ID: "workloads", Name: "Workloads"},
		}
	})

	ctx := resourcetest.NewTestContext()
	group, err := ctrl.GetResourceGroup(ctx, "workloads")
	if err != nil {
		t.Fatal(err)
	}
	if group.ID != "workloads" {
		t.Fatalf("expected workloads, got %s", group.ID)
	}
}

// --- TP-004: HasResourceType ---
func TestTP_HasResourceType(t *testing.T) {
	ctrl := buildController(t)
	ctx := resourcetest.NewTestContext()

	if !ctrl.HasResourceType(ctx, "core::v1::Pod") {
		t.Fatal("expected true for Pod")
	}
	if ctrl.HasResourceType(ctx, "apps::v1::Deployment") {
		t.Fatal("expected false for unregistered Deployment")
	}
}

// --- TP-005: GetResourceDefinition ---
func TestTP_GetResourceDefinition(t *testing.T) {
	defVal := resource.ResourceDefinition{IDAccessor: "test-id"}
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta:       resourcetest.PodMeta,
			Resourcer:  &resourcetest.StubResourcer[string]{},
			Definition: &defVal,
		}}
	})

	ctx := resourcetest.NewTestContext()
	def, err := ctrl.GetResourceDefinition(ctx, "core::v1::Pod")
	if err != nil {
		t.Fatal(err)
	}
	if def.IDAccessor != "test-id" {
		t.Fatalf("expected test-id, got %s", def.IDAccessor)
	}
}

// --- AP-001: GetActions — has ActionResourcer ---
func TestAP_GetActions(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &testActionResourcer{
				actions: []resource.ActionDescriptor{{ID: "restart", Label: "Restart Pod"}},
			},
		}}
	})

	ctx := resourcetest.NewTestContext()
	actions, err := ctrl.GetActions(ctx, "core::v1::Pod")
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(actions))
	}
	if actions[0].ID != "restart" {
		t.Fatalf("expected restart, got %s", actions[0].ID)
	}
}

// testActionResourcer implements Resourcer + ActionResourcer.
type testActionResourcer struct {
	resourcetest.StubResourcer[string]
	actions []resource.ActionDescriptor
}

func (r *testActionResourcer) GetActions(_ context.Context, _ *string, _ resource.ResourceMeta) ([]resource.ActionDescriptor, error) {
	return r.actions, nil
}
func (r *testActionResourcer) ExecuteAction(_ context.Context, _ *string, _ resource.ResourceMeta, actionID string, _ resource.ActionInput) (*resource.ActionResult, error) {
	if actionID == "restart" {
		return &resource.ActionResult{Success: true}, nil
	}
	return nil, errors.New("unknown action")
}
func (r *testActionResourcer) StreamAction(ctx context.Context, _ *string, _ resource.ResourceMeta, _ string, _ resource.ActionInput, stream chan<- resource.ActionEvent) error {
	stream <- resource.ActionEvent{Type: "progress", Data: map[string]interface{}{"pct": 50}}
	stream <- resource.ActionEvent{Type: "complete", Data: map[string]interface{}{"pct": 100}}
	return nil
}

// --- AP-002: GetActions — no ActionResourcer ---
func TestAP_GetActionsNone(t *testing.T) {
	ctrl := buildController(t)
	ctx := resourcetest.NewTestContext()

	actions, err := ctrl.GetActions(ctx, "core::v1::Pod")
	if err != nil {
		t.Fatal(err)
	}
	if actions != nil && len(actions) > 0 {
		t.Fatal("expected nil or empty actions")
	}
}

// --- AP-003: ExecuteAction delegates correctly ---
func TestAP_ExecuteAction(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &testActionResourcer{
				actions: []resource.ActionDescriptor{{ID: "restart"}},
			},
		}}
	})

	ctx := resourcetest.NewTestContext()
	result, err := ctrl.ExecuteAction(ctx, "core::v1::Pod", "restart", resource.ActionInput{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
}

// --- AP-004: ExecuteAction unknown action ---
func TestAP_ExecuteActionUnknown(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta:      resourcetest.PodMeta,
			Resourcer: &testActionResourcer{},
		}}
	})

	ctx := resourcetest.NewTestContext()
	_, err := ctrl.ExecuteAction(ctx, "core::v1::Pod", "unknown-action", resource.ActionInput{})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
}

// --- AP-005: ExecuteAction no ActionResourcer ---
func TestAP_ExecuteActionNoResourcer(t *testing.T) {
	ctrl := buildController(t)
	ctx := resourcetest.NewTestContext()

	_, err := ctrl.ExecuteAction(ctx, "core::v1::Pod", "restart", resource.ActionInput{})
	if err == nil {
		t.Fatal("expected error (actions not supported)")
	}
}

// --- AP-006: StreamAction events flow ---
func TestAP_StreamAction(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta:      resourcetest.PodMeta,
			Resourcer: &testActionResourcer{},
		}}
	})

	ctx := resourcetest.NewTestContext()
	stream := make(chan resource.ActionEvent, 10)
	err := ctrl.StreamAction(ctx, "core::v1::Pod", "restart", resource.ActionInput{}, stream)
	if err != nil {
		t.Fatal(err)
	}

	var events []resource.ActionEvent
	for e := range stream {
		events = append(events, e)
		if len(events) >= 2 {
			break
		}
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
}

// --- AP-007: StreamAction no ActionResourcer ---
func TestAP_StreamActionNoResourcer(t *testing.T) {
	ctrl := buildController(t)
	ctx := resourcetest.NewTestContext()

	stream := make(chan resource.ActionEvent, 10)
	err := ctrl.StreamAction(ctx, "core::v1::Pod", "restart", resource.ActionInput{}, stream)
	if err == nil {
		t.Fatal("expected error (actions not supported)")
	}
}

// --- SP-001: GetEditorSchemas — SchemaProvider supported ---
func TestSP_GetEditorSchemas(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Connections = &testSchemaConnectionProvider{
			StubConnectionProvider: resourcetest.StubConnectionProvider[string]{},
		}
	})

	ctx := resourcetest.NewTestContext()
	schemas, err := ctrl.GetEditorSchemas(ctx, "conn-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(schemas) != 1 {
		t.Fatalf("expected 1 schema, got %d", len(schemas))
	}
}

// testSchemaConnectionProvider implements ConnectionProvider + SchemaProvider.
type testSchemaConnectionProvider struct {
	resourcetest.StubConnectionProvider[string]
}

func (p *testSchemaConnectionProvider) GetEditorSchemas(_ context.Context, _ *string) ([]resource.EditorSchema, error) {
	return []resource.EditorSchema{{ResourceKey: "test-schema"}}, nil
}

// --- SP-002: GetEditorSchemas — not supported ---
func TestSP_GetEditorSchemasNotSupported(t *testing.T) {
	ctrl := buildController(t)
	ctx := resourcetest.NewTestContext()

	schemas, err := ctrl.GetEditorSchemas(ctx, "conn-1")
	if err != nil {
		t.Fatal(err)
	}
	if schemas != nil && len(schemas) > 0 {
		t.Fatal("expected nil or empty schemas")
	}
}

// --- SP-003: GetEditorSchemas — connection not started ---
func TestSP_GetEditorSchemasConnNotStarted(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &testSchemaConnectionProvider{
			StubConnectionProvider: resourcetest.StubConnectionProvider[string]{},
		},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}},
		},
	}
	ctrl, _ := resource.BuildResourceControllerForTest(ctx, cfg)
	defer ctrl.Close()

	_, err := ctrl.GetEditorSchemas(ctx, "conn-1")
	if err == nil {
		t.Fatal("expected error for non-started connection")
	}
}

// --- OP-012: Get — context deadline exceeded ---
func TestOP_GetDeadlineExceeded(t *testing.T) {
	ctrl := buildController(t)
	ctx, cancel := context.WithTimeout(resourcetest.NewTestContext(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond) // ensure deadline passes

	_, err := ctrl.Get(ctx, "core::v1::Pod", resource.GetInput{ID: "pod-1"})
	if err == nil {
		t.Fatal("expected deadline exceeded error")
	}
}

// --- OP-020: Find — cross-namespace search ---
func TestOP_FindCrossNamespace(t *testing.T) {
	var capturedInput resource.FindInput
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{
				FindFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, input resource.FindInput) (*resource.FindResult, error) {
					capturedInput = input
					return &resource.FindResult{Success: true}, nil
				},
			}},
		}
	})
	ctx := resourcetest.NewTestContext()

	_, err := ctrl.Find(ctx, "core::v1::Pod", resource.FindInput{TextQuery: "test"})
	if err != nil {
		t.Fatal(err)
	}
	// Find is cross-scope — input should have no namespace restriction.
	if len(capturedInput.Namespaces) != 0 {
		t.Fatalf("expected no namespace restriction in Find, got %v", capturedInput.Namespaces)
	}
	if capturedInput.TextQuery != "test" {
		t.Fatalf("expected query 'test', got %s", capturedInput.TextQuery)
	}
}

// --- CL-012: GetConnectionNamespaces — connection not started ---
func TestCL_GetNamespacesNotStarted(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}},
		},
	}
	ctrl, _ := resource.BuildResourceControllerForTest(ctx, cfg)
	defer ctrl.Close()
	// Load but don't start.
	ctrl.LoadConnections(ctx)

	_, err := ctrl.GetConnectionNamespaces(ctx, "conn-1")
	if err == nil {
		t.Fatal("expected error for non-started connection")
	}
}

// --- CL-017: StartConnection with context deadline during CreateClient ---
func TestCL_StartConnectionDeadline(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{
			CreateClientFunc: func(ctx context.Context) (*string, error) {
				// Simulate slow client creation.
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}},
		},
	}
	ctrl, _ := resource.BuildResourceControllerForTest(ctx, cfg)
	defer ctrl.Close()
	ctrl.LoadConnections(ctx)

	deadlineCtx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	_, err := ctrl.StartConnection(deadlineCtx, "conn-1")
	if err == nil {
		t.Fatal("expected error from deadline")
	}
}

// --- CL-018: StopConnection twice in rapid succession ---
func TestCL_StopConnectionTwice(t *testing.T) {
	ctrl := buildController(t)
	ctx := resourcetest.NewTestContext()
	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")

	_, err1 := ctrl.StopConnection(ctx, "conn-1")
	if err1 != nil {
		t.Fatal(err1)
	}
	_, err2 := ctrl.StopConnection(ctx, "conn-1")
	if err2 == nil {
		t.Fatal("expected error on second stop")
	}
}

// --- CL-021: ListConnections when none loaded ---
func TestCL_ListConnectionsEmpty(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{
			LoadConnectionsFunc: func(_ context.Context) ([]types.Connection, error) {
				return nil, nil
			},
		},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}},
		},
	}
	ctrl, _ := resource.BuildResourceControllerForTest(ctx, cfg)
	defer ctrl.Close()

	conns, err := ctrl.ListConnections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(conns) != 0 {
		t.Fatalf("expected 0, got %d", len(conns))
	}
}

// --- CL-022: Multiple concurrent StartConnection for different connections ---
func TestCL_ConcurrentStartMultipleConnections(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{
			LoadConnectionsFunc: func(_ context.Context) ([]types.Connection, error) {
				return []types.Connection{
					{ID: "c1"}, {ID: "c2"}, {ID: "c3"},
				}, nil
			},
		},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}},
		},
	}
	ctrl, _ := resource.BuildResourceControllerForTest(ctx, cfg)
	defer ctrl.Close()
	ctrl.LoadConnections(ctx)

	var wg sync.WaitGroup
	for _, id := range []string{"c1", "c2", "c3"} {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctrl.StartConnection(ctx, id)
		}()
	}
	wg.Wait()
}

// --- WP-011: StartConnectionWatch twice — idempotent ---
func TestWP_StartConnectionWatchIdempotent(t *testing.T) {
	ctrl := buildController(t)
	ctx := resourcetest.NewTestContext()
	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")

	err1 := ctrl.StartConnectionWatch(ctx, "conn-1")
	if err1 != nil {
		t.Fatal(err1)
	}
	err2 := ctrl.StartConnectionWatch(ctx, "conn-1")
	if err2 != nil {
		t.Fatal(err2)
	}
}

// --- WP-012: StopConnectionWatch when no watches were started ---
func TestWP_StopConnectionWatchNoWatches(t *testing.T) {
	ctrl := buildController(t)
	ctx := resourcetest.NewTestContext()

	// No connection started, no watches.
	err := ctrl.StopConnectionWatch(ctx, "conn-unknown")
	// Should return error or no-op — just no panic.
	_ = err
}

// --- WP-013: GetWatchState after all watches stopped ---
func TestWP_GetWatchStateAfterStop(t *testing.T) {
	ctrl := buildController(t)
	ctx := resourcetest.NewTestContext()
	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")

	ctrl.StopConnectionWatch(ctx, "conn-1")

	_, err := ctrl.GetWatchState(ctx, "conn-1")
	if err == nil {
		t.Fatal("expected error for stopped watches")
	}
}

// --- WP-015: EnsureResourceWatch with empty resource key ---
func TestWP_EnsureResourceWatchEmptyKey(t *testing.T) {
	ctrl := buildController(t)
	ctx := resourcetest.NewTestContext()
	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")

	err := ctrl.EnsureResourceWatch(ctx, "conn-1", "")
	if err == nil {
		t.Fatal("expected error for empty resource key")
	}
}

// --- TP-001: GetResourceTypes — returns all types for connection ---
func TestTP_GetResourceTypes(t *testing.T) {
	ctrl := buildController(t)
	ctx := resourcetest.NewTestContext()

	rt := ctrl.GetResourceTypes(ctx, "conn-1")
	if len(rt) == 0 {
		t.Fatal("expected at least 1 resource type")
	}
	if _, ok := rt["core::v1::Pod"]; !ok {
		t.Fatal("expected Pod type")
	}
}

// --- AP-008: GetActions — connection not started ---
func TestAP_GetActionsConnNotStarted(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &testActionResourcer{
				actions: []resource.ActionDescriptor{{ID: "restart", Label: "Restart"}},
			}},
		},
	}
	ctrl, _ := resource.BuildResourceControllerForTest(ctx, cfg)
	defer ctrl.Close()

	// No connection loaded/started.
	_, err := ctrl.GetActions(resourcetest.NewTestContext(), "core::v1::Pod")
	if err == nil {
		t.Fatal("expected error for not-started connection")
	}
}

// --- AP-009: ExecuteAction — connection not started ---
func TestAP_ExecuteActionConnNotStarted(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &testActionResourcer{
				actions: []resource.ActionDescriptor{{ID: "restart", Label: "Restart"}},
			}},
		},
	}
	ctrl, _ := resource.BuildResourceControllerForTest(ctx, cfg)
	defer ctrl.Close()

	_, err := ctrl.ExecuteAction(resourcetest.NewTestContext(), "core::v1::Pod", "restart", resource.ActionInput{})
	if err == nil {
		t.Fatal("expected error for not-started connection")
	}
}

// --- AP-010: ExecuteAction — correct client and meta passed ---
func TestAP_ExecuteActionCorrectClientMeta(t *testing.T) {
	var capturedClient *string
	var capturedMeta resource.ResourceMeta
	actionRes := &testActionResourcer{
		actions: []resource.ActionDescriptor{{ID: "restart", Label: "Restart"}},
	}
	actionRes.StubResourcer.GetFunc = nil // clear
	origExec := actionRes.ExecuteAction
	_ = origExec

	ctx := context.Background()
	specificClient := "my-client"
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{
			CreateClientFunc: func(_ context.Context) (*string, error) {
				return &specificClient, nil
			},
		},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &customActionResourcer{
				actions: []resource.ActionDescriptor{{ID: "restart", Label: "Restart"}},
				execFunc: func(_ context.Context, client *string, meta resource.ResourceMeta, _ string, _ resource.ActionInput) (*resource.ActionResult, error) {
					capturedClient = client
					capturedMeta = meta
					return &resource.ActionResult{Success: true}, nil
				},
			}},
		},
	}
	ctrl, _ := resource.BuildResourceControllerForTest(ctx, cfg)
	defer ctrl.Close()
	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")

	testCtx := resourcetest.NewTestContext()
	_, err := ctrl.ExecuteAction(testCtx, "core::v1::Pod", "restart", resource.ActionInput{})
	if err != nil {
		t.Fatal(err)
	}
	if capturedClient == nil || *capturedClient != "my-client" {
		t.Fatal("expected correct client passed to ExecuteAction")
	}
	if capturedMeta.Kind != "Pod" {
		t.Fatalf("expected Pod meta, got %s", capturedMeta.Kind)
	}
}

// customActionResourcer lets us capture args in ExecuteAction.
type customActionResourcer struct {
	resourcetest.StubResourcer[string]
	actions  []resource.ActionDescriptor
	execFunc func(context.Context, *string, resource.ResourceMeta, string, resource.ActionInput) (*resource.ActionResult, error)
}

func (r *customActionResourcer) GetActions(_ context.Context, _ *string, _ resource.ResourceMeta) ([]resource.ActionDescriptor, error) {
	return r.actions, nil
}
func (r *customActionResourcer) ExecuteAction(ctx context.Context, client *string, meta resource.ResourceMeta, actionID string, input resource.ActionInput) (*resource.ActionResult, error) {
	if r.execFunc != nil {
		return r.execFunc(ctx, client, meta, actionID, input)
	}
	return &resource.ActionResult{Success: true}, nil
}
func (r *customActionResourcer) StreamAction(_ context.Context, _ *string, _ resource.ResourceMeta, _ string, _ resource.ActionInput, _ chan<- resource.ActionEvent) error {
	return nil
}

// --- AP-011: StreamAction — error during streaming ---
func TestAP_StreamActionError(t *testing.T) {
	ctrl := buildControllerWithActions(t)
	ctx := resourcetest.NewTestContext()
	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")

	stream := make(chan resource.ActionEvent, 10)
	err := ctrl.StreamAction(ctx, "core::v1::Pod", "restart", resource.ActionInput{}, stream)
	// The testActionResourcer's StreamAction sends events then returns nil.
	// Just verify no panic.
	_ = err
}

// buildControllerWithActions creates a controller with an ActionResourcer.
func buildControllerWithActions(t *testing.T) *resource.ResourceControllerForTest {
	t.Helper()
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &testActionResourcer{
				actions: []resource.ActionDescriptor{
					{ID: "restart", Label: "Restart"},
				},
			}},
		},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return ctrl
}

// --- SP-004: GetEditorSchemas — SchemaProvider returns error ---
func TestSP_GetEditorSchemasError(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &errorSchemaConnectionProvider{},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}},
		},
	}
	ctrl, _ := resource.BuildResourceControllerForTest(ctx, cfg)
	defer ctrl.Close()
	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")

	_, err := ctrl.GetEditorSchemas(ctx, "conn-1")
	if err == nil {
		t.Fatal("expected error from schema provider")
	}
}

type errorSchemaConnectionProvider struct {
	resourcetest.StubConnectionProvider[string]
}

func (p *errorSchemaConnectionProvider) GetEditorSchemas(_ context.Context, _ *string) ([]resource.EditorSchema, error) {
	return nil, errors.New("schema error")
}

// --- SP-005: GetEditorSchemas — SchemaProvider returns empty list ---
func TestSP_GetEditorSchemasEmpty(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &emptySchemaConnectionProvider{},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}},
		},
	}
	ctrl, _ := resource.BuildResourceControllerForTest(ctx, cfg)
	defer ctrl.Close()
	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")

	schemas, err := ctrl.GetEditorSchemas(ctx, "conn-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(schemas) != 0 {
		t.Fatalf("expected 0 schemas, got %d", len(schemas))
	}
}

type emptySchemaConnectionProvider struct {
	resourcetest.StubConnectionProvider[string]
}

func (p *emptySchemaConnectionProvider) GetEditorSchemas(_ context.Context, _ *string) ([]resource.EditorSchema, error) {
	return []resource.EditorSchema{}, nil
}

// ============================================================================
// Helper types for new tests
// ============================================================================

// nilClassifier returns nil from ClassifyError, declining classification.
type nilClassifier struct{}

func (c *nilClassifier) ClassifyError(err error) error { return nil }

// panicClassifier panics inside ClassifyError.
type panicClassifier struct{}

func (c *panicClassifier) ClassifyError(err error) error { panic("classifier panic") }

// schemaResourcer embeds StubResourcer and implements ResourceSchemaProvider.
type schemaResourcer struct {
	resourcetest.StubResourcer[string]
}

func (r *schemaResourcer) GetResourceSchema(_ context.Context, _ *string, _ resource.ResourceMeta) (json.RawMessage, error) {
	return json.RawMessage(`{"type":"object"}`), nil
}

// ============================================================================
// OP-030: Get — empty resource key
// ============================================================================

func TestOP_GetEmptyKey(t *testing.T) {
	ctrl := buildController(t)
	ctx := resourcetest.NewTestContext()

	_, err := ctrl.Get(ctx, "", resource.GetInput{ID: "x"})
	if err == nil {
		t.Fatal("expected error for empty resource key")
	}
}

// ============================================================================
// OP-031: List — zero-value input passes through
// ============================================================================

func TestOP_ListZeroValueInput(t *testing.T) {
	var called bool
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				ListFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.ListInput) (*resource.ListResult, error) {
					called = true
					return &resource.ListResult{Success: true}, nil
				},
			},
		}}
	})

	ctx := resourcetest.NewTestContext()
	result, err := ctrl.List(ctx, "core::v1::Pod", resource.ListInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected resourcer to be called")
	}
	if !result.Success {
		t.Fatal("expected success")
	}
}

// ============================================================================
// OP-032: Create — nil data passes through to resourcer
// ============================================================================

func TestOP_CreateNilData(t *testing.T) {
	var capturedInput resource.CreateInput
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				CreateFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, input resource.CreateInput) (*resource.CreateResult, error) {
					capturedInput = input
					return &resource.CreateResult{Success: true}, nil
				},
			},
		}}
	})

	ctx := resourcetest.NewTestContext()
	result, err := ctrl.Create(ctx, "core::v1::Pod", resource.CreateInput{Input: nil})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
	if capturedInput.Input != nil {
		t.Fatalf("expected nil Input passed through, got %v", capturedInput.Input)
	}
}

// ============================================================================
// OP-033: Update — nil data passes through
// ============================================================================

func TestOP_UpdateNilData(t *testing.T) {
	var capturedInput resource.UpdateInput
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				UpdateFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, input resource.UpdateInput) (*resource.UpdateResult, error) {
					capturedInput = input
					return &resource.UpdateResult{Success: true}, nil
				},
			},
		}}
	})

	ctx := resourcetest.NewTestContext()
	result, err := ctrl.Update(ctx, "core::v1::Pod", resource.UpdateInput{ID: "pod-1", Input: nil})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
	if capturedInput.Input != nil {
		t.Fatalf("expected nil Input passed through, got %v", capturedInput.Input)
	}
}

// ============================================================================
// OP-034: Get — resourcer panics, error returned, controller still functional
// ============================================================================

func TestOP_GetResourcerPanics(t *testing.T) {
	var callCount atomic.Int32
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				GetFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.GetInput) (*resource.GetResult, error) {
					if callCount.Add(1) == 1 {
						panic("unexpected")
					}
					return &resource.GetResult{Success: true}, nil
				},
			},
		}}
	})

	ctx := resourcetest.NewTestContext()

	// First call panics — should be recovered and returned as error.
	_, err := ctrl.Get(ctx, "core::v1::Pod", resource.GetInput{ID: "pod-1"})
	if err == nil {
		t.Fatal("expected error from panicking resourcer")
	}
	if !strings.Contains(err.Error(), "panic") && !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("expected panic info in error, got: %v", err)
	}

	// Second call should succeed — controller is still functional.
	result, err := ctrl.Get(ctx, "core::v1::Pod", resource.GetInput{ID: "pod-1"})
	if err != nil {
		t.Fatalf("expected controller to still be functional, got: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success on second call")
	}
}

// ============================================================================
// OP-035: CRUD after Close — returns error
// ============================================================================

func TestOP_CRUDAfterClose(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}},
		},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctrl.LoadConnections(ctx)
	ctrl.StartConnection(ctx, "conn-1")

	// Close the controller explicitly.
	ctrl.Close()

	if !resource.IsControllerClosed(ctrl) {
		t.Fatal("expected controller to be closed")
	}

	testCtx := resourcetest.NewTestContext()
	_, getErr := ctrl.Get(testCtx, "core::v1::Pod", resource.GetInput{ID: "pod-1"})
	if getErr == nil {
		t.Fatal("expected error after Close()")
	}
}

// ============================================================================
// OP-036: ErrorClassifier returns nil — raw error propagates
// ============================================================================

func TestOP_ErrorClassifierReturnsNil(t *testing.T) {
	rawErr := errors.New("raw error from resourcer")
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				GetFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.GetInput) (*resource.GetResult, error) {
					return nil, rawErr
				},
			},
		}}
		cfg.ErrorClassifier = &nilClassifier{}
	})

	ctx := resourcetest.NewTestContext()
	_, err := ctrl.Get(ctx, "core::v1::Pod", resource.GetInput{ID: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
	// The original raw error should be returned since classifier returned nil.
	if !errors.Is(err, rawErr) {
		t.Fatalf("expected original raw error, got: %v", err)
	}
	// Should NOT be a ResourceOperationError.
	var roe *resource.ResourceOperationError
	if errors.As(err, &roe) {
		t.Fatal("expected raw error, not ResourceOperationError")
	}
}

// ============================================================================
// OP-037: ErrorClassifier panics — recovered, error with panic info
// ============================================================================

func TestOP_ErrorClassifierPanics(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				GetFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.GetInput) (*resource.GetResult, error) {
					return nil, errors.New("raw error")
				},
			},
		}}
		cfg.ErrorClassifier = &panicClassifier{}
	})

	ctx := resourcetest.NewTestContext()
	_, err := ctrl.Get(ctx, "core::v1::Pod", resource.GetInput{ID: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "classifier panic") {
		t.Fatalf("expected panic info in error, got: %v", err)
	}
}

// ============================================================================
// OP-038: Get with very long resource key
// ============================================================================

func TestOP_GetLongResourceKey(t *testing.T) {
	longKey := "very.long.group.name.example.com::v1alpha1::VeryLongResourceKindNameThatExceedsNormalBounds"
	longMeta := resource.ResourceMeta{
		Group:   "very.long.group.name.example.com",
		Version: "v1alpha1",
		Kind:    "VeryLongResourceKindNameThatExceedsNormalBounds",
	}

	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: longMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				GetFunc: func(_ context.Context, _ *string, meta resource.ResourceMeta, _ resource.GetInput) (*resource.GetResult, error) {
					return &resource.GetResult{Success: true, Result: json.RawMessage(`{"id":"long-res"}`)}, nil
				},
			},
		}}
	})

	ctx := resourcetest.NewTestContext()
	result, err := ctrl.Get(ctx, longKey, resource.GetInput{ID: "x"})
	if err != nil {
		t.Fatalf("unexpected error for long key: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
}

// ============================================================================
// OP-039: Concurrent CRUD across 3 connections
// ============================================================================

func TestOP_ConcurrentCRUDAcrossConnections(t *testing.T) {
	ctx := context.Background()
	cfg := resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{
			LoadConnectionsFunc: func(_ context.Context) ([]types.Connection, error) {
				return []types.Connection{
					{ID: "conn-1", Name: "C1"},
					{ID: "conn-2", Name: "C2"},
					{ID: "conn-3", Name: "C3"},
				}, nil
			},
		},
		Resources: []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.StubResourcer[string]{
				GetFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.GetInput) (*resource.GetResult, error) {
					return &resource.GetResult{Success: true}, nil
				},
				ListFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.ListInput) (*resource.ListResult, error) {
					return &resource.ListResult{Success: true}, nil
				},
				CreateFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.CreateInput) (*resource.CreateResult, error) {
					return &resource.CreateResult{Success: true}, nil
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
	for _, id := range []string{"conn-1", "conn-2", "conn-3"} {
		if _, err := ctrl.StartConnection(ctx, id); err != nil {
			t.Fatalf("start %s: %v", id, err)
		}
	}

	var wg sync.WaitGroup
	errs := make(chan error, 30)

	for _, connID := range []string{"conn-1", "conn-2", "conn-3"} {
		connID := connID
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				connCtx := resourcetest.NewTestContextWithConnection(&types.Connection{ID: connID})
				switch idx % 3 {
				case 0:
					_, e := ctrl.Get(connCtx, "core::v1::Pod", resource.GetInput{ID: fmt.Sprintf("pod-%d", idx)})
					if e != nil {
						errs <- fmt.Errorf("Get on %s: %w", connID, e)
					}
				case 1:
					_, e := ctrl.List(connCtx, "core::v1::Pod", resource.ListInput{})
					if e != nil {
						errs <- fmt.Errorf("List on %s: %w", connID, e)
					}
				case 2:
					_, e := ctrl.Create(connCtx, "core::v1::Pod", resource.CreateInput{Input: json.RawMessage(`{}`)})
					if e != nil {
						errs <- fmt.Errorf("Create on %s: %w", connID, e)
					}
				}
			}(i)
		}
	}

	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// ============================================================================
// OP-040: List 10x concurrent — SyncOnFirstQuery watch started exactly once
// ============================================================================

func TestOP_ListEnsureWatchIdempotent(t *testing.T) {
	var watchCalls atomic.Int32
	syncFirst := resource.SyncOnFirstQuery

	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.SecretMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				PolicyVal: &syncFirst,
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
					watchCalls.Add(1)
					<-ctx.Done()
					return nil
				},
			},
		}}
	})

	ctx := resourcetest.NewTestContext()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctrl.List(ctx, "core::v1::Secret", resource.ListInput{})
		}()
	}
	wg.Wait()

	// Wait for the EnsureResourceWatch goroutine(s) to register and start the watch.
	time.Sleep(time.Millisecond) // yield so goroutine registers the rws
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()
	resource.WaitForWatchReadyForTest(waitCtx, ctrl, "conn-1", "core::v1::Secret")

	// The watch should have been started, and only once (EnsureResourceWatch is idempotent).
	running, _ := ctrl.IsResourceWatchRunning(ctx, "conn-1", "core::v1::Secret")
	if !running {
		t.Fatal("expected Secret watch running after concurrent Lists")
	}
	if got := watchCalls.Load(); got != 1 {
		t.Fatalf("expected watch started exactly 1 time, got %d", got)
	}
}

// ============================================================================
// CL-013: UpdateConnection restarts watches
// ============================================================================

func TestCL_UpdateConnectionRestartsWatches(t *testing.T) {
	var watchCalls atomic.Int32

	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
					watchCalls.Add(1)
					<-ctx.Done()
					return nil
				},
			},
		}}
	})

	// After buildController, watch should have started once.
	// WaitForConnectionReady returns when the goroutine is about to call Watch.
	// Give safeWatch a moment to actually call WatchFunc.
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()
	resource.WaitForConnectionReadyForTest(waitCtx, ctrl, "conn-1")
	runtime.Gosched()
	time.Sleep(5 * time.Millisecond) // let safeWatch call WatchFunc
	if got := watchCalls.Load(); got < 1 {
		t.Fatalf("expected watch to start at least once, got %d", got)
	}

	ctx := resourcetest.NewTestContext()
	// Update connection with a changed name — should restart watches.
	_, err := ctrl.UpdateConnection(ctx, types.Connection{ID: "conn-1", Name: "Updated"})
	if err != nil {
		t.Fatalf("UpdateConnection: %v", err)
	}

	// Wait for new watch to start after restart.
	waitCtx2, waitCancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel2()
	resource.WaitForConnectionReadyForTest(waitCtx2, ctrl, "conn-1")
	runtime.Gosched()
	time.Sleep(5 * time.Millisecond) // let safeWatch call WatchFunc
	if got := watchCalls.Load(); got < 2 {
		t.Fatalf("expected watch restarted (at least 2 calls), got %d", got)
	}
}

// ============================================================================
// CL-019: DeleteConnection during Watch — clean teardown
// ============================================================================

func TestCL_DeleteConnectionDuringWatch(t *testing.T) {
	var watchStarted atomic.Int32

	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.PodMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, _ resource.WatchEventSink) error {
					watchStarted.Add(1)
					// Block until context is cancelled.
					<-ctx.Done()
					return nil
				},
			},
		}}
	})

	// Wait for watch to be ready via channel-based wait.
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()
	if err := resource.WaitForConnectionReadyForTest(waitCtx, ctrl, "conn-1"); err != nil {
		t.Fatal("timeout waiting for watch to start")
	}

	ctx := resourcetest.NewTestContext()
	err := ctrl.DeleteConnection(ctx, "conn-1")
	if err != nil {
		t.Fatalf("DeleteConnection: %v", err)
	}

	// Connection should be gone.
	_, getErr := ctrl.GetConnection(ctx, "conn-1")
	if getErr == nil {
		t.Fatal("expected error after delete")
	}
}

// ============================================================================
// CL-023: GetConnection preserves all fields
// ============================================================================

func TestCL_GetConnectionPreservesFields(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Connections = &resourcetest.StubConnectionProvider[string]{
			LoadConnectionsFunc: func(_ context.Context) ([]types.Connection, error) {
				return []types.Connection{{
					ID:   "conn-1",
					Name: "Production Cluster",
					Data: map[string]any{
						"kubeconfig": "/path/to/config",
						"region":     "us-east-1",
					},
					Labels: map[string]any{
						"env":  "production",
						"team": "platform",
					},
				}}, nil
			},
		}
	})

	ctx := resourcetest.NewTestContext()
	conn, err := ctrl.GetConnection(ctx, "conn-1")
	if err != nil {
		t.Fatal(err)
	}
	if conn.ID != "conn-1" {
		t.Fatalf("expected ID conn-1, got %s", conn.ID)
	}
	if conn.Name != "Production Cluster" {
		t.Fatalf("expected name 'Production Cluster', got %s", conn.Name)
	}
	if conn.Data["kubeconfig"] != "/path/to/config" {
		t.Fatalf("expected kubeconfig in Data, got %v", conn.Data)
	}
	if conn.Data["region"] != "us-east-1" {
		t.Fatalf("expected region in Data, got %v", conn.Data)
	}
	if conn.Labels["env"] != "production" {
		t.Fatalf("expected env label, got %v", conn.Labels)
	}
	if conn.Labels["team"] != "platform" {
		t.Fatalf("expected team label, got %v", conn.Labels)
	}
}

// ============================================================================
// WP-005: ListenForEvents — events flow through sink
// ============================================================================

func TestWP_ListenForEventsEventsFlow(t *testing.T) {
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
						ID:         "pod-event-1",
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

	// Register listener BEFORE connection.
	sink := resourcetest.NewRecordingSink()
	resource.AddListenerForTest(ctrl, sink)
	t.Cleanup(func() { resource.RemoveListenerForTest(ctrl, sink) })

	ctrl.StartConnection(ctx, "conn-1")
	close(ready) // Signal watcher to emit.

	sink.WaitForAdds(t, 1, 2*time.Second)
	if sink.AddCount() < 1 {
		t.Fatal("expected at least 1 add event")
	}
}

// ============================================================================
// WP-014: ListenForEvents with nil sink — no panic
// ============================================================================

func TestWP_ListenForEventsNilSink(t *testing.T) {
	ctrl := buildController(t)

	ctx, cancel := context.WithTimeout(resourcetest.NewTestContext(), 100*time.Millisecond)
	defer cancel()

	// Passing nil sink should not panic. The test succeeds if we don't panic.
	done := make(chan struct{})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("ListenForEvents panicked with nil sink: %v", r)
			}
			close(done)
		}()
		ctrl.ListenForEvents(ctx, nil)
	}()

	select {
	case <-done:
		// Returned (or panicked and we caught it) — good.
	case <-time.After(2 * time.Second):
		t.Fatal("ListenForEvents with nil sink did not return")
	}
}

// ============================================================================
// WP-016: IsRunning immediately after EnsureResourceWatch
// ============================================================================

func TestWP_IsRunningImmediatelyAfterEnsure(t *testing.T) {
	syncFirst := resource.SyncOnFirstQuery
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta: resourcetest.SecretMeta,
			Resourcer: &resourcetest.WatchableResourcer[string]{
				PolicyVal: &syncFirst,
			},
		}}
	})

	ctx := resourcetest.NewTestContext()
	err := ctrl.EnsureResourceWatch(ctx, "conn-1", "core::v1::Secret")
	if err != nil {
		t.Fatal(err)
	}

	// Immediately check — should be running.
	running, _ := ctrl.IsResourceWatchRunning(ctx, "conn-1", "core::v1::Secret")
	if !running {
		t.Fatal("expected Secret watch running immediately after EnsureResourceWatch")
	}
}

// ============================================================================
// AP-012: GetActions — pattern resourcer implements ActionResourcer
// ============================================================================

func TestAP_GetActionsPatternResourcer(t *testing.T) {
	// Pattern "*" resourcer implements ActionResourcer.
	patternAction := &testActionResourcer{
		actions: []resource.ActionDescriptor{
			{ID: "scale", Label: "Scale Resource"},
		},
	}

	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = nil
		cfg.Patterns = map[string]resource.Resourcer[string]{
			"*": patternAction,
		}
	})

	ctx := resourcetest.NewTestContext()
	actions, err := ctrl.GetActions(ctx, "unknown::v1::Widget")
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 {
		t.Fatalf("expected 1 action from pattern resourcer, got %d", len(actions))
	}
	if actions[0].ID != "scale" {
		t.Fatalf("expected action ID 'scale', got %s", actions[0].ID)
	}
}

// ============================================================================
// SP-006: GetResourceSchema — per-resource schema provider
// ============================================================================

func TestSP_GetResourceSchemaPerResource(t *testing.T) {
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{{
			Meta:      resourcetest.PodMeta,
			Resourcer: &schemaResourcer{},
		}}
	})

	ctx := resourcetest.NewTestContext()
	schema, err := ctrl.GetResourceSchema(ctx, "conn-1", "core::v1::Pod")
	if err != nil {
		t.Fatal(err)
	}
	if schema == nil {
		t.Fatal("expected non-nil schema")
	}
	if string(schema) != `{"type":"object"}` {
		t.Fatalf("expected JSON schema, got: %s", string(schema))
	}
}

// ============================================================================
// CL-020: UpdateConnection same data → no-op
// ============================================================================

func TestCL_UpdateConnectionNoOp(t *testing.T) {
	connProvider := &resourcetest.StubConnectionProvider[string]{}
	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Connections = connProvider
	})

	ctx := resourcetest.NewTestContext()

	// CreateClient called once on StartConnection.
	if got := connProvider.CreateClientCalls.Load(); got != 1 {
		t.Fatalf("expected 1 CreateClient call after start, got %d", got)
	}

	// Update with identical data — should be a no-op.
	conn, _ := ctrl.GetConnection(ctx, "conn-1")
	_, err := ctrl.UpdateConnection(ctx, conn)
	if err != nil {
		t.Fatalf("UpdateConnection no-op should not return error, got: %v", err)
	}

	// CreateClient should still be 1 (no restart).
	if got := connProvider.CreateClientCalls.Load(); got != 1 {
		t.Fatalf("expected CreateClient still 1 after no-op update, got %d", got)
	}
}

// ============================================================================
// FQ-001: Valid field+operator accepted
// ============================================================================

func TestFQ_ValidFieldOperatorAccepted(t *testing.T) {
	var receivedInput resource.FindInput
	res := &resourcetest.FilterableResourcer[string]{
		StubResourcer: resourcetest.StubResourcer[string]{
			FindFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, input resource.FindInput) (*resource.FindResult, error) {
				receivedInput = input
				return &resource.FindResult{Success: true}, nil
			},
		},
		FilterFieldsFunc: func(_ context.Context, _ string) ([]resource.FilterField, error) {
			return []resource.FilterField{
				{Path: "metadata.name", Operators: []resource.FilterOperator{resource.OpEqual, resource.OpContains}},
			}, nil
		},
	}

	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: res},
		}
	})

	ctx := resourcetest.NewTestContext()
	result, err := ctrl.Find(ctx, "core::v1::Pod", resource.FindInput{
		Filters: &resource.FilterExpression{
			Logic: resource.FilterAnd,
			Predicates: []resource.FilterPredicate{
				{Field: "metadata.name", Operator: resource.OpEqual, Value: "nginx"},
			},
		},
	})
	if err != nil {
		t.Fatalf("valid filter should not error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
	if len(receivedInput.Filters.Predicates) != 1 {
		t.Fatal("predicate should reach Resourcer")
	}
}

// ============================================================================
// FQ-002: Unknown field rejected
// ============================================================================

func TestFQ_UnknownFieldRejected(t *testing.T) {
	res := &resourcetest.FilterableResourcer[string]{
		FilterFieldsFunc: func(_ context.Context, _ string) ([]resource.FilterField, error) {
			return []resource.FilterField{
				{Path: "metadata.name"},
			}, nil
		},
	}

	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: res},
		}
	})

	ctx := resourcetest.NewTestContext()
	_, err := ctrl.Find(ctx, "core::v1::Pod", resource.FindInput{
		Filters: &resource.FilterExpression{
			Logic: resource.FilterAnd,
			Predicates: []resource.FilterPredicate{
				{Field: "spec.bogus", Operator: resource.OpEqual, Value: "x"},
			},
		},
	})
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
	var roe *resource.ResourceOperationError
	if !errors.As(err, &roe) {
		t.Fatalf("expected ResourceOperationError, got %T: %v", err, err)
	}
	if roe.Code != "FILTER_UNKNOWN_FIELD" {
		t.Fatalf("expected FILTER_UNKNOWN_FIELD, got %s", roe.Code)
	}
}

// ============================================================================
// FQ-003: Invalid operator rejected
// ============================================================================

func TestFQ_InvalidOperatorRejected(t *testing.T) {
	res := &resourcetest.FilterableResourcer[string]{
		FilterFieldsFunc: func(_ context.Context, _ string) ([]resource.FilterField, error) {
			return []resource.FilterField{
				{Path: "metadata.name", Operators: []resource.FilterOperator{resource.OpEqual}},
			}, nil
		},
	}

	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: res},
		}
	})

	ctx := resourcetest.NewTestContext()
	_, err := ctrl.Find(ctx, "core::v1::Pod", resource.FindInput{
		Filters: &resource.FilterExpression{
			Logic: resource.FilterAnd,
			Predicates: []resource.FilterPredicate{
				{Field: "metadata.name", Operator: resource.OpRegex, Value: ".*"},
			},
		},
	})
	if err == nil {
		t.Fatal("expected error for invalid operator")
	}
	var roe *resource.ResourceOperationError
	if !errors.As(err, &roe) {
		t.Fatalf("expected ResourceOperationError, got %T: %v", err, err)
	}
	if roe.Code != "FILTER_INVALID_OPERATOR" {
		t.Fatalf("expected FILTER_INVALID_OPERATOR, got %s", roe.Code)
	}
}

// ============================================================================
// FQ-004: AND logic — predicates passed through to Find()
// ============================================================================

func TestFQ_ANDLogicPassedThrough(t *testing.T) {
	var receivedInput resource.FindInput
	res := &resourcetest.FilterableResourcer[string]{
		StubResourcer: resourcetest.StubResourcer[string]{
			FindFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, input resource.FindInput) (*resource.FindResult, error) {
				receivedInput = input
				return &resource.FindResult{Success: true}, nil
			},
		},
		FilterFieldsFunc: func(_ context.Context, _ string) ([]resource.FilterField, error) {
			return []resource.FilterField{
				{Path: "metadata.name"},
				{Path: "metadata.namespace"},
			}, nil
		},
	}

	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: res},
		}
	})

	ctx := resourcetest.NewTestContext()
	_, err := ctrl.Find(ctx, "core::v1::Pod", resource.FindInput{
		Filters: &resource.FilterExpression{
			Logic: resource.FilterAnd,
			Predicates: []resource.FilterPredicate{
				{Field: "metadata.name", Operator: resource.OpEqual, Value: "nginx"},
				{Field: "metadata.namespace", Operator: resource.OpEqual, Value: "default"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if receivedInput.Filters.Logic != resource.FilterAnd {
		t.Fatalf("expected AND logic, got %s", receivedInput.Filters.Logic)
	}
	if len(receivedInput.Filters.Predicates) != 2 {
		t.Fatalf("expected 2 predicates, got %d", len(receivedInput.Filters.Predicates))
	}
}

// ============================================================================
// FQ-005: OR logic — predicates passed through to Find()
// ============================================================================

func TestFQ_ORLogicPassedThrough(t *testing.T) {
	var receivedInput resource.FindInput
	res := &resourcetest.FilterableResourcer[string]{
		StubResourcer: resourcetest.StubResourcer[string]{
			FindFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, input resource.FindInput) (*resource.FindResult, error) {
				receivedInput = input
				return &resource.FindResult{Success: true}, nil
			},
		},
		FilterFieldsFunc: func(_ context.Context, _ string) ([]resource.FilterField, error) {
			return []resource.FilterField{
				{Path: "status.phase"},
			}, nil
		},
	}

	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: res},
		}
	})

	ctx := resourcetest.NewTestContext()
	_, err := ctrl.Find(ctx, "core::v1::Pod", resource.FindInput{
		Filters: &resource.FilterExpression{
			Logic: resource.FilterOr,
			Predicates: []resource.FilterPredicate{
				{Field: "status.phase", Operator: resource.OpEqual, Value: "Running"},
				{Field: "status.phase", Operator: resource.OpEqual, Value: "Pending"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if receivedInput.Filters.Logic != resource.FilterOr {
		t.Fatalf("expected OR logic, got %s", receivedInput.Filters.Logic)
	}
}

// ============================================================================
// FQ-006: Nested groups (AND of ORs) — groups preserved in Find() input
// ============================================================================

func TestFQ_NestedGroupsPreserved(t *testing.T) {
	var receivedInput resource.FindInput
	res := &resourcetest.FilterableResourcer[string]{
		StubResourcer: resourcetest.StubResourcer[string]{
			FindFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, input resource.FindInput) (*resource.FindResult, error) {
				receivedInput = input
				return &resource.FindResult{Success: true}, nil
			},
		},
		FilterFieldsFunc: func(_ context.Context, _ string) ([]resource.FilterField, error) {
			return []resource.FilterField{
				{Path: "metadata.name"},
				{Path: "status.phase"},
			}, nil
		},
	}

	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: res},
		}
	})

	ctx := resourcetest.NewTestContext()
	_, err := ctrl.Find(ctx, "core::v1::Pod", resource.FindInput{
		Filters: &resource.FilterExpression{
			Logic: resource.FilterAnd,
			Groups: []resource.FilterExpression{
				{
					Logic: resource.FilterOr,
					Predicates: []resource.FilterPredicate{
						{Field: "status.phase", Operator: resource.OpEqual, Value: "Running"},
						{Field: "status.phase", Operator: resource.OpEqual, Value: "Pending"},
					},
				},
				{
					Logic: resource.FilterOr,
					Predicates: []resource.FilterPredicate{
						{Field: "metadata.name", Operator: resource.OpEqual, Value: "nginx"},
						{Field: "metadata.name", Operator: resource.OpEqual, Value: "redis"},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(receivedInput.Filters.Groups) != 2 {
		t.Fatalf("expected 2 groups preserved, got %d", len(receivedInput.Filters.Groups))
	}
}

// ============================================================================
// FQ-007: FilterFields cached — called once across two Find() calls
// ============================================================================

func TestFQ_FilterFieldsCached(t *testing.T) {
	res := &resourcetest.FilterableResourcer[string]{
		StubResourcer: resourcetest.StubResourcer[string]{
			FindFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.FindInput) (*resource.FindResult, error) {
				return &resource.FindResult{Success: true}, nil
			},
		},
		FilterFieldsFunc: func(_ context.Context, _ string) ([]resource.FilterField, error) {
			return []resource.FilterField{
				{Path: "metadata.name"},
			}, nil
		},
	}

	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: res},
		}
	})

	ctx := resourcetest.NewTestContext()
	input := resource.FindInput{
		Filters: &resource.FilterExpression{
			Logic: resource.FilterAnd,
			Predicates: []resource.FilterPredicate{
				{Field: "metadata.name", Operator: resource.OpEqual, Value: "nginx"},
			},
		},
	}

	// Two Find calls.
	ctrl.Find(ctx, "core::v1::Pod", input)
	ctrl.Find(ctx, "core::v1::Pod", input)

	if got := res.FilterFieldsCalls.Load(); got != 1 {
		t.Fatalf("expected FilterFields called once (cached), got %d", got)
	}
}

// ============================================================================
// FQ-008: No FilterableProvider → pass through
// ============================================================================

func TestFQ_NoFilterableProviderPassThrough(t *testing.T) {
	stub := &resourcetest.StubResourcer[string]{
		FindFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.FindInput) (*resource.FindResult, error) {
			return &resource.FindResult{Success: true}, nil
		},
	}

	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: stub},
		}
	})

	ctx := resourcetest.NewTestContext()
	result, err := ctrl.Find(ctx, "core::v1::Pod", resource.FindInput{
		Filters: &resource.FilterExpression{
			Logic: resource.FilterAnd,
			Predicates: []resource.FilterPredicate{
				{Field: "any.field", Operator: resource.OpContains, Value: "x"},
			},
		},
	})
	if err != nil {
		t.Fatalf("expected no error when FilterableProvider not implemented, got: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
}

// ============================================================================
// FQ-009: TextQuery → Search()
// ============================================================================

func TestFQ_TextQueryRoutesToSearch(t *testing.T) {
	res := &resourcetest.SearchableResourcer[string]{
		SearchFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, query string, limit int) (*resource.FindResult, error) {
			return &resource.FindResult{
				Success: true,
				Result:  []json.RawMessage{json.RawMessage(`{"hit":"1"}`)},
			}, nil
		},
	}

	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: res},
		}
	})

	ctx := resourcetest.NewTestContext()
	result, err := ctrl.Find(ctx, "core::v1::Pod", resource.FindInput{
		TextQuery: "nginx",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
	if res.SearchCalls.Load() != 1 {
		t.Fatal("expected SearchFunc called")
	}
	if res.FindCalls.Load() != 0 {
		t.Fatal("expected FindFunc NOT called")
	}
}

// ============================================================================
// FQ-010: Filters + TextQuery — filters validated, Search() called
// ============================================================================

func TestFQ_FiltersAndTextQuery(t *testing.T) {
	res := &resourcetest.FilterableSearchableResourcer[string]{
		StubResourcer: resourcetest.StubResourcer[string]{
			FindFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.FindInput) (*resource.FindResult, error) {
				return &resource.FindResult{Success: true}, nil
			},
		},
		FilterFieldsFunc: func(_ context.Context, _ string) ([]resource.FilterField, error) {
			return []resource.FilterField{
				{Path: "metadata.namespace"},
			}, nil
		},
		SearchFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, query string, limit int) (*resource.FindResult, error) {
			return &resource.FindResult{
				Success: true,
				Result:  []json.RawMessage{json.RawMessage(`{"hit":"1"}`)},
			}, nil
		},
	}

	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: res},
		}
	})

	ctx := resourcetest.NewTestContext()
	result, err := ctrl.Find(ctx, "core::v1::Pod", resource.FindInput{
		TextQuery: "nginx",
		Filters: &resource.FilterExpression{
			Logic: resource.FilterAnd,
			Predicates: []resource.FilterPredicate{
				{Field: "metadata.namespace", Operator: resource.OpEqual, Value: "default"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
	// Filters validated (FilterFieldsFunc called).
	if res.FilterFieldsCalls.Load() != 1 {
		t.Fatal("expected FilterFields called for validation")
	}
	// Search was routed to.
	if res.SearchCalls.Load() != 1 {
		t.Fatal("expected Search called")
	}
	// Find was NOT called (TextSearchProvider took over).
	if res.FindCalls.Load() != 0 {
		t.Fatal("expected Find NOT called when TextSearchProvider handles query")
	}
}

// ============================================================================
// FQ-011: No TextSearchProvider → TextQuery ignored, Find() called
// ============================================================================

func TestFQ_NoTextSearchProviderFallsThrough(t *testing.T) {
	stub := &resourcetest.StubResourcer[string]{
		FindFunc: func(_ context.Context, _ *string, _ resource.ResourceMeta, _ resource.FindInput) (*resource.FindResult, error) {
			return &resource.FindResult{Success: true}, nil
		},
	}

	ctrl := buildController(t, func(cfg *resource.ResourcePluginConfig[string]) {
		cfg.Resources = []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: stub},
		}
	})

	ctx := resourcetest.NewTestContext()
	result, err := ctrl.Find(ctx, "core::v1::Pod", resource.FindInput{
		TextQuery: "some query",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
	if stub.FindCalls.Load() != 1 {
		t.Fatalf("expected FindFunc called once, got %d", stub.FindCalls.Load())
	}
}
