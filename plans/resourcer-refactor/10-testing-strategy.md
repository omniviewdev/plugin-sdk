# 10: Testing Strategy — SDK Refactor Validation

## 1. Goal

Before migrating the K8s plugin or engine controller to the new interfaces (doc 09), the new
SDK must be **fully implemented and fully tested in isolation**. "Fully tested" means: every
interface boundary exercised, every lifecycle path verified, every error/recovery scenario
covered — all without importing `go-plugin`, spawning processes, or touching gRPC.

Only after every gate in this document passes do we begin migrating consumers.

---

## 2. Principles

1. **Test the new before breaking the old.** The new SDK interfaces, internal services
   (`watchManager`, `connectionManager`, `resourcerRegistry`, `resourceController`), and
   `Provider` implementation must be 100% tested before any consumer code changes.

2. **No mocking frameworks.** Hand-written test doubles only — configurable structs with
   function fields or canned return values. Matches the existing codebase pattern
   (`newFakeClientSet`, `mockInformerHandle`, `InProcessBackend`). No `mockgen`, no
   `gomock`, no `testify/mock`.

3. **No go-plugin in test imports.** SDK tests must compile and run with zero dependency on
   `hashicorp/go-plugin`. The `resourceController[ClientT]` satisfies `Provider` (non-generic)
   and is testable in-process.

4. **No gRPC in unit/integration tests.** gRPC proto compliance is verified separately
   (§7). SDK tests call Go methods directly.

5. **Real concurrency, not simulated.** Watch goroutines, context cancellation, backoff —
   test with real goroutines and `assert.Eventually`. No fake timers or test clocks unless
   testing specific timing behavior.

6. **Fail gates block migration.** Each test level (§4–§7) is a gate. All tests in a gate
   must pass before proceeding to the next gate. Migration (§8) only starts after all gates
   are green.

---

## 3. Test Levels

```
Level 1: SDK Unit Tests (§4)
  │  Interfaces + internal services in isolation
  │  No integration between services
  │
Level 2: SDK Integration Tests (§5)
  │  resourceController wired to mock plugin-author implementations
  │  Full Provider interface exercised in-process
  │
Level 3: Engine Integration Tests (§6)
  │  Engine controller → SDK controller via InProcessBackend
  │  Full data flow without gRPC
  │
Level 4: gRPC Conformance Tests (§7)
  │  Proto ↔ Go interface round-trip
  │  Streaming RPCs verified
  │
Gate: All green → Begin migration (§8)
```

---

## 4. Level 1: SDK Unit Tests

Each internal service is tested in isolation with minimal test doubles. These tests verify
that individual components behave correctly before they're wired together.

### 4.1 resourcerRegistry[ClientT]

**What it does:** Stores `ResourceRegistration[ClientT]` entries, looks up by resource key,
falls back to pattern resourcers, detects optional interfaces (`Watcher`, `SyncPolicyDeclarer`,
`DefinitionProvider`, `ActionResourcer`, `SchemaResourcer`, `ErrorClassifier`).

**Test file:** `plugin-sdk/pkg/resource/services/resourcer_registry_test.go`

| Test | What it verifies |
|------|-----------------|
| `TestRegistry_LookupExact` | Exact key match returns correct resourcer |
| `TestRegistry_LookupPattern` | `"*"` pattern matches unknown keys |
| `TestRegistry_LookupPrecedence` | Exact match wins over pattern match |
| `TestRegistry_LookupNotFound` | Unknown key with no pattern returns error |
| `TestRegistry_DetectsWatcher` | Type-asserts `Watcher` on resourcer |
| `TestRegistry_DetectsSyncPolicy` | Type-asserts `SyncPolicyDeclarer`, returns default if absent |
| `TestRegistry_DetectsDefinitionProvider` | `DefinitionProvider` wins over `Registration.Definition` |
| `TestRegistry_DefinitionFallback` | `Registration.Definition` used if no `DefinitionProvider` |
| `TestRegistry_DefaultDefinitionFallback` | Config's `DefaultDefinition` used if both are nil |
| `TestRegistry_DetectsActionResourcer` | Type-asserts `ActionResourcer` on resourcer |
| `TestRegistry_DetectsErrorClassifier` | Per-resource classifier wins over global |
| `TestRegistry_ListAll` | Returns all registered metas |
| `TestRegistry_ListWatchable` | Returns only metas where resourcer implements `Watcher` |
| `TestRegistry_Empty` | Empty registry returns appropriate errors |

**Test doubles needed:**

