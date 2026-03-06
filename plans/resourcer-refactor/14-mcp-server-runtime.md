# 14: MCP Server Runtime — Hosting, Registration & Transport

This document describes how Omniview hosts an MCP server that auto-exposes plugin resources
as MCP tools. It covers server lifecycle, tool discovery, transport options, authentication,
and how the runtime maps SDK interfaces (doc 09, doc 13) to MCP protocol primitives.

**Scope:** Design document only. Not implemented in the SDK refactor — this is a future
integration sprint that consumes the interfaces designed in docs 09 and 13.

**Prerequisite reading:** Doc 09 (SDK interface design), Doc 13 (query/filter/MCP readiness).

---

## 1. Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────┐
│  External AI Agent / IDE Extension / CLI                            │
│  (Claude, Cursor, VS Code Copilot, custom agent)                   │
│                                                                     │
│  MCP Client ─── stdio / SSE / HTTP ──────────────────────┐         │
└──────────────────────────────────────────────────────────┬─────────┘
                                                           │
┌──────────────────────────────────────────────────────────┴─────────┐
│  Omniview MCP Server (embedded in engine process)                  │
│                                                                     │
│  ┌───────────────────────────────────────────────────────────────┐ │
│  │ Tool Registry                                                  │ │
│  │                                                                 │ │
│  │  For each plugin:                                               │ │
│  │    For each resource type:                                      │ │
│  │      ResourceCapabilities → which tools to generate             │ │
│  │      FilterFields → inputSchema for find/list                   │ │
│  │      ResourceSchema → outputSchema for get/list                 │ │
│  │      ActionDescriptors → action tools with ParamsSchema         │ │
│  │                                                                 │ │
│  │  Tools generated:                                               │ │
│  │    {plugin}_{group}_{version}_{kind}_get                        │ │
│  │    {plugin}_{group}_{version}_{kind}_list                       │ │
│  │    {plugin}_{group}_{version}_{kind}_find                       │ │
│  │    {plugin}_{group}_{version}_{kind}_create                     │ │
│  │    {plugin}_{group}_{version}_{kind}_update                     │ │
│  │    {plugin}_{group}_{version}_{kind}_delete                     │ │
│  │    {plugin}_{group}_{version}_{kind}_{actionID}                 │ │
│  └───────────────────────────────────────────────────────────────┘ │
│                              │                                      │
│                              ▼                                      │
│  ┌───────────────────────────────────────────────────────────────┐ │
│  │ Request Router                                                  │ │
│  │   tool name → (pluginID, resourceKey, operation)                │ │
│  │   → controller.Get/List/Find/Create/Update/Delete/ExecuteAction │ │
│  └───────────────────────────────────────────────────────────────┘ │
│                              │                                      │
│                              ▼                                      │
│  ┌───────────────────────────────────────────────────────────────┐ │
│  │ Engine Controller (existing — doc 09 §4.7)                      │ │
│  │   clients map[pluginID] → ResourceProvider (gRPC stub)          │ │
│  └───────────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────────────┘
```

**Key insight:** The MCP server does NOT live inside plugins. It lives in the engine process
and delegates to plugins through the existing `controller` → `ResourceProvider` chain.
Plugins don't know they're being called by an AI agent — they see the same CRUD/action
interface they always see.

---

## 2. Server Lifecycle

### 2.1 Startup

The MCP server starts when Omniview starts (or on first AI connection). It does NOT wait for
plugins to load — it starts with an empty tool registry and populates dynamically.

```
1. Engine starts → MCP server binds to transport (see §4)
2. Plugin loads → engine calls TypeProvider.GetResourceTypes()
3. For each resource type:
   a. TypeProvider.GetResourceCapabilities(resourceKey) → capability flags
   b. If filterable: TypeProvider.GetFilterFields(connID, resourceKey) → filter fields
   c. If hasSchema: TypeProvider.GetResourceSchema(connID, resourceKey) → JSON Schema
   d. If hasActions: provider.GetActions(connID, resourceKey) → action descriptors
