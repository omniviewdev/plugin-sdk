# Query, Filter & MCP Readiness — Design Additions

## 1. Motivation

The new SDK design (doc 09) must support three future use cases from day 1 without requiring another refactor:

1. **Automatic MCP server generation** — plugins auto-expose resources as MCP tools with proper `inputSchema`/`outputSchema`. An AI agent discovers tools, understands parameters, and queries efficiently.

2. **AI-agent-efficient querying** — structured filter predicates so agents get exactly the data they need. An agent querying Pods should be able to say "phase=Running AND node=worker-1" without listing everything and filtering client-side.

3. **IDE query syntax** — the same filter infrastructure serves a command palette, query bar, and advanced search UI. Users type structured queries; the same `FilterExpression` type drives both AI and human queries.

**Current gaps in doc 09:**

| Gap | Impact |
|-----|--------|
| `FindInput.Conditions` is `map[string]interface{}` | Agent can't discover valid filter fields or operators |
| No filter field declarations | No `FilterableProvider` — agent has no way to ask "what can I filter on?" |
| No `ResourceCapabilities` flags | Agent can't know if a resource is watchable, filterable, namespace-scoped |
| `ActionDescriptor` has no `ParamsSchema` | Agent can't know what parameters an action expects |
| `OrderParams` is single-field | Insufficient for complex queries |
| No text search capability | No command palette or AI natural language search |
| No cardinality/scale hints | Agent can't decide list-all vs. paginate vs. filter-first |
| `EditorSchema` is Monaco-specific | Agent needs raw JSON Schema, not editor metadata |
| `ListInput.Params` / `FindInput.Params` are `map[string]interface{}` | Untyped escape hatches with no discoverability |

All of these gaps can be closed by adding **optional interfaces** (type-asserted on Resourcer implementations) and **new data types** that follow the existing doc 09 design principles. No core interfaces change — this is purely additive.

---

## 2. Design Principles Alignment

Each addition follows doc 09's design principles:

| # | Principle | How this design follows it |
|---|-----------|--------------------------|
| 1 | `context.Context` first | All new methods take `context.Context` first |
| 2 | Interfaces at boundaries, structs internally | `FilterableProvider`, `TextSearchProvider`, `ResourceSchemaProvider`, `ScaleHintProvider` are interfaces; `FilterField`, `FilterPredicate`, `ResourceCapabilities` are structs |
| 3 | Optional capabilities via interface assertion | All new capabilities are optional — type-asserted on Resourcer implementations |
| 4 | Co-locate related concerns | Filter fields live on the same Resourcer as CRUD and Watch |
| 5 | `ClientT` where it pays | `TextSearchProvider[ClientT]` and `ResourceSchemaProvider[ClientT]` need the typed client; `FilterableProvider` does not |
| 11 | **Introspectable by construction** (NEW) | Every capability is discoverable at runtime — filter fields, schemas, capabilities, action params |

---

## 3. Filter/Query Type System

### 3.1 FilterOperator (closed enum)

A finite set of comparison operators. Adding an operator is a protocol change — this keeps the set manageable for AI agents and MCP tool input schemas.

```go
type FilterOperator string

const (
    // Equality
    OpEqual          FilterOperator = "eq"       // field == value
    OpNotEqual       FilterOperator = "neq"      // field != value

    // Comparison (numeric, time)
    OpGreaterThan    FilterOperator = "gt"
    OpGreaterOrEqual FilterOperator = "gte"
    OpLessThan       FilterOperator = "lt"
    OpLessOrEqual    FilterOperator = "lte"

    // String matching
    OpContains       FilterOperator = "contains" // substring match
    OpPrefix         FilterOperator = "prefix"   // starts with
    OpSuffix         FilterOperator = "suffix"   // ends with
    OpRegex          FilterOperator = "regex"    // RE2 regex

    // Set membership
    OpIn             FilterOperator = "in"       // value in set
    OpNotIn          FilterOperator = "notin"    // value not in set

    // Existence
    OpExists         FilterOperator = "exists"    // field present & non-nil
    OpNotExists      FilterOperator = "notexists" // field absent or nil

    // Map/label-specific
    OpHasKey         FilterOperator = "haskey"   // map contains key
)
```

