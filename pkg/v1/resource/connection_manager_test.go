package resource_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/resource/resourcetest"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

func loadAndStart(t *testing.T, mgr *resource.ConnectionManagerForTest, ctx context.Context) {
	t.Helper()
	_, err := mgr.LoadConnections(ctx)
	if err != nil {
		t.Fatalf("LoadConnections: %v", err)
	}
	_, err = mgr.StartConnection(ctx, "conn-1")
	if err != nil {
		t.Fatalf("StartConnection: %v", err)
	}
}

// --- CM-001: LoadConnections delegates ---
func TestCM_LoadConnections(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{
		LoadConnectionsFunc: func(_ context.Context) ([]types.Connection, error) {
			return []types.Connection{
				{ID: "a"}, {ID: "b"}, {ID: "c"},
			}, nil
		},
	}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	conns, err := mgr.LoadConnections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(conns) != 3 {
		t.Fatalf("expected 3, got %d", len(conns))
	}
}

// --- CM-002: LoadConnections error propagates ---
func TestCM_LoadConnectionsError(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{
		LoadConnectionsFunc: func(_ context.Context) ([]types.Connection, error) {
			return nil, errors.New("fail")
		},
	}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	conns, err := mgr.LoadConnections(ctx)
	if err == nil {
		t.Fatal("expected error")
	}
	if conns != nil {
		t.Fatal("expected nil conns")
	}
}

// --- CM-003: StartConnection creates client ---
func TestCM_StartConnection(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{
		CreateClientFunc: func(_ context.Context) (*string, error) {
			s := "client-1"
			return &s, nil
		},
	}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	loadAndStart(t, mgr, ctx)

	if cp.CreateClientCalls.Load() != 1 {
		t.Fatalf("expected 1 CreateClient call, got %d", cp.CreateClientCalls.Load())
	}
}

// --- CM-004: StartConnection already started returns existing ---
func TestCM_StartConnectionIdempotent(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	_, _ = mgr.LoadConnections(ctx)
	_, _ = mgr.StartConnection(ctx, "conn-1")
	_, err := mgr.StartConnection(ctx, "conn-1")
	if err != nil {
		t.Fatal(err)
	}
	if cp.CreateClientCalls.Load() != 1 {
		t.Fatalf("expected 1 CreateClient call (not 2), got %d", cp.CreateClientCalls.Load())
	}
}