```go
// Minimal resourcer — CRUD only, no optional interfaces
type stubResourcer struct {
    getResult  *GetResult
    listResult *ListResult
    // etc.
}

// Resourcer + Watcher
type watchableResourcer struct {
    stubResourcer
    watchFunc func(ctx context.Context, client *string, meta ResourceMeta, sink WatchEventSink) error
}
func (r *watchableResourcer) Watch(...) error { return r.watchFunc(...) }

// Resourcer + Watcher + SyncPolicyDeclarer
type policyResourcer struct {
    watchableResourcer
    policy SyncPolicy
}
func (r *policyResourcer) SyncPolicy() SyncPolicy { return r.policy }

// Resourcer + DefinitionProvider
type definingResourcer struct {
    stubResourcer
    def ResourceDefinition
}
func (r *definingResourcer) Definition() ResourceDefinition { return r.def }
```

### 4.2 connectionManager[ClientT]

**What it does:** Manages the lifecycle of connections and their typed clients. Delegates to
`ConnectionProvider[ClientT]` for creation/destruction. Optionally detects `ConnectionWatcher`
and `ClientRefresher`. Tracks connection state.

**Test file:** `plugin-sdk/pkg/resource/services/connection_manager_test.go`

| Test | What it verifies |
|------|-----------------|
| `TestConnMgr_LoadConnections` | Calls `ConnectionProvider.LoadConnections`, stores result |
| `TestConnMgr_StartConnection` | Calls `CreateClient`, stores client, returns status |
| `TestConnMgr_StartConnection_AlreadyStarted` | Returns existing status, doesn't re-create |
| `TestConnMgr_StartConnection_CreateFails` | Returns error, no client stored |
| `TestConnMgr_StopConnection` | Calls `DestroyClient`, removes client, cancels connection ctx |
| `TestConnMgr_StopConnection_NotStarted` | Returns error |
| `TestConnMgr_GetClient` | Returns client for active connection |
| `TestConnMgr_GetClient_NotStarted` | Returns error |
| `TestConnMgr_GetNamespaces` | Delegates to `ConnectionProvider.GetNamespaces` |
| `TestConnMgr_CheckConnection` | Delegates to `ConnectionProvider.CheckConnection` |
| `TestConnMgr_ListConnections` | Returns all connections with runtime state |
| `TestConnMgr_UpdateConnection` | Updates stored connection, optionally re-creates client |
| `TestConnMgr_DeleteConnection` | Stops client if running, removes connection |
| `TestConnMgr_WatchConnections_Supported` | Detects `ConnectionWatcher`, starts goroutine |
| `TestConnMgr_WatchConnections_NotSupported` | No `ConnectionWatcher` → no-op |
| `TestConnMgr_RefreshClient_Supported` | Detects `ClientRefresher`, calls `RefreshClient` |
| `TestConnMgr_RefreshClient_NotSupported` | No `ClientRefresher` → returns error |
| `TestConnMgr_ConnectionContext` | Connection ctx is child of root ctx |
| `TestConnMgr_StopCancelsContext` | Stopping connection cancels its context |
| `TestConnMgr_Concurrency` | Concurrent Start/Stop/Get don't race (run with `-race`) |

**Test doubles needed:**

```go
type stubConnectionProvider[ClientT any] struct {
    createFunc     func(ctx context.Context) (*ClientT, error)
    destroyFunc    func(ctx context.Context, client *ClientT) error
    loadFunc       func(ctx context.Context) ([]types.Connection, error)
    checkFunc      func(ctx context.Context, conn *types.Connection, client *ClientT) (types.ConnectionStatus, error)
    namespacesFunc func(ctx context.Context, client *ClientT) ([]string, error)
}

// Optional: also implement ConnectionWatcher, ClientRefresher
type watchingConnectionProvider[ClientT any] struct {
    stubConnectionProvider[ClientT]
    watchFunc func(ctx context.Context) (<-chan []types.Connection, error)
}
func (p *watchingConnectionProvider[ClientT]) WatchConnections(...) (<-chan []types.Connection, error) {
    return p.watchFunc(...)
}
```

### 4.3 watchManager[ClientT]

**What it does:** Scans `resourcerRegistry` for `Watcher`-capable resourcers. When a connection
starts, starts Watch goroutines for `SyncOnConnect` resources. Lazily starts `SyncOnFirstQuery`
watches. Manages per-resource lifecycle (start/stop/restart). Handles error recovery with
backoff. Provides `WatchEventSink` that fans events into a subscriber.

**Test file:** `plugin-sdk/pkg/resource/services/watch_manager_test.go`

**This is the most critical test file.** The `watchManager` owns the complexity that was
previously split across `InformerFactory`, `InformerHandle`, and `InformerManager`.