**Rationale:** Modeled after K8s label selector operators (In, NotIn, Exists, DoesNotExist) extended with string matching (contains, prefix, regex) and comparison (gt, lt) for non-K8s backends. The set is small enough for an AI agent to reason about, yet expressive enough for real-world queries. An MCP tool generator puts these in a JSON Schema `enum`.

### 3.2 FilterFieldType

```go
type FilterFieldType string

const (
    FilterFieldString  FilterFieldType = "string"
    FilterFieldInt     FilterFieldType = "integer"
    FilterFieldFloat   FilterFieldType = "number"
    FilterFieldBool    FilterFieldType = "boolean"
    FilterFieldTime    FilterFieldType = "datetime"  // RFC3339
    FilterFieldEnum    FilterFieldType = "enum"      // fixed set of values
    FilterFieldMap     FilterFieldType = "map"       // label/annotation selectors
)
```

### 3.3 FilterField (introspection type)

This is the declaration type — a plugin declares what fields are filterable, with what types and operators. An MCP tool generator iterates these to build `inputSchema`.

```go
// FilterField declares a field that can be used in filter predicates.
// This is the introspection/discovery type — not the request type.
type FilterField struct {
    // Path is the dot-separated field path into the resource data structure.
    // Examples: "metadata.name", "metadata.namespace", "metadata.labels",
    //           "status.phase", "spec.nodeName", "spec.replicas"
    Path string `json:"path"`

    // DisplayName is the human-readable name for UI filter dropdowns
    // and MCP tool descriptions. E.g., "Name", "Phase", "Node Name".
    DisplayName string `json:"displayName"`

    // Description explains what this field represents.
    // Used as the JSON Schema "description" in MCP tool inputSchema.
    // Should be concise — 1-2 sentences.
    Description string `json:"description"`

    // Type declares the value type for this field.
    // Drives JSON Schema type generation and UI input component selection.
    Type FilterFieldType `json:"type"`

    // Operators lists the comparison operators valid for this field.
    // If empty, defaults to [OpEqual, OpNotEqual].
    // An MCP tool generator uses these as enum values for the operator parameter.
    Operators []FilterOperator `json:"operators"`

    // AllowedValues is the fixed set of valid values (only for FilterFieldEnum).
    // E.g., for "status.phase": ["Running", "Pending", "Succeeded", "Failed", "Unknown"].
    // Used as JSON Schema enum values and UI dropdown options.
    AllowedValues []string `json:"allowedValues,omitempty"`

    // Required indicates this field MUST be provided in filter queries.
    // Most fields are optional. Some backends require certain selectors
    // (e.g., AWS requires region, some APIs require a label filter).
    Required bool `json:"required,omitempty"`
}
```

### 3.4 FilterPredicate (request type)

```go
// FilterPredicate is a single condition in a query.
// The caller sends these — the Resourcer's Find() implementation applies them.
type FilterPredicate struct {
    // Field is the filter field path (must match a FilterField.Path).
    Field string `json:"field"`

    // Operator is the comparison operator (must be in the field's Operators list).
    Operator FilterOperator `json:"operator"`

    // Value is the comparison value. Type depends on field and operator:
    //   - OpIn/OpNotIn: []string (set of values)
    //   - OpExists/OpNotExists: ignored (nil)
    //   - All others: scalar matching the field's FilterFieldType
    Value interface{} `json:"value,omitempty"`
}
```

### 3.5 FilterExpression (composite predicates)

```go
type FilterLogic string

const (
    FilterAnd FilterLogic = "and" // all predicates must match (default)
    FilterOr  FilterLogic = "or"  // any predicate must match
)

// FilterExpression combines predicates with a logical operator.
// Supports one level of nesting (AND of ORs, or OR of ANDs).
// One level covers virtually every real-world query without the complexity
// of arbitrary expression trees that no backend would fully support.
type FilterExpression struct {
    Logic      FilterLogic        `json:"logic,omitempty"`      // default: FilterAnd
    Predicates []FilterPredicate  `json:"predicates,omitempty"` // leaf conditions
    Groups     []FilterExpression `json:"groups,omitempty"`     // nested groups (1 level max)
}
```

