# 18: Resource Relationship Graph — FR-8 Design

This document describes the resource relationship system — how resources reference each
other (Pod → Node, Pod → ReplicaSet → Deployment), how plugins declare relationships,
and how AI agents and the UI traverse the graph.

**Scope:** Design document only. Addresses FR-8 (resource relationship graph). Not implemented
in the SDK refactor — designed for a future sprint.

**Key principle:** Relationships are declared by plugins via an optional interface.
The engine builds and maintains the graph. AI agents and the UI query the graph
through engine APIs.

---

## 1. Current State

### 1.1 Existing ResourceLink Pattern

The K8s plugin already declares relationships via `ResourceLink` in column definitions:

```go
// plugin-sdk/pkg/resource/types/definition.go
type ResourceLink struct {
    IDAccessor        string            `json:"idAccessor"`
    NamespaceAccessor string            `json:"namespaceAccessor"`
    Namespaced        bool              `json:"namespaced"`
    Key               string            `json:"resourceKey"`      // static: "core::v1::Node"
    KeyAccessor       string            `json:"keyAccessor"`      // dynamic: extract kind from data
    KeyMap            map[string]string `json:"keyMap"`           // kind → resourceKey
    DetailExtractors  map[string]string `json:"detailExtractors"`
    DisplayID         bool              `json:"displayId"`
}
```

**Example — Pod column definitions** (`plugins/kubernetes/.../pod.go`):

```go
// "Controlled By" column → links to ReplicaSet, StatefulSet, DaemonSet, Job, CronJob
ResourceLink: &types.ResourceLink{
    IDAccessor:  "name",
    KeyAccessor: "kind",
    Namespaced:  true,
    KeyMap: map[string]string{
        "ReplicaSet":  "apps::v1::ReplicaSet",
        "StatefulSet": "apps::v1::StatefulSet",
        "DaemonSet":   "apps::v1::DaemonSet",
        "Job":         "batch::v1::Job",
        "CronJob":     "batch::v1::CronJob",
    },
}

// "Node" column → links to Node
ResourceLink: &types.ResourceLink{
    IDAccessor: ".",
    Namespaced: false,
    Key:        "core::v1::Node",
}
```

### 1.2 Limitations

| Limitation | Impact |
|-----------|--------|
| Tied to column definitions | Relationships only visible when rendering table columns |
| One-directional only | Pod → Node is declared, but Node → Pods is not |
| No relationship type | "controlled by" vs "runs on" vs "uses" — all treated the same |
| No graph traversal | Can't answer "what depends on this Node?" |
| No relationship metadata | Can't express "Pod X uses 500m CPU on Node Y" |
| Not queryable by AI | AI agent can't ask "what are this Pod's relationships?" |

---

## 2. Relationship Types

### 2.1 Taxonomy

| Type | Direction | Example | Semantics |
|------|-----------|---------|-----------|
| `owns` | Parent → Child | Deployment → ReplicaSet → Pod | Owner reference chain. Deleting parent cascades. |
| `runs_on` | Workload → Infrastructure | Pod → Node | Scheduling/placement relationship. |
| `uses` | Consumer → Resource | Pod → ConfigMap, Pod → Secret, Pod → PVC | Data/config dependency. |
| `exposes` | Service → Workload | Service → Pod (via selector) | Network access path. |
| `manages` | Controller → Target | HPA → Deployment | Control loop relationship. |
| `member_of` | Resource → Group | Node → NodePool, Pod → Namespace | Membership/containment. |

### 2.2 Relationship Definition