4. MCP tool registry generates tool descriptors from the above
5. MCP server emits tools/list_changed notification to connected clients
```

### 2.2 Dynamic Updates

Tools update when:

| Event | Action |
|-------|--------|
| Plugin loads | Generate tools for all resource types, emit `tools/list_changed` |
| Plugin unloads/crashes | Remove tools for that plugin, emit `tools/list_changed` |
| Connection added | Refresh connection-dependent filter fields and schemas |
| Connection removed | No tool change (tools are per-resource-type, not per-connection) |
| Dynamic resource discovered (CRD) | Generate tools for new type, emit `tools/list_changed` |

### 2.3 Shutdown

The MCP server shuts down when Omniview exits. In-flight requests are canceled via
`context.Context` propagation through the engine controller to the plugin.

---

## 3. Tool Generation

### 3.1 Tool Name Convention

```
{pluginID}_{group}_{version}_{kind}_{operation}
```

Examples:
- `kubernetes_core_v1_Pod_get`
- `kubernetes_core_v1_Pod_list`
- `kubernetes_core_v1_Pod_find`
- `kubernetes_apps_v1_Deployment_scale` (action)
- `kubernetes_apps_v1_Deployment_delete`

The `group::version::kind` resource key format maps directly: `::` → `_`.

### 3.2 Tool Generation Rules

For each `(pluginID, resourceKey)` pair:

```
caps := TypeProvider.GetResourceCapabilities(resourceKey)

if caps.CanGet    → generate {prefix}_get
if caps.CanList   → generate {prefix}_list
if caps.CanFind   → generate {prefix}_find     // only if CanFind AND Filterable
if caps.CanCreate → generate {prefix}_create
if caps.CanUpdate → generate {prefix}_update
if caps.CanDelete → generate {prefix}_delete   // mark as destructiveHint

if caps.HasActions:
  for each ActionDescriptor:
    generate {prefix}_{action.ID}
    if action.Dangerous → mark as destructiveHint