// --- CM-005: StartConnection CreateClient fails ---
func TestCM_StartConnectionCreateClientFails(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{
		CreateClientFunc: func(_ context.Context) (*string, error) {
			return nil, errors.New("auth failed")
		},
	}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	_, _ = mgr.LoadConnections(ctx)
	_, err := mgr.StartConnection(ctx, "conn-1")
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- CM-006: StartConnection unknown connection ---
func TestCM_StartConnectionUnknown(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	_, _ = mgr.LoadConnections(ctx)
	_, err := mgr.StartConnection(ctx, "conn-unknown")
	if err == nil {
		t.Fatal("expected error for unknown connection")
	}
}

// --- CM-007: StopConnection destroys client ---
func TestCM_StopConnection(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	loadAndStart(t, mgr, ctx)

	_, err := mgr.StopConnection(ctx, "conn-1")
	if err != nil {
		t.Fatal(err)
	}
	if cp.DestroyClientCalls.Load() != 1 {
		t.Fatalf("expected 1 DestroyClient call, got %d", cp.DestroyClientCalls.Load())
	}
}

// --- CM-008: StopConnection not started ---
func TestCM_StopConnectionNotStarted(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	_, _ = mgr.LoadConnections(ctx)
	_, err := mgr.StopConnection(ctx, "conn-1")
	if err == nil {
		t.Fatal("expected error for not-started connection")
	}
}

// --- CM-010: GetClient active connection ---
func TestCM_GetClient(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	loadAndStart(t, mgr, ctx)

	client, err := mgr.GetClient("conn-1")
	if err != nil {
		t.Fatal(err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}
}

// --- CM-011: GetClient not started ---
func TestCM_GetClientNotStarted(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	client, err := mgr.GetClient("conn-1")
	if err == nil {
		t.Fatal("expected error")
	}
	if client != nil {
		t.Fatal("expected nil client")
	}
}

// --- CM-012: GetNamespaces delegates ---
func TestCM_GetNamespaces(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{
		GetNamespacesFunc: func(_ context.Context, _ *string) ([]string, error) {
			return []string{"default", "kube-system"}, nil
		},
	}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	loadAndStart(t, mgr, ctx)

	ns, err := mgr.GetNamespaces(ctx, "conn-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ns) != 2 {
		t.Fatalf("expected 2 namespaces, got %d", len(ns))
	}
}

// --- CM-013: GetNamespaces not started ---
func TestCM_GetNamespacesNotStarted(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	_, err := mgr.GetNamespaces(ctx, "conn-1")
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- CM-018: DeleteConnection stops if running ---
func TestCM_DeleteConnection(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	loadAndStart(t, mgr, ctx)

	err := mgr.DeleteConnection(ctx, "conn-1")
	if err != nil {
		t.Fatal(err)
	}
	if cp.DestroyClientCalls.Load() != 1 {
		t.Fatalf("expected 1 DestroyClient call, got %d", cp.DestroyClientCalls.Load())
	}
	_, err = mgr.GetConnection("conn-1")
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

// --- CM-021: StopConnection cancels connection context ---
func TestCM_StopCancelsContext(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	loadAndStart(t, mgr, ctx)

	connCtx, err := mgr.GetConnectionCtx("conn-1")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = mgr.StopConnection(ctx, "conn-1")
	if connCtx.Err() == nil {
		t.Fatal("expected connection context to be cancelled")
	}
}

// --- CM-022: Root context cancel cancels all connection contexts ---
func TestCM_RootCancelCancelsAll(t *testing.T) {
	rootCtx, rootCancel := context.WithCancel(context.Background())
	cp := &resourcetest.StubConnectionProvider[string]{
		LoadConnectionsFunc: func(_ context.Context) ([]types.Connection, error) {
			return []types.Connection{{ID: "conn-1", Name: "Test"}, {ID: "conn-2", Name: "Test2"}}, nil
		},
	}
	mgr := resource.NewConnectionManagerForTest(rootCtx, cp)
	_, _ = mgr.LoadConnections(rootCtx)
	_, _ = mgr.StartConnection(rootCtx, "conn-1")
	_, _ = mgr.StartConnection(rootCtx, "conn-2")

	ctx1, _ := mgr.GetConnectionCtx("conn-1")
	ctx2, _ := mgr.GetConnectionCtx("conn-2")

	rootCancel()

	if ctx1.Err() == nil || ctx2.Err() == nil {
		t.Fatal("expected both connection contexts cancelled")
	}
}

// --- CM-009: StopConnection DestroyClient fails, still removes ---
func TestCM_StopConnectionDestroyFails(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{
		DestroyClientFunc: func(_ context.Context, _ *string) error {
			return errors.New("destroy failed")
		},
	}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	loadAndStart(t, mgr, ctx)

	_, err := mgr.StopConnection(ctx, "conn-1")
	if err == nil {
		t.Fatal("expected error from DestroyClient")
	}
	// Client should be removed despite error (best-effort cleanup).
	_, getErr := mgr.GetClient("conn-1")
	if getErr == nil {
		t.Fatal("expected client to be removed after stop")
	}
}

// --- CM-014: CheckConnection delegates ---
func TestCM_CheckConnection(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{
		CheckConnectionFunc: func(_ context.Context, conn *types.Connection, _ *string) (types.ConnectionStatus, error) {
			return types.ConnectionStatus{
				Connection: conn,
				Status:     types.ConnectionStatusConnected,
			}, nil
		},
	}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	loadAndStart(t, mgr, ctx)

	status, err := mgr.CheckConnection(ctx, "conn-1")
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != types.ConnectionStatusConnected {
		t.Fatalf("expected CONNECTED, got %s", status.Status)
	}
}

// --- CM-015: ListConnections returns all ---
func TestCM_ListConnections(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{
		LoadConnectionsFunc: func(_ context.Context) ([]types.Connection, error) {
			return []types.Connection{
				{ID: "conn-1", Name: "One"},
				{ID: "conn-2", Name: "Two"},
				{ID: "conn-3", Name: "Three"},
			}, nil
		},
	}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	mgr.LoadConnections(ctx)
	mgr.StartConnection(ctx, "conn-1")
	mgr.StartConnection(ctx, "conn-2")

	conns, err := mgr.ListConnections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(conns) != 3 {
		t.Fatalf("expected 3 connections, got %d", len(conns))
	}
}

// --- CM-016: UpdateConnection updates stored ---
func TestCM_UpdateConnection(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	loadAndStart(t, mgr, ctx)

	updated, err := mgr.UpdateConnection(ctx, types.Connection{ID: "conn-1", Name: "Updated"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Updated" {
		t.Fatalf("expected Updated, got %s", updated.Name)
	}

	conn, _ := mgr.GetConnection("conn-1")
	if conn.Name != "Updated" {
		t.Fatalf("expected stored connection updated, got %s", conn.Name)
	}
}

// --- CM-019: DeleteConnection not running, just removes ---
func TestCM_DeleteConnectionNotRunning(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	mgr.LoadConnections(ctx)
	// Don't start — just delete.
	err := mgr.DeleteConnection(ctx, "conn-1")
	if err != nil {
		t.Fatal(err)
	}
	if cp.DestroyClientCalls.Load() != 0 {
		t.Fatal("expected 0 DestroyClient calls (not running)")
	}
	_, getErr := mgr.GetConnection("conn-1")
	if getErr == nil {
		t.Fatal("expected error after delete")
	}
}

// --- CM-020: Connection context is child of root ---
func TestCM_ConnectionContextIsChildOfRoot(t *testing.T) {
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	cp := &resourcetest.StubConnectionProvider[string]{}
	mgr := resource.NewConnectionManagerForTest(rootCtx, cp)
	mgr.LoadConnections(rootCtx)
	mgr.StartConnection(rootCtx, "conn-1")

	connCtx, err := mgr.GetConnectionCtx("conn-1")
	if err != nil {
		t.Fatal(err)
	}
	// Root cancel should cancel connection context.
	rootCancel()
	if connCtx.Err() == nil {
		t.Fatal("expected connection ctx cancelled when root cancelled")
	}
}

// --- CM-028: Concurrent GetClient during Start/Stop ---
func TestCM_ConcurrentGetClientDuringStartStop(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	mgr.LoadConnections(ctx)
	mgr.StartConnection(ctx, "conn-1")

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mgr.GetClient("conn-1")
		}()
	}
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mgr.StopConnection(ctx, "conn-1")
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			mgr.StartConnection(ctx, "conn-1")
		}()
	}
	wg.Wait()
}

// --- CM-029: StartConnection context carries Session ---
func TestCM_StartConnectionSessionInContext(t *testing.T) {
	ctx := context.Background()
	var capturedCtx context.Context
	cp := &resourcetest.StubConnectionProvider[string]{
		CreateClientFunc: func(ctx context.Context) (*string, error) {
			capturedCtx = ctx
			s := "client"
			return &s, nil
		},
	}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	mgr.LoadConnections(ctx)
	mgr.StartConnection(ctx, "conn-1")

	sess := resource.SessionFromContext(capturedCtx)
	if sess == nil {
		t.Fatal("expected session in CreateClient ctx")
	}
	if sess.Connection == nil || sess.Connection.ID != "conn-1" {
		t.Fatal("expected session with conn-1")
	}
}

// --- CM-030: StopConnection context carries Session ---
func TestCM_StopConnectionSessionInContext(t *testing.T) {
	ctx := context.Background()
	var capturedCtx context.Context
	cp := &resourcetest.StubConnectionProvider[string]{
		DestroyClientFunc: func(ctx context.Context, _ *string) error {
			capturedCtx = ctx
			return nil
		},
	}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	loadAndStart(t, mgr, ctx)
	mgr.StopConnection(ctx, "conn-1")

	sess := resource.SessionFromContext(capturedCtx)
	if sess == nil {
		t.Fatal("expected session in DestroyClient ctx")
	}
	if sess.Connection == nil || sess.Connection.ID != "conn-1" {
		t.Fatal("expected session with conn-1")
	}
}

// --- CM-017: UpdateConnection restarts client if active ---
func TestCM_UpdateConnectionRestartsClient(t *testing.T) {
	ctx := context.Background()
	callCount := 0
	cp := &resourcetest.StubConnectionProvider[string]{
		CreateClientFunc: func(_ context.Context) (*string, error) {
			callCount++
			s := fmt.Sprintf("client-%d", callCount)
			return &s, nil
		},
	}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	loadAndStart(t, mgr, ctx)

	// Verify initial client.
	c1, _ := mgr.GetClient("conn-1")
	if *c1 != "client-1" {
		t.Fatalf("expected client-1, got %s", *c1)
	}

	// Capture old context.
	oldCtx, _ := mgr.GetConnectionCtx("conn-1")

	// Update connection — should restart client.
	_, err := mgr.UpdateConnection(ctx, types.Connection{ID: "conn-1", Name: "Updated"})
	if err != nil {
		t.Fatal(err)
	}

	// Verify new client.
	c2, _ := mgr.GetClient("conn-1")
	if *c2 != "client-2" {
		t.Fatalf("expected client-2, got %s", *c2)
	}

	// Verify old context was cancelled.
	if oldCtx.Err() == nil {
		t.Fatal("expected old connection context to be cancelled")
	}

	// Verify new context is active.
	newCtx, _ := mgr.GetConnectionCtx("conn-1")
	if newCtx.Err() != nil {
		t.Fatal("expected new connection context to be active")
	}

	// CreateClient called twice, DestroyClient once.
	if cp.CreateClientCalls.Load() != 2 {
		t.Fatalf("expected 2 CreateClient calls, got %d", cp.CreateClientCalls.Load())
	}
	if cp.DestroyClientCalls.Load() != 1 {
		t.Fatalf("expected 1 DestroyClient call, got %d", cp.DestroyClientCalls.Load())
	}
}

// --- CM-025: RefreshClient detected and works ---
func TestCM_RefreshClientSupported(t *testing.T) {
	ctx := context.Background()
	rp := &resourcetest.RefreshableConnectionProvider[string]{}
	mgr := resource.NewConnectionManagerForTest(ctx, rp)
	mgr.LoadConnections(ctx)
	mgr.StartConnection(ctx, "conn-1")

	err := mgr.RefreshClient(ctx, "conn-1")
	if err != nil {
		t.Fatal(err)
	}
	if rp.RefreshCalls.Load() != 1 {
		t.Fatalf("expected 1 RefreshClient call, got %d", rp.RefreshCalls.Load())
	}
}

// --- CM-026: RefreshClient not supported ---
func TestCM_RefreshClientNotSupported(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	loadAndStart(t, mgr, ctx)

	err := mgr.RefreshClient(ctx, "conn-1")
	if err == nil {
		t.Fatal("expected error for unsupported RefreshClient")
	}
}

// --- CM-031: StartConnection with already-cancelled context ---
func TestCM_StartConnectionCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	cp := &resourcetest.StubConnectionProvider[string]{}
	mgr := resource.NewConnectionManagerForTest(context.Background(), cp)
	mgr.LoadConnections(context.Background())

	// CreateClient receives the cancelled context, but the implementation still
	// delegates to the provider. The provider decides what to do.
	_, err := mgr.StartConnection(ctx, "conn-1")
	// May or may not error depending on provider behavior — just no panic.
	_ = err
}