**Example — "Running or Pending pods on node worker-1":**

```go
FilterExpression{
    Logic: FilterAnd,
    Groups: []FilterExpression{
        {
            Logic: FilterOr,
            Predicates: []FilterPredicate{
                {Field: "status.phase", Operator: OpEqual, Value: "Running"},
                {Field: "status.phase", Operator: OpEqual, Value: "Pending"},
            },
        },
    },
    Predicates: []FilterPredicate{
        {Field: "spec.nodeName", Operator: OpEqual, Value: "worker-1"},
    },
}
```

---

## 4. Optional Interfaces

### 4.1 FilterableProvider

```go
// FilterableProvider declares the filter fields available for a resource type.
// Optional — type-asserted on Resourcer implementations.
//
// If NOT implemented:
//   - Find() still works but callers can't discover valid filter fields
//   - The SDK does NOT filter for the plugin — the Resourcer applies filters itself
//   - MCP tool generators omit filter parameters from the tool schema
//
// If implemented:
//   - AI agents and MCP tool generators enumerate filter fields
//   - IDE command palette shows autocomplete for filter fields
//   - The SDK MAY validate predicates against declared fields (rejecting
//     unknown fields/operators before calling Find())
//
// connectionID is provided because some backends have different filterable
// fields per connection (e.g., CRD custom columns). Ignore it for static field sets.
type FilterableProvider interface {
    FilterFields(ctx context.Context, connectionID string) ([]FilterField, error)
}
```

**K8s base resourcer example:**

```go
func (r *KubeResourcerBase) FilterFields(ctx context.Context, _ string) ([]FilterField, error) {
    return []FilterField{
        {
            Path: "metadata.name", DisplayName: "Name",
            Description: "Resource name",
            Type: FilterFieldString,
            Operators: []FilterOperator{OpEqual, OpNotEqual, OpContains, OpPrefix, OpRegex},
        },
        {
            Path: "metadata.namespace", DisplayName: "Namespace",
            Description: "Resource namespace",
            Type: FilterFieldString,
            Operators: []FilterOperator{OpEqual, OpNotEqual, OpIn},
        },
        {
            Path: "metadata.labels", DisplayName: "Labels",
            Description: "Resource labels (key=value pairs)",
            Type: FilterFieldMap,
            Operators: []FilterOperator{OpHasKey, OpEqual, OpNotEqual, OpIn},
        },
        {
            Path: "metadata.annotations", DisplayName: "Annotations",
            Description: "Resource annotations (key=value pairs)",
            Type: FilterFieldMap,
            Operators: []FilterOperator{OpHasKey, OpEqual},
        },
        {
            Path: "metadata.creationTimestamp", DisplayName: "Created",
            Description: "Resource creation time",
            Type: FilterFieldTime,
            Operators: []FilterOperator{OpGreaterThan, OpLessThan, OpGreaterOrEqual, OpLessOrEqual},
        },
    }, nil
}

// Pod resourcer adds type-specific fields:
func (r *PodResourcer) FilterFields(ctx context.Context, connID string) ([]FilterField, error) {
    base, _ := r.KubeResourcerBase.FilterFields(ctx, connID)
    return append(base,
        FilterField{
            Path: "status.phase", DisplayName: "Phase",
            Description: "Pod lifecycle phase",
            Type: FilterFieldEnum,
            Operators: []FilterOperator{OpEqual, OpNotEqual, OpIn},
            AllowedValues: []string{"Pending", "Running", "Succeeded", "Failed", "Unknown"},
        },
        FilterField{
            Path: "spec.nodeName", DisplayName: "Node",
            Description: "Node the pod is scheduled on",
            Type: FilterFieldString,
            Operators: []FilterOperator{OpEqual, OpNotEqual, OpIn},
        },
        FilterField{
            Path: "status.containerStatuses.restartCount", DisplayName: "Restarts",
            Description: "Container restart count",
            Type: FilterFieldInt,
            Operators: []FilterOperator{OpEqual, OpGreaterThan, OpLessThan},
        },
    ), nil
}
```