| Test | What it verifies |
|------|-----------------|
| **Lifecycle — Connection** | |
| `TestWatchMgr_StartConnection_SyncOnConnect` | Starts Watch goroutines for all SyncOnConnect resourcers |
| `TestWatchMgr_StartConnection_SyncOnFirstQuery` | Does NOT start Watch for SyncOnFirstQuery resourcers |
| `TestWatchMgr_StartConnection_SyncNever` | Does NOT start Watch for SyncNever resourcers |
| `TestWatchMgr_StartConnection_NonWatcher` | Resourcers without Watcher are skipped |
| `TestWatchMgr_StopConnection` | Cancels all Watch goroutines for connection, cleans up state |
| `TestWatchMgr_StopConnection_WatchReturns` | Watch() goroutines return nil after ctx cancel |
| **Lifecycle — Per-Resource** | |
| `TestWatchMgr_EnsureResourceWatch` | Starts Watch for a specific resource |
| `TestWatchMgr_EnsureResourceWatch_AlreadyRunning` | No-op if already watching |
| `TestWatchMgr_EnsureResourceWatch_SyncOnFirstQuery` | Triggers lazy start |
| `TestWatchMgr_StopResourceWatch` | Cancels specific resource's Watch, others continue |
| `TestWatchMgr_RestartResourceWatch` | Cancels and re-starts Watch with fresh context |
| `TestWatchMgr_IsResourceWatchRunning` | Returns true/false based on goroutine state |
| **Event Flow** | |
| `TestWatchMgr_EventsFlowToSink` | OnAdd/OnUpdate/OnDelete from Watch reach the subscriber |
| `TestWatchMgr_StateEventsFlow` | OnStateChange from Watch reaches the subscriber |
| `TestWatchMgr_MultipleConnections` | Events from different connections are distinguished |
| **Error Recovery** | |
| `TestWatchMgr_WatchError_Restarts` | Watch() returns error → restarted with backoff |
| `TestWatchMgr_WatchError_MaxRetries` | After max retries → emits Failed state, stops retrying |
| `TestWatchMgr_WatchError_BackoffIncreases` | Each retry waits longer (exponential backoff) |
| `TestWatchMgr_WatchPanic_Recovered` | Panic inside Watch() is recovered, treated as error |
| **Context Hierarchy** | |
| `TestWatchMgr_ConnectionCtxCancelsAllWatches` | Cancel connection ctx → all resource watches stop |
| `TestWatchMgr_RootCtxCancelsEverything` | Cancel root ctx → all connections and watches stop |
| `TestWatchMgr_ResourceCtxIndependent` | Cancelling one resource doesn't affect others |
| **Edge Cases** | |
| `TestWatchMgr_NoWatchers` | Connection with zero Watcher resourcers → no goroutines |
| `TestWatchMgr_WatchBlocksUntilCancel` | Verify Watch goroutine doesn't exit prematurely |
| `TestWatchMgr_ConcurrentStartStop` | Concurrent EnsureResourceWatch/StopResourceWatch don't race |
| `TestWatchMgr_StopBeforeStart` | StopResourceWatch on never-started resource → no-op/error |

**Test doubles needed:**

```go
// Watch that blocks until cancelled and optionally emits events
type blockingWatcher struct {
    stubResourcer
    onStart func(sink WatchEventSink)  // called when Watch begins, before blocking
}
func (w *blockingWatcher) Watch(ctx context.Context, client *string, meta ResourceMeta, sink WatchEventSink) error {
    if w.onStart != nil { w.onStart(sink) }
    <-ctx.Done()
    return nil
}

// Watch that fails after emitting some events
type failingWatcher struct {
    stubResourcer
    failAfter time.Duration
    err       error
}
func (w *failingWatcher) Watch(ctx context.Context, ...) error {
    select {
    case <-time.After(w.failAfter):
        return w.err
    case <-ctx.Done():
        return nil
    }
}

// Watch that panics
type panickingWatcher struct {
    stubResourcer
}
func (w *panickingWatcher) Watch(ctx context.Context, ...) error {
    panic("simulated crash")
}

// Recording sink
type recordingSink struct {
    mu      sync.Mutex
    adds    []WatchAddPayload
    updates []WatchUpdatePayload
    deletes []WatchDeletePayload
    states  []WatchStateEvent
}
func (s *recordingSink) OnAdd(p WatchAddPayload)       { s.mu.Lock(); s.adds = append(s.adds, p); s.mu.Unlock() }
func (s *recordingSink) OnUpdate(p WatchUpdatePayload) { s.mu.Lock(); s.updates = append(s.updates, p); s.mu.Unlock() }
func (s *recordingSink) OnDelete(p WatchDeletePayload) { s.mu.Lock(); s.deletes = append(s.deletes, p); s.mu.Unlock() }
func (s *recordingSink) OnStateChange(e WatchStateEvent) { s.mu.Lock(); s.states = append(s.states, e); s.mu.Unlock() }
```

### 4.4 Session & Context Helpers

**Test file:** `plugin-sdk/pkg/resource/types/session_test.go`

| Test | What it verifies |
|------|-----------------|
| `TestWithSession_RoundTrip` | `WithSession` → `SessionFromContext` returns same Session |
| `TestSessionFromContext_Missing` | Returns nil when no Session in context |
| `TestConnectionFromContext` | Convenience accessor returns `Session.Connection` |
| `TestConnectionFromContext_NoSession` | Returns nil when no Session |
| `TestSession_ContextCancellation` | Session travels through cancelled context correctly |

