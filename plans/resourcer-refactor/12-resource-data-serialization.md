# Resource Data Serialization — Efficiency Analysis

## 1. Motivation

Resource data (K8s Pods, Deployments, etc.) is the highest-volume payload in the system. Every
List response carries N full resource objects. Every informer ADD/UPDATE/DELETE event carries
1–2 full resource objects. These objects are large — a typical K8s Pod is 200+ fields deep.

This document traces every serialization step from K8s API to frontend React component,
identifies unnecessary deep copies, and recommends optimizations for the new SDK design (doc 09).

**Key finding:** Resource data currently goes through **4 full deep copies** between the plugin
process and the frontend. Two of these (`structpb.NewStruct()` and `.AsMap()`) are entirely
eliminable by changing the gRPC wire format.

---

## 2. Current Serialization Pipeline

### 2.1 Overview

```
K8s API (JSON bytes)
  → [Step 1] runtime.Decode() → *unstructured.Unstructured → .Object field
    = map[string]interface{}  (JSON parse, fresh allocation)

  → [Step 2] Wrap in GetResult/ListResult struct
    = Pure struct field assignment (zero cost)

  → [Step 3] gRPC server: structpb.NewStruct(map)    ← DEEP COPY #1
    = Full recursive walk, every value wrapped in *structpb.Value

  → [Step 4] Proto binary serialization over gRPC wire
    = Standard protobuf encoding (necessary — cross-process boundary)

  → [Step 5] gRPC client: structpb.Struct.AsMap()     ← DEEP COPY #2
    = Full recursive unwrap, new map[string]interface{} tree

  → [Step 6] Engine controller: pass-through
    = No transformation (pointer forwarding + runtime.EventsEmit)

  → [Step 7] Wails IPC: encoding/json marshal → JSON.parse()  ← DEEP COPY #3 + #4
    = Go reflection-based JSON marshal, JS JSON parse
```

### 2.2 Per-Step Detail

#### Step 1: K8s API → `map[string]interface{}`

**Source files:**
- `plugins/kubernetes/pkg/plugin/resource/resourcers/base.go:100,127,144`
- `plugins/kubernetes/pkg/plugin/resource/resourcers/pattern_resourcer.go:45-54,127-145`

**What happens:**

For **pattern_resourcer Get** (line 127-145):
```go
retBytes, err := result.Raw()                                    // []byte from K8s REST API
uncastObj, err := runtime.Decode(unstructured.UnstructuredJSONScheme, retBytes)
obj, ok := uncastObj.(*unstructured.Unstructured)
return &pkgtypes.GetResult{Success: true, Result: obj.Object}, nil
```
Full JSON parse from bytes — every value becomes a Go native type (`string`, `float64`, `bool`,
`map[string]interface{}`, `[]interface{}`). Fresh allocation.

For **base.go List** (informer synced, line 136-151):
```go
lister := client.DynamicInformerFactory.ForResource(s.GroupVersionResource()).Lister()
resources, err := lister.List(labels.Everything())
for _, r := range resources {
    obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(r)
    result = append(result, obj)
}
```
`ToUnstructured()` on `*unstructured.Unstructured` simply returns `.Object` — the backing
`map[string]interface{}` that the informer cache already holds. **Not a deep copy.**

For **base.go List** (not yet synced, line 116-133):
```go
resources, err := client.DynamicClient.Resource(s.GroupVersionResource()).List(ctx.Context, v1.ListOptions{})
for _, r := range resources.Items {
    p := r
    obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&p)
    result = append(result, obj)
}
```
`p := r` copies the struct value, but the inner `map[string]interface{}` is a reference type —
the backing map is shared. `ToUnstructured(&p)` returns `p.Object` (same backing map).

For **pattern_resourcer parseList** (line 45-54):
```go
for _, r := range list.Items {
    obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&r)
    result = append(result, obj)
}
```
Same as above — returns `.Object` reference.

**Cost:** One JSON parse for REST API calls (unavoidable). Zero-cost for informer-cached data.

#### Step 2: Wrap in SDK Result Types

**Source file:** `plugin-sdk/pkg/resource/types/operation.go:128-148`

```go
type GetResult struct {
    Result  map[string]interface{} `json:"result"`
    Success bool                   `json:"success"`
}

type ListResult struct {
    Result     []map[string]interface{} `json:"result"`
    Success    bool                     `json:"success"`
    Pagination PaginationResult         `json:"pagination"`
}
```

