# 11: Exhaustive Test Specification — TDD Implementation Guide

## 1. Purpose

This document is the complete test specification for the resource plugin SDK refactor (doc 09).
Every test case is enumerated with setup, action, and assertion — no gaps, no ambiguity. The
implementation follows strict TDD (red-green-refactor):

1. **Scaffold** — define all interfaces, types, and empty struct implementations (compiles, zero functionality)
2. **Test infrastructure** — build helpers, stubs, recording sinks
3. **Red** — write every test in this document. All fail (methods return zero values or panic).
4. **Green** — implement each component until its tests pass. One component at a time.
5. **Refactor** — clean up without breaking tests.

No consumer migration begins until every test in this document is green.

---

## 2. TDD Implementation Workflow

### 2.1 Phase 0: Scaffold (Day 1)

Define all interfaces and types. Empty struct implementations that compile but do nothing.

**Files created:**
```
plugin-sdk/pkg/resource/types/
    session.go          — Session struct, WithSession, SessionFromContext, ConnectionFromContext
    resourcer.go        — Resourcer[ClientT], Watcher[ClientT], SyncPolicyDeclarer, etc.
    watcher.go          — WatchEventSink, WatchAddPayload, WatchUpdatePayload, etc.
    connection.go       — ConnectionProvider[ClientT], ConnectionWatcher, ClientRefresher, SchemaProvider
    discovery.go        — DiscoveryProvider
    provider.go         — Provider, OperationProvider, ConnectionLifecycleProvider, WatchProvider, etc.

plugin-sdk/pkg/sdk/
    resource_config.go  — ResourcePluginConfig[ClientT], ResourceRegistration[ClientT], RegisterResourcePlugin

plugin-sdk/pkg/resource/services/
    resourcer_registry.go   — stub: type resourcerRegistry[ClientT any] struct{}
    connection_manager.go   — stub: type connectionManager[ClientT any] struct{}
    watch_manager.go        — stub: type watchManager[ClientT any] struct{}
    type_manager.go         — stub: type typeManager struct{}

plugin-sdk/pkg/resource/
    controller.go           — stub: type resourceController[ClientT any] struct{}
                              satisfies Provider interface with zero-value returns
```

**Gate:** `go build ./plugin-sdk/...` passes. `go vet ./plugin-sdk/...` passes.
No logic — just types, interfaces, and method signatures.

### 2.2 Phase 1: Test Infrastructure (Day 1-2)

Build all test helpers BEFORE writing any tests. These are the foundation.

**Files created:**
```
plugin-sdk/pkg/resource/resourcetest/
    connection_provider.go   — StubConnectionProvider[ClientT]
    resourcer.go             — StubResourcer[ClientT], WatchableResourcer[ClientT]
    recording_sink.go        — RecordingSink with assertion helpers
    session.go               — NewTestSession, NewTestContext
    meta.go                  — PodMeta, DeploymentMeta, ServiceMeta, SecretMeta constants
    assertions.go            — AssertEventuallyAdds, AssertEventuallyState, etc.
    options.go               — TestProviderOption, WithResources, WithPatterns, etc.
    provider_builder.go      — NewTestProvider(t, ...opts) → Provider
```

**Gate:** `go build ./plugin-sdk/pkg/resource/resourcetest/...` passes.

### 2.3 Phase 2: Red — Write All Tests (Day 2-4)

Write every test in this document. Every test fails. This is intentional.

**Gate:** `go test ./plugin-sdk/pkg/resource/... 2>&1 | grep -c FAIL` = 331 (total test count).
Every test compiles. Every test fails. Zero panics (stubs return zero values, not panics).

### 2.4 Phase 3: Green — TDD Each Component (Day 4-12)

Implement one component at a time, in dependency order:

```
1. Session & context helpers         (trivial, no deps)
2. resourcerRegistry[ClientT]        (no deps on other services)
3. connectionManager[ClientT]        (depends on ConnectionProvider interface only)
4. watchManager[ClientT]             (depends on resourcerRegistry, connectionManager)
5. typeManager                       (depends on resourcerRegistry, DiscoveryProvider)
6. resourceController[ClientT]       (depends on all services above)
7. gRPC server/client stubs          (depends on controller)
8. Engine integration                (depends on everything)
```

For each component:
1. Run its tests → all red
2. Implement minimum code to pass the first test
3. Run tests → one green, rest red
4. Implement next test's requirement
5. Repeat until all green
6. Refactor if needed (tests must stay green)
7. Move to next component

### 2.5 Phase 4: Full Green

**Gate:** `go test -race ./plugin-sdk/pkg/resource/...` — zero failures, zero races.
**Gate:** `go test -race ./backend/pkg/plugin/resource/...` — zero failures (engine integration).
**Gate:** `go vet ./plugin-sdk/... ./backend/...` — zero issues.

Only after Phase 4 passes: begin K8s plugin migration and engine controller update.

---

## 3. Test Infrastructure Specification

### 3.1 StubConnectionProvider

```go
// StubConnectionProvider[ClientT] — configurable function fields, sensible defaults.
// Every method checks for a non-nil function field first, then falls back to default.
type StubConnectionProvider[ClientT any] struct {
    CreateClientFunc      func(ctx context.Context) (*ClientT, error)
    DestroyClientFunc     func(ctx context.Context, client *ClientT) error
    LoadConnectionsFunc   func(ctx context.Context) ([]types.Connection, error)
    CheckConnectionFunc   func(ctx context.Context, conn *types.Connection, client *ClientT) (types.ConnectionStatus, error)
    GetNamespacesFunc     func(ctx context.Context, client *ClientT) ([]string, error)

    // Track calls for assertions
    CreateClientCalls     int
    DestroyClientCalls    int
}

// Defaults:
//   CreateClient  → returns pointer to zero-value ClientT, nil error
//   DestroyClient → returns nil error
//   LoadConnections → returns []Connection{{ID: "conn-1", Name: "Test"}}, nil
//   CheckConnection → returns ConnectionStatus{Status: Connected}, nil
//   GetNamespaces → returns []string{"default"}, nil
```

### 3.2 StubConnectionWatcher (separate type, composes with StubConnectionProvider)

```go
type WatchingConnectionProvider[ClientT any] struct {
    StubConnectionProvider[ClientT]
    WatchConnectionsFunc func(ctx context.Context) (<-chan []types.Connection, error)
}
```

### 3.3 StubResourcer

```go
type StubResourcer[ClientT any] struct {
    GetFunc    func(ctx context.Context, client *ClientT, meta ResourceMeta, input GetInput) (*GetResult, error)
    ListFunc   func(ctx context.Context, client *ClientT, meta ResourceMeta, input ListInput) (*ListResult, error)
    FindFunc   func(ctx context.Context, client *ClientT, meta ResourceMeta, input FindInput) (*FindResult, error)
    CreateFunc func(ctx context.Context, client *ClientT, meta ResourceMeta, input CreateInput) (*CreateResult, error)
    UpdateFunc func(ctx context.Context, client *ClientT, meta ResourceMeta, input UpdateInput) (*UpdateResult, error)
    DeleteFunc func(ctx context.Context, client *ClientT, meta ResourceMeta, input DeleteInput) (*DeleteResult, error)

    // Track calls
    GetCalls, ListCalls, FindCalls, CreateCalls, UpdateCalls, DeleteCalls int
}

// Defaults: all return nil result, nil error
```

### 3.4 WatchableResourcer (Resourcer + Watcher)

```go
type WatchableResourcer[ClientT any] struct {
    StubResourcer[ClientT]
    WatchFunc     func(ctx context.Context, client *ClientT, meta ResourceMeta, sink WatchEventSink) error
    PolicyVal     *SyncPolicy       // if non-nil, implements SyncPolicyDeclarer
    DefinitionVal *ResourceDefinition // if non-nil, implements DefinitionProvider
}

// Satisfies: Resourcer[ClientT] + Watcher[ClientT]
// Optionally satisfies: SyncPolicyDeclarer, DefinitionProvider

// Default WatchFunc: blocks until ctx.Done(), returns nil
```

### 3.5 RecordingSink

```go
type RecordingSink struct {
    mu      sync.Mutex
    Adds    []WatchAddPayload
    Updates []WatchUpdatePayload
    Deletes []WatchDeletePayload
    States  []WatchStateEvent
    closed  bool
}

// Thread-safe methods:
func (s *RecordingSink) OnAdd(p WatchAddPayload)
func (s *RecordingSink) OnUpdate(p WatchUpdatePayload)
func (s *RecordingSink) OnDelete(p WatchDeletePayload)
func (s *RecordingSink) OnStateChange(e WatchStateEvent)

// Assertion helpers (all use t.Helper()):
func (s *RecordingSink) WaitForAdds(t *testing.T, count int, timeout time.Duration)
func (s *RecordingSink) WaitForUpdates(t *testing.T, count int, timeout time.Duration)
func (s *RecordingSink) WaitForDeletes(t *testing.T, count int, timeout time.Duration)
func (s *RecordingSink) WaitForState(t *testing.T, resourceKey string, state WatchState, timeout time.Duration)
func (s *RecordingSink) AddCount() int
func (s *RecordingSink) UpdateCount() int
func (s *RecordingSink) DeleteCount() int
func (s *RecordingSink) Reset()
```

### 3.6 Test Meta Constants

```go
var (
    PodMeta = ResourceMeta{
        Group: "core", Version: "v1", Kind: "Pod",
        Description: "A pod", Namespaced: true,
    }
    DeploymentMeta = ResourceMeta{
        Group: "apps", Version: "v1", Kind: "Deployment",
        Description: "A deployment", Namespaced: true,
    }
    ServiceMeta = ResourceMeta{
        Group: "core", Version: "v1", Kind: "Service",
        Description: "A service", Namespaced: true,
    }
    SecretMeta = ResourceMeta{
        Group: "core", Version: "v1", Kind: "Secret",
        Description: "A secret", Namespaced: true,
    }
    NodeMeta = ResourceMeta{
        Group: "core", Version: "v1", Kind: "Node",
        Description: "A node", Namespaced: false,
    }
)
```

### 3.7 TestProviderBuilder

```go
// Builds a fully wired resourceController[string] as a Provider for integration tests.
func NewTestProvider(t *testing.T, opts ...TestProviderOption) Provider {
    cfg := DefaultTestConfig()
    for _, opt := range opts { opt(&cfg) }
    provider, err := sdk.BuildResourceController(cfg)
    require.NoError(t, err)
    t.Cleanup(func() { provider.Close() })
    return provider
}

func DefaultTestConfig() sdk.ResourcePluginConfig[string] { ... }

// Options:
type TestProviderOption func(*sdk.ResourcePluginConfig[string])

func WithResources(regs ...sdk.ResourceRegistration[string]) TestProviderOption
func WithPatterns(patterns map[string]resource.Resourcer[string]) TestProviderOption
func WithConnectionProvider(cp resource.ConnectionProvider[string]) TestProviderOption
func WithDiscovery(dp resource.DiscoveryProvider) TestProviderOption
func WithErrorClassifier(ec resource.ErrorClassifier) TestProviderOption
func WithGroups(groups ...resource.ResourceGroup) TestProviderOption
func WithDefaultDefinition(def resource.ResourceDefinition) TestProviderOption
```

---

## 4. Test Specification: Session & Context Helpers

**File:** `plugin-sdk/pkg/resource/types/session_test.go`
**Component:** `Session`, `WithSession`, `SessionFromContext`, `ConnectionFromContext`

---

### SR-001: WithSession → SessionFromContext round-trip

**Setup:** Create `Session{Connection: &Connection{ID: "c1"}, RequestID: "r1", RequesterID: "u1"}`
**Action:** `ctx := WithSession(context.Background(), session)` → `got := SessionFromContext(ctx)`
**Assert:**
- `got` is not nil
- `got.Connection.ID == "c1"`
- `got.RequestID == "r1"`
- `got.RequesterID == "u1"`
- `got == session` (same pointer)

### SR-002: SessionFromContext with no session returns nil

**Setup:** Plain `context.Background()`
**Action:** `got := SessionFromContext(ctx)`
**Assert:** `got == nil`

### SR-003: ConnectionFromContext returns session's connection

**Setup:** Session with `Connection: &Connection{ID: "c1"}`
**Action:** `ctx := WithSession(ctx, session)` → `conn := ConnectionFromContext(ctx)`
**Assert:** `conn.ID == "c1"`

### SR-004: ConnectionFromContext with no session returns nil

**Setup:** Plain `context.Background()`
**Action:** `conn := ConnectionFromContext(ctx)`
**Assert:** `conn == nil`

### SR-005: Session survives context wrapping

**Setup:** `ctx := WithSession(ctx, session)` → `ctx = context.WithValue(ctx, someOtherKey, "val")`
**Action:** `got := SessionFromContext(ctx)`
**Assert:** `got == session`

### SR-006: Session with cancelled context

**Setup:** `ctx, cancel := context.WithCancel(ctx)` → `ctx = WithSession(ctx, session)` → `cancel()`
**Action:** `got := SessionFromContext(ctx)`
**Assert:** `got == session` (session is still retrievable even on cancelled ctx)

### SR-007: Session with nil connection

**Setup:** `Session{Connection: nil, RequestID: "r1"}`
**Action:** `ctx := WithSession(ctx, session)` → `conn := ConnectionFromContext(ctx)`
**Assert:** `conn == nil` (does not panic)

### SR-008: Overwriting session in nested context

**Setup:** `ctx1 := WithSession(ctx, session1)` → `ctx2 := WithSession(ctx1, session2)`
**Action:** `got1 := SessionFromContext(ctx1)` → `got2 := SessionFromContext(ctx2)`
**Assert:** `got1 == session1`, `got2 == session2`

---

## 5. Test Specification: resourcerRegistry

**File:** `plugin-sdk/pkg/resource/services/resourcer_registry_test.go`
**Component:** `resourcerRegistry[string]` (uses `string` as `ClientT` for simplicity)

All tests use `resourcetest.PodMeta`, `DeploymentMeta`, etc. as test fixtures.

---

### RR-001: Lookup exact match

**Setup:** Register `{Meta: PodMeta, Resourcer: stubPod}`
**Action:** `r, err := registry.Lookup("core::v1::Pod")`
**Assert:** `err == nil`, `r == stubPod`

### RR-002: Lookup returns correct resourcer among multiple

**Setup:** Register Pod, Deployment, Service resourcers
**Action:** `r, _ := registry.Lookup("apps::v1::Deployment")`
**Assert:** `r` is the Deployment resourcer (not Pod or Service)

### RR-003: Lookup with pattern fallback

**Setup:** Register pattern `"*": patternResourcer`, no Pod registration
**Action:** `r, err := registry.Lookup("core::v1::Pod")`
**Assert:** `err == nil`, `r == patternResourcer`

### RR-004: Lookup exact wins over pattern

**Setup:** Register `{Meta: PodMeta, Resourcer: exactPod}` AND pattern `"*": patternResourcer`
**Action:** `r, _ := registry.Lookup("core::v1::Pod")`
**Assert:** `r == exactPod` (not patternResourcer)

### RR-005: Lookup not found, no pattern

**Setup:** Register Pod only, no patterns
**Action:** `r, err := registry.Lookup("apps::v1::Deployment")`
**Assert:** `err != nil` (resource not found), `r == nil`

### RR-006: Lookup on empty registry

**Setup:** Empty registry (no registrations, no patterns)
**Action:** `r, err := registry.Lookup("core::v1::Pod")`
**Assert:** `err != nil`, `r == nil`

### RR-007: Detects Watcher capability

**Setup:** Register `{Meta: PodMeta, Resourcer: watchablePod}` where `watchablePod` implements `Watcher[string]`
**Action:** `isWatcher := registry.IsWatcher("core::v1::Pod")`
**Assert:** `isWatcher == true`

### RR-008: Non-Watcher detected correctly

**Setup:** Register `{Meta: PodMeta, Resourcer: stubPod}` (no Watcher)
**Action:** `isWatcher := registry.IsWatcher("core::v1::Pod")`
**Assert:** `isWatcher == false`

### RR-009: Detects SyncPolicyDeclarer — custom policy

**Setup:** Register watchable Pod with `SyncPolicy() == SyncOnFirstQuery`
**Action:** `policy := registry.GetSyncPolicy("core::v1::Pod")`
**Assert:** `policy == SyncOnFirstQuery`

### RR-010: SyncPolicyDeclarer absent — defaults to SyncOnConnect

