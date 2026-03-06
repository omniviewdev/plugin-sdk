# 20: SDK Protocol Versioning — Rolling Forward Without Breaking Plugins

This document designs the versioning strategy for the resource plugin SDK protocol. The
goal: when the SDK needs breaking interface changes in the future (v2, v3, ...), plugins
built against older versions continue working alongside plugins built against the latest
version, and plugin authors migrate at their own pace.

**Scope:** Design document only. Defines how the v1 design (doc 09) becomes explicitly
versioned, and how future versions are introduced without a flag day migration. Covers
all 6 plugin capabilities (resource, exec, logs, metric, networker, settings) plus the
lifecycle bootstrap service. Includes in-tree migration strategy and frontend impact
analysis.

**Key constraint:** We're designing from scratch. Every part of the existing SDK, proto
definitions, go-plugin configuration, and engine loading code will be rewritten as part
of the doc 09 refactor. There are no backwards compatibility constraints with the current
implementation.

---

## 1. The Problem

### 1.1 Why Versioning Matters

The SDK interface (doc 09) defines the contract between the engine and plugins across a
gRPC boundary. Today there's one plugin (Kubernetes). Tomorrow there will be many —
AWS, Terraform, databases, custom infrastructure. These will be:

- Built by different authors
- Updated on different schedules
- Compiled against different SDK versions
- Possibly distributed as pre-built binaries

When we want to add new capabilities that require breaking the gRPC protocol (new RPC
method signatures, changed message semantics, removed fields), we need a way to:

1. **Roll out the new version** without breaking existing plugins
2. **Support old and new simultaneously** so the engine works with both
3. **Give authors a migration window** before dropping old versions
4. **Negotiate cleanly** so both sides know what they're speaking

### 1.2 What Counts as a Breaking Change

Proto is naturally forward-compatible — adding new fields, new enum values, or new RPC
methods doesn't break existing plugins. A version bump is only needed when:

| Change Type | Breaking? | Example |
|------------|-----------|---------|
| Add field to message | No | Add `cursor` to `ListRequest` |
| Add new RPC method | No | Add `GetFilterFields()` to service |
| Add new enum value | No | Add `FILTER_OP_REGEX` to `FilterOperator` |
| Remove or rename field | **Yes** | Remove `Conditions` from `FindRequest` |
| Change field semantics | **Yes** | `data` changes from `Struct` to `bytes` |
| Remove RPC method | **Yes** | Remove `Find()` in favor of `Query()` |
| Change method signature | **Yes** | `Get()` takes different request message |
| Change stream direction | **Yes** | Bidirectional stream where unary used to be |

**Rule:** If it's additive and existing clients/servers still work with the old behavior,
it's not breaking. If existing code would fail or silently produce wrong results, it's
breaking.

### 1.3 Current State (Pre-Refactor)

The existing SDK has version infrastructure that's never been used properly:

| Component | Current State | Problem |
|-----------|--------------|---------|
| `PluginMeta.SDKProtocolVersion` | Exists, defaults to 1 | Engine never checks it |
| `HandshakeConfig.ProtocolVersion` | Fixed at 1 | go-plugin transport version, not SDK version |
| `GetInfo().SdkProtocolVersion` | Returned via RPC | Engine ignores it |
| Proto package | `com.omniview.pluginsdk` | Unversioned — can't have v1 and v2 coexist |
| gRPC service | `ResourcePlugin` | Single service, no version disambiguation |
| go-plugin `VersionedPlugins` | Not used | Uses plain `Plugins` map instead |

All of this gets rewritten.

---

## 2. Design: Versioned Protocol Architecture

### 2.1 Three-Layer Versioning

```
┌─────────────────────────────────────────────────────────────────┐
│ Layer 1: go-plugin Transport Protocol                            │
│                                                                   │
│ HandshakeConfig.ProtocolVersion = 1                               │
│ This is go-plugin's wire protocol version. Almost never changes.  │
│ Only bumped if go-plugin itself makes breaking transport changes.  │
│ NOT our concern — managed by hashicorp/go-plugin.                 │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│ Layer 2: SDK Protocol Version (THIS DOCUMENT)                    │
│                                                                   │
│ Negotiated at plugin load time via go-plugin's VersionedPlugins.  │
│ Determines which proto package / gRPC service to use.             │
│ v1, v2, v3, ... — each is a complete proto service definition.    │
│                                                                   │
│ Engine supports: {v1, v2}                                         │
│ Plugin A speaks: {v1}      → engine uses v1 adapter               │
│ Plugin B speaks: {v1, v2}  → engine uses v2 (highest common)      │
│ Plugin C speaks: {v2}      → engine uses v2                       │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│ Layer 3: Additive Feature Flags (within a version)               │
│                                                                   │
│ Within v1, new optional features are added without a version bump.│
│ Engine discovers them via GetCapabilities() or type assertions.   │
│ This is the doc 09 optional interface pattern:                    │
│   - FilterableProvider, TextSearchProvider, etc.                  │
│   - Declared via ResourceCapabilities flags                       │
│                                                                   │
│ No version bump needed. Proto messages just grow.                 │
└─────────────────────────────────────────────────────────────────┘
```

### 2.2 When Each Layer Changes

| Scenario | Layer | Action |
|----------|-------|--------|
| Add `FilterField` message + `GetFilterFields()` RPC | 3 (additive) | No version bump. New RPC, new message. Old plugins don't implement it; engine gets "unimplemented" and handles gracefully. |
| Change `data` field from `Struct` to `bytes` | 2 (breaking) | Version bump: v1 → v2. New proto package. Engine supports both via adapters. |
| Add `Dangerous` field to `ActionDescriptor` | 3 (additive) | No version bump. Old plugins send `false` (proto default). |
| Redesign `Watch` from server-stream to bidi-stream | 2 (breaking) | Version bump. New service definition with different stream semantics. |
| go-plugin changes its transport framing | 1 (transport) | Update go-plugin dependency. Not our version. |

---

## 3. Versioned Proto Packages

### 3.1 Package Structure

Each SDK protocol version gets its own proto package and Go module path:

```
plugin-sdk/
├── proto/
│   ├── v1/
│   │   ├── resource.proto          # package omniview.sdk.resource.v1
│   │   ├── resource_informer.proto
│   │   ├── filter.proto
│   │   ├── lifecycle.proto         # package omniview.sdk.lifecycle.v1
│   │   └── generate.go            # go:generate protoc commands
│   │
│   └── v2/                        # (future — when breaking changes needed)
│       ├── resource.proto          # package omniview.sdk.resource.v2
│       ├── resource_informer.proto
│       ├── filter.proto
│       ├── lifecycle.proto
│       └── generate.go
│
├── pkg/
│   ├── resource/
│   │   ├── v1/                    # v1 Go interfaces and types
│   │   │   ├── interfaces.go      # Resourcer[ClientT], Watcher[ClientT], etc.
│   │   │   ├── types.go           # FindInput, ListInput, ResourceHealth, etc.
│   │   │   ├── plugin.go          # GRPCPlugin implementation for v1
│   │   │   ├── server.go          # gRPC server (plugin side)
│   │   │   └── client.go          # gRPC client (engine side)
│   │   │
│   │   └── v2/                    # (future)
│   │       ├── interfaces.go
│   │       ├── types.go
│   │       ├── plugin.go
│   │       ├── server.go
│   │       └── client.go
│   │
│   └── sdk/
│       └── plugin.go              # Serve() with VersionedPlugins
```

