# Resource Plugin SDK — Interface Design

## 1. Context & Motivation

The current plugin SDK uses a function-passing pattern via `ResourcePluginOpts[ClientT]` — a 17-field struct where connection lifecycle, watch/informer creation, sync policies, error classification, schema generation, and resource definitions are all passed as individual functions or maps. This is not idiomatic Go.

**Problems:**
1. Not idiomatic Go — should be interfaces, not function bags
2. No `context.Context` propagation on connection lifecycle functions
3. Sync policies are in a central map, disconnected from the resources they apply to
4. Resource definitions (column defs) are separate from resourcer registration
5. ConnectionManager is assembled internally from 8 individual functions
6. No lifecycle hooks for watch/connection state changes
7. Informer system has complex two-phase init (RegisterResource N times → Start once) with three separate event channels, a connection-scoped factory pattern that disconnects watch from CRUD, and state tracking duplicated across InformerManager and engine controller
8. Error classification is a function field, not an interface

**Goal:** Replace everything with clean interfaces. Breaking changes are acceptable — there's only one plugin (Kubernetes) and it will be migrated as part of this work.

---

## 2. Business Requirements

### 2.1 Current Requirements

| ID | Requirement |
|----|------------|
| CR-1 | **CRUD operations** on arbitrary resource types (Get, List, Create, Update, Delete) |
| CR-2 | **Typed client per connection** — plugin authors define their client type, SDK manages lifecycle |
| CR-3 | **Connection lifecycle** — load from config, create clients, start/stop, check health, watch for changes |
| CR-4 | **Namespace support** (optional) — some backends have namespaced resources |
| CR-5 | **Watch support** (optional) — real-time ADD/UPDATE/DELETE events via `Watcher[ClientT]` interface |
| CR-6 | **Sync policies** — control when watches start: on connect, on first query, or never |
| CR-7 | **Dynamic resource discovery** (optional) — discover resource types at runtime (CRDs) |
| CR-8 | **Resource definitions** — column defs, ID/namespace accessors, supported operations for UI table rendering |
| CR-9 | **Resource actions** (optional) — named operations beyond CRUD (restart, scale, drain). One-shot or streaming |
| CR-10 | **Editor schemas** (optional) — JSON/YAML schemas for Monaco editor validation |
| CR-11 | **Error classification** — structured `ResourceOperationError` with code, title, message, suggestions |
| CR-12 | **Pull-based data model** — zero unsolicited IPC during sync (see `plans/resourcer-refactor/08-resource-data-flow-requirements.md`) |
| CR-13 | **Pattern/wildcard resourcers** — single resourcer handles unknown types (CRDs via `"*"` pattern) |
| CR-14 | **Plugin crash recovery** — engine detects crashes and re-establishes gRPC with backoff |
| CR-15 | **SDK testable without go-plugin** — all SDK internals and the Provider interface testable in-process with mock implementations. No gRPC or process spawning required for unit/integration tests. Follows the `PluginBackend`/`InProcessBackend` pattern already established in the engine (`backend/pkg/plugin/types/backend.go`). |
| CR-16 | **Minimize serialization overhead** — resource data crosses the gRPC boundary as pre-serialized JSON bytes (`bytes` proto field), not `google.protobuf.Struct`. Eliminates 2 of 4 deep copies in the current pipeline. UPDATE events carry only the new state (OldData dropped). See `plans/resourcer-refactor/12-resource-data-serialization.md` for full analysis. |

### 2.2 Future Requirements

These inform design decisions but are NOT implemented in this refactor.

| ID | Requirement | Design Impact |
|----|------------|---------------|
| FR-1 | **Central query interface** | Engine-side query router maps `resourceKey → pluginID`. SDK just needs CRUD — no SDK changes. |
| FR-2 | **Central lookup index** | Engine-side full-text index subscribes to all watch events. SDK keeps streaming ALL events. |
| FR-3 | **Cross-plugin querying** | Engine routes queries between plugins. Resourcer must support non-UI callers. |
| FR-4 | **Subscribable updates** | Any consumer (not just frontend) subscribes to resource changes. Engine generalizes subscriptions. |
| FR-5 | **Watch lifecycle management** | Fine-grained start/stop/restart of individual resource watches. **Included in this design** — SDK's `watchManager` manages per-resource Watch goroutine lifecycle via context cancellation. `WatchProvider` exposes `EnsureResourceWatch`, `StopResourceWatch`, `RestartResourceWatch` on the gRPC boundary. |
| FR-6 | **Connection lifecycle hooks** | Plugins react to state changes (connected, disconnected, credentials rotated). |
| FR-7 | **Credential providing** | Plugin A provides credentials to Plugin B. New `CredentialProvider` capability + engine routing. Connection type needs `CredentialRef`. |
| FR-8 | **Resource relationship graph** | Dependencies between resources (Pod → Node). Optional `RelationshipProvider` interface. |
| FR-9 | **Bulk operations** | Create/Update/Delete multiple resources in one call. Optional `BulkWriter` interface. |
| FR-10 | **Resource validation** | Pre-create/pre-update validation. Optional `Validator` interface. |
| FR-11 | **AI/agent queryability** | Structured filter types (`FilterPredicate`, `FilterExpression`), `FilterableProvider` optional interface on Resourcer, `ResourceCapabilities` auto-derived from type assertions. Agents discover filter fields, build typed queries, avoid listing everything. See `plans/resourcer-refactor/13-query-filter-mcp-design.md`. |
| FR-12 | **MCP tool auto-generation** | Plugin resources auto-exposed as MCP tools. Requires: `ActionDescriptor.ParamsSchema` (JSON Schema), `ResourceSchemaProvider` for resource data schemas, `ResourceCapabilities` flags, `FilterField` declarations — all needed to generate MCP `inputSchema`/`outputSchema`. See doc 13. |
| FR-13 | **IDE query/command palette** | `TextSearchProvider` optional interface, `FindInput.TextQuery` field, structured filters usable from command palette. See doc 13. |
| FR-14 | **Cursor-based pagination** | `PaginationParams.Cursor` / `PaginationResult.NextCursor` for backends with continuation tokens (K8s `continue`, DynamoDB `LastEvaluatedKey`). See doc 13. |
| FR-15 | **Protocol versioning** | Versioned proto packages (`omniview.sdk.resource.v1`), go-plugin `VersionedPlugins`, engine adapters per version. Enables future breaking changes without flag-day migrations. See `plans/resourcer-refactor/20-sdk-protocol-versioning.md`. |

---

## 3. Design Principles

1. **`context.Context` first on all methods** — cancellation, deadlines, and tracing propagate naturally.
2. **Interfaces at boundaries, structs internally** — plugin authors implement interfaces; data types are concrete structs.
3. **Optional capabilities via interface assertion** — core is small, extras are type-asserted.
4. **Co-locate related concerns** — resource's definition, sync policy, and CRUD live on the same type.
5. **Keep `ClientT` where it pays for itself** — on `Resourcer` and `ConnectionProvider` (typed clients). Not on engine-side interfaces.
6. **Engine owns subscriptions, SDK owns watches** — SDK streams everything; engine gates what reaches the frontend.
7. **Logically group methods** — don't split into tiny 1-method interfaces when methods belong together.
8. **Layout is a UI concern** — the SDK provides resource groups as metadata. Rendering/ordering is the frontend's job.
9. **No backward compat for v1 launch** — breaking changes from the current SDK are fine. One plugin, one migration. For future versions (v2+), versioned proto packages and engine adapters enable rolling forward without breaking existing plugins. See doc 20.
10. **Testable by construction** — every interface boundary is a test seam. `resourceController[ClientT]` satisfies `Provider` (non-generic) and can be constructed with mock dependencies for in-process testing. The engine's `InProcessBackend` can dispense it directly, enabling full-stack tests without gRPC.
11. **Introspectable by construction** — every capability is discoverable at runtime. Filter fields, supported operations, resource schemas, action parameters, and capability flags can all be queried programmatically. This enables AI agents and MCP tool generators to build correct queries without hard-coded knowledge.

---

## 4. Interface Design

### 4.1 Session Context (replaces `*PluginContext`)