```

### 3.3 Schema Generation

Each tool's `inputSchema` is built from SDK types:

| Tool Type | inputSchema Source |
|-----------|-------------------|
| `_get` | Static: `{connectionId, id, namespace?}` |
| `_list` | Static: `{connectionId, namespaces?, pageSize?, cursor?}` |
| `_find` | `FilterFields` → field enum + operator enum + value. Plus `textQuery`, pagination |
| `_create` | `GetResourceSchema()` — full resource JSON Schema |
| `_update` | `GetResourceSchema()` — full resource JSON Schema + `{id, namespace?}` |
| `_delete` | Static: `{connectionId, id, namespace?}`. `destructiveHint: true` |
| `_{actionID}` | `ActionDescriptor.ParamsSchema` merged with `{connectionId, id, namespace?}` |

The `outputSchema` is `GetResourceSchema()` for CRUD tools, `ActionDescriptor.OutputSchema`
for action tools.

### 3.4 connectionId Handling

Every tool requires a `connectionId` parameter. The MCP server also exposes utility tools:

- `{pluginID}_list_connections` — returns available connections with status
- `{pluginID}_list_namespaces` — returns namespaces for a connection
- `{pluginID}_list_resource_types` — returns available resource types with capabilities

These let an AI agent discover what's available before making resource queries.

---

## 4. Transport Options

### 4.1 stdio (Primary)

The primary transport for local AI tools (Claude Code, IDE extensions).

```
┌──────────┐  stdin/stdout  ┌──────────────────┐
│ AI Agent │ ◄────────────► │ Omniview MCP     │
│          │    JSON-RPC    │ Server (embedded) │
└──────────┘                └──────────────────┘
```

**Implementation:** Omniview exposes itself as an MCP server that can be configured in
an AI agent's MCP server list. The agent spawns Omniview's MCP bridge process (or connects
to the running Omniview instance via a local socket relay).

**Option A — Embedded stdio bridge:**
Omniview ships a lightweight CLI binary (`omniview-mcp`) that:
1. Connects to the running Omniview engine via local IPC (Unix socket or named pipe)
2. Exposes stdio MCP protocol to the calling AI agent
3. Proxies tool calls to the engine controller

**Option B — Direct stdio on Omniview process:**
If the AI agent can attach to the Omniview process's stdio (unlikely for a GUI app, but
valid for headless/server mode).

**Recommendation:** Option A — a separate `omniview-mcp` bridge binary.

### 4.2 SSE (Server-Sent Events)

For remote AI agents or web-based integrations. Omniview serves an HTTP endpoint:

```
GET  /mcp/sse          → SSE stream (server → client notifications)
POST /mcp/messages     → JSON-RPC requests (client → server)
```

### 4.3 Streamable HTTP (MCP 2025-03-26+)

The latest MCP spec supports streamable HTTP transport — single HTTP endpoint that handles
both requests and notifications. If adopted, simplifies to:

```
POST /mcp → JSON-RPC (request/response + server-initiated notifications via SSE)
```

### 4.4 Transport Selection

| Transport | Use Case | Latency | Complexity |
|-----------|----------|---------|------------|
| stdio (via bridge) | Claude Code, local IDE | Lowest | Medium (bridge binary) |
| SSE | Remote agents, web | Low | Low |
| Streamable HTTP | Future standard | Low | Low |

All transports use the same `ToolRegistry` and `RequestRouter` — transport is pluggable.

---

## 5. Authentication & Authorization

### 5.1 Local (stdio)

No authentication needed — the bridge binary runs as the same user as Omniview and
communicates via local IPC. The OS enforces access control.

### 5.2 Network (SSE / HTTP)

When exposed over the network:

| Mechanism | Description |
|-----------|-------------|
| API key | Simple bearer token, configured in Omniview settings |
| Connection scoping | MCP session is bound to specific connections (not all clusters) |
| Read-only mode | Optional flag: disable create/update/delete tools |
| Action confirmation | `ActionDescriptor.Dangerous` requires explicit confirmation even from AI |

### 5.3 Per-Tool Authorization

The MCP server respects existing Omniview permission model:

- **Connection access:** An MCP session can only access connections the user has configured
- **Operation access:** Respects `ResourceDefinition.SupportedOperations` — if a resource
  doesn't support Delete, no delete tool is generated
- **Dangerous actions:** `ActionDescriptor.Dangerous = true` → MCP tool has
  `annotations.destructiveHint = true`. The AI agent is expected to confirm with the user.

---

## 6. Request Flow

```
AI Agent calls tools/call("kubernetes_core_v1_Pod_find", {
  connectionId: "k8s-prod",
  filters: [
    {field: "status.phase", operator: "eq", value: "Running"},
    {field: "spec.nodeName", operator: "eq", value: "worker-1"}
  ],
  pageSize: 50
})
    │
    ▼
MCP Server: RequestRouter
    │ Parse tool name → pluginID="kubernetes", resourceKey="core::v1::Pod", op="find"
    │ Convert MCP args → FindInput{
    │     Filters: &FilterExpression{
    │         Logic: FilterAnd,
    │         Predicates: []FilterPredicate{
    │             {Field: "status.phase", Operator: OpEqual, Value: "Running"},
    │             {Field: "spec.nodeName", Operator: OpEqual, Value: "worker-1"},
    │         },
    │     },
    │     Pagination: &PaginationParams{PageSize: 50},
    │ }
    │
    ▼
Engine Controller: Find("kubernetes", "k8s-prod", "core::v1::Pod", findInput)
    │ client := clients["kubernetes"]
    │ ctx := connectedCtx(conn)
    │
    ▼
Plugin (gRPC): ResourceProvider.Find(ctx, "core::v1::Pod", findInput)
    │ Pod Resourcer converts FilterPredicates → K8s ListOptions
    │ (label selectors, field selectors)
    │
    ▼
K8s API Server → response → FindResult → MCP tool result (JSON)
```

---

## 7. Notifications & Subscriptions

### 7.1 Resource Change Notifications

MCP supports server-initiated notifications. The MCP server can subscribe to resource
watch events and forward them:

```
MCP notification: notifications/resources/updated
  {pluginID: "kubernetes", resourceKey: "core::v1::Pod", connectionId: "k8s-prod",
   event: "UPDATE", resourceId: "my-pod", namespace: "default"}