### 4.5 Type & Discovery Tests

**Test file:** `plugin-sdk/pkg/resource/services/type_manager_test.go`

| Test | What it verifies |
|------|-----------------|
| `TestTypeMgr_StaticTypes` | Returns types from registry |
| `TestTypeMgr_DiscoveredTypes` | Merges discovery results with static types |
| `TestTypeMgr_DiscoveryDisabled` | No `DiscoveryProvider` → static types only |
| `TestTypeMgr_GetResourceGroups` | Groups include both static and discovered resources |
| `TestTypeMgr_OnConnectionRemoved` | Calls `DiscoveryProvider.OnConnectionRemoved` |
| `TestTypeMgr_HasResourceType` | Returns true for known, false for unknown |
| `TestTypeMgr_GetDefinition` | Returns definition via DefinitionProvider → Registration.Definition → DefaultDefinition precedence |

---

## 5. Level 2: SDK Integration Tests

Wire `resourceController[ClientT]` to mock implementations and exercise the full `Provider`
interface in-process. These tests verify that the internal services collaborate correctly.

**Test file:** `plugin-sdk/pkg/resource/controller_test.go`

### 5.1 Setup Pattern

```go
func newTestProvider(t *testing.T, opts ...testProviderOption) resource.Provider {
    cfg := defaultTestConfig()  // sensible defaults
    for _, opt := range opts {
        opt(&cfg)
    }
    provider, err := sdk.BuildResourceController(cfg)
    require.NoError(t, err)
    return provider
}

// Test config with simple string client
func defaultTestConfig() sdk.ResourcePluginConfig[string] {
    return sdk.ResourcePluginConfig[string]{
        Connections: &stubConnectionProvider[string]{
            createFunc: func(ctx context.Context) (*string, error) {
                client := "test-client"
                return &client, nil
            },
            destroyFunc: func(ctx context.Context, client *string) error {
                return nil
            },
            loadFunc: func(ctx context.Context) ([]types.Connection, error) {
                return []types.Connection{{ID: "conn-1", Name: "Test Connection"}}, nil
            },
            checkFunc: func(ctx context.Context, conn *types.Connection, client *string) (types.ConnectionStatus, error) {
                return types.ConnectionStatus{Status: types.Connected}, nil
            },
            namespacesFunc: func(ctx context.Context, client *string) ([]string, error) {
                return []string{"default", "kube-system"}, nil
            },
        },
        Resources: []sdk.ResourceRegistration[string]{
            {
                Meta:      podMeta,
                Resourcer: &watchablePodResourcer{},
            },
        },
        DefaultDefinition: defaultDef,
    }
}
```

### 5.2 CRUD Flow Tests

| Test | What it verifies |
|------|-----------------|
| `TestProvider_Get` | Get → resolves resourcer → calls `Resourcer.Get` with correct client |
| `TestProvider_List` | List → resolves resourcer → calls `Resourcer.List` |
| `TestProvider_Find` | Find → resolves resourcer → calls `Resourcer.Find` |
| `TestProvider_Create` | Create → resolves resourcer → calls `Resourcer.Create` |
| `TestProvider_Update` | Update → resolves resourcer → calls `Resourcer.Update` |
| `TestProvider_Delete` | Delete → resolves resourcer → calls `Resourcer.Delete` |
| `TestProvider_CRUD_UnknownResource` | Returns error for unregistered resource key |
| `TestProvider_CRUD_PatternFallback` | Falls back to `"*"` pattern resourcer |
| `TestProvider_CRUD_ConnectionNotStarted` | Returns error if connection not started |
| `TestProvider_CRUD_ContextCancelled` | Cancelled context propagates to resourcer |
| `TestProvider_CRUD_ErrorClassification` | ErrorClassifier wraps raw errors |

### 5.3 Connection Lifecycle Tests

| Test | What it verifies |
|------|-----------------|
| `TestProvider_LoadConnections` | Returns connections from ConnectionProvider |
| `TestProvider_StartConnection` | Creates client, starts SyncOnConnect watches |
| `TestProvider_StopConnection` | Destroys client, stops all watches for connection |
| `TestProvider_StartConnection_Twice` | Second start is idempotent |
| `TestProvider_ListConnections` | Returns runtime state of all connections |
| `TestProvider_GetConnection` | Returns specific connection by ID |
| `TestProvider_GetConnectionNamespaces` | Returns namespaces for connection |
| `TestProvider_UpdateConnection` | Updates connection, optionally restarts client |
| `TestProvider_DeleteConnection` | Stops connection, removes from state |
| `TestProvider_WatchConnections` | Connection changes stream to caller |

### 5.4 Watch Lifecycle Tests (via Provider)

