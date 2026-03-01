# 08: Resource Data Flow — Performance Requirements & Refactor Guide

This document captures the business requirements, performance invariants, and implementation
details of the resource data flow system — from Go informer caches through the Wails bridge
to the React frontend. It is written to survive a full plugin-sdk refactor: every requirement
here must be met by whatever replaces the current function-passing `ResourcePluginOpts` struct.

---

## Table of Contents

1. [Problem Statement](#1-problem-statement)
2. [Architecture Overview](#2-architecture-overview)
3. [Performance Invariants](#3-performance-invariants)
4. [Requirement: Pull-Based Data Model](#4-requirement-pull-based-data-model)
5. [Requirement: Frontend Subscription Lifecycle](#5-requirement-frontend-subscription-lifecycle)
6. [Requirement: Sync Policies](#6-requirement-sync-policies)
7. [Requirement: Informer State Propagation](#7-requirement-informer-state-propagation)
8. [Requirement: Event Batching (Future)](#8-requirement-event-batching-future)
9. [Current Implementation Map](#9-current-implementation-map)
10. [Refactor Guidance: Where Logic Should Live](#10-refactor-guidance-where-logic-should-live)
11. [Refactor Guidance: Interface Design](#11-refactor-guidance-interface-design)
12. [Anti-Patterns to Avoid](#12-anti-patterns-to-avoid)
13. [Verification Criteria](#13-verification-criteria)

---

## 1. Problem Statement

### What happened

Connecting to a Kubernetes cluster with 90+ resource types and thousands of objects caused the
WebView to pin at 100% CPU and freeze for 30+ seconds. Freelens loads the same cluster instantly.

### Root cause

The Go backend pushed **every resource object** through the Wails IPC bridge during initial
informer sync — unsolicited. Each `runtime.EventsEmit()` call serializes a full K8s object to
JSON and pushes it through the WebView bridge. For a cluster with thousands of objects across
90+ resource types, this means thousands of IPC calls before the user has navigated to any
resource view.

### Freelens comparison

| Technique | Freelens | Omniview (before fix) |
|-----------|----------|-----------------------|
| Data loading | Renderer fetches directly from K8s API | Go pushes all data through IPC |
| Subscription model | Per-view `subscribeStores()` | Global — all events to all views |
| Event batching | 1s MobX `reaction({ delay: 1000 })` | None — each event triggers a React update |
| Sidebar data | Discovery metadata only, zero instances | Full resource objects |
| Watch dedup | `WatchCount` ref-counts per resource type | No dedup |

### Design principle

**Keep Go-side informers running for everything** (we want full cluster data for future
full-text indexing, cross-resource dependency graphs, and policy evaluation). **Don't push data
to the browser unless the browser asks for it.** The browser pulls via `List()` and explicitly
subscribes to live updates only for resources currently in view.

---

## 2. Architecture Overview

```
┌──────────────────────────────────────────────────────────────────────────┐
│  Frontend (React)                                                        │
│                                                                          │
│  useResources({ resourceKey: "core::v1::Pod", connectionID: "..." })     │
│    │                                                                     │
│    ├─ mount:  SubscribeResource(pluginID, connID, resourceKey)           │
│    │          List(pluginID, connID, resourceKey, input) → React Query   │
│    │          EventsOn("{pluginID}/{connID}/{key}/ADD|UPDATE|DELETE")     │
│    │                                                                     │
│    ├─ live:   Buffered events → queryClient.setQueryData()               │
│    │                                                                     │
│    └─ unmount: UnsubscribeResource(pluginID, connID, resourceKey)        │
│                EventsOff (via closer functions)                          │
│                                                                          │
└───────────────────────────────┬──────────────────────────────────────────┘
                                │ Wails IPC Bridge
┌───────────────────────────────┴──────────────────────────────────────────┐
│  Backend Engine (Go)                                                     │
│                                                                          │
│  resource.controller                                                     │
│    ├─ subscriptions map[string]int   (ref-counted, keyed by              │
│    │                                  pluginID/connID/resourceKey)        │
│    ├─ informerListener()             select loop:                        │
│    │     addChan    → if subscribed → EventsEmit ADD                     │
│    │     updateChan → if subscribed → EventsEmit UPDATE                  │
│    │     deleteChan → if subscribed → EventsEmit DELETE                  │
│    │     stateChan  → ALWAYS emit   → EventsEmit STATE                   │
│    │                                                                     │
│    ├─ List() → delegates to plugin via gRPC                              │
│    └─ SubscribeResource() / UnsubscribeResource()                        │
│                                                                          │
└───────────────────────────────┬──────────────────────────────────────────┘
                                │ gRPC (hashicorp/go-plugin)
┌───────────────────────────────┴──────────────────────────────────────────┐
│  Plugin SDK (Go, runs in plugin process)                                 │
│                                                                          │
│  resourceController[ClientT]                                             │
│    ├─ InformerManager                                                    │
│    │    ├─ syncPolicies map[string]InformerSyncPolicy                    │
│    │    ├─ SyncOnConnect  → started during StartConnectionInformer()     │
│    │    ├─ SyncOnFirstQuery → lazy-started by ensureInformer() on List() │
│    │    └─ SyncNever → never started, always direct queries              │
│    │                                                                     │
│    ├─ List(ctx, key, input) → ensureInformer() → resourcer.List()        │
│    └─ ListenForEvents() → streams ADD/UPDATE/DELETE/STATE to engine      │
│                                                                          │
└───────────────────────────────┬──────────────────────────────────────────┘
                                │
┌───────────────────────────────┴──────────────────────────────────────────┐
│  Plugin Implementation (e.g. Kubernetes)                                 │
│                                                                          │
│  kubeInformerHandle                                                      │
│    ├─ DynamicSharedInformerFactory (client-go)                            │
│    ├─ RegisterResource() → ForResource(gvr).Informer().AddEventHandler() │
│    ├─ Start()            → factory.Start(stopCh) + WaitForCacheSync()    │
│    ├─ StartResource()    → single informer start for lazy resources      │
│    └─ resolveGVR()       → discovery API fallback for CRDs               │
│                                                                          │
└──────────────────────────────────────────────────────────────────────────┘
```

---

## 3. Performance Invariants

These are non-negotiable. Any refactor must preserve all of these.

| # | Invariant | Metric |
|---|-----------|--------|
| P1 | **Zero unsolicited IPC during connection** | 0 `EventsEmit(ADD\|UPDATE\|DELETE)` calls between `StartConnection()` and the first `SubscribeResource()` call |
| P2 | **UI interactive within 3 seconds of click** | From cluster card click to a scrollable/clickable resource list |
| P3 | **Web Content CPU below 30% during connect** | Measured in Activity Monitor while informers sync in the background |
| P4 | **UI never freezes** | No janked frame longer than 100ms at any point during connection |
| P5 | **Sub-second live updates for subscribed resources** | From K8s object mutation to UI update visible: <1s |
| P6 | **Unsubscribed resources have zero IPC cost** | A resource type not currently viewed consumes zero bridge bandwidth |
| P7 | **Go-side data completeness** | All informer caches are populated for all connected resources regardless of subscription state |
| P8 | **Graceful degradation for large resource types** | Viewing a resource type with 10,000+ objects does not freeze the UI (requires batching — see §8) |
| P9 | **Minimal serialization boundaries** | Resource data crosses at most 2 serialization boundaries: `json.Marshal` in plugin process → `JSON.parse` in frontend. No intermediate `structpb` round-trip. See `plans/resourcer-refactor/12-resource-data-serialization.md` |

---

## 4. Requirement: Pull-Based Data Model

### REQ-PULL-1: Browser pulls data via List()

The frontend gets initial data by calling `List()` (a Wails-bound RPC). This reads from the
Go-side informer cache (or triggers a direct API call for SyncNever resources). The result
populates the React Query cache.

**Current implementation**: `useResources.ts` → `useQuery({ queryFn: () => List(...) })`

**Refactor requirement**: Whatever replaces `ResourcePluginOpts.Resourcers` must still provide
a `List()` path that reads from the informer cache when available. The List response is the
source of truth for the initial view — live events only update incrementally from this baseline.

### REQ-PULL-2: Live events only for subscribed resources

ADD/UPDATE/DELETE events for a resource type are only forwarded over the Wails bridge if the
frontend has an active subscription for that `(pluginID, connectionID, resourceKey)` tuple.

**Current implementation**: `controller.informerListener()` checks `isSubscribed()` before
calling `runtime.EventsEmit()`. The subscription is ref-counted to support multiple UI
components viewing the same resource.

**Refactor requirement**: The subscription gate must live in the **engine** (backend controller),
not in the plugin SDK. Plugins should not need to know about frontend subscriptions. The engine
receives all events from all plugins via gRPC streams and decides what to forward.

### REQ-PULL-3: STATE events are always forwarded

`InformerStateEvent` events (SYNCING, SYNCED, ERROR, CANCELLED) are always emitted regardless
of subscription state. They are small (no resource payloads) and needed by:
- Status bar: global `informer/STATE` event for aggregate sync progress
- Per-connection: `{pluginID}/{connectionID}/informer/STATE` for per-resource state
- `useInformerState` hook: renders sync progress indicators

**Refactor requirement**: STATE events must never be gated by subscription. This is an engine-level
concern.

### REQ-PULL-4: Subscription refetch on activation

When a subscription is registered, the frontend should refetch via `List()` to catch any events
that occurred between the initial `List()` and the subscription becoming active. This closes the
race window.

**Current implementation**: `SubscribeResource().then(() => queryClient.invalidateQueries())`

**Refactor requirement**: This is purely a frontend concern. The backend just needs to ensure
that `SubscribeResource` returning means events will flow from that point forward.

---

## 5. Requirement: Frontend Subscription Lifecycle

### REQ-SUB-1: Subscribe on view mount

When a resource view component mounts, it must:
1. Call `SubscribeResource(pluginID, connectionID, resourceKey)` — tells Go to start forwarding
2. Register `EventsOn` listeners for ADD/UPDATE/DELETE
3. Call `List()` to populate initial data (via React Query)
4. After subscription confirms, refetch List() to close the race window

### REQ-SUB-2: Unsubscribe on view unmount

When a resource view component unmounts (user navigates away), it must:
1. Call closer functions from `EventsOn` to stop receiving events
2. Call `UnsubscribeResource(pluginID, connectionID, resourceKey)` — tells Go to stop forwarding

The React Query cache retains the data after unmount. When the user navigates back, a fresh
`List()` call repopulates current state.

### REQ-SUB-3: Ref-counted subscriptions

Multiple UI components may view the same resource type simultaneously (e.g., a detail drawer
open while the list is visible). Subscriptions are ref-counted: events flow as long as
`refcount > 0`, and stop only when the last subscriber unsubscribes.

**Current implementation**: `controller.subscriptions` is `map[string]int`. Subscribe increments,
unsubscribe decrements or deletes at zero.

**Refactor requirement**: Ref-counting must live in the engine, not the SDK. The SDK has no
concept of "frontend views."

### REQ-SUB-4: Subscription survives re-renders

The subscription effect must have correct dependency arrays so it re-subscribes when the user
navigates to a different resource type within the same view. Dependencies:
`[pluginID, connectionID, resourceKey]`.

The cleanup function unsubscribes from the old resource, the new effect subscribes to the new one.

### REQ-SUB-5: Subscription key matches event key

The subscription key format must exactly match the event key format used in `EventsEmit`. Both
use `pluginID/connectionID/resourceKey` where `resourceKey` is the `event.Key` field from the
informer payload (e.g., `core::v1::Pod`).

---

## 6. Requirement: Sync Policies

### REQ-SYNC-1: Three sync policies

| Policy | Behavior | Use Case |
|--------|----------|----------|
| `SyncOnConnect` (default) | Informer starts when connection opens | Core resources: Pods, Deployments, Services, Nodes, Namespaces |
| `SyncOnFirstQuery` | Informer starts on first `Get()`/`List()`/`Find()` call | Non-core builtins: RBAC, Storage, Networking |
| `SyncNever` | No informer; always direct API calls | High-cardinality or ephemeral resources |

### REQ-SYNC-2: Plugin declares policies via configuration

Plugins specify sync policies as a map of resource key → policy. Resources not in the map
default to `SyncOnConnect`.

**Current implementation**: `ResourcePluginOpts.SyncPolicies map[string]types.InformerSyncPolicy`

**Refactor requirement**: In the new interface-based SDK, sync policies should be declarative.
Options:
- A method on a `ResourcePlugin` interface: `SyncPolicies() map[string]InformerSyncPolicy`
- Per-resource annotation on a `Resourcer` interface: `SyncPolicy() InformerSyncPolicy`
- Registration-time configuration (preferred — co-located with resource registration)

The per-resource annotation approach is most idiomatic: each resource type knows its own policy.
The plugin doesn't need a central map.

### REQ-SYNC-3: Lazy start is transparent to callers

When a `List()` arrives for a `SyncOnFirstQuery` resource whose informer hasn't started:
1. The SDK triggers `InformerManager.EnsureResource()` which calls `InformerHandle.StartResource()`
2. The informer starts and syncs
3. The `List()` call returns data (first call may be slower — informer cache is being populated)
4. Subsequent calls return instantly from the warm cache

**The caller (frontend) does not need to know about sync policies.** It just calls `List()`.
The lazy start is an engine/SDK internal optimization.

### REQ-SYNC-4: Sync policy affects startup order, not data availability

Sync policies control **when informers start**, not what data is available. All three policy
types support `List()` — for `SyncOnConnect` and `SyncOnFirstQuery`, the informer cache serves
the data. For `SyncNever`, the resourcer does a direct API call.

### REQ-SYNC-5: Fast path for core resources

`SyncOnConnect` resources should be ready before the user opens any resource view. This means
the informer cache for Pods, Deployments, Services, Namespaces, and Nodes should be populated
within the first few seconds of connection.

**Current implementation**: The Kubernetes plugin's `kubeInformerHandle.Start()` starts all
`SyncOnConnect` informers via `factory.Start()` and waits for cache sync with per-resource
30-second timeouts and parallel WaitForCacheSync goroutines.

---

## 7. Requirement: Informer State Propagation

### REQ-STATE-1: Per-resource state lifecycle

Every resource type tracked by the informer system has a state:

```
Pending → Syncing → Synced
                  → Error
         Cancelled (resource skipped by InformerHandle)
```

State changes are emitted as `InformerStateEvent` via the STATE channel.

### REQ-STATE-2: Connection-level aggregate

The `InformerConnectionSummary` provides:
- `resources`: map of resource key → current state
- `resourceCounts`: map of resource key → item count in cache
- `totalResources`: total number of registered resource types
- `syncedCount`: number in Synced state
- `errorCount`: number in Error state

This is used by `useInformerState` for progress indicators and by the status bar.

### REQ-STATE-3: State is queryable

`GetInformerState(pluginID, connectionID)` returns the current aggregate on demand. This is
the initial fetch for the `useInformerState` hook. Live updates come via STATE events.

### REQ-STATE-4: State survives subscription changes

Informer state is independent of frontend subscriptions. An informer can be in `Synced` state
even if no frontend component is subscribed to that resource. State reflects the Go-side cache
status, not the frontend's interest.

---

## 8. Requirement: Event Batching (Future)

**Not yet implemented. Document requirements for future work.**

### REQ-BATCH-1: RAF-based batching for live updates

Even with subscriptions, a high-churn resource type (e.g., Events, with thousands of objects
being created/deleted rapidly) can flood the frontend with individual state updates. Batch
ADD/UPDATE/DELETE events and flush via `requestAnimationFrame` (~16ms batches) for a single
`queryClient.setQueryData()` + `produce()` pass.

### REQ-BATCH-2: Timer-based batching for initial sync

When the user navigates to a resource view whose informer is still syncing (rare with
SyncOnConnect, common with SyncOnFirstQuery), the initial flood of ADD events should be
batched with a larger window (500ms–1s, like Freelens's `eventsBuffer`).

### REQ-BATCH-3: Batching is a frontend concern

The Go side sends individual events. Batching happens in the frontend event handlers. This
keeps the Go→frontend protocol simple and pushes presentation-layer optimization to the
presentation layer.

**Planned implementation location**: `packages/omniviewdev-runtime/src/utils/batchedEventBuffer.ts`

---

## 9. Requirement: Structured Query/Filter Support (Future — FR-11)

### REQ-QUERY-1: Structured filter predicates on Find

The SDK must support structured filter predicates on Find operations. Predicates use a closed
set of operators (`eq`, `neq`, `gt`, `lt`, `contains`, `prefix`, `in`, `regex`, `haskey`, `exists`, etc.)
and declared field paths. The Resourcer's `Find()` implementation applies the filters to its
backend (label selectors for K8s, API filters for AWS, SQL WHERE for databases). The SDK does
NOT execute filters on behalf of plugins — it may validate predicates against declared fields.

See `plans/resourcer-refactor/13-query-filter-mcp-design.md` for full type definitions.

### REQ-QUERY-2: Discoverable filter fields

Resourcers implementing `FilterableProvider` declare which fields are filterable, with types,
valid operators, and enum values. AI agents and MCP tool generators enumerate these to build
query interfaces without hard-coded knowledge.

### REQ-QUERY-3: Auto-derived capability flags

The SDK auto-derives `ResourceCapabilities` from type assertions at registration time. This
includes: CRUD flags, Watchable, Filterable, Searchable, HasActions, HasSchema, NamespaceScoped,
ScaleHint. Exposed via `TypeProvider.GetResourceCapabilities()`.

---

## 10. Current Implementation Map

### Files changed (Phase 1 — Pull-Based Data Model)

| File | What changed |
|------|-------------|
| `backend/pkg/plugin/resource/controller.go` | Added `subscriptions map[string]int`, `subsMu sync.RWMutex`. Added `SubscribeResource()`, `UnsubscribeResource()`, `isSubscribed()`. Gated ADD/UPDATE/DELETE in `informerListener()` behind `isSubscribed()`. STATE events pass unconditionally. |
| `backend/pkg/plugin/resource/client.go` | Added `SubscribeResource` and `UnsubscribeResource` to `IClient` interface and `Client` wrapper struct. Both return `error` for Wails binding compatibility. |
| `packages/omniviewdev-runtime/src/wailsjs/go/resource/Client.{js,d.ts}` | Auto-generated Wails bindings for `SubscribeResource` and `UnsubscribeResource`. |
| `packages/omniviewdev-runtime/src/hooks/resource/useResources.ts` | Imports `SubscribeResource`/`UnsubscribeResource`. Mount effect calls `SubscribeResource()`, registers EventsOn listeners, then refetches on subscription confirmation. Unmount calls `UnsubscribeResource()`. Deps: `[pluginID, connectionID, resourceKey]`. Removed 4 `console.log` calls from hot paths. |
| `ui/pages/connecting/index.tsx` | Removed 3 `console.log` calls from reducer. |

### Files NOT changed (existing infrastructure used as-is)

| File | Role |
|------|------|
| `plugin-sdk/pkg/resource/types/informer_state.go` | `InformerSyncPolicy`, `InformerResourceState`, `InformerStateEvent` types |
| `plugin-sdk/pkg/resource/types/informer.go` | `InformerHandle` interface: `RegisterResource`, `Start`, `StartResource`, `Stop` |
| `plugin-sdk/pkg/resource/controller.go` | SDK controller: `ensureInformer()` on List path, `StartConnectionInformer()` flow |
| `plugin-sdk/pkg/resource/services/informer_manager.go` | `InformerManager`: connection-level informer lifecycle, `EnsureResource()` for lazy start |
| `plugin-sdk/pkg/sdk/resource_opts.go` | `SyncPolicies` field on `ResourcePluginOpts` |
| `plugins/kubernetes/pkg/plugin/resource/informer.go` | `kubeInformerHandle`: K8s-specific informer implementation |
| `packages/omniviewdev-runtime/src/hooks/resource/useInformerState.ts` | Informer state hook (uses STATE events — unaffected by subscription changes) |

---

## 10. Refactor Guidance: Where Logic Should Live

### Engine (backend/pkg/plugin/resource/)

The engine is the **bridge orchestrator**. It owns:

- **Subscription management**: `SubscribeResource`, `UnsubscribeResource`, subscription map, ref-counting
- **Event gating**: Only forward ADD/UPDATE/DELETE for subscribed resources
- **STATE forwarding**: Always forward STATE events (no gating)
- **Wails IPC emission**: `runtime.EventsEmit()` calls
- **Plugin lifecycle**: Start/stop plugin gRPC connections, crash recovery
- **Connection state**: Merge, persist, and serve connection data

The engine should **never** know about:
- Kubernetes, AWS, or any specific backend
- Informer cache internals
- Resource schemas or GVR resolution
- Client creation or authentication

### Plugin SDK (plugin-sdk/pkg/)

The SDK is the **plugin-side framework**. It owns:

- **Informer lifecycle**: Create, start, stop informers per connection
- **Sync policy enforcement**: `SyncOnConnect` starts immediately, `SyncOnFirstQuery` defers, `SyncNever` skips
- **Lazy informer start**: `ensureInformer()` on Get/List/Find paths
- **Resource type management**: Registration, lookup, metadata
- **Client management**: Create, refresh, start, stop per-connection clients
- **Event streaming**: Fan-in from informer channels to gRPC stream (ListenForEvents)
- **State aggregation**: Per-resource state tracking, `InformerConnectionSummary`

The SDK should **never** know about:
- Frontend subscriptions (that's an engine concern)
- Wails events or IPC
- React Query, hooks, or any UI framework
- Connection persistence or local stores

### Plugin Implementation (e.g., plugins/kubernetes/)

The plugin is the **backend-specific adapter**. It owns:

- **InformerHandle**: Wraps client-go's `DynamicSharedInformerFactory`
- **GVR resolution**: Static map + discovery API fallback
- **Resource skip logic**: Which resources to ignore (bindings, token reviews, etc.)
- **Client creation**: Build clientsets from kubeconfig/connection context
- **Resourcer implementations**: How to List/Get/Create/Update/Delete each resource type

### Frontend (packages/omniviewdev-runtime/)

The frontend is the **presentation layer**. It owns:

- **Subscription lifecycle**: Subscribe on mount, unsubscribe on unmount
- **Data fetching**: List() via React Query
- **Live update application**: Event handlers that update React Query cache via immer
- **Event batching**: RAF/timer batching of incoming events (future)
- **State rendering**: Sync progress, error indicators, resource tables

---

## 11. Refactor Guidance: Interface Design

> **Finalized in [09-resource-plugin-sdk-interface-design.md](09-resource-plugin-sdk-interface-design.md).**
> The tentative sketches below have been replaced by the concrete design in doc 09.
> This section is retained as a summary of how data flow requirements map to the new interfaces.

### Per-resource Watcher & WatchEventSink (doc 09 §4.4)

Watch is a per-resource capability via the `Watcher[ClientT]` interface (type-asserted on `Resourcer`
implementations). The plugin author implements a single blocking `Watch()` method. The SDK manages
all lifecycle — starting, stopping, restarting goroutines via context cancellation. Events flow
through `WatchEventSink` (OnAdd, OnUpdate, OnDelete, OnStateChange). `InformerFactory` and
`InformerHandle` are eliminated from the plugin-author API.

### Resourcer with optional SyncPolicyDeclarer (doc 09 §4.3)

Sync policies are no longer a central map. Each `Resourcer[ClientT]` implementation can optionally
implement `SyncPolicyDeclarer` (a separate interface, type-asserted). Default is `SyncOnConnect`.
This co-locates policy with resource logic without bloating the core `Resourcer` interface.
Only meaningful if the Resourcer also implements `Watcher`.

### ResourcePluginConfig (doc 09 §4.6)

`ResourcePluginOpts` is replaced by `ResourcePluginConfig[ClientT]` — a struct with interface-typed
fields: `ConnectionProvider[ClientT]` (required), `[]ResourceRegistration[ClientT]` (resources),
`DiscoveryProvider` (optional), `ErrorClassifier` (optional). Watch support is auto-detected
on Resourcer implementations via `Watcher` type assertion — no config field needed.

### gRPC boundary: Provider (doc 09 §4.7)

`ResourceProvider` is renamed to `Provider`, composed of:
- `OperationProvider` — CRUD (Get, List, Find, Create, Update, Delete)
- `ConnectionLifecycleProvider` — Start, Stop, Load, List, Watch connections
- `WatchProvider` — StartConnectionWatch, StopConnectionWatch, ListenForEvents, GetWatchState + per-resource lifecycle (EnsureResourceWatch, StopResourceWatch, RestartResourceWatch)
- `TypeProvider` — GetResourceTypes, GetResourceGroups, GetResourceDefinition
- `ActionProvider` — GetActions, ExecuteAction, StreamAction
- `EditorSchemaProvider` — GetEditorSchemas

Layout is **removed** — it's a UI concern, not an SDK concern.

**Important**: `SubscribeResource`/`UnsubscribeResource` are NOT part of this interface. They
are engine-only methods that never cross the gRPC boundary. The plugin always streams all
events; the engine decides what to forward.

---

## 12. Anti-Patterns to Avoid

### AP-1: Don't push data the browser didn't ask for

The original bug. Never `EventsEmit` resource payloads without checking subscriptions first.
The Go informer cache is the data store; the browser is a view.

### AP-2: Don't gate STATE events

STATE events carry no resource payloads and are needed by status bar, progress indicators, and
other global UI. Gating them behind subscriptions would break sync progress visibility.

### AP-3: Don't put subscription logic in the SDK

The SDK runs inside the plugin process. It has no knowledge of the frontend. Subscription
management is an engine concern. The SDK just streams everything to the engine.

### AP-4: Don't use void Wails bindings

All Wails-bound Go methods must return at least `error`. Void methods may not properly bind
through the Wails IPC bridge. (Discovered during implementation: void SubscribeResource calls
were silently failing.)

### AP-5: Don't use empty dependency arrays for subscription effects

The old code used `React.useEffect(() => { ... }, [])` for event listeners. This breaks when
the user navigates between resource types within the same view (pluginID/connectionID/
resourceKey change but the component doesn't unmount). Dependencies must include all values
used in the subscription key.

### AP-6: Don't console.log in hot paths

`console.log` in event handlers (ADD/UPDATE/DELETE callbacks) causes measurable UI jank when
receiving hundreds of events. All such logging was removed.

### AP-7: Don't serialize full objects when metadata suffices

STATE events carry counts and strings, not resource payloads. This is by design. If future
features need summary data in the status bar, use pre-aggregated counters, not object dumps.

---

## 13. Verification Criteria

Any refactor that touches the resource data flow must pass these tests:

| # | Test | How to verify |
|---|------|---------------|
| V1 | Connect to a cluster with 90+ resource types | Click connect → UI interactive <3s, CPU <30% |
| V2 | Navigate to Pods | Data appears via List(), table renders correctly |
| V3 | Create a pod while viewing Pods | Pod appears in table within ~1s (live ADD event) |
| V4 | Delete a pod while viewing Pods | Pod disappears from table within ~1s (live DELETE event) |
| V5 | Navigate away from Pods, then back | Data refreshes via List(), live updates resume |
| V6 | View Pods in two panels simultaneously | Both receive live updates (ref-counted subscription) |
| V7 | Close one panel, keep the other | Remaining panel continues receiving updates |
| V8 | Navigate to a SyncOnFirstQuery resource | First load may be slower (informer starting), subsequent loads instant |
| V9 | Check Go informer caches via logs | All resource types have populated caches regardless of subscription state |
| V10 | No `EventsEmit(ADD\|UPDATE\|DELETE)` during connect | Log/instrument the informerListener to verify zero emissions before first SubscribeResource call |
| V11 | Wails binding error surfacing | If SubscribeResource fails, error is logged (not silently swallowed) |
