# 16: NL-to-Filter Translation — AI Agent Query Building

This document describes how AI agents translate natural language queries into structured
`FilterExpression` predicates using `FilterField` declarations from the SDK. This is the
AI agent's responsibility, not the SDK's — but the SDK must provide enough introspection
data for the agent to do this correctly.

**Scope:** Design document only. Describes the contract between the SDK (data provider)
and AI agents (query builders). No implementation in the SDK refactor.

**Key principle:** The SDK provides declarative metadata; the AI agent reasons about it.
The SDK never parses natural language.

---

## 1. The Translation Problem

An AI agent receives a user query like:

> "Show me all pods that are crashing on worker-1 in production"

The agent must translate this into:

```json
{
  "filters": {
    "logic": "and",
    "predicates": [
      {"field": "spec.nodeName", "operator": "eq", "value": "worker-1"},
      {"field": "status.containerStatuses.restartCount", "operator": "gt", "value": 0}
    ]
  },
  "namespaces": ["production"]
}
```

To do this, the agent needs:
1. What fields exist and what they mean (→ `FilterField.Description`)
2. What operators are valid per field (→ `FilterField.Operators`)
3. What values are valid for enum fields (→ `FilterField.AllowedValues`)
4. Whether the resource is namespace-scoped (→ `ResourceCapabilities.NamespaceScoped`)
5. Scale expectations to choose query strategy (→ `ResourceCapabilities.ScaleHint`)

---

## 2. What the SDK Provides

### 2.1 FilterField as Agent Context

Each `FilterField` declaration (doc 13 §3.3) provides exactly what an AI agent needs
for tool parameter completion:

```go
FilterField{
    Path:          "status.phase",
    DisplayName:   "Phase",
    Description:   "Pod lifecycle phase",           // ← AI reads this
    Type:          FilterFieldEnum,
    Operators:     []FilterOperator{OpEqual, OpNotEqual, OpIn},
    AllowedValues: []string{"Pending", "Running", "Succeeded", "Failed", "Unknown"},
}
```

The agent sees this in the MCP tool's `inputSchema` and knows:
- "phase" maps to `status.phase`
- Only `eq`, `neq`, `in` operators are valid
- Only 5 values are valid
- "crashing" is not a valid phase value — must infer `restartCount > 0` instead

### 2.2 ResourceCapabilities as Strategy Guide

```json
{
  "canFind": true,
  "filterable": true,
  "namespaceScoped": true,
  "scaleHint": {
    "expectedCount": "many",
    "defaultPageSize": 100
  }
}
```

The agent sees `scaleHint: "many"` and knows NOT to list all pods — must filter first.

### 2.3 Resource Schema as Structure Guide

