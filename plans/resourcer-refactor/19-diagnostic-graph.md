# 19: Diagnostic Graph — Health-Annotated Relationship Traversal

This document designs the health-annotated diagnostic layer that builds on top of the
relationship graph (doc 18). While doc 18 defines the topology — what's connected to what —
this document adds the "is it healthy?" dimension that transforms the graph from a map
into a diagnostic navigation system.

**Prerequisite:** Doc 18 (resource relationship graph) must be implemented first. This
document is a separate enhancement sprint that layers health, events, and diagnostic
traversal patterns on top of the existing topology graph.

**The core insight:** Without health annotations, an AI agent investigating "why is my
app down?" must fetch every related resource and manually parse its status. With health
annotations, the agent can say "follow the unhealthy edges" and arrive at the root cause
in 1-2 tool calls instead of 10+. The graph stops being a topology map and becomes a
**diagnostic navigation system** — it tells the agent where to look next and, critically,
where NOT to look.

**Scope:** Design document only. Future enhancement sprint.

---

## 1. Why Health on the Graph Changes Everything

### 1.1 Without Health (Doc 18 — Topology Only)

```
User: "Why is frontend-svc returning 503?"

Agent with topology graph only:
  1. get("Service", "frontend-svc")                              → 1 call
  2. getRelated(service, "outgoing", "exposes") → 3 pods         → 1 call
  3. get("Pod", "frontend-abc")                                  → 1 call
  4. get("Pod", "frontend-def")                                  → 1 call
  5. get("Pod", "frontend-ghi")                                  → 1 call
  6. "Hmm, pod-def is CrashLoopBackOff. Why?"
  7. getRelated(pod-def, "outgoing")                             → 1 call
     → Node worker-2, ConfigMap app-config, Secret db-creds,
       ServiceAccount app-sa, PVC data-vol
  8. get("Node", "worker-2")                                     → 1 call
  9. get("ConfigMap", "app-config")                               → 1 call
  10. get("Secret", "db-creds")                                   → 1 call
  11. get("ServiceAccount", "app-sa")                             → 1 call
  12. get("PVC", "data-vol")                                      → 1 call
  13. "Node is fine, ConfigMap is fine... oh, Secret db-creds
      was updated 5 minutes ago"
  ─────────────────────────────────────────────────
  Total: 12 tool calls, agent inspected 5 healthy resources for nothing
```

The agent wastes time on tangents — checking healthy Nodes, healthy ConfigMaps — because
it has no way to know which edges lead toward the problem.

### 1.2 With Health (This Document)

```
User: "Why is frontend-svc returning 503?"

Agent with diagnostic graph:
  1. diagnose("Service", "frontend-svc")                         → 1 call
     → Service: healthy (has endpoints)
       Events: none
       Related (with health):
         Pod frontend-abc: healthy
         Pod frontend-def: UNHEALTHY (CrashLoopBackOff)
         Pod frontend-ghi: healthy

  2. diagnose("Pod", "frontend-def")                             → 1 call
     → Pod: unhealthy (CrashLoopBackOff, 47 restarts)
       Events: [
         Warning BackOff "Back-off restarting failed container"
         Warning Failed  "Error: secret 'db-creds' key 'password' not found"
       ]
       Related (with health):
         Node worker-2: healthy
         ConfigMap app-config: healthy
         Secret db-creds: DEGRADED (key 'password' missing since 5m ago)

  3. Agent has the answer: "frontend-svc is 503 because Pod frontend-def is
     crash-looping. The pod can't start because Secret db-creds is missing
     the 'password' key — it was likely modified ~5 minutes ago."
  ─────────────────────────────────────────────────
  Total: 2 tool calls, zero tangents, root cause identified
```

The difference: **the health annotations on each related resource tell the agent exactly
which edges to follow.** It skips the 3 healthy resources and goes straight to the
unhealthy Secret.

---

## 2. New Interfaces

### 2.1 HealthProvider

```go
type HealthStatus string

const (
    HealthHealthy   HealthStatus = "healthy"    // fully operational
    HealthDegraded  HealthStatus = "degraded"   // partially working, needs attention
    HealthUnhealthy HealthStatus = "unhealthy"  // failing, broken
    HealthPending   HealthStatus = "pending"    // not yet ready (starting up, provisioning)
    HealthUnknown   HealthStatus = "unknown"    // can't determine health
)

// ResourceHealth is the health assessment for a single resource instance.
type ResourceHealth struct {
    // Status is the overall health classification.
    Status HealthStatus `json:"status"`

    // Reason is a machine-readable short code.
    // For K8s: "CrashLoopBackOff", "ImagePullBackOff", "DiskPressure", "Ready"
    // For AWS: "running", "stopped", "terminated"
    Reason string `json:"reason,omitempty"`

    // Message is a human-readable explanation.
    // For AI context — the agent reads this to understand the problem.
    // E.g., "Container app restarted 47 times, last exit code 1"
    Message string `json:"message,omitempty"`

    // Since is when this health status began.
    // Helps the agent correlate with timeline ("unhealthy since 5 minutes ago").
    Since *time.Time `json:"since,omitempty"`

    // Conditions are the individual health signals that contributed to the status.
    // For K8s: Pod conditions (Ready, ContainersReady, PodScheduled, Initialized)
    // For Nodes: (Ready, DiskPressure, MemoryPressure, PIDPressure, NetworkUnavailable)
    Conditions []HealthCondition `json:"conditions,omitempty"`
}

type HealthCondition struct {
    Type    string       `json:"type"`              // "Ready", "DiskPressure"
    Status  string       `json:"status"`            // "True", "False", "Unknown"
    Reason  string       `json:"reason,omitempty"`  // "KubeletReady", "KubeletNotReady"
    Message string       `json:"message,omitempty"` // human-readable detail
    Since   *time.Time   `json:"since,omitempty"`   // last transition time
}

// HealthProvider extracts a normalized health assessment from resource data.
// Optional — type-asserted on Resourcer implementations.
//
// The SDK calls this on resource data (json.RawMessage) to derive health
// without knowing the resource type's specific status schema.
//
// If NOT implemented: health is HealthUnknown for this resource type.
// Graph traversal still works, but the agent can't filter by health.
//
// If implemented: the engine caches health per resource instance and
// updates it on every watch UPDATE event. Health is available for:
//   - Graph annotations (GetRelatedWithHealth)
//   - Diagnostic tools (diagnose, traceUnhealthy)
//   - UI health indicators
//   - MCP tool responses
type HealthProvider interface {
    // GetHealth derives health from resource data.
    // data is the raw JSON resource object (e.g., a K8s Pod's full JSON).
    // The implementation inspects status fields and returns a normalized assessment.
    GetHealth(ctx context.Context, data json.RawMessage) (*ResourceHealth, error)
}
```