### 3.2 Proto Package Naming

```protobuf
// proto/v1/resource.proto
syntax = "proto3";
package omniview.sdk.resource.v1;

option go_package = "github.com/omniviewdev/plugin-sdk/proto/v1/resourcepb";

service ResourcePlugin {
    rpc Get(GetRequest) returns (GetResponse);
    rpc List(ListRequest) returns (ListResponse);
    rpc Find(FindRequest) returns (FindResponse);
    // ... all v1 methods
}
```

```protobuf
// proto/v2/resource.proto (future)
syntax = "proto3";
package omniview.sdk.resource.v2;

option go_package = "github.com/omniviewdev/plugin-sdk/proto/v2/resourcepb";

service ResourcePlugin {
    // v2 can freely change method signatures, message shapes, stream types
    rpc Query(QueryRequest) returns (QueryResponse);  // replaces Get+List+Find
    rpc Watch(stream WatchControl) returns (stream WatchEvent);  // bidi stream
    // ...
}
```

Different proto packages = different generated Go code. Both can exist in the same
binary simultaneously. No conflicts.

### 3.3 Go Interface Versioning

```go
// pkg/resource/v1/interfaces.go
package v1

// Resourcer is the v1 resource interface from doc 09.
type Resourcer[ClientT any] interface {
    Get(ctx context.Context, client *ClientT, meta ResourceMeta, input GetInput) (*GetResult, error)
    List(ctx context.Context, client *ClientT, meta ResourceMeta, input ListInput) (*ListResult, error)
    Find(ctx context.Context, client *ClientT, meta ResourceMeta, input FindInput) (*FindResult, error)
    Create(ctx context.Context, client *ClientT, meta ResourceMeta, input CreateInput) (*CreateResult, error)
    Update(ctx context.Context, client *ClientT, meta ResourceMeta, input UpdateInput) (*UpdateResult, error)
    Delete(ctx context.Context, client *ClientT, meta ResourceMeta, input DeleteInput) (*DeleteResult, error)
}
```

```go
// pkg/resource/v2/interfaces.go (future — hypothetical breaking changes)
package v2

// Resourcer is the v2 resource interface.
// Breaking change: Query() replaces separate Get/List/Find.
type Resourcer[ClientT any] interface {
    Query(ctx context.Context, client *ClientT, meta ResourceMeta, input QueryInput) (*QueryResult, error)
    Mutate(ctx context.Context, client *ClientT, meta ResourceMeta, input MutateInput) (*MutateResult, error)
}
```

Plugin authors import the version they want:

```go
// K8s plugin built against v1
import resourcev1 "github.com/omniviewdev/plugin-sdk/pkg/resource/v1"

type PodResourcer struct{}
var _ resourcev1.Resourcer[*kubernetes.Clientset] = (*PodResourcer)(nil)
```

---

## 4. go-plugin VersionedPlugins

### 4.1 Plugin Side (SDK Serve)

```go
// pkg/sdk/plugin.go
func (p *Plugin) Serve() {
    plugin.Serve(&plugin.ServeConfig{
        HandshakeConfig: plugin.HandshakeConfig{
            ProtocolVersion:  1,               // go-plugin transport (fixed)
            MagicCookieKey:   "OMNIVIEW",
            MagicCookieValue: p.meta.ID,
        },
        VersionedPlugins: map[int]plugin.PluginSet{
            // SDK protocol version 1
            1: {
                "lifecycle": &lifecycle.PluginV1{Impl: p.lifecycleServer},
                "resource":  &resourcev1.GRPCPlugin{Impl: p.resourceServer},
            },
            // When v2 exists, a plugin built with SDK v2 registers both:
            // 2: {
            //     "lifecycle": &lifecycle.PluginV2{Impl: p.lifecycleServer},
            //     "resource":  &resourcev2.GRPCPlugin{Impl: p.resourceServer},
            // },
        },
        GRPCServer:       plugin.DefaultGRPCServer,
        AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
    })
}
```

**go-plugin's `VersionedPlugins` semantics:**
- The map key is the SDK protocol version (int)
- The engine and plugin both declare which versions they support
- go-plugin negotiates the **highest version both sides support**
- If no overlap → plugin fails to load (incompatible)

### 4.2 Engine Side (Client Config)

```go
// backend/pkg/plugin/loader.go
func (l *Loader) loadPlugin(id string, location string) (*ExternalBackend, error) {
    client := plugin.NewClient(&plugin.ClientConfig{
        HandshakeConfig: plugin.HandshakeConfig{
            ProtocolVersion:  1,
            MagicCookieKey:   "OMNIVIEW",
            MagicCookieValue: id,
        },
        VersionedPlugins: map[int]plugin.PluginSet{
            // Engine supports v1 (and later, v1 + v2)
            1: {
                "lifecycle": &lifecycle.PluginV1{},
                "resource":  &resourcev1.GRPCPlugin{},
            },
            // When engine adds v2 support:
            // 2: {
            //     "lifecycle": &lifecycle.PluginV2{},
            //     "resource":  &resourcev2.GRPCPlugin{},
            // },
        },
        Cmd:              exec.Command(filepath.Join(location, "bin", "plugin")),
        AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
    })

    // ...
    // After connection: client.NegotiatedVersion() returns the agreed version
    negotiatedVersion := client.NegotiatedVersion()
    // Engine stores this per-plugin to route through the correct adapter
}
```

### 4.3 Version Negotiation Flow

```
Engine starts plugin process:
  Engine supports: [1, 2]
  Plugin supports: [1]

  go-plugin negotiation:
    → highest common version = 1
    → engine.NegotiatedVersion() = 1
    → plugin registers v1 gRPC services
    → engine dispenses v1 gRPC clients

  Engine stores: plugins["kubernetes"].protocolVersion = 1
  Engine uses: v1 adapter for all communication with this plugin
```

```
Later, after plugin author upgrades to SDK v2:
  Engine supports: [1, 2]
  Plugin supports: [1, 2]

  go-plugin negotiation:
    → highest common version = 2
    → engine.NegotiatedVersion() = 2
    → plugin registers v2 gRPC services
    → engine dispenses v2 gRPC clients

  Engine stores: plugins["kubernetes"].protocolVersion = 2
  Engine uses: v2 adapter — no translation needed
```

---

## 5. Engine Adapter Layer

### 5.1 Canonical Internal Types

The engine has its own canonical types that are version-independent. Adapters translate
between wire format and canonical types:

```
Plugin A (v1) ←→ gRPC v1 ←→ [Adapter V1] ←→ Engine Canonical Types
Plugin B (v2) ←→ gRPC v2 ←→ [Adapter V2] ←→ Engine Canonical Types
                                                    │
                                              ┌─────┴─────┐
                                              │ Controller │
                                              │ MCP Server │
                                              │ Frontend   │
                                              └───────────┘
```

### 5.2 Provider Interface (Engine Side)