// --- CM-033: Multiple connections started in rapid succession ---
func TestCM_MultipleConnectionsRapidStart(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{
		LoadConnectionsFunc: func(_ context.Context) ([]types.Connection, error) {
			return []types.Connection{
				{ID: "c1"}, {ID: "c2"}, {ID: "c3"}, {ID: "c4"}, {ID: "c5"},
			}, nil
		},
	}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	mgr.LoadConnections(ctx)

	for _, id := range []string{"c1", "c2", "c3", "c4", "c5"} {
		_, err := mgr.StartConnection(ctx, id)
		if err != nil {
			t.Fatalf("StartConnection %s: %v", id, err)
		}
	}
	ids := mgr.ActiveConnectionIDs()
	if len(ids) != 5 {
		t.Fatalf("expected 5 active, got %d", len(ids))
	}
}

// --- CM-035: GetNamespaces returns error ---
func TestCM_GetNamespacesError(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{
		GetNamespacesFunc: func(_ context.Context, _ *string) ([]string, error) {
			return nil, errors.New("ns error")
		},
	}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	loadAndStart(t, mgr, ctx)

	_, err := mgr.GetNamespaces(ctx, "conn-1")
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- CM-036: CheckConnection returns error ---
func TestCM_CheckConnectionError(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{
		CheckConnectionFunc: func(_ context.Context, _ *types.Connection, _ *string) (types.ConnectionStatus, error) {
			return types.ConnectionStatus{}, errors.New("check failed")
		},
	}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	loadAndStart(t, mgr, ctx)

	_, err := mgr.CheckConnection(ctx, "conn-1")
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- CM-038: LoadConnections called multiple times (refresh) ---
func TestCM_LoadConnectionsRefresh(t *testing.T) {
	ctx := context.Background()
	callCount := 0
	cp := &resourcetest.StubConnectionProvider[string]{
		LoadConnectionsFunc: func(_ context.Context) ([]types.Connection, error) {
			callCount++
			if callCount == 1 {
				return []types.Connection{{ID: "c1"}}, nil
			}
			return []types.Connection{{ID: "c1"}, {ID: "c2"}}, nil
		},
	}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	mgr.LoadConnections(ctx)
	mgr.StartConnection(ctx, "c1") // start c1

	// Reload — should not disrupt running connection.
	mgr.LoadConnections(ctx)
	if !mgr.IsStarted("c1") {
		t.Fatal("expected c1 still started after reload")
	}
	conns, _ := mgr.ListConnections(ctx)
	if len(conns) != 2 {
		t.Fatalf("expected 2 connections after reload, got %d", len(conns))
	}
}

// --- CM-039: UpdateConnection for connection that doesn't exist ---
func TestCM_UpdateConnectionNonExistent(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	// Don't load any connections.
	_, err := mgr.UpdateConnection(ctx, types.Connection{ID: "ghost"})
	// Should not error — just stores in loaded.
	if err != nil {
		t.Fatal(err)
	}
}

// --- CM-040: DeleteConnection for connection that doesn't exist ---
func TestCM_DeleteConnectionNonExistent(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	err := mgr.DeleteConnection(ctx, "ghost")
	if err != nil {
		t.Fatal("expected no error for deleting non-existent connection")
	}
}

// --- CM-044: Concurrent LoadConnections calls ---
func TestCM_ConcurrentLoadConnections(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mgr.LoadConnections(ctx)
		}()
	}
	wg.Wait()
}