**Setup:** Register watchable Pod WITHOUT SyncPolicyDeclarer
**Action:** `policy := registry.GetSyncPolicy("core::v1::Pod")`
**Assert:** `policy == SyncOnConnect`

### RR-011: SyncPolicy for non-Watcher — returns SyncNever

**Setup:** Register stub Pod (no Watcher, no SyncPolicyDeclarer)
**Action:** `policy := registry.GetSyncPolicy("core::v1::Pod")`
**Assert:** `policy == SyncNever`

### RR-012: Detects DefinitionProvider — interface wins over registration

**Setup:** Register Pod with `Registration.Definition = &defA` AND resourcer implements `DefinitionProvider` returning `defB`
**Action:** `def := registry.GetDefinition("core::v1::Pod")`
**Assert:** `def == defB` (DefinitionProvider wins)

### RR-013: Definition fallback to Registration.Definition

**Setup:** Register Pod with `Registration.Definition = &defA`, resourcer does NOT implement DefinitionProvider
**Action:** `def := registry.GetDefinition("core::v1::Pod")`
**Assert:** `def == defA`

### RR-014: Definition fallback to DefaultDefinition

**Setup:** Register Pod with `Registration.Definition = nil`, no DefinitionProvider, DefaultDefinition = `defDefault`
**Action:** `def := registry.GetDefinition("core::v1::Pod")`
**Assert:** `def == defDefault`

### RR-015: Detects ActionResourcer

**Setup:** Register Pod where resourcer implements `ActionResourcer[string]`
**Action:** `ar, ok := registry.GetActionResourcer("core::v1::Pod")`
**Assert:** `ok == true`, `ar` is not nil

### RR-016: No ActionResourcer

**Setup:** Register Pod with plain stub (no ActionResourcer)
**Action:** `ar, ok := registry.GetActionResourcer("core::v1::Pod")`
**Assert:** `ok == false`, `ar == nil`

### RR-017: Detects ErrorClassifier — per-resource

**Setup:** Register Pod where resourcer implements `ErrorClassifier`
**Action:** `ec, ok := registry.GetErrorClassifier("core::v1::Pod")`
**Assert:** `ok == true`

### RR-018: No per-resource ErrorClassifier

**Setup:** Register Pod with plain stub
**Action:** `ec, ok := registry.GetErrorClassifier("core::v1::Pod")`
**Assert:** `ok == false`

### RR-019: Detects SchemaResourcer

**Setup:** Register Pod where resourcer implements `SchemaResourcer[string]`
**Action:** `sr, ok := registry.GetSchemaResourcer("core::v1::Pod")`
**Assert:** `ok == true`

### RR-020: ListAll returns all registered metas

**Setup:** Register Pod, Deployment, Service
**Action:** `metas := registry.ListAll()`
**Assert:** len == 3, contains PodMeta, DeploymentMeta, ServiceMeta

### RR-021: ListWatchable returns only Watcher-capable metas

**Setup:** Register Pod (watchable), Deployment (watchable), Service (NOT watchable)
**Action:** `metas := registry.ListWatchable()`
**Assert:** len == 2, contains PodMeta and DeploymentMeta, NOT ServiceMeta

### RR-022: GetWatcher returns the Watcher interface

**Setup:** Register watchable Pod
**Action:** `w, ok := registry.GetWatcher("core::v1::Pod")`
**Assert:** `ok == true`, `w` is the same resourcer cast to `Watcher[string]`

### RR-023: GetWatcher for non-Watcher returns false

**Setup:** Register stub Pod
**Action:** `w, ok := registry.GetWatcher("core::v1::Pod")`
**Assert:** `ok == false`, `w == nil`

### RR-024: Pattern resourcer with named pattern

**Setup:** Register patterns: `"extensions::*::*": extensionsResourcer`, `"*": catchAllResourcer`
**Action:** `r1, _ := registry.Lookup("extensions::v1beta1::Ingress")`; `r2, _ := registry.Lookup("core::v1::ConfigMap")`
**Assert:** `r1 == extensionsResourcer`, `r2 == catchAllResourcer`

### RR-025: Duplicate registration for same key overwrites

**Setup:** Register Pod with `resourcerA`, then register Pod again with `resourcerB`
**Action:** `r, _ := registry.Lookup("core::v1::Pod")`
**Assert:** `r == resourcerB`

---

## 6. Test Specification: connectionManager

**File:** `plugin-sdk/pkg/resource/services/connection_manager_test.go`
**Component:** `connectionManager[string]`

---

### CM-001: LoadConnections — delegates to ConnectionProvider

**Setup:** StubConnectionProvider with `LoadConnectionsFunc` returning 3 connections
**Action:** `conns, err := mgr.LoadConnections(ctx)`
**Assert:** `err == nil`, `len(conns) == 3`

### CM-002: LoadConnections — provider error propagates

**Setup:** StubConnectionProvider with `LoadConnectionsFunc` returning error
**Action:** `conns, err := mgr.LoadConnections(ctx)`
**Assert:** `err != nil`, `conns == nil`

### CM-003: StartConnection — creates client and stores it

**Setup:** StubConnectionProvider with `CreateClientFunc` returning `"client-1"`
**Action:** `status, err := mgr.StartConnection(ctx, "conn-1")`
**Assert:** `err == nil`, `status.Status == Connected`, `CreateClientCalls == 1`

### CM-004: StartConnection — already started returns existing status

**Setup:** Start "conn-1" successfully
**Action:** `status, err := mgr.StartConnection(ctx, "conn-1")` (second call)
**Assert:** `err == nil`, `CreateClientCalls == 1` (NOT 2 — no re-creation)

### CM-005: StartConnection — CreateClient fails

**Setup:** StubConnectionProvider with `CreateClientFunc` returning error
**Action:** `status, err := mgr.StartConnection(ctx, "conn-1")`
**Assert:** `err != nil`, no client stored

### CM-006: StartConnection — unknown connection ID

**Setup:** LoadConnections returns `[{ID: "conn-1"}]`
**Action:** `status, err := mgr.StartConnection(ctx, "conn-unknown")`
**Assert:** `err != nil` (connection not found)

### CM-007: StopConnection — destroys client

**Setup:** Start "conn-1" successfully
**Action:** `conn, err := mgr.StopConnection(ctx, "conn-1")`
**Assert:** `err == nil`, `DestroyClientCalls == 1`

### CM-008: StopConnection — not started returns error

**Setup:** LoadConnections returns "conn-1", but never started
**Action:** `conn, err := mgr.StopConnection(ctx, "conn-1")`
**Assert:** `err != nil`

### CM-009: StopConnection — DestroyClient fails, still removes client

**Setup:** Start "conn-1", StubConnectionProvider.DestroyClientFunc returns error
**Action:** `conn, err := mgr.StopConnection(ctx, "conn-1")`
**Assert:** `err != nil` BUT client is removed from state (best-effort cleanup)

### CM-010: GetClient — active connection

**Setup:** Start "conn-1"
**Action:** `client, err := mgr.GetClient("conn-1")`
**Assert:** `err == nil`, `*client == "client-1"`

### CM-011: GetClient — connection not started

**Setup:** No connections started
**Action:** `client, err := mgr.GetClient("conn-1")`
**Assert:** `err != nil`, `client == nil`

### CM-012: GetNamespaces — delegates to ConnectionProvider

**Setup:** Start "conn-1", `GetNamespacesFunc` returns `["default", "kube-system"]`
**Action:** `ns, err := mgr.GetNamespaces(ctx, "conn-1")`
**Assert:** `err == nil`, `ns == ["default", "kube-system"]`

### CM-013: GetNamespaces — connection not started

**Setup:** No connections started
**Action:** `ns, err := mgr.GetNamespaces(ctx, "conn-1")`
**Assert:** `err != nil`

### CM-014: CheckConnection — delegates to ConnectionProvider

**Setup:** Start "conn-1", `CheckConnectionFunc` returns `{Status: Connected}`
**Action:** `status, err := mgr.CheckConnection(ctx, "conn-1")`
**Assert:** `err == nil`, `status.Status == Connected`

### CM-015: ListConnections — returns all with runtime state

**Setup:** LoadConnections returns 3 connections, start 2 of them
**Action:** `conns, err := mgr.ListConnections(ctx)`
**Assert:** `len(conns) == 3`, 2 have `Status: Connected`, 1 has `Status: Disconnected`

### CM-016: UpdateConnection — updates stored connection

**Setup:** Load and start "conn-1", then update with new data
**Action:** `updated, err := mgr.UpdateConnection(ctx, Connection{ID: "conn-1", Name: "Updated"})`
**Assert:** `err == nil`, `GetConnection("conn-1").Name == "Updated"`

### CM-017: UpdateConnection — restarts client if connection data changed

**Setup:** Start "conn-1", update with changed connection data
**Action:** `mgr.UpdateConnection(ctx, updatedConn)`
**Assert:** `DestroyClientCalls == 1`, `CreateClientCalls == 2` (old destroyed, new created)

### CM-018: DeleteConnection — stops if running, removes from state

**Setup:** Start "conn-1"
**Action:** `err := mgr.DeleteConnection(ctx, "conn-1")`
**Assert:** `err == nil`, `DestroyClientCalls == 1`, `GetConnection("conn-1")` returns error

### CM-019: DeleteConnection — not running, just removes

**Setup:** Load "conn-1" but don't start
**Action:** `err := mgr.DeleteConnection(ctx, "conn-1")`
**Assert:** `err == nil`, `DestroyClientCalls == 0`

### CM-020: Connection context is child of root context

**Setup:** Create manager with root context
**Action:** Start "conn-1", get its context
**Assert:** Connection context's parent chain includes root context

### CM-021: StopConnection cancels connection context

**Setup:** Start "conn-1", capture its context
**Action:** `mgr.StopConnection(ctx, "conn-1")`
**Assert:** Connection context is cancelled (`ctx.Err() == context.Canceled`)

### CM-022: Root context cancel cancels all connection contexts

**Setup:** Start "conn-1" and "conn-2" with cancellable root context
**Action:** Cancel root context
**Assert:** Both connection contexts are cancelled

### CM-023: WatchConnections — detected when ConnectionProvider implements ConnectionWatcher

**Setup:** Use `WatchingConnectionProvider` with `WatchConnectionsFunc` that sends updates
**Action:** Start watching, send update on channel
**Assert:** Manager receives and processes connection changes

### CM-024: WatchConnections — not detected on plain ConnectionProvider

**Setup:** Use plain `StubConnectionProvider` (no ConnectionWatcher)
**Action:** Attempt to watch
**Assert:** No-op, no error, no goroutine

### CM-025: RefreshClient — detected when ConnectionProvider implements ClientRefresher

**Setup:** Use `RefreshingConnectionProvider` with `RefreshClientFunc`
**Action:** `err := mgr.RefreshClient(ctx, "conn-1")`
**Assert:** `err == nil`, refresh function was called with correct client

### CM-026: RefreshClient — not supported

**Setup:** Plain StubConnectionProvider (no ClientRefresher)
**Action:** `err := mgr.RefreshClient(ctx, "conn-1")`
**Assert:** `err != nil` (not supported)

### CM-027: Concurrency — concurrent Start/Stop/Get don't race

**Setup:** Start manager
**Action:** Launch 10 goroutines: 5 calling StartConnection, 5 calling StopConnection, all for "conn-1"
**Assert:** No data race (run with `-race`), no panic. Final state is deterministic.

### CM-028: Concurrency — concurrent GetClient during Start/Stop

**Setup:** Start manager
**Action:** Launch goroutines: Start "conn-1", then concurrent GetClient and StopConnection
**Assert:** No race, GetClient returns either valid client or error

### CM-029: StartConnection — context passed to CreateClient carries Session

**Setup:** StubConnectionProvider captures ctx in CreateClientFunc
**Action:** `mgr.StartConnection(ctx, "conn-1")`
**Assert:** Captured ctx has Session with correct Connection data

### CM-030: StopConnection — context passed to DestroyClient carries Session

**Setup:** StubConnectionProvider captures ctx in DestroyClientFunc
**Action:** Start then stop "conn-1"
**Assert:** Captured ctx has Session with correct Connection data

---

## 7. Test Specification: watchManager

**File:** `plugin-sdk/pkg/resource/services/watch_manager_test.go`
**Component:** `watchManager[string]` — the most complex component

All tests use `-race` flag. All tests use `assert.Eventually` for async assertions
with reasonable timeouts (5s) and polling intervals (10ms).

---

### Connection-Level Lifecycle

### WM-001: StartConnectionWatch — starts SyncOnConnect watchers

**Setup:** Registry has Pod (watchable, SyncOnConnect) and Deployment (watchable, SyncOnConnect).
Both Watch() implementations: emit `Syncing` → `Synced` → block until ctx.Done().
**Action:** `mgr.StartConnectionWatch(ctx, "conn-1", client)`
**Assert:**
- Pod Watch goroutine is running
- Deployment Watch goroutine is running
- RecordingSink receives Syncing then Synced for both resources

### WM-002: StartConnectionWatch — skips SyncOnFirstQuery

**Setup:** Registry has Pod (SyncOnConnect) and Secret (SyncOnFirstQuery). Both watchable.
**Action:** `mgr.StartConnectionWatch(ctx, "conn-1", client)`
**Assert:**
- Pod Watch goroutine is running
- Secret Watch goroutine is NOT running
- `mgr.IsResourceWatchRunning("conn-1", "core::v1::Secret") == false`

### WM-003: StartConnectionWatch — skips SyncNever

**Setup:** Registry has Pod (SyncOnConnect) and ConfigMap (SyncNever). Both watchable.
**Action:** `mgr.StartConnectionWatch(ctx, "conn-1", client)`
**Assert:**
- Pod Watch goroutine is running
- ConfigMap Watch goroutine is NOT running

### WM-004: StartConnectionWatch — skips non-Watcher resourcers

**Setup:** Registry has Pod (watchable) and Service (plain stub, no Watcher)
**Action:** `mgr.StartConnectionWatch(ctx, "conn-1", client)`
**Assert:**
- Pod Watch goroutine is running
- No goroutine started for Service
- No error

### WM-005: StartConnectionWatch — zero watchable resourcers

**Setup:** Registry has only non-Watcher resourcers
**Action:** `mgr.StartConnectionWatch(ctx, "conn-1", client)`
**Assert:** No error, no goroutines started. Manager state shows zero active watches.

### WM-006: StopConnectionWatch — cancels all watch goroutines

**Setup:** Start connection with Pod and Deployment watching
**Action:** `mgr.StopConnectionWatch(ctx, "conn-1")`
**Assert:**
- Both Watch goroutines return (blocked on ctx.Done() → returns nil)
- `mgr.IsResourceWatchRunning("conn-1", "core::v1::Pod") == false`
- `mgr.IsResourceWatchRunning("conn-1", "apps::v1::Deployment") == false`
- RecordingSink receives no further events after stop

### WM-007: StopConnectionWatch — Watch() goroutines return nil on clean shutdown

**Setup:** Start connection, watchers are blocking
**Action:** Stop connection
**Assert:** All Watch() calls return nil (not error). No restart triggered.

### WM-008: StopConnectionWatch — connection not found

**Setup:** No connections started
**Action:** `err := mgr.StopConnectionWatch(ctx, "conn-unknown")`
**Assert:** Returns error (connection not found)

### WM-009: StopConnectionWatch — idempotent

**Setup:** Start then stop "conn-1"
**Action:** `err := mgr.StopConnectionWatch(ctx, "conn-1")` (second call)
**Assert:** No error (or specific "already stopped" error), no panic

---

### Per-Resource Lifecycle

### WM-010: EnsureResourceWatch — starts lazy watch

**Setup:** Start connection. Secret is SyncOnFirstQuery (not auto-started).
**Action:** `err := mgr.EnsureResourceWatch(ctx, "conn-1", "core::v1::Secret")`
**Assert:**
- `err == nil`
- Secret Watch goroutine is now running
- `mgr.IsResourceWatchRunning("conn-1", "core::v1::Secret") == true`
- RecordingSink receives Syncing then Synced for Secret

### WM-011: EnsureResourceWatch — already running is no-op

**Setup:** Start connection with Pod watching (SyncOnConnect). Pod is already running.
**Action:** `err := mgr.EnsureResourceWatch(ctx, "conn-1", "core::v1::Pod")`
**Assert:** `err == nil`, no additional goroutine started, Watch() called only once total

### WM-012: EnsureResourceWatch — non-Watcher resource returns error