### 4.2 TextSearchProvider

```go
// TextSearchProvider adds free-text search to a Resourcer.
// Optional — for command palette, AI natural language queries, global search.
//
// Separate from FilterableProvider: structured predicates ("phase=Running")
// vs. text matching ("find things related to nginx").
//
// The implementation decides which fields to search and how to rank results.
// For K8s: typically name, labels, annotations, status messages.
// For AWS: instance names, tags, IDs.
type TextSearchProvider[ClientT any] interface {
    Search(ctx context.Context, client *ClientT, meta ResourceMeta, query string, limit int) (*FindResult, error)
}
```

### 4.3 ResourceSchemaProvider

```go
// ResourceSchemaProvider provides the raw JSON Schema for a resource type.
// Optional — describes the full resource data structure (all fields, types, descriptions).
//
// DISTINCT from EditorSchema (§4.2): EditorSchema wraps JSON Schema in Monaco-specific
// metadata (FileMatch, URI, Language). This returns the raw schema directly.
// Both may use the same underlying data (e.g., K8s OpenAPI specs), but serve different consumers.
//
// Used by:
//   - MCP tool generator: as outputSchema for Get/List tools
//   - AI agents: to understand resource structure before querying
//   - IDE: for YAML/JSON autocompletion
type ResourceSchemaProvider[ClientT any] interface {
    GetResourceSchema(ctx context.Context, client *ClientT, meta ResourceMeta) (json.RawMessage, error)
}
```

### 4.4 ScaleHintProvider

```go
// ScaleHintProvider declares expected cardinality for a resource type.
// Optional — helps AI agents decide query strategy:
//   - ScaleFew: safe to List all
//   - ScaleModerate: should paginate or filter
//   - ScaleMany: must filter first, never list all
type ScaleHintProvider interface {
    ScaleHint() *ScaleHint
}
```

---

## 5. ResourceCapabilities

Auto-derived by the SDK from type assertions on registered Resourcers. Plugin authors don't implement this type directly — the SDK inspects each Resourcer at registration time and builds the capabilities struct.

```go
// ResourceCapabilities — auto-derived by SDK, exposed via TypeProvider.
// An MCP tool generator reads these to decide which tools to create.
// An AI agent reads these to understand how to interact with a resource type.
type ResourceCapabilities struct {
    // CRUD capability flags (from ResourceDefinition.SupportedOperations)
    CanGet    bool `json:"canGet"`
    CanList   bool `json:"canList"`
    CanFind   bool `json:"canFind"`
    CanCreate bool `json:"canCreate"`
    CanUpdate bool `json:"canUpdate"`
    CanDelete bool `json:"canDelete"`

    // Extended capabilities (from interface type assertions)
    Watchable       bool `json:"watchable"`       // Resourcer implements Watcher
    Filterable      bool `json:"filterable"`      // Resourcer implements FilterableProvider
    Searchable      bool `json:"searchable"`      // Resourcer implements TextSearchProvider
    HasActions      bool `json:"hasActions"`      // Resourcer implements ActionResourcer
    HasSchema       bool `json:"hasSchema"`       // Resourcer implements ResourceSchemaProvider
    NamespaceScoped bool `json:"namespaceScoped"` // NamespaceAccessor is non-empty

    // Cardinality guidance (from ScaleHintProvider type assertion)
    ScaleHint *ScaleHint `json:"scaleHint,omitempty"`
}

type ScaleCategory string

const (
    ScaleFew      ScaleCategory = "few"      // <100 typical (Namespaces, Nodes, ClusterRoles)
    ScaleModerate ScaleCategory = "moderate"  // 100-10K (Deployments, Services, ConfigMaps)
    ScaleMany     ScaleCategory = "many"      // 10K+ (Pods, Events, Endpoints in large clusters)
)

type ScaleHint struct {
    ExpectedCount   ScaleCategory `json:"expectedCount"`
    DefaultPageSize int           `json:"defaultPageSize,omitempty"` // 0 = system default
}
```

**How the SDK derives capabilities:**