`GetResourceSchema()` returns the full JSON Schema of the resource. The agent can
understand the complete data structure, even for fields not declared as filterable.
This helps the agent:
- Interpret results (what fields exist in the response)
- Suggest filters the user might want
- Know the difference between `spec.nodeName` (where it's scheduled) vs.
  `status.hostIP` (the node's IP)

---

## 3. Translation Flow

```
┌──────────────────────────────────────────────────────────────────┐
│ User: "Show me crashing pods on worker-1 in production"          │
└────────────────────────┬─────────────────────────────────────────┘
                         │
                         ▼
┌──────────────────────────────────────────────────────────────────┐
│ AI Agent (Claude, GPT, etc.)                                      │
│                                                                    │
│ Step 1: Identify resource type                                     │
│   "pods" → kubernetes_core_v1_Pod                                  │
│                                                                    │
│ Step 2: Get capabilities and filter fields                         │
│   tools/call("kubernetes_list_resource_types")                     │
│   → Pods: {filterable: true, namespaceScoped: true,               │
│            scaleHint: "many"}                                      │
│                                                                    │
│ Step 3: Read filter fields from tool inputSchema                   │
│   kubernetes_core_v1_Pod_find.inputSchema.filters:                 │
│     fields: [metadata.name, metadata.namespace, metadata.labels,   │
│              status.phase, spec.nodeName,                          │
│              status.containerStatuses.restartCount]                 │
│                                                                    │
│ Step 4: Map NL concepts → filter predicates                        │
│   "crashing" → restartCount > 0  (no "crashing" enum value)       │
│   "worker-1" → spec.nodeName = "worker-1"                         │
│   "production" → namespace = "production"                          │
│                                                                    │
│ Step 5: Build structured query                                     │
│   tools/call("kubernetes_core_v1_Pod_find", {                     │
│     connectionId: "k8s-prod",                                      │
│     namespaces: ["production"],                                    │
│     filters: [...],                                                │
│     pageSize: 100                                                  │
│   })                                                               │
└──────────────────────────────────────────────────────────────────┘
```

---

## 4. NL Mapping Patterns

### 4.1 Direct Mappings

Many user concepts map directly to filter fields:

| User Says | Maps To |
|-----------|---------|
| "running pods" | `{field: "status.phase", op: "eq", value: "Running"}` |
| "pods on node X" | `{field: "spec.nodeName", op: "eq", value: "X"}` |
| "labeled app=nginx" | `{field: "metadata.labels", op: "haskey", value: "app"}` + eq |
| "created today" | `{field: "metadata.creationTimestamp", op: "gte", value: "<today>"}` |
| "deployments with 3+ replicas" | `{field: "spec.replicas", op: "gte", value: 3}` |

### 4.2 Semantic Mappings (Agent Inference)

Some user concepts don't map to a single filter field. The agent must infer:

| User Says | Agent Infers | Query |
|-----------|-------------|-------|
| "crashing pods" | Restarts > 0 | `{field: "restartCount", op: "gt", value: 0}` |
| "unhealthy pods" | Phase != Running + not Succeeded | `{field: "status.phase", op: "notin", value: ["Running", "Succeeded"]}` |
| "old deployments" | Created > 30 days ago | `{field: "metadata.creationTimestamp", op: "lt", value: "<30 days ago>"}` |
| "large pods" | Resource requests/limits above threshold | May not be filterable — agent should List + inspect |
| "pods related to nginx" | Text search | `{textQuery: "nginx"}` (if TextSearchProvider available) |

### 4.3 Ambiguous Mappings

When the user's intent is ambiguous, the agent should:

1. **Ask for clarification** if the ambiguity significantly affects results
2. **Use the broadest reasonable interpretation** if clarification would be annoying
3. **Explain what it searched for** in the response

Example:
- "Show me nginx" → could mean pods named nginx, pods with label app=nginx, or text search.
  Agent should use text search if available, or name prefix match, and explain the choice.

---

## 5. Agent Prompt Engineering Guidelines

### 5.1 System Prompt Context

When an AI agent is configured to use Omniview MCP tools, its system prompt should include:

```
You have access to Omniview, a Kubernetes/infrastructure management tool, through MCP tools.

When querying resources:
1. Use the _find tool (not _list) when the user wants filtered results
2. Check the tool's inputSchema for available filter fields, operators, and enum values
3. Use the scaleHint to decide query strategy:
   - "few": safe to list all
   - "moderate": prefer _find with filters
   - "many": ALWAYS use _find with specific filters, never list all
4. Namespace is separate from filters — use the namespaces parameter
5. For destructive actions (delete, drain), ALWAYS confirm with the user first
```

### 5.2 Tool Description Best Practices

The MCP tool descriptions (generated from SDK metadata) should:

| Element | Source | Example |
|---------|--------|---------|
| Tool description | `ResourceMeta.Description` | "Find Pods matching filters. Pods are sets of running containers." |
| Filter field description | `FilterField.Description` | "Pod lifecycle phase" |
| Enum values | `FilterField.AllowedValues` | "One of: Pending, Running, Succeeded, Failed, Unknown" |
| Scale warning | `ScaleHint` | "This resource type typically has 10K+ instances. Always use filters." |
| Danger warning | `ActionDescriptor.Dangerous` | "WARNING: This action is destructive. Confirm with user first." |

### 5.3 Multi-Step Query Patterns

For complex user requests, the agent may need multiple tool calls:

```
User: "Which nodes have the most crashing pods?"

Agent strategy:
1. Find crashing pods (restartCount > 0) across all namespaces
2. Group by spec.nodeName (client-side, in agent logic)
3. Get node details for top nodes
4. Present summary
```

The agent is doing the join/aggregation logic — not the SDK.

---

## 6. FilterField Description Guidelines

Plugin authors should write `FilterField.Description` with AI agents in mind:

### Do:
- Be specific about what the field represents
- Mention common user terms that map to this field
- Include value format for non-obvious types

### Don't:
- Use implementation jargon
- Assume the reader knows the backend

### Examples:

```go
// Good — AI agent can match "crashing" → restart count
FilterField{
    Path: "status.containerStatuses.restartCount",
    Description: "Number of times containers in this pod have restarted. " +
        "High restart counts indicate crashes or OOM kills.",
}

// Bad — too terse, AI can't infer meaning
FilterField{
    Path: "status.containerStatuses.restartCount",
    Description: "Restart count",
}

// Good — AI agent can match "node" or "host" or "machine"
FilterField{
    Path: "spec.nodeName",
    Description: "Name of the cluster node (host machine) this pod is scheduled on. " +
        "Empty if the pod hasn't been scheduled yet.",
}

// Good — AI agent knows the value format
FilterField{
    Path: "metadata.creationTimestamp",
    Description: "When this resource was created. RFC3339 format (e.g., '2024-01-15T10:30:00Z'). " +
        "Use gt/lt operators for time range queries.",
    Type: FilterFieldTime,
}
```

---

## 7. Handling Edge Cases

### 7.1 Resource Type Discovery

Before querying, the agent may not know what resource types exist. The flow:

```
User: "Show me what's running in production"

Agent:
1. tools/call("kubernetes_list_resource_types") → all resource types with capabilities
2. Identify workload types: Pod, Deployment, StatefulSet, DaemonSet, Job
3. Query each with appropriate filters
4. Aggregate and present
```

### 7.2 Connection Discovery

```
User: "What pods are crashing?"

Agent:
1. tools/call("kubernetes_list_connections") → available clusters
2. If one connection → use it
3. If multiple → ask user which cluster, or query all
```

### 7.3 No FilterableProvider

If a resource type doesn't implement `FilterableProvider`:
- The `_find` tool is NOT generated (or has no filter parameters)
- The agent should use `_list` with pagination and process results itself
- The agent explains: "This resource type doesn't support server-side filtering.
  I'll list all and filter the results."

### 7.4 Unsupported Filters

If the user asks for a filter that doesn't match any declared field:
- The agent explains which fields ARE available
- Suggests the closest match
- May fall back to listing + client-side search

---

## 8. What the SDK Does NOT Do

| Concern | Responsible Party |
|---------|------------------|
| Parse natural language | AI agent |
| Map synonyms (e.g., "crashing" → restartCount > 0) | AI agent |
| Resolve ambiguity | AI agent (ask user or make best guess) |
| Join/aggregate across resources | AI agent |
| Cache query results | Engine or frontend (React Query) |
| Suggest queries | AI agent, using FilterField.Description for context |
| Learn from past queries | AI agent |

The SDK's job is to provide rich, descriptive `FilterField` declarations and
`ResourceCapabilities` so the agent has everything it needs to make good decisions.

---

## 9. Relationship to Other Documents

| Doc | Relationship |
|-----|-------------|
| 13 | Filter type system — defines `FilterField`, `FilterPredicate`, `FilterExpression` that agents build |
| 14 | MCP server — exposes filter fields in tool `inputSchema` for agents to read |
| 15 | Query execution — how the predicates agents build get executed by Resourcers |
| 17 | Cross-resource queries — agents may query across resource types/plugins |