### 2.2 K8s HealthProvider Examples

```go
// Pod health — considers phase, conditions, container statuses
func (r *PodResourcer) GetHealth(ctx context.Context, data json.RawMessage) (*ResourceHealth, error) {
    phase := gjson.GetBytes(data, "status.phase").String()

    switch phase {
    case "Succeeded":
        return &ResourceHealth{Status: HealthHealthy, Reason: "Completed"}, nil
    case "Failed":
        msg := gjson.GetBytes(data, "status.message").String()
        return &ResourceHealth{Status: HealthUnhealthy, Reason: "Failed", Message: msg}, nil
    case "Pending":
        // Check if scheduling failed
        conditions := gjson.GetBytes(data, "status.conditions")
        for _, c := range conditions.Array() {
            if c.Get("type").String() == "PodScheduled" && c.Get("status").String() == "False" {
                return &ResourceHealth{
                    Status:  HealthUnhealthy,
                    Reason:  c.Get("reason").String(), // "Unschedulable"
                    Message: c.Get("message").String(),
                }, nil
            }
        }
        return &ResourceHealth{Status: HealthPending, Reason: "Pending"}, nil
    case "Running":
        // Check container statuses for crashes
        containers := gjson.GetBytes(data, "status.containerStatuses")
        for _, c := range containers.Array() {
            if c.Get("state.waiting").Exists() {
                reason := c.Get("state.waiting.reason").String()
                if reason == "CrashLoopBackOff" || reason == "ImagePullBackOff" ||
                   reason == "ErrImagePull" || reason == "CreateContainerError" {
                    return &ResourceHealth{
                        Status:  HealthUnhealthy,
                        Reason:  reason,
                        Message: c.Get("state.waiting.message").String(),
                    }, nil
                }
            }
            if !c.Get("ready").Bool() {
                return &ResourceHealth{
                    Status: HealthDegraded,
                    Reason: "ContainerNotReady",
                    Message: fmt.Sprintf("Container %s not ready", c.Get("name").String()),
                }, nil
            }
            restarts := c.Get("restartCount").Int()
            if restarts > 10 {
                return &ResourceHealth{
                    Status:  HealthDegraded,
                    Reason:  "HighRestartCount",
                    Message: fmt.Sprintf("Container %s has restarted %d times",
                        c.Get("name").String(), restarts),
                }, nil
            }
        }
        return &ResourceHealth{Status: HealthHealthy, Reason: "Running"}, nil
    default:
        return &ResourceHealth{Status: HealthUnknown}, nil
    }
}

// Node health — considers conditions
func (r *NodeResourcer) GetHealth(ctx context.Context, data json.RawMessage) (*ResourceHealth, error) {
    conditions := gjson.GetBytes(data, "status.conditions")

    health := &ResourceHealth{Status: HealthHealthy, Reason: "Ready"}
    var problems []HealthCondition

    for _, c := range conditions.Array() {
        condType := c.Get("type").String()
        condStatus := c.Get("status").String()

        hc := HealthCondition{
            Type:    condType,
            Status:  condStatus,
            Reason:  c.Get("reason").String(),
            Message: c.Get("message").String(),
        }

        switch condType {
        case "Ready":
            if condStatus != "True" {
                health.Status = HealthUnhealthy
                health.Reason = "NotReady"
                health.Message = c.Get("message").String()
            }
        case "DiskPressure", "MemoryPressure", "PIDPressure":
            if condStatus == "True" {
                health.Status = HealthDegraded
                health.Reason = condType
                health.Message = c.Get("message").String()
            }
        case "NetworkUnavailable":
            if condStatus == "True" {
                health.Status = HealthUnhealthy
                health.Reason = "NetworkUnavailable"
                health.Message = c.Get("message").String()
            }
        }

        problems = append(problems, hc)
    }

    health.Conditions = problems
    return health, nil
}

// Deployment health — considers replicas and conditions
func (r *DeploymentResourcer) GetHealth(ctx context.Context, data json.RawMessage) (*ResourceHealth, error) {
    desired := gjson.GetBytes(data, "spec.replicas").Int()
    available := gjson.GetBytes(data, "status.availableReplicas").Int()
    ready := gjson.GetBytes(data, "status.readyReplicas").Int()

    if desired == 0 {
        return &ResourceHealth{Status: HealthHealthy, Reason: "ScaledToZero"}, nil
    }
    if ready == desired {
        return &ResourceHealth{Status: HealthHealthy, Reason: "AllReplicasReady",
            Message: fmt.Sprintf("%d/%d replicas ready", ready, desired)}, nil
    }
    if available > 0 {
        return &ResourceHealth{Status: HealthDegraded, Reason: "PartiallyAvailable",
            Message: fmt.Sprintf("%d/%d replicas ready, %d available", ready, desired, available)}, nil
    }
    return &ResourceHealth{Status: HealthUnhealthy, Reason: "NoAvailableReplicas",
        Message: fmt.Sprintf("0/%d replicas available", desired)}, nil
}

// Service health — considers endpoints
func (r *ServiceResourcer) GetHealth(ctx context.Context, data json.RawMessage) (*ResourceHealth, error) {
    svcType := gjson.GetBytes(data, "spec.type").String()
    clusterIP := gjson.GetBytes(data, "spec.clusterIP").String()

    if svcType == "ExternalName" {
        return &ResourceHealth{Status: HealthHealthy, Reason: "ExternalName"}, nil
    }
    if clusterIP == "" || clusterIP == "None" {
        return &ResourceHealth{Status: HealthHealthy, Reason: "Headless"}, nil
    }
    // Note: actual endpoint health requires checking Endpoints resource
    // The RelationshipResolver or a separate check would handle this
    return &ResourceHealth{Status: HealthHealthy, Reason: "Active"}, nil
}

// PVC health
func (r *PVCResourcer) GetHealth(ctx context.Context, data json.RawMessage) (*ResourceHealth, error) {
    phase := gjson.GetBytes(data, "status.phase").String()
    switch phase {
    case "Bound":
        return &ResourceHealth{Status: HealthHealthy, Reason: "Bound"}, nil
    case "Pending":
        return &ResourceHealth{Status: HealthPending, Reason: "Pending",
            Message: "Waiting for volume to be provisioned"}, nil
    case "Lost":
        return &ResourceHealth{Status: HealthUnhealthy, Reason: "Lost",
            Message: "Underlying PersistentVolume has been deleted"}, nil
    default:
        return &ResourceHealth{Status: HealthUnknown}, nil
    }
}
```