```go
// backend/pkg/plugin/resource/types/provider.go
//
// ResourceProvider is the engine's canonical interface for communicating
// with resource plugins. It is version-independent — adapters for each
// protocol version implement this interface.
//
// The engine controller, MCP server, and frontend all use this interface.
// They never deal with proto types directly.
type ResourceProvider interface {
    // CRUD
    Get(ctx context.Context, key string, input GetInput) (*GetResult, error)
    List(ctx context.Context, key string, input ListInput) (*ListResult, error)
    Find(ctx context.Context, key string, input FindInput) (*FindResult, error)
    Create(ctx context.Context, key string, input CreateInput) (*CreateResult, error)
    Update(ctx context.Context, key string, input UpdateInput) (*UpdateResult, error)
    Delete(ctx context.Context, key string, input DeleteInput) (*DeleteResult, error)

    // Type info
    GetResourceGroups(ctx context.Context, connectionID string) (map[string]ResourceGroup, error)
    GetResourceTypes(ctx context.Context, connectionID string) (map[string]ResourceMeta, error)
    GetResourceCapabilities(ctx context.Context, resourceKey string) (*ResourceCapabilities, error)
    GetFilterFields(ctx context.Context, connectionID, resourceKey string) ([]FilterField, error)
    GetResourceSchema(ctx context.Context, connectionID, resourceKey string) (json.RawMessage, error)

    // Actions
    GetActions(ctx context.Context, connectionID, key string) ([]ActionDescriptor, error)
    ExecuteAction(ctx context.Context, connectionID, key, actionID string, input ActionInput) (*ActionResult, error)

    // Connection lifecycle
    StartConnection(ctx context.Context, connectionID string) error
    StopConnection(ctx context.Context, connectionID string) error
    ListConnections(ctx context.Context) ([]Connection, error)
    GetConnectionNamespaces(ctx context.Context, connectionID string) ([]string, error)

    // Watch
    ListenForEvents(ctx context.Context) (<-chan WatchEvent, error)

    // Relationships (doc 18) — optional, returns nil if not supported
    GetRelationships(ctx context.Context, connectionID, resourceKey string) ([]RelationshipDescriptor, error)

    // Health (doc 19) — optional, returns nil if not supported
    GetHealth(ctx context.Context, connectionID, resourceKey string, data json.RawMessage) (*ResourceHealth, error)
    GetEvents(ctx context.Context, connectionID, resourceKey, resourceID, namespace string, limit int) ([]ResourceEvent, error)
}
```

### 5.3 Adapter Implementation

```go
// backend/pkg/plugin/resource/adapter_v1.go
//
// AdapterV1 implements ResourceProvider by translating between
// engine canonical types and v1 proto types.
type AdapterV1 struct {
    client resourcev1pb.ResourcePluginClient  // gRPC client for v1 proto
}

func (a *AdapterV1) Get(ctx context.Context, key string, input GetInput) (*GetResult, error) {
    // Translate canonical GetInput → v1 proto GetRequest
    req := &resourcev1pb.GetRequest{
        Key:       key,
        Context:   marshalContext(ctx),
        Id:        input.ID,
        Namespace: input.Namespace,
    }

    // Call v1 gRPC
    resp, err := a.client.Get(ctx, req)
    if err != nil {
        return nil, err
    }

    // Translate v1 proto GetResponse → canonical GetResult
    return &GetResult{
        Data: json.RawMessage(resp.Data),  // v1 uses bytes (doc 12)
    }, nil
}

func (a *AdapterV1) Find(ctx context.Context, key string, input FindInput) (*FindResult, error) {
    // Translate FindInput.Filters → v1 proto FilterExpression
    req := &resourcev1pb.FindRequest{
        Key:       key,
        Context:   marshalContext(ctx),
        Filters:   convertFiltersToV1Proto(input.Filters),
        TextQuery: input.TextQuery,
        // ...
    }

    resp, err := a.client.Find(ctx, req)
    // ... translate response
}

// Methods not supported in v1 return graceful defaults:
func (a *AdapterV1) GetRelationships(ctx context.Context, _, _ string) ([]RelationshipDescriptor, error) {
    // v1 didn't have RelationshipProvider — return nil (not supported)
    // Engine handles nil gracefully
    return nil, nil
}
```

```go
// backend/pkg/plugin/resource/adapter_v2.go (future)
//
// AdapterV2 implements ResourceProvider by translating between
// engine canonical types and v2 proto types.
type AdapterV2 struct {
    client resourcev2pb.ResourcePluginClient
}

func (a *AdapterV2) Get(ctx context.Context, key string, input GetInput) (*GetResult, error) {
    // v2 might use a unified Query() RPC instead of separate Get/List/Find
    req := &resourcev2pb.QueryRequest{
        Key:       key,
        Mode:      resourcev2pb.QUERY_GET,
        ID:        input.ID,
        Namespace: input.Namespace,
    }

    resp, err := a.client.Query(ctx, req)
    // ... translate v2 response to canonical GetResult
}
```

### 5.4 Adapter Selection

```go
// backend/pkg/plugin/loader.go
func (l *Loader) createProvider(backend *ExternalBackend) (ResourceProvider, error) {
    version := backend.NegotiatedVersion()

    switch version {
    case 1:
        raw, err := backend.Dispense("resource")
        client := raw.(resourcev1pb.ResourcePluginClient)
        return &AdapterV1{client: client}, nil

    case 2:
        raw, err := backend.Dispense("resource")
        client := raw.(resourcev2pb.ResourcePluginClient)
        return &AdapterV2{client: client}, nil

    default:
        return nil, fmt.Errorf("unsupported SDK protocol version: %d (engine supports v1-v2)", version)
    }
}
```

---

## 6. Version Lifecycle

### 6.1 States

```
                    ┌──────────┐
                    │ CURRENT  │ ← actively developed, recommended for new plugins
                    └────┬─────┘
                         │ new version released
                    ┌────▼─────┐
                    │ SUPPORTED│ ← still works, receives security fixes only
                    └────┬─────┘
                         │ support window expires
                    ┌────▼──────┐
                    │ DEPRECATED│ ← engine warns on load, may have degraded functionality
                    └────┬──────┘
                         │ removal release
                    ┌────▼─────┐
                    │ REMOVED  │ ← engine refuses to load, adapter code deleted
                    └──────────┘
```

### 6.2 Support Windows

| Version | Status | Engine Support | Notes |
|---------|--------|---------------|-------|
| v1 | CURRENT | Required | The doc 09 design. All new plugins build against this. |
| v2 | (future) | When released, v1 → SUPPORTED | v1 supported for at least 2 major engine releases after v2 ships. |

### 6.3 Deprecation Communication

When the engine loads a plugin using a deprecated version:

```go
if version < currentVersion {
    log.Warn("Plugin %s uses SDK protocol v%d (deprecated). " +
             "Current version is v%d. Please upgrade the plugin SDK.",
             pluginID, version, currentVersion)

    // Emit event for UI notification
    runtime.EventsEmit(ctx, "plugin/deprecation", map[string]interface{}{
        "pluginID": pluginID,
        "version":  version,
        "current":  currentVersion,
        "message":  "This plugin uses an older SDK version. Some features may not be available.",
    })
}
```