```
At ResourceRegistration time:
  resourcer := registration.Resourcer

  caps.CanGet    = definition.SupportedOperations contains Get
  caps.CanList   = definition.SupportedOperations contains List
  // ... etc for all CRUD ops

  if _, ok := resourcer.(Watcher[ClientT]); ok       { caps.Watchable = true }
  if _, ok := resourcer.(FilterableProvider); ok      { caps.Filterable = true }
  if _, ok := resourcer.(TextSearchProvider[ClientT]); ok { caps.Searchable = true }
  if _, ok := resourcer.(ActionResourcer[ClientT]); ok    { caps.HasActions = true }
  if _, ok := resourcer.(ResourceSchemaProvider[ClientT]); ok { caps.HasSchema = true }
  if _, ok := resourcer.(ScaleHintProvider); ok       { caps.ScaleHint = resourcer.(ScaleHintProvider).ScaleHint() }

  caps.NamespaceScoped = definition.NamespaceAccessor != ""
```

---

## 6. ActionDescriptor Enhancement

Add three fields to `ActionDescriptor` (doc 09 §4.3 ActionResourcer):

```go
type ActionDescriptor struct {
    // Existing fields:
    ID          string      `json:"id"`
    Label       string      `json:"label"`
    Description string      `json:"description"`
    Icon        string      `json:"icon"`
    Scope       ActionScope `json:"scope"`
    Streaming   bool        `json:"streaming"`

    // NEW — ParamsSchema is a JSON Schema describing ActionInput.Params.
    // Enables MCP tool inputSchema generation. An AI agent reads this
    // to know what parameters to pass.
    //
    // Example for "scale" action:
    //   {"type":"object","properties":{"replicas":{"type":"integer","minimum":0,
    //    "description":"Target replica count"}},"required":["replicas"]}
    //
    // nil = action takes no extra parameters (only resource ID/namespace).
    ParamsSchema json.RawMessage `json:"paramsSchema,omitempty"`

    // NEW — OutputSchema is a JSON Schema describing ActionResult.Data.
    // Enables MCP tool outputSchema generation.
    // nil = output is unstructured.
    OutputSchema json.RawMessage `json:"outputSchema,omitempty"`

    // NEW — Dangerous marks actions requiring confirmation.
    // AI agents MUST prompt before executing. UI shows confirmation dialog.
    // Examples: delete, drain, cordon, force-restart.
    Dangerous bool `json:"dangerous,omitempty"`
}
```

---

## 7. FindInput / ListInput Redesign

### FindInput (replaces untyped Conditions)

```go
type FindInput struct {
    // Filters is the structured filter expression.
    // Replaces the old Conditions map[string]interface{}.
    // If nil, Find returns all resources (same as List).
    Filters *FilterExpression `json:"filters,omitempty"`

    // TextQuery is an optional free-text search string.
    // Only used if the Resourcer implements TextSearchProvider.
    // If both Filters and TextQuery are set, results must match BOTH.
    TextQuery string `json:"textQuery,omitempty"`

    // Namespaces scopes the search. Empty = all namespaces.
    Namespaces []string `json:"namespaces,omitempty"`

    // Order specifies result ordering. Supports multiple fields.
    Order []OrderField `json:"order,omitempty"`

    // Pagination specifies result pagination.
    Pagination *PaginationParams `json:"pagination,omitempty"`
}
```

### ListInput (enhanced)

```go
type ListInput struct {
    // Namespaces scopes the list. Empty = all namespaces.
    Namespaces []string `json:"namespaces,omitempty"`

    // Order specifies result ordering. Supports multiple fields.
    // Replaces the old single-field OrderParams.
    Order []OrderField `json:"order,omitempty"`

    // Pagination specifies result pagination.
    Pagination *PaginationParams `json:"pagination,omitempty"`
}
```

### OrderField (replaces OrderParams)

```go
type OrderField struct {
    Path       string `json:"path"`                 // dot-path field to order by
    Descending bool   `json:"descending,omitempty"` // default: ascending
}
```

### PaginationParams (cursor support)