```go
package resource

// Session carries per-request metadata through context.Context values.
// Set by the SDK framework before calling into plugin code.
type Session struct {
    Connection    *types.Connection
    PluginConfig  settings.Provider
    GlobalConfig  *config.GlobalConfig
    RequestID     string
    RequesterID   string
}

// Context helpers (unexported key to prevent collisions)
func WithSession(ctx context.Context, s *Session) context.Context
func SessionFromContext(ctx context.Context) *Session
func ConnectionFromContext(ctx context.Context) *types.Connection
```

**Why:** `PluginContext.Context` is confusing. Go convention: `context.Context` is the first parameter. Session metadata travels as context values. Cancellation/deadline propagation works naturally.

### 4.2 ConnectionProvider[ClientT]

One interface for all connection lifecycle concerns. Replaces 8 separate functions.

```go
// ConnectionProvider manages connections and their typed clients.
// All plugins must implement this.
type ConnectionProvider[ClientT any] interface {
    // CreateClient creates a typed client for a connection.
    // ctx carries Session with connection data. Deadline applies to creation.
    CreateClient(ctx context.Context) (*ClientT, error)

    // DestroyClient tears down a client. Called once per CreateClient.
    // Use for cleanup (close sockets, cancel watches, etc.).
    DestroyClient(ctx context.Context, client *ClientT) error

    // LoadConnections returns all connections from plugin configuration.
    // Called on plugin start and when config changes.
    LoadConnections(ctx context.Context) ([]types.Connection, error)

    // CheckConnection verifies a connection is alive and healthy.
    // Returns structured status (CONNECTED, UNAUTHORIZED, TIMEOUT, etc.).
    CheckConnection(ctx context.Context, conn *types.Connection, client *ClientT) (types.ConnectionStatus, error)

    // GetNamespaces returns namespaces for a connection.
    // Return nil if the backend doesn't support namespaces.
    GetNamespaces(ctx context.Context, client *ClientT) ([]string, error)
}

// ConnectionWatcher watches for external connection changes (e.g., kubeconfig file changes).
// Optional — type-asserted on the ConnectionProvider implementation.
type ConnectionWatcher interface {
    WatchConnections(ctx context.Context) (<-chan []types.Connection, error)
}

// ClientRefresher refreshes credentials without recreating the client.
// Optional — type-asserted on the ConnectionProvider implementation.
type ClientRefresher[ClientT any] interface {
    RefreshClient(ctx context.Context, client *ClientT) error
}

// SchemaProvider provides editor schemas for a connection (e.g., OpenAPI schemas).
// Optional — type-asserted on the ConnectionProvider implementation.
// Connection-level, not per-resource — schemas are fetched once per connection.
type SchemaProvider[ClientT any] interface {
    GetEditorSchemas(ctx context.Context, client *ClientT) ([]EditorSchema, error)
}
```

**Grouping rationale:** CreateClient, DestroyClient, LoadConnections, CheckConnection, and GetNamespaces are all core connection concerns that virtually every plugin needs. WatchConnections (file watching), RefreshClient (credential rotation), and SchemaProvider (editor schemas) are genuinely optional and specialized.

**For K8s plugin:**
```go
type kubeConnectionProvider struct { logger *zap.SugaredLogger }

// Satisfies ConnectionProvider + ConnectionWatcher + ClientRefresher + SchemaProvider
var _ resource.ConnectionProvider[clients.ClientSet] = (*kubeConnectionProvider)(nil)
var _ resource.ConnectionWatcher = (*kubeConnectionProvider)(nil)
var _ resource.ClientRefresher[clients.ClientSet] = (*kubeConnectionProvider)(nil)
var _ resource.SchemaProvider[clients.ClientSet] = (*kubeConnectionProvider)(nil)

func (k *kubeConnectionProvider) CreateClient(ctx context.Context) (*clients.ClientSet, error) { ... }
func (k *kubeConnectionProvider) DestroyClient(ctx context.Context, client *clients.ClientSet) error { ... }
func (k *kubeConnectionProvider) LoadConnections(ctx context.Context) ([]types.Connection, error) { ... }
func (k *kubeConnectionProvider) CheckConnection(ctx context.Context, conn *types.Connection, client *clients.ClientSet) (types.ConnectionStatus, error) { ... }
func (k *kubeConnectionProvider) GetNamespaces(ctx context.Context, client *clients.ClientSet) ([]string, error) { ... }
func (k *kubeConnectionProvider) WatchConnections(ctx context.Context) (<-chan []types.Connection, error) { ... }
func (k *kubeConnectionProvider) RefreshClient(ctx context.Context, client *clients.ClientSet) error { ... }
func (k *kubeConnectionProvider) GetEditorSchemas(ctx context.Context, client *clients.ClientSet) ([]resource.EditorSchema, error) { ... }
```

### 4.3 Resourcer[ClientT]

Core CRUD interface. Unchanged shape except `context.Context` replaces `*PluginContext`.

```go
// Resourcer handles CRUD for a single resource type (or pattern of types).
type Resourcer[ClientT any] interface {
    // Get retrieves a single resource by ID (and optionally namespace).
    Get(ctx context.Context, client *ClientT, meta ResourceMeta, input GetInput) (*GetResult, error)

    // List returns all resources matching the input criteria (namespace, label selectors, etc.).
    List(ctx context.Context, client *ClientT, meta ResourceMeta, input ListInput) (*ListResult, error)

    // Find searches across all namespaces/scopes for resources matching a query string.
    // Unlike List (which filters within a scope), Find is a cross-scope search.
    // Used for global search, command palette, and cross-namespace lookups.
    Find(ctx context.Context, client *ClientT, meta ResourceMeta, input FindInput) (*FindResult, error)

    // Create creates a new resource.
    Create(ctx context.Context, client *ClientT, meta ResourceMeta, input CreateInput) (*CreateResult, error)

    // Update modifies an existing resource.
    Update(ctx context.Context, client *ClientT, meta ResourceMeta, input UpdateInput) (*UpdateResult, error)

    // Delete removes a resource.
    Delete(ctx context.Context, client *ClientT, meta ResourceMeta, input DeleteInput) (*DeleteResult, error)
}
```

**Optional capability interfaces** (type-asserted on Resourcer implementations):

```go
// Watcher adds real-time event streaming to a Resourcer.
// See §4.4 for full design, lifecycle management, and examples.
// If implemented, the SDK manages a Watch goroutine for this resource.
type Watcher[ClientT any] interface {
    Watch(ctx context.Context, client *ClientT, meta ResourceMeta, sink WatchEventSink) error
}

// SyncPolicyDeclarer declares the sync policy for this resource type.
// Only meaningful if the Resourcer also implements Watcher.
// If not implemented, defaults to SyncOnConnect.
type SyncPolicyDeclarer interface {
    SyncPolicy() SyncPolicy
}

// ActionResourcer adds named actions (restart, scale, drain, etc.).
type ActionResourcer[ClientT any] interface {
    GetActions(ctx context.Context, client *ClientT, meta ResourceMeta) ([]ActionDescriptor, error)
    ExecuteAction(ctx context.Context, client *ClientT, meta ResourceMeta, actionID string, input ActionInput) (*ActionResult, error)
    StreamAction(ctx context.Context, client *ClientT, meta ResourceMeta, actionID string, input ActionInput, stream chan<- ActionEvent) error
}

// DefinitionProvider provides column defs, ID/namespace accessors.
// If not implemented, the SDK uses the default definition from config.
// Takes precedence over ResourceRegistration.Definition if both are set.
type DefinitionProvider interface {
    Definition() ResourceDefinition
}

// ErrorClassifier classifies errors into structured ResourceOperationErrors.
// Can be on a Resourcer (per-resource) or on ConnectionProvider (plugin-wide).
type ErrorClassifier interface {
    ClassifyError(err error) error
}

// SchemaResourcer provides per-resource editor schemas.
// Optional — type-asserted on Resourcer implementations.
// Complements the connection-level SchemaProvider[ClientT] on ConnectionProvider.
// The SDK merges per-resource schemas with connection-level schemas.
type SchemaResourcer[ClientT any] interface {
    GetEditorSchemas(ctx context.Context, client *ClientT, meta ResourceMeta) ([]EditorSchema, error)
}

// FilterableProvider declares filter fields for a resource type (FR-11).
// Optional — type-asserted on Resourcer implementations.
// If not implemented, Find() still works but callers can't discover valid fields.
// The SDK MAY validate predicates against declared fields before calling Find().
// See plans/resourcer-refactor/13-query-filter-mcp-design.md for full design.
type FilterableProvider interface {
    FilterFields(ctx context.Context, connectionID string) ([]FilterField, error)
}

// TextSearchProvider adds free-text search to a Resourcer (FR-13).
// Optional — for command palette, AI natural language queries, global search.
// Separate from FilterableProvider (structured predicates vs. text matching).
type TextSearchProvider[ClientT any] interface {
    Search(ctx context.Context, client *ClientT, meta ResourceMeta, query string, limit int) (*FindResult, error)
}

// ResourceSchemaProvider provides raw JSON Schema for a resource type (FR-12).
// Optional — describes the full resource data structure (all fields, types).
// Distinct from EditorSchema (which wraps schema in Monaco-specific metadata).
// Used by MCP tool generators as outputSchema, by AI agents to understand structure.
type ResourceSchemaProvider[ClientT any] interface {
    GetResourceSchema(ctx context.Context, client *ClientT, meta ResourceMeta) (json.RawMessage, error)
}

// ScaleHintProvider declares expected cardinality for a resource type (FR-11).
// Optional — helps AI agents decide list-all vs. paginate vs. filter-first.
type ScaleHintProvider interface {
    ScaleHint() *ScaleHint
}
```