| Test | What it verifies |
|------|-----------------|
| `TestProvider_StartConnectionWatch` | Starts Watch goroutines for watchable resources |
| `TestProvider_StopConnectionWatch` | Stops all Watch goroutines for connection |
| `TestProvider_HasWatch` | Returns true when watches are active |
| `TestProvider_GetWatchState` | Returns per-resource sync state summary |
| `TestProvider_ListenForEvents` | Events from Watch reach the WatchEventSink |
| `TestProvider_EnsureResourceWatch` | Lazily starts Watch for a specific resource |
| `TestProvider_StopResourceWatch` | Stops specific resource's Watch |
| `TestProvider_RestartResourceWatch` | Restarts specific resource's Watch |
| `TestProvider_IsResourceWatchRunning` | Returns correct running state |
| `TestProvider_WatchErrorRecovery` | Watch failure → backoff → restart |
| `TestProvider_ConnectionDisconnect_StopsWatches` | StopConnection → all watches stop |

### 5.5 Full Lifecycle Scenarios

These are end-to-end scenarios exercising multiple Provider methods in sequence.

| Test | Scenario |
|------|----------|
| `TestScenario_ConnectAndBrowse` | LoadConnections → StartConnection → List → Get (verifies CRUD works after connection) |
| `TestScenario_WatchAndReceiveEvents` | StartConnection → ListenForEvents → verify adds/updates/deletes arrive |
| `TestScenario_LazyWatch` | StartConnection (SyncOnFirstQuery) → List (triggers EnsureResourceWatch) → events flow |
| `TestScenario_DisconnectReconnect` | StartConnection → StopConnection → StartConnection → watches restart |
| `TestScenario_MultipleConnections` | Start 3 connections → events from each are distinguished |
| `TestScenario_StopDuringSync` | StartConnection → StopConnection before sync completes → clean teardown |
| `TestScenario_CRUDDuringWatch` | Watch running + concurrent CRUD operations → no interference |

### 5.6 Type & Definition Tests

| Test | What it verifies |
|------|-----------------|
| `TestProvider_GetResourceTypes` | Returns merged static + discovered types |
| `TestProvider_GetResourceGroups` | Returns groups |
| `TestProvider_HasResourceType` | Returns true for known types |
| `TestProvider_GetResourceDefinition` | Returns definition with correct precedence |
| `TestProvider_Discovery` | DiscoveryProvider called on connection start |

### 5.7 Action Tests

| Test | What it verifies |
|------|-----------------|
| `TestProvider_GetActions` | Detects `ActionResourcer`, returns actions |
| `TestProvider_GetActions_NoActions` | Resourcer without `ActionResourcer` → empty list |
| `TestProvider_ExecuteAction` | Delegates to `ActionResourcer.ExecuteAction` |
| `TestProvider_StreamAction` | Delegates to `ActionResourcer.StreamAction` |

### 5.8 Schema Tests

| Test | What it verifies |
|------|-----------------|
| `TestProvider_GetEditorSchemas` | Detects `SchemaProvider` on ConnectionProvider |
| `TestProvider_GetEditorSchemas_NotSupported` | No SchemaProvider → empty list |

---

## 6. Level 3: Engine Integration Tests

Test the engine controller → SDK controller path using `InProcessBackend`. This verifies
the full data flow without gRPC or process spawning.

**Test file:** `backend/pkg/plugin/resource/controller_integration_test.go`

### 6.1 Setup Pattern

```go
func newIntegrationTest(t *testing.T) (*controller, resource.Provider) {
    // 1. Build SDK provider with mock implementations
    sdkProvider := newTestProvider(t)

    // 2. Create engine controller
    engineCtrl := NewController(zap.NewNop().Sugar(), nil).(*controller)
    engineCtrl.Run(context.Background())

    // 3. Wire via InProcessBackend
    backend := plugintypes.NewInProcessBackend(map[string]interface{}{
        "resource": sdkProvider,
    })
    engineCtrl.OnPluginStart("test-plugin", testMeta, backend)

    t.Cleanup(func() {
        engineCtrl.OnPluginStop("test-plugin")
    })

    return engineCtrl, sdkProvider
}
```

### 6.2 Tests

| Test | What it verifies |
|------|-----------------|
| `TestIntegration_Dispense` | InProcessBackend dispenses SDK Provider correctly |
| `TestIntegration_LoadConnections` | Engine → SDK → ConnectionProvider.LoadConnections |
| `TestIntegration_StartConnection` | Engine → SDK → CreateClient + start watches |
| `TestIntegration_CRUD` | Engine → SDK → Resourcer.Get/List/Create/Update/Delete |
| `TestIntegration_WatchEvents` | Engine subscribes → SDK emits → engine receives events |
| `TestIntegration_Subscriptions` | SubscribeResource gates event delivery |
| `TestIntegration_Unsubscribe` | UnsubscribeResource stops event delivery |
| `TestIntegration_ConnectionDisconnect` | StopConnection → watches stop → events stop |
| `TestIntegration_PluginStop` | OnPluginStop cleans up all state |
| `TestIntegration_MultiPlugin` | Two plugins registered, events don't cross |