```go
type PaginationParams struct {
    Page     int    `json:"page,omitempty"`     // 1-based page number
    PageSize int    `json:"pageSize,omitempty"` // 0 = backend default
    Cursor   string `json:"cursor,omitempty"`   // opaque continuation token
}

type PaginationResult struct {
    Page       int    `json:"page"`
    PageSize   int    `json:"pageSize"`
    Total      int    `json:"total"`
    Pages      int    `json:"pages"`
    NextCursor string `json:"nextCursor,omitempty"` // empty = no more results
}
```

### Dropped fields

| Removed | Replacement | Reason |
|---------|-------------|--------|
| `FindInput.Conditions map[string]interface{}` | `FindInput.Filters *FilterExpression` | Typed, introspectable predicates |
| `FindInput.Params map[string]interface{}` | Removed entirely | Untyped escape hatch with no discoverability |
| `ListInput.Params map[string]interface{}` | Removed entirely | Same — if filtering is needed, use Find with Filters |
| `OrderParams` (single field, `bool` direction) | `[]OrderField` (multi-field, named path) | Multi-field ordering for complex queries |

---

## 8. TypeProvider Additions (gRPC Boundary)

Add to `TypeProvider` in doc 09 §4.7:

```go
type TypeProvider interface {
    // ... existing methods (GetResourceGroups, GetResourceTypes, etc.) ...

    // GetResourceCapabilities returns auto-derived capability flags.
    // Used by MCP tool generators to decide which tools to create.
    GetResourceCapabilities(ctx context.Context, resourceKey string) (*ResourceCapabilities, error)

    // GetResourceSchema returns the JSON Schema for a resource type.
    // Used by MCP tool generators as outputSchema.
    // Returns nil if the Resourcer doesn't implement ResourceSchemaProvider.
    GetResourceSchema(ctx context.Context, connectionID string, resourceKey string) (json.RawMessage, error)

    // GetFilterFields returns declared filter fields for a resource type.
    // Used by MCP tool generators to build inputSchema for Find tools.
    // Returns [] if the Resourcer doesn't implement FilterableProvider.
    GetFilterFields(ctx context.Context, connectionID string, resourceKey string) ([]FilterField, error)
}
```

---

## 9. MCP Tool Generation Mapping

This validates that the design is sufficient for automatic MCP tool generation. Every piece of information an MCP tool descriptor needs is available through introspectable SDK interfaces.

### 9.1 Mapping Table

| MCP Tool Element | SDK Source | Interface/Method |
|-----------------|-----------|-----------------|
| Tool name | `ResourceMeta.Key()` | `TypeProvider.GetResourceTypes()` |
| Tool description | `ResourceMeta.Description` | Same |
| Which tools to create per resource | `ResourceCapabilities` flags | `TypeProvider.GetResourceCapabilities()` |
| List tool inputSchema | `ListInput` fields + namespace list | `ConnectionLifecycleProvider.GetConnectionNamespaces()` |
| Find tool inputSchema — filter fields | `[]FilterField` | `TypeProvider.GetFilterFields()` |
| Find tool inputSchema — filter operators | `FilterField.Operators` | Same |
| Find tool inputSchema — enum values | `FilterField.AllowedValues` | Same |
| Get tool inputSchema | `GetInput` (id + namespace) | Static schema |
| Create/Update tool inputSchema | Resource JSON Schema | `TypeProvider.GetResourceSchema()` |
| Action tool inputSchema | `ActionDescriptor.ParamsSchema` + id/namespace | `ActionProvider.GetActions()` |
| Action safety flag | `ActionDescriptor.Dangerous` | Same |
| Output schema (all tools) | Resource JSON Schema | `TypeProvider.GetResourceSchema()` |
| Namespace scoping | `ResourceCapabilities.NamespaceScoped` | `TypeProvider.GetResourceCapabilities()` |
| Scale guidance | `ResourceCapabilities.ScaleHint` | Same |
| Streaming support | `ActionDescriptor.Streaming` | `ActionProvider.GetActions()` |

### 9.2 Generated MCP Tool Examples

**List tool:**
```json
{
    "name": "kubernetes_core_v1_Pod_list",
    "description": "List Pods — a set of running containers in your cluster",
    "inputSchema": {
        "type": "object",
        "properties": {
            "connectionId": {"type": "string", "description": "Cluster connection ID"},
            "namespaces": {"type": "array", "items": {"type": "string"}},
            "pageSize": {"type": "integer", "default": 100},
            "cursor": {"type": "string"}
        },
        "required": ["connectionId"]
    },
    "annotations": {"readOnlyHint": true}
}
```