```go
type RelationshipType string

const (
    RelOwns     RelationshipType = "owns"      // parent-child ownership
    RelRunsOn   RelationshipType = "runs_on"   // workload → infrastructure
    RelUses     RelationshipType = "uses"       // consumer → dependency
    RelExposes  RelationshipType = "exposes"    // service → backend
    RelManages  RelationshipType = "manages"    // controller → target
    RelMemberOf RelationshipType = "member_of"  // resource → group
)

// RelationshipDescriptor declares a relationship from one resource type to another.
type RelationshipDescriptor struct {
    // Type categorizes the relationship.
    Type RelationshipType `json:"type"`

    // TargetResourceKey is the resource type of the related resource.
    // Uses the standard "group::version::kind" format.
    TargetResourceKey string `json:"targetResourceKey"`

    // Label is a human-readable description of the relationship.
    // E.g., "runs on", "controlled by", "uses secret"
    Label string `json:"label"`

    // InverseLabel is the label for the reverse direction.
    // E.g., if Label is "runs on", InverseLabel is "runs pods"
    InverseLabel string `json:"inverseLabel"`

    // Cardinality: "one" (Pod → Node) or "many" (Node → Pods)
    Cardinality string `json:"cardinality"` // "one" or "many"

    // Extractor describes how to find the related resource ID from the source data.
    Extractor RelationshipExtractor `json:"extractor"`
}

// RelationshipExtractor defines how to extract relationship targets from resource data.
type RelationshipExtractor struct {
    // Method: how to find the target
    //   "field"         → extract target ID from a field path
    //   "ownerRef"      → K8s ownerReferences array
    //   "labelSelector" → match by label selector
    //   "fieldSelector" → match by field value (e.g., spec.nodeName)
    //   "custom"        → plugin computes relationships via callback
    Method string `json:"method"`

    // FieldPath: for "field" and "fieldSelector" methods.
    // Dot-path to the field containing the target resource ID.
    // E.g., "spec.nodeName", "spec.volumes[*].configMap.name"
    FieldPath string `json:"fieldPath,omitempty"`

    // OwnerRefKind: for "ownerRef" method.
    // Filter ownerReferences by this kind. Empty = all owner refs.
    OwnerRefKind string `json:"ownerRefKind,omitempty"`

    // LabelSelector: for "labelSelector" method.
    // The labels to match on the target resource.
    // Key = label key in source, Value = label key in target (usually same)
    LabelSelector map[string]string `json:"labelSelector,omitempty"`
}
```

---

## 3. Optional Interface: RelationshipProvider

### 3.1 Interface

```go
// RelationshipProvider declares relationships for a resource type.
// Optional — type-asserted on Resourcer implementations.
// If not implemented, no relationships are declared (existing ResourceLink
// column defs still work for UI table rendering, but not for graph queries).
type RelationshipProvider interface {
    // DeclareRelationships returns the relationship descriptors for this resource type.
    // Called once at registration time, cached by the SDK.
    DeclareRelationships(ctx context.Context) ([]RelationshipDescriptor, error)
}

// RelationshipResolver resolves actual relationship instances for a specific resource.
// Optional — more expensive than declaration, called on demand.
type RelationshipResolver[ClientT any] interface {
    // ResolveRelationships returns the actual related resource IDs for a given resource.
    // Called when an AI agent or UI requests "what is related to this Pod?"
    ResolveRelationships(ctx context.Context, client *ClientT, meta ResourceMeta,
        resourceID string, namespace string) ([]ResolvedRelationship, error)
}

type ResolvedRelationship struct {
    Descriptor   RelationshipDescriptor `json:"descriptor"`
    TargetIDs    []ResourceRef          `json:"targetIds"`
}

type ResourceRef struct {
    PluginID     string `json:"pluginId,omitempty"`     // empty = same plugin
    ConnectionID string `json:"connectionId,omitempty"` // empty = same connection
    ResourceKey  string `json:"resourceKey"`
    ID           string `json:"id"`
    Namespace    string `json:"namespace,omitempty"`
    DisplayName  string `json:"displayName,omitempty"`  // for UI
}
```

### 3.2 K8s Plugin Example