**Cost:** Zero — pure struct field assignment. The `map[string]interface{}` is placed by reference.

#### Step 3: gRPC Server — `map[string]interface{}` → `*structpb.Struct`

**Source file:** `plugin-sdk/pkg/resource/plugin/resource_server.go`

For **List** (line 311-322):
```go
data := make([]*structpb.Struct, 0, len(resp.Result))
for _, item := range resp.Result {
    dataItem, err := structpb.NewStruct(item)
    data = append(data, dataItem)
}
```

For **informer ADD** (line 538-554):
```go
data, err := structpb.NewStruct(event.Data)
```

For **informer UPDATE** (line 557-577):
```go
olddata, err := structpb.NewStruct(event.OldData)
newdata, err := structpb.NewStruct(event.NewData)
```

**What `structpb.NewStruct()` does:** Full recursive walk of every key/value in the map tree.
Each value is wrapped in a new `*structpb.Value` struct:
- `nil` → `NullValue`
- `bool` → `BoolValue`
- numeric types → `NumberValue` (all become `float64`)
- `string` → `StringValue`
- `[]interface{}` → `ListValue` (recursive)
- `map[string]interface{}` → `StructValue` (recursive)

**Cost:** Full deep copy. For a typical K8s Pod (~200 fields, ~4 levels deep), this allocates
hundreds of Go objects. For a List of 100 Pods, 100 × full deep copy.

**Failure mode:** `structpb.NewStruct()` returns an error if any value is not a JSON-compatible
primitive. K8s unstructured guarantees this, but custom plugins must be careful.

#### Step 4: Proto Binary Serialization

**Source files:**
- `plugin-sdk/proto/resource.proto:41-44`
- `plugin-sdk/proto/resource_informer.proto:81-92`

```protobuf
message ListResponse {
  bool success = 1;
  repeated google.protobuf.Struct data = 2;
  ResourceError error = 3;
}

message InformerAddEvent {
  google.protobuf.Struct data = 1;
}

message InformerUpdateEvent {
  google.protobuf.Struct old_data = 1;
  google.protobuf.Struct new_data = 2;
}
```

**Cost:** Standard protobuf binary encoding/decoding. Necessary — this is the cross-process boundary.

#### Step 5: gRPC Client — `*structpb.Struct` → `map[string]interface{}`

**Source file:** `plugin-sdk/pkg/resource/plugin/resource_client.go`

For **List** (line 315-319):
```go
data := resp.GetData()
res := make([]map[string]interface{}, 0, len(data))
for _, d := range data {
    res = append(res, d.AsMap())
}
```

For **informer ADD** (line 526-534):
```go
addStream <- types.InformerAddPayload{
    Data: add.GetData().AsMap(),
}
```

**What `.AsMap()` does:** Full recursive unwrap — creates a new `map[string]interface{}`
tree from the `*structpb.Struct`. All `NumberValue` become `float64`.

**Cost:** Another full deep copy — the reverse of Step 3.

#### Step 6: Engine Controller — Pass-Through

**Source file:** `backend/pkg/plugin/resource/controller.go`

For **List** (line 713-731):
```go
result, err := client.List(ctx, key, input)
return result, err  // pointer pass-through
```

For **informer events** (line 185-217):
```go
case event := <-c.addChan:
    if !c.isSubscribed(event.PluginID, event.Connection, event.Key) {
        continue
    }
    eventKey := fmt.Sprintf("%s/%s/%s/ADD", event.PluginID, event.Connection, event.Key)
    runtime.EventsEmit(c.ctx, eventKey, event)
```

**Cost:** Zero transformation. The controller is purely a routing/lifecycle layer. It adds
`PluginID` to informer event structs (single field mutation) and gates events by subscription
status.

#### Step 7: Wails IPC — Go → JavaScript

**What Wails does internally:**