### 2.3 EventProvider

```go
// ResourceEvent is a normalized diagnostic event for a resource instance.
// For K8s: maps to core/v1 Event. For other backends: whatever diagnostic
// events the backend produces (CloudTrail, audit logs, etc.).
type ResourceEvent struct {
    // Type classifies the event severity.
    // K8s uses "Normal" and "Warning". This normalizes to a broader set.
    Type EventSeverity `json:"type"`

    // Reason is a machine-readable event code.
    // K8s: "Pulled", "Created", "Started", "Killing", "FailedScheduling",
    //       "FailedMount", "OOMKilled", "BackOff", "Unhealthy"
    Reason string `json:"reason"`

    // Message is the human-readable detail.
    // K8s: "Successfully pulled image 'nginx:latest'"
    //      "Back-off restarting failed container"
    //      "Error: secret 'db-creds' not found"
    Message string `json:"message"`

    // Source identifies what generated the event.
    // K8s: "kubelet", "default-scheduler", "deployment-controller"
    Source string `json:"source,omitempty"`

    // Count is how many times this event has occurred.
    Count int `json:"count"`

    // FirstSeen is when this event first occurred.
    FirstSeen time.Time `json:"firstSeen"`

    // LastSeen is when this event most recently occurred.
    LastSeen time.Time `json:"lastSeen"`
}

type EventSeverity string

const (
    EventNormal  EventSeverity = "normal"   // informational
    EventWarning EventSeverity = "warning"  // problem detected
    EventError   EventSeverity = "error"    // critical failure
)

// EventProvider retrieves diagnostic events for a resource instance.
// Optional — type-asserted on Resourcer implementations.
//
// Events are ephemeral diagnostic signals, NOT relationships. A Pod doesn't
// "own" or "use" its Events — Events are observations ABOUT the Pod.
// This is why EventProvider is a separate interface, not a RelationshipType.
//
// For K8s: queries core/v1/Event with fieldSelector involvedObject.name=<id>
// For AWS: queries CloudTrail or CloudWatch for resource-related events
// For SQL: might query an audit log table
//
// If NOT implemented: diagnose() returns empty events list.
// Graph traversal still works; the agent just doesn't get event context.
type EventProvider[ClientT any] interface {
    // GetEvents retrieves recent diagnostic events for a resource instance.
    // limit controls how many events to return (most recent first).
    // 0 = reasonable default (e.g., 20).
    GetEvents(ctx context.Context, client *ClientT, meta ResourceMeta,
        resourceID string, namespace string, limit int) ([]ResourceEvent, error)
}
```

### 2.4 K8s EventProvider Example

```go
func (r *PodResourcer) GetEvents(ctx context.Context, client *kubernetes.Clientset,
    meta ResourceMeta, resourceID string, namespace string, limit int) ([]ResourceEvent, error) {

    if limit == 0 {
        limit = 20
    }

    events, err := client.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
        FieldSelector: fmt.Sprintf("involvedObject.name=%s,involvedObject.kind=Pod", resourceID),
        Limit:         int64(limit),
    })
    if err != nil {
        return nil, err
    }

    result := make([]ResourceEvent, 0, len(events.Items))
    for _, e := range events.Items {
        severity := EventNormal
        if e.Type == "Warning" {
            severity = EventWarning
        }

        result = append(result, ResourceEvent{
            Type:      severity,
            Reason:    e.Reason,
            Message:   e.Message,
            Source:    e.Source.Component,
            Count:     int(e.Count),
            FirstSeen: e.FirstTimestamp.Time,
            LastSeen:  e.LastTimestamp.Time,
        })
    }

    // Sort: warnings first, then by LastSeen descending
    sort.Slice(result, func(i, j int) bool {
        if result[i].Type != result[j].Type {
            return result[i].Type == EventWarning
        }
        return result[i].LastSeen.After(result[j].LastSeen)
    })

    return result, nil
}
```

---

## 3. Health-Annotated Graph Queries

These build on doc 18's `RelationshipGraphQuerier` — same graph, new query methods
that include health and events.

### 3.1 New Types