```go
func (r *PodResourcer) DeclareRelationships(ctx context.Context) ([]RelationshipDescriptor, error) {
    return []RelationshipDescriptor{
        {
            Type:              RelOwns,
            TargetResourceKey: "", // dynamic — determined by ownerRef kind
            Label:             "controlled by",
            InverseLabel:      "controls pods",
            Cardinality:       "one",
            Extractor: RelationshipExtractor{
                Method:       "ownerRef",
                // ownerRefKind empty = all owner refs
            },
        },
        {
            Type:              RelRunsOn,
            TargetResourceKey: "core::v1::Node",
            Label:             "runs on",
            InverseLabel:      "runs pods",
            Cardinality:       "one",
            Extractor: RelationshipExtractor{
                Method:    "fieldSelector",
                FieldPath: "spec.nodeName",
            },
        },
        {
            Type:              RelUses,
            TargetResourceKey: "core::v1::ConfigMap",
            Label:             "uses config map",
            InverseLabel:      "used by pods",
            Cardinality:       "many",
            Extractor: RelationshipExtractor{
                Method:    "field",
                FieldPath: "spec.volumes[*].configMap.name",
            },
        },
        {
            Type:              RelUses,
            TargetResourceKey: "core::v1::Secret",
            Label:             "uses secret",
            InverseLabel:      "used by pods",
            Cardinality:       "many",
            Extractor: RelationshipExtractor{
                Method:    "field",
                FieldPath: "spec.volumes[*].secret.secretName",
            },
        },
        {
            Type:              RelUses,
            TargetResourceKey: "core::v1::PersistentVolumeClaim",
            Label:             "uses PVC",
            InverseLabel:      "used by pods",
            Cardinality:       "many",
            Extractor: RelationshipExtractor{
                Method:    "field",
                FieldPath: "spec.volumes[*].persistentVolumeClaim.claimName",
            },
        },
        {
            Type:              RelUses,
            TargetResourceKey: "core::v1::ServiceAccount",
            Label:             "uses service account",
            InverseLabel:      "used by pods",
            Cardinality:       "one",
            Extractor: RelationshipExtractor{
                Method:    "field",
                FieldPath: "spec.serviceAccountName",
            },
        },
    }, nil
}

func (r *ServiceResourcer) DeclareRelationships(ctx context.Context) ([]RelationshipDescriptor, error) {
    return []RelationshipDescriptor{
        {
            Type:              RelExposes,
            TargetResourceKey: "core::v1::Pod",
            Label:             "selects pods",
            InverseLabel:      "exposed by service",
            Cardinality:       "many",
            Extractor: RelationshipExtractor{
                Method:        "labelSelector",
                LabelSelector: nil, // dynamic — uses spec.selector from the Service object
            },
        },
    }, nil
}
```

---

## 4. Engine-Side Relationship Graph

### 4.1 Graph Structure

The engine builds a bidirectional relationship graph from declarations:

```go
type RelationshipGraph struct {
    // Forward edges: source → targets
    // Key: "{pluginID}/{resourceKey}/{resourceID}"
    forward map[string][]RelationshipEdge

    // Reverse edges: target → sources
    // Automatically maintained from forward declarations
    reverse map[string][]RelationshipEdge

    // Type declarations: resourceKey → relationship descriptors
    declarations map[string][]RelationshipDescriptor
}

type RelationshipEdge struct {
    SourceRef    ResourceRef           `json:"sourceRef"`
    TargetRef    ResourceRef           `json:"targetRef"`
    Descriptor   RelationshipDescriptor `json:"descriptor"`
}
```

### 4.2 Graph Population

The graph is populated from watch events:

```
Resource ADD event:
  1. Get resource data (json.RawMessage)
  2. For each RelationshipDescriptor on this resource type:
     a. Apply Extractor to resource data → target resource IDs
     b. Create forward edges: source → targets
     c. Create reverse edges: targets → source
  3. Update graph

Resource UPDATE event:
  1. Remove old edges for this resource
  2. Re-extract and create new edges (relationships may change —
     e.g., Pod rescheduled to different Node)

Resource DELETE event:
  1. Remove all edges for this resource (forward and reverse)
```

### 4.3 Graph Queries

```go
type RelationshipGraphQuerier interface {
    // GetRelated returns all resources related to the given resource.
    // direction: "outgoing" (this → targets), "incoming" (sources → this), "both"
    GetRelated(ctx context.Context, ref ResourceRef, direction string,
        relationshipType *RelationshipType) ([]RelationshipEdge, error)

    // GetRelationshipChain follows relationship chains.
    // E.g., Pod → ReplicaSet → Deployment (depth=2, type=owns)
    GetRelationshipChain(ctx context.Context, ref ResourceRef,
        relationshipType RelationshipType, maxDepth int) ([][]RelationshipEdge, error)

    // GetDependencyTree returns the full dependency tree for a resource.
    // Includes all relationship types, useful for impact analysis.
    GetDependencyTree(ctx context.Context, ref ResourceRef,
        maxDepth int) (*DependencyTree, error)
}

type DependencyTree struct {
    Root     ResourceRef         `json:"root"`
    Children []DependencyNode    `json:"children"`
}

type DependencyNode struct {
    Edge     RelationshipEdge    `json:"edge"`
    Children []DependencyNode    `json:"children,omitempty"`
}
```