### 6.3 What This Level Does NOT Test

- gRPC serialization/deserialization (Level 4)
- Wails event emission (manual/E2E testing)
- Frontend subscription hooks (frontend tests)
- Process spawning and crash recovery (existing tests cover this)

---

## 7. Level 4: gRPC Conformance Tests

Verify that the gRPC proto definitions correctly round-trip with the Go interfaces. This is
the only level that imports gRPC packages.

**Test file:** `plugin-sdk/pkg/resource/plugin/resource_grpc_test.go`

### 7.1 Approach

Start a real gRPC server (in-process, no TCP) with the SDK's `ResourcePluginServer` wired to
a mock `Provider`. Connect a `ResourcePluginClient`. Verify round-trip.

```go
func newGRPCTest(t *testing.T, provider resource.Provider) resource.Provider {
    // 1. Create in-memory gRPC connection (bufconn)
    lis := bufconn.Listen(1024 * 1024)
    srv := grpc.NewServer()
    proto.RegisterResourcePluginServer(srv, NewResourcePluginServer(provider))
    go srv.Serve(lis)

    // 2. Connect client
    conn, _ := grpc.DialContext(ctx, "bufnet",
        grpc.WithContextDialer(func(ctx context.Context, s string) (net.Conn, error) {
            return lis.Dial()
        }),
        grpc.WithInsecure(),
    )
    client := NewResourcePluginClient(conn)

    t.Cleanup(func() { srv.Stop(); conn.Close() })
    return client  // Same Provider interface — tests are symmetric
}
```

### 7.2 Tests

| Test | What it verifies |
|------|-----------------|
| `TestGRPC_Get_RoundTrip` | Get request → proto → response → Go types |
| `TestGRPC_List_RoundTrip` | List with namespace, labels, limit |
| `TestGRPC_Create_RoundTrip` | Create with data payload |
| `TestGRPC_Update_RoundTrip` | Update with data payload |
| `TestGRPC_Delete_RoundTrip` | Delete by ID |
| `TestGRPC_StartConnection` | Connection lifecycle over gRPC |
| `TestGRPC_ListenForEvents_Streaming` | Server-streaming RPC delivers events |
| `TestGRPC_WatchConnections_Streaming` | Server-streaming RPC for connection changes |
| `TestGRPC_StreamAction_Streaming` | Server-streaming RPC for action progress |
| `TestGRPC_GetResourceTypes` | Metadata round-trip |
| `TestGRPC_ContextCancellation` | Client cancels → server ctx cancelled |
| `TestGRPC_Deadline` | Client deadline propagated to server |
| `TestGRPC_LargePayload` | List with 1000+ items serializes correctly |
| `TestGRPC_ErrorPropagation` | SDK errors → gRPC status codes → Go errors |
| `TestGRPC_WatchState_RoundTrip` | WatchConnectionSummary serialization |

### 7.3 Proto Changes Required

The current `resource.proto` will need updates for the new interface. These tests verify
the new proto works correctly. Key changes:

- `ListenForEvents` → server-streaming RPC returning `WatchEvent` messages
- `StreamAction` → server-streaming RPC returning `ActionEvent` messages
- `WatchConnections` → server-streaming RPC returning `ConnectionChangeEvent` messages
- New RPCs: `StartConnectionWatch`, `StopConnectionWatch`, `EnsureResourceWatch`, etc.
- Remove layout RPCs

---

## 8. Migration Gates

Migration proceeds in phases. Each phase has entry criteria (tests that must pass) and
deliverables.

### Phase 0: New SDK Implementation (No Migration Yet)

**Entry criteria:** None — this is the first phase.

**Deliverables:**
1. All new types defined (`Watcher`, `WatchEventSink`, `SyncPolicy`, `Session`, etc.)
2. All internal services implemented (`watchManager`, `connectionManager`, `resourcerRegistry`)
3. `resourceController[ClientT]` implements `Provider`
4. `resourcetest` package with test helpers
5. All Level 1 tests pass
6. All Level 2 tests pass

**Gate:** `go test ./plugin-sdk/pkg/resource/...` — zero failures, zero go-plugin imports.

### Phase 1: gRPC Layer

**Entry criteria:** Phase 0 gate passed.

**Deliverables:**
1. Updated proto definitions
2. Updated `ResourcePluginServer` (gRPC server stub)
3. Updated `ResourcePluginClient` (gRPC client stub)
4. All Level 4 tests pass

**Gate:** `go test ./plugin-sdk/pkg/resource/plugin/...` — gRPC round-trip verified.

### Phase 2: Engine Integration

**Entry criteria:** Phase 1 gate passed.

**Deliverables:**
1. Engine controller updated to new `Provider` interface
2. `WatchEventSink` replaces 4 channels in engine controller
3. `WatchProvider` methods wired (StartConnectionWatch, etc.)
4. All Level 3 tests pass
5. Existing engine controller tests updated and passing