```go
// AnnotatedNode is a resource in the graph with health and event context.
type AnnotatedNode struct {
    Ref      ResourceRef     `json:"ref"`
    Health   *ResourceHealth `json:"health,omitempty"`   // nil if HealthProvider not implemented
    Events   []ResourceEvent `json:"events,omitempty"`   // nil if EventProvider not implemented, or if not requested
}

// AnnotatedEdge is a relationship edge with health on both endpoints.
type AnnotatedEdge struct {
    Edge     RelationshipEdge `json:"edge"`       // from doc 18
    Target   AnnotatedNode    `json:"target"`     // target with health
}

// DiagnosticContext is the complete debugging context for a single resource.
// One tool call returns everything an AI agent needs to start investigating.
type DiagnosticContext struct {
    // Resource is the full resource data.
    Resource json.RawMessage `json:"resource"`

    // Health is the normalized health assessment.
    Health *ResourceHealth `json:"health"`

    // Events are recent diagnostic events for this resource.
    Events []ResourceEvent `json:"events"`

    // Related is every immediate relationship with health annotations.
    // The agent can scan this to see which related resources are unhealthy.
    Related []AnnotatedEdge `json:"related"`

    // OwnerChain is the ownership hierarchy to the root controller.
    // Pod → ReplicaSet → Deployment, each with health.
    OwnerChain []AnnotatedNode `json:"ownerChain"`
}

// DiagnosticTree is a health-annotated dependency tree for visual rendering.
type DiagnosticTree struct {
    Root     AnnotatedNode      `json:"root"`
    Children []DiagnosticBranch `json:"children"`
}

type DiagnosticBranch struct {
    Edge     RelationshipEdge   `json:"edge"`
    Node     AnnotatedNode      `json:"node"`
    Children []DiagnosticBranch `json:"children,omitempty"`
}

// ImpactReport describes what would be affected by a resource's failure/removal.
type ImpactReport struct {
    // Target is the resource being analyzed.
    Target ResourceRef `json:"target"`

    // DirectDependents are resources that directly depend on this one.
    // Grouped by resource type for readability.
    DirectDependents map[string][]AnnotatedNode `json:"directDependents"`
    // key: resourceKey ("core::v1::Pod"), value: dependent resources with health

    // TransitiveDependents are resources that indirectly depend on this one.
    // E.g., for a Node: Pods (direct) → Services that expose those Pods (transitive)
    TransitiveDependents map[string][]AnnotatedNode `json:"transitiveDependents"`

    // Summary is a human-readable impact summary for AI agents.
    // E.g., "Removing Node worker-1 would affect 15 pods across 8 deployments.
    //        3 services would lose backends. 2 StatefulSets need data migration."
    Summary string `json:"summary"`

    // AffectedCounts is a quick numeric summary.
    AffectedCounts map[string]int `json:"affectedCounts"`
    // key: resourceKey, value: count of affected resources
}

// CommonCauseResult identifies shared dependencies between multiple resources.
type CommonCauseResult struct {
    // Resources are the input resources being analyzed.
    Resources []ResourceRef `json:"resources"`

    // SharedDependencies are resources that ALL input resources depend on.
    // These are the potential root cause candidates.
    SharedDependencies []AnnotatedNode `json:"sharedDependencies"`

    // SharedByCount groups dependencies by how many input resources share them.
    // Sorted descending — things shared by ALL inputs first.
    SharedByCount []SharedDependency `json:"sharedByCount"`
}

type SharedDependency struct {
    Resource   AnnotatedNode `json:"resource"`
    SharedBy   int           `json:"sharedBy"`     // how many input resources share this
    TotalInput int           `json:"totalInput"`   // total input resources
    DependedBy []ResourceRef `json:"dependedBy"`   // which input resources depend on this
}
```

### 3.2 Diagnostic Graph Querier

```go
// DiagnosticGraphQuerier extends RelationshipGraphQuerier (doc 18) with
// health-annotated traversal methods. Built on top of the same graph —
// same edges, same declarations — but queries return health context.
type DiagnosticGraphQuerier interface {
    // --- Single-resource diagnostics ---

    // DiagnoseResource returns complete debugging context in one call.
    // This is the FIRST tool an AI agent should call when investigating an issue.
    // Returns: resource data + health + events + related resources with health + owner chain.
    DiagnoseResource(ctx context.Context, ref ResourceRef) (*DiagnosticContext, error)

    // GetRelatedWithHealth returns related resources annotated with health status.
    // Same as doc 18's GetRelated() but each target includes health.
    // The agent uses this to decide which edges to follow (unhealthy targets first).
    GetRelatedWithHealth(ctx context.Context, ref ResourceRef, direction string,
        relationshipType *RelationshipType) ([]AnnotatedEdge, error)

    // GetDiagnosticTree returns a health-annotated dependency tree for UI rendering.
    // Like doc 18's GetDependencyTree() but every node includes health status.
    // Perfect for rendering a visual topology with red/yellow/green health indicators.
    GetDiagnosticTree(ctx context.Context, ref ResourceRef,
        maxDepth int) (*DiagnosticTree, error)

    // --- Failure investigation ---

    // TraceUnhealthy follows only unhealthy/degraded edges through the graph.
    // Starting from a known-unhealthy resource, it finds the failure chain:
    //   Service(unhealthy) → Pod(unhealthy) → Node(degraded: DiskPressure)
    // Skips all healthy branches entirely. This is "follow the problem."
    //
    // Returns a tree (not a flat list) because failures can have multiple branches.
    // maxDepth limits how deep to traverse (default 5).
    TraceUnhealthy(ctx context.Context, ref ResourceRef,
        maxDepth int) (*DiagnosticTree, error)

    // --- Impact analysis ---

    // BlastRadius analyzes what would be affected if this resource fails or is removed.
    // Follows reverse (incoming) edges: who depends on this?
    // Recursive: Pod depends on Node, Service depends on Pod → Service is transitively affected.
    //
    // Use case: "What happens if I drain this node?" or "What breaks if this Secret is deleted?"
    BlastRadius(ctx context.Context, ref ResourceRef,
        maxDepth int) (*ImpactReport, error)

    // --- Multi-resource correlation ---

    // FindCommonCause identifies shared dependencies between multiple failing resources.
    // "These 5 pods are all crashing — what do they have in common?"
    //
    // Walks outgoing edges from each input resource, finds intersections.
    // Returns shared dependencies sorted by how many inputs share them.
    //
    // The killer use case: 5 pods crash-looping on different nodes, but they all
    // mount the same ConfigMap that was just updated. CommonCause finds it.
    FindCommonCause(ctx context.Context, refs []ResourceRef) (*CommonCauseResult, error)
}
```

---

## 4. How the Engine Maintains Health State

### 4.1 Health Cache

```
┌─────────────────────────────────────────────────────────────┐
│ Engine: HealthCache                                          │
│                                                               │
│ healthByRef map[string]*ResourceHealth                        │
│   key: "{pluginID}/{connID}/{resourceKey}/{namespace}/{id}"   │
│                                                               │
│ Updated from watch events:                                    │
│   ADD    → call HealthProvider.GetHealth(data) → cache        │
│   UPDATE → call HealthProvider.GetHealth(data) → update cache │
│   DELETE → remove from cache                                  │
└─────────────────────────────────────────────────────────────┘
```