**Setup:** Start connection. Service is registered but NOT a Watcher.
**Action:** `err := mgr.EnsureResourceWatch(ctx, "conn-1", "core::v1::Service")`
**Assert:** `err != nil` (resource does not support watching)

### WM-013: EnsureResourceWatch — unknown resource key

**Setup:** Start connection.
**Action:** `err := mgr.EnsureResourceWatch(ctx, "conn-1", "unknown::v1::Foo")`
**Assert:** `err != nil` (resource not registered)

### WM-014: EnsureResourceWatch — connection not found

**Setup:** No connections started.
**Action:** `err := mgr.EnsureResourceWatch(ctx, "conn-unknown", "core::v1::Pod")`
**Assert:** `err != nil` (connection not found)

### WM-015: StopResourceWatch — stops specific resource only

**Setup:** Start connection with Pod and Deployment watching.
**Action:** `err := mgr.StopResourceWatch(ctx, "conn-1", "core::v1::Pod")`
**Assert:**
- `err == nil`
- Pod Watch goroutine has stopped
- Deployment Watch goroutine is STILL running
- `mgr.IsResourceWatchRunning("conn-1", "core::v1::Pod") == false`
- `mgr.IsResourceWatchRunning("conn-1", "apps::v1::Deployment") == true`

### WM-016: StopResourceWatch — not running is no-op or error

**Setup:** Start connection. Secret was never started (SyncOnFirstQuery).
**Action:** `err := mgr.StopResourceWatch(ctx, "conn-1", "core::v1::Secret")`
**Assert:** No panic. Either no error (no-op) or appropriate error.

### WM-017: RestartResourceWatch — stops and restarts

**Setup:** Start connection with Pod watching. Track watch invocation count.
**Action:** `err := mgr.RestartResourceWatch(ctx, "conn-1", "core::v1::Pod")`
**Assert:**
- `err == nil`
- Old Watch goroutine exited
- New Watch goroutine started (Watch() called twice total)
- `mgr.IsResourceWatchRunning("conn-1", "core::v1::Pod") == true`
- New events flow through to sink

### WM-018: RestartResourceWatch — fresh context on restart

**Setup:** Start connection. Pod watcher captures its ctx.
**Action:** `mgr.RestartResourceWatch(ctx, "conn-1", "core::v1::Pod")`
**Assert:** The new invocation's ctx is different from the old one. Old ctx is cancelled.

### WM-019: IsResourceWatchRunning — true when running

**Setup:** Start connection with Pod watching
**Action:** `running := mgr.IsResourceWatchRunning("conn-1", "core::v1::Pod")`
**Assert:** `running == true`

### WM-020: IsResourceWatchRunning — false when stopped

**Setup:** Start connection, stop Pod watch
**Action:** `running := mgr.IsResourceWatchRunning("conn-1", "core::v1::Pod")`
**Assert:** `running == false`

### WM-021: IsResourceWatchRunning — false for never-started

**Setup:** Start connection. Secret is SyncOnFirstQuery, never ensured.
**Action:** `running := mgr.IsResourceWatchRunning("conn-1", "core::v1::Secret")`
**Assert:** `running == false`

### WM-022: IsResourceWatchRunning — connection not found

**Setup:** No connections.
**Action:** `running := mgr.IsResourceWatchRunning("conn-unknown", "core::v1::Pod")`
**Assert:** `running == false`

---

### Event Flow

### WM-023: Events flow from Watch to subscriber sink

**Setup:** Start connection. Pod watcher emits: `OnAdd({ID: "pod-1"})`, `OnUpdate({ID: "pod-1"})`, `OnDelete({ID: "pod-1"})`
**Action:** Start ListenForEvents with RecordingSink
**Assert:**
- `sink.WaitForAdds(t, 1, 5s)` — receives add with `ID == "pod-1"`
- `sink.WaitForUpdates(t, 1, 5s)` — receives update
- `sink.WaitForDeletes(t, 1, 5s)` — receives delete

### WM-024: State events flow to subscriber sink

**Setup:** Start connection. Pod watcher emits `OnStateChange(Syncing)` then `OnStateChange(Synced)`
**Action:** Start ListenForEvents with RecordingSink
**Assert:** sink receives both state events in order

### WM-025: Events from multiple connections are distinguished

**Setup:** Start "conn-1" and "conn-2". Both have Pod watchers emitting events.
**Action:** Start ListenForEvents
**Assert:** Events carry connection ID. Can filter adds by connection.

### WM-026: Events from multiple resource types are distinguished

**Setup:** Start connection. Pod and Deployment watchers emit events.
**Action:** Start ListenForEvents
**Assert:** Events carry resource key. Pod events have `ResourceKey == "core::v1::Pod"`, Deployment events have `ResourceKey == "apps::v1::Deployment"`.

### WM-027: No events after StopConnectionWatch

**Setup:** Start connection with Pod watching. Start ListenForEvents. Verify events arrive. Stop connection.
**Action:** After stop, Pod watcher would emit more events (but can't — it's stopped).
**Assert:** No new events arrive after stop. RecordingSink count doesn't increase.

### WM-028: No events from stopped resource

**Setup:** Start connection with Pod and Deployment watching. Stop Pod watch.
**Action:** Deployment watcher continues emitting.
**Assert:** Deployment events still arrive. No Pod events after stop.

### WM-029: ListenForEvents — multiple listeners receive same events

**Setup:** Start connection with Pod watching.
**Action:** Start ListenForEvents with sink1 AND sink2 (two concurrent listeners).
**Assert:** Both sinks receive the same events.

### WM-030: ListenForEvents — listener disconnect doesn't affect others

**Setup:** Two listeners. Cancel listener1's context.
**Action:** Pod watcher continues emitting.
**Assert:** Listener2 still receives events. No panic.

---

### Error Recovery

### WM-031: Watch returns error — restarted with backoff

**Setup:** Pod watcher that returns error after 100ms. Track call count.
**Action:** Start connection. Wait for restart.
**Assert:**
- Watch() called at least 2 times
- RecordingSink receives `OnStateChange(Error)` after first failure
- Second call happens after backoff interval (> 0ms)

### WM-032: Watch returns error — max retries exceeded

**Setup:** Pod watcher that ALWAYS returns error after 50ms. Max retries = 3.
**Action:** Start connection. Wait for retries to exhaust.
**Assert:**
- Watch() called exactly `maxRetries + 1` times (1 initial + 3 retries)
- RecordingSink receives `OnStateChange(Failed)` after max retries
- No further Watch() calls after Failed state
- `mgr.IsResourceWatchRunning("conn-1", "core::v1::Pod") == false`

### WM-033: Watch returns error — backoff increases exponentially

**Setup:** Pod watcher that returns error immediately. Track call timestamps.
**Action:** Start connection. Record time of each Watch() call.
**Assert:** Time between consecutive calls increases (delay[1] < delay[2] < delay[3]).

### WM-034: Watch returns error then succeeds — resets retry count

**Setup:** Pod watcher: fails on 1st call, succeeds (blocks) on 2nd call, fails on 3rd call after restart.
**Action:** Start connection → fails → restarts → succeeds. Later, manually restart → fails.
**Assert:** 3rd call's retry count is reset to 0 (not cumulative from first failure).

### WM-035: Watch panics — recovered and treated as error

**Setup:** Pod watcher that panics.
**Action:** Start connection.
**Assert:**
- No process crash
- RecordingSink receives `OnStateChange(Error)` with panic info
- Watch() is retried (recovered panic = error)

### WM-036: Watch returns nil (clean shutdown) — no restart

**Setup:** Pod watcher that returns nil after 100ms (simulating clean exit).
**Action:** Start connection.
**Assert:** This should only happen when ctx is cancelled. If Watch returns nil while ctx is still active, the manager should NOT restart (ambiguous — may need to mark as stopped).

### WM-037: Watch error recovery — context cancelled during backoff

**Setup:** Pod watcher returns error. During backoff wait, cancel connection.
**Action:** Cancel connection context.
**Assert:** Backoff wait is interrupted. No further restart attempt. Clean shutdown.

---

### Context Hierarchy

### WM-038: Connection context cancel stops all watches

**Setup:** Start connection with Pod, Deployment, Service watching. Capture connection ctx.
**Action:** Cancel connection context.
**Assert:**
- ALL three Watch() goroutines return
- All `IsResourceWatchRunning` return false
- No further events

### WM-039: Root context cancel stops everything

**Setup:** Start "conn-1" and "conn-2" with watchers. Root context is cancellable.
**Action:** Cancel root context.
**Assert:** All watchers for both connections stop. Clean shutdown.

### WM-040: Per-resource context cancel doesn't affect other resources

**Setup:** Start connection with Pod and Deployment watching.
**Action:** `mgr.StopResourceWatch(ctx, "conn-1", "core::v1::Pod")`
**Assert:** Pod Watch returns. Deployment Watch continues running.

### WM-041: Per-resource context is child of connection context

**Setup:** Start connection. Capture Pod's watch context (via Watch() capturing its ctx).
**Action:** Cancel connection context.
**Assert:** Pod's watch context is also cancelled (child inherits parent cancellation).

---

### Edge Cases

### WM-042: No Watcher-capable resourcers — zero goroutines

**Setup:** Registry has only non-Watcher resourcers.
**Action:** `mgr.StartConnectionWatch(ctx, "conn-1", client)`
**Assert:** No goroutines started. No error. `HasWatch("conn-1") == false` or `true` with zero active resources.

### WM-043: Watch blocks until cancelled — doesn't exit prematurely

**Setup:** Pod watcher: blocks forever on `<-ctx.Done()`.
**Action:** Start connection. Wait 2 seconds.
**Assert:** Watch goroutine is still running after 2 seconds.

### WM-044: Concurrent EnsureResourceWatch for same resource

**Setup:** Start connection. Secret is SyncOnFirstQuery.
**Action:** Launch 10 goroutines all calling `mgr.EnsureResourceWatch(ctx, "conn-1", "core::v1::Secret")`
**Assert:** Run with `-race`. Watch() called exactly once. No panic.

### WM-045: Concurrent Start/Stop connection watch

**Setup:** Manager ready.
**Action:** Launch goroutines: some calling StartConnectionWatch, some calling StopConnectionWatch.
**Assert:** Run with `-race`. No data race. No panic. Final state is consistent.

### WM-046: StopResourceWatch before any Start

**Setup:** Start connection. Secret is SyncOnFirstQuery, never started.
**Action:** `err := mgr.StopResourceWatch(ctx, "conn-1", "core::v1::Secret")`
**Assert:** No panic. Either error or no-op.

### WM-047: GetWatchState returns per-resource summary

**Setup:** Start connection with Pod (Synced), Deployment (Syncing), Secret (not started).
**Action:** `state, err := mgr.GetWatchState(ctx, "conn-1")`
**Assert:** Summary shows Pod=Synced, Deployment=Syncing, Secret=NotStarted (or absent).

### WM-048: HasWatch — true when connection has active watches

**Setup:** Start connection with at least one SyncOnConnect watcher.
**Action:** `has := mgr.HasWatch("conn-1")`
**Assert:** `has == true`

### WM-049: HasWatch — false when no connection

**Setup:** No connections.
**Action:** `has := mgr.HasWatch("conn-unknown")`
**Assert:** `has == false`

### WM-050: Restart connection — new watches with new client

**Setup:** Start "conn-1" with Pod watching. Stop connection. Start again with new client.
**Action:** `mgr.StartConnectionWatch(ctx, "conn-1", newClient)`
**Assert:**
- New Watch() goroutine started with `newClient`
- Events reference the new client
- Old Watch is fully stopped

---

## 8. Test Specification: typeManager

**File:** `plugin-sdk/pkg/resource/services/type_manager_test.go`
**Component:** `typeManager`

---

### TM-001: Static types from registry

**Setup:** Registry has Pod, Deployment, Service
**Action:** `types := mgr.GetResourceTypes(ctx, "conn-1")`
**Assert:** Returns map with all 3 types

### TM-002: Discovered types merged with static

**Setup:** Registry has Pod. DiscoveryProvider returns CRDMeta.
**Action:** `mgr.DiscoverForConnection(ctx, conn)` → `types := mgr.GetResourceTypes(ctx, "conn-1")`
**Assert:** Returns Pod AND CRDMeta

### TM-003: Discovery disabled (nil DiscoveryProvider)

**Setup:** DiscoveryProvider is nil
**Action:** `types := mgr.GetResourceTypes(ctx, "conn-1")`
**Assert:** Returns only static types. No error.

### TM-004: Discovery error — falls back to static types

**Setup:** DiscoveryProvider returns error
**Action:** `types, err := mgr.GetResourceTypes(ctx, "conn-1")`
**Assert:** Returns static types only. Error logged but not returned (graceful degradation).

### TM-005: GetResourceGroups — includes static and discovered resources

**Setup:** Groups configured: `[{ID: "workloads", ResourceKeys: ["apps::v1::Deployment"]}]`. Discovery adds CRD to a group.
**Action:** `groups := mgr.GetResourceGroups(ctx, "conn-1")`
**Assert:** Groups include both static and discovered resources

### TM-006: GetResourceGroup — exists

**Setup:** Groups: `[{ID: "workloads"}]`
**Action:** `group, err := mgr.GetResourceGroup(ctx, "workloads")`
**Assert:** `err == nil`, `group.ID == "workloads"`

### TM-007: GetResourceGroup — not found

**Setup:** Groups: `[{ID: "workloads"}]`
**Action:** `group, err := mgr.GetResourceGroup(ctx, "networking")`
**Assert:** `err != nil`

### TM-008: OnConnectionRemoved — delegates to DiscoveryProvider

**Setup:** DiscoveryProvider tracks OnConnectionRemoved calls
**Action:** `mgr.OnConnectionRemoved(ctx, &conn)`
**Assert:** DiscoveryProvider.OnConnectionRemoved called with correct connection

### TM-009: OnConnectionRemoved — clears discovered types for connection

**Setup:** Discover types for "conn-1". Then remove.
**Action:** `mgr.OnConnectionRemoved(ctx, &conn)`
**Assert:** `mgr.GetResourceTypes(ctx, "conn-1")` returns only static types

### TM-010: HasResourceType — true for known

**Setup:** Registry has Pod
**Action:** `has := mgr.HasResourceType(ctx, "core::v1::Pod")`
**Assert:** `has == true`

### TM-011: HasResourceType — false for unknown

**Setup:** Registry has Pod only
**Action:** `has := mgr.HasResourceType(ctx, "apps::v1::Deployment")`
**Assert:** `has == false`

### TM-012: GetResourceType — returns full meta

**Setup:** Register Pod with full metadata
**Action:** `meta, err := mgr.GetResourceType(ctx, "core::v1::Pod")`
**Assert:** `err == nil`, `meta.Kind == "Pod"`, `meta.Description == "A pod"`

### TM-013: GetResourceDefinition — precedence chain

**Setup:** Pod has DefinitionProvider returning `defA`. Registration has `defB`. DefaultDef is `defC`.
**Action:** `def := mgr.GetResourceDefinition(ctx, "core::v1::Pod")`
**Assert:** `def == defA` (DefinitionProvider wins)

---

## 9. Test Specification: resourceController (Provider Interface)

**File:** `plugin-sdk/pkg/resource/controller_test.go`
**Component:** `resourceController[string]` implementing full `Provider` interface

These tests wire the real internal services together (resourcerRegistry, connectionManager,
watchManager, typeManager) with mock plugin-author implementations. This is the integration
layer that validates correct service collaboration.

---

### 9.1 OperationProvider — CRUD

For each CRUD operation (Get, List, Find, Create, Update, Delete), the same pattern of tests
applies. Tests are labeled `OP-NNN`.

---

#### Get

### OP-001: Get — happy path

**Setup:** Register Pod resourcer with `GetFunc` returning `{ID: "pod-1", Data: {...}}`. Start "conn-1".
**Action:** `result, err := provider.Get(ctx, "core::v1::Pod", GetInput{ID: "pod-1", Namespace: "default"})`
**Assert:** `err == nil`, `result.ID == "pod-1"`

### OP-002: Get — correct client passed to resourcer

**Setup:** Pod `GetFunc` captures the `client` argument. ConnectionProvider creates `"my-client"`.
**Action:** `provider.Get(ctx, "core::v1::Pod", GetInput{...})`
**Assert:** Captured `*client == "my-client"`

### OP-003: Get — correct meta passed to resourcer