See `plans/resourcer-refactor/13-query-filter-mcp-design.md` for full type definitions (FilterField, FilterPredicate, FilterExpression, FilterOperator, ResourceCapabilities, ScaleHint, enhanced ActionDescriptor, redesigned FindInput/ListInput).

### 4.4 Watch System (per-resource Watcher)

Current problems:
- Two-phase init: `RegisterResource()` called N times, then `Start()` once
- Three separate event channels (add, update, delete) plus a state channel
- State tracking duplicated between plugin-side InformerManager and engine controller
- InformerHandle receives channels at registration time, coupling it to the SDK's internal plumbing
- `InformerFactory` and `InformerHandle` are connection-scoped, disconnecting watch logic from the per-resource `Resourcer` — breaking co-location (principle #4)
- Plugin authors must implement 3 types (factory + handle + resourcer) and 8+ methods to support watching

**New design:** Watch is an optional capability on `Resourcer` via the `Watcher[ClientT]` interface (principle #3). The plugin author implements a single blocking `Watch()` method. The SDK manages ALL lifecycle — starting, stopping, restarting, and tracking goroutines. No `InformerFactory` or `InformerHandle` in the plugin-author API.

```go
// Watcher adds real-time event streaming to a Resourcer.
// Optional — type-asserted on Resourcer implementations.
// If a Resourcer implements Watcher, the SDK will manage a Watch goroutine for it.
//
// The Watch method MUST:
//   - Block until ctx is cancelled (clean shutdown) or an unrecoverable error occurs
//   - Emit events via the sink as they arrive (OnAdd, OnUpdate, OnDelete)
//   - Emit state changes via the sink (OnStateChange: Syncing → Synced, Error, etc.)
//   - Return nil on clean shutdown (ctx cancelled), non-nil on failure
//
// The SDK WILL:
//   - Call Watch() in a goroutine — plugin authors never call it themselves
//   - Cancel ctx to stop watching (connection disconnect, resource unsubscribed, etc.)
//   - Restart Watch() on failure (with backoff) or when explicitly requested
//   - Track per-resource running state
//   - Honor SyncPolicyDeclarer to decide WHEN to start watching
type Watcher[ClientT any] interface {
    Watch(ctx context.Context, client *ClientT, meta ResourceMeta, sink WatchEventSink) error
}

// WatchEventSink receives all events from a Watch.
// The SDK provides the implementation — plugin authors never implement this.
// Thread-safe: multiple Watch goroutines may call methods concurrently.
type WatchEventSink interface {
    OnAdd(payload WatchAddPayload)
    OnUpdate(payload WatchUpdatePayload)
    OnDelete(payload WatchDeletePayload)
    OnStateChange(event WatchStateEvent)
}
```

**Why per-resource Watch instead of a connection-scoped factory:**

1. **Co-location** (principle #4): CRUD + Watch + SyncPolicy all live on the same Resourcer type. Plugin authors see everything about a resource type in one place.
2. **Capability detection** (principle #3): `resourcer.(Watcher)` tells the SDK exactly which resources support watching. No separate config field, no separate interface to implement.
3. **Simpler plugin-author API**: Implement ONE blocking method. No factory, no handle, no lifecycle methods. The SDK does all lifecycle management.
4. **Backend-agnostic**: Works naturally for polling backends (ticker loop), streaming backends (change streams), and shared-infrastructure backends (K8s shared informer factory via base struct embedding).
5. **Lifecycle stays with the SDK**: The plugin author doesn't implement start/stop/restart — the SDK manages goroutines via context cancellation and restarts.

**Connection lifecycle — how the SDK manages watches:**

```
Connection starts (StartConnection):
  1. SDK creates client via ConnectionProvider.CreateClient()
  2. SDK scans all registered resourcers for Watcher capability
  3. For each Watcher with SyncOnConnect policy:
     → SDK starts a goroutine: go watcher.Watch(connCtx, client, meta, sink)
  4. For SyncOnFirstQuery: deferred until first List() call
  5. For SyncNever: not started

Connection disconnects (StopConnection):
  1. SDK cancels the connection context (connCtx)
  2. ALL Watch goroutines for this connection receive ctx.Done()
  3. Each Watch() returns (nil or error) — SDK doesn't care which
  4. SDK cleans up goroutine tracking state
  5. No per-resource teardown loop needed — context tree handles it

Connection reconnects (after crash recovery or manual restart):
  1. SDK creates new client via CreateClient()
  2. SDK starts new Watch goroutines for all previously-active resources
  3. Each Watch() gets a fresh context and fresh client
  4. Plugin author's Watch() doesn't need to handle reconnection — it's a fresh call

Per-resource lifecycle (SDK-managed):
  - Start:   SDK calls go watcher.Watch(ctx, client, meta, sink)
  - Stop:    SDK cancels the per-resource ctx (child of connection ctx)
  - Restart: SDK cancels old ctx, then calls Watch() again with new ctx
  - IsRunning: SDK tracks goroutine state internally
  - Error recovery: Watch() returns error → SDK restarts with exponential backoff
```

**Context tree:**
```
Connection ctx (cancelled on disconnect)
  ├─ Resource "core::v1::Pod" watch ctx
  ├─ Resource "apps::v1::Deployment" watch ctx
  ├─ Resource "core::v1::Service" watch ctx
  └─ ... (one child ctx per active Watch goroutine)
```

Cancelling the connection ctx cancels ALL child watch contexts atomically. No loops, no race conditions.

**For K8s plugin** (shared `DynamicSharedInformerFactory` via base struct):

```go
// Base struct embedded by all K8s resourcers.
// Provides Watch() via the shared DynamicSharedInformerFactory on the ClientSet.
type KubeResourcerBase struct {
    GVR    schema.GroupVersionResource
    Logger *zap.SugaredLogger
}

func (r *KubeResourcerBase) Watch(
    ctx context.Context,
    client *clients.ClientSet,
    meta resource.ResourceMeta,
    sink resource.WatchEventSink,
) error {
    // Ensure the shared factory is started (idempotent — safe from all 92 resourcers)
    client.EnsureFactoryStarted(ctx)

    // Register this GVR with the shared factory
    informer := client.DynamicInformerFactory.ForResource(r.GVR)

    sink.OnStateChange(resource.WatchStateEvent{
        ResourceKey: meta.Key(), State: resource.Syncing,
    })

    // Add event handler — returns registration for cleanup
    reg, err := informer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
        AddFunc:    func(obj interface{}) { sink.OnAdd(toAddPayload(meta, obj)) },
        UpdateFunc: func(old, new interface{}) { sink.OnUpdate(toUpdatePayload(meta, old, new)) },
        DeleteFunc: func(obj interface{}) { sink.OnDelete(toDeletePayload(meta, obj)) },
    })
    if err != nil {
        return fmt.Errorf("add event handler for %s: %w", r.GVR, err)
    }
    defer informer.Informer().RemoveEventHandler(reg) // K8s 1.28+ — clean per-resource teardown

    // Wait for initial sync
    if !cache.WaitForCacheSync(ctx.Done(), informer.Informer().HasSynced) {
        return fmt.Errorf("cache sync failed for %s", r.GVR)
    }

    sink.OnStateChange(resource.WatchStateEvent{
        ResourceKey: meta.Key(), State: resource.Synced,
    })

    // Block until context is cancelled (connection disconnect or SDK stop)
    <-ctx.Done()
    return nil
}

// On the ClientSet — idempotent factory start
func (cs *ClientSet) EnsureFactoryStarted(ctx context.Context) {
    cs.factoryStartOnce.Do(func() {
        cs.DynamicInformerFactory.Start(ctx.Done())
    })
}
```

All 92 K8s resourcers embed `KubeResourcerBase` and inherit `Watch()`. Each resourcer is self-describing: CRUD methods + Watch + SyncPolicy all in one type. The shared `DynamicSharedInformerFactory` is an implementation detail on `ClientSet`, invisible to the SDK.

**For a polling backend** (e.g., AWS):

```go
type S3BucketResourcer struct { /* ... */ }

func (r *S3BucketResourcer) Watch(
    ctx context.Context,
    client *AWSClient,
    meta resource.ResourceMeta,
    sink resource.WatchEventSink,
) error {
    sink.OnStateChange(resource.WatchStateEvent{ResourceKey: meta.Key(), State: resource.Synced})

    var previous []Bucket
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return nil
        case <-ticker.C:
            current, err := client.S3.ListBuckets(ctx, &s3.ListBucketsInput{})
            if err != nil {
                sink.OnStateChange(resource.WatchStateEvent{
                    ResourceKey: meta.Key(), State: resource.Error, Error: err,
                })
                continue
            }
            // Diff previous vs current, emit OnAdd/OnUpdate/OnDelete
            emitDiff(previous, current, meta, sink)
            previous = current
        }
    }
}
```

No factory, no handle, no shared infrastructure. Just a simple polling loop.

**For a streaming backend** (e.g., MongoDB change stream):

```go
func (r *MongoCollectionResourcer) Watch(
    ctx context.Context,
    client *mongo.Client,
    meta resource.ResourceMeta,
    sink resource.WatchEventSink,
) error {
    coll := client.Database(r.database).Collection(r.collection)
    stream, err := coll.Watch(ctx, mongo.Pipeline{})
    if err != nil {
        return err
    }
    defer stream.Close(ctx)

    sink.OnStateChange(resource.WatchStateEvent{ResourceKey: meta.Key(), State: resource.Synced})

    for stream.Next(ctx) {
        var event ChangeEvent
        if err := stream.Decode(&event); err != nil {
            continue
        }
        switch event.OperationType {
        case "insert":
            sink.OnAdd(toAddPayload(meta, event.FullDocument))
        case "update":
            sink.OnUpdate(toUpdatePayload(meta, event.FullDocument))
        case "delete":
            sink.OnDelete(toDeletePayload(meta, event.DocumentKey))
        }
    }
    return stream.Err()
}
```

**What changed from the factory/handle design:**
1. **`InformerFactory` deleted** from plugin-author API — watch support auto-detected via `Watcher` type assertion
2. **`InformerHandle` deleted** from plugin-author API — SDK manages lifecycle internally via context cancellation
3. **Single `Watch()` method** replaces factory + handle + 6 lifecycle methods
4. **Co-located** — Watch lives on the same type as CRUD and SyncPolicy
5. **SDK owns ALL lifecycle** — start, stop, restart, error recovery, connection disconnect/reconnect
6. **`WatchEventSink` retained** — still the event delivery mechanism, but passed directly to `Watch()`
7. **Connection disconnect is atomic** — cancel connection ctx, all Watch goroutines stop via context tree

### 4.5 DiscoveryProvider

```go
// DiscoveryProvider discovers resource types available for a connection at runtime.
// Optional — without it, the plugin uses statically registered types only.
//
// Note: Discover takes an explicit *types.Connection rather than reading from Session context.
// This is intentional — discovery is a connection-lifecycle operation called by the SDK's
// typeManager during connection setup, not a user-initiated request. The SDK calls it with
// the connection it's setting up, not within a request context.
type DiscoveryProvider interface {
    // Discover returns resource types available for a connection (e.g., CRDs in K8s).
    // Called when a connection starts and periodically to refresh.
    Discover(ctx context.Context, conn *types.Connection) ([]ResourceMeta, error)

    // OnConnectionRemoved cleans up any cached discovery state for a removed connection.
    OnConnectionRemoved(ctx context.Context, conn *types.Connection) error
}
```

Unchanged from current except `context.Context` replaces `*PluginContext`. Takes explicit `*types.Connection` because discovery is a connection-lifecycle operation (see note above).

### 4.6 ResourcePluginConfig (Registration)

```go
package sdk

// ResourcePluginConfig holds all configuration for a resource plugin.
type ResourcePluginConfig[ClientT any] struct {
    // Required
    Connections  resource.ConnectionProvider[ClientT]

    // Resource registration (at least one must be non-empty)
    Resources    []ResourceRegistration[ClientT]
    Patterns     map[string]resource.Resourcer[ClientT]  // wildcard fallbacks

    // Resource metadata
    Groups            []resource.ResourceGroup
    DefaultDefinition resource.ResourceDefinition

    // Optional capabilities
    Discovery       resource.DiscoveryProvider           // nil = static types only
    ErrorClassifier resource.ErrorClassifier             // nil = no classification

    // Watch support: auto-detected on Resourcer implementations via Watcher[ClientT]
    // type assertion. No config field needed — if a Resourcer implements Watcher,
    // the SDK will manage a Watch goroutine for it. See §4.4.

    // Schemas: if ConnectionProvider also implements SchemaProvider[ClientT],
    // the SDK auto-detects and wires it. No separate field needed.
}

// ResourceRegistration binds a resource type to its implementation.
// Co-locates metadata, resourcer, and definition.
type ResourceRegistration[ClientT any] struct {
    Meta       resource.ResourceMeta
    Resourcer  resource.Resourcer[ClientT]

    // Definition is the fallback resource definition (column defs, ID accessor, etc.).
    // Precedence: if the Resourcer implements DefinitionProvider, that wins.
    // Otherwise, this Definition is used. If both are nil, the config's
    // DefaultDefinition applies.
    Definition *resource.ResourceDefinition
}

// RegisterResourcePlugin registers the resource capability.
func RegisterResourcePlugin[ClientT any](p *Plugin, config ResourcePluginConfig[ClientT]) error
```

**For K8s plugin, registration becomes:**
```go
sdk.RegisterResourcePlugin(plugin, sdk.ResourcePluginConfig[clients.ClientSet]{
    Connections: &kubeConnectionProvider{logger: logger},
    Resources:   kubeResources(logger),  // []ResourceRegistration — each Resourcer embeds KubeResourcerBase which implements Watcher
    Patterns:    map[string]resource.Resourcer[clients.ClientSet]{
        "*": resourcers.NewKubePatternResourcer(logger),
    },
    Groups:            groups.KubeResourceGroups,
    DefaultDefinition: definitions.DefaultDef,
    Discovery:         kubeDiscovery(logger),
    ErrorClassifier:   &kubeErrorClassifier{},
    // Watch support: auto-detected on resourcers (KubeResourcerBase implements Watcher)
    // SchemaProvider: auto-detected on kubeConnectionProvider
})
```

### 4.7 gRPC Boundary — Provider

Non-generic interface the engine sees across the gRPC boundary. The SDK's `resourceController` implements this. Names are simplified — no redundant `Resource` prefix.

```go
// Provider is the composite interface that crosses the gRPC boundary.
// The SDK's resourceController[ClientT] satisfies this.
// The engine's gRPC client stub also satisfies this.
type Provider interface {
    OperationProvider
    ConnectionLifecycleProvider
    WatchProvider
    TypeProvider
    ActionProvider
    EditorSchemaProvider
}

// OperationProvider — CRUD operations on resources.
type OperationProvider interface {
    Get(ctx context.Context, key string, input GetInput) (*GetResult, error)
    List(ctx context.Context, key string, input ListInput) (*ListResult, error)
    Find(ctx context.Context, key string, input FindInput) (*FindResult, error)
    Create(ctx context.Context, key string, input CreateInput) (*CreateResult, error)
    Update(ctx context.Context, key string, input UpdateInput) (*UpdateResult, error)
    Delete(ctx context.Context, key string, input DeleteInput) (*DeleteResult, error)
}

// ConnectionLifecycleProvider — connection lifecycle over gRPC.
// Named differently from the plugin-author ConnectionProvider[ClientT] to avoid confusion.
type ConnectionLifecycleProvider interface {
    StartConnection(ctx context.Context, connectionID string) (types.ConnectionStatus, error)
    StopConnection(ctx context.Context, connectionID string) (types.Connection, error)

    // LoadConnections reads connections from plugin configuration (e.g., kubeconfig files).
    // Called on plugin start and when config changes. Source of truth for available connections.
    LoadConnections(ctx context.Context) ([]types.Connection, error)

    // ListConnections returns the current runtime state of all managed connections.
    // Unlike LoadConnections (reads from config), this returns what the SDK is actively managing,
    // including connection status, active client state, etc.
    ListConnections(ctx context.Context) ([]types.Connection, error)

    GetConnection(ctx context.Context, id string) (types.Connection, error)
    GetConnectionNamespaces(ctx context.Context, id string) ([]string, error)
    UpdateConnection(ctx context.Context, connection types.Connection) (types.Connection, error)
    DeleteConnection(ctx context.Context, id string) error
    WatchConnections(ctx context.Context, stream chan<- []types.Connection) error
}

// WatchProvider — watch lifecycle and event streaming over gRPC.
// Includes per-connection and per-resource lifecycle control.
// The SDK's watchManager implements these by managing Watch goroutine contexts.
type WatchProvider interface {
    // Connection-level watch lifecycle
    StartConnectionWatch(ctx context.Context, connectionID string) error
    StopConnectionWatch(ctx context.Context, connectionID string) error
    HasWatch(ctx context.Context, connectionID string) bool
    GetWatchState(ctx context.Context, connectionID string) (*WatchConnectionSummary, error)

    // Event streaming — long-lived gRPC server-stream
    ListenForEvents(ctx context.Context, sink WatchEventSink) error

    // Per-resource watch lifecycle
    EnsureResourceWatch(ctx context.Context, connectionID string, resourceKey string) error
    StopResourceWatch(ctx context.Context, connectionID string, resourceKey string) error
    RestartResourceWatch(ctx context.Context, connectionID string, resourceKey string) error
    IsResourceWatchRunning(ctx context.Context, connectionID string, resourceKey string) (bool, error)
}

// TypeProvider — resource type metadata and introspection.
type TypeProvider interface {
    GetResourceGroups(ctx context.Context, connectionID string) map[string]ResourceGroup
    GetResourceGroup(ctx context.Context, id string) (ResourceGroup, error)
    GetResourceTypes(ctx context.Context, connectionID string) map[string]ResourceMeta
    GetResourceType(ctx context.Context, id string) (*ResourceMeta, error)
    HasResourceType(ctx context.Context, id string) bool
    GetResourceDefinition(ctx context.Context, id string) (ResourceDefinition, error)

    // AI/MCP introspection (FR-11, FR-12) — see doc 13 for full type definitions
    GetResourceCapabilities(ctx context.Context, resourceKey string) (*ResourceCapabilities, error)
    GetResourceSchema(ctx context.Context, connectionID string, resourceKey string) (json.RawMessage, error)
    GetFilterFields(ctx context.Context, connectionID string, resourceKey string) ([]FilterField, error)
}

// ActionProvider — resource actions.
type ActionProvider interface {
    GetActions(ctx context.Context, key string) ([]ActionDescriptor, error)
    ExecuteAction(ctx context.Context, key string, actionID string, input ActionInput) (*ActionResult, error)
    StreamAction(ctx context.Context, key string, actionID string, input ActionInput, stream chan<- ActionEvent) error
}

// EditorSchemaProvider — editor schemas.
type EditorSchemaProvider interface {
    GetEditorSchemas(ctx context.Context, connectionID string) ([]EditorSchema, error)
}
```

**Key changes:**
- Renamed: `ResourceProvider` → `Provider`, dropped `Resource` prefix on all sub-interfaces
- Renamed gRPC-side `ConnectionProvider` → `ConnectionLifecycleProvider` to disambiguate from the plugin-author `ConnectionProvider[ClientT]`
- Renamed gRPC-side `SchemaProvider` → `EditorSchemaProvider` to disambiguate from the plugin-author `SchemaProvider[ClientT]`
- Renamed gRPC-side `InformerProvider` → `WatchProvider` — "informer" is a K8s-specific term; the SDK is backend-agnostic
- All method names use "Watch" terminology: `StartConnectionWatch`, `EnsureResourceWatch`, `StopResourceWatch`, etc.
- `ListenForEvents` takes `WatchEventSink` instead of 4 channels
- `WatchProvider` includes per-resource lifecycle (Stop, Restart, IsRunning) — the SDK's internal `watchManager` implements these by managing Watch goroutine contexts

**Serialization optimization (CR-16):**

All resource data fields in proto definitions use `bytes` (pre-serialized JSON) instead of
`google.protobuf.Struct`. This eliminates the `structpb.NewStruct()` / `.AsMap()` round-trip
— two full deep copies per object. The engine controller receives `json.RawMessage` and passes
it through to Wails without deserializing.

Result types at the gRPC boundary use `json.RawMessage`:
```go
type GetResult struct {
    Result  json.RawMessage `json:"result"`
    Success bool            `json:"success"`
}
type ListResult struct {
    Result     []json.RawMessage `json:"result"`
    Success    bool              `json:"success"`
    Pagination PaginationResult  `json:"pagination"`
}
```

Watch event UPDATE payloads carry only the new state (OldData dropped — frontend doesn't use it):
```go
type WatchUpdatePayload struct {
    Data       json.RawMessage `json:"data"`     // new state only (was OldData + NewData)
    PluginID   string          `json:"pluginId"`
    Key        string          `json:"key"`
    Connection string          `json:"connection"`
    ID         string          `json:"id"`
    Namespace  string          `json:"namespace"`
}
```

See `plans/resourcer-refactor/12-resource-data-serialization.md` for the full analysis, pipeline
trace, and proto changes required.

**gRPC transport note:** Some Provider methods use Go-specific constructs (`WatchEventSink`, `chan<- ActionEvent`, `chan<- []types.Connection`) that don't map directly to gRPC. At the gRPC boundary:
- `ListenForEvents(sink)` → gRPC server-streaming RPC. The gRPC client stub implements `WatchEventSink` by sending each event as a stream message. The engine-side gRPC client receives the stream and reconstructs sink calls.
- `StreamAction(..., stream chan<-)` → gRPC server-streaming RPC for action progress events.
- `WatchConnections(..., stream chan<-)` → gRPC server-streaming RPC for connection change events.
- The proto definitions will use `stream` responses for these. The Go interfaces shown here are the **in-process** representation — the gRPC client/server stubs translate between Go interfaces and proto streams.

**Query/filter/MCP readiness (FR-11, FR-12, FR-13, FR-14):**

The following changes ensure the SDK is future-proofed for AI agent queryability and automatic MCP tool generation. See `plans/resourcer-refactor/13-query-filter-mcp-design.md` for full type definitions, K8s examples, proto changes, and MCP tool generation mapping.

**Input type changes:**
- `FindInput.Conditions map[string]interface{}` → `FindInput.Filters *FilterExpression` (typed predicates with introspectable operators)
- `FindInput.Params map[string]interface{}` → removed (untyped escape hatch)
- `ListInput.Params map[string]interface{}` → removed
- `OrderParams` (single field) → `[]OrderField` (multi-field ordering)
- `PaginationParams` gains `Cursor string` for continuation-token pagination
- `PaginationResult` gains `NextCursor string`
- `FindInput` gains `TextQuery string` for free-text search

**ActionDescriptor enhancements:**
- `ParamsSchema json.RawMessage` — JSON Schema for action parameters (MCP `inputSchema`)
- `OutputSchema json.RawMessage` — JSON Schema for action results (MCP `outputSchema`)
- `Dangerous bool` — safety flag for AI confirmation prompts

**New auto-derived type:**
- `ResourceCapabilities` — auto-derived from type assertions at registration time. Flags: `CanGet`, `CanList`, `CanFind`, `CanCreate`, `CanUpdate`, `CanDelete`, `Watchable`, `Filterable`, `Searchable`, `HasActions`, `HasSchema`, `NamespaceScoped`, `ScaleHint`.

---

## 5. SDK Internal Architecture

```
ResourcePluginConfig[ClientT]
  │
  ├─ ConnectionProvider[ClientT]       ──→ connectionManager[ClientT]
  │   (+ConnectionWatcher, optional)        (manages clients, connections, namespaces)
  │   (+ClientRefresher, optional)
  │
  ├─ []ResourceRegistration[ClientT]   ──→ resourcerRegistry[ClientT]
  │   (each: Meta + Resourcer + Def)        (lookup by key, pattern fallback)
  │   (some Resourcers implement Watcher)   (detects Watcher capability via type assertion)
  │
  │                                    ──→ watchManager[ClientT]  (SDK-internal, NOT in plugin API)
  │                                         (per-connection, per-resource goroutine lifecycle)
  │                                         (for each Watcher-capable resourcer:
  │                                          calls Watch(), manages ctx, handles restart/backoff)
  │                                         (provides WatchEventSink → fans events into gRPC stream)
  │                                         (connection disconnect: cancel connCtx → all Watch() stop)
  │                                         (connection reconnect: restart all previously-active watches)
  │
  ├─ DiscoveryProvider                 ──→ typeManager
  │                                         (static + dynamic resource type registry)
  │
  └─ ErrorClassifier                    ──→ error handling
  │   (SchemaProvider auto-detected on ConnectionProvider)
  │
  └──────────────────────────────────────→ resourceController[ClientT]
                                            (implements Provider — the gRPC interface)
                                            (registered as "resource" gRPC capability)
```

**Layout is excluded.** Resource groups are metadata the frontend uses to organize the sidebar, but the SDK doesn't manage layout state, rendering, or ordering.

**Watch data flow (simplified):**
```
K8s API watch event
  → KubeResourcerBase.Watch() (plugin impl, blocking goroutine)
  → WatchEventSink.OnAdd/OnUpdate/OnDelete (SDK-provided by watchManager)
  → watchManager fans into gRPC stream
  → engine controller receives via ListenForEvents
  → if subscribed: runtime.EventsEmit to frontend
  → if not subscribed: dropped (event still in Go cache)
```

**watchManager lifecycle (SDK-internal):**
```
StartConnection("cluster-1"):
  1. connectionManager creates client
  2. watchManager scans resourcerRegistry for Watcher-capable resourcers
  3. For each with SyncOnConnect:
     → watchManager starts goroutine: go resourcer.Watch(childCtx, client, meta, sink)
  4. watchManager tracks: map[connectionID]map[resourceKey]*watchState

First List("cluster-1", "core::v1::Secret"):
  → If Secret resourcer has SyncOnFirstQuery + Watcher:
    → watchManager starts Watch goroutine (lazy start)

StopConnection("cluster-1"):
  1. connectionManager cancels connection context
  2. ALL Watch goroutines for "cluster-1" receive ctx.Done() atomically
  3. Each Watch() returns, watchManager cleans up tracking state
  4. connectionManager calls DestroyClient()

Watch() returns error (resource-level failure):
  1. watchManager detects goroutine exit with non-nil error
  2. Emits OnStateChange(Error) to sink
  3. Waits backoff interval
  4. Restarts: go resourcer.Watch(newChildCtx, client, meta, sink)
  5. Gives up after max retries, emits OnStateChange(Failed)

EnsureResourceWatch("cluster-1", "core::v1::Pod"):
  → If not running: start Watch goroutine
  → If already running: no-op

StopResourceWatch("cluster-1", "core::v1::Pod"):
  → Cancel the per-resource child ctx
  → Watch() returns, watchManager marks as stopped

RestartResourceWatch("cluster-1", "core::v1::Pod"):
  → Cancel old ctx, start new Watch() with fresh ctx
```

---

## 6. Context Cancellation & Timeout Design

### Context Hierarchy

```
Plugin root ctx (from Serve())
  └─ Connection ctx (created at StartConnection, cancelled at StopConnection)
       ├─ Watch ctx per resource (child of connection — cancelled when connection stops)
       │    └─ Watcher.Watch() goroutine (one per Watcher-capable resource)
       └─ Request ctx (per CRUD/action call — has deadline from RequestOptions)
            └─ Passed to Resourcer methods
```

### Request-Level
```
Frontend → Wails RPC → Engine Controller → gRPC → Plugin SDK → Resourcer
           (timeout)                       (propagates deadline)

- Wails creates context with configurable timeout (default 30s)
- gRPC propagates deadline automatically
- If user navigates away (Wails RPC cancelled), context is cancelled
- Resourcer should check ctx.Err() / ctx.Done() for long operations
```

### Connection-Level
```
- Long-lived context created at StartConnection
- Cancelled at StopConnection
- All Watch goroutine contexts are children — when connection stops, all Watch() calls return
- No manual channel management needed — context tree handles teardown atomically
```

### Timeout Configuration
```go
type RequestOptions struct {
    Timeout         time.Duration   // default: 30s
    MaxRetries      int             // default: 3
    BackoffInterval time.Duration   // default: 1s
}
// NOT stored in Session — Session carries identity/connection info.
// RequestOptions are per-request settings applied by the SDK framework as a
// context.WithDeadline/context.WithTimeout on the request ctx before calling
// Resourcer methods. gRPC propagates deadlines automatically.
// Plugin authors don't interact with RequestOptions directly — they just
// respect ctx.Done() / ctx.Err() in their Resourcer and Watch implementations.
```

---

## 7. Migration Strategy

No backward compat needed. Direct replacement. All new code uses versioned package
paths (`pkg/v1/resource/`, `proto/v1/`) and the in-tree migration strategy from
doc 20 §14 — build in parallel, switch over, delete old.

### Step 1: Define New Interfaces
- New types in `plugin-sdk/pkg/v1/resource/` (versioned path per doc 20)
- Session context helpers
- `Watcher[ClientT]`, `WatchEventSink`, `SyncPolicyDeclarer`
- ResourcePluginConfig, ResourceRegistration

### Step 2: Rewrite SDK Internal Services
- `connectionManager[ClientT]` accepts `ConnectionProvider[ClientT]` directly
- `watchManager[ClientT]` (new, replaces `informerManager`) — scans `resourcerRegistry` for `Watcher`-capable resourcers, manages per-connection per-resource Watch goroutines via context tree, provides `WatchEventSink` that fans events into gRPC stream
- `resourcerRegistry[ClientT]` uses `[]ResourceRegistration` with optional interface detection (`Watcher`, `SyncPolicyDeclarer`, `DefinitionProvider`, `ActionResourcer`, etc.)
- `resourceController[ClientT]` wired to new services

### Step 3: Update gRPC Layer
- Add `context.Context` to all ResourceProvider methods
- `ListenForEvents` uses `WatchEventSink` instead of 4 channels
- Streaming RPCs for `ListenForEvents`, `StreamAction`, `WatchConnections`
- Update proto if needed (likely minimal — context propagation is automatic in gRPC)

### Step 4: Migrate K8s Plugin
- Implement `ConnectionProvider[clients.ClientSet]` on a struct (consolidates 8 functions)
- Add `Watch()` method to `KubeResourcerBase` (base struct embedded by all K8s resourcers) — uses `DynamicSharedInformerFactory` on `ClientSet`
- Add `EnsureFactoryStarted()` to `ClientSet` for idempotent shared factory startup
- Update resourcer implementations: `context.Context` replaces `*PluginContext`
- Update code generator templates
- Delete old `ResourcePluginOpts`, `RegisterResourcePlugin`, `PluginContext`, `InformerHandle`, `InformerFactory`

### Step 5: Update Engine Controller
- Update `backend/pkg/plugin/resource/controller.go` to use new `Provider` signatures
- Update `ListenForEvents` call to use `WatchEventSink`
- Clean up 4-channel watch listener (replaced by single sink)
- Clean up any stale state tracking that's now handled by the SDK's `watchManager`

### Dropped Features (Dead Code)

| Feature | File | Reason |
|---------|------|--------|
| `PreHook`/`PostHook` types | `plugin-sdk/pkg/resource/types/hook.go` | Never imported outside definition file. Dead code. Delete. If needed later, add as optional `HookProvider` interface. |
| `StartClient`/`StopClient` functions | `plugin-sdk/pkg/sdk/resource_opts.go` | No-ops in K8s plugin. `CreateClient`/`DestroyClient` on `ConnectionProvider` cover the lifecycle. Delete. |
| `LayoutManager` / `ResourceLayoutProvider` | `plugin-sdk/pkg/resource/services/layout_manager.go`, `types/providers.go` | Layout is a UI concern. Delete from SDK and gRPC boundary. |
| `LayoutOpts` field | `plugin-sdk/pkg/sdk/resource_opts.go` | Removed with layout. Delete. |

---

## 8. Key Files to Modify

| File | Change |
|------|--------|
| `plugin-sdk/proto/v1/resource.proto` | **New**: Versioned proto definitions per doc 20 §3 |
| `plugin-sdk/proto/v1/common.proto` | **New**: Shared types (Connection, ResourceMeta, etc.) |
| `plugin-sdk/pkg/v1/resource/interfaces.go` | **New**: Resourcer with context.Context, `Watcher[ClientT]`, optional interfaces |
| `plugin-sdk/pkg/v1/resource/types.go` | **New**: FindInput, ListInput, WatchEventSink, SyncPolicy, etc. |
| `plugin-sdk/pkg/v1/resource/plugin.go` | **New**: GRPCPlugin implementing go-plugin interface for v1 |
| `plugin-sdk/pkg/v1/resource/server.go` | **New**: gRPC server (plugin side) |
| `plugin-sdk/pkg/v1/resource/client.go` | **New**: gRPC client (engine side) |
| `plugin-sdk/pkg/v1/resource/config.go` | **New**: ResourcePluginConfig, ResourceRegistration |
| `plugin-sdk/pkg/v1/resource/controller.go` | **New**: resourceController wired to new interfaces |
| `plugin-sdk/pkg/v1/resource/connection_manager.go` | **New**: accepts ConnectionProvider |
| `plugin-sdk/pkg/v1/resource/watch_manager.go` | **New** (replaces `informer_manager.go`): scans registry for `Watcher`, manages per-connection per-resource Watch goroutines, provides `WatchEventSink`, handles lifecycle (start/stop/restart/backoff) |
| `plugin-sdk/pkg/v1/resource/resourcer_registry.go` | **New**: ResourceRegistration + Watcher capability detection |
| `plugin-sdk/pkg/v1/resource/session.go` | **New**: Session type + context accessors |
| `plugin-sdk/pkg/sdk/plugin.go` | Rewrite to use `VersionedPlugins` per doc 20 §4 |
| `plugin-sdk/pkg/resource/services/informer_manager.go` | **Delete** — replaced by watch_manager |
| `plugin-sdk/pkg/resource/services/layout_manager.go` | **Delete** — layout is a UI concern |
| `plugin-sdk/pkg/sdk/resource_opts.go` | **Delete** — replaced by v1 config |
| `plugin-sdk/pkg/types/context.go` | **Delete** — replaced by Session |
| `plugins/kubernetes/pkg/plugin/resource/connections.go` | Implement ConnectionProvider struct |
| `plugins/kubernetes/pkg/plugin/resource/resourcer_base.go` | Add `Watch()` method to `KubeResourcerBase` (uses `DynamicSharedInformerFactory`) |
| `plugins/kubernetes/pkg/plugin/resource/clients.go` | Add `EnsureFactoryStarted()` to `ClientSet` |
| `plugins/kubernetes/pkg/plugin/resource/informer.go` | **Delete** or simplify — `InformerFactory`/`InformerHandle` no longer needed. `Watch()` lives on resourcer base. |
| `plugins/kubernetes/pkg/plugin/resource/register_gen.go` | Switch to `ResourceRegistration[]` |
| `plugins/kubernetes/generators/` | Update templates for new registration format |
| `backend/pkg/plugin/resource/adapter_v1.go` | **New**: AdapterV1 implementing canonical ResourceProvider per doc 20 §5 |
| `backend/pkg/plugin/resource/types/provider.go` | **New**: Canonical ResourceProvider interface (version-independent) |
| `backend/pkg/plugin/resource/controller.go` | Rewrite to use canonical ResourceProvider |
| `backend/pkg/plugin/resource/client.go` | Update IClient interface |
| `backend/pkg/plugin/loader.go` | Switch to `VersionedPlugins` per doc 20 §4.2 |
| `plugin-sdk/pkg/v1/resource/resourcetest/` | **New**: Test helpers — mock ConnectionProvider, mock Resourcer (with optional Watcher), recording WatchEventSink, test Session helpers |
| `plugin-sdk/pkg/v1/resource/watch_manager_test.go` | **New**: Tests for Watch goroutine lifecycle, backoff, connection disconnect, SyncPolicy |
| `plugin-sdk/pkg/resource/types/hook.go` | **Delete** — dead code (PreHook/PostHook never used) |
| `plugin-sdk/pkg/resource/types/informer.go` | **Delete** — replaced by v1 watch_manager |
| `backend/pkg/plugin/resource/controller_integration_test.go` | **New**: Integration tests using InProcessBackend + mock SDK Provider |
| `backend/pkg/plugin/resource/adapter_v1_test.go` | **New**: Adapter conformance tests per doc 20 §11 |

---

## 9. Verification

| # | Test | How to verify |
|---|------|---------------|
| V1 | K8s plugin loads and connects | Click connect → cluster connects, resources visible |
| V2 | CRUD operations work | Get, List, Create, Update, Delete pods/deployments |
| V3 | Live updates work | Create/delete pod while viewing → updates within 1s |
| V4 | Sync policies work | SyncOnConnect ready fast, SyncOnFirstQuery lazy on navigate |
| V5 | Actions work | Restart deployment, drain node |
| V6 | Streaming actions work | Restart with progress streaming |
| V7 | Connection watch works | Modify kubeconfig → connections refresh |
| V8 | Plugin crash recovery | Kill plugin process → recovers automatically |
| V9 | Context cancellation | Navigate away during long List → cancelled, no leak |
| V10 | Performance invariants | All 8 from doc 08 pass (zero unsolicited IPC, <3s connect, etc.) |
| V11 | SDK unit tests pass without gRPC | `go test ./plugin-sdk/pkg/resource/...` — no network, no process spawning |
| V12 | Engine integration test with InProcessBackend | Construct mock SDK Provider, dispense via InProcessBackend, verify CRUD + watch event flow |
| V13 | K8s plugin unit tests pass | ConnectionProvider, Resourcer (including Watch), tested with fake K8s clients |

---

## 10. Code Generator Changes

The K8s code generator (`plugins/kubernetes/generators/`) currently generates `register_gen.go` with:
- A flat `map[types.ResourceMeta]types.Resourcer[clients.ClientSet]` (92 entries)
- Separate `resourceMap` and `gvrMap` lookup tables
- Special-cased resourcers for 4 types (Node, Deployment, StatefulSet, DaemonSet)

**Required changes:**
1. Template outputs `[]sdk.ResourceRegistration[clients.ClientSet]` instead of a map
2. Each registration co-locates `Meta`, `Resourcer`, and `Definition` (pull from definitions packages)
3. Remove `resourceMap`/`gvrMap` — each resourcer's `Watch()` resolves its own GVR via the embedded `KubeResourcerBase.GVR` field
4. `SyncPolicy()` is implemented on each resourcer type instead of a central map
5. Base resourcer (`KubeResourcerBase`) gains:
   - `SyncPolicy() SyncPolicy` method (returns `SyncOnConnect` by default)
   - `Watch()` method (uses `DynamicSharedInformerFactory` on `ClientSet` — see §4.4)
   - All generated resourcers embed `KubeResourcerBase`, so they automatically implement `Watcher` and `SyncPolicyDeclarer`

**Template sketch:**
```go
// Generated by generators/generator.go — DO NOT EDIT
func Resources(logger *zap.SugaredLogger) []sdk.ResourceRegistration[clients.ClientSet] {
    return []sdk.ResourceRegistration[clients.ClientSet]{
        {
            Meta: resource.ResourceMeta{
                Group: "apps", Version: "v1", Kind: "Deployment",
                Description: "...",
            },
            Resourcer:  resourcers.NewDeploymentResourcer(logger),
            Definition: &definitions.Deployment,
        },
        {
            Meta: resource.ResourceMeta{
                Group: "core", Version: "v1", Kind: "Pod",
                Description: "...",
            },
            Resourcer:  resourcers.NewKubernetesResourcerBase[resourcers.MetaAccessor](
                logger, corev1.SchemeGroupVersion.WithResource("pods"),
            ),
            Definition: &definitions.Pod,
        },
        // ... 90 more entries
    }
}
```

---

## 11. Open Design Questions

1. **Should resource groups be part of ResourceRegistration?** Currently groups are a separate list. Co-locating would mean each resource declares its group. Tradeoff: more verbose registration vs. better co-location. Recommendation: keep separate — groups are organizational metadata, not per-resource behavior.

### Resolved Questions

2. **Should the gRPC ConnectionProvider and plugin-author ConnectionProvider share a name?** **Decision: renamed gRPC side to `ConnectionLifecycleProvider`.** They serve different layers (gRPC boundary vs plugin implementation). Adopted in this doc.

3. **Should WatchEventSink have a single `OnEvent(WatchEvent)` method with a type discriminator, or separate methods?** **Decision: keep separate** (4 methods: OnAdd, OnUpdate, OnDelete, OnStateChange). More type-safe — no switch needed.

4. **Should watching use a connection-scoped factory (InformerFactory) or per-resource Watch?** **Decision: per-resource `Watcher[ClientT]` interface.** Co-locates CRUD + Watch + SyncPolicy on the same Resourcer type. SDK manages all lifecycle (goroutines, context cancellation, backoff). K8s handles shared `DynamicSharedInformerFactory` via base struct embedding. Non-K8s backends write a simple blocking `Watch()` method. See §4.4 for full design.

---

## 12. Testability Design

### 12.1 Design Requirement

The SDK must be testable at three levels without requiring go-plugin, gRPC, or process spawning:

1. **Plugin-author unit tests** — test `ConnectionProvider`, `Resourcer` (including `Watcher`) implementations in isolation with `context.Background()` and fake clients.
2. **SDK integration tests** — test `resourceController` with mock plugin-author implementations. Full `Provider` interface exercised in-process.
3. **Engine integration tests** — test engine controller → SDK controller via `InProcessBackend`. Full data flow without gRPC.

This follows the pattern already established in the engine with `PluginBackend` / `InProcessBackend` (`backend/pkg/plugin/types/backend.go`).

### 12.2 Test Seam: resourceController as Provider

The SDK's `resourceController[ClientT]` implements `Provider` (non-generic). This is the primary test seam. Construct it with mock interfaces:

```go
mockConn := &mockConnectionProvider[string]{ ... }
mockResourcer := &mockResourcer[string]{ ... }
config := sdk.ResourcePluginConfig[string]{
    Connections: mockConn,
    Resources: []sdk.ResourceRegistration[string]{
        {Meta: podMeta, Resourcer: mockResourcer},
    },
}
// Builds the controller in-process — no gRPC, no go-plugin
provider := sdk.BuildResourceController(config)

// Test CRUD directly on the Provider interface
result, err := provider.List(ctx, "core::v1::Pod", ListInput{...})
```

No gRPC server, no go-plugin, no process boundary.

### 12.3 Test Seam: InProcessBackend for Engine Tests

The engine's resource controller receives `ResourceProvider` via `PluginBackend.Dispense()`. For testing, use `InProcessBackend`:

```go
// Build an SDK controller with mocks
sdkProvider := sdk.BuildResourceController(config)

// Wire into the engine via InProcessBackend (no gRPC)
backend := plugintypes.NewInProcessBackend(map[string]interface{}{
    "resource": sdkProvider,
})

// Engine controller calls backend.Dispense("resource") → gets sdkProvider directly
engineCtrl.OnPluginStart("test-plugin", meta, backend)

// Now test the full engine→SDK path
result, err := engineCtrl.List("test-plugin", "conn1", "core::v1::Pod", ListInput{})
```

This pattern is already used in existing tests (`backend/pkg/plugin/types/inprocess_backend_test.go`, `loader_test.go`).

### 12.4 Test Seam: Mock Interfaces for Plugin Authors

All plugin-author interfaces are simple and mockable without code generation:

```go
// Mock ConnectionProvider
type mockConnectionProvider[ClientT any] struct {
    clients     map[string]*ClientT
    connections []types.Connection
}
func (m *mockConnectionProvider[ClientT]) CreateClient(ctx context.Context) (*ClientT, error) { ... }
func (m *mockConnectionProvider[ClientT]) DestroyClient(ctx context.Context, client *ClientT) error { ... }
func (m *mockConnectionProvider[ClientT]) LoadConnections(ctx context.Context) ([]types.Connection, error) { ... }
func (m *mockConnectionProvider[ClientT]) CheckConnection(ctx context.Context, conn *types.Connection, client *ClientT) (types.ConnectionStatus, error) { ... }
func (m *mockConnectionProvider[ClientT]) GetNamespaces(ctx context.Context, client *ClientT) ([]string, error) { ... }

// Mock Resourcer
type mockResourcer[ClientT any] struct {
    listResult *ListResult
    getResult  *GetResult
}
func (m *mockResourcer[ClientT]) List(ctx context.Context, client *ClientT, meta ResourceMeta, input ListInput) (*ListResult, error) {
    return m.listResult, nil
}
// etc.

// Recording WatchEventSink — useful for testing Watcher implementations
type recordingSink struct {
    mu      sync.Mutex
    adds    []WatchAddPayload
    updates []WatchUpdatePayload
    deletes []WatchDeletePayload
    states  []WatchStateEvent
}
func (s *recordingSink) OnAdd(p WatchAddPayload)         { s.mu.Lock(); s.adds = append(s.adds, p); s.mu.Unlock() }
func (s *recordingSink) OnUpdate(p WatchUpdatePayload)   { s.mu.Lock(); s.updates = append(s.updates, p); s.mu.Unlock() }
func (s *recordingSink) OnDelete(p WatchDeletePayload)   { s.mu.Lock(); s.deletes = append(s.deletes, p); s.mu.Unlock() }
func (s *recordingSink) OnStateChange(e WatchStateEvent) { s.mu.Lock(); s.states = append(s.states, e); s.mu.Unlock() }
```

### 12.5 Test Seam: Watcher Testing

The per-resource `Watcher` interface is significantly easier to test than the current channel-based design:

- **Current:** must create 3 channels + 1 state channel, wire them through `RegisterResource` N times, then call `Start` on InformerHandle
- **New:** create a `recordingSink`, call `Watch()` directly, assert events on the sink

Plugin authors testing their `Watcher` implementations:

```go
// Test a resourcer's Watch directly — no SDK, no factory, no handle
sink := &recordingSink{}
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

resourcer := &myPodResourcer{...}
go resourcer.Watch(ctx, fakeClient, podMeta, sink)

// Trigger some events in the backend...
// (e.g., create a pod in the fake K8s client)

assert.Eventually(t, func() bool { return len(sink.adds) > 0 }, 5*time.Second, 100*time.Millisecond)

// Test clean shutdown
cancel()
// Watch() returns nil — verify no goroutine leaks
```

Testing the SDK's `watchManager` (internal, but exercised via Provider):

```go
// Create a mock resourcer that implements Watcher
mockPod := &mockWatchableResourcer[string]{
    listResult: &ListResult{Items: []Item{{ID: "pod-1"}}},
    watchFunc: func(ctx context.Context, client *string, meta ResourceMeta, sink WatchEventSink) error {
        sink.OnStateChange(WatchStateEvent{ResourceKey: meta.Key(), State: Synced})
        sink.OnAdd(WatchAddPayload{ResourceKey: meta.Key(), ID: "pod-1"})
        <-ctx.Done()
        return nil
    },
}

config := sdk.ResourcePluginConfig[string]{
    Connections: mockConn,
    Resources: []sdk.ResourceRegistration[string]{
        {Meta: podMeta, Resourcer: mockPod},
    },
}
provider := sdk.BuildResourceController(config)

// Start connection → watchManager detects Watcher, starts Watch goroutine
status, err := provider.StartConnection(ctx, "conn-1")
require.NoError(t, err)

// Verify events flow through to ListenForEvents
recordSink := &recordingSink{}
go provider.ListenForEvents(ctx, recordSink)
assert.Eventually(t, func() bool { return len(recordSink.adds) > 0 }, 5*time.Second, 100*time.Millisecond)
```

### 12.6 Test Helpers Package

The SDK should ship test helpers in a `resourcetest` package:

```
plugin-sdk/pkg/resource/resourcetest/
    mock_connection_provider.go   // configurable mock ConnectionProvider
    mock_resourcer.go             // configurable mock Resourcer (with optional Watcher)
    recording_sink.go             // WatchEventSink that records all events
    test_session.go               // helper to create Session + context for tests
```

These are hand-written, configurable test doubles — matching the existing pattern in the codebase. No mockgen dependency.
