# 15: Query Execution Model — Filter Predicate Flow

This document describes how filter predicates flow from caller (frontend, AI agent, MCP tool)
through the SDK to the plugin's Resourcer, and how each backend translates predicates to
native query operations. The SDK validates but does NOT execute filters.

**Scope:** Design document only. Companion to doc 13 (filter type system) and doc 14 (MCP runtime).

**Key principle:** The SDK is a validation and routing layer, not a query engine. Predicates
pass through to the Resourcer's `Find()` method, which translates them to backend-specific
query semantics.

---

## 1. Why the SDK Does NOT Execute Filters

### 1.1 Backend Diversity

Different backends have fundamentally different filtering capabilities:

| Backend | Native Filter Mechanism |
|---------|------------------------|
| Kubernetes | Label selectors (`metav1.LabelSelector`), field selectors (`fields.Selector`), namespace filtering |
| AWS | API-level filters (`ec2.DescribeInstancesInput.Filters`), tag filters |
| SQL databases | `WHERE` clauses with indexes |
| Terraform | State file filtering (client-side) |
| Cloud APIs | Per-API query parameters |

A universal query engine would need to:
1. Understand every backend's query language
2. Know which filters can be pushed down vs. must be client-side
3. Handle partial push-down (some filters native, some post-filter)

This is the plugin's job, not the SDK's.

### 1.2 Performance

If the SDK executed filters, it would need to:
1. `List()` everything from the backend
2. Deserialize every object
3. Evaluate predicates on each object in Go
4. Re-serialize matching objects

This defeats the purpose of structured filters — the whole point is that backends like K8s
can apply filters server-side using indexes (label selectors, field selectors) to return
only matching objects. The Resourcer knows how to translate predicates to efficient
backend queries.

### 1.3 Semantic Fidelity

Backends have filter semantics that don't map cleanly to generic predicates:

- K8s label selectors use set-based matching (`In`, `NotIn`, `Exists`, `DoesNotExist`)
- K8s field selectors only support `=` and `!=` on a small set of fields
- AWS filters use `Name`/`Values` pairs with per-filter semantics
- Some backends support regex; others don't

The Resourcer knows its backend's semantics and can translate faithfully.

---

## 2. Predicate Flow

```
┌──────────────────────────────────────────────────────────────────────┐
│ Caller (Frontend / AI Agent / MCP Tool)                              │
│                                                                      │
│ FindInput{                                                           │
│   Filters: &FilterExpression{                                        │
│     Logic: "and",                                                    │
│     Predicates: [                                                    │
│       {Field: "status.phase", Operator: "eq", Value: "Running"},     │
│       {Field: "metadata.labels", Operator: "haskey", Value: "app"},  │
│     ],                                                               │
│   },                                                                 │
│   Namespaces: ["production"],                                        │
│   Pagination: {PageSize: 100},                                       │
│ }                                                                    │
└────────────────────────────┬─────────────────────────────────────────┘
                             │
                             ▼
┌──────────────────────────────────────────────────────────────────────┐
│ Engine Controller (controller.Find)                                   │
│                                                                      │
│ 1. Resolve plugin: client := clients[pluginID]                       │
│ 2. Create context: ctx := connectedCtx(conn)                         │
│ 3. Pass through: client.Find(ctx, resourceKey, findInput)            │
│                                                                      │
│ The engine controller is a PASS-THROUGH. It does not inspect,        │
│ validate, or modify FindInput. This keeps the engine generic.        │
└────────────────────────────┬─────────────────────────────────────────┘
                             │ gRPC boundary
                             ▼
┌──────────────────────────────────────────────────────────────────────┐
│ SDK (plugin process) — resourceController.Find                       │
│                                                                      │
│ STEP 1: Validate (optional, if FilterableProvider implemented)       │
│   fields := resourcer.(FilterableProvider).FilterFields(ctx, connID) │
│   for each predicate in findInput.Filters:                           │
│     - reject if predicate.Field not in declared fields               │
│     - reject if predicate.Operator not in field.Operators            │
│     - reject if value type doesn't match field.Type                  │
│   → return ResourceOperationError if validation fails                │
│                                                                      │
│ STEP 2: Resolve client                                               │
│   client := connectionManager.GetClient(connID)                      │
│                                                                      │
│ STEP 3: Delegate to Resourcer                                        │
│   result := resourcer.Find(ctx, client, meta, findInput)             │
│                                                                      │
│ STEP 4: Serialize result as JSON bytes (doc 12)                      │
│   return FindResult{Data: result.Data}  // json.RawMessage           │
└────────────────────────────┬─────────────────────────────────────────┘
                             │
                             ▼
┌──────────────────────────────────────────────────────────────────────┐
│ Plugin Resourcer (e.g., PodResourcer.Find)                           │
│                                                                      │
│ Translates FilterPredicates → backend-native query.                  │
│ This is where all the smart query building happens.                  │
└──────────────────────────────────────────────────────────────────────┘
```