---

## 5. AI Agent Use Cases

### 5.1 Impact Analysis

```
User: "What would be affected if I drain node worker-1?"

Agent:
1. Get all pods on worker-1:
   find("core::v1::Pod", {filters: [{field: "spec.nodeName", op: "eq", value: "worker-1"}]})

2. For each pod, get ownership chain:
   getRelationshipChain(pod, "owns", depth=3)
   → Pod → ReplicaSet → Deployment

3. Get services exposing these pods:
   getRelated(pod, "incoming", "exposes")
   → Services that select these pods

4. Report:
   "Draining worker-1 would affect:
    - 15 pods across 8 deployments
    - 3 services would lose backends
    - 2 stateful workloads (data migration needed)"
```

### 5.2 Root Cause Analysis

```
User: "Why is service frontend-svc returning 503?"

Agent:
1. Get service details:
   get("core::v1::Service", "frontend-svc")

2. Get pods selected by service:
   getRelated(service, "outgoing", "exposes")
   → 3 pods

3. Check pod status:
   For each pod: get("core::v1::Pod", podID)
   → 2 pods in CrashLoopBackOff

4. Get ownership:
   getRelationshipChain(crashingPod, "owns")
   → Pod → ReplicaSet → Deployment

5. Report:
   "frontend-svc is returning 503 because 2 of 3 backend pods are crashing.
    The pods are managed by deployment frontend-deploy.
    Restart count: 47. Last restart: 2 minutes ago."
```

### 5.3 Resource Discovery

```
User: "What does this deployment depend on?"

Agent:
1. getDependencyTree(deployment, maxDepth=3)
   → Deployment
      ├── ReplicaSet (owns)
      │   └── Pod (owns)
      │       ├── Node (runs_on)
      │       ├── ConfigMap: app-config (uses)
      │       ├── Secret: db-credentials (uses)
      │       ├── PVC: data-volume (uses)
      │       └── ServiceAccount: app-sa (uses)
      └── HPA (managed by, incoming)

2. Report the dependency tree visually
```

---

## 6. MCP Tool Integration

### 6.1 Relationship MCP Tools

The MCP server (doc 14) generates relationship-specific tools:

```json
{
    "name": "omniview_get_related_resources",
    "description": "Get resources related to a given resource. Returns relationship graph edges.",
    "inputSchema": {
        "type": "object",
        "properties": {
            "pluginId": {"type": "string"},
            "connectionId": {"type": "string"},
            "resourceKey": {"type": "string"},
            "resourceId": {"type": "string"},
            "namespace": {"type": "string"},
            "direction": {"type": "string", "enum": ["outgoing", "incoming", "both"]},
            "relationshipType": {"type": "string", "enum": ["owns", "runs_on", "uses", "exposes", "manages", "member_of"]}
        },
        "required": ["pluginId", "connectionId", "resourceKey", "resourceId"]
    }
}
```

```json
{
    "name": "omniview_get_dependency_tree",
    "description": "Get the full dependency tree for a resource, showing all related resources at each level.",
    "inputSchema": {
        "type": "object",
        "properties": {
            "pluginId": {"type": "string"},
            "connectionId": {"type": "string"},
            "resourceKey": {"type": "string"},
            "resourceId": {"type": "string"},
            "namespace": {"type": "string"},
            "maxDepth": {"type": "integer", "default": 3, "maximum": 5}
        },
        "required": ["pluginId", "connectionId", "resourceKey", "resourceId"]
    }
}
```

### 6.2 ResourceCapabilities Addition

Add to `ResourceCapabilities` (doc 13):

```go
type ResourceCapabilities struct {
    // ... existing fields ...
    HasRelationships bool `json:"hasRelationships"` // implements RelationshipProvider
}
```

---

## 7. Relationship to Existing ResourceLink

### 7.1 Migration Path

`ResourceLink` (existing) → `RelationshipDescriptor` (new):