**Setup:** Pod `GetFunc` captures the `meta` argument.
**Action:** `provider.Get(ctx, "core::v1::Pod", GetInput{...})`
**Assert:** Captured `meta == PodMeta`

### OP-004: Get — context carries Session

**Setup:** Pod `GetFunc` captures `ctx`. Checks `SessionFromContext(ctx)`.
**Action:** `provider.Get(ctx, "core::v1::Pod", GetInput{...})`
**Assert:** Session is present in ctx with correct Connection data.

### OP-005: Get — unknown resource key, no pattern

**Setup:** Only Pod registered, no patterns.
**Action:** `result, err := provider.Get(ctx, "apps::v1::Deployment", GetInput{...})`
**Assert:** `err != nil` (resource not found), `result == nil`

### OP-006: Get — unknown resource key, pattern fallback

**Setup:** Pattern `"*": patternResourcer` registered. No Pod registration.
**Action:** `result, err := provider.Get(ctx, "core::v1::Pod", GetInput{...})`
**Assert:** `err == nil`, patternResourcer.GetFunc was called

### OP-007: Get — connection not started

**Setup:** Pod registered, but no StartConnection called.
**Action:** `result, err := provider.Get(ctx, "core::v1::Pod", GetInput{...})`
**Assert:** `err != nil` (connection not started)

### OP-008: Get — resourcer returns error

**Setup:** Pod `GetFunc` returns `errors.New("not found")`
**Action:** `result, err := provider.Get(ctx, "core::v1::Pod", GetInput{...})`
**Assert:** `err != nil`, error message contains "not found"

### OP-009: Get — ErrorClassifier wraps error (global)

**Setup:** Pod `GetFunc` returns raw error. Config-level `ErrorClassifier` classifies it.
**Action:** `result, err := provider.Get(ctx, "core::v1::Pod", GetInput{...})`
**Assert:** `err` is a `ResourceOperationError` with classified fields

### OP-010: Get — ErrorClassifier wraps error (per-resource wins over global)

**Setup:** Pod resourcer implements `ErrorClassifier` (per-resource). Config also has global classifier.
**Action:** `result, err := provider.Get(ctx, "core::v1::Pod", GetInput{...})`
**Assert:** Per-resource classifier was used (not global)

### OP-011: Get — context cancelled

**Setup:** Cancel context before calling Get.
**Action:** `result, err := provider.Get(cancelledCtx, "core::v1::Pod", GetInput{...})`
**Assert:** `err != nil`, error is context cancellation

### OP-012: Get — context deadline exceeded

**Setup:** Context with 1ns deadline (expires immediately).
**Action:** Pod `GetFunc` sleeps 1s then returns.
**Assert:** `err != nil`, error is context deadline exceeded

#### List

### OP-013: List — happy path

**Setup:** Pod `ListFunc` returns `{Items: [{ID: "pod-1"}, {ID: "pod-2"}]}`
**Action:** `result, err := provider.List(ctx, "core::v1::Pod", ListInput{Namespace: "default"})`
**Assert:** `err == nil`, `len(result.Items) == 2`

### OP-014: List — triggers EnsureResourceWatch for SyncOnFirstQuery

**Setup:** Secret is SyncOnFirstQuery + Watcher. Not started yet.
**Action:** `provider.List(ctx, "core::v1::Secret", ListInput{Namespace: "default"})`
**Assert:** After List, `mgr.IsResourceWatchRunning("conn-1", "core::v1::Secret") == true`

### OP-015: List — does NOT trigger watch for SyncNever

**Setup:** ConfigMap is SyncNever + Watcher.
**Action:** `provider.List(ctx, "core::v1::ConfigMap", ListInput{...})`
**Assert:** `mgr.IsResourceWatchRunning("conn-1", "core::v1::ConfigMap") == false`

### OP-016: List — empty result

**Setup:** Pod `ListFunc` returns `{Items: []}`
**Action:** `result, err := provider.List(ctx, "core::v1::Pod", ListInput{...})`
**Assert:** `err == nil`, `len(result.Items) == 0`

### OP-017: List — connection not started

**Setup:** No StartConnection called.
**Action:** `result, err := provider.List(ctx, "core::v1::Pod", ListInput{...})`
**Assert:** `err != nil`

### OP-018: List — resourcer returns error

**Setup:** Pod `ListFunc` returns error
**Action:** `result, err := provider.List(ctx, "core::v1::Pod", ListInput{...})`
**Assert:** `err != nil`

#### Find

### OP-019: Find — happy path

**Setup:** Pod `FindFunc` returns `{Items: [{ID: "pod-match"}]}`
**Action:** `result, err := provider.Find(ctx, "core::v1::Pod", FindInput{Query: "match"})`
**Assert:** `err == nil`, `len(result.Items) == 1`

### OP-020: Find — cross-namespace search

**Setup:** Pod `FindFunc` captures input.
**Action:** `provider.Find(ctx, "core::v1::Pod", FindInput{Query: "test"})`
**Assert:** Input has no namespace restriction (Find is cross-scope)

#### Create

### OP-021: Create — happy path

**Setup:** Pod `CreateFunc` returns `{ID: "new-pod"}`
**Action:** `result, err := provider.Create(ctx, "core::v1::Pod", CreateInput{Data: []byte(`{}`)})`
**Assert:** `err == nil`, `result.ID == "new-pod"`

### OP-022: Create — resourcer returns error (e.g., validation failure)

**Setup:** Pod `CreateFunc` returns error
**Action:** `result, err := provider.Create(ctx, "core::v1::Pod", CreateInput{...})`
**Assert:** `err != nil`

#### Update

### OP-023: Update — happy path

**Setup:** Pod `UpdateFunc` returns `{ID: "pod-1"}`
**Action:** `result, err := provider.Update(ctx, "core::v1::Pod", UpdateInput{ID: "pod-1", Data: []byte(`{}`)})`
**Assert:** `err == nil`, `result.ID == "pod-1"`

### OP-024: Update — resourcer returns error

**Setup:** Pod `UpdateFunc` returns error
**Action:** `result, err := provider.Update(ctx, "core::v1::Pod", UpdateInput{...})`
**Assert:** `err != nil`

#### Delete

### OP-025: Delete — happy path

**Setup:** Pod `DeleteFunc` returns nil
**Action:** `result, err := provider.Delete(ctx, "core::v1::Pod", DeleteInput{ID: "pod-1"})`
**Assert:** `err == nil`

### OP-026: Delete — resourcer returns error

**Setup:** Pod `DeleteFunc` returns error
**Action:** `result, err := provider.Delete(ctx, "core::v1::Pod", DeleteInput{...})`
**Assert:** `err != nil`

#### Cross-cutting CRUD

### OP-027: CRUD — ErrorClassifier not set, raw error passes through

**Setup:** No ErrorClassifier configured. Pod GetFunc returns raw error.
**Action:** `_, err := provider.Get(ctx, "core::v1::Pod", GetInput{...})`
**Assert:** `err` is the raw error (not wrapped)

### OP-028: CRUD — concurrent operations on same resource

**Setup:** Pod resourcer's methods have no internal state races.
**Action:** Launch 10 concurrent Get calls for the same resource.
**Assert:** All complete without race. Run with `-race`.

### OP-029: CRUD — concurrent operations on different resources

**Setup:** Pod and Deployment resourcers.
**Action:** Concurrent Get on Pod and List on Deployment.
**Assert:** Both complete correctly. No interference.

---

### 9.2 ConnectionLifecycleProvider

### CL-001: StartConnection — creates client and returns status

**Setup:** ConnectionProvider creates client successfully.
**Action:** `status, err := provider.StartConnection(ctx, "conn-1")`
**Assert:** `err == nil`, `status.Status == Connected`

### CL-002: StartConnection — starts SyncOnConnect watches

**Setup:** Pod is watchable + SyncOnConnect.
**Action:** `provider.StartConnection(ctx, "conn-1")`
**Assert:** `provider.IsResourceWatchRunning(ctx, "conn-1", "core::v1::Pod") == true`

### CL-003: StartConnection — twice is idempotent

**Setup:** Start "conn-1".
**Action:** `provider.StartConnection(ctx, "conn-1")` again.
**Assert:** `err == nil`. Client NOT re-created. Watches NOT restarted.

### CL-004: StartConnection — CreateClient fails

**Setup:** `CreateClientFunc` returns error.
**Action:** `status, err := provider.StartConnection(ctx, "conn-1")`
**Assert:** `err != nil`. No watches started.

### CL-005: StopConnection — destroys client and stops watches

**Setup:** Start "conn-1" with watches.
**Action:** `conn, err := provider.StopConnection(ctx, "conn-1")`
**Assert:** `err == nil`. All watches stopped. Client destroyed.

### CL-006: StopConnection — not started

**Setup:** No StartConnection called.
**Action:** `conn, err := provider.StopConnection(ctx, "conn-1")`
**Assert:** `err != nil`

### CL-007: LoadConnections — returns from ConnectionProvider

**Setup:** ConnectionProvider returns 3 connections.
**Action:** `conns, err := provider.LoadConnections(ctx)`
**Assert:** `len(conns) == 3`

### CL-008: ListConnections — returns runtime state

**Setup:** Load 3 connections, start 2.
**Action:** `conns, err := provider.ListConnections(ctx)`
**Assert:** `len(conns) == 3`, 2 show Connected

### CL-009: GetConnection — exists

**Setup:** Load connections.
**Action:** `conn, err := provider.GetConnection(ctx, "conn-1")`
**Assert:** `err == nil`, `conn.ID == "conn-1"`

### CL-010: GetConnection — not found

**Setup:** Load connections. "conn-999" doesn't exist.
**Action:** `conn, err := provider.GetConnection(ctx, "conn-999")`
**Assert:** `err != nil`

### CL-011: GetConnectionNamespaces — returns namespaces

**Setup:** Start "conn-1". GetNamespacesFunc returns `["default", "kube-system"]`.
**Action:** `ns, err := provider.GetConnectionNamespaces(ctx, "conn-1")`
**Assert:** `err == nil`, `ns == ["default", "kube-system"]`

### CL-012: GetConnectionNamespaces — connection not started

**Setup:** No StartConnection.
**Action:** `ns, err := provider.GetConnectionNamespaces(ctx, "conn-1")`
**Assert:** `err != nil`

### CL-013: UpdateConnection — updates and optionally restarts

**Setup:** Start "conn-1". Update with changed data.
**Action:** `updated, err := provider.UpdateConnection(ctx, Connection{ID: "conn-1", Name: "New"})`
**Assert:** `err == nil`, `updated.Name == "New"`

### CL-014: DeleteConnection — stops and removes

**Setup:** Start "conn-1".
**Action:** `err := provider.DeleteConnection(ctx, "conn-1")`
**Assert:** `err == nil`. Subsequent GetConnection returns error.

### CL-015: WatchConnections — streams changes

**Setup:** ConnectionProvider implements ConnectionWatcher. Sends update after 100ms.
**Action:** `err := provider.WatchConnections(ctx, stream)`
**Assert:** `stream` receives the connection update.

### CL-016: WatchConnections — not supported

**Setup:** ConnectionProvider does NOT implement ConnectionWatcher.
**Action:** `err := provider.WatchConnections(ctx, stream)`
**Assert:** `err != nil` (not supported) or no-op.

---

### 9.3 WatchProvider

### WP-001: StartConnectionWatch — starts watches

**Setup:** Start "conn-1". Pod is SyncOnConnect.
**Action:** `err := provider.StartConnectionWatch(ctx, "conn-1")`
**Assert:** `err == nil`. Pod watch running.

### WP-002: StopConnectionWatch — stops all watches

**Setup:** Start connection and watches.
**Action:** `err := provider.StopConnectionWatch(ctx, "conn-1")`
**Assert:** All watches stopped.

### WP-003: HasWatch — true/false

**Setup:** Start "conn-1" with watches. "conn-2" not started.
**Action:** `has1 := provider.HasWatch(ctx, "conn-1")`, `has2 := provider.HasWatch(ctx, "conn-2")`
**Assert:** `has1 == true`, `has2 == false`

### WP-004: GetWatchState — returns summary

**Setup:** Start "conn-1". Pod synced, Deployment syncing.
**Action:** `state, err := provider.GetWatchState(ctx, "conn-1")`
**Assert:** Summary has per-resource state entries.

### WP-005: ListenForEvents — events flow through

**Setup:** Start "conn-1". Pod watcher emits add event.
**Action:** Start `provider.ListenForEvents(ctx, sink)`.
**Assert:** RecordingSink receives add event.

### WP-006: ListenForEvents — blocks until context cancelled

**Setup:** Start connection with quiet watcher (no events after sync).
**Action:** `go provider.ListenForEvents(ctx, sink)`. Wait 1s. Cancel ctx.
**Assert:** ListenForEvents returns after cancellation.

### WP-007: EnsureResourceWatch — lazy start

**Setup:** Secret is SyncOnFirstQuery. Not started.
**Action:** `err := provider.EnsureResourceWatch(ctx, "conn-1", "core::v1::Secret")`
**Assert:** Secret watch now running.

### WP-008: StopResourceWatch

**Setup:** Pod watch running.
**Action:** `err := provider.StopResourceWatch(ctx, "conn-1", "core::v1::Pod")`
**Assert:** Pod watch stopped. Other watches continue.

### WP-009: RestartResourceWatch

**Setup:** Pod watch running.
**Action:** `err := provider.RestartResourceWatch(ctx, "conn-1", "core::v1::Pod")`
**Assert:** Pod watch restarted (new goroutine). Events flow again.

### WP-010: IsResourceWatchRunning — correct state

**Setup:** Pod running, Secret not started.
**Action:** Check both.
**Assert:** Pod = true, Secret = false.

---

### 9.4 TypeProvider

### TP-001: GetResourceTypes — returns all types for connection

**Setup:** Pod, Deployment registered. DiscoveryProvider returns CRD for "conn-1".
**Action:** `types := provider.GetResourceTypes(ctx, "conn-1")`
**Assert:** Contains Pod, Deployment, and CRD.

### TP-002: GetResourceGroups — returns configured groups

**Setup:** Groups: `[{ID: "workloads", Resources: [...]}]`
**Action:** `groups := provider.GetResourceGroups(ctx, "conn-1")`
**Assert:** Contains "workloads" group.

### TP-003: GetResourceGroup — specific group

**Setup:** Groups configured.
**Action:** `group, err := provider.GetResourceGroup(ctx, "workloads")`
**Assert:** `err == nil`, correct group returned.

### TP-004: HasResourceType — true/false

**Setup:** Pod registered.
**Action:** `has := provider.HasResourceType(ctx, "core::v1::Pod")`
**Assert:** `true`

### TP-005: GetResourceDefinition — full precedence

**Setup:** Pod has DefinitionProvider.
**Action:** `def, err := provider.GetResourceDefinition(ctx, "core::v1::Pod")`
**Assert:** Returns DefinitionProvider's definition.

---

### 9.5 ActionProvider

### AP-001: GetActions — resourcer has ActionResourcer

**Setup:** Pod implements ActionResourcer. Returns `[{ID: "restart", Label: "Restart Pod"}]`.
**Action:** `actions, err := provider.GetActions(ctx, "core::v1::Pod")`
**Assert:** `err == nil`, `len(actions) == 1`, `actions[0].ID == "restart"`

### AP-002: GetActions — resourcer has no ActionResourcer

**Setup:** Pod is plain stub.
**Action:** `actions, err := provider.GetActions(ctx, "core::v1::Pod")`
**Assert:** `err == nil`, `len(actions) == 0`

### AP-003: ExecuteAction — delegates correctly

**Setup:** Pod ActionResourcer.ExecuteAction returns `{Success: true}`.
**Action:** `result, err := provider.ExecuteAction(ctx, "core::v1::Pod", "restart", ActionInput{...})`
**Assert:** `err == nil`, `result.Success == true`

### AP-004: ExecuteAction — unknown action ID

**Setup:** Pod ActionResourcer.ExecuteAction returns error for unknown action.
**Action:** `result, err := provider.ExecuteAction(ctx, "core::v1::Pod", "unknown-action", ActionInput{})`
**Assert:** `err != nil`

### AP-005: ExecuteAction — no ActionResourcer

**Setup:** Pod is plain stub.
**Action:** `result, err := provider.ExecuteAction(ctx, "core::v1::Pod", "restart", ActionInput{})`
**Assert:** `err != nil` (actions not supported)

### AP-006: StreamAction — events flow through channel