---

## 3. SDK Validation Layer

### 3.1 When Validation Happens

```
FilterableProvider implemented?
  ├── YES → SDK validates predicates against declared FilterFields
  │         Invalid predicates → ResourceOperationError (before calling Find)
  │
  └── NO  → SDK passes predicates through unvalidated
            Resourcer's Find() handles unknown fields as it sees fit
            (ignore, error, best-effort)
```

### 3.2 What the SDK Validates

| Check | Condition | Error |
|-------|-----------|-------|
| Unknown field | `predicate.Field` not in `FilterFields[].Path` | `"unknown filter field: {field}"` |
| Invalid operator | `predicate.Operator` not in `FilterField.Operators` | `"operator {op} not supported for field {field}"` |
| Type mismatch | `predicate.Value` type doesn't match `FilterField.Type` | `"expected {type} for field {field}, got {actual}"` |
| Missing required | Required field not present in predicates | `"required filter field missing: {field}"` |
| Invalid enum value | Value not in `FilterField.AllowedValues` (for enum type) | `"invalid value for {field}: {value}, allowed: [...]"` |

### 3.3 Validation is Best-Effort

Validation catches obvious errors early. It does NOT guarantee the query will succeed —
the backend may have additional constraints the SDK can't express:

- K8s field selectors only work on specific fields (`metadata.name`, `metadata.namespace`,
  `spec.nodeName`, `status.phase`)
- AWS API filters have per-API limits on filter count
- Some backends don't support regex even though the field declares `OpRegex`

The Resourcer's `Find()` may return errors for queries that pass SDK validation.

---

## 4. Backend Translation Patterns

### 4.1 Kubernetes

```go
func (r *PodResourcer) Find(ctx context.Context, client *kubernetes.Clientset,
    meta ResourceMeta, input FindInput) (*FindResult, error) {

    opts := metav1.ListOptions{}

    if input.Filters != nil {
        var labelReqs []string
        var fieldReqs []string

        for _, p := range input.Filters.Predicates {
            switch p.Field {
            // Label selectors (server-side filtering via K8s API)
            case "metadata.labels":
                switch p.Operator {
                case OpHasKey:
                    labelReqs = append(labelReqs, p.Value.(string))
                case OpEqual:
                    // p.Value is "key=value" for map fields
                    labelReqs = append(labelReqs, fmt.Sprintf("%s=%s", p.Value...))
                case OpIn:
                    labelReqs = append(labelReqs, fmt.Sprintf("%s in (%s)",
                        p.Field, strings.Join(p.Value.([]string), ",")))
                }

            // Field selectors (limited server-side)
            case "metadata.name":
                if p.Operator == OpEqual {
                    fieldReqs = append(fieldReqs, fmt.Sprintf("metadata.name=%s", p.Value))
                }
            case "spec.nodeName":
                if p.Operator == OpEqual {
                    fieldReqs = append(fieldReqs, fmt.Sprintf("spec.nodeName=%s", p.Value))
                }
            case "status.phase":
                if p.Operator == OpEqual {
                    fieldReqs = append(fieldReqs, fmt.Sprintf("status.phase=%s", p.Value))
                }

            // Client-side post-filter for fields K8s doesn't support server-side
            default:
                // Accumulate for post-filter pass
            }
        }

        if len(labelReqs) > 0 {
            opts.LabelSelector = strings.Join(labelReqs, ",")
        }
        if len(fieldReqs) > 0 {
            opts.FieldSelector = strings.Join(fieldReqs, ",")
        }
    }

    // Cursor pagination → K8s continue token
    if input.Pagination != nil {
        if input.Pagination.Cursor != "" {
            opts.Continue = input.Pagination.Cursor
        }
        if input.Pagination.PageSize > 0 {
            opts.Limit = int64(input.Pagination.PageSize)
        }
    }

    list, err := client.CoreV1().Pods(namespace).List(ctx, opts)
    // ... post-filter remaining predicates that couldn't be pushed down
    // ... build FindResult with NextCursor = list.Continue
}
```

### 4.2 Predicate Push-Down Decision

Not all predicates can be pushed to the backend. The Resourcer must decide:

```
                FilterPredicate
                       │
            ┌──────────┴──────────┐
            ▼                     ▼
    Can push to backend?     Client-side post-filter
    (label selector,         (field not indexable,
     field selector,          operator not supported,
     API filter param)        complex expression)
            │                     │
            ▼                     ▼
    Backend returns          Filter in-memory
    pre-filtered results     after List/Find
```

**The K8s plugin's predicate push-down mapping:**