// --- CM-045: DestroyClient called with correct client pointer ---
func TestCM_DestroyClientCorrectPointer(t *testing.T) {
	ctx := context.Background()
	original := "the-client"
	var destroyed *string
	cp := &resourcetest.StubConnectionProvider[string]{
		CreateClientFunc: func(_ context.Context) (*string, error) {
			return &original, nil
		},
		DestroyClientFunc: func(_ context.Context, client *string) error {
			destroyed = client
			return nil
		},
	}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	loadAndStart(t, mgr, ctx)
	mgr.StopConnection(ctx, "conn-1")

	if destroyed != &original {
		t.Fatal("expected DestroyClient called with same pointer returned by CreateClient")
	}
}

// --- CM-027: Concurrent Start/Stop don't race ---
func TestCM_ConcurrentStartStop(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	_, _ = mgr.LoadConnections(ctx)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = mgr.StartConnection(ctx, "conn-1")
		}()
	}
	wg.Wait()

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = mgr.StopConnection(ctx, "conn-1")
		}()
	}
	wg.Wait()
}

// --- CM-032: StartConnection — CreateClient returns nil pointer without error ---
func TestCM_CreateClientReturnsNil(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{
		CreateClientFunc: func(_ context.Context) (*string, error) {
			return nil, nil // nil client, no error
		},
	}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	mgr.LoadConnections(ctx)

	status, err := mgr.StartConnection(ctx, "conn-1")
	// Should succeed (nil client stored — SDK trusts the provider).
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "CONNECTED" {
		t.Fatalf("expected CONNECTED, got %s", status.Status)
	}
	client, err := mgr.GetClient("conn-1")
	if err != nil {
		t.Fatal(err)
	}
	if client != nil {
		t.Fatal("expected nil client")
	}
}