**Gate:** `go test ./backend/pkg/plugin/resource/...` — all engine tests pass.

### Phase 3: K8s Plugin Migration

**Entry criteria:** Phase 2 gate passed.

**Deliverables:**
1. `kubeConnectionProvider` struct (implements `ConnectionProvider`)
2. `KubeResourcerBase.Watch()` method (implements `Watcher`)
3. `ClientSet.EnsureFactoryStarted()` (idempotent shared factory)
4. Updated code generator templates
5. Regenerated `register_gen.go`
6. All existing K8s plugin tests updated and passing
7. New K8s Watch tests (verify Watch with fake K8s clients)

**Gate:** `go test ./plugins/kubernetes/...` — all K8s plugin tests pass.

### Phase 4: Cleanup

**Entry criteria:** Phase 3 gate passed.

**Deliverables:**
1. Delete old types: `ResourcePluginOpts`, `IResourcePluginOpts`, `PluginContext`
2. Delete old services: `InformerManager`, `LayoutManager`
3. Delete dead code: `hook.go` (PreHook/PostHook)
4. Delete old registration: `RegisterResourcePlugin` (old version)
5. Full build passes: `go build ./...`
6. Full test suite passes: `go test ./...`

**Gate:** Clean build + all tests green across entire repo.

### Phase 5: Manual Verification

**Entry criteria:** Phase 4 gate passed.

**Deliverables:** All verification items from doc 09 §9:

| V# | Test | How |
|----|------|-----|
| V1 | K8s plugin loads and connects | Manual: click connect, verify resources visible |
| V2 | CRUD operations work | Manual: get, list, create, update, delete pods |
| V3 | Live updates work | Manual: create/delete pod while viewing |
| V4 | Sync policies work | Manual: SyncOnConnect fast, SyncOnFirstQuery lazy |
| V5 | Actions work | Manual: restart deployment, drain node |
| V6 | Streaming actions work | Manual: restart with progress |
| V7 | Connection watch works | Manual: modify kubeconfig |
| V8 | Plugin crash recovery | Manual: kill plugin process |
| V9 | Context cancellation | Manual: navigate away during long List |
| V10 | Performance invariants | Profile: doc 08 invariants |

---

## 9. Test Helpers Package

**Location:** `plugin-sdk/pkg/resource/resourcetest/`

This package ships with the SDK for plugin-author testing. It is also used by the SDK's
own Level 1 and Level 2 tests.

### 9.1 Files

```
resourcetest/
    connection_provider.go   // Configurable stub ConnectionProvider[string]
    resourcer.go             // Configurable stub Resourcer[string] with optional Watcher
    recording_sink.go        // WatchEventSink that records all events
    session.go               // Helpers to create Session + context
    meta.go                  // Common ResourceMeta constants for tests
    assertions.go            // Custom test assertions (e.g., AssertEventuallyAdds)
```

### 9.2 Design

All test doubles use **function fields** for configurability. Every method has a sensible
default (return zero value + nil error). Tests override only what they need.

```go
// ConnectionProvider with function fields
type StubConnectionProvider struct {
    CreateClientFunc     func(ctx context.Context) (*string, error)
    DestroyClientFunc    func(ctx context.Context, client *string) error
    LoadConnectionsFunc  func(ctx context.Context) ([]types.Connection, error)
    CheckConnectionFunc  func(ctx context.Context, conn *types.Connection, client *string) (types.ConnectionStatus, error)
    GetNamespacesFunc    func(ctx context.Context, client *string) ([]string, error)
}

func (s *StubConnectionProvider) CreateClient(ctx context.Context) (*string, error) {
    if s.CreateClientFunc != nil {
        return s.CreateClientFunc(ctx)
    }
    client := "default-client"
    return &client, nil
}
// ... other methods follow the same pattern

// Resourcer with optional Watcher support
type StubResourcer struct {
    GetFunc    func(ctx context.Context, client *string, meta ResourceMeta, input GetInput) (*GetResult, error)
    ListFunc   func(ctx context.Context, client *string, meta ResourceMeta, input ListInput) (*ListResult, error)
    // ... other CRUD funcs
    WatchFunc  func(ctx context.Context, client *string, meta ResourceMeta, sink WatchEventSink) error
    SyncPolicyVal *SyncPolicy  // if set, implements SyncPolicyDeclarer
}

// Satisfies Resourcer
func (s *StubResourcer) Get(...) (*GetResult, error) { ... }
func (s *StubResourcer) List(...) (*ListResult, error) { ... }

// Conditionally satisfies Watcher (via wrapper)
func (s *StubResourcer) AsWatchable() *WatchableResourcer {
    return &WatchableResourcer{StubResourcer: s}
}
type WatchableResourcer struct {
    *StubResourcer
}
func (w *WatchableResourcer) Watch(ctx context.Context, client *string, meta ResourceMeta, sink WatchEventSink) error {
    if w.WatchFunc != nil {
        return w.WatchFunc(ctx, client, meta, sink)
    }
    <-ctx.Done()
    return nil
}

// Recording sink with assertion helpers
type RecordingSink struct {
    mu      sync.Mutex
    Adds    []WatchAddPayload
    Updates []WatchUpdatePayload
    Deletes []WatchDeletePayload
    States  []WatchStateEvent
}

func (s *RecordingSink) WaitForAdds(t *testing.T, count int, timeout time.Duration) {
    t.Helper()
    assert.Eventually(t, func() bool {
        s.mu.Lock()
        defer s.mu.Unlock()
        return len(s.Adds) >= count
    }, timeout, 10*time.Millisecond, "expected %d adds, got %d", count, len(s.Adds))
}

func (s *RecordingSink) WaitForState(t *testing.T, resourceKey string, state WatchState, timeout time.Duration) {
    t.Helper()
    assert.Eventually(t, func() bool {
        s.mu.Lock()
        defer s.mu.Unlock()
        for _, e := range s.States {
            if e.ResourceKey == resourceKey && e.State == state {
                return true
            }
        }
        return false
    }, timeout, 10*time.Millisecond)
}

// Session helpers
func NewTestSession(connectionID string) *Session {
    return &Session{
        Connection:  &types.Connection{ID: connectionID, Name: "test-" + connectionID},
        RequestID:   uuid.NewString(),
        RequesterID: "test",
    }
}

func NewTestContext(connectionID string) context.Context {
    return WithSession(context.Background(), NewTestSession(connectionID))
}
```