---

## 7. What the v1 Design Looks Like

The doc 09 refactor is explicitly **v1**. Here's how it maps:

### 7.1 Proto

```protobuf
// proto/v1/resource.proto
syntax = "proto3";
package omniview.sdk.resource.v1;

service ResourcePlugin {
    // Connection lifecycle
    rpc LoadConnections(LoadConnectionsRequest) returns (LoadConnectionsResponse);
    rpc StartConnection(ConnectionRequest) returns (ConnectionStatusResponse);
    rpc StopConnection(ConnectionRequest) returns (ConnectionResponse);
    rpc GetConnectionNamespaces(ConnectionRequest) returns (NamespacesResponse);

    // CRUD
    rpc Get(GetRequest) returns (GetResponse);
    rpc List(ListRequest) returns (ListResponse);
    rpc Find(FindRequest) returns (FindResponse);
    rpc Create(CreateRequest) returns (CreateResponse);
    rpc Update(UpdateRequest) returns (UpdateResponse);
    rpc Delete(DeleteRequest) returns (DeleteResponse);

    // Type information
    rpc GetResourceGroups(ResourceGroupsRequest) returns (ResourceGroupsResponse);
    rpc GetResourceTypes(ResourceTypesRequest) returns (ResourceTypesResponse);
    rpc GetResourceCapabilities(ResourceCapabilitiesRequest) returns (ResourceCapabilitiesResponse);
    rpc GetFilterFields(FilterFieldsRequest) returns (FilterFieldsResponse);
    rpc GetResourceSchema(ResourceSchemaRequest) returns (ResourceSchemaResponse);
    rpc GetEditorSchemas(EditorSchemasRequest) returns (EditorSchemasResponse);

    // Actions
    rpc GetActions(GetActionsRequest) returns (GetActionsResponse);
    rpc ExecuteAction(ExecuteActionRequest) returns (ExecuteActionResponse);
    rpc StreamAction(ExecuteActionRequest) returns (stream StreamActionEvent);

    // Watch
    rpc ListenForEvents(ListenRequest) returns (stream WatchEvent);
    rpc EnsureResourceWatch(WatchResourceRequest) returns (WatchResourceResponse);
    rpc StopResourceWatch(WatchResourceRequest) returns (WatchResourceResponse);

    // Relationships (doc 18)
    rpc GetRelationships(RelationshipsRequest) returns (RelationshipsResponse);
    rpc ResolveRelationships(ResolveRelationshipsRequest) returns (ResolveRelationshipsResponse);

    // Health (doc 19)
    rpc GetHealth(HealthRequest) returns (HealthResponse);
    rpc GetResourceEvents(ResourceEventsRequest) returns (ResourceEventsResponse);
}
```

### 7.2 Go SDK

```go
// pkg/resource/v1/interfaces.go
package v1

// Plugin authors import this package and implement these interfaces.
// This is the v1 contract — it won't change until v2.
```

### 7.3 Plugin Import

```go
// plugins/kubernetes/main.go
import (
    sdk "github.com/omniviewdev/plugin-sdk/pkg/sdk"
    resourcev1 "github.com/omniviewdev/plugin-sdk/pkg/resource/v1"
)

func main() {
    p := sdk.NewPlugin(sdk.PluginConfig{
        Meta: config.PluginMeta{
            ID:      "kubernetes",
            Version: "1.0.0",
        },
        Resource: resourcev1.NewResourcePluginConfig[*kubernetes.Clientset](...),
    })
    p.Serve()  // registers v1 in VersionedPlugins
}
```

---

## 8. How a Hypothetical v2 Would Ship

### 8.1 Trigger: A Breaking Change Is Needed

Example: we decide that separate `Get/List/Find` methods should be a single `Query()`
with a mode enum, and `Watch` should be a bidirectional stream instead of server-only.

### 8.2 Steps

```
1. Create proto/v2/ with new service definition
   - package omniview.sdk.resource.v2
   - New message types as needed
   - Can freely redesign any method

2. Create pkg/resource/v2/ with new Go interfaces
   - New Resourcer interface
   - New types

3. Update SDK's Serve() to register both v1 and v2
   - VersionedPlugins: {1: v1PluginSet, 2: v2PluginSet}
   - Plugin built with new SDK supports both versions

4. Create AdapterV2 in the engine
   - Implements same ResourceProvider interface as AdapterV1
   - Translates v2 proto ↔ engine canonical types

5. Engine adds v2 to its VersionedPlugins
   - VersionedPlugins: {1: v1PluginSet, 2: v2PluginSet}
   - Existing v1 plugins continue working via AdapterV1
   - New plugins negotiate v2 via go-plugin

6. Plugin authors migrate at their own pace
   - Update SDK dependency
   - Implement v2 interfaces
   - Their plugin now registers both v1 and v2
   - Engine negotiates v2 with them automatically

7. After support window: remove v1 adapter from engine
   - Only v2 (and later) supported
   - Old plugins fail to load with clear error message
```

### 8.3 SDK Dual-Version Support

When a plugin is built with SDK v2, it can support both v1 and v2 callers.
The SDK provides a bridge:

```go
// pkg/resource/v2/compat.go
//
// WrapV2AsV1 creates a v1-compatible server from a v2 Resourcer.
// This allows a plugin built with v2 interfaces to also serve v1 clients.
// The SDK does this automatically — plugin authors don't call this directly.
func WrapV2AsV1[ClientT any](r Resourcer[ClientT]) v1.Resourcer[ClientT] {
    return &v2ToV1Wrapper[ClientT]{inner: r}
}

type v2ToV1Wrapper[ClientT any] struct {
    inner Resourcer[ClientT]
}

// v2's Query() is called with v1's Get() parameters
func (w *v2ToV1Wrapper[ClientT]) Get(ctx context.Context, client *ClientT,
    meta v1.ResourceMeta, input v1.GetInput) (*v1.GetResult, error) {
    // Translate v1 Get → v2 Query
    result, err := w.inner.Query(ctx, client, convertMetaV1ToV2(meta), QueryInput{
        Mode:      QueryGet,
        ID:        input.ID,
        Namespace: input.Namespace,
    })
    if err != nil {
        return nil, err
    }
    return convertResultV2ToV1(result), nil
}
```

This means:
- Plugin authors implement **only v2** interfaces
- The SDK automatically generates v1 compatibility
- The plugin advertises both `{1, 2}` in `VersionedPlugins`
- Old engines that only know v1 still work

---

## 9. Additive Changes Within a Version (No Bump Needed)

Most SDK evolution happens WITHIN v1 via additive changes:

### 9.1 New Optional RPC Methods

```protobuf
// Added to v1 service — not breaking
service ResourcePlugin {
    // ... existing methods ...

    // NEW — added in SDK v1.5.0
    rpc GetRelationships(RelationshipsRequest) returns (RelationshipsResponse);
}
```

Old plugins compiled without this method → engine gets `Unimplemented` gRPC status →
engine returns `nil` (feature not supported by this plugin). Handled via the existing
capability flag pattern (`ResourceCapabilities.HasRelationships = false`).