// --- CM-034: GetNamespaces returns nil vs empty slice ---
func TestCM_GetNamespacesNilVsEmpty(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{
		GetNamespacesFunc: func(_ context.Context, _ *string) ([]string, error) {
			return nil, nil // nil slice, no error
		},
	}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	mgr.LoadConnections(ctx)
	mgr.StartConnection(ctx, "conn-1")

	ns, err := mgr.GetNamespaces(ctx, "conn-1")
	if err != nil {
		t.Fatal(err)
	}
	if ns != nil && len(ns) != 0 {
		t.Fatalf("expected nil or empty, got %v", ns)
	}
}

// --- CM-037: LoadConnections returns duplicate connection IDs ---
func TestCM_LoadConnectionsDuplicateIDs(t *testing.T) {
	ctx := context.Background()
	cp := &resourcetest.StubConnectionProvider[string]{
		LoadConnectionsFunc: func(_ context.Context) ([]types.Connection, error) {
			return []types.Connection{
				{ID: "conn-1", Name: "First"},
				{ID: "conn-1", Name: "Second"}, // duplicate
			}, nil
		},
	}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	conns, err := mgr.LoadConnections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(conns) != 2 {
		t.Fatalf("expected 2 conns returned, got %d", len(conns))
	}
	// Last write wins in the loaded map.
	conn, err := mgr.GetConnection("conn-1")
	if err != nil {
		t.Fatal(err)
	}
	if conn.Name != "Second" {
		t.Fatalf("expected last duplicate to win, got %s", conn.Name)
	}
}