**Setup:** Pod ActionResourcer.StreamAction sends 3 ActionEvents then closes.
**Action:** `stream := make(chan ActionEvent, 10)`; `err := provider.StreamAction(ctx, "core::v1::Pod", "restart", ActionInput{}, stream)`
**Assert:** `err == nil`, received 3 events from stream.

### AP-007: StreamAction — context cancellation stops stream

**Setup:** Pod ActionResourcer.StreamAction blocks until ctx cancelled.
**Action:** Cancel ctx during streaming.
**Assert:** StreamAction returns. No goroutine leak.

---

### 9.6 EditorSchemaProvider

### SP-001: GetEditorSchemas — ConnectionProvider has SchemaProvider

**Setup:** ConnectionProvider also implements SchemaProvider. Returns 2 schemas.
**Action:** `schemas, err := provider.GetEditorSchemas(ctx, "conn-1")`
**Assert:** `err == nil`, `len(schemas) == 2`

### SP-002: GetEditorSchemas — not supported

**Setup:** ConnectionProvider does NOT implement SchemaProvider.
**Action:** `schemas, err := provider.GetEditorSchemas(ctx, "conn-1")`
**Assert:** `err == nil`, `len(schemas) == 0` (empty, not error)

### SP-003: GetEditorSchemas — connection not started

**Setup:** No StartConnection.
**Action:** `schemas, err := provider.GetEditorSchemas(ctx, "conn-1")`
**Assert:** `err != nil` (connection not started)

---

## 10. Test Specification: Full Lifecycle Scenarios

**File:** `plugin-sdk/pkg/resource/controller_scenario_test.go`
**Component:** Full end-to-end scenarios exercising multiple Provider methods in sequence.

These tests verify the system works as a whole, not just individual methods.

---

### SC-001: Connect → Browse → Disconnect

**Setup:** Pod and Deployment registered.
**Steps:**
1. `provider.LoadConnections(ctx)` → returns `[conn-1]`
2. `provider.StartConnection(ctx, "conn-1")` → success
3. `provider.List(ctx, "core::v1::Pod", ListInput{Namespace: "default"})` → returns items
4. `provider.Get(ctx, "core::v1::Pod", GetInput{ID: "pod-1"})` → returns item
5. `provider.StopConnection(ctx, "conn-1")` → success
6. `provider.List(ctx, "core::v1::Pod", ListInput{...})` → error (connection stopped)

### SC-002: Watch → Receive Events → Disconnect

**Setup:** Pod is watchable + SyncOnConnect. Watch emits add events.
**Steps:**
1. Start "conn-1"
2. Start `ListenForEvents(ctx, sink)` in goroutine
3. Wait for Synced state
4. Trigger adds from Pod watcher
5. Assert sink receives adds
6. Stop "conn-1"
7. Assert no more events after stop

### SC-003: Lazy Watch — SyncOnFirstQuery triggers on List

**Setup:** Secret is SyncOnFirstQuery + Watcher. Pod is SyncOnConnect.
**Steps:**
1. Start "conn-1" → Pod watch starts, Secret watch does NOT start
2. Assert `IsResourceWatchRunning("conn-1", "core::v1::Secret") == false`
3. `provider.List(ctx, "core::v1::Secret", ListInput{...})` → triggers EnsureResourceWatch
4. Assert `IsResourceWatchRunning("conn-1", "core::v1::Secret") == true`
5. Assert sink receives Secret state events (Syncing → Synced)

### SC-004: Disconnect → Reconnect → Watches restart

**Setup:** Pod is watchable + SyncOnConnect. Track Watch invocation count.
**Steps:**
1. Start "conn-1" → Pod watch starts (invocation 1)
2. Stop "conn-1" → Pod watch stops
3. Start "conn-1" again → Pod watch starts (invocation 2)
4. Assert Watch called twice total
5. Assert events flow from the new watch

### SC-005: Multiple connections — events distinguished

**Setup:** Pod watchable. Create 3 connections.
**Steps:**
1. Start "conn-1", "conn-2", "conn-3"
2. Each connection's Pod watcher emits different events
3. ListenForEvents receives all events
4. Filter by connection ID — events correctly attributed

### SC-006: Stop during initial sync

**Setup:** Pod watcher that takes 5 seconds to sync (sleeps before emitting Synced).
**Steps:**
1. Start "conn-1" → Pod watch starts syncing
2. Immediately call StopConnection("conn-1") (before sync completes)
3. Assert clean shutdown — no goroutine leak, no panic
4. Assert Pod Watch() returned (ctx cancelled during sleep)

### SC-007: CRUD during active Watch — no interference

**Setup:** Pod watchable + SyncOnConnect. Watch is emitting events.
**Steps:**
1. Start "conn-1" → Pod watch running
2. Concurrently: Watch emits events AND `provider.Get/List/Create/Update/Delete` calls
3. Assert all CRUD calls succeed
4. Assert all watch events arrive
5. Run with `-race`

### SC-008: Error recovery end-to-end

**Setup:** Pod watcher fails on first call, succeeds on second.
**Steps:**
1. Start "conn-1" → Pod watch starts → fails → Error state emitted
2. After backoff → Pod watch restarts → succeeds → Synced state emitted
3. Assert events from the successful watch arrive at sink
4. Assert state progression: Syncing → Error → Syncing → Synced

### SC-009: Multiple listeners — all receive events

**Setup:** Pod watchable.
**Steps:**
1. Start "conn-1"
2. Start 3 ListenForEvents with 3 different sinks
3. Pod watcher emits events
4. All 3 sinks receive all events

### SC-010: Discovery + CRUD for discovered type

**Setup:** DiscoveryProvider returns CRD type. Pattern resourcer handles CRDs.
**Steps:**
1. Start "conn-1" → discovery runs, finds CRD
2. `provider.HasResourceType(ctx, "custom::v1::Widget") == true`
3. `provider.List(ctx, "custom::v1::Widget", ListInput{...})` → pattern resourcer handles it
4. Assert pattern resourcer's ListFunc was called with correct meta

### SC-011: Connection watch — external config changes

**Setup:** ConnectionProvider implements ConnectionWatcher. Sends new connection list after 200ms.
**Steps:**
1. Start WatchConnections
2. After 200ms, watcher sends updated connection list
3. Assert provider processes the update
4. New connections available via ListConnections

### SC-012: Full cleanup — root context cancel

**Setup:** 2 connections, each with 3 watchers. 2 listeners.
**Steps:**
1. Start everything
2. Cancel root context
3. Assert all watchers stop
4. Assert all listeners return
5. Assert no goroutine leaks (use runtime.NumGoroutine() before/after)

---

## 11. Test Specification: Performance Invariants

**File:** `plugin-sdk/pkg/resource/controller_perf_test.go`
**Component:** Validating doc 08 performance invariants P1-P8 at the SDK level

These tests verify the SDK's contribution to performance invariants. Some invariants (P2-P4)
require full-stack testing (frontend) and can't be verified at the SDK level — those are marked
as out-of-scope here.

---

### PI-001: P1 — Zero unsolicited events during connection (SDK side)

**Setup:** Start "conn-1". Pod is SyncOnConnect. Watch emits 100 add events during sync.
DO NOT call ListenForEvents yet.
**Action:** Wait for sync to complete.
**Assert:** No events were dropped or lost — they're buffered in the SDK. But crucially,
the SDK does NOT push to any sink until ListenForEvents is called. Events queue internally.

### PI-002: P1 — Events only flow after ListenForEvents is called

**Setup:** Start "conn-1". Watches sync and emit events.
**Steps:**
1. Wait 1 second (events accumulating)
2. Start ListenForEvents with RecordingSink
3. Assert events arrive (either buffered or from ongoing watch)

### PI-003: P5 — Sub-second event delivery

**Setup:** Start "conn-1". Start ListenForEvents.
**Action:** Pod watcher emits add event at time T.
**Assert:** RecordingSink receives it at time T+delta where delta < 1 second.
(In practice, in-process should be <10ms. The 1s budget is for the full stack including gRPC.)

### PI-004: P6 — Non-watched resources have zero event cost

**Setup:** Start "conn-1". Service is NOT watchable (no Watcher). Pod IS watchable.
**Action:** Pod watcher emits events.
**Assert:** No events emitted with `ResourceKey == "core::v1::Service"`. Zero overhead for Service.

### PI-005: P7 — Go-side data completeness (SDK contribution)

**Setup:** Start "conn-1". 5 resources with SyncOnConnect. All watchers sync successfully.
**Action:** Wait for all to reach Synced state.
**Assert:** GetWatchState shows all 5 as Synced. No resources skipped or lost.

### PI-006: P8 — Large resource type doesn't block other watches

**Setup:** Pod watcher emits 10,000 add events during sync. Deployment watcher emits 10 events.
**Action:** Start connection. Both watches run concurrently.
**Assert:** Deployment events arrive at sink while Pod is still syncing (no head-of-line blocking).

---

## 12. Test Specification: Engine Integration (Level 3)

**File:** `backend/pkg/plugin/resource/controller_integration_test.go`
**Component:** Engine controller → SDK controller via InProcessBackend

---

### EI-001: Dispense — InProcessBackend returns SDK Provider

**Setup:** Build SDK Provider with mocks. Create InProcessBackend.
**Action:** `raw, err := backend.Dispense("resource")`
**Assert:** `err == nil`. `raw` satisfies `Provider` interface.

### EI-002: OnPluginStart — wires provider correctly

**Setup:** Create engine controller. Call OnPluginStart with InProcessBackend.
**Action:** `conns, err := engineCtrl.LoadConnections("test-plugin")`
**Assert:** `err == nil`. Returns connections from SDK's ConnectionProvider.

### EI-003: Full CRUD through engine → SDK

**Setup:** Engine controller with SDK provider via InProcessBackend. Start connection.
**Steps:**
1. `engineCtrl.Get("test-plugin", "conn-1", "core::v1::Pod", GetInput{ID: "pod-1"})` → success
2. `engineCtrl.List(...)` → success
3. `engineCtrl.Create(...)` → success
4. `engineCtrl.Update(...)` → success
5. `engineCtrl.Delete(...)` → success

### EI-004: Watch events flow engine → frontend (with subscription)

**Setup:** Engine controller with SDK provider. Start connection. Subscribe to Pod.
**Steps:**
1. `engineCtrl.SubscribeResource("test-plugin", "conn-1", "core::v1::Pod")`
2. Pod watcher in SDK emits add event
3. Assert engine receives the event (via its internal listener)
4. Assert event would be emitted via runtime.EventsEmit (mock the runtime)

### EI-005: Subscription gates event delivery

**Setup:** Engine controller with SDK provider. Pod watcher emitting events.
**Steps:**
1. Do NOT subscribe
2. Assert no events forwarded (subscription gate blocks)
3. Subscribe
4. Assert events now flow

### EI-006: Unsubscribe stops event delivery

**Setup:** Subscribe, verify events flow. Then unsubscribe.
**Action:** `engineCtrl.UnsubscribeResource("test-plugin", "conn-1", "core::v1::Pod")`
**Assert:** Events stop flowing after unsubscribe.

### EI-007: Ref-counted subscriptions

**Setup:** Subscribe twice. Unsubscribe once.
**Assert:** Events still flow (refcount > 0). Unsubscribe again → events stop.

### EI-008: Connection disconnect — watches stop, events stop

**Setup:** Subscribe to Pod. Events flowing.
**Action:** `engineCtrl.StopConnection("test-plugin", "conn-1")`
**Assert:** Events stop. Watches stopped in SDK.

### EI-009: OnPluginStop — cleans up all state

**Setup:** Multiple connections, watches, subscriptions active.
**Action:** `engineCtrl.OnPluginStop("test-plugin")`
**Assert:** All state cleaned up. No goroutine leaks.

### EI-010: Multi-plugin isolation

**Setup:** Register 2 plugins via InProcessBackend. Both have Pod resources.
**Steps:**
1. Start connections on both
2. Subscribe to Pod on plugin-1 only
3. Plugin-2's Pod events do NOT reach plugin-1's subscribers
4. Plugin-1's Pod events only reach plugin-1's subscribers

### EI-011: STATE events always flow (regardless of subscription)

**Setup:** Do NOT subscribe to any resource. Start connection.
**Action:** Watches emit state events (Syncing → Synced).
**Assert:** Engine receives state events. State events are NOT gated by subscription.

---

## 13. Test Specification: gRPC Conformance (Level 4)

**File:** `plugin-sdk/pkg/resource/plugin/resource_grpc_test.go`
**Component:** gRPC server/client stubs — proto round-trip verification

Uses `bufconn` for in-process gRPC (no TCP).

---

### GR-001: Get round-trip

**Setup:** bufconn server with mock Provider. Client via bufconn.
**Action:** `client.Get(ctx, "core::v1::Pod", GetInput{ID: "pod-1", Namespace: "default"})`
**Assert:** Request arrives at server with correct key, ID, namespace. Response round-trips correctly.

### GR-002: List round-trip

**Setup:** Mock Provider returns 5 items with labels and annotations.
**Action:** `client.List(ctx, "core::v1::Pod", ListInput{Namespace: "default", LabelSelector: "app=web"})`
**Assert:** Items, labels, annotations all survive proto serialization.

### GR-003: Find round-trip

**Setup:** Mock Provider returns results.
**Action:** `client.Find(ctx, "core::v1::Pod", FindInput{Query: "search-term"})`
**Assert:** Query string survives. Results round-trip correctly.

### GR-004: Create round-trip

**Setup:** Mock Provider returns created resource.
**Action:** `client.Create(ctx, "core::v1::Pod", CreateInput{Data: largeJSON})`
**Assert:** Data payload survives. Created resource round-trips.

### GR-005: Update round-trip

**Setup:** Mock Provider returns updated resource.
**Action:** `client.Update(ctx, "core::v1::Pod", UpdateInput{ID: "pod-1", Data: largeJSON})`
**Assert:** Data payload and ID survive.

### GR-006: Delete round-trip

**Setup:** Mock Provider returns success.
**Action:** `client.Delete(ctx, "core::v1::Pod", DeleteInput{ID: "pod-1"})`
**Assert:** ID survives. Success response round-trips.

### GR-007: StartConnection round-trip

**Setup:** Mock Provider returns ConnectionStatus.
**Action:** `client.StartConnection(ctx, "conn-1")`
**Assert:** Connection ID arrives at server. Status round-trips.

### GR-008: ListenForEvents — server-streaming round-trip

**Setup:** Mock Provider's ListenForEvents writes 3 events to sink, then blocks.
**Action:** `stream := client.ListenForEvents(ctx, sink)` — read from stream.
**Assert:** All 3 events received. Event types (add/update/delete) and payloads round-trip.

### GR-009: StreamAction — server-streaming round-trip

**Setup:** Mock Provider's StreamAction sends 5 ActionEvents.
**Action:** Read from stream.
**Assert:** All 5 events received with correct data.

### GR-010: WatchConnections — server-streaming round-trip

**Setup:** Mock Provider's WatchConnections sends connection update.
**Action:** Read from stream.
**Assert:** Connection list round-trips correctly.

### GR-011: Context cancellation propagated

**Setup:** Client cancels context during ListenForEvents stream.
**Action:** Cancel client context.
**Assert:** Server-side context is cancelled. Server's ListenForEvents returns.

### GR-012: Deadline propagation

**Setup:** Client sets 500ms deadline.
**Action:** `client.Get(ctx, "core::v1::Pod", GetInput{...})` — server's GetFunc checks ctx.Deadline().
**Assert:** Server's ctx has deadline within ~500ms of client's.

### GR-013: Large payload — List with 1000 items

**Setup:** Mock Provider returns 1000 items, each with ~1KB of data.
**Action:** `client.List(ctx, "core::v1::Pod", ListInput{...})`
**Assert:** All 1000 items received correctly. No truncation.

### GR-014: Error propagation — gRPC status codes

**Setup:** Mock Provider returns `ResourceOperationError{Code: NotFound, ...}`.
**Action:** `_, err := client.Get(ctx, "core::v1::Pod", GetInput{...})`
**Assert:** `err` is a `ResourceOperationError` with `Code == NotFound`. gRPC status code maps correctly.

### GR-015: Empty response handling

**Setup:** Mock Provider returns `{Items: []}` for List.
**Action:** `result, err := client.List(ctx, ...)`
**Assert:** `err == nil`, `len(result.Items) == 0`. No nil pointer issues.