### 9.2 New Fields on Existing Messages

```protobuf
message FindRequest {
    string key = 1;
    string context = 2;
    // ... existing fields ...

    // NEW — added in SDK v1.3.0
    FilterExpression filters = 10;
    string text_query = 11;
}
```

Old plugins ignore unknown fields (proto default behavior). New plugins use them.

### 9.3 New Enum Values

```protobuf
enum FilterOperator {
    // ... existing values ...

    // NEW — added in SDK v1.4.0
    FILTER_OP_FUZZY = 15;
}
```

Old plugins receiving an unknown enum value get `0` (proto default). The SDK can
detect this and return an error: "operator not supported by this plugin version."

### 9.4 When Is an Additive Change NOT Safe?

An additive change requires a version bump if:
- The new field changes the semantics of existing fields
- The new method replaces the behavior of an existing method
- Omitting the new field causes incorrect behavior (not just missing features)

**Example of unsafe "additive" change:**
Adding a `required_auth_token` field to `GetRequest` that the server rejects if missing.
This breaks old clients that don't send it. → Needs a version bump.

---

## 10. Version Discovery & Introspection

### 10.1 Lifecycle Service (Versioned)

```protobuf
// proto/v1/lifecycle.proto
service PluginLifecycle {
    rpc GetInfo(google.protobuf.Empty) returns (GetInfoResponse);
    rpc GetCapabilities(google.protobuf.Empty) returns (GetCapabilitiesResponse);
    rpc HealthCheck(google.protobuf.Empty) returns (HealthCheckResponse);
}

message GetInfoResponse {
    string plugin_id = 1;
    string plugin_version = 2;      // semantic version of the plugin itself
    int32 sdk_protocol_version = 3; // negotiated SDK protocol version
    string sdk_version = 4;         // SDK Go module version (e.g., "v2.1.0")
    repeated int32 supported_protocol_versions = 5; // all versions this plugin can speak
}
```

### 10.2 Engine Plugin Registry

```go
type LoadedPlugin struct {
    ID                      string
    Version                 string
    NegotiatedProtocol      int
    SupportedProtocols      []int
    SDKVersion              string
    Capabilities            []string
    Provider                ResourceProvider  // versioned adapter
}
```

### 10.3 MCP Exposure

The MCP server (doc 14) can expose version info:

```json
{
    "name": "omniview_plugin_info",
    "description": "Get information about loaded plugins including SDK protocol versions",
    "inputSchema": {
        "type": "object",
        "properties": {
            "pluginId": {"type": "string"}
        }
    }
}
```

---

## 11. Testing Versioned Protocols

### 11.1 Cross-Version Integration Tests

```go
// Test that a v1 plugin works with a v2-capable engine
func TestV1PluginWithV2Engine(t *testing.T) {
    // Start a plugin that only speaks v1
    plugin := startTestPlugin(v1Only)

    // Engine supports v1 + v2
    engine := startTestEngine(supportedVersions(1, 2))

    // Should negotiate v1
    assert.Equal(t, 1, engine.plugins["test"].NegotiatedProtocol)

    // All CRUD should work via v1 adapter
    result, err := engine.Get("test", "conn1", "core::v1::Pod", GetInput{ID: "test-pod"})
    assert.NoError(t, err)
    assert.NotNil(t, result)

    // v2-only features should gracefully return nil
    rels, err := engine.GetRelationships("test", "conn1", "core::v1::Pod")
    assert.NoError(t, err)
    assert.Nil(t, rels)  // v1 plugin doesn't support relationships
}

// Test that a v2 plugin works with a v1-only engine (backwards compat)
func TestV2PluginWithV1Engine(t *testing.T) {
    // Start a plugin that speaks v1 + v2
    plugin := startTestPlugin(v1AndV2)

    // Engine only supports v1
    engine := startTestEngine(supportedVersions(1))

    // Should negotiate v1 (highest common)
    assert.Equal(t, 1, engine.plugins["test"].NegotiatedProtocol)

    // All CRUD should work via v1 adapter
    result, err := engine.Get("test", "conn1", "core::v1::Pod", GetInput{ID: "test-pod"})
    assert.NoError(t, err)
}
```

### 11.2 Adapter Conformance Tests

```go
// Shared test suite that both AdapterV1 and AdapterV2 must pass
func RunAdapterConformanceTests(t *testing.T, adapter ResourceProvider) {
    t.Run("Get returns resource data", func(t *testing.T) { ... })
    t.Run("List returns paginated results", func(t *testing.T) { ... })
    t.Run("Find with filters returns matching resources", func(t *testing.T) { ... })
    t.Run("Unknown resource key returns error", func(t *testing.T) { ... })
    t.Run("Canceled context propagates", func(t *testing.T) { ... })
    // ... all canonical behaviors that any adapter must satisfy
}

func TestAdapterV1Conformance(t *testing.T) {
    adapter := newAdapterV1(mockV1Client)
    RunAdapterConformanceTests(t, adapter)
}

func TestAdapterV2Conformance(t *testing.T) {
    adapter := newAdapterV2(mockV2Client)
    RunAdapterConformanceTests(t, adapter)
}
```

---

## 12. Decision Record

### 12.1 Why Versioned Proto Packages (Not Single Service)

| Approach | Verdict | Reason |
|----------|---------|--------|
| Single service, add methods | Rejected | Can't change or remove methods. Service grows forever. Proto compatibility rules prevent meaningful redesign. |
| Versioned proto packages | **Chosen** | Each version is a clean slate. v2 can freely redesign without proto compatibility constraints. Both exist in the same binary. |
| Version negotiation per-RPC | Rejected | Complexity: every RPC call carries a version header. Adapter logic scattered across every handler. |
| go-plugin VersionedPlugins only | Insufficient alone | go-plugin handles transport negotiation but doesn't address proto schema versioning. Need both. |

### 12.2 Why Engine Adapters (Not Plugin Adapters)

| Approach | Verdict | Reason |
|----------|---------|--------|
| Plugin translates old → new | Rejected | Old plugins can't be updated. The whole point is that old plugins work without changes. |
| Engine adapts each version | **Chosen** | Engine is always the latest version. It knows how to translate from any supported version to canonical types. Adapters are centralized, testable, and removable. |
| Shared adapter library | Rejected | Would need to be embedded in both engine and plugin. Versioning of the adapter itself becomes a problem. |

### 12.3 Why Not gRPC Reflection

gRPC server reflection could theoretically allow the engine to discover what methods
a plugin supports at runtime. Rejected because:
- Adds complexity for no gain — go-plugin's version negotiation is simpler
- Doesn't help with message schema changes (same method, different message shape)
- Doesn't work for removed methods (can't call what doesn't exist)

---

## 13. Multi-Capability Versioning

### 13.1 Current Capabilities

The plugin system has 6 plugin-implemented capabilities, each with its own gRPC service:

| Capability | Proto Service | Engine Interface | Controller Type | Wails Methods |
|-----------|---------------|-----------------|-----------------|---------------|
| **resource** | `ResourcePlugin` | `ResourceProvider` | ConnfullController | 36 |
| **exec** | `ExecPlugin` | `exec.Controller` | ConnlessController | 13 |
| **logs** | `LogsPlugin` | `logs.Controller` | ConnlessController | 8 |
| **metric** | `MetricPlugin` | `metric.Controller` | ConnlessController | 6 |
| **networker** | `NetworkerPlugin` | `networker.Controller` | ConnlessController | 7 |
| **settings** | `SettingsPlugin` | `settings.Controller` | ConnlessController | 7 |

Plus **lifecycle** — an internal bootstrap service (not a user-implemented capability).

### 13.2 Single Version Number, All Capabilities

**Decision: One SDK protocol version covers all capabilities.**

Rationale:
- All capabilities ship together in a single plugin binary
- go-plugin negotiates one version per plugin process — it cannot negotiate per-capability
- Plugin authors import one SDK version, not per-capability versions
- Simplifies the mental model: "this plugin speaks SDK v1" not "resource v1, exec v2, logs v1"

```
VersionedPlugins: map[int]plugin.PluginSet{
    1: {
        "lifecycle": &lifecycle.PluginV1{},
        "resource":  &resourcev1.GRPCPlugin{},
        "exec":      &execv1.GRPCPlugin{},
        "logs":      &logsv1.GRPCPlugin{},
        "metric":    &metricv1.GRPCPlugin{},
        "networker": &networkerv1.GRPCPlugin{},
        "settings":  &settingsv1.GRPCPlugin{},
    },
    // Future:
    // 2: {
    //     "lifecycle": &lifecycle.PluginV2{},
    //     "resource":  &resourcev2.GRPCPlugin{},
    //     "exec":      &execv2.GRPCPlugin{},
    //     // logs, metric, networker, settings may be unchanged
    //     // but still use v2 package for consistency
    //     "logs":      &logsv2.GRPCPlugin{},
    //     ...
    // },
}
```

### 13.3 Per-Capability Proto Packages

Even though the version number is shared, each capability gets its own versioned proto package:

```
plugin-sdk/
├── proto/
│   ├── v1/
│   │   ├── resource.proto          # package omniview.sdk.resource.v1
│   │   ├── resource_informer.proto
│   │   ├── filter.proto
│   │   ├── exec.proto              # package omniview.sdk.exec.v1
│   │   ├── logs.proto              # package omniview.sdk.logs.v1
│   │   ├── metric.proto            # package omniview.sdk.metric.v1
│   │   ├── networker.proto         # package omniview.sdk.networker.v1
│   │   ├── settings.proto          # package omniview.sdk.settings.v1
│   │   ├── lifecycle.proto         # package omniview.sdk.lifecycle.v1
│   │   └── common.proto            # shared types (Connection, PluginMeta, etc.)
│   │
│   └── v2/                         # (future)
│       ├── resource.proto          # only changed services get new definitions
│       ├── exec.proto              # unchanged services can re-export v1 types
│       └── ...
```

### 13.4 Unchanged Capabilities in a Version Bump

When bumping to v2 for a breaking resource change, capabilities that didn't change
(e.g., settings, logs) can either:

**Option A: Copy forward (chosen)** — Each version is self-contained. v2/settings.proto
may be identical to v1/settings.proto, but it lives in `omniview.sdk.settings.v2`.
Clean, no cross-version imports, each version is a snapshot.

**Option B: Re-export v1** — v2 imports v1's unchanged types. Creates cross-version
dependencies. Rejected — it couples versions that should be independent.

The small cost of duplicating unchanged protos is worth the isolation guarantee.

### 13.5 Engine Adapter Pattern for All Capabilities

Each capability gets its own adapter pair:

```go
// backend/pkg/plugin/resource/adapter_v1.go
type ResourceAdapterV1 struct { ... }
func (a *ResourceAdapterV1) Get(...) (*GetResult, error) { ... }

// backend/pkg/plugin/exec/adapter_v1.go
type ExecAdapterV1 struct { ... }
func (a *ExecAdapterV1) CreateSession(...) (*Session, error) { ... }

// backend/pkg/plugin/logs/adapter_v1.go
type LogsAdapterV1 struct { ... }
func (a *LogsAdapterV1) CreateSession(...) (*LogSession, error) { ... }

// ... one adapter per capability per version
```

Adapter selection at load time:

```go
func (l *Loader) createCapabilities(backend *ExternalBackend) (*PluginCapabilities, error) {
    version := backend.NegotiatedVersion()

    caps := &PluginCapabilities{}

    switch version {
    case 1:
        if raw, err := backend.Dispense("resource"); err == nil {
            caps.Resource = &ResourceAdapterV1{client: raw.(resourcev1pb.ResourcePluginClient)}
        }
        if raw, err := backend.Dispense("exec"); err == nil {
            caps.Exec = &ExecAdapterV1{client: raw.(execv1pb.ExecPluginClient)}
        }
        // ... same for all capabilities

    case 2:
        if raw, err := backend.Dispense("resource"); err == nil {
            caps.Resource = &ResourceAdapterV2{client: raw.(resourcev2pb.ResourcePluginClient)}
        }
        // ... etc
    }

    return caps, nil
}
```

### 13.6 Lifecycle Service: The Bootstrap Exception

Lifecycle is special — it's the service used for handshake and capability detection
**before** the engine knows which capabilities a plugin has. It must be present in
every plugin and every version.

```go
// Lifecycle is the FIRST thing dispensed after go-plugin connects.
// It's used to:
//   1. Get plugin info (ID, version, supported protocols)
//   2. Discover capabilities (which services this plugin implements)
//   3. Health check

// Design rule: lifecycle.proto changes must ALWAYS be additive within a version.
// If lifecycle itself needs breaking changes, that's a version bump.
// But lifecycle should be designed to never need breaking changes —
// it's intentionally minimal.
```

Lifecycle proto for v1:

```protobuf
// proto/v1/lifecycle.proto
package omniview.sdk.lifecycle.v1;

service PluginLifecycle {
    rpc GetInfo(google.protobuf.Empty) returns (GetInfoResponse);
    rpc GetCapabilities(google.protobuf.Empty) returns (GetCapabilitiesResponse);
    rpc HealthCheck(google.protobuf.Empty) returns (HealthCheckResponse);
}

message GetInfoResponse {
    string plugin_id = 1;
    string plugin_version = 2;
    int32 sdk_protocol_version = 3;
    string sdk_version = 4;
    repeated int32 supported_protocol_versions = 5;
}

message GetCapabilitiesResponse {
    repeated string capabilities = 1;  // ["resource", "exec", "logs", ...]
}
```

Even in a hypothetical v2, `GetCapabilities()` remains the same pattern — it just
returns the list of services the plugin implements. The engine uses this to know
which adapters to create.

### 13.7 Non-Resource Capabilities: Scope & Future Redesign

**Explicit scope boundary:** Exec, logs, metric, networker, and settings are **NOT
redesigned** in this v1 refactor. They get version-numbered proto packages
(`proto/v1/exec.proto`, `proto/v1/logs.proto`, etc.) but their interface designs are
copy-forwarded unchanged from the current implementation.

**Rationale:**
- Only one plugin (Kubernetes) implements them today
- Resource capability is the critical path (90%+ of data flow, the performance bottleneck)
- Non-resource capabilities work adequately — no UI freezing, no performance crisis
- Redesigning all 6 capabilities simultaneously would expand scope beyond a single sprint

