# 17: Cross-Resource Query Routing — Engine-Level Query Router

This document describes how queries route across resource types and plugins at the engine
level. Currently, all CRUD routes through `pluginID → clients[pluginID]` with no cross-plugin
awareness. This doc designs the future engine-level query router that enables cross-resource
queries, central type discovery, and unified search.

**Scope:** Design document only. Addresses FR-1 (central query interface), FR-3 (cross-plugin
querying), and FR-2 (central lookup index). Not implemented in the SDK refactor.

**Key principle:** The SDK doesn't change. Cross-resource routing is an engine concern.
Plugins expose the same `Resourcer` interface — the engine routes queries to the right plugin.

---

## 1. Current State

### 1.1 Current Routing Model

```
Frontend:
  controller.Get(pluginID, connectionID, resourceKey, input)
                  │
Engine Controller (controller.go):
  client := clients[pluginID]           // map[string]ResourceProvider
  result := client.Get(ctx, resourceKey, input)
                  │
Plugin (gRPC):
  resourceController.Get(ctx, resourceKey, input)
                  │
Resourcer:
  Get(ctx, client, meta, input)
```

**Problem:** The caller must know `pluginID` to route the request. There's no way to say
"find all Pods across all plugins" or "what resource types exist across all plugins?"

### 1.2 Current Type Discovery

```go
// client.go — per-plugin type queries
GetResourceGroups(pluginID, connectionID string) map[string]ResourceGroup
GetResourceTypes(pluginID, connectionID string) map[string]ResourceMeta
GetResourceType(pluginID, typeID string) (*ResourceMeta, error)
HasResourceType(pluginID, typeID string) bool
```

Every call requires `pluginID`. The frontend iterates all plugins manually to get a
complete picture.

### 1.3 Resource Key Format