### 4.2 Update Flow

```
Watch event (UPDATE, Pod frontend-def):
  1. Resource data arrives as json.RawMessage
  2. Engine checks: does Pod resourcer implement HealthProvider?
     YES → call GetHealth(data)
     NO  → skip (health remains HealthUnknown)
  3. Compare new health with cached health
  4. If changed:
     a. Update cache
     b. Emit health change event: "health/changed"
        {pluginID, connectionID, resourceKey, resourceID, oldHealth, newHealth}
     c. UI updates health indicator on the resource
     d. Diagnostic graph queries reflect new health immediately
```

### 4.3 Health Is Cheap

`GetHealth()` is a pure function on JSON data — no API calls, no I/O. It reads
status fields from the already-received resource data. Cost:

| Operation | Cost |
|-----------|------|
| `GetHealth()` per resource | ~1μs (JSON path lookups via gjson) |
| 10,000 pods UPDATE | ~10ms total health derivation |
| Memory per cached health | ~200 bytes per resource |
| 10,000 resources health cache | ~2MB |

This is negligible compared to the watch event processing itself.

---

## 5. Diagnostic MCP Tools

These are the high-level tools an AI agent uses for debugging. They combine graph
traversal + health + events into purpose-built diagnostic operations.

### 5.1 omniview_diagnose

```json
{
    "name": "omniview_diagnose",
    "description": "Get complete diagnostic context for a resource: its current status, health assessment, recent events (warnings first), and all related resources with their health status. This is the FIRST tool to call when investigating any issue. Returns everything needed to understand a resource's state without multiple round-trips.",
    "inputSchema": {
        "type": "object",
        "properties": {
            "connectionId": {
                "type": "string",
                "description": "Cluster/connection to query"
            },
            "resourceKey": {
                "type": "string",
                "description": "Resource type key (e.g., 'core::v1::Pod')"
            },
            "resourceId": {
                "type": "string",
                "description": "Resource name/ID"
            },
            "namespace": {
                "type": "string",
                "description": "Resource namespace (omit for cluster-scoped resources)"
            },
            "includeEvents": {
                "type": "boolean",
                "default": true,
                "description": "Include recent diagnostic events"
            },
            "eventLimit": {
                "type": "integer",
                "default": 10,
                "description": "Max events to return"
            }
        },
        "required": ["connectionId", "resourceKey", "resourceId"]
    },
    "annotations": {"readOnlyHint": true}
}
```

**Example response:**

```json
{
    "resource": { "metadata": { "name": "frontend-def", ... }, "status": { "phase": "Running", ... } },
    "health": {
        "status": "unhealthy",
        "reason": "CrashLoopBackOff",
        "message": "Container app restarted 47 times, last exit code 1",
        "since": "2024-01-15T10:25:00Z",
        "conditions": [
            {"type": "Ready", "status": "False", "reason": "ContainersNotReady"},
            {"type": "ContainersReady", "status": "False", "reason": "ContainersNotReady"}
        ]
    },
    "events": [
        {"type": "warning", "reason": "BackOff", "message": "Back-off restarting failed container app",
         "source": "kubelet", "count": 47, "lastSeen": "2024-01-15T10:30:00Z"},
        {"type": "warning", "reason": "Failed", "message": "Error: secret 'db-creds' key 'password' not found",
         "source": "kubelet", "count": 47, "lastSeen": "2024-01-15T10:30:00Z"},
        {"type": "normal", "reason": "Pulled", "message": "Container image 'myapp:v2' already present",
         "source": "kubelet", "count": 47, "lastSeen": "2024-01-15T10:30:00Z"}
    ],
    "related": [
        {"edge": {"type": "runs_on", "label": "runs on"}, "target": {
            "ref": {"resourceKey": "core::v1::Node", "id": "worker-2"},
            "health": {"status": "healthy", "reason": "Ready"}}},
        {"edge": {"type": "uses", "label": "uses config map"}, "target": {
            "ref": {"resourceKey": "core::v1::ConfigMap", "id": "app-config"},
            "health": {"status": "healthy"}}},
        {"edge": {"type": "uses", "label": "uses secret"}, "target": {
            "ref": {"resourceKey": "core::v1::Secret", "id": "db-creds"},
            "health": {"status": "degraded", "reason": "KeyMissing",
                       "message": "Key 'password' not found (referenced by 3 pods)"}}}
    ],
    "ownerChain": [
        {"ref": {"resourceKey": "apps::v1::ReplicaSet", "id": "frontend-def-abc"},
         "health": {"status": "degraded", "reason": "PartiallyAvailable", "message": "2/3 replicas ready"}},
        {"ref": {"resourceKey": "apps::v1::Deployment", "id": "frontend"},
         "health": {"status": "degraded", "reason": "PartiallyAvailable", "message": "2/3 replicas ready"}}
    ]
}
```

An AI agent reading this immediately knows:
- The Pod is unhealthy (CrashLoopBackOff)
- The Events tell it WHY: `secret 'db-creds' key 'password' not found`
- The related resources confirm: Secret db-creds is degraded (KeyMissing)
- The Node is fine (no need to investigate)
- The owner chain shows the Deployment is partially available (2/3)

**Root cause identified in a single tool call.**

### 5.2 omniview_trace_failure

```json
{
    "name": "omniview_trace_failure",
    "description": "Follow the failure chain from a resource through its unhealthy dependencies. Only traverses edges where targets are degraded or unhealthy — skips healthy branches entirely. Use this after omniview_diagnose when you need to go deeper into the dependency chain to find the root cause.",
    "inputSchema": {
        "type": "object",
        "properties": {
            "connectionId": {"type": "string"},
            "resourceKey": {"type": "string"},
            "resourceId": {"type": "string"},
            "namespace": {"type": "string"},
            "maxDepth": {
                "type": "integer",
                "default": 5,
                "maximum": 10,
                "description": "How many relationship hops to follow"
            }
        },
        "required": ["connectionId", "resourceKey", "resourceId"]
    },
    "annotations": {"readOnlyHint": true}
}
```