**Known pain points for future redesign:**

| Pain Point | exec | logs | metric | networker | settings |
|-----------|------|------|--------|-----------|----------|
| Function-passing (not interfaces) | `TTYHandler` | `LogHandlerFunc` | `QueryFunc` | `ResourcePortForwarder` | N/A (already interface) |
| `*types.PluginContext` not `context.Context` | Yes | Yes | `*QueryContext` wrapper | Yes | Yes (no ctx at all) |
| Tight Wails coupling in controller | `EventsEmit` for data/signals | `EventsEmit` for log lines | `EventsEmit` for metric data | `EventsEmit` for session events | N/A |
| Session/subscription index in engine | exec session map | log session map | metric subscription map | port-forward session map | N/A |

**What "copy-forward into v1" means concretely:**

1. Current proto definitions (e.g., `exec.proto`) placed in `proto/v1/exec.proto` with
   updated package name (`omniview.sdk.exec.v1`)
2. Current Go interfaces placed in `pkg/v1/exec/` with no signature changes beyond
   import paths
3. Engine adapters (`ExecAdapterV1`, `LogsAdapterV1`, etc.) are thin pass-throughs —
   they wrap the current controller interface with no translation logic
4. K8s plugin exec/logs/metric/networker code migrates with **import path changes only**
   — no behavioral changes

**Future redesign:** These will be redesigned in a future sprint (possibly v2, possibly
v1.x additive if interface-only changes suffice), applying the same design principles
from doc 09: `context.Context` first, interfaces instead of function bags, optional
capabilities via type assertion, co-location of related concerns. See doc 22 §8 for
the K8s plugin's non-resource capability migration details.

---

## 14. In-Tree Migration Strategy

### 14.1 The Problem

The v1 refactor (doc 09) is a complete rewrite of the plugin SDK, engine controllers,
and the K8s plugin. This is a significant amount of code. We need to build v1 in-tree
alongside the existing implementation, then cut over cleanly.

### 14.2 Strategy: Parallel Packages, Single Switchover

```
Phase 1: Build v1 packages alongside existing code
Phase 2: Implement K8s plugin against v1
Phase 3: Switch loader to use v1
Phase 4: Delete old code
```

### 14.3 Directory Layout During Migration

```
plugin-sdk/
├── pkg/
│   ├── resource/              # EXISTING — current unversioned code
│   │   ├── plugin.go          # current ResourcePlugin
│   │   ├── server.go          # current gRPC server
│   │   └── ...
│   │
│   ├── v1/                    # NEW — v1 versioned code
│   │   ├── resource/
│   │   │   ├── interfaces.go  # new Resourcer[ClientT], Watcher[ClientT]
│   │   │   ├── types.go       # new FindInput, ListInput, etc.
│   │   │   ├── plugin.go      # new GRPCPlugin
│   │   │   ├── server.go      # new gRPC server
│   │   │   └── client.go      # new gRPC client
│   │   ├── exec/
│   │   │   └── ...
│   │   ├── logs/
│   │   │   └── ...
│   │   └── ...
│   │
│   └── sdk/
│       └── plugin.go          # updated Serve() with VersionedPlugins
│
├── proto/
│   ├── v1/                    # NEW — versioned proto definitions
│   │   ├── resource.proto
│   │   ├── exec.proto
│   │   └── ...
│   │
│   └── *.proto                # EXISTING — current unversioned protos (deleted in Phase 4)

backend/pkg/plugin/
├── resource/
│   ├── controller.go          # EXISTING — current controller
│   ├── client.go              # EXISTING — current Wails client
│   ├── controller_v1.go       # NEW — v1 controller (built in Phase 1)
│   └── adapter_v1.go          # NEW — v1 adapter (built in Phase 1)
│
├── exec/
│   ├── controller.go          # EXISTING
│   └── controller_v1.go       # NEW
│
├── loader.go                  # MODIFIED in Phase 3 (switch to VersionedPlugins)
└── ...

plugins/kubernetes/
├── pkg/plugin/resource/
│   ├── register_gen.go        # EXISTING — current resource registration
│   └── register_v1.go         # NEW — v1 resource registration (built in Phase 2)
│
├── main.go                    # MODIFIED in Phase 3 (switch to sdk.NewPlugin with v1)
└── ...
```

### 14.4 Phase Details

**Phase 1: Build v1 packages (no behavioral change)**

- Create `plugin-sdk/proto/v1/` with all versioned proto definitions
- Create `plugin-sdk/pkg/v1/resource/`, `pkg/v1/exec/`, etc. with new interfaces
- Create engine-side `adapter_v1.go` and `controller_v1.go` for each capability
- Create engine-side canonical types (version-independent `ResourceProvider`, etc.)
- Write adapter conformance tests against mock gRPC clients
- **Zero impact on running system** — new code isn't wired up yet

**Phase 2: Implement K8s plugin against v1**

- Create `register_v1.go` for each resource type (implementing v1 `Resourcer[ClientT]`)
- Create `main_v1.go` that uses `sdk.NewPlugin()` with v1 config
- Write integration tests: v1 K8s plugin ↔ v1 adapters
- Can test via separate binary or build tag
- **Zero impact on running system** — old main.go still active

**Phase 3: Switch over**

- Update `loader.go`: `Plugins` → `VersionedPlugins` with `{1: v1PluginSet}`
- Update `manager.go`: create controllers using v1 adapters
- Update K8s `main.go`: use v1 `sdk.NewPlugin()` / `Serve()`
- Run full integration test suite
- **This is the single switchover point** — after this, v1 is live

**Phase 4: Delete old code**

- Delete `plugin-sdk/pkg/resource/` (old unversioned)
- Delete `plugin-sdk/proto/*.proto` (old unversioned)
- Delete `backend/pkg/plugin/resource/controller.go` (old controller)
- Delete `plugins/kubernetes/pkg/plugin/resource/register_gen.go` (old registration)
- Clean up any remaining references
- **No behavioral change** — just removing dead code

### 14.5 Build Tags (Optional)

If you want to test both old and new in CI simultaneously:

```go
//go:build v1refactor

package resource

// v1 controller code...
```

```go
//go:build !v1refactor

package resource

// existing controller code...
```

This allows `go build` (old) and `go build -tags v1refactor` (new) to coexist.
Optional — the parallel-packages approach works without build tags too.

### 14.6 Migration Verification Gates

Before each phase transition:

| Gate | Phase 1 → 2 | Phase 2 → 3 | Phase 3 → 4 |
|------|------------|------------|------------|
| All v1 interfaces compile | ✓ | ✓ | ✓ |
| Adapter conformance tests pass | ✓ | ✓ | ✓ |
| K8s plugin implements all v1 interfaces | — | ✓ | ✓ |
| Integration tests (v1 K8s ↔ v1 engine) pass | — | ✓ | ✓ |
| Old code removed, no dead imports | — | — | ✓ |
| Full E2E suite passes | — | — | ✓ |

---

## 15. Frontend & Event System Impact

### 15.1 Wails Binding Types: Version-Agnostic

