package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	resourcepb "github.com/omniviewdev/plugin-sdk/proto/v1/resource"

	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/resource/resourcetest"
)

const bufSize = 1024 * 1024

// setupBufconn creates an in-process gRPC server + client pair using bufconn.
// Returns a resource.Provider backed by the gRPC client.
func setupBufconn(t *testing.T, provider resource.Provider) resource.Provider {
	t.Helper()
	lis := bufconn.Listen(bufSize)

	s := grpc.NewServer()
	resourcepb.RegisterResourcePluginServer(s, NewServer(provider))

	go func() {
		if err := s.Serve(lis); err != nil {
			// Server stopped, expected during cleanup.
		}
	}()

	dialer := func(ctx context.Context, addr string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}
	//nolint:staticcheck // grpc.DialContext is needed for grpc v1.61.
	conn, err := grpc.DialContext(context.Background(), "bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("bufconn dial: %v", err)
	}

	t.Cleanup(func() {
		conn.Close()
		s.Stop()
		lis.Close()
	})

	return NewClient(resourcepb.NewResourcePluginClient(conn))
}

// ============================================================================
// GR-001..006: CRUD round-trips
// ============================================================================

func TestGR001_GetRoundTrip(t *testing.T) {
	data := json.RawMessage(`{"name":"test-pod","status":"Running"}`)
	tp := resourcetest.NewTestProvider(t,
		resourcetest.WithGetFunc(func(_ context.Context, key string, input resource.GetInput) (*resource.GetResult, error) {
			return &resource.GetResult{Success: true, Result: data}, nil
		}),
	)
	c := setupBufconn(t, tp)
	ctx := resource.WithSession(context.Background(), &resource.Session{
		Connection: &types.Connection{ID: "conn-1"},
	})
	result, err := c.Get(ctx, "core::v1::Pod", resource.GetInput{ID: "pod-1", Namespace: "default"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !result.Success {
		t.Fatal("expected Success=true")
	}
	if string(result.Result) != string(data) {
		t.Fatalf("data mismatch: got %s, want %s", result.Result, data)
	}
}

func TestGR002_ListRoundTrip(t *testing.T) {
	items := make([]json.RawMessage, 5)
	for i := range items {
		items[i] = json.RawMessage(fmt.Sprintf(`{"id":"item-%d"}`, i))
	}
	tp := resourcetest.NewTestProvider(t,
		resourcetest.WithListFunc(func(_ context.Context, key string, input resource.ListInput) (*resource.ListResult, error) {
			return &resource.ListResult{Success: true, Result: items, TotalCount: 5}, nil
		}),
	)
	c := setupBufconn(t, tp)
	ctx := resource.WithSession(context.Background(), &resource.Session{
		Connection: &types.Connection{ID: "conn-1"},
	})
	result, err := c.List(ctx, "core::v1::Pod", resource.ListInput{Namespaces: []string{"default"}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(result.Result) != 5 {
		t.Fatalf("expected 5 items, got %d", len(result.Result))
	}
	if result.TotalCount != 5 {
		t.Fatalf("expected TotalCount=5, got %d", result.TotalCount)
	}
}

func TestGR003_FindRoundTrip(t *testing.T) {
	items := []json.RawMessage{json.RawMessage(`{"id":"found-1"}`)}
	tp := resourcetest.NewTestProvider(t)
	tp.FindFunc = func(_ context.Context, key string, input resource.FindInput) (*resource.FindResult, error) {
		if input.TextQuery != "nginx" {
			t.Errorf("expected TextQuery=nginx, got %q", input.TextQuery)
		}
		if input.Filters == nil {
			t.Error("expected non-nil Filters")
		}
		return &resource.FindResult{Success: true, Result: items, TotalCount: 1}, nil
	}
	c := setupBufconn(t, tp)
	ctx := resource.WithSession(context.Background(), &resource.Session{
		Connection: &types.Connection{ID: "conn-1"},
	})
	result, err := c.Find(ctx, "core::v1::Pod", resource.FindInput{
		TextQuery: "nginx",
		Filters: &resource.FilterExpression{
			Logic: resource.FilterAnd,
			Predicates: []resource.FilterPredicate{
				{Field: "status.phase", Operator: resource.OpEqual, Value: "Running"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(result.Result) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Result))
	}
}

func TestGR004_CreateRoundTrip(t *testing.T) {
	input := json.RawMessage(`{"apiVersion":"v1","kind":"Pod"}`)
	tp := resourcetest.NewTestProvider(t)
	tp.CreateFunc = func(_ context.Context, key string, in resource.CreateInput) (*resource.CreateResult, error) {
		return &resource.CreateResult{Success: true, Result: in.Input}, nil
	}
	c := setupBufconn(t, tp)
	ctx := resource.WithSession(context.Background(), &resource.Session{
		Connection: &types.Connection{ID: "conn-1"},
	})
	result, err := c.Create(ctx, "core::v1::Pod", resource.CreateInput{Input: input, Namespace: "default"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if string(result.Result) != string(input) {
		t.Fatalf("data mismatch: got %s, want %s", result.Result, input)
	}
}

func TestGR005_UpdateRoundTrip(t *testing.T) {
	data := json.RawMessage(`{"metadata":{"name":"test"}}`)
	tp := resourcetest.NewTestProvider(t)
	tp.UpdateFunc = func(_ context.Context, key string, in resource.UpdateInput) (*resource.UpdateResult, error) {
		if in.ID != "pod-1" {
			t.Errorf("expected ID=pod-1, got %q", in.ID)
		}
		return &resource.UpdateResult{Success: true, Result: in.Input}, nil
	}
	c := setupBufconn(t, tp)
	ctx := resource.WithSession(context.Background(), &resource.Session{
		Connection: &types.Connection{ID: "conn-1"},
	})
	result, err := c.Update(ctx, "core::v1::Pod", resource.UpdateInput{Input: data, ID: "pod-1", Namespace: "default"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if string(result.Result) != string(data) {
		t.Fatalf("data mismatch: got %s, want %s", result.Result, data)
	}
}

func TestGR006_DeleteRoundTrip(t *testing.T) {
	tp := resourcetest.NewTestProvider(t)
	tp.DeleteFunc = func(_ context.Context, key string, in resource.DeleteInput) (*resource.DeleteResult, error) {
		if in.ID != "pod-1" {
			t.Errorf("expected ID=pod-1, got %q", in.ID)
		}
		return &resource.DeleteResult{Success: true}, nil
	}
	c := setupBufconn(t, tp)
	ctx := resource.WithSession(context.Background(), &resource.Session{
		Connection: &types.Connection{ID: "conn-1"},
	})
	result, err := c.Delete(ctx, "core::v1::Pod", resource.DeleteInput{ID: "pod-1", Namespace: "default"})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !result.Success {
		t.Fatal("expected Success=true")
	}
}

// ============================================================================
// GR-007..010: Connection + streaming
// ============================================================================

func TestGR007_StartConnectionRoundTrip(t *testing.T) {
	tp := resourcetest.NewTestProvider(t)
	tp.StartConnectionFunc = func(_ context.Context, id string) (types.ConnectionStatus, error) {
		return types.ConnectionStatus{
			Status:  types.ConnectionStatusConnected,
			Details: "connected",
		}, nil
	}
	c := setupBufconn(t, tp)
	status, err := c.StartConnection(context.Background(), "conn-1")
	if err != nil {
		t.Fatalf("StartConnection: %v", err)
	}
	if status.Status != types.ConnectionStatusConnected {
		t.Fatalf("expected Connected, got %v", status.Status)
	}
}

func TestGR008_ListenForEventsStreaming(t *testing.T) {
	tp := resourcetest.NewTestProvider(t,
		resourcetest.WithListenForEventsFunc(func(ctx context.Context, sink resource.WatchEventSink) error {
			sink.OnAdd(resource.WatchAddPayload{
				Connection: "conn-1", Key: "core::v1::Pod",
				ID: "pod-1", Namespace: "default",
				Data: json.RawMessage(`{"kind":"Pod"}`),
			})
			sink.OnUpdate(resource.WatchUpdatePayload{
				Connection: "conn-1", Key: "core::v1::Pod",
				ID: "pod-1", Namespace: "default",
				Data: json.RawMessage(`{"kind":"Pod","status":"Running"}`),
			})
			sink.OnDelete(resource.WatchDeletePayload{
				Connection: "conn-1", Key: "core::v1::Pod",
				ID: "pod-1", Namespace: "default",
				Data: json.RawMessage(`{"kind":"Pod"}`),
			})
			return nil
		}),
	)
	c := setupBufconn(t, tp)

	var adds, updates, deletes int
	sink := &countingSink{
		onAdd:    func(_ resource.WatchAddPayload) { adds++ },
		onUpdate: func(_ resource.WatchUpdatePayload) { updates++ },
		onDelete: func(_ resource.WatchDeletePayload) { deletes++ },
	}

	err := c.ListenForEvents(context.Background(), sink)
	if err != nil {
		t.Fatalf("ListenForEvents: %v", err)
	}
	if adds != 1 || updates != 1 || deletes != 1 {
		t.Fatalf("expected 1/1/1 events, got %d/%d/%d", adds, updates, deletes)
	}
}

func TestGR009_StreamActionStreaming(t *testing.T) {
	tp := resourcetest.NewTestProvider(t)
	tp.StreamActionFunc = func(ctx context.Context, key string, actionID string, input resource.ActionInput, stream chan<- resource.ActionEvent) error {
		for i := 0; i < 5; i++ {
			stream <- resource.ActionEvent{Type: "progress", Data: map[string]interface{}{"step": float64(i)}}
		}
		close(stream)
		return nil
	}
	c := setupBufconn(t, tp)
	ctx := resource.WithSession(context.Background(), &resource.Session{
		Connection: &types.Connection{ID: "conn-1"},
	})
	ch := make(chan resource.ActionEvent, 16)
	err := c.StreamAction(ctx, "core::v1::Pod", "restart", resource.ActionInput{ID: "pod-1"}, ch)
	if err != nil {
		t.Fatalf("StreamAction: %v", err)
	}
	var count int
	for range ch {
		count++
	}
	if count != 5 {
		t.Fatalf("expected 5 events, got %d", count)
	}
}

func TestGR010_WatchConnectionsStreaming(t *testing.T) {
	tp := resourcetest.NewTestProvider(t)
	tp.WatchConnectionsFunc = func(ctx context.Context, stream chan<- []types.Connection) error {
		stream <- []types.Connection{{ID: "conn-1", Name: "First"}}
		stream <- []types.Connection{{ID: "conn-1", Name: "First"}, {ID: "conn-2", Name: "Second"}}
		close(stream)
		return nil
	}
	c := setupBufconn(t, tp)
	ch := make(chan []types.Connection, 16)
	err := c.WatchConnections(context.Background(), ch)
	if err != nil {
		t.Fatalf("WatchConnections: %v", err)
	}
	var batches [][]types.Connection
	for conns := range ch {
		batches = append(batches, conns)
	}
	if len(batches) != 2 {
		t.Fatalf("expected 2 batches, got %d", len(batches))
	}
	if len(batches[0]) != 1 {
		t.Fatalf("batch 0: expected 1 conn, got %d", len(batches[0]))
	}
	if len(batches[1]) != 2 {
		t.Fatalf("batch 1: expected 2 conns, got %d", len(batches[1]))
	}
}

// ============================================================================
// GR-011..015: Transport behavior
// ============================================================================

func TestGR011_ContextCancellation(t *testing.T) {
	started := make(chan struct{})
	tp := resourcetest.NewTestProvider(t,
		resourcetest.WithListenForEventsFunc(func(ctx context.Context, sink resource.WatchEventSink) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}),
	)
	c := setupBufconn(t, tp)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- c.ListenForEvents(ctx, &countingSink{})
	}()
	<-started
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error from cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for cancellation")
	}
}

func TestGR012_DeadlinePropagation(t *testing.T) {
	tp := resourcetest.NewTestProvider(t)
	tp.GetFunc = func(ctx context.Context, key string, input resource.GetInput) (*resource.GetResult, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Error("expected deadline to be set")
		}
		if time.Until(deadline) > 10*time.Second {
			t.Error("deadline too far in the future")
		}
		return &resource.GetResult{Success: true, Result: json.RawMessage(`{}`)}, nil
	}
	c := setupBufconn(t, tp)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = resource.WithSession(ctx, &resource.Session{
		Connection: &types.Connection{ID: "conn-1"},
	})
	_, err := c.Get(ctx, "core::v1::Pod", resource.GetInput{ID: "x"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
}

func TestGR013_LargePayload(t *testing.T) {
	items := make([]json.RawMessage, 1000)
	for i := range items {
		items[i] = json.RawMessage(fmt.Sprintf(`{"id":"item-%d","data":"payload-%d"}`, i, i))
	}
	tp := resourcetest.NewTestProvider(t,
		resourcetest.WithListFunc(func(_ context.Context, _ string, _ resource.ListInput) (*resource.ListResult, error) {
			return &resource.ListResult{Success: true, Result: items, TotalCount: 1000}, nil
		}),
	)
	c := setupBufconn(t, tp)
	ctx := resource.WithSession(context.Background(), &resource.Session{
		Connection: &types.Connection{ID: "conn-1"},
	})
	result, err := c.List(ctx, "core::v1::Pod", resource.ListInput{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(result.Result) != 1000 {
		t.Fatalf("expected 1000 items, got %d", len(result.Result))
	}
}

func TestGR014_ErrorPropagation(t *testing.T) {
	tp := resourcetest.NewTestProvider(t)
	tp.GetFunc = func(_ context.Context, _ string, _ resource.GetInput) (*resource.GetResult, error) {
		return nil, &resource.ResourceOperationError{
			Code:        "NOT_FOUND",
			Title:       "Not Found",
			Message:     "Pod not found",
			Suggestions: []string{"check the name", "check the namespace"},
		}
	}
	c := setupBufconn(t, tp)
	ctx := resource.WithSession(context.Background(), &resource.Session{
		Connection: &types.Connection{ID: "conn-1"},
	})
	_, err := c.Get(ctx, "core::v1::Pod", resource.GetInput{ID: "missing"})
	if err == nil {
		t.Fatal("expected error")
	}
	var roe *resource.ResourceOperationError
	if !errors.As(err, &roe) {
		t.Fatalf("expected ResourceOperationError, got %T: %v", err, err)
	}
	if roe.Code != "NOT_FOUND" {
		t.Fatalf("expected code NOT_FOUND, got %q", roe.Code)
	}
	if roe.Title != "Not Found" {
		t.Fatalf("expected title 'Not Found', got %q", roe.Title)
	}
	if len(roe.Suggestions) != 2 {
		t.Fatalf("expected 2 suggestions, got %d", len(roe.Suggestions))
	}
}

func TestGR015_EmptyResponse(t *testing.T) {
	tp := resourcetest.NewTestProvider(t,
		resourcetest.WithListFunc(func(_ context.Context, _ string, _ resource.ListInput) (*resource.ListResult, error) {
			return &resource.ListResult{Success: true, Result: nil, TotalCount: 0}, nil
		}),
	)
	c := setupBufconn(t, tp)
	ctx := resource.WithSession(context.Background(), &resource.Session{
		Connection: &types.Connection{ID: "conn-1"},
	})
	result, err := c.List(ctx, "core::v1::Pod", resource.ListInput{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(result.Result) != 0 {
		t.Fatalf("expected 0 items, got %d", len(result.Result))
	}
}

// ============================================================================
// GR-016..018: Type info + watch
// ============================================================================

func TestGR016_WatchStateRoundTrip(t *testing.T) {
	tp := resourcetest.NewTestProvider(t)
	tp.GetWatchStateFunc = func(_ context.Context, connID string) (*resource.WatchConnectionSummary, error) {
		return &resource.WatchConnectionSummary{
			ConnectionID: connID,
			Resources: map[string]resource.WatchState{
				"core::v1::Pod":     resource.WatchStateSynced,
				"apps::v1::Deploy":  resource.WatchStateSyncing,
				"core::v1::Service": resource.WatchStateError,
			},
		}, nil
	}
	c := setupBufconn(t, tp)
	summary, err := c.GetWatchState(context.Background(), "conn-1")
	if err != nil {
		t.Fatalf("GetWatchState: %v", err)
	}
	if summary.ConnectionID != "conn-1" {
		t.Fatalf("expected conn-1, got %q", summary.ConnectionID)
	}
	if len(summary.Resources) != 3 {
		t.Fatalf("expected 3 resources, got %d", len(summary.Resources))
	}
	if summary.Resources["core::v1::Pod"] != resource.WatchStateSynced {
		t.Fatalf("Pod: expected Synced, got %v", summary.Resources["core::v1::Pod"])
	}
	if summary.Resources["apps::v1::Deploy"] != resource.WatchStateSyncing {
		t.Fatalf("Deploy: expected Syncing, got %v", summary.Resources["apps::v1::Deploy"])
	}
	if summary.Resources["core::v1::Service"] != resource.WatchStateError {
		t.Fatalf("Service: expected Error, got %v", summary.Resources["core::v1::Service"])
	}
}

func TestGR017_GetResourceTypesRoundTrip(t *testing.T) {
	tp := resourcetest.NewTestProvider(t)
	tp.GetResourceTypesFunc = func(_ context.Context, connID string) map[string]resource.ResourceMeta {
		return map[string]resource.ResourceMeta{
			"core::v1::Pod": {Group: "core", Version: "v1", Kind: "Pod", Label: "Pods"},
			"core::v1::Svc": {Group: "core", Version: "v1", Kind: "Service", Label: "Services"},
		}
	}
	c := setupBufconn(t, tp)
	types := c.GetResourceTypes(context.Background(), "conn-1")
	if len(types) != 2 {
		t.Fatalf("expected 2 types, got %d", len(types))
	}
	pod := types["core::v1::Pod"]
	if pod.Kind != "Pod" {
		t.Fatalf("expected Kind=Pod, got %q", pod.Kind)
	}
	if pod.Label != "Pods" {
		t.Fatalf("expected Label=Pods, got %q", pod.Label)
	}
}

func TestGR018_WatchLifecycleRPCs(t *testing.T) {
	var ensured, stopped bool
	tp := resourcetest.NewTestProvider(t)
	tp.EnsureResourceWatchFunc = func(_ context.Context, connID, key string) error {
		ensured = true
		return nil
	}
	tp.StopResourceWatchFunc = func(_ context.Context, connID, key string) error {
		stopped = true
		return nil
	}
	c := setupBufconn(t, tp)
	if err := c.EnsureResourceWatch(context.Background(), "conn-1", "core::v1::Pod"); err != nil {
		t.Fatalf("EnsureResourceWatch: %v", err)
	}
	if !ensured {
		t.Fatal("EnsureResourceWatch not called")
	}
	if err := c.StopResourceWatch(context.Background(), "conn-1", "core::v1::Pod"); err != nil {
		t.Fatalf("StopResourceWatch: %v", err)
	}
	if !stopped {
		t.Fatal("StopResourceWatch not called")
	}
}

// ============================================================================
// GR-019..028: Edge cases
// ============================================================================

func TestGR019_NilEmptyNamespace(t *testing.T) {
	tp := resourcetest.NewTestProvider(t)
	tp.GetFunc = func(_ context.Context, key string, input resource.GetInput) (*resource.GetResult, error) {
		return &resource.GetResult{Success: true, Result: json.RawMessage(`{}`)}, nil
	}
	c := setupBufconn(t, tp)
	ctx := resource.WithSession(context.Background(), &resource.Session{
		Connection: &types.Connection{ID: "conn-1"},
	})
	result, err := c.Get(ctx, "core::v1::Node", resource.GetInput{ID: "node-1", Namespace: ""})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !result.Success {
		t.Fatal("expected Success=true")
	}
}

func TestGR020_UnicodeInData(t *testing.T) {
	data := json.RawMessage(`{"name":"テスト-pod","emoji":"🚀","desc":"日本語テスト"}`)
	tp := resourcetest.NewTestProvider(t)
	tp.GetFunc = func(_ context.Context, _ string, _ resource.GetInput) (*resource.GetResult, error) {
		return &resource.GetResult{Success: true, Result: data}, nil
	}
	c := setupBufconn(t, tp)
	ctx := resource.WithSession(context.Background(), &resource.Session{
		Connection: &types.Connection{ID: "conn-1"},
	})
	result, err := c.Get(ctx, "core::v1::Pod", resource.GetInput{ID: "pod-1"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(result.Result) != string(data) {
		t.Fatalf("unicode data mismatch: got %s", result.Result)
	}
}

func TestGR021_BinaryDataInPayload(t *testing.T) {
	// Binary data embedded in a JSON string via base64.
	data := json.RawMessage(`{"binary":"AQIDBA=="}`)
	tp := resourcetest.NewTestProvider(t)
	tp.GetFunc = func(_ context.Context, _ string, _ resource.GetInput) (*resource.GetResult, error) {
		return &resource.GetResult{Success: true, Result: data}, nil
	}
	c := setupBufconn(t, tp)
	ctx := resource.WithSession(context.Background(), &resource.Session{
		Connection: &types.Connection{ID: "conn-1"},
	})
	result, err := c.Get(ctx, "core::v1::Secret", resource.GetInput{ID: "secret-1"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(result.Result) != string(data) {
		t.Fatalf("binary data mismatch: got %s", result.Result)
	}
}

func TestGR022_ConcurrentRPCs(t *testing.T) {
	var counter int64
	tp := resourcetest.NewTestProvider(t)
	tp.GetFunc = func(_ context.Context, _ string, input resource.GetInput) (*resource.GetResult, error) {
		atomic.AddInt64(&counter, 1)
		return &resource.GetResult{
			Success: true,
			Result:  json.RawMessage(fmt.Sprintf(`{"id":"%s"}`, input.ID)),
		}, nil
	}
	c := setupBufconn(t, tp)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			ctx := resource.WithSession(context.Background(), &resource.Session{
				Connection: &types.Connection{ID: "conn-1"},
			})
			result, err := c.Get(ctx, "core::v1::Pod", resource.GetInput{ID: fmt.Sprintf("pod-%d", n)})
			if err != nil {
				t.Errorf("concurrent Get %d: %v", n, err)
				return
			}
			expected := fmt.Sprintf(`{"id":"pod-%d"}`, n)
			if string(result.Result) != expected {
				t.Errorf("concurrent Get %d: got %s, want %s", n, result.Result, expected)
			}
		}(i)
	}
	wg.Wait()
	if atomic.LoadInt64(&counter) != 20 {
		t.Fatalf("expected 20 calls, got %d", counter)
	}
}

func TestGR023_ServerShutdownDuringStream(t *testing.T) {
	lis := bufconn.Listen(bufSize)
	s := grpc.NewServer()

	tp := resourcetest.NewTestProvider(t,
		resourcetest.WithListenForEventsFunc(func(ctx context.Context, sink resource.WatchEventSink) error {
			// Send one event then block until server stops.
			sink.OnAdd(resource.WatchAddPayload{Connection: "c1", Key: "k", ID: "1", Data: json.RawMessage(`{}`)})
			<-ctx.Done()
			return ctx.Err()
		}),
	)
	resourcepb.RegisterResourcePluginServer(s, NewServer(tp))
	go func() { _ = s.Serve(lis) }()

	dialer := func(ctx context.Context, addr string) (net.Conn, error) { return lis.DialContext(ctx) }
	//nolint:staticcheck // grpc.DialContext is needed for grpc v1.61.
	conn, _ := grpc.DialContext(context.Background(), "bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	c := NewClient(resourcepb.NewResourcePluginClient(conn))

	errCh := make(chan error, 1)
	go func() {
		errCh <- c.ListenForEvents(context.Background(), &countingSink{
			onAdd: func(_ resource.WatchAddPayload) {},
		})
	}()

	time.Sleep(100 * time.Millisecond)
	s.Stop()

	select {
	case err := <-errCh:
		// Client should get an error (EOF or transport closing), not panic.
		_ = err
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for stream to end")
	}
	conn.Close()
	lis.Close()
}

func TestGR024_ClientDisconnectDuringStream(t *testing.T) {
	serverReturned := make(chan struct{})
	tp := resourcetest.NewTestProvider(t,
		resourcetest.WithListenForEventsFunc(func(ctx context.Context, sink resource.WatchEventSink) error {
			defer close(serverReturned)
			<-ctx.Done()
			return ctx.Err()
		}),
	)
	c := setupBufconn(t, tp)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = c.ListenForEvents(ctx, &countingSink{})
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-serverReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not return after client disconnect")
	}
}

func TestGR025_EmptyConnectionID(t *testing.T) {
	tp := resourcetest.NewTestProvider(t)
	tp.GetFunc = func(_ context.Context, _ string, _ resource.GetInput) (*resource.GetResult, error) {
		return &resource.GetResult{Success: true, Result: json.RawMessage(`{}`)}, nil
	}
	c := setupBufconn(t, tp)
	// No session = empty connection ID.
	result, err := c.Get(context.Background(), "core::v1::Pod", resource.GetInput{ID: "pod-1"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !result.Success {
		t.Fatal("expected Success=true")
	}
}

func TestGR026_LargeMetadataMaps(t *testing.T) {
	labels := make(map[string]any, 1000)
	for i := 0; i < 1000; i++ {
		labels[fmt.Sprintf("key-%d", i)] = fmt.Sprintf("value-%d", i)
	}
	tp := resourcetest.NewTestProvider(t,
		resourcetest.WithLoadConnectionsFunc(func(_ context.Context) ([]types.Connection, error) {
			return []types.Connection{{ID: "conn-1", Name: "Test", Labels: labels}}, nil
		}),
	)
	c := setupBufconn(t, tp)
	conns, err := c.LoadConnections(context.Background())
	if err != nil {
		t.Fatalf("LoadConnections: %v", err)
	}
	if len(conns) != 1 {
		t.Fatalf("expected 1 connection, got %d", len(conns))
	}
	if len(conns[0].Labels) != 1000 {
		t.Fatalf("expected 1000 labels, got %d", len(conns[0].Labels))
	}
}

func TestGR027_StreamingZeroEvents(t *testing.T) {
	tp := resourcetest.NewTestProvider(t,
		resourcetest.WithListenForEventsFunc(func(ctx context.Context, sink resource.WatchEventSink) error {
			// Return immediately — no events.
			return nil
		}),
	)
	c := setupBufconn(t, tp)
	var eventCount int
	err := c.ListenForEvents(context.Background(), &countingSink{
		onAdd: func(_ resource.WatchAddPayload) { eventCount++ },
	})
	if err != nil {
		t.Fatalf("ListenForEvents: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("expected 0 events, got %d", eventCount)
	}
}

func TestGR028_ErrorCodesRoundTrip(t *testing.T) {
	codes := []string{"NOT_FOUND", "ALREADY_EXISTS", "PERMISSION_DENIED", "INVALID_INPUT", "CONFLICT", "INTERNAL", "UNAVAILABLE", "TIMEOUT"}
	for _, code := range codes {
		t.Run(code, func(t *testing.T) {
			tp := resourcetest.NewTestProvider(t)
			tp.GetFunc = func(_ context.Context, _ string, _ resource.GetInput) (*resource.GetResult, error) {
				return nil, &resource.ResourceOperationError{Code: code, Title: code, Message: "test"}
			}
			c := setupBufconn(t, tp)
			ctx := resource.WithSession(context.Background(), &resource.Session{
				Connection: &types.Connection{ID: "conn-1"},
			})
			_, err := c.Get(ctx, "core::v1::Pod", resource.GetInput{ID: "x"})
			if err == nil {
				t.Fatal("expected error")
			}
			var roe *resource.ResourceOperationError
			if !errors.As(err, &roe) {
				t.Fatalf("expected ResourceOperationError, got %T", err)
			}
			if roe.Code != code {
				t.Fatalf("expected code %q, got %q", code, roe.Code)
			}
		})
	}
}

// ============================================================================
// GR-029: Stress — backpressure
// ============================================================================

func TestGR029_Backpressure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping backpressure test in short mode")
	}
	const eventCount = 10000
	tp := resourcetest.NewTestProvider(t,
		resourcetest.WithListenForEventsFunc(func(ctx context.Context, sink resource.WatchEventSink) error {
			for i := 0; i < eventCount; i++ {
				sink.OnAdd(resource.WatchAddPayload{
					Connection: "conn-1",
					Key:        "core::v1::Pod",
					ID:         fmt.Sprintf("pod-%d", i),
					Data:       json.RawMessage(`{"ok":true}`),
				})
			}
			return nil
		}),
	)
	c := setupBufconn(t, tp)

	var received int64
	sink := &countingSink{
		onAdd: func(_ resource.WatchAddPayload) {
			atomic.AddInt64(&received, 1)
			time.Sleep(10 * time.Microsecond) // Slow consumer.
		},
	}
	err := c.ListenForEvents(context.Background(), sink)
	if err != nil {
		t.Fatalf("ListenForEvents: %v", err)
	}
	if atomic.LoadInt64(&received) != eventCount {
		t.Fatalf("expected %d events, got %d", eventCount, received)
	}
}

// ============================================================================
// GR-030..033: Relationships + Health round-trips
// ============================================================================

func TestGR030_GetRelationshipsRoundTrip(t *testing.T) {
	tp := resourcetest.NewTestProvider(t)
	tp.GetRelationshipsFunc = func(_ context.Context, key string) ([]resource.RelationshipDescriptor, error) {
		return []resource.RelationshipDescriptor{
			{
				Type:              resource.RelOwns,
				TargetResourceKey: "core::v1::Container",
				Label:             "owns",
				InverseLabel:      "owned by",
				Cardinality:       "1:N",
				Extractor: &resource.RelationshipExtractor{
					Method:        "field_path",
					FieldPath:     "spec.containers",
					LabelSelector: map[string]string{"app": "nginx"},
				},
			},
			{
				Type:              resource.RelRunsOn,
				TargetResourceKey: "core::v1::Node",
				Label:             "runs on",
			},
		}, nil
	}
	c := setupBufconn(t, tp)
	rels, err := c.GetRelationships(context.Background(), "core::v1::Pod")
	if err != nil {
		t.Fatalf("GetRelationships: %v", err)
	}
	if len(rels) != 2 {
		t.Fatalf("expected 2 relationships, got %d", len(rels))
	}
	if rels[0].Type != resource.RelOwns {
		t.Fatalf("expected RelOwns, got %v", rels[0].Type)
	}
	if rels[0].Extractor == nil {
		t.Fatal("expected non-nil Extractor")
	}
	if rels[0].Extractor.FieldPath != "spec.containers" {
		t.Fatalf("expected FieldPath=spec.containers, got %q", rels[0].Extractor.FieldPath)
	}
	if rels[0].Extractor.LabelSelector["app"] != "nginx" {
		t.Fatal("LabelSelector mismatch")
	}
	if rels[1].Type != resource.RelRunsOn {
		t.Fatalf("expected RelRunsOn, got %v", rels[1].Type)
	}
}

func TestGR031_ResolveRelationshipsRoundTrip(t *testing.T) {
	tp := resourcetest.NewTestProvider(t)
	tp.ResolveRelationshipsFunc = func(_ context.Context, connID, key, id, ns string) ([]resource.ResolvedRelationship, error) {
		return []resource.ResolvedRelationship{
			{
				Descriptor: resource.RelationshipDescriptor{
					Type:              resource.RelOwns,
					TargetResourceKey: "core::v1::Container",
					Label:             "owns",
				},
				Targets: []resource.ResourceRef{
					{ConnectionID: connID, ResourceKey: "core::v1::Container", ID: "container-1", DisplayName: "nginx"},
					{ConnectionID: connID, ResourceKey: "core::v1::Container", ID: "container-2", DisplayName: "sidecar"},
				},
			},
		}, nil
	}
	c := setupBufconn(t, tp)
	resolved, err := c.ResolveRelationships(context.Background(), "conn-1", "core::v1::Pod", "pod-1", "default")
	if err != nil {
		t.Fatalf("ResolveRelationships: %v", err)
	}
	if len(resolved) != 1 {
		t.Fatalf("expected 1 resolved, got %d", len(resolved))
	}
	if len(resolved[0].Targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(resolved[0].Targets))
	}
	if resolved[0].Targets[0].DisplayName != "nginx" {
		t.Fatalf("expected DisplayName=nginx, got %q", resolved[0].Targets[0].DisplayName)
	}
	if resolved[0].Descriptor.Type != resource.RelOwns {
		t.Fatalf("expected RelOwns, got %v", resolved[0].Descriptor.Type)
	}
}

func TestGR032_GetHealthRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second) // Truncate for timestamp precision.
	probe := now.Add(-5 * time.Minute)
	transition := now.Add(-10 * time.Minute)

	tp := resourcetest.NewTestProvider(t)
	tp.GetHealthFunc = func(_ context.Context, connID, key string, data json.RawMessage) (*resource.ResourceHealth, error) {
		return &resource.ResourceHealth{
			Status:  resource.HealthDegraded,
			Reason:  "CrashLoopBackOff",
			Message: "Container keeps crashing",
			Since:   &now,
			Conditions: []resource.HealthCondition{
				{
					Type:               "Ready",
					Status:             "False",
					Reason:             "ContainersNotReady",
					Message:            "containers not ready",
					LastProbeTime:      &probe,
					LastTransitionTime: &transition,
				},
			},
		}, nil
	}
	c := setupBufconn(t, tp)
	health, err := c.GetHealth(context.Background(), "conn-1", "core::v1::Pod", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	if health.Status != resource.HealthDegraded {
		t.Fatalf("expected Degraded, got %v", health.Status)
	}
	if health.Reason != "CrashLoopBackOff" {
		t.Fatalf("expected reason CrashLoopBackOff, got %q", health.Reason)
	}
	if health.Since == nil || !health.Since.Equal(now) {
		t.Fatalf("Since mismatch: got %v, want %v", health.Since, now)
	}
	if len(health.Conditions) != 1 {
		t.Fatalf("expected 1 condition, got %d", len(health.Conditions))
	}
	cond := health.Conditions[0]
	if cond.Type != "Ready" {
		t.Fatalf("expected condition type Ready, got %q", cond.Type)
	}
	if cond.LastProbeTime == nil || !cond.LastProbeTime.Equal(probe) {
		t.Fatalf("LastProbeTime mismatch: got %v, want %v", cond.LastProbeTime, probe)
	}
	if cond.LastTransitionTime == nil || !cond.LastTransitionTime.Equal(transition) {
		t.Fatalf("LastTransitionTime mismatch: got %v, want %v", cond.LastTransitionTime, transition)
	}
}

func TestGR033_GetResourceEventsRoundTrip(t *testing.T) {
	first := time.Now().UTC().Add(-1 * time.Hour).Truncate(time.Second)
	last := time.Now().UTC().Truncate(time.Second)

	tp := resourcetest.NewTestProvider(t)
	tp.GetResourceEventsFunc = func(_ context.Context, connID, key, id, ns string, limit int32) ([]resource.ResourceEvent, error) {
		if limit != 50 {
			t.Errorf("expected limit=50, got %d", limit)
		}
		return []resource.ResourceEvent{
			{
				Type:      resource.SeverityWarning,
				Reason:    "BackOff",
				Message:   "Back-off restarting failed container",
				Source:    "kubelet",
				Count:     5,
				FirstSeen: first,
				LastSeen:  last,
			},
			{
				Type:      resource.SeverityNormal,
				Reason:    "Pulled",
				Message:   "Successfully pulled image",
				Source:    "kubelet",
				Count:     1,
				FirstSeen: last,
				LastSeen:  last,
			},
		}, nil
	}
	c := setupBufconn(t, tp)
	events, err := c.GetResourceEvents(context.Background(), "conn-1", "core::v1::Pod", "pod-1", "default", 50)
	if err != nil {
		t.Fatalf("GetResourceEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Type != resource.SeverityWarning {
		t.Fatalf("expected Warning, got %v", events[0].Type)
	}
	if events[0].Count != 5 {
		t.Fatalf("expected count=5, got %d", events[0].Count)
	}
	if !events[0].FirstSeen.Equal(first) {
		t.Fatalf("FirstSeen mismatch: got %v, want %v", events[0].FirstSeen, first)
	}
	if events[1].Type != resource.SeverityNormal {
		t.Fatalf("expected Normal, got %v", events[1].Type)
	}
}

// ============================================================================
// Test helpers
// ============================================================================

// countingSink is a WatchEventSink for tests that dispatches to optional callbacks.
type countingSink struct {
	onAdd         func(resource.WatchAddPayload)
	onUpdate      func(resource.WatchUpdatePayload)
	onDelete      func(resource.WatchDeletePayload)
	onStateChange func(resource.WatchStateEvent)
}

func (s *countingSink) OnAdd(p resource.WatchAddPayload) {
	if s.onAdd != nil {
		s.onAdd(p)
	}
}

func (s *countingSink) OnUpdate(p resource.WatchUpdatePayload) {
	if s.onUpdate != nil {
		s.onUpdate(p)
	}
}

func (s *countingSink) OnDelete(p resource.WatchDeletePayload) {
	if s.onDelete != nil {
		s.onDelete(p)
	}
}

func (s *countingSink) OnStateChange(e resource.WatchStateEvent) {
	if s.onStateChange != nil {
		s.onStateChange(e)
	}
}