// --- CM-041: Connection ID with special characters ---
func TestCM_SpecialCharacterConnectionID(t *testing.T) {
	ctx := context.Background()
	specialID := "conn/with spaces&special=chars"
	cp := &resourcetest.StubConnectionProvider[string]{
		LoadConnectionsFunc: func(_ context.Context) ([]types.Connection, error) {
			return []types.Connection{{ID: specialID, Name: "Special"}}, nil
		},
	}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	mgr.LoadConnections(ctx)

	status, err := mgr.StartConnection(ctx, specialID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "CONNECTED" {
		t.Fatalf("expected CONNECTED, got %s", status.Status)
	}
	conn, err := mgr.GetConnection(specialID)
	if err != nil {
		t.Fatal(err)
	}
	if conn.ID != specialID {
		t.Fatalf("expected %q, got %q", specialID, conn.ID)
	}
}

// --- CM-043: StopConnection during slow CreateClient ---
func TestCM_StopDuringSlowCreate(t *testing.T) {
	ctx := context.Background()
	creating := make(chan struct{})
	cp := &resourcetest.StubConnectionProvider[string]{
		CreateClientFunc: func(ctx context.Context) (*string, error) {
			close(creating)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	mgr := resource.NewConnectionManagerForTest(ctx, cp)
	mgr.LoadConnections(ctx)

	go func() {
		mgr.StartConnection(ctx, "conn-1")
	}()
	<-creating

	// Stop should handle gracefully even though start is in progress.
	_, err := mgr.StopConnection(ctx, "conn-1")
	_ = err // either error or nil — just no panic
}

// --- CM-023: WatchConnections detected when ConnectionProvider implements ConnectionWatcher ---
func TestCM_WatchConnectionsDetected(t *testing.T) {
	ctx := context.Background()
	wp := &resourcetest.WatchingConnectionProvider[string]{
		WatchConnectionsFunc: func(ctx context.Context) (<-chan []types.Connection, error) {
			ch := make(chan []types.Connection)
			go func() { <-ctx.Done(); close(ch) }()
			return ch, nil
		},
	}

	// The connectionManager stores the provider as ConnectionProvider[ClientT].
	// Verify the stored provider can be type-asserted to ConnectionWatcher.
	mgr := resource.NewConnectionManagerForTest(ctx, wp)
	_ = mgr // manager created without error

	// The controller's WatchConnections does: watcher, ok := c.connMgr.provider.(ConnectionWatcher)
	// Verify the assertion succeeds on the WatchingConnectionProvider.
	var iface interface{} = wp
	_, ok := iface.(resource.ConnectionWatcher)
	if !ok {
		t.Fatal("WatchingConnectionProvider should implement ConnectionWatcher")
	}
}

// --- CM-024: WatchConnections not detected on plain ConnectionProvider ---
func TestCM_WatchConnectionsNotDetected(t *testing.T) {
	cp := &resourcetest.StubConnectionProvider[string]{}

	// Plain StubConnectionProvider does NOT implement ConnectionWatcher.
	var iface interface{} = cp
	_, ok := iface.(resource.ConnectionWatcher)
	if ok {
		t.Fatal("StubConnectionProvider should NOT implement ConnectionWatcher")
	}
}

// --- CM-042: WatchConnections provider channel closed ---
func TestCM_WatchConnectionsChannelClosed(t *testing.T) {
	ctx := context.Background()
	ch := make(chan []types.Connection, 1)

	wp := &resourcetest.WatchingConnectionProvider[string]{
		WatchConnectionsFunc: func(ctx context.Context) (<-chan []types.Connection, error) {
			return ch, nil
		},
	}

	cfg := resource.ResourcePluginConfig[string]{
		Connections: wp,
		Resources:   []resource.ResourceRegistration[string]{},
	}
	ctrl, err := resource.BuildResourceControllerForTest(ctx, cfg)
	if err != nil {
		t.Fatalf("BuildResourceController: %v", err)
	}
	defer ctrl.Close()

	stream := make(chan []types.Connection, 10)
	done := make(chan error, 1)
	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()

	go func() {
		done <- ctrl.WatchConnections(watchCtx, stream)
	}()

	// Send one update, then close the provider channel.
	ch <- []types.Connection{{ID: "conn-new"}}
	conns := <-stream
	if len(conns) != 1 || conns[0].ID != "conn-new" {
		t.Fatalf("expected conn-new, got %v", conns)
	}

	// Close the provider channel — WatchConnections should return nil.
	close(ch)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil error on channel close, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WatchConnections did not return after channel close")
	}
}