Wails auto-generates `models.ts` from Go structs via `wails generate`. The generated
types come from **engine canonical types**, not SDK proto types:

```
Plugin (SDK types) → gRPC → [Adapter] → Engine Canonical Types → Wails Binding → models.ts
                                              ↑
                                     This is the source of truth
                                     for frontend TypeScript types
```

**Key insight:** When the SDK bumps from v1 to v2, the adapter translates v2 proto
types to the same engine canonical types. `models.ts` only changes when engine
canonical types change — which happens once (during the v1 refactor) and then stays
stable across SDK version bumps.

### 15.2 Event System: 34 Events, 11 SDK-Dependent

The Go→Frontend event system uses `runtime.EventsEmit()` with string keys and
untyped payloads. Audit results:

| Category | Events | SDK-Dependent Payloads | Dynamic Keys |
|----------|--------|----------------------|--------------|
| Plugin lifecycle | 14 | 1 (state_change) | 0 |
| Resource/Informer | 5 | 5 (ADD/UPDATE/DELETE/STATE/sync) | 5 |
| Streaming actions | 1 | 1 (ActionEvent) | 1 |
| Exec sessions | 2 | 1 (session data) | 2 |
| Logs streaming | 2 | 1 (log batches) | 2 |
| Metrics | 2 | 1 (query response) | 2 |
| Port forwarding | 2 | 1 (session info) | 0 |
| Menu/UI/diagnostics | 6 | 0 | 1 |
| **Total** | **34** | **11** | **13** |

### 15.3 Why Events Are Version-Agnostic Too

Events are emitted by **engine controllers**, not by plugins directly. The controller
receives data through the adapter (already translated to canonical types) and emits
events using those canonical types:

```go
// Engine controller (version-independent)
func (c *Controller) handleWatchEvent(event WatchEvent) {
    // event is already in canonical types (from adapter)
    // The event payload is canonical, not proto
    runtime.EventsEmit(c.ctx, eventKey, InformerAddPayload{
        ID:        event.ID,
        Namespace: event.Namespace,
        Data:      event.Data,  // json.RawMessage — already canonical
    })
}
```

The event payload structs (`InformerAddPayload`, `InformerUpdatePayload`, etc.)
are engine-side types, not SDK proto types. They only change when the engine
refactors them — not when the SDK protocol version bumps.

### 15.4 Dynamic Event Key Contract

13 events use dynamic keys constructed identically in Go and TypeScript:

```go
// Go (controller)
key := fmt.Sprintf("%s/%s/%s/%s", pluginID, connectionID, resourceKey, action)
```

```typescript
// TypeScript (hook)
const key = `${pluginID}/${connectionID}/${resourceKey}/${action}`;
```

**This key format is an engine contract, not an SDK contract.** It changes only
during the v1 engine refactor, not during SDK version bumps. After v1 ships, the
key format is stable.

### 15.5 What Changes During the v1 Refactor

The v1 refactor (doc 09) changes engine canonical types. This is a one-time event:

| Component | What Changes | Impact |
|-----------|-------------|--------|
| `models.ts` | Regenerated from new engine types | All frontend type imports update |
| Event payloads | New canonical structs | Frontend hooks update to match |
| Event keys | Potentially new format | Frontend listeners update |
| React hooks | Updated for new subscription model | `useResources.ts` rewritten |
| Wails client methods | New `IClient` interface | `Client.d.ts` / `Client.js` regenerated |

**After the v1 refactor ships, none of these change for SDK version bumps.**
The adapter pattern isolates the frontend completely.

### 15.6 Frontend Migration Checklist (v1 Refactor)

- [ ] Engine canonical types finalized → `wails generate` produces new `models.ts`
- [ ] Event payload structs finalized → update all frontend event listeners
- [ ] Event key format finalized → update all dynamic key construction in hooks
- [ ] New `IClient` interface finalized → regenerate `Client.d.ts` and `Client.js`
- [ ] React hooks rewritten for v1 subscription model
- [ ] Runtime package rebuilt (`npm run build` in `packages/omniviewdev-runtime/`)
- [ ] Full UI smoke test

---

## 16. Relationship to Other Documents

| Doc | Relationship |
|-----|-------------|
| 09 | SDK interface design — becomes explicitly v1 with versioned proto packages. All interfaces live in `pkg/v1/resource/`. |
| 12 | Serialization — `bytes` proto fields are a v1 design decision. v2 could change encoding. Adapter handles translation. |
| 13 | Filter/query types — part of v1. FilterOperator enum is additive within v1. FilterExpression is a v1 proto message. |
| 14 | MCP server — uses engine canonical types, not proto types. Version-agnostic by design. |
| 15 | Query execution — filter push-down is a plugin-side concern. Version-independent (Resourcer's Find() is versioned, but the push-down pattern is a code convention, not a protocol contract). |
| 17 | Cross-resource routing — engine-side GlobalResourceRegistry uses canonical types. Version-agnostic. |
| 18 | Relationships — `RelationshipProvider` is additive in v1 (new RPC). Could be redesigned in v2. Engine RelationshipGraph uses canonical `RelationshipDescriptor`, not proto types. |
| 19 | Diagnostic graph — `HealthProvider`, `EventProvider` are additive in v1 (new RPCs). Engine HealthCache uses canonical `ResourceHealth`, not proto types. |

---

## 17. Implementation Checklist for v1

When implementing the doc 09 refactor, these versioning foundations must be in place:

### Proto & SDK

- [ ] All proto files in `proto/v1/` with versioned package names (`omniview.sdk.resource.v1`, etc.)
- [ ] One proto file per capability: `resource.proto`, `exec.proto`, `logs.proto`, `metric.proto`, `networker.proto`, `settings.proto`, `lifecycle.proto`, `common.proto`
- [ ] Go interfaces in `pkg/v1/resource/`, `pkg/v1/exec/`, etc.
- [ ] `GRPCPlugin` per capability in `pkg/v1/{capability}/plugin.go`
- [ ] SDK `Serve()` uses `VersionedPlugins` with `{1: v1PluginSet}` containing all 7 capability entries

### Engine

- [ ] Engine loader uses `VersionedPlugins` with `{1: v1PluginSet}`
- [ ] Engine stores `NegotiatedProtocol` per loaded plugin in `LoadedPlugin`
- [ ] Engine canonical types defined (version-independent `ResourceProvider`, `ExecProvider`, etc.)
- [ ] `AdapterV1` per capability — implements engine canonical interface
- [ ] Adapter selection based on `NegotiatedVersion()` in `createCapabilities()`
- [ ] `PluginLifecycle.GetInfo()` returns `supported_protocol_versions: [1]`
- [ ] `PluginLifecycle.GetCapabilities()` returns list of implemented capabilities

### Testing

- [ ] Adapter conformance test suite per capability
- [ ] Cross-version integration test skeleton (initially v1 vs v1)
- [ ] Migration verification gates (§14.6) documented and automated

### Frontend

- [ ] Engine canonical types finalized → `wails generate` → new `models.ts`
- [ ] Event payload structs using canonical types (not proto types)
- [ ] All React hooks updated for v1 model
- [ ] Runtime package rebuilt