**Find tool (with filters):**
```json
{
    "name": "kubernetes_core_v1_Pod_find",
    "description": "Find Pods matching filters",
    "inputSchema": {
        "type": "object",
        "properties": {
            "connectionId": {"type": "string"},
            "namespaces": {"type": "array", "items": {"type": "string"}},
            "textQuery": {"type": "string", "description": "Free-text search"},
            "filters": {
                "type": "array",
                "items": {
                    "type": "object",
                    "properties": {
                        "field": {
                            "type": "string",
                            "enum": ["metadata.name", "metadata.namespace", "metadata.labels",
                                     "status.phase", "spec.nodeName"],
                            "description": "Field to filter on"
                        },
                        "operator": {
                            "type": "string",
                            "enum": ["eq", "neq", "contains", "prefix", "regex", "in",
                                     "notin", "haskey", "exists", "gt", "lt"]
                        },
                        "value": {}
                    },
                    "required": ["field", "operator"]
                }
            }
        },
        "required": ["connectionId"]
    }
}
```

**Action tool:**
```json
{
    "name": "kubernetes_apps_v1_Deployment_scale",
    "description": "Scale a Deployment to a target replica count",
    "inputSchema": {
        "type": "object",
        "properties": {
            "connectionId": {"type": "string"},
            "namespace": {"type": "string"},
            "id": {"type": "string", "description": "Deployment name"},
            "replicas": {"type": "integer", "minimum": 0}
        },
        "required": ["connectionId", "namespace", "id", "replicas"]
    },
    "annotations": {"destructiveHint": false}
}
```

---

## 10. Filter Execution Model

The SDK does **NOT** execute `FilterExpression` on behalf of plugins. The Resourcer's `Find()` implementation receives the typed predicates and applies them to its backend:

- **K8s**: Convert `FilterPredicate{Field: "metadata.labels", Operator: OpHasKey, Value: "app"}` → `metav1.ListOptions{LabelSelector: "app"}`. Convert `FilterPredicate{Field: "metadata.name", Operator: OpEqual, Value: "nginx"}` → `fields.SelectorFromSet({"metadata.name": "nginx"})`.

- **AWS**: Convert `FilterPredicate{Field: "State", Operator: OpIn, Value: ["running", "stopped"]}` → `ec2.DescribeInstancesInput{Filters: [{Name: "instance-state-name", Values: [...]}]}`.

- **SQL**: Convert `FilterPredicate{Field: "status", Operator: OpEqual, Value: "active"}` → `WHERE status = 'active'`.

**The SDK MAY validate predicates against declared FilterFields before calling Find():**
- Reject unknown field paths (not in `FilterableProvider.FilterFields()`)
- Reject invalid operators (not in `FilterField.Operators`)
- Reject type mismatches (string value for integer field)

This validation is best-effort — if `FilterableProvider` is not implemented, predicates pass through unvalidated.

---

## 11. What Is NOT Part of This Design

| Excluded | Reason |
|----------|--------|
| MCP server runtime | Separate concern — how the server is hosted, registered, authenticated |
| MCP transport (stdio, SSE, HTTP) | Separate concern — protocol implementation |
| Query execution engine | SDK does NOT parse/execute FilterExpression; plugins apply their own backend logic |
| NL-to-filter translation | AI agent's job — uses FilterField declarations as context |
| Cross-resource-type queries | Engine-level routing (FR-1, FR-3), not SDK |
| Relationship graph | FR-8, separate design doc when needed |
| Result projection (select fields) | Potential future addition; not needed for v1 — agents handle field extraction |
| Aggregation queries (count, sum, avg) | Potential future addition; not needed for v1 |

---

## 12. Proto Changes Required

### New proto messages for filter types:

```protobuf
// filter.proto (new file)

enum FilterOperator {
    FILTER_OP_EQUAL = 0;
    FILTER_OP_NOT_EQUAL = 1;
    FILTER_OP_GREATER_THAN = 2;
    FILTER_OP_GREATER_OR_EQUAL = 3;
    FILTER_OP_LESS_THAN = 4;
    FILTER_OP_LESS_OR_EQUAL = 5;
    FILTER_OP_CONTAINS = 6;
    FILTER_OP_PREFIX = 7;
    FILTER_OP_SUFFIX = 8;
    FILTER_OP_REGEX = 9;
    FILTER_OP_IN = 10;
    FILTER_OP_NOT_IN = 11;
    FILTER_OP_EXISTS = 12;
    FILTER_OP_NOT_EXISTS = 13;
    FILTER_OP_HAS_KEY = 14;
}

enum FilterFieldType {
    FILTER_FIELD_STRING = 0;
    FILTER_FIELD_INTEGER = 1;
    FILTER_FIELD_NUMBER = 2;
    FILTER_FIELD_BOOLEAN = 3;
    FILTER_FIELD_DATETIME = 4;
    FILTER_FIELD_ENUM = 5;
    FILTER_FIELD_MAP = 6;
}

enum FilterLogic {
    FILTER_AND = 0;
    FILTER_OR = 1;
}

message FilterField {
    string path = 1;
    string display_name = 2;
    string description = 3;
    FilterFieldType type = 4;
    repeated FilterOperator operators = 5;
    repeated string allowed_values = 6;
    bool required = 7;
}

message FilterPredicate {
    string field = 1;
    FilterOperator operator = 2;
    google.protobuf.Value value = 3;   // scalar or list
}

message FilterExpression {
    FilterLogic logic = 1;
    repeated FilterPredicate predicates = 2;
    repeated FilterExpression groups = 3;
}

message OrderField {
    string path = 1;
    bool descending = 2;
}
```

### Updates to existing proto messages:

```protobuf
// resource.proto — FindRequest update
message FindRequest {
    string key = 1;
    string context = 2;
    repeated string namespaces = 3;
    FilterExpression filters = 4;      // NEW — replaces nothing (Conditions was never in proto)
    string text_query = 5;             // NEW
    repeated OrderField order = 6;     // NEW
    int32 page_size = 7;               // NEW
    string cursor = 8;                 // NEW
}

// resource.proto — ListRequest update
message ListRequest {
    string key = 1;
    string context = 2;
    repeated string namespaces = 3;
    repeated OrderField order = 4;     // NEW
    int32 page_size = 5;               // NEW
    string cursor = 6;                 // NEW
}

// Pagination in responses
message ListResponse {
    bool success = 1;
    repeated bytes data = 2;           // per doc 12 optimization
    ResourceError error = 3;
    int32 total = 4;                   // NEW
    string next_cursor = 5;            // NEW
}

// ResourceCapabilities
message ResourceCapabilities {
    bool can_get = 1;
    bool can_list = 2;
    bool can_find = 3;
    bool can_create = 4;
    bool can_update = 5;
    bool can_delete = 6;
    bool watchable = 7;
    bool filterable = 8;
    bool searchable = 9;
    bool has_actions = 10;
    bool has_schema = 11;
    bool namespace_scoped = 12;
    ScaleHint scale_hint = 13;
}

message ScaleHint {
    string expected_count = 1;    // "few", "moderate", "many"
    int32 default_page_size = 2;
}

// ActionDescriptor update
message ActionDescriptor {
    string id = 1;
    string label = 2;
    string description = 3;
    string icon = 4;
    string scope = 5;
    bool streaming = 6;
    bytes params_schema = 7;       // NEW — JSON Schema bytes
    bytes output_schema = 8;       // NEW — JSON Schema bytes
    bool dangerous = 9;            // NEW
}
```

---

## 13. Summary

This design adds **5 optional interfaces**, **1 auto-derived type**, **3 enhanced fields**, and **redesigned input types** to the doc 09 SDK interface design. All additions follow the existing design principles (especially #3: optional capabilities via interface assertion). No core interfaces change — everything is additive.

The result: **every piece of information an MCP tool generator needs is introspectable from SDK interfaces**, and the same filter infrastructure serves IDE query syntax, command palette search, and AI agent queries.