**Example — tracing from a Deployment:**

```json
{
    "root": {
        "ref": {"resourceKey": "apps::v1::Deployment", "id": "frontend"},
        "health": {"status": "degraded", "reason": "PartiallyAvailable"}
    },
    "children": [
        {
            "edge": {"type": "owns", "label": "owns"},
            "node": {
                "ref": {"resourceKey": "apps::v1::ReplicaSet", "id": "frontend-abc"},
                "health": {"status": "degraded", "reason": "PartiallyAvailable"}
            },
            "children": [
                {
                    "edge": {"type": "owns", "label": "owns"},
                    "node": {
                        "ref": {"resourceKey": "core::v1::Pod", "id": "frontend-def"},
                        "health": {"status": "unhealthy", "reason": "CrashLoopBackOff",
                                   "message": "secret 'db-creds' key 'password' not found"}
                    },
                    "children": [
                        {
                            "edge": {"type": "uses", "label": "uses secret"},
                            "node": {
                                "ref": {"resourceKey": "core::v1::Secret", "id": "db-creds"},
                                "health": {"status": "degraded", "reason": "KeyMissing"}
                            },
                            "children": []
                        }
                    ]
                }
            ]
        }
    ]
}
```

The failure chain: Deployment → ReplicaSet → Pod → Secret. No healthy branches returned.
The agent reads this top-to-bottom and sees the full failure path.

### 5.3 omniview_blast_radius

```json
{
    "name": "omniview_blast_radius",
    "description": "Analyze impact: what would be affected if this resource fails, is drained, or is removed. Shows all dependent resources (both direct and transitive) grouped by type, with health status and counts. Use this before performing destructive actions to understand consequences.",
    "inputSchema": {
        "type": "object",
        "properties": {
            "connectionId": {"type": "string"},
            "resourceKey": {"type": "string"},
            "resourceId": {"type": "string"},
            "namespace": {"type": "string"},
            "maxDepth": {
                "type": "integer",
                "default": 3,
                "maximum": 5
            }
        },
        "required": ["connectionId", "resourceKey", "resourceId"]
    },
    "annotations": {"readOnlyHint": true}
}
```

**Example — blast radius of a Node:**

```json
{
    "target": {"resourceKey": "core::v1::Node", "id": "worker-1"},
    "directDependents": {
        "core::v1::Pod": [
            {"ref": {"id": "nginx-abc"}, "health": {"status": "healthy"}},
            {"ref": {"id": "api-def"}, "health": {"status": "healthy"}},
            {"ref": {"id": "worker-ghi"}, "health": {"status": "healthy"}}
        ]
    },
    "transitiveDependents": {
        "apps::v1::Deployment": [
            {"ref": {"id": "nginx"}, "health": {"status": "healthy"}},
            {"ref": {"id": "api-server"}, "health": {"status": "healthy"}}
        ],
        "core::v1::Service": [
            {"ref": {"id": "nginx-svc"}, "health": {"status": "healthy"}},
            {"ref": {"id": "api-svc"}, "health": {"status": "healthy"}}
        ],
        "apps::v1::StatefulSet": [
            {"ref": {"id": "redis"}, "health": {"status": "healthy"}}
        ]
    },
    "affectedCounts": {
        "core::v1::Pod": 15,
        "apps::v1::Deployment": 8,
        "apps::v1::StatefulSet": 2,
        "core::v1::Service": 3
    },
    "summary": "Removing Node worker-1 would affect 15 pods across 8 deployments and 2 StatefulSets. 3 services would lose backends. The 2 StatefulSets (redis, postgresql) use local storage and require data migration."
}
```

### 5.4 omniview_common_cause

```json
{
    "name": "omniview_common_cause",
    "description": "Find shared dependencies between multiple failing resources. When several resources fail simultaneously, this identifies what they have in common — the likely root cause. For example: 5 pods crashing on different nodes that all mount the same ConfigMap.",
    "inputSchema": {
        "type": "object",
        "properties": {
            "connectionId": {"type": "string"},
            "resources": {
                "type": "array",
                "items": {
                    "type": "object",
                    "properties": {
                        "resourceKey": {"type": "string"},
                        "resourceId": {"type": "string"},
                        "namespace": {"type": "string"}
                    },
                    "required": ["resourceKey", "resourceId"]
                },
                "minItems": 2,
                "description": "The failing resources to analyze"
            }
        },
        "required": ["connectionId", "resources"]
    },
    "annotations": {"readOnlyHint": true}
}
```

**Example — 3 pods crashing, what do they share?**

```json
{
    "resources": [
        {"resourceKey": "core::v1::Pod", "id": "frontend-abc"},
        {"resourceKey": "core::v1::Pod", "id": "backend-def"},
        {"resourceKey": "core::v1::Pod", "id": "worker-ghi"}
    ],
    "sharedByCount": [
        {
            "resource": {
                "ref": {"resourceKey": "core::v1::Secret", "id": "db-credentials"},
                "health": {"status": "degraded", "reason": "KeyMissing"}
            },
            "sharedBy": 3,
            "totalInput": 3,
            "dependedBy": [
                {"id": "frontend-abc"}, {"id": "backend-def"}, {"id": "worker-ghi"}
            ]
        },
        {
            "resource": {
                "ref": {"resourceKey": "core::v1::Node", "id": "worker-2"},
                "health": {"status": "healthy"}
            },
            "sharedBy": 2,
            "totalInput": 3,
            "dependedBy": [
                {"id": "frontend-abc"}, {"id": "backend-def"}
            ]
        }
    ]
}
```

**Agent reads this:** "All 3 pods share Secret db-credentials, and that Secret is degraded.
That's the common cause — not the Node (healthy, and only shared by 2 of 3)."

---

## 6. Real-World Debugging Scenarios

### 6.1 Scenario: "My app is slow"