| FilterPredicate Field | K8s Push-Down? | Mechanism |
|-----------------------|---------------|-----------|
| `metadata.labels` | Yes | `LabelSelector` |
| `metadata.name` | Yes | `FieldSelector` |
| `metadata.namespace` | Yes | Namespace parameter |
| `spec.nodeName` | Yes (Pods) | `FieldSelector` |
| `status.phase` | Yes (Pods) | `FieldSelector` |
| `metadata.annotations` | No | Client-side post-filter |
| `status.containerStatuses.restartCount` | No | Client-side post-filter |
| `metadata.creationTimestamp` (with gt/lt) | No | Client-side post-filter |

### 4.3 AWS Example

```go
func (r *EC2InstanceResourcer) Find(ctx context.Context, client *ec2.Client,
    meta ResourceMeta, input FindInput) (*FindResult, error) {

    awsInput := &ec2.DescribeInstancesInput{}
    var filters []ec2types.Filter

    if input.Filters != nil {
        for _, p := range input.Filters.Predicates {
            switch p.Field {
            case "State":
                if p.Operator == OpIn {
                    filters = append(filters, ec2types.Filter{
                        Name:   aws.String("instance-state-name"),
                        Values: p.Value.([]string),
                    })
                }
            case "metadata.tags":
                if p.Operator == OpEqual {
                    // tag:Key=Value → AWS tag filter
                    kv := p.Value.(string) // "environment=production"
                    parts := strings.SplitN(kv, "=", 2)
                    filters = append(filters, ec2types.Filter{
                        Name:   aws.String("tag:" + parts[0]),
                        Values: []string{parts[1]},
                    })
                }
            case "InstanceType":
                if p.Operator == OpIn {
                    filters = append(filters, ec2types.Filter{
                        Name:   aws.String("instance-type"),
                        Values: p.Value.([]string),
                    })
                }
            }
        }
    }

    if len(filters) > 0 {
        awsInput.Filters = filters
    }

    // Cursor pagination → AWS NextToken
    if input.Pagination != nil && input.Pagination.Cursor != "" {
        awsInput.NextToken = aws.String(input.Pagination.Cursor)
    }

    result, err := client.DescribeInstances(ctx, awsInput)
    // ... build FindResult with NextCursor = result.NextToken
}
```

### 4.4 SQL Example

```go
func (r *UserResourcer) Find(ctx context.Context, db *sql.DB,
    meta ResourceMeta, input FindInput) (*FindResult, error) {

    query := "SELECT * FROM users WHERE 1=1"
    var args []interface{}

    if input.Filters != nil {
        for _, p := range input.Filters.Predicates {
            switch p.Operator {
            case OpEqual:
                query += fmt.Sprintf(" AND %s = ?", sanitizeColumn(p.Field))
                args = append(args, p.Value)
            case OpNotEqual:
                query += fmt.Sprintf(" AND %s != ?", sanitizeColumn(p.Field))
                args = append(args, p.Value)
            case OpContains:
                query += fmt.Sprintf(" AND %s LIKE ?", sanitizeColumn(p.Field))
                args = append(args, "%"+p.Value.(string)+"%")
            case OpIn:
                placeholders := strings.Repeat("?,", len(p.Value.([]string)))
                query += fmt.Sprintf(" AND %s IN (%s)", sanitizeColumn(p.Field),
                    strings.TrimRight(placeholders, ","))
                for _, v := range p.Value.([]string) {
                    args = append(args, v)
                }
            case OpGreaterThan:
                query += fmt.Sprintf(" AND %s > ?", sanitizeColumn(p.Field))
                args = append(args, p.Value)
            }
        }
    }

    // Page-based pagination
    if input.Pagination != nil && input.Pagination.PageSize > 0 {
        offset := (input.Pagination.Page - 1) * input.Pagination.PageSize
        query += fmt.Sprintf(" LIMIT %d OFFSET %d", input.Pagination.PageSize, offset)
    }

    rows, err := db.QueryContext(ctx, query, args...)
    // ...
}
```

---

## 5. Partial Push-Down Pattern

When a backend supports some but not all predicates natively, the Resourcer uses a
two-phase approach:

```
Phase 1: Push-down
  - Extract predicates the backend supports natively
  - Build backend query with only those predicates
  - Execute query → get partially-filtered results

Phase 2: Post-filter
  - Remaining predicates applied in-memory by the Resourcer
  - Iterate results, evaluate each remaining predicate
  - Return only fully-matching results
```

### 5.1 SDK Post-Filter Helper (Optional)

The SDK MAY provide a helper for the common post-filter pattern:

```go
// PostFilter applies FilterPredicates to in-memory results.
// Used by Resourcers that can't push all predicates to their backend.
//
// data is a slice of json.RawMessage (resource objects).
// predicates is the remaining predicates not handled by the backend.
// Returns only the objects matching all predicates.
func PostFilter(data []json.RawMessage, predicates []FilterPredicate) ([]json.RawMessage, error)
```

This is a convenience utility, not a core SDK requirement. It uses `gjson` or similar
to evaluate dot-path field access on JSON objects.

### 5.2 Push-Down Reporting (Future)

A future enhancement could have the Resourcer report which predicates were pushed down:

```go
type FindResult struct {
    Data       []json.RawMessage  `json:"data"`
    Pagination *PaginationResult  `json:"pagination,omitempty"`

    // Future: which predicates were applied server-side vs. client-side
    // PushedDown []string `json:"pushedDown,omitempty"` // field paths
    // PostFiltered []string `json:"postFiltered,omitempty"`
}
```

This helps AI agents understand result accuracy and completeness. Not in v1.

---

## 6. FilterExpression Evaluation Semantics

### 6.1 Logic

```
FilterExpression{Logic: "and"}
  → ALL predicates AND ALL groups must match

FilterExpression{Logic: "or"}
  → ANY predicate OR ANY group must match

Default: "and" (if Logic is empty)
```

### 6.2 Nesting

One level of nesting max:

```
Top-level expression:
  Logic: AND
  Predicates: [P1, P2]        ← leaf conditions
  Groups: [                    ← nested expressions
    {Logic: OR, Predicates: [P3, P4]},
    {Logic: OR, Predicates: [P5, P6]},
  ]

Evaluates as: P1 AND P2 AND (P3 OR P4) AND (P5 OR P6)
```

Groups MUST NOT contain nested Groups — one level only. The SDK rejects deeper nesting.

### 6.3 Empty Expression

- `nil` Filters → no filtering, equivalent to List
- Empty `Predicates` and empty `Groups` → no filtering
- `Predicates: [P1]` with no Groups → just P1

---

## 7. Error Handling

### 7.1 SDK Validation Errors

Returned before calling the Resourcer:

```go
ResourceOperationError{
    Code:    "INVALID_FILTER",
    Title:   "Invalid Filter",
    Message: "unknown filter field: spec.containers",
    Suggestions: []string{
        "Valid fields: metadata.name, metadata.namespace, status.phase, ...",
        "Use GetFilterFields() to discover available filter fields",
    },
}
```

### 7.2 Backend Errors

The Resourcer returns errors from its backend:

```go
ResourceOperationError{
    Code:    "BACKEND_QUERY_FAILED",
    Title:   "K8s API Error",
    Message: "field selector not supported: spec.containers.image",
    Suggestions: []string{
        "This field cannot be used as a field selector in Kubernetes",
        "Use label selectors or client-side filtering instead",
    },
}
```

### 7.3 Partial Results

If a backend returns partial results with errors (e.g., timeout mid-page):

```go
FindResult{
    Data: partialResults,
    Pagination: &PaginationResult{
        NextCursor: "continue-token",
        Total:      -1, // unknown total due to partial results
    },
    // Error in response indicates partial results
}
```

---

## 8. Performance Considerations

### 8.1 Filter Field Caching

`FilterableProvider.FilterFields()` is called once per resource type registration and cached.
Connection-dependent fields are refreshed when connections change.

```
Registration:
  resourcer.FilterFields(ctx, "") → cache as default fields

Connection established:
  resourcer.FilterFields(ctx, connID) → cache per-connection fields
  (only if fields differ from default — e.g., CRD custom columns)
```

### 8.2 Query Planning Hints

`ScaleHint` (doc 13) helps callers choose the right query strategy:

| ScaleCategory | Recommended Strategy |
|--------------|---------------------|
| `few` (<100) | `List()` all, filter client-side |
| `moderate` (100-10K) | `Find()` with filters, paginate |
| `many` (10K+) | `Find()` with filters required, small page size |

The MCP server uses these hints in tool descriptions to guide AI agents.

### 8.3 No SDK-Side Caching of Query Results

The SDK does not cache Find/List results. Each call goes to the backend.
Caching is the engine's or frontend's responsibility (React Query on frontend,
potential future cache in engine controller).

---

## 9. Relationship to Other Documents

| Doc | Relationship |
|-----|-------------|
| 09 | SDK interface design — defines `Resourcer.Find()`, `FindInput`, `FindResult` |
| 13 | Filter type system — `FilterPredicate`, `FilterExpression`, `FilterField`, `FilterableProvider` |
| 14 | MCP server — consumes Find() and translates MCP tool calls to FindInput |
| 16 | NL-to-filter — AI agent translates natural language to FilterExpression using FilterField declarations |
| 17 | Cross-resource queries — engine routes Find() across plugins |