For **synchronous RPC** (List, Get via `resource/Client.js`):
1. Go `*ListResult` → `encoding/json.Marshal()` → JSON bytes (**deep copy #3**)
2. JSON bytes → IPC bridge → JavaScript `JSON.parse()` (**deep copy #4**)

For **events** (`runtime.EventsEmit` for informer ADD/UPDATE/DELETE):
1. Go `InformerAddPayload` struct → `encoding/json.Marshal()` → JSON bytes
2. JSON bytes → IPC bridge → JavaScript `JSON.parse()`

**Cost:** Two more serialization steps (marshal + parse). Unavoidable with current Wails architecture.

### 2.3 Informer Event Path (Separate from CRUD)

The informer event path has a slightly different Step 1:

```
K8s informer cache
  → [Step 1'] r.Object (direct reference to cache map)     ← NO COPY
  → [Step 2'] Wrap in InformerAddPayload{Data: r.Object}   ← NO COPY
  → [Step 3'] structpb.NewStruct(event.Data)                ← DEEP COPY #1
  → [Step 4'] Proto binary over gRPC
  → [Step 5'] structpb.Struct.AsMap()                       ← DEEP COPY #2
  → [Step 6'] Engine controller pass-through
  → [Step 7'] encoding/json.Marshal → JSON.parse()          ← DEEP COPY #3 + #4
```

**Source:** `plugins/kubernetes/pkg/plugin/resource/informer.go:115-176`

```go
AddFunc: func(obj any) {
    r, ok := obj.(*unstructured.Unstructured)
    addChan <- types.InformerAddPayload{
        Data: r.Object,  // direct reference to informer cache
    }
}
```

The K8s informer never mutates existing cached objects (it creates new objects for updates),
so passing `r.Object` by reference is safe.

**UPDATE events send both objects:**
```go
UpdateFunc: func(oldObj, obj any) {
    updateChan <- types.InformerUpdatePayload{
        OldData: orig.Object,     // reference to old cached object
        NewData: updated.Object,  // reference to new cached object
    }
}
```

This means UPDATE events go through `structpb.NewStruct()` **twice** at Step 3 — once for
OldData, once for NewData. And `.AsMap()` **twice** at Step 5. And `encoding/json.Marshal()`
serializes **both** at Step 7.

---

## 3. Identified Inefficiencies

### INE-1: `structpb.NewStruct()` / `.AsMap()` Round-Trip (Critical)

| Metric | Value |
|--------|-------|
| **Steps** | 3 → 5 |
| **Cost per object** | 2 full recursive tree walks + 2 full allocations |
| **Per List(100 items)** | 200 deep copies |
| **Per UPDATE event** | 4 deep copies (2 objects × 2 walks each) |
| **Why it exists** | `google.protobuf.Struct` is proto's only type for arbitrary JSON-like data |
| **Engine inspects data?** | No — pure pass-through. No field-level access needed. |

The `structpb` round-trip exists solely because the proto definitions use `google.protobuf.Struct`
for resource data fields. The engine controller never inspects the data — it just forwards it.
This means the typed proto representation provides zero value and costs two full deep copies.

### INE-2: UPDATE Events Send Both OldData and NewData

| Metric | Value |
|--------|-------|
| **Steps** | 3 (×2), 5 (×2), 7 (×2) |
| **Cost** | Double the serialization + network cost of ADD/DELETE events |
| **Frontend uses OldData?** | No — `useResources.ts:onResourceUpdate` replaces item by index, only uses `newData` |
| **OldData used anywhere?** | No — engine controller passes it through unused |

The `InformerUpdatePayload.OldData` field and the `InformerUpdateEvent.old_data` proto field
are dead data. They're serialized, transmitted, deserialized, and then discarded.

**Frontend handler** (`packages/omniviewdev-runtime/src/hooks/resource/useResources.ts`):
```typescript
const onResourceUpdate = React.useCallback((update: UpdatePayload) => {
    queryClient.setQueryData(queryKey, (oldData: types.ListResult) => {
        return produce(oldData, (draft) => {
            const index = draft.result.findIndex(
                item => get(update.newData, idAccessor) === get(item, idAccessor)
            );
            if (index !== -1) {
                draft.result[index] = update.newData;  // Only newData used
            }
        });
    });
}, ...);
```

### INE-3: Wails `encoding/json` Marshal of `map[string]interface{}` (Moderate)

| Metric | Value |
|--------|-------|
| **Step** | 7 |
| **Cost** | Reflection-based JSON marshal of entire object tree |
| **Avoidable?** | Partially — if data arrives as pre-serialized JSON bytes, could use `json.RawMessage` |

`encoding/json.Marshal` on `map[string]interface{}` uses reflection to walk the tree and
format each value. If the data were already JSON bytes (from the gRPC layer), this step
could potentially be skipped by storing the data as `json.RawMessage` (which `encoding/json`
passes through without re-serializing).

**Caveat:** Wails must support `json.RawMessage` in its IPC layer. This needs verification.

### INE-4: `ToUnstructured()` on Already-Unstructured Items (Cosmetic)

| Metric | Value |
|--------|-------|
| **Steps** | 1a, 1c |
| **Files** | `base.go:100,127,144`, `pattern_resourcer.go:48` |
| **Actual cost** | Near-zero — `ToUnstructured()` on `*unstructured.Unstructured` returns `.Object` directly |
| **Apparent cost** | Looks expensive (the function name suggests conversion) |

This is not a real performance issue — `runtime.DefaultUnstructuredConverter.ToUnstructured()`
has a fast path for `*unstructured.Unstructured` that just returns the `.Object` field. But the
code is semantically misleading. Using `r.Object` directly would be clearer.

---

## 4. Optimization Recommendations

### 4.1 Option A: JSON Bytes on the Wire (Recommended)

**Change:** Replace `google.protobuf.Struct` with `bytes` in all proto definitions for resource
data fields.

**New pipeline:**
```
map[string]interface{}
  → [Step 3'] json.Marshal(map)           ← Single JSON encode (replaces structpb.NewStruct)
  → bytes on gRPC wire
  → [Step 5'] Store as json.RawMessage    ← Zero-copy (replaces structpb.AsMap)
  → [Step 7'] Wails passes RawMessage     ← Potentially zero re-serialization
```

**Proto changes:**
```protobuf
// Before
message ListResponse {
  bool success = 1;
  repeated google.protobuf.Struct data = 2;
}
message InformerAddEvent {
  google.protobuf.Struct data = 1;
}

// After
message ListResponse {
  bool success = 1;
  repeated bytes data = 2;    // pre-serialized JSON
}
message InformerAddEvent {
  bytes data = 1;             // pre-serialized JSON
}
```

**Go type changes:**
```go
// Before
type GetResult struct {
    Result  map[string]interface{} `json:"result"`
    Success bool                   `json:"success"`
}

// After
type GetResult struct {
    Result  json.RawMessage `json:"result"`    // pre-serialized JSON bytes
    Success bool            `json:"success"`
}
```

**Savings:**
- Eliminates `structpb.NewStruct()` (Step 3) — replaced by `json.Marshal()` which is faster
  and produces directly usable output
- Eliminates `.AsMap()` (Step 5) — `bytes` from proto are stored as `json.RawMessage`
- Potentially eliminates Wails re-serialization (Step 7) — `json.RawMessage` implements
  `json.Marshaler` and returns itself without re-encoding

**Trade-offs:**
- Engine cannot inspect individual fields of resource data without deserializing
- Engine currently never does this (pure pass-through) — no functional loss
- Plugin-side CRUD methods still work with `map[string]interface{}` internally —
  `json.Marshal` is called at the gRPC boundary in the server stub

**Impact on SDK interfaces:**

The **plugin-author** interfaces (`Resourcer[ClientT]`) are unaffected — they still return
`map[string]interface{}`. The serialization change happens in the SDK's internal gRPC server
stub, which converts `map[string]interface{}` → `json.RawMessage` before sending.

The **gRPC boundary** interfaces (`Provider` in doc 09 §4.7) would change result types:
```go
// Before
type OperationProvider interface {
    List(ctx context.Context, key string, input ListInput) (*ListResult, error)
}
// ListResult.Result is []map[string]interface{}

// After — engine side
type OperationProvider interface {
    List(ctx context.Context, key string, input ListInput) (*ListResult, error)
}
// ListResult.Result is []json.RawMessage (engine never deserializes)
```

### 4.2 Option B: Drop OldData from UPDATE Events (Recommended)

**Change:** `InformerUpdatePayload` carries only `NewData`. Drop `OldData` field.

```go
// Before
type InformerUpdatePayload struct {
    OldData    map[string]interface{} `json:"oldData"`
    NewData    map[string]interface{} `json:"newData"`
    // ...
}

// After
type InformerUpdatePayload struct {
    Data       map[string]interface{} `json:"data"`    // the updated resource (was NewData)
    // ...
}
```

```protobuf
// Before
message InformerUpdateEvent {
  google.protobuf.Struct old_data = 1;
  google.protobuf.Struct new_data = 2;
}

// After
message InformerUpdateEvent {
  bytes data = 1;    // only the new state (combined with Option A)
}
```

**Savings:**
- 50% reduction in UPDATE event serialization cost
- 50% reduction in UPDATE event network traffic
- UPDATE events become same cost as ADD/DELETE events

**Trade-offs:**
- Future features needing OldData (diff views, audit trails) would need a separate mechanism
- Could add an optional `WithOldData` flag on the watch subscription if needed later

### 4.3 Option C: Batch List Serialization (Subsumed by Option A)

If Option A is adopted, List responses naturally serialize as `repeated bytes` — each item is
independently marshaled to JSON. This is already batch-friendly since proto handles the framing.

No additional work needed beyond Option A.

### 4.4 Option D: Direct `.Object` Access (Cosmetic, Recommended)

**Change:** In K8s plugin resourcers, replace `ToUnstructured()` calls with direct `.Object` access.

```go
// Before (base.go)
obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(r)
result = append(result, obj)

// After
u, ok := r.(*unstructured.Unstructured)
if !ok { return nil, fmt.Errorf("unexpected type %T", r) }
result = append(result, u.Object)
```

**Savings:** Minimal performance gain (ToUnstructured is already a no-op for unstructured types).
Main benefit is code clarity — readers don't wonder about the conversion cost.

---

## 5. Proto Changes Required

If Options A + B are adopted, the following proto messages change:

### `resource.proto`

```protobuf
// Get
message GetResponse {
  bool success = 1;
  bytes data = 2;              // was: google.protobuf.Struct data
  ResourceError error = 3;
}

// List
message ListResponse {
  bool success = 1;
  repeated bytes data = 2;     // was: repeated google.protobuf.Struct data
  ResourceError error = 3;
}

// Find
message FindResponse {
  bool success = 1;
  repeated bytes data = 2;     // was: repeated google.protobuf.Struct data
  ResourceError error = 3;
}

// Create
message CreateRequest {
  string key = 1;
  string context = 2;
  string id = 3;
  string namespace = 4;
  bytes data = 5;              // was: google.protobuf.Struct data
}
message CreateResponse {
  bool success = 1;
  bytes data = 2;              // was: google.protobuf.Struct data
  ResourceError error = 3;
}

// Update
message UpdateRequest {
  string key = 1;
  string context = 2;
  string id = 3;
  string namespace = 4;
  bytes data = 5;              // was: google.protobuf.Struct data
}
message UpdateResponse {
  bool success = 1;
  bytes data = 2;              // was: google.protobuf.Struct data
  ResourceError error = 3;
}

// Delete
message DeleteResponse {
  bool success = 1;
  bytes data = 2;              // was: google.protobuf.Struct data
  ResourceError error = 3;
}
```

### `resource_informer.proto`

```protobuf
message InformerAddEvent {
  bytes data = 1;              // was: google.protobuf.Struct data
}

message InformerUpdateEvent {
  bytes data = 1;              // was: old_data + new_data (2 Structs)
  // old_data dropped — frontend doesn't use it
}

message InformerDeleteEvent {
  bytes data = 1;              // was: google.protobuf.Struct data
}
```

### Removed import

`google/protobuf/struct.proto` is no longer needed in `resource.proto` or `resource_informer.proto`
(assuming no other messages use it — check `resource_common.proto` and `resource_connection.proto`).

---

## 6. SDK Interface Implications

### 6.1 Plugin-Author Interfaces (Unchanged)

`Resourcer[ClientT]` methods still return `map[string]interface{}` in their result types.
Plugin authors are unaffected — they work with maps internally.

The `WatchEventSink` (doc 09 §4.4) still receives `map[string]interface{}` in its payloads.
Plugin authors push maps into the sink; the SDK serializes to JSON bytes internally.

### 6.2 gRPC Boundary Types (Changed)

The SDK's internal gRPC server stub changes:
```go
// Before (resource_server.go)
dataItem, err := structpb.NewStruct(item)

// After
dataBytes, err := json.Marshal(item)
```

The SDK's internal gRPC client stub changes:
```go
// Before (resource_client.go)
res = append(res, d.AsMap())

// After
res = append(res, json.RawMessage(d))  // d is []byte from proto
```

### 6.3 Engine-Side Result Types (Changed)

The engine controller's `ListResult`, `GetResult`, etc. hold `json.RawMessage` instead of
`map[string]interface{}`. Since the controller is a pass-through, this is transparent —
it never inspects the data.

The Wails binding layer receives `json.RawMessage` and Wails' `encoding/json.Marshal` passes
it through without re-encoding (since `json.RawMessage` implements `json.Marshaler`).

### 6.4 Frontend Types (Unchanged)

The frontend receives the same JSON structure via Wails IPC. `JSON.parse()` produces the same
JavaScript objects. No frontend changes needed.

### 6.5 WatchEventSink Payload Types (Changed)

```go
// Before (doc 09 current)
type WatchAddPayload struct {
    Data map[string]interface{}
    // ...
}
type WatchUpdatePayload struct {
    OldData map[string]interface{}
    NewData map[string]interface{}
    // ...
}

// After
type WatchAddPayload struct {
    Data map[string]interface{}    // unchanged — plugin side still uses maps
    // ...
}
type WatchUpdatePayload struct {
    Data map[string]interface{}    // was NewData; OldData dropped
    // ...
}
```

The `WatchEventSink` interface itself is unchanged — it still has `OnAdd`, `OnUpdate`, `OnDelete`.
The payload struct for `OnUpdate` just loses the `OldData` field.

---

## 7. Performance Test Additions

The following tests should be added to the test spec (doc 11) to verify serialization efficiency:

### 7.1 Serialization Boundary Tests

| ID | Test | Assertion |
|----|------|-----------|
| SER-001 | List(100 items) result type is `[]json.RawMessage` | Engine receives raw bytes, not deserialized maps |
| SER-002 | Get result type is `json.RawMessage` | Same as above for single item |
| SER-003 | Informer ADD event data is `json.RawMessage` at engine | No intermediate `map[string]interface{}` |
| SER-004 | Informer UPDATE event has no OldData field | Only `Data` (new state) is present |
| SER-005 | `json.RawMessage` passes through Wails `encoding/json.Marshal` without re-encoding | Verify with byte comparison |

### 7.2 Performance Invariant Tests

| ID | Test | Assertion |
|----|------|-----------|
| SER-006 | No `structpb` import in new SDK server/client code | Grep for `structpb` in new implementation |
| SER-007 | Resource data crosses exactly 2 serialization boundaries | `json.Marshal` in plugin → `JSON.parse` in frontend |
| SER-008 | List(1000 items) completes in <100ms at gRPC boundary | Benchmark: `json.Marshal` 1000 maps → proto encode → receive |
| SER-009 | Informer throughput ≥5000 events/sec at gRPC boundary | Benchmark with pre-built event payloads |

### 7.3 Backward Compatibility (Negative Tests)

| ID | Test | Assertion |
|----|------|-----------|
| SER-010 | `structpb.NewStruct()` is NOT called anywhere in new code | No regression to old serialization path |
| SER-011 | `structpb.Struct.AsMap()` is NOT called anywhere in new code | Same |
| SER-012 | Proto definitions contain no `google.protobuf.Struct` for resource data | Check `.proto` files |

---

## 8. Summary

### Recommended Optimizations

| Option | Priority | Savings | Complexity |
|--------|----------|---------|------------|
| **A: JSON bytes on wire** | P0 | Eliminates 2 of 4 deep copies | Medium — proto + server/client stub changes |
| **B: Drop OldData from UPDATE** | P0 | Halves UPDATE event cost | Low — remove field + update handlers |
| **D: Direct `.Object` access** | P2 | Cosmetic — code clarity | Low — simple substitution |

### Net Effect

| Metric | Before | After |
|--------|--------|-------|
| Deep copies per object (CRUD) | 4 | 2 |
| Deep copies per ADD event | 4 | 2 |
| Deep copies per UPDATE event | 8 (2 objects × 4) | 2 (1 object × 2) |
| `structpb` allocations | Hundreds per object | 0 |
| Engine-side deserialization | Full `map[string]interface{}` | Zero (raw bytes pass-through) |

The two remaining deep copies (Steps 3' and 7: `json.Marshal` + `JSON.parse`) are fundamental
to the architecture — data must be serialized to cross the process boundary and must be
deserialized for JavaScript to use it. These cannot be eliminated without architectural changes
to Wails or gRPC.