### GR-016: WatchState round-trip

**Setup:** Mock Provider returns WatchConnectionSummary with per-resource states.
**Action:** `state, err := client.GetWatchState(ctx, "conn-1")`
**Assert:** Per-resource states survive serialization. State enum values correct.

### GR-017: GetResourceTypes round-trip

**Setup:** Mock Provider returns map of 10 resource types with full metadata.
**Action:** `types := client.GetResourceTypes(ctx, "conn-1")`
**Assert:** All 10 types with all fields round-trip correctly.

### GR-018: Per-resource watch lifecycle RPCs

**Setup:** Mock Provider tracks calls.
**Action:** Call each: EnsureResourceWatch, StopResourceWatch, RestartResourceWatch, IsResourceWatchRunning.
**Assert:** All RPCs round-trip. Arguments arrive at server correctly. Responses arrive at client.

---

## 14. Additional Edge Cases

This section adds deeper edge case coverage across every component. These tests target boundary
conditions, nil handling, malformed inputs, state machine correctness, goroutine lifecycle,
memory safety, and cross-component interaction edge cases that the core tests above don't cover.

---

### 14.1 Session & Context — Edge Cases

### SR-009: WithSession with nil session

**Setup:** `ctx := WithSession(context.Background(), nil)`
**Action:** `got := SessionFromContext(ctx)`
**Assert:** `got == nil` (does not panic, stores nil gracefully)

### SR-010: Concurrent reads of SessionFromContext

**Setup:** `ctx := WithSession(ctx, session)`
**Action:** Launch 100 goroutines all calling `SessionFromContext(ctx)` concurrently
**Assert:** All return same session. Run with `-race`. No race.

### SR-011: Session fields individually nil

**Setup:** `Session{Connection: nil, PluginConfig: nil, GlobalConfig: nil, RequestID: "", RequesterID: ""}`
**Action:** `ctx := WithSession(ctx, session)` → `got := SessionFromContext(ctx)`
**Assert:** All nil/empty fields preserved. No nil dereference.

### SR-012: Deeply nested context chain

**Setup:** Wrap context 100 times with `context.WithValue` before and after `WithSession`
**Action:** `got := SessionFromContext(deepCtx)`
**Assert:** Session still retrievable through deep nesting

---

### 14.2 resourcerRegistry — Edge Cases

### RR-026: Lookup with empty string key

**Setup:** Registry with Pod registered
**Action:** `r, err := registry.Lookup("")`
**Assert:** `err != nil` (invalid key), `r == nil`

### RR-027: Lookup with malformed key (missing parts)

**Setup:** Registry with Pod registered
**Action:** `r, err := registry.Lookup("core::v1")` (missing Kind)
**Assert:** `err != nil` (malformed key)

### RR-028: Lookup with extra separators

**Setup:** Registry with Pod registered
**Action:** `r, err := registry.Lookup("core::v1::Pod::extra")`
**Assert:** `err != nil` or handles gracefully

### RR-029: Registration with nil Resourcer

**Setup:** Attempt to register `{Meta: PodMeta, Resourcer: nil}`
**Action:** `err := registry.Register(reg)`
**Assert:** `err != nil` (nil resourcer rejected) or panic-safe

### RR-030: Registration with zero-value Meta

**Setup:** `ResourceRegistration{Meta: ResourceMeta{}, Resourcer: stubPod}`
**Action:** `err := registry.Register(reg)`
**Assert:** Either error (invalid meta) or generates a key from zero-value fields

### RR-031: Concurrent lookups during registration

**Setup:** Pre-register 10 resources
**Action:** Launch goroutines: 5 registering new resources, 10 doing lookups concurrently
**Assert:** No race. Run with `-race`. Lookups return either found or not-found (no partial state).

### RR-032: Large registry — 100+ registrations

**Setup:** Register 100 unique resources
**Action:** Look up each one
**Assert:** All found correctly. Performance doesn't degrade noticeably.

### RR-033: Pattern match does NOT apply to exact-registered keys

**Setup:** Register Pod exactly. Register pattern `"core::*": corePatternResourcer`
**Action:** `r, _ := registry.Lookup("core::v1::Pod")`
**Assert:** Returns exact Pod resourcer, not the core pattern

### RR-034: Multiple patterns — most specific wins

**Setup:** Patterns: `"core::v1::*": specificPattern`, `"core::*": broadPattern`, `"*": catchAll`
**Action:** `r1, _ := registry.Lookup("core::v1::ConfigMap")`; `r2, _ := registry.Lookup("core::v2::Future")`; `r3, _ := registry.Lookup("apps::v1::Unknown")`
**Assert:** `r1 == specificPattern`, `r2 == broadPattern`, `r3 == catchAll`

### RR-035: Resourcer implementing ALL optional interfaces

**Setup:** Register Pod that implements Watcher + SyncPolicyDeclarer + DefinitionProvider + ActionResourcer + ErrorClassifier + SchemaResourcer simultaneously
**Action:** Check each: `IsWatcher`, `GetSyncPolicy`, `GetDefinition`, `GetActionResourcer`, `GetErrorClassifier`, `GetSchemaResourcer`
**Assert:** All detected correctly. No interface assertion interference.

### RR-036: GetDefinition for pattern-matched resource

**Setup:** Pattern resourcer `"*"` does NOT implement DefinitionProvider. DefaultDefinition set.
**Action:** `def := registry.GetDefinition("unknown::v1::Foo")` (resolved via pattern)
**Assert:** Falls back to DefaultDefinition

### RR-037: GetWatcher for pattern-matched resource

**Setup:** Pattern resourcer `"*"` implements Watcher
**Action:** `w, ok := registry.GetWatcher("unknown::v1::Foo")`
**Assert:** `ok == true`, returns the pattern resourcer's Watcher

### RR-038: ListAll after removing/overwriting a registration

**Setup:** Register Pod, Deployment. Overwrite Pod with new resourcer.
**Action:** `metas := registry.ListAll()`
**Assert:** `len == 2` (not 3). Pod meta appears once.

---

### 14.3 connectionManager — Edge Cases

### CM-031: StartConnection with already-cancelled context

**Setup:** `ctx, cancel := context.WithCancel(ctx)` → `cancel()` before calling Start
**Action:** `status, err := mgr.StartConnection(ctx, "conn-1")`
**Assert:** `err != nil` (context cancelled). CreateClient NOT called.

### CM-032: StartConnection — CreateClient returns nil pointer without error

**Setup:** `CreateClientFunc` returns `(nil, nil)` — no error but nil client
**Action:** `status, err := mgr.StartConnection(ctx, "conn-1")`
**Assert:** Either `err != nil` (defensive check) or nil client stored. Subsequent GetClient returns the nil. Must not panic.

### CM-033: Multiple connections started in rapid succession

**Setup:** LoadConnections returns 10 connections
**Action:** Start all 10 sequentially in a tight loop
**Assert:** All 10 clients created. `CreateClientCalls == 10`. No race.

### CM-034: GetNamespaces returns nil vs empty slice

**Setup:** `GetNamespacesFunc` returns `(nil, nil)` for conn-1 and `([]string{}, nil)` for conn-2
**Action:** Get namespaces for both
**Assert:** Both cases handled without panic. Nil and empty may be treated equivalently.

### CM-035: GetNamespaces returns error

**Setup:** `GetNamespacesFunc` returns error
**Action:** `ns, err := mgr.GetNamespaces(ctx, "conn-1")`
**Assert:** `err != nil`, `ns == nil`

### CM-036: CheckConnection returns error

**Setup:** `CheckConnectionFunc` returns `(ConnectionStatus{}, errors.New("timeout"))`
**Action:** `status, err := mgr.CheckConnection(ctx, "conn-1")`
**Assert:** `err != nil`. Error propagated.

### CM-037: LoadConnections returns duplicate connection IDs

**Setup:** `LoadConnectionsFunc` returns `[{ID: "a"}, {ID: "a"}]` — duplicate IDs
**Action:** `conns, err := mgr.LoadConnections(ctx)`
**Assert:** Either deduplicates or returns error. Must not corrupt state.

### CM-038: LoadConnections called multiple times (refresh)

**Setup:** First call returns `[{ID: "a"}]`. Second call returns `[{ID: "a"}, {ID: "b"}]`.
**Action:** Call LoadConnections twice
**Assert:** Second call adds "b" without disrupting "a". If "a" is running, it stays running.

### CM-039: UpdateConnection for connection that doesn't exist

**Setup:** No connections loaded
**Action:** `updated, err := mgr.UpdateConnection(ctx, Connection{ID: "nonexistent"})`
**Assert:** `err != nil`

### CM-040: DeleteConnection for connection that doesn't exist

**Setup:** No connections loaded
**Action:** `err := mgr.DeleteConnection(ctx, "nonexistent")`
**Assert:** `err != nil`

### CM-041: Connection ID with special characters

**Setup:** Connection ID: `"conn/with:special chars & unicode 日本語"`
**Action:** Start, GetClient, Stop this connection
**Assert:** All operations work. IDs are opaque strings.

### CM-042: WatchConnections — provider channel closed

**Setup:** `WatchConnectionsFunc` returns a channel, then closes it after sending one update
**Action:** Watch processes the update
**Assert:** Watch goroutine handles channel close gracefully (returns or logs, no panic)

### CM-043: StopConnection during CreateClient (slow creation)

**Setup:** `CreateClientFunc` blocks for 2 seconds
**Action:** Start "conn-1" in goroutine. After 100ms, call StopConnection("conn-1") from another goroutine.
**Assert:** No race. Either creation completes then stops, or creation cancelled via context. Run with `-race`.

### CM-044: Concurrent LoadConnections calls

**Setup:** `LoadConnectionsFunc` has a 100ms delay
**Action:** Launch 5 concurrent LoadConnections calls
**Assert:** No race. Final state is consistent. Run with `-race`.

### CM-045: DestroyClient called with correct client pointer

**Setup:** `CreateClientFunc` returns specific pointer. `DestroyClientFunc` captures argument.
**Action:** Start then stop connection
**Assert:** DestroyClient received the exact same pointer that CreateClient returned.

---

### 14.4 watchManager — Edge Cases

### WM-051: Watch emits events at very high rate (backpressure)

**Setup:** Pod watcher emits 10,000 add events in a tight loop (no sleep between events)
**Action:** Start connection with ListenForEvents
**Assert:** All events eventually arrive at sink (none dropped). Watcher goroutine is not blocked indefinitely. Run with `-race`.

### WM-052: Watch emits events with wrong ResourceKey

**Setup:** Pod watcher emits `WatchAddPayload{ResourceKey: "wrong::key"}` instead of `"core::v1::Pod"`
**Action:** Start connection, observe events
**Assert:** Events arrive at sink with whatever ResourceKey the watcher set. SDK does NOT validate/rewrite ResourceKey. (The watcher is responsible for correctness.)

### WM-053: Watch emits state transitions in wrong order

**Setup:** Pod watcher emits `Synced` before `Syncing` (invalid transition)
**Action:** Start connection
**Assert:** SDK forwards events as-is. State tracking records whatever the watcher reports. No panic.

### WM-054: EnsureResourceWatch after connection already stopped

**Setup:** Start "conn-1", then stop it
**Action:** `err := mgr.EnsureResourceWatch(ctx, "conn-1", "core::v1::Pod")`
**Assert:** `err != nil` (connection no longer active)

### WM-055: RestartResourceWatch during error recovery backoff

**Setup:** Pod watcher fails. WatchManager is in backoff wait before retry.
**Action:** `mgr.RestartResourceWatch(ctx, "conn-1", "core::v1::Pod")` during backoff
**Assert:** Backoff cancelled, immediate restart with fresh context. No double-start.

### WM-056: StopResourceWatch during restart transition

**Setup:** Pod watcher running. Call RestartResourceWatch (which cancels old and starts new).
**Action:** While restart is in progress, call StopResourceWatch
**Assert:** Final state is stopped. No race. No goroutine leak.

### WM-057: Multiple rapid RestartResourceWatch calls

**Setup:** Pod watcher running
**Action:** Call RestartResourceWatch 10 times in rapid succession
**Assert:** Only one Watch goroutine running at the end. Previous goroutines all returned. Run with `-race`.

### WM-058: Watch returns error with partial events emitted

**Setup:** Pod watcher emits 3 add events, then returns error
**Action:** Start connection with ListenForEvents
**Assert:** Sink received the 3 events. Then receives Error state. Then restart begins.

### WM-059: Watch state machine — verify exact transitions

**Setup:** Pod watcher: Syncing → Synced → (ctx cancelled) → cleanup
**Action:** Record all state events
**Assert:** State progression is exactly: `[Syncing, Synced]`. No extra states.

### WM-060: Watch state machine — error path

**Setup:** Pod watcher: Syncing → Error (fails during sync)
**Action:** Record state events for first attempt
**Assert:** `[Syncing, Error]`. On retry: `[Syncing, Synced]` (or `[Syncing, Error]` again).

### WM-061: Watch state machine — Failed terminal state

**Setup:** Pod watcher always fails. Max retries = 2.
**Action:** Record all state events across all attempts
**Assert:** `[Syncing, Error, Syncing, Error, Syncing, Error, Failed]`. Failed is terminal — no more events.

### WM-062: GetWatchState for connection with zero watchers started

**Setup:** Start connection. All resourcers are SyncOnFirstQuery (none auto-started).
**Action:** `state, err := mgr.GetWatchState(ctx, "conn-1")`
**Assert:** Returns summary with all resources in "NotStarted" state. No error.

### WM-063: ListenForEvents when no watches are active

**Setup:** Start connection with zero watchable resources. Start ListenForEvents.
**Action:** Wait 1 second
**Assert:** No events. No error. ListenForEvents blocks until ctx cancelled.

### WM-064: ListenForEvents called before StartConnectionWatch

**Setup:** Start ListenForEvents first, then start connection watch
**Action:** Pod watcher emits events after connection starts
**Assert:** Events arrive at the listener that was registered early. No missed events.

### WM-065: Sink method called after listener context cancelled

**Setup:** Start listener with cancellable context. Pod watcher running.
**Action:** Cancel listener context. Pod watcher continues emitting.
**Assert:** SDK stops forwarding to the cancelled listener. No panic from writing to cancelled sink. Other listeners (if any) continue receiving.

### WM-066: Very large number of concurrent watches (50+ resources)

**Setup:** Register 50 watchable resources, all SyncOnConnect.
**Action:** Start connection
**Assert:** All 50 Watch goroutines started. All reach Synced state. Goroutine count is stable (50 watch goroutines + manager overhead). Run with `-race`.

### WM-067: Goroutine leak detection after full lifecycle

**Setup:** Record `runtime.NumGoroutine()` before test
**Action:** Start connection with 10 watchers. Wait for sync. Stop connection. Wait for cleanup.
**Assert:** `runtime.NumGoroutine()` returns to within ±2 of the initial count. No goroutine leak.

### WM-068: Watch that returns nil while context is still active

**Setup:** Pod watcher returns `nil` after 100ms WITHOUT ctx being cancelled (simulating a bug or intentional early exit)
**Action:** Start connection
**Assert:** SDK treats this as an unexpected exit. Either restarts (treating as recoverable) or marks as stopped with a warning. Must not loop infinitely with zero backoff.

### WM-069: StartConnectionWatch called twice for same connection

**Setup:** Start "conn-1" watches
**Action:** Call StartConnectionWatch("conn-1") again
**Assert:** Idempotent — no duplicate goroutines. Watch count unchanged.

### WM-070: EnsureResourceWatch for SyncNever resource

**Setup:** ConfigMap is SyncNever + implements Watcher
**Action:** `err := mgr.EnsureResourceWatch(ctx, "conn-1", "core::v1::ConfigMap")`
**Assert:** Decide on behavior: either start it (explicit override of SyncNever) or error. Document which.

### WM-071: Event ordering — events arrive in emission order

**Setup:** Pod watcher emits: Add("a"), Add("b"), Update("a"), Delete("b") in that order
**Action:** Check RecordingSink
**Assert:** Events arrive in the same order. No reordering.

### WM-072: Watch error — backoff capped at max interval

**Setup:** Pod watcher always fails. Backoff: initial=100ms, max=5s, multiplier=2x
**Action:** Let it retry 10 times. Track intervals.
**Assert:** Intervals increase: 100, 200, 400, 800, 1600, 3200, 5000, 5000, 5000, 5000 (capped at 5s)

### WM-073: Watch error — jitter in backoff