Resource keys use `group::version::kind` (e.g., `core::v1::Pod`, `apps::v1::Deployment`).
Keys are unique within a plugin but NOT globally unique — two plugins could theoretically
register `core::v1::Pod` (though this shouldn't happen in practice).

---

## 2. Design: Central Resource Registry

### 2.1 Global Type Registry

The engine maintains a global registry of all resource types across all plugins:

```go
// ResourceTypeEntry maps a resource type to its source plugin and capabilities.
type ResourceTypeEntry struct {
    PluginID     string
    ResourceKey  string               // "core::v1::Pod"
    Meta         ResourceMeta         // description, column defs, etc.
    Capabilities ResourceCapabilities // from doc 13
    Connections  []string             // active connections that have this type
}

// GlobalResourceRegistry maintains a unified view of all resource types.
type GlobalResourceRegistry struct {
    // Primary index: globalKey → entry
    // globalKey = "{pluginID}/{resourceKey}" for uniqueness
    types map[string]*ResourceTypeEntry

    // Secondary indexes for efficient lookup
    byKind     map[string][]*ResourceTypeEntry  // "Pod" → entries
    byGroup    map[string][]*ResourceTypeEntry  // "core" → entries
    byPlugin   map[string][]*ResourceTypeEntry  // "kubernetes" → entries
}
```

### 2.2 Registry Population

```
Plugin loads:
  1. Engine calls TypeProvider.GetResourceTypes(connID) → map[string]ResourceMeta
  2. For each resource type:
     a. TypeProvider.GetResourceCapabilities(resourceKey) → capabilities
     b. Registry.Register(pluginID, resourceKey, meta, caps)
  3. Emit "registry/types_changed" event

Plugin unloads:
  1. Registry.UnregisterPlugin(pluginID)
  2. Emit "registry/types_changed" event

Dynamic resource discovered (CRD):
  1. Engine receives new resource type from TypeProvider
  2. Registry.Register(pluginID, resourceKey, meta, caps)
  3. Emit "registry/types_changed" event
  4. MCP server refreshes tool list (doc 14)
```

### 2.3 Global Query Methods

```go
type GlobalResourceRegistry interface {
    // Discovery — no pluginID required
    ListAllResourceTypes() []*ResourceTypeEntry
    FindResourceType(kind string) []*ResourceTypeEntry  // "Pod" → entries from all plugins
    SearchResourceTypes(query string) []*ResourceTypeEntry  // fuzzy match on kind, group, description

    // Capability-filtered discovery
    ListFilterableTypes() []*ResourceTypeEntry
    ListWatchableTypes() []*ResourceTypeEntry
    ListActionableTypes() []*ResourceTypeEntry

    // Resolve for routing
    ResolveResourceKey(resourceKey string) (*ResourceTypeEntry, error)  // returns pluginID
    ResolveKind(kind string) ([]*ResourceTypeEntry, error)
}
```

---

## 3. Design: Query Router

### 3.1 Single-Resource Queries (Existing)

No change to existing routing. The caller specifies `pluginID`:

```go
controller.Get(pluginID, connectionID, resourceKey, input)
```

### 3.2 Cross-Resource Queries (New)

A new `QueryRouter` sits above the existing controller:

```go
type QueryRouter interface {
    // Query routes a request to the correct plugin based on resourceKey.
    // Caller doesn't need to know pluginID.
    Query(ctx context.Context, resourceKey string, connectionID string,
        input FindInput) (*FindResult, error)

    // QueryAll executes a query across ALL plugins that have this resource type.
    // Returns aggregated results.
    QueryAll(ctx context.Context, resourceKey string,
        input FindInput) (map[string]*FindResult, error)
    // Key: "{pluginID}/{connectionID}", Value: results from that plugin/connection

    // QueryMultiType queries multiple resource types in one call.
    // Useful for "show me all workloads" (Pods + Deployments + StatefulSets + ...).
    QueryMultiType(ctx context.Context, resourceKeys []string, connectionID string,
        input FindInput) (map[string]*FindResult, error)
    // Key: resourceKey, Value: results for that type
}
```

### 3.3 Query Routing Flow

```
QueryRouter.Query(ctx, "core::v1::Pod", "k8s-prod", findInput)
    │
    ├── Registry.ResolveResourceKey("core::v1::Pod")
    │   → ResourceTypeEntry{PluginID: "kubernetes", ...}
    │
    ├── Validate: does "k8s-prod" exist for plugin "kubernetes"?
    │
    └── controller.Find("kubernetes", "k8s-prod", "core::v1::Pod", findInput)
        │
        └── (existing flow — doc 15)
```

### 3.4 Multi-Plugin Query Flow

```
QueryRouter.QueryAll(ctx, "core::v1::Pod", findInput)
    │
    ├── Registry.FindResourceType("Pod")
    │   → [
    │       {PluginID: "kubernetes", Connections: ["k8s-prod", "k8s-staging"]},
    │       {PluginID: "rancher", Connections: ["rancher-main"]},
    │     ]
    │
    ├── For each (pluginID, connectionID) pair:
    │   controller.Find(pluginID, connectionID, "core::v1::Pod", findInput)
    │   (parallel execution with context.Context deadline)
    │
    └── Aggregate results:
        {
          "kubernetes/k8s-prod": FindResult{...},
          "kubernetes/k8s-staging": FindResult{...},
          "rancher/rancher-main": FindResult{...},
        }
```

---

## 4. MCP Integration

### 4.1 MCP Tool Routing

The MCP server (doc 14) uses the `QueryRouter` instead of directly calling the controller:

```
MCP tool: kubernetes_core_v1_Pod_find
    │
    ├── Parse tool name → pluginID="kubernetes", resourceKey="core::v1::Pod"
    │
    └── QueryRouter.Query(ctx, "core::v1::Pod", connectionID, findInput)
```

### 4.2 Cross-Plugin MCP Tools

Additional MCP tools for cross-plugin queries:

```
omniview_query_all("core::v1::Pod", filters, ...)
  → QueryRouter.QueryAll(ctx, "core::v1::Pod", findInput)

omniview_search_resources("nginx", ...)
  → QueryRouter.TextSearch(ctx, "nginx", limit)
  → searches across all plugins/types with TextSearchProvider
```

### 4.3 Resource Type Discovery Tools

```
omniview_list_all_resource_types()
  → GlobalResourceRegistry.ListAllResourceTypes()
  → returns [{pluginID, resourceKey, capabilities}, ...]

omniview_find_resource_type("Pod")
  → GlobalResourceRegistry.FindResourceType("Pod")
  → returns entries from all plugins
```

---

## 5. Central Lookup Index (FR-2)

### 5.1 Purpose

A central full-text index of all watched resources, enabling fast text search across
all resource types and plugins without calling each plugin individually.

### 5.2 Architecture

```
┌─────────────────────────────────────────────────────────┐
│ Engine: Central Lookup Index                              │
│                                                           │
│ Subscribes to ALL watch events from ALL plugins           │
│   ADD    → index resource                                 │
│   UPDATE → re-index resource                              │
│   DELETE → remove from index                              │
│                                                           │
│ Index fields: name, namespace, labels, annotations,       │
│               kind, group, pluginID, connectionID         │
│                                                           │
│ Query: TextSearch("nginx") → matching resources across    │
│        all types and plugins                              │
└─────────────────────────────────────────────────────────┘
```

### 5.3 Index Entry

```go
type IndexEntry struct {
    PluginID     string
    ConnectionID string
    ResourceKey  string
    ResourceID   string
    Namespace    string
    Name         string
    Labels       map[string]string
    SearchText   string  // concatenated searchable fields
}
```

### 5.4 Relationship to TextSearchProvider

- `TextSearchProvider` (doc 13) → per-resource-type, plugin-side search
- Central Lookup Index → cross-type, engine-side search using indexed watch data

Both can coexist:
- Fast global search → Central Lookup Index
- Deep per-type search with plugin-specific logic → TextSearchProvider

### 5.5 Index Implementation Options

| Option | Pros | Cons |
|--------|------|------|
| bleve (Go full-text search) | Pure Go, embedded, good for moderate scale | Memory usage for large clusters |
| SQLite FTS5 | Efficient, battle-tested, low memory | CGo dependency |
| In-memory trie/inverted index | Fastest, no dependency | Must build from scratch |

Recommendation: bleve for v1 — pure Go, embedded, well-documented.

---

## 6. Event Key Format and Routing

### 6.1 Current Event Keys

Watch events use: `{pluginID}/{connectionID}/{resourceKey}/{ACTION}`

Example: `kubernetes/k8s-prod/core::v1::Pod/ADD`

### 6.2 Central Index Subscription

The central index subscribes to all events using wildcard or explicit registration:

```go
// On plugin load, subscribe to all resource events for indexing
for _, resourceKey := range plugin.ResourceKeys() {
    for _, connID := range plugin.ConnectionIDs() {
        events.On(fmt.Sprintf("%s/%s/%s/*", pluginID, connID, resourceKey), index.HandleEvent)
    }
}
```

Or, more practically, the controller's `informerListener()` notifies the index directly:

```go
// In informerListener(), after emitting to frontend subscribers:
if c.centralIndex != nil {
    c.centralIndex.HandleEvent(event)  // always, regardless of subscription
}
```

---

## 7. Frontend Impact

### 7.1 New API Methods on IClient

```go
// Global discovery (no pluginID required)
ListAllResourceTypes() ([]*ResourceTypeEntry, error)
SearchResourceTypes(query string) ([]*ResourceTypeEntry, error)

// Cross-plugin queries
QueryAll(resourceKey string, input FindInput) (map[string]*FindResult, error)
GlobalTextSearch(query string, limit int) ([]IndexEntry, error)
```

### 7.2 Frontend Usage Patterns

**Command palette / global search:**
```typescript
// User types "nginx" in command palette
const results = await client.GlobalTextSearch("nginx", 20);
// → [{pluginID: "kubernetes", resourceKey: "core::v1::Pod", name: "nginx-abc123", ...}, ...]
```

**Resource type picker:**
```typescript
// "What resource types are available?"
const types = await client.ListAllResourceTypes();
// → [{pluginID: "kubernetes", resourceKey: "core::v1::Pod", capabilities: {...}}, ...]
```

---

## 8. Edge Cases

### 8.1 Resource Key Conflicts

Two plugins register the same `resourceKey` (unlikely but possible):

```
kubernetes: "core::v1::Pod"
k3s:        "core::v1::Pod"
```

Resolution: The global key is `{pluginID}/{resourceKey}`. Cross-plugin queries return
results from all matching plugins, keyed by `{pluginID}/{connectionID}`.

### 8.2 Plugin Crashes During Query

`QueryAll` executes queries in parallel with `context.Context` deadlines. If a plugin
crashes mid-query:

```go
results := map[string]*FindResult{}
errors := map[string]error{}

// Parallel fan-out
for _, entry := range matchingEntries {
    go func(e *ResourceTypeEntry) {
        result, err := controller.Find(e.PluginID, connID, e.ResourceKey, input)
        if err != nil {
            errors[key] = err  // partial failure
        } else {
            results[key] = result
        }
    }(entry)
}

// Return partial results + errors
return results, errors
```

The caller (MCP server, frontend) handles partial failures gracefully.

### 8.3 Large Fan-Out

`QueryMultiType` with many resource types (e.g., "all workloads" = 10+ types) must limit
concurrency:

```go
sem := make(chan struct{}, 5)  // max 5 concurrent plugin queries
for _, key := range resourceKeys {
    sem <- struct{}{}
    go func(k string) {
        defer func() { <-sem }()
        // query...
    }(key)
}
```

---

## 9. Implementation Phases

| Phase | Scope | Dependencies |
|-------|-------|-------------|
| 1 | `GlobalResourceRegistry` — central type registry | SDK refactor complete |
| 2 | `QueryRouter.Query()` — single-resource routing without pluginID | Phase 1 |
| 3 | `QueryRouter.QueryAll()` — cross-plugin queries | Phase 2 |
| 4 | Central Lookup Index — full-text search across all resources | Phase 2, watch events |
| 5 | `QueryRouter.QueryMultiType()` — multi-type queries | Phase 3 |
| 6 | Frontend integration — command palette, global search | Phases 3-4 |
| 7 | MCP integration — cross-plugin MCP tools | Phase 3, doc 14 |

Each phase is independently shippable. Phase 1-2 can happen immediately after the SDK
refactor. Phases 3-7 are future sprints.

---

## 10. What This Document Does NOT Cover

| Topic | Where |
|-------|-------|
| SDK interface design | Doc 09 |
| Filter type system | Doc 13 |
| MCP server runtime | Doc 14 |
| Query execution within a single plugin | Doc 15 |
| NL-to-filter translation | Doc 16 |
| Resource relationships/graph | Doc 18 |
| Cross-resource aggregation (count pods per node) | Future — agent-side or engine-side |