| ResourceLink Field | RelationshipDescriptor Equivalent |
|-------------------|----------------------------------|
| `Key` (static resource key) | `TargetResourceKey` |
| `KeyAccessor` + `KeyMap` | `Extractor{Method: "ownerRef"}` |
| `IDAccessor` | `Extractor{FieldPath: ...}` |
| `NamespaceAccessor` | Part of `Extractor` |
| `DetailExtractors` | Dropped — UI concern, not relationship |
| N/A | `Type` (new — relationship categorization) |
| N/A | `Label` / `InverseLabel` (new — human-readable) |
| N/A | Bidirectional traversal (new — automatic inverse) |

### 7.2 Coexistence Strategy

`ResourceLink` in column definitions continues to work for UI table rendering.
`RelationshipProvider` is a superset used for graph queries, AI agents, and MCP tools.

Plugin authors can implement `RelationshipProvider` alongside existing `ResourceLink`
columns. The engine does NOT auto-derive `RelationshipDescriptor` from `ResourceLink` —
they're separate (different semantics, different consumers).

---

## 8. Frontend UI Integration

### 8.1 Existing AI UI Components

The frontend already has relationship-aware AI components:

```typescript
// packages/omniviewdev-ui/src/ai/context/AIRelatedResources.tsx
export interface AIRelatedResourcesProps {
    primary: { kind: string; name: string };
    related: Array<{
        kind: string;
        name: string;
        relationship: string;  // matches RelationshipDescriptor.Label
    }>;
}
```

These components can be populated from `RelationshipGraph` queries.

### 8.2 Resource Detail View

When viewing a resource, the UI can show a relationship panel:

```
Pod: nginx-abc123
├── Controlled by: ReplicaSet nginx-abc123-xyz
│   └── Owned by: Deployment nginx
├── Runs on: Node worker-1
├── Uses:
│   ├── ConfigMap: nginx-config
│   ├── Secret: tls-cert
│   └── ServiceAccount: nginx-sa
└── Exposed by: Service nginx-svc
```

### 8.3 Graph Visualization

A future graph view renders relationships visually:
- Nodes = resources
- Edges = relationships (colored by type)
- Interactive: click a node to navigate to its detail view
- Filterable: show only specific relationship types

---

## 9. Performance Considerations

### 9.1 Graph Size

For a moderate K8s cluster (1000 pods, 200 deployments, 100 services):

| Metric | Estimate |
|--------|----------|
| Nodes in graph | ~3,000 resources |
| Edges in graph | ~5,000 relationships |
| Memory | ~10MB (edge + index structures) |
| Update cost per event | O(R) where R = relationships per resource (typically 3-6) |

For large clusters (10K+ pods), the graph remains manageable — edges are proportional
to resources, not quadratic.

### 9.2 Lazy Resolution

`ResolveRelationships` (the expensive callback) is only called on demand, not during
watch event processing. The graph is built from `DeclareRelationships` (static declarations)
+ field extraction (cheap JSON path lookups).

### 9.3 Caching

Relationship declarations are cached per resource type (static).
Graph edges are updated incrementally via watch events (ADD/UPDATE/DELETE).
No periodic full rebuild needed.

---

## 10. Implementation Phases

| Phase | Scope | Dependencies |
|-------|-------|-------------|
| 1 | `RelationshipProvider` interface in SDK | SDK refactor |
| 2 | K8s plugin implements RelationshipProvider for core types | Phase 1 |
| 3 | Engine `RelationshipGraph` — build from declarations + watch events | Phase 2 |
| 4 | Engine graph query APIs (GetRelated, GetDependencyTree) | Phase 3 |
| 5 | MCP tools for relationship queries | Phase 4, doc 14 |
| 6 | Frontend relationship panel in resource detail view | Phase 4 |
| 7 | Graph visualization view | Phase 6 |

---

## 11. What This Document Does NOT Cover

| Topic | Where |
|-------|-------|
| SDK interface design | Doc 09 |
| Filter/query system | Doc 13 |
| MCP server runtime | Doc 14 |
| Cross-resource query routing | Doc 17 |
| Graph visualization implementation | Future UI doc |
| Cross-plugin relationships | Future — e.g., K8s Pod → AWS EBS volume |