**Setup:** Pod watcher always fails. Backoff with jitter enabled.
**Action:** Track intervals across retries
**Assert:** Intervals are not perfectly deterministic — jitter adds randomness. No two intervals identical (with very high probability).

---

### 14.5 typeManager — Edge Cases

### TM-014: DiscoveryProvider returns duplicate key as static type

**Setup:** Registry has Pod. DiscoveryProvider also returns Pod with different description.
**Action:** `types := mgr.GetResourceTypes(ctx, "conn-1")`
**Assert:** Defined behavior: either discovery overrides static, static wins, or error. Document which.

### TM-015: DiscoveryProvider returns empty list

**Setup:** DiscoveryProvider returns `([]ResourceMeta{}, nil)` — no error, no types
**Action:** `types := mgr.GetResourceTypes(ctx, "conn-1")`
**Assert:** Returns static types only. No error.

### TM-016: DiscoverForConnection called multiple times for same connection

**Setup:** Call DiscoverForConnection("conn-1") twice. Second call returns different types.
**Action:** `types := mgr.GetResourceTypes(ctx, "conn-1")`
**Assert:** Second discovery replaces first. No accumulation of stale types.

### TM-017: GetResourceTypes for unknown connection

**Setup:** No discovery run for "conn-unknown"
**Action:** `types := mgr.GetResourceTypes(ctx, "conn-unknown")`
**Assert:** Returns static types only (discovery is per-connection).

### TM-018: Large number of discovered types (500+)

**Setup:** DiscoveryProvider returns 500 CRD types
**Action:** `types := mgr.GetResourceTypes(ctx, "conn-1")`
**Assert:** All 500 + static types returned. No performance issue.

### TM-019: GetResourceType for discovered type

**Setup:** Discovery returns `{Group: "custom", Version: "v1", Kind: "Widget"}`
**Action:** `meta, err := mgr.GetResourceType(ctx, "custom::v1::Widget")`
**Assert:** `err == nil`, returns the discovered meta with all fields

### TM-020: OnConnectionRemoved with nil DiscoveryProvider

**Setup:** No DiscoveryProvider configured
**Action:** `err := mgr.OnConnectionRemoved(ctx, &conn)`
**Assert:** No error, no panic. No-op.

---

### 14.6 Controller CRUD — Edge Cases

### OP-030: Get with empty resource key

**Setup:** Connection started
**Action:** `result, err := provider.Get(ctx, "", GetInput{ID: "pod-1"})`
**Assert:** `err != nil` (invalid key)

### OP-031: List with nil input fields

**Setup:** Connection started, Pod registered
**Action:** `result, err := provider.List(ctx, "core::v1::Pod", ListInput{})` — all fields zero-value
**Assert:** `err == nil`. Resourcer receives zero-value input. Returns whatever it returns.

### OP-032: Create with empty Data

**Setup:** Connection started, Pod registered
**Action:** `result, err := provider.Create(ctx, "core::v1::Pod", CreateInput{Data: nil})`
**Assert:** Passes through to resourcer. Resourcer decides if nil data is valid.

### OP-033: Update with empty Data

**Setup:** Connection started, Pod registered
**Action:** `result, err := provider.Update(ctx, "core::v1::Pod", UpdateInput{ID: "pod-1", Data: nil})`
**Assert:** Passes through to resourcer. Resourcer decides validity.

### OP-034: CRUD — resourcer panics

**Setup:** Pod `GetFunc` panics with `"unexpected error"`
**Action:** `result, err := provider.Get(ctx, "core::v1::Pod", GetInput{...})`
**Assert:** Panic recovered. `err != nil` with panic information. Provider remains functional for subsequent calls.

### OP-035: CRUD after provider shutdown/close

**Setup:** Create provider, close/shutdown it
**Action:** `result, err := provider.Get(ctx, "core::v1::Pod", GetInput{...})`
**Assert:** `err != nil` (provider closed/shutdown)

### OP-036: ErrorClassifier returns nil (doesn't classify)

**Setup:** ErrorClassifier's `ClassifyError` returns `nil` for some errors (meaning: don't wrap)
**Action:** Pod GetFunc returns error. ErrorClassifier returns nil for it.
**Assert:** Original raw error propagated (not wrapped, not swallowed)

### OP-037: ErrorClassifier itself panics

**Setup:** ErrorClassifier's `ClassifyError` panics
**Action:** Pod GetFunc returns error
**Assert:** Panic recovered. Error handling degrades gracefully (raw error returned or wrapped panic).

### OP-038: Get/List with very long resource key

**Setup:** Key: `"very.long.group.name.example.com::v1alpha1::VeryLongResourceKindNameThatExceedsNormalBounds"`
**Action:** Register resource with that key and call Get
**Assert:** Works correctly. Keys are opaque strings.

### OP-039: Concurrent CRUD operations across multiple connections

**Setup:** 3 connections started. 3 different resourcers.
**Action:** 30 concurrent CRUD calls: 10 per connection, mixed Get/List/Create
**Assert:** All complete. Correct client used for each connection. Run with `-race`.

### OP-040: List triggers EnsureResourceWatch exactly once (not per-call)

**Setup:** Secret is SyncOnFirstQuery. Connection started.
**Action:** Call List for Secret 10 times concurrently
**Assert:** EnsureResourceWatch called (Watch started). NOT started 10 times. Idempotent.

---

### 14.7 ConnectionLifecycleProvider — Edge Cases

### CL-017: StartConnection with context deadline during CreateClient

**Setup:** `CreateClientFunc` takes 2 seconds. Context has 500ms deadline.
**Action:** `status, err := provider.StartConnection(ctx, "conn-1")`
**Assert:** `err != nil` (deadline exceeded). No client stored.

### CL-018: StopConnection twice in rapid succession

**Setup:** Start "conn-1"
**Action:** Call StopConnection("conn-1") twice concurrently
**Assert:** No race. No double DestroyClient. Run with `-race`.

### CL-019: DeleteConnection while watches are syncing

**Setup:** Start "conn-1". Pod watcher syncing (takes 5s).
**Action:** `provider.DeleteConnection(ctx, "conn-1")` during sync
**Assert:** Watches cancelled. Client destroyed. Clean teardown.

### CL-020: UpdateConnection with same data (no-op)

**Setup:** Start "conn-1" with `{ID: "conn-1", Name: "Test"}`
**Action:** `provider.UpdateConnection(ctx, Connection{ID: "conn-1", Name: "Test"})` — same data
**Assert:** No client restart (data unchanged). No watch restart.

### CL-021: ListConnections when none loaded

**Setup:** No LoadConnections called
**Action:** `conns, err := provider.ListConnections(ctx)`
**Assert:** `err == nil`, `len(conns) == 0` (or error — depends on design)

### CL-022: Multiple concurrent StartConnection for different connections

**Setup:** 5 connections loaded
**Action:** Start all 5 concurrently
**Assert:** All 5 clients created. All SyncOnConnect watches started. Run with `-race`.

### CL-023: GetConnection preserves all connection fields

**Setup:** Connection loaded with ID, Name, Data map, Labels map, all populated
**Action:** `conn, _ := provider.GetConnection(ctx, "conn-1")`
**Assert:** All fields preserved: Name, Data entries, Labels entries. Nothing lost.

---

### 14.8 WatchProvider — Edge Cases

### WP-011: StartConnectionWatch twice — idempotent

**Setup:** Start connection. Start watches.
**Action:** `provider.StartConnectionWatch(ctx, "conn-1")` again
**Assert:** No error. No duplicate goroutines. Watch count unchanged.

### WP-012: StopConnectionWatch when no watches were started

**Setup:** Start "conn-1" but all resources are SyncNever (no watches auto-started)
**Action:** `err := provider.StopConnectionWatch(ctx, "conn-1")`
**Assert:** No error. No-op.

### WP-013: GetWatchState after all watches stopped

**Setup:** Start "conn-1", watches start. Stop all watches.
**Action:** `state, _ := provider.GetWatchState(ctx, "conn-1")`
**Assert:** Summary shows all resources as stopped/inactive.

### WP-014: ListenForEvents with nil sink

**Setup:** Start connection with watches
**Action:** `err := provider.ListenForEvents(ctx, nil)`
**Assert:** `err != nil` (nil sink rejected). No panic.

### WP-015: EnsureResourceWatch with empty resource key

**Setup:** Start connection
**Action:** `err := provider.EnsureResourceWatch(ctx, "conn-1", "")`
**Assert:** `err != nil` (invalid key)

### WP-016: IsResourceWatchRunning immediately after EnsureResourceWatch

**Setup:** Start connection. EnsureResourceWatch for Secret.
**Action:** Immediately check `IsResourceWatchRunning("conn-1", "core::v1::Secret")`
**Assert:** Returns true even before Watch goroutine has emitted any state. (The goroutine has been launched.)

---

### 14.9 ActionProvider — Edge Cases

### AP-008: GetActions — connection not started

**Setup:** No StartConnection
**Action:** `actions, err := provider.GetActions(ctx, "core::v1::Pod")`
**Assert:** `err != nil` (connection not started)

### AP-009: ExecuteAction — connection not started

**Setup:** No StartConnection
**Action:** `result, err := provider.ExecuteAction(ctx, "core::v1::Pod", "restart", ActionInput{})`
**Assert:** `err != nil`

### AP-010: ExecuteAction — correct client and meta passed

**Setup:** Pod ActionResourcer captures arguments
**Action:** `provider.ExecuteAction(ctx, "core::v1::Pod", "restart", ActionInput{...})`
**Assert:** Captured client matches connection's client. Captured meta matches PodMeta.

### AP-011: StreamAction — error during streaming

**Setup:** Pod ActionResourcer.StreamAction sends 2 events then returns error
**Action:** Read from stream
**Assert:** Received 2 events. Then got error. Stream closed.

### AP-012: GetActions — pattern resourcer with ActionResourcer

**Setup:** Pattern `"*"` resourcer implements ActionResourcer
**Action:** `actions, err := provider.GetActions(ctx, "unknown::v1::Foo")`
**Assert:** Returns actions from pattern resourcer

---

### 14.10 EditorSchemaProvider — Edge Cases

### SP-004: GetEditorSchemas — SchemaProvider returns error

**Setup:** ConnectionProvider implements SchemaProvider. `GetEditorSchemas` returns error.
**Action:** `schemas, err := provider.GetEditorSchemas(ctx, "conn-1")`
**Assert:** `err != nil`. Error propagated.

### SP-005: GetEditorSchemas — SchemaProvider returns empty list

**Setup:** `GetEditorSchemas` returns `([]EditorSchema{}, nil)` — no error, no schemas
**Action:** `schemas, err := provider.GetEditorSchemas(ctx, "conn-1")`
**Assert:** `err == nil`, `len(schemas) == 0`

### SP-006: GetEditorSchemas — per-resource SchemaResourcer

**Setup:** Pod implements SchemaResourcer. ConnectionProvider implements SchemaProvider.
**Action:** `schemas, err := provider.GetEditorSchemas(ctx, "conn-1")` — connection-level
**Assert:** Returns connection-level schemas. (Per-resource schemas fetched separately via GetResourceDefinition or a dedicated API.)

---

### 14.11 Full Lifecycle Scenarios — Edge Cases

### SC-013: Rapid connect/disconnect cycles

**Setup:** Pod watchable
**Steps:**
1. Repeat 10 times: StartConnection → StopConnection
2. Assert no goroutine leak after all cycles
3. `runtime.NumGoroutine()` stable
4. Run with `-race`

### SC-014: Connection with zero registered resources

**Setup:** No resources registered (empty Resources list, no patterns)
**Action:** StartConnection → ListConnections → StopConnection
**Assert:** Connection lifecycle works. No watches started (nothing to watch). CRUD returns "resource not found" for any key.

### SC-015: Discovery changes between connect cycles

**Setup:** DiscoveryProvider returns `[WidgetCRD]` on first connect, `[WidgetCRD, GadgetCRD]` on second.
**Steps:**
1. Start "conn-1" → discover 1 CRD
2. Stop "conn-1"
3. Start "conn-1" again → discover 2 CRDs
4. `HasResourceType(ctx, "custom::v1::Gadget") == true`

### SC-016: All watches fail simultaneously

**Setup:** 5 watchable resources, ALL watchers return error after 100ms
**Action:** Start connection
**Assert:** All 5 enter Error state. All retry. If all exhaust retries → all reach Failed. Connection itself stays active (client is fine — watches are what failed).

### SC-017: Connection stays up but all watches fail permanently

**Setup:** Pod and Deployment watchers always fail. Max retries = 2.
**Steps:**
1. Start "conn-1" → watches start → fail → retry → fail → Failed
2. Assert connection is still "up" (client alive)
3. CRUD operations still work (List/Get succeed without watch)
4. Watches are in Failed state

### SC-018: Concurrent CRUD + watch restart

**Setup:** Pod watchable and running
**Steps:**
1. Concurrently: 10 Get/List calls + RestartResourceWatch
2. Assert all CRUD calls complete (may see brief interruption during restart)
3. Watch restarts successfully
4. Run with `-race`

### SC-019: ListenForEvents — listener disconnects and reconnects

**Setup:** Pod watchable, events flowing
**Steps:**
1. Start listener1 → receives events
2. Cancel listener1's context → stops receiving
3. Start listener2 (new context) → receives events from current state onward
4. Assert listener2 gets events but not historical events from before listener1

### SC-020: Provider reuse after shutdown and restart

**Setup:** Build provider. StartConnection, do CRUD, StopConnection.
**Action:** StartConnection again on same provider.
**Assert:** Works correctly. State properly cleaned up between sessions.

### SC-021: Watch with many connections — selective disconnect

**Setup:** 5 connections, each with Pod and Deployment watching
**Steps:**
1. Stop "conn-3" only
2. Assert "conn-3" watches stopped
3. Assert "conn-1", "conn-2", "conn-4", "conn-5" watches still running
4. Events from conn-3 stop; events from others continue

### SC-022: Slow CreateClient blocks only that connection

**Setup:** "conn-1" CreateClient takes 3 seconds. "conn-2" CreateClient is instant.
**Steps:**
1. Start "conn-1" in goroutine
2. Start "conn-2"
3. Assert "conn-2" is connected and watches running while "conn-1" is still connecting
4. Eventually "conn-1" also connects

---

### 14.12 Performance Invariants — Edge Cases

### PI-007: P1 — Events buffered but not lost before ListenForEvents

**Setup:** Start "conn-1". 1000 events emitted during sync. ListenForEvents called after sync.
**Action:** Start ListenForEvents
**Assert:** Either: (a) events are buffered and delivered, or (b) only new events delivered (documented behavior). No events lost after listener attaches.

### PI-008: Memory stability during sustained watch

**Setup:** Pod watcher emits 1 event per millisecond for 5 seconds (5000 events)
**Action:** ListenForEvents consumes them
**Assert:** Memory usage (via `runtime.ReadMemStats`) doesn't grow unboundedly. Events consumed, not accumulated.

### PI-009: Goroutine count stabilizes after initial sync

**Setup:** 20 watchable resources, all SyncOnConnect
**Action:** Start connection. Wait for all to reach Synced.
**Assert:** `runtime.NumGoroutine()` is stable (±2) for 1 second after sync completes. No goroutine churn.

### PI-010: P5 — Event delivery latency under load

**Setup:** 10 resources all emitting events concurrently (100 events/sec total). ListenForEvents active.
**Action:** Record timestamp at emission and reception for a sample of events.
**Assert:** p99 latency < 100ms in-process. Average < 10ms.

---

### 14.13 Engine Integration — Edge Cases

### EI-012: Subscribe to non-existent resource key

**Setup:** Engine controller with SDK provider. Pod registered only.
**Action:** `engineCtrl.SubscribeResource("test-plugin", "conn-1", "apps::v1::NonExistent")`
**Assert:** No crash. Subscription recorded but no events will ever arrive for it.

### EI-013: Subscribe before connection started

**Setup:** Engine controller with SDK provider. No StartConnection.
**Action:** `engineCtrl.SubscribeResource("test-plugin", "conn-1", "core::v1::Pod")`
**Assert:** Subscription recorded. When connection eventually starts, events flow.

### EI-014: Events arrive out of order (add after delete)

**Setup:** SDK watcher emits: Add("pod-1"), Delete("pod-1"), Add("pod-1") (recreated)
**Action:** Engine receives via ListenForEvents, forwards to subscribed frontend.
**Assert:** All 3 events forwarded in order. Engine doesn't deduplicate or reorder.