```

This uses the existing subscription mechanism (doc 08) — the MCP server acts as another
subscriber alongside the frontend.

### 7.2 Tool List Changes

When plugins load/unload or CRDs are discovered:

```
MCP notification: notifications/tools/list_changed
```

The AI agent then calls `tools/list` to refresh its tool list.

---

## 8. Implementation Components

### 8.1 New Engine Packages

```
backend/pkg/mcp/
├── server.go           // MCP server lifecycle, transport binding
├── registry.go         // Tool registry — generates MCP tool descriptors from SDK types
├── router.go           // Request router — tool name → controller method dispatch
├── transport_stdio.go  // stdio transport adapter
├── transport_sse.go    // SSE transport adapter
├── auth.go             // Authentication middleware
└── bridge/
    └── main.go         // omniview-mcp bridge binary (stdio relay)
```

### 8.2 Dependencies on SDK Interfaces

| MCP Component | SDK Interface Used | Method |
|--------------|-------------------|--------|
| Tool discovery | `TypeProvider` | `GetResourceTypes()`, `GetResourceCapabilities()` |
| Filter schema | `TypeProvider` | `GetFilterFields()` |
| Resource schema | `TypeProvider` | `GetResourceSchema()` |
| Action tools | `ActionProvider` | `GetActions()` |
| CRUD execution | `ResourceProvider` | `Get()`, `List()`, `Find()`, `Create()`, `Update()`, `Delete()` |
| Action execution | `ActionProvider` | `ExecuteAction()`, `StreamAction()` |
| Connection info | `ConnectionLifecycleProvider` | `ListConnections()`, `GetConnectionNamespaces()` |
| Watch events | Subscription system | `SubscribeResource()`, event channels |

### 8.3 No SDK Changes Required

The MCP server is a consumer of SDK interfaces, not a provider. It lives in the engine
process and uses the same `controller` → `ResourceProvider` path as the frontend.
**No changes to plugin-sdk are needed** — everything the MCP server needs is already
defined in docs 09 and 13.

---

## 9. Configuration

### 9.1 User Settings

```json
{
  "mcp": {
    "enabled": true,
    "transport": "stdio",
    "sse": {
      "port": 9850,
      "host": "127.0.0.1"
    },
    "auth": {
      "apiKey": "...",
      "readOnly": false
    },
    "connectionScope": ["k8s-prod", "k8s-staging"]
  }
}
```

### 9.2 AI Agent Configuration (Claude Code example)

```json
{
  "mcpServers": {
    "omniview": {
      "command": "omniview-mcp",
      "args": ["--socket", "/tmp/omniview.sock"]
    }
  }
}
```

---

## 10. What This Document Does NOT Cover

| Topic | Where |
|-------|-------|
| Filter/query type system | Doc 13 |
| SDK interface design | Doc 09 |
| Query execution model (how filters reach backends) | Doc 15 |
| NL-to-filter translation | Doc 16 |
| Cross-resource queries | Doc 17 |
| Relationship graph | Doc 18 |
| Specific MCP protocol wire format | MCP specification |
| AI agent prompt engineering for Omniview tools | Future doc |

---

## 11. Open Questions

| # | Question | Options | Recommendation |
|---|----------|---------|----------------|
| 1 | Bridge binary vs. in-process stdio? | Separate `omniview-mcp` binary vs. stdio on main process | Separate binary — GUI apps can't use stdio |
| 2 | Tool granularity? | One tool per operation vs. combined `resource_query` tool | One per operation — cleaner for AI agents |
| 3 | Connection auto-selection? | Agent must specify connectionId vs. auto-pick default | Agent specifies — explicit is better for multi-cluster |
| 4 | Streaming actions over MCP? | MCP doesn't natively support streaming tool results | Return operation ID, agent polls or subscribes |
| 5 | Tool name length limits? | `kubernetes_networking.k8s.io_v1_NetworkPolicy_find` is long | Acceptable — MCP has no name length limit |