```
User: "The checkout service is really slow"

Agent:
1. omniview_diagnose("core::v1::Service", "checkout-svc")
   → Service: healthy
     Related pods:
       checkout-pod-1: healthy
       checkout-pod-2: degraded (HighRestartCount, 12 restarts)
       checkout-pod-3: healthy

2. omniview_diagnose("core::v1::Pod", "checkout-pod-2")
   → Pod: degraded (containers restarting under memory pressure)
     Events: [Warning OOMKilled "Container app exceeded memory limit"]
     Related:
       Node worker-3: degraded (MemoryPressure=True)

3. omniview_diagnose("core::v1::Node", "worker-3")
   → Node: degraded (MemoryPressure)
     Events: [Warning SystemOOM "System OOM encountered"]

4. Agent: "Your checkout service is slow because Pod checkout-pod-2 is
   repeatedly OOM-killed on Node worker-3, which is under memory pressure.
   2 of 3 pods are handling all traffic. Recommendations:
   - Increase pod memory limits
   - Consider moving workloads off worker-3 or adding capacity"
```

### 6.2 Scenario: "Everything in staging is broken"

```
User: "Staging is completely down"

Agent:
1. kubernetes_core_v1_Pod_find(connection="staging", filters=[
     {field: "status.phase", op: "neq", value: "Running"}
   ])
   → 12 pods not running: [app-1, app-2, api-1, api-2, worker-1, ...]

2. omniview_common_cause(resources=[app-1, app-2, api-1, api-2, worker-1, ...])
   → Shared by 12/12: Node staging-node-1 (unhealthy: NotReady)
     Shared by 8/12: Secret staging-tls (healthy)

3. omniview_diagnose("core::v1::Node", "staging-node-1")
   → Node: unhealthy (NotReady since 10 minutes ago)
     Events: [Warning NodeNotReady "Node staging-node-1 status is now: NodeNotReady"]
     Conditions: Ready=False (reason: KubeletNotReady)

4. Agent: "Staging is down because the only node (staging-node-1) became
   NotReady 10 minutes ago. The kubelet is not responding. All 12 pods
   were running on this single node. This is a single point of failure —
   staging has no redundancy."
```

### 6.3 Scenario: "Canary deployment is failing"

```
User: "We deployed v2 of the payment service and it's not working"

Agent:
1. omniview_diagnose("apps::v1::Deployment", "payment-v2")
   → Deployment: unhealthy (NoAvailableReplicas, 0/3 available)
     Related:
       ReplicaSet payment-v2-abc: unhealthy (0/3 replicas ready)

2. omniview_trace_failure("apps::v1::Deployment", "payment-v2")
   → Deployment (unhealthy)
     └── ReplicaSet (unhealthy)
         ├── Pod payment-v2-001 (unhealthy: ImagePullBackOff)
         ├── Pod payment-v2-002 (unhealthy: ImagePullBackOff)
         └── Pod payment-v2-003 (unhealthy: ImagePullBackOff)

3. omniview_diagnose("core::v1::Pod", "payment-v2-001")
   → Events: [Warning Failed "Failed to pull image 'registry.io/payment:v2':
              unauthorized: authentication required"]

4. Agent: "Payment v2 deployment failed because the container image
   'registry.io/payment:v2' can't be pulled — the registry requires
   authentication. Check that the image pull secret is configured correctly
   for the 'registry.io' registry."
```

### 6.4 Scenario: "Intermittent 503s from API gateway"

```
User: "Our API gateway returns 503 sometimes, not always"

Agent:
1. omniview_diagnose("core::v1::Service", "api-gateway")
   → Service: healthy
     Related pods (with health):
       gateway-pod-1: healthy
       gateway-pod-2: healthy
       gateway-pod-3: degraded (HighRestartCount, 6 restarts in 1h)
       gateway-pod-4: healthy

   "Intermittent" + "1 of 4 pods degraded" = likely cause.

2. omniview_diagnose("core::v1::Pod", "gateway-pod-3")
   → Pod: degraded (container restarting)
     Events: [Warning Unhealthy "Readiness probe failed: connection refused"]
     Related:
       Node worker-5: healthy
       ConfigMap gateway-config: healthy

3. Agent: "Intermittent 503s because Pod gateway-pod-3 is cycling between
   ready and not-ready (6 restarts in the last hour). When it's not ready,
   the Service removes it from endpoints and 25% of traffic sees 503 until
   K8s routes around it. The readiness probe is failing with 'connection refused'
   — the app process is crashing and restarting. Check application logs for
   gateway-pod-3."
```

### 6.5 Scenario: Pre-action impact check

```
User: "I want to drain node worker-2 for maintenance"

Agent:
1. omniview_blast_radius("core::v1::Node", "worker-2")
   → Direct: 22 pods
     Transitive:
       12 Deployments (can reschedule — replicated)
       1 StatefulSet: postgresql (LOCAL STORAGE — needs migration!)
       5 Services would temporarily lose backends

     Summary: "22 pods across 12 Deployments and 1 StatefulSet.
              The postgresql StatefulSet uses local storage and cannot
              be automatically rescheduled. 5 services will lose some
              backends during drain but will recover as pods reschedule."

2. Agent: "Before draining worker-2:
   ⚠ CRITICAL: StatefulSet 'postgresql' uses local storage on this node.
   You must manually migrate its data before draining.

   12 Deployments will automatically reschedule (no data loss).
   5 Services will experience brief disruption during pod rescheduling.

   Recommended steps:
   1. Migrate postgresql data to another node
   2. Cordon worker-2 (prevent new scheduling)
   3. Drain worker-2 (evict remaining pods)
   4. Perform maintenance
   5. Uncordon worker-2"
```

---

## 7. UI Visualization

### 7.1 Health-Annotated Topology View

The diagnostic graph enables a visual topology where every node shows health:

```
┌─────────────────────────────────────────────────────────┐
│  Deployment: frontend                                    │
│  ┌─────────┐    ┌─────────────┐    ┌─────────────────┐ │
│  │ Service  │───▶│ Pod (●green)│───▶│ Node (●green)   │ │
│  │ ●green   │    │ frontend-abc│    │ worker-1        │ │
│  │          │    └─────────────┘    └─────────────────┘ │
│  │          │    ┌─────────────┐    ┌─────────────────┐ │
│  │          │───▶│ Pod (●red)  │───▶│ Node (●green)   │ │
│  │          │    │ frontend-def│    │ worker-2        │ │
│  │          │    │ CrashLoop   │    └─────────────────┘ │
│  │          │    │     │       │                         │
│  │          │    │     ▼       │    ┌─────────────────┐ │
│  │          │    │  Secret     │───▶│ Secret (●yellow)│ │
│  │          │    │  db-creds ──┘    │ db-creds        │ │
│  └─────────┘    └─────────────┘    │ KeyMissing      │ │
│                                     └─────────────────┘ │
│  ● = healthy  ● = degraded  ● = unhealthy              │
└─────────────────────────────────────────────────────────┘
```

### 7.2 Rendering Data Source

The UI renders from `DiagnosticTree`:

```typescript
interface DiagnosticTreeNode {
    ref: ResourceRef;
    health: ResourceHealth | null;  // null = unknown
    edges: Array<{
        type: RelationshipType;
        label: string;
        target: DiagnosticTreeNode;
    }>;
}

// Color mapping
function healthColor(status: HealthStatus): string {
    switch (status) {
        case 'healthy':   return '#4caf50'; // green
        case 'degraded':  return '#ff9800'; // orange
        case 'unhealthy': return '#f44336'; // red
        case 'pending':   return '#2196f3'; // blue
        case 'unknown':   return '#9e9e9e'; // grey
    }
}
```

### 7.3 Interactive Features

| Feature | Data Source |
|---------|-----------|
| Click node → navigate to resource detail | `ResourceRef` in `DiagnosticTreeNode` |
| Hover node → show health tooltip | `ResourceHealth.Message` |
| Filter by health status | `HealthStatus` on each node |
| Highlight failure chain | `TraceUnhealthy` result |
| Show blast radius on hover | `BlastRadius` result |
| Show events on click | `EventProvider.GetEvents()` |
| Animate edge direction | `RelationshipDescriptor.Type` |

---

## 8. ResourceCapabilities Additions

Add to `ResourceCapabilities` (doc 13):

```go
type ResourceCapabilities struct {
    // ... existing fields from doc 13 ...

    HasRelationships bool `json:"hasRelationships"` // implements RelationshipProvider (doc 18)
    HasHealth        bool `json:"hasHealth"`         // implements HealthProvider (this doc)
    HasEvents        bool `json:"hasEvents"`         // implements EventProvider (this doc)
}
```

The SDK derives these at registration time:

```
if _, ok := resourcer.(RelationshipProvider); ok { caps.HasRelationships = true }
if _, ok := resourcer.(HealthProvider); ok        { caps.HasHealth = true }
if _, ok := resourcer.(EventProvider[ClientT]); ok { caps.HasEvents = true }
```

---

## 9. How This Builds on Doc 18

| Doc 18 (Topology) | This Doc (Diagnostics) |
|-------------------|----------------------|
| `RelationshipProvider` declares edges | Same edges, reused |
| `RelationshipGraph` stores edges | Same graph, reused |
| `GetRelated()` returns edges | `GetRelatedWithHealth()` adds health to each edge |
| `GetDependencyTree()` returns tree | `GetDiagnosticTree()` adds health to each node |
| `RelationshipGraphQuerier` interface | `DiagnosticGraphQuerier` extends it |
| No health concept | `HealthProvider` + `ResourceHealth` |
| No events | `EventProvider` + `ResourceEvent` |
| No failure tracing | `TraceUnhealthy()` |
| No impact analysis | `BlastRadius()` |
| No correlation | `FindCommonCause()` |
| No single diagnostic call | `DiagnoseResource()` |

**Doc 18 is implemented first.** The topology graph works standalone — AI agents can
navigate relationships, UI can render topology diagrams. This document adds the diagnostic
layer as a second sprint.

---

## 10. Implementation Phases

| Phase | Scope | Dependencies |
|-------|-------|-------------|
| 1 | `HealthProvider` interface in SDK | SDK refactor (doc 09) |
| 2 | K8s plugin implements HealthProvider for core types (Pod, Node, Deployment, Service, PVC, StatefulSet, DaemonSet, Job) | Phase 1 |
| 3 | Engine `HealthCache` — derive and cache health from watch events | Phase 2 |
| 4 | `EventProvider` interface in SDK | Phase 1 |
| 5 | K8s plugin implements EventProvider for core types | Phase 4 |
| 6 | `DiagnosticGraphQuerier` — health-annotated graph queries | Phase 3, doc 18 graph |
| 7 | `DiagnoseResource()` and `GetRelatedWithHealth()` | Phase 6 |
| 8 | `TraceUnhealthy()` | Phase 6 |
| 9 | `BlastRadius()` and `FindCommonCause()` | Phase 6 |
| 10 | Diagnostic MCP tools (omniview_diagnose, etc.) | Phase 7-9, doc 14 |
| 11 | UI: health indicators on topology view | Phase 3, doc 18 UI |
| 12 | UI: failure chain highlighting | Phase 8 |
| 13 | UI: blast radius visualization | Phase 9 |

Phases 1-5 can begin immediately after doc 18 is complete. Phases 6-9 build
incrementally. Each phase is independently valuable:
- Phase 3 alone gives health indicators in the UI
- Phase 7 alone gives the `diagnose` MCP tool (the highest-impact single feature)
- Phases 8-9 add the advanced traversal patterns

---

## 11. What This Document Does NOT Cover

| Topic | Where |
|-------|-------|
| Topology graph structure, relationship types, extractors | Doc 18 |
| SDK interface design (Resourcer, CRUD, Watch) | Doc 09 |
| Filter/query type system | Doc 13 |
| MCP server runtime | Doc 14 |
| Cross-resource query routing | Doc 17 |
| Alerting / notification thresholds | Future doc — triggered by health transitions |
| Historical health timeline | Future doc — health snapshots over time |
| SLO/SLI integration | Future doc — map health to SLOs |
| Automated remediation | Future doc — auto-actions triggered by health patterns |
| Log correlation | Future doc — connecting resource health to log streams |