### EI-015: Engine handles SDK Provider returning errors gracefully

**Setup:** SDK Provider's List returns error. Engine calls List.
**Action:** `result, err := engineCtrl.List("test-plugin", "conn-1", "core::v1::Pod", ListInput{})`
**Assert:** Error propagated to caller. Engine state not corrupted.

### EI-016: Two plugins with same resource key

**Setup:** plugin-1 and plugin-2 both register "core::v1::Pod"
**Steps:**
1. Subscribe to Pod on plugin-1
2. Subscribe to Pod on plugin-2
3. Pod events from plugin-1's watcher → only plugin-1 subscriber
4. Pod events from plugin-2's watcher → only plugin-2 subscriber
5. No cross-contamination

### EI-017: Rapid Subscribe/Unsubscribe cycles

**Setup:** Engine with SDK provider. Connection started. Events flowing.
**Action:** Subscribe and Unsubscribe to Pod 100 times in rapid succession
**Assert:** No race. Final state is consistent (refcount == 0). Run with `-race`.

### EI-018: OnPluginStart called twice for same plugin

**Setup:** Engine controller
**Action:** Call OnPluginStart("test-plugin", ...) twice
**Assert:** Either replaces first (clean) or errors. No duplicate state.

### EI-019: Engine controller concurrent CRUD from multiple goroutines

**Setup:** Engine with SDK provider. Connection started.
**Action:** 20 goroutines each doing List/Get operations on different resources
**Assert:** All complete successfully. Run with `-race`.

---

### 14.14 gRPC Conformance — Edge Cases

### GR-019: Nil fields in request — Get with empty namespace

**Setup:** bufconn server/client
**Action:** `client.Get(ctx, "core::v1::Node", GetInput{ID: "node-1", Namespace: ""})` — cluster-scoped
**Assert:** Empty namespace survives round-trip. Server receives empty string, not nil.

### GR-020: Unicode in resource data

**Setup:** Mock Provider returns resource with Data containing `"name": "日本語テスト"` and `"emoji": "🚀"`
**Action:** `client.Get(ctx, "core::v1::Pod", GetInput{...})`
**Assert:** Unicode data survives proto serialization. Exact bytes preserved.

### GR-021: Binary data in resource payload

**Setup:** Mock Provider returns resource with Data containing non-UTF8 binary bytes
**Action:** `client.Get(ctx, "core::v1::Secret", GetInput{...})`
**Assert:** Binary data survives. Proto `bytes` field preserves arbitrary bytes.

### GR-022: Concurrent RPCs on same gRPC connection

**Setup:** Single bufconn client
**Action:** 20 concurrent Get/List calls via the same client
**Assert:** All complete. gRPC multiplexing works. No corrupted responses.

### GR-023: Server shutdown during streaming

**Setup:** Start ListenForEvents stream. Server is sending events.
**Action:** `srv.GracefulStop()` during active stream
**Assert:** Client receives EOF or transport error. No panic on either side.

### GR-024: Client disconnects during streaming — server cleanup

**Setup:** Start ListenForEvents stream.
**Action:** Close client connection while server is still streaming
**Assert:** Server detects disconnect (context cancelled). ListenForEvents returns. No goroutine leak.

### GR-025: Empty connection ID in RPC

**Setup:** bufconn server/client
**Action:** `client.StartConnection(ctx, "")` — empty connection ID
**Assert:** Server receives empty string. Behavior depends on server validation. No crash.

### GR-026: Very large metadata maps in resource

**Setup:** Resource with 1000 labels and 1000 annotations
**Action:** `client.Get(ctx, "core::v1::Pod", GetInput{...})`
**Assert:** All labels and annotations survive round-trip.

### GR-027: Streaming RPC — zero events

**Setup:** Mock Provider's ListenForEvents blocks without emitting any events
**Action:** Start stream, wait 500ms, cancel
**Assert:** Client receives no events. No error. Clean cancellation.

### GR-028: Error codes for all ResourceOperationError types

**Setup:** Mock Provider returns errors with different codes: NotFound, Unauthorized, Timeout, Conflict, Invalid, Internal
**Action:** For each: `client.Get(ctx, "core::v1::Pod", GetInput{...})`
**Assert:** Each code maps to correct gRPC status code and back. Round-trip preserves: Code, Title, Message, Suggestions.

### GR-029: Streaming RPC — backpressure from slow client

**Setup:** Mock Provider's ListenForEvents emits 10,000 events rapidly. Client reads slowly (10ms per event).
**Action:** Start stream
**Assert:** All events eventually received. gRPC flow control handles backpressure. Server doesn't OOM.

---

## 15. Filter/Query/MCP Tests (FR-11, FR-12)

These tests cover the structured filter system, capability introspection, and MCP-readiness
additions from `plans/resourcer-refactor/13-query-filter-mcp-design.md`.

### 15.1 FilterableProvider Tests

| ID | Test | Assertion |
|----|------|-----------|
| FQ-001 | FilterPredicate with valid field+operator accepted | SDK validation passes, predicate reaches Resourcer's Find() |
| FQ-002 | FilterPredicate with unknown field path rejected | Error returned before calling Find() (if FilterableProvider implemented) |
| FQ-003 | FilterPredicate with invalid operator for field rejected | Error: operator not in field's Operators list |
| FQ-004 | FilterExpression with AND logic | All predicates passed to Find() |
| FQ-005 | FilterExpression with OR logic | All predicates passed to Find() |
| FQ-006 | Nested FilterExpression (AND of ORs) | Groups flattened correctly in Find() input |
| FQ-007 | FilterableProvider.FilterFields() result cached per resource registration | Second call returns cached result without calling FilterFields() again |
| FQ-008 | Resourcer without FilterableProvider: filters pass through unvalidated | Find() receives FilterExpression without SDK validation |

### 15.2 TextSearchProvider Tests

| ID | Test | Assertion |
|----|------|-----------|
| FQ-009 | TextSearchProvider.Search() invoked when FindInput.TextQuery is set | Search() called with query string and limit |
| FQ-010 | FindInput with both Filters and TextQuery | Both applied — results match filters AND text query |
| FQ-011 | Resourcer without TextSearchProvider: TextQuery ignored | Find() called with Filters only, no error |

### 15.3 ResourceCapabilities Tests

| ID | Test | Assertion |
|----|------|-----------|
| FQ-012 | ResourceCapabilities auto-derived from type assertions | All flags correctly set based on implemented interfaces |
| FQ-013 | Watchable=true iff Resourcer implements Watcher | Type assertion check |
| FQ-014 | Filterable=true iff Resourcer implements FilterableProvider | Type assertion check |
| FQ-015 | HasActions=true iff Resourcer implements ActionResourcer | Type assertion check |
| FQ-016 | HasSchema=true iff Resourcer implements ResourceSchemaProvider | Type assertion check |
| FQ-017 | Searchable=true iff Resourcer implements TextSearchProvider | Type assertion check |
| FQ-018 | NamespaceScoped=true iff ResourceDefinition.NamespaceAccessor != "" | Check definition field |
| FQ-019 | ScaleHint populated from ScaleHintProvider | Type assertion + value check |
| FQ-020 | ScaleHint nil when ScaleHintProvider not implemented | Nil check |

### 15.4 ResourceSchemaProvider Tests

| ID | Test | Assertion |
|----|------|-----------|
| FQ-021 | GetResourceSchema() returns JSON Schema from ResourceSchemaProvider | Valid JSON Schema bytes returned |
| FQ-022 | GetResourceSchema() returns nil when not implemented | Nil, no error |

### 15.5 ActionDescriptor Enhancement Tests

| ID | Test | Assertion |
|----|------|-----------|
| FQ-023 | ActionDescriptor.ParamsSchema serializes through gRPC | JSON Schema bytes preserved across boundary |
| FQ-024 | ActionDescriptor.OutputSchema serializes through gRPC | Same |
| FQ-025 | ActionDescriptor.Dangerous flag propagates to engine | Bool preserved |
| FQ-026 | ActionDescriptor with nil ParamsSchema | No error, field omitted in serialization |

### 15.6 Input Type Tests

| ID | Test | Assertion |
|----|------|-----------|
| FQ-027 | OrderField multi-field ordering preserved through gRPC | Multiple OrderField entries round-trip correctly |
| FQ-028 | PaginationParams.Cursor propagates through gRPC | Cursor string preserved |
| FQ-029 | PaginationResult.NextCursor returned from Find/List response | NextCursor string preserved |
| FQ-030 | FindInput.Filters=nil equivalent to List (no filtering) | Returns all resources |
| FQ-031 | FindInput with empty FilterExpression (no predicates) | Returns all resources |

### 15.7 TypeProvider Introspection Tests

| ID | Test | Assertion |
|----|------|-----------|
| FQ-032 | GetResourceCapabilities returns correct flags | All capability flags match interface assertions |
| FQ-033 | GetFilterFields returns declared fields | FilterField list matches FilterableProvider output |
| FQ-034 | GetFilterFields returns [] for non-filterable resource | Empty slice, no error |

---

## 16. Implementation Order Reference

After all tests are written (red phase), implement in this order:

```
Component                    Core Tests      Edge Cases      Estimated Effort
──────────────────────────────────────────────────────────────────────────────
1. Session (types)           SR-001..008     SR-009..012     ~1 hour
2. resourcerRegistry         RR-001..025     RR-026..038     ~4 hours
3. connectionManager         CM-001..030     CM-031..045     ~5 hours
4. watchManager              WM-001..050     WM-051..073     ~10 hours (most complex)
5. typeManager               TM-001..013     TM-014..020     ~3 hours
6. resourceController        OP-001..029     OP-030..040     ~6 hours
                             CL-001..016     CL-017..023
                             WP-001..010     WP-011..016
                             TP-001..005
                             AP-001..007     AP-008..012
                             SP-001..003     SP-004..006
7. Scenarios                 SC-001..012     SC-013..022     ~4 hours
8. Performance               PI-001..006     PI-007..010     ~3 hours
9. gRPC stubs                GR-001..018     GR-019..029     ~5 hours
10. Engine integration       EI-001..011     EI-012..019     ~4 hours
──────────────────────────────────────────────────────────────────────────────
Total test cases:            ~218 core       ~113 edge       = ~331 total
```

Each component: run tests (red) → implement minimum → run tests (green) → refactor → next.

---

## 15. Green Criteria

### Per-Component Green

A component is "green" when:
1. All its tests pass
2. `go test -race` passes for the package
3. `go vet` passes for the package
4. No `TODO` or `panic("not implemented")` remains in the implementation

### Full Green (Ready for Migration)

All of these must be true:
1. **Every test in this document passes** (331 tests)
2. **`go test -race ./plugin-sdk/pkg/resource/...`** — zero failures, zero races
3. **`go test -race ./backend/pkg/plugin/resource/...`** — zero failures (engine integration)
4. **`go test ./plugin-sdk/pkg/resource/plugin/...`** — gRPC conformance passes
5. **`go vet ./plugin-sdk/... ./backend/...`** — zero issues
6. **`go build ./plugin-sdk/...`** — no go-plugin imports in non-gRPC packages
7. **Coverage targets met** (doc 10 §10): watchManager ≥95%, controller ≥90%, registry ≥95%

Only after all green criteria pass: begin K8s plugin migration (doc 09 §7 Step 4).

---

## 16. Test Naming Convention

All test functions follow Go convention and include the test ID for traceability:

```go
func TestSessionFromContext_Missing(t *testing.T) {          // SR-002
func TestRegistry_LookupExactMatch(t *testing.T) {           // RR-001
func TestConnMgr_StartConnection_AlreadyStarted(t *testing.T) { // CM-004
func TestWatchMgr_EnsureResourceWatch_LazyStart(t *testing.T) { // WM-010
func TestProvider_Get_HappyPath(t *testing.T) {               // OP-001
func TestScenario_ConnectAndBrowse(t *testing.T) {             // SC-001
func TestGRPC_Get_RoundTrip(t *testing.T) {                    // GR-001
func TestIntegration_Dispense(t *testing.T) {                  // EI-001
```

Each test function has a comment `// <ID>` linking back to this spec.

---

## 17. Relationship to Other Documents

| Document | Relationship |
|----------|-------------|
| **Doc 09** (Interface Design) | This spec tests every interface defined there. Test IDs map to §4 interfaces. |
| **Doc 10** (Testing Strategy) | Doc 10 is the strategy/philosophy. This doc is the exhaustive spec. Doc 10's test tables are a subset of what's here. |
| **Doc 08** (Data Flow Requirements) | Performance invariants P1-P8 tested in §11 (PI-001..006). Frontend invariants (P2-P4) out of scope. |
| **Doc 09 §7** (Migration Strategy) | This doc's Phase 3-4 (green + full green) corresponds to doc 09 §7 Steps 1-2. Migration only starts after full green. |
| **Doc 20** (SDK Protocol Versioning) | Versioning tests in §18 (VR-001..012) verify adapter conformance, version negotiation, and multi-capability version handling. |

---

## 18. Versioning & Adapter Tests (Doc 20)

These tests verify the versioning infrastructure from doc 20. They are part of the v1
implementation since the versioned package layout and adapter pattern are built from day 1.

### 18.1 Adapter Conformance (VR-001..005)

Each adapter must satisfy the canonical `ResourceProvider` interface identically.

| ID | Test | Setup | Action | Assert |
|----|------|-------|--------|--------|
| VR-001 | AdapterV1 Get round-trip | Create AdapterV1 with mock v1 gRPC client returning pod data | `adapter.Get(ctx, "core::v1::Pod", GetInput{ID: "pod-1"})` | Returns canonical `GetResult` with correct data. Mock client received correct v1 proto `GetRequest`. |
| VR-002 | AdapterV1 Find with filters | Create AdapterV1 with mock v1 gRPC client | `adapter.Find(ctx, key, FindInput{Filters: expr})` | FilterExpression correctly translated to v1 proto `FilterExpression`. Response translated back to canonical `FindResult`. |
| VR-003 | AdapterV1 unsupported feature returns nil | Create AdapterV1 with mock v1 client that doesn't implement relationships | `adapter.GetRelationships(ctx, connID, key)` | Returns `(nil, nil)` — not an error, just unsupported. |
| VR-004 | AdapterV1 context cancellation propagates | Create AdapterV1 with mock that blocks | Cancel context during `adapter.List()` | Returns `context.Canceled` error. Mock client received cancelled context. |
| VR-005 | Adapter conformance suite | Run shared `RunAdapterConformanceTests(t, adapter)` against AdapterV1 | All canonical behaviors tested (Get, List, Find, Create, Update, Delete, errors, context) | All pass. Same suite will be run against future AdapterV2. |

### 18.2 Version Negotiation (VR-006..009)

| ID | Test | Setup | Action | Assert |
|----|------|-------|--------|--------|
| VR-006 | v1 plugin with v1 engine | Start test plugin (v1 only), engine supports {v1} | Check `NegotiatedVersion()` | Returns 1. Plugin loads successfully. |
| VR-007 | v1 plugin with v1+v2 engine | Start test plugin (v1 only), engine supports {v1, v2} | Check `NegotiatedVersion()` | Returns 1. Plugin loads via v1 adapter. CRUD works. |
| VR-008 | Incompatible plugin fails cleanly | Start test plugin (v99), engine supports {v1} | Attempt to load | Returns clear error: "unsupported SDK protocol version". No panic. |
| VR-009 | NegotiatedVersion stored per plugin | Load two plugins: A (v1), B (v1) | Check `loadedPlugins["A"].NegotiatedProtocol`, `loadedPlugins["B"].NegotiatedProtocol` | Both store version independently. |

### 18.3 Multi-Capability Versioning (VR-010..012)

| ID | Test | Setup | Action | Assert |
|----|------|-------|--------|--------|
| VR-010 | All capabilities dispensed at negotiated version | Load plugin with v1, implements resource+exec+logs | Dispense all capabilities | Each returns the correct v1 adapter type. |
| VR-011 | Lifecycle GetInfo returns supported versions | Load v1 plugin | Call `lifecycle.GetInfo()` | Response contains `supported_protocol_versions: [1]`, `sdk_protocol_version: 1`. |
| VR-012 | Lifecycle GetCapabilities lists implemented capabilities | Load plugin implementing resource+exec (not logs/metric/networker/settings) | Call `lifecycle.GetCapabilities()` | Returns `["resource", "exec"]`. Engine only creates adapters for those two. |