### 9.3 Why Not Generated Mocks

1. **Function fields are more readable.** `mock.GetFunc = func(...) { ... }` reads better
   than `mock.EXPECT().Get(gomock.Any(), ...).Return(...)`.
2. **No build dependency.** No `go generate` step, no `mockgen` binary.
3. **Matches existing patterns.** The K8s plugin uses `fake.NewSimpleClientset()`,
   the engine uses `InProcessBackend`. Hand-written stubs fit.
4. **Type-safe at compile time.** Stubs implement the interface directly. Mismatch = compile error.

---

## 10. Coverage Targets

| Package | Target | Rationale |
|---------|--------|-----------|
| `resource/services/resourcer_registry.go` | 95%+ | Core lookup logic, must be bulletproof |
| `resource/services/connection_manager.go` | 90%+ | Connection lifecycle, includes concurrency |
| `resource/services/watch_manager.go` | 95%+ | Most complex component, owns goroutine lifecycle |
| `resource/services/type_manager.go` | 85%+ | Metadata aggregation |
| `resource/controller.go` | 90%+ | Provider implementation, wires everything |
| `resource/types/session.go` | 100% | Simple context helpers, easy to cover |
| `resource/plugin/resource_server.go` | 80%+ | gRPC translation (Level 4) |
| `resource/plugin/resource_client.go` | 80%+ | gRPC translation (Level 4) |
| `resource/resourcetest/` | N/A | Test helpers, not production code |

**How to measure:**
```bash
# SDK tests (Levels 1 + 2)
go test -coverprofile=cover.out ./plugin-sdk/pkg/resource/...
go tool cover -func=cover.out

# Engine integration (Level 3)
go test -coverprofile=cover.out ./backend/pkg/plugin/resource/...
go tool cover -func=cover.out

# Race detector (critical for watchManager)
go test -race ./plugin-sdk/pkg/resource/...
```

---

## 11. Test Execution Order

```bash
# 1. Compile check — no go-plugin imports in SDK test files
go vet ./plugin-sdk/pkg/resource/...

# 2. Level 1 — Unit tests
go test -v -race ./plugin-sdk/pkg/resource/types/...
go test -v -race ./plugin-sdk/pkg/resource/services/...

# 3. Level 2 — SDK integration
go test -v -race ./plugin-sdk/pkg/resource/...

# 4. Level 4 — gRPC conformance
go test -v ./plugin-sdk/pkg/resource/plugin/...

# 5. Level 3 — Engine integration
go test -v -race ./backend/pkg/plugin/resource/...

# 6. Full suite
go test ./...
```

---

## 12. What This Testing Strategy Does NOT Cover

These are tested by other means:

| Concern | How Tested |
|---------|------------|
| Frontend subscription hooks (`useResources.ts`) | Frontend unit tests (vitest/jest) |
| Wails event emission | Manual E2E testing (Phase 5) |
| Actual plugin process spawning | Existing `loader_test.go` tests |
| Plugin crash recovery (process death) | Existing `controller_crash_test.go` tests |
| K8s API server behavior | K8s fake clients (existing pattern) |
| Performance under load | Profiling during Phase 5 (doc 08 invariants) |
| UI rendering of resources | Manual testing + frontend tests |
