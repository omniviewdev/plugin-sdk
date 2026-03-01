# 21: v1 Protobuf Message Definitions

This document defines the complete protobuf3 schema for the v1 SDK protocol. Every RPC
method in the `ResourcePlugin` service (doc 20 §7.1) has its request and response messages
defined here. All types from docs 09, 13, 18, and 19 are mapped to proto messages.

**Scope:** Reference document. These are the authoritative proto definitions for v1.
Implementation creates `.proto` files from these definitions.

**Package:** `omniview.sdk.resource.v1`

---

## 1. Proto File Organization

| File | Package | Contents |
|------|---------|----------|
| `common.proto` | `omniview.sdk.common.v1` | Shared types used across services (Connection, ResourceMeta, ResourceError) |
| `resource.proto` | `omniview.sdk.resource.v1` | Service definition (27 RPCs), CRUD messages, type info, actions |
| `filter.proto` | `omniview.sdk.resource.v1` | Filter/query types (FilterField, FilterPredicate, FilterExpression) |
| `watch.proto` | `omniview.sdk.resource.v1` | Watch event types (WatchEvent oneof, state events) |
| `relationship.proto` | `omniview.sdk.resource.v1` | Relationship graph types (descriptors, extractors, dependency trees) |
| `health.proto` | `omniview.sdk.resource.v1` | Health & diagnostic types (ResourceHealth, DiagnosticContext, ImpactReport) |

**Imports:** `resource.proto` imports all other files. `relationship.proto` and `health.proto`
import `common.proto`. All proto files import `google/protobuf/timestamp.proto` and
`google/protobuf/struct.proto` as needed.

---

## 2. common.proto — Shared Types

```protobuf
syntax = "proto3";
package omniview.sdk.common.v1;

option go_package = "github.com/omniviewdev/plugin-sdk/proto/v1/commonpb";

import "google/protobuf/struct.proto";
import "google/protobuf/timestamp.proto";

// Connection represents a configured connection to a backend system.
// K8s: one Connection per kubeconfig context.
// AWS: one Connection per region + profile.
message Connection {
    string id = 1;                              // unique identifier (e.g., kubeconfig context name)
    string name = 2;                            // human-readable display name
    string description = 3;                     // optional description
    string avatar = 4;                          // avatar URL or icon name
    map<string, string> labels = 5;             // metadata labels (cloud provider, region, etc.)
    google.protobuf.Struct data = 6;            // arbitrary connection data (kubeconfig path, etc.)
}

// ConnectionStatus reports the health of a connection.
message ConnectionStatus {
    Connection connection = 1;
    ConnectionState state = 2;
    string message = 3;                         // human-readable status message
    string error = 4;                           // error message if state is ERROR
    google.protobuf.Struct metadata = 5;        // extra metadata (K8s version, node count, etc.)
}

enum ConnectionState {
    CONNECTION_STATE_UNSPECIFIED = 0;
    CONNECTION_STATE_CONNECTED = 1;
    CONNECTION_STATE_DISCONNECTED = 2;
    CONNECTION_STATE_RECONNECTING = 3;
    CONNECTION_STATE_ERROR = 4;
}

// ResourceMeta uniquely identifies a resource type within a plugin.
message ResourceMeta {
    string group = 1;                           // API group: "core", "apps", "ec2"
    string version = 2;                         // API version: "v1", "v1beta1"
    string kind = 3;                            // resource kind: "Pod", "Deployment", "Instance"
    string singular = 4;                        // singular form: "pod" (optional, derived from kind)
    string plural = 5;                          // plural form: "pods" (optional, derived from kind)
    string display_name = 6;                    // human-friendly name: "Kubernetes Pod"
    string description = 7;                     // resource type description
    repeated string short_names = 8;            // aliases: ["po"] for Pod
}

// ResourceGroup is a logical grouping of resource types for UI rendering.
message ResourceGroup {
    string id = 1;                              // group identifier
    string name = 2;                            // display name
    string icon = 3;                            // icon name or URL
    string description = 4;
    repeated ResourceMeta resources = 5;        // resource types in this group
}

// ResourceDefinition provides UI rendering metadata for a resource type.
message ResourceDefinition {
    ResourceMeta meta = 1;
    string id_accessor = 2;                     // JSON path to resource ID: "metadata.name"
    string namespace_accessor = 3;              // JSON path to namespace: "metadata.namespace" (empty = cluster-scoped)
    string memoizer_accessor = 4;               // JSON path for memoization key: "metadata.resourceVersion"
    repeated ColumnDefinition column_definitions = 5;
    repeated OperationType supported_operations = 6;
}

// ColumnDefinition describes a table column for UI rendering.
message ColumnDefinition {
    string id = 1;                              // column identifier
    string header = 2;                          // column header text
    repeated string accessors = 3;              // JSON paths to extract value(s)
    ColumnType type = 4;
    ColumnAlignment alignment = 5;
    int32 width = 6;                            // width in pixels (0 = auto)
    string formatter = 7;                       // formatter name: "age", "bytes", "status"
    ResourceLink resource_link = 8;             // optional link to another resource type
    bool sortable = 9;
    bool hidden = 10;                           // hidden by default
}

enum ColumnType {
    COLUMN_TYPE_UNSPECIFIED = 0;
    COLUMN_TYPE_STRING = 1;
    COLUMN_TYPE_NUMBER = 2;
    COLUMN_TYPE_DATE = 3;
    COLUMN_TYPE_BOOLEAN = 4;
    COLUMN_TYPE_ENUM = 5;
    COLUMN_TYPE_PROGRESS = 6;
    COLUMN_TYPE_STATUS = 7;
    COLUMN_TYPE_ARRAY = 8;
    COLUMN_TYPE_OBJECT = 9;
}

enum ColumnAlignment {
    COLUMN_ALIGNMENT_UNSPECIFIED = 0;
    COLUMN_ALIGNMENT_LEFT = 1;
    COLUMN_ALIGNMENT_CENTER = 2;
    COLUMN_ALIGNMENT_RIGHT = 3;
}

// ResourceLink connects a column value to another resource type for navigation.
message ResourceLink {
    string id_accessor = 1;                     // JSON path to linked resource ID
    string namespace_accessor = 2;              // JSON path to linked resource namespace
    bool namespaced = 3;                        // whether linked resource is namespaced
    string key = 4;                             // static resource key (group::version::kind)
    string key_accessor = 5;                    // JSON path to resource key (for dynamic lookup)
    map<string, string> key_map = 6;            // value → resource key mapping
    map<string, string> detail_extractors = 7;  // extra fields to extract for display
    string display_id = 8;                      // JSON path for display name of linked resource
}

enum OperationType {
    OPERATION_TYPE_UNSPECIFIED = 0;
    OPERATION_TYPE_GET = 1;
    OPERATION_TYPE_LIST = 2;
    OPERATION_TYPE_FIND = 3;
    OPERATION_TYPE_CREATE = 4;
    OPERATION_TYPE_UPDATE = 5;
    OPERATION_TYPE_DELETE = 6;
}

// ResourceError is a structured error for resource operations.
message ResourceError {
    int32 code = 1;                             // error code
    string title = 2;                           // short error title
    string message = 3;                         // detailed error message
    repeated string suggestions = 4;            // actionable suggestions for the user
    google.protobuf.Struct details = 5;         // extra structured error data
}

// EditorSchema provides Monaco editor validation for a resource type.
message EditorSchema {
    string uri = 1;                             // schema URI
    repeated string file_match = 2;             // glob patterns: ["*.k8s.json"]
    string language = 3;                        // editor language mode: "yaml", "json"
    bytes schema = 4;                           // raw JSON Schema bytes
}
```

---

## 3. resource.proto — Service Definition, CRUD, Type Info, Actions

```protobuf
syntax = "proto3";
package omniview.sdk.resource.v1;

option go_package = "github.com/omniviewdev/plugin-sdk/proto/v1/resourcepb";

import "google/protobuf/empty.proto";
import "google/protobuf/struct.proto";
import "google/protobuf/timestamp.proto";
import "v1/common.proto";
import "v1/filter.proto";
import "v1/watch.proto";
import "v1/relationship.proto";
import "v1/health.proto";

// ─────────────────────────────────────────────────────────────────────
// Service Definition — 27 RPCs
// ─────────────────────────────────────────────────────────────────────

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

// ─────────────────────────────────────────────────────────────────────
// Connection Lifecycle
// ─────────────────────────────────────────────────────────────────────

message LoadConnectionsRequest {
    // Empty — plugin loads connections from its configured sources.
}

message LoadConnectionsResponse {
    repeated omniview.sdk.common.v1.Connection connections = 1;
}

message ConnectionRequest {
    string connection_id = 1;
}

message ConnectionStatusResponse {
    omniview.sdk.common.v1.ConnectionStatus status = 1;
}

message ConnectionResponse {
    omniview.sdk.common.v1.Connection connection = 1;
}

message NamespacesResponse {
    repeated string namespaces = 1;
}

// ─────────────────────────────────────────────────────────────────────
// CRUD Operations
// ─────────────────────────────────────────────────────────────────────

// -- Get --

message GetRequest {
    string resource_key = 1;                    // "group::version::kind"
    string connection_id = 2;
    string id = 3;                              // resource identifier
    string namespace = 4;                       // empty for cluster-scoped
}

message GetResponse {
    bytes data = 1;                             // resource as pre-serialized JSON (doc 12)
    omniview.sdk.common.v1.ResourceError error = 2;
}

// -- List --

message ListRequest {
    string resource_key = 1;
    string connection_id = 2;
    repeated string namespaces = 3;             // empty = all namespaces
    repeated OrderField order = 4;              // multi-field ordering
    PaginationParams pagination = 5;
}

message ListResponse {
    repeated bytes items = 1;                   // each item is pre-serialized JSON
    int32 total = 2;                            // total count (if known)
    string next_cursor = 3;                     // cursor for next page (empty = no more)
    omniview.sdk.common.v1.ResourceError error = 4;
}

// -- Find --

message FindRequest {
    string resource_key = 1;
    string connection_id = 2;
    repeated string namespaces = 3;
    FilterExpression filters = 4;               // structured filter predicates
    string text_query = 5;                      // free-text search (TextSearchProvider)
    repeated OrderField order = 6;
    PaginationParams pagination = 7;
}

message FindResponse {
    repeated bytes items = 1;
    int32 total = 2;
    string next_cursor = 3;
    omniview.sdk.common.v1.ResourceError error = 4;
}

// -- Create --

message CreateRequest {
    string resource_key = 1;
    string connection_id = 2;
    string namespace = 3;
    bytes data = 4;                             // resource to create as JSON
}

message CreateResponse {
    bytes data = 1;                             // created resource (with server-assigned fields)
    omniview.sdk.common.v1.ResourceError error = 2;
}

// -- Update --

message UpdateRequest {
    string resource_key = 1;
    string connection_id = 2;
    string id = 3;
    string namespace = 4;
    bytes data = 5;                             // updated resource data as JSON
}

message UpdateResponse {
    bytes data = 1;                             // updated resource (server state after update)
    omniview.sdk.common.v1.ResourceError error = 2;
}

// -- Delete --

message DeleteRequest {
    string resource_key = 1;
    string connection_id = 2;
    string id = 3;
    string namespace = 4;
    optional int32 grace_period_seconds = 5;    // 0 = immediate, -1 = backend default
}

message DeleteResponse {
    bool success = 1;
    omniview.sdk.common.v1.ResourceError error = 2;
}

// ─────────────────────────────────────────────────────────────────────
// Type Information
// ─────────────────────────────────────────────────────────────────────

message ResourceGroupsRequest {
    string connection_id = 1;
}

message ResourceGroupsResponse {
    // key = group ID, value = ResourceGroup
    map<string, omniview.sdk.common.v1.ResourceGroup> groups = 1;
}

message ResourceTypesRequest {
    string connection_id = 1;
}

message ResourceTypesResponse {
    // key = resource key (group::version::kind), value = ResourceMeta
    map<string, omniview.sdk.common.v1.ResourceMeta> types = 1;
}

message ResourceCapabilitiesRequest {
    string resource_key = 1;
}

message ResourceCapabilitiesResponse {
    ResourceCapabilities capabilities = 1;
}

// ResourceCapabilities — auto-derived by SDK from type assertions.
// Tells callers (MCP server, AI agents, frontend) what operations are supported.
message ResourceCapabilities {
    // CRUD flags
    bool can_get = 1;
    bool can_list = 2;
    bool can_find = 3;
    bool can_create = 4;
    bool can_update = 5;
    bool can_delete = 6;

    // Extended capabilities
    bool watchable = 7;                         // implements Watcher
    bool filterable = 8;                        // implements FilterableProvider
    bool searchable = 9;                        // implements TextSearchProvider
    bool has_actions = 10;                      // implements ActionResourcer
    bool has_schema = 11;                       // implements ResourceSchemaProvider
    bool namespace_scoped = 12;                 // resource has namespaces
    bool has_relationships = 13;                // implements RelationshipProvider
    bool has_health = 14;                       // implements HealthProvider
    bool has_events = 15;                       // implements EventProvider

    // Scale guidance
    ScaleHint scale_hint = 16;
}

message ScaleHint {
    ScaleCategory expected_count = 1;
    int32 default_page_size = 2;                // 0 = system default
}

enum ScaleCategory {
    SCALE_CATEGORY_UNSPECIFIED = 0;
    SCALE_CATEGORY_FEW = 1;                     // <100 (Namespaces, Nodes)
    SCALE_CATEGORY_MODERATE = 2;                // 100-10K (Deployments, Services)
    SCALE_CATEGORY_MANY = 3;                    // 10K+ (Pods, Events)
}

message FilterFieldsRequest {
    string connection_id = 1;
    string resource_key = 2;
}

message FilterFieldsResponse {
    repeated FilterField fields = 1;
}

message ResourceSchemaRequest {
    string connection_id = 1;
    string resource_key = 2;
}

message ResourceSchemaResponse {
    bytes schema = 1;                           // raw JSON Schema bytes (nil = no schema)
}

message EditorSchemasRequest {
    string connection_id = 1;
}

message EditorSchemasResponse {
    repeated omniview.sdk.common.v1.EditorSchema schemas = 1;
}

// ─────────────────────────────────────────────────────────────────────
// Actions
// ─────────────────────────────────────────────────────────────────────

message GetActionsRequest {
    string resource_key = 1;
    string connection_id = 2;
}

message GetActionsResponse {
    repeated ActionDescriptor actions = 1;
}

// ActionDescriptor describes an action on a resource type.
message ActionDescriptor {
    string id = 1;                              // unique action ID: "restart", "drain", "scale"
    string label = 2;                           // human-readable: "Restart Pod"
    string description = 3;                     // what the action does
    string icon = 4;                            // icon name or URL
    ActionScope scope = 5;
    bool streaming = 6;                         // true = StreamAction, false = ExecuteAction
    bytes params_schema = 7;                    // JSON Schema for ActionInput.params (nil = no params)
    bytes output_schema = 8;                    // JSON Schema for ActionResult.data (nil = unstructured)
    bool dangerous = 9;                         // requires confirmation (delete, drain, cordon)
}

enum ActionScope {
    ACTION_SCOPE_UNSPECIFIED = 0;
    ACTION_SCOPE_INSTANCE = 1;                  // single resource
    ACTION_SCOPE_COLLECTION = 2;                // batch operation
    ACTION_SCOPE_GLOBAL = 3;                    // system-wide
}

message ExecuteActionRequest {
    string resource_key = 1;
    string connection_id = 2;
    string action_id = 3;
    ActionInput input = 4;
}

message ActionInput {
    string resource_id = 1;                     // target resource ID
    string namespace = 2;                       // target namespace (empty for cluster-scoped)
    google.protobuf.Struct params = 3;          // action parameters (schema in ActionDescriptor)
}

message ExecuteActionResponse {
    ActionResult result = 1;
}

message ActionResult {
    bool success = 1;
    string message = 2;                         // human-readable result summary
    google.protobuf.Struct data = 3;            // structured result data
    omniview.sdk.common.v1.ResourceError error = 4;
}

// StreamActionEvent is sent during streaming actions (e.g., drain progress).
message StreamActionEvent {
    string type = 1;                            // "progress", "complete", "error"
    string message = 2;                         // human-readable status
    float progress = 3;                         // 0.0 - 1.0 (if applicable)
    google.protobuf.Struct data = 4;            // extra event data
}
```

---

## 4. filter.proto — Filter/Query Types (Doc 13)

```protobuf
syntax = "proto3";
package omniview.sdk.resource.v1;

option go_package = "github.com/omniviewdev/plugin-sdk/proto/v1/resourcepb";

import "google/protobuf/struct.proto";

// ─────────────────────────────────────────────────────────────────────
// Filter Operators & Field Types
// ─────────────────────────────────────────────────────────────────────

enum FilterOperator {
    FILTER_OP_UNSPECIFIED = 0;
    FILTER_OP_EQ = 1;                           // equal
    FILTER_OP_NEQ = 2;                          // not equal
    FILTER_OP_GT = 3;                           // greater than
    FILTER_OP_GTE = 4;                          // greater or equal
    FILTER_OP_LT = 5;                           // less than
    FILTER_OP_LTE = 6;                          // less or equal
    FILTER_OP_CONTAINS = 7;                     // substring match
    FILTER_OP_PREFIX = 8;                        // starts with
    FILTER_OP_SUFFIX = 9;                        // ends with
    FILTER_OP_REGEX = 10;                       // RE2 regex
    FILTER_OP_IN = 11;                          // value in set
    FILTER_OP_NOT_IN = 12;                      // value not in set
    FILTER_OP_EXISTS = 13;                      // field present & non-nil
    FILTER_OP_NOT_EXISTS = 14;                  // field absent or nil
    FILTER_OP_HAS_KEY = 15;                     // map contains key (labels)
}

enum FilterFieldType {
    FILTER_FIELD_TYPE_UNSPECIFIED = 0;
    FILTER_FIELD_TYPE_STRING = 1;
    FILTER_FIELD_TYPE_INTEGER = 2;
    FILTER_FIELD_TYPE_NUMBER = 3;               // float
    FILTER_FIELD_TYPE_BOOLEAN = 4;
    FILTER_FIELD_TYPE_DATETIME = 5;             // RFC3339
    FILTER_FIELD_TYPE_ENUM = 6;                 // fixed set of values
    FILTER_FIELD_TYPE_MAP = 7;                  // label/annotation selectors
}

// ─────────────────────────────────────────────────────────────────────
// Introspection Types (discovery — what can be filtered)
// ─────────────────────────────────────────────────────────────────────

// FilterField declares a filterable field for introspection.
// MCP tool generators iterate these to build inputSchema.
// AI agents read these to understand what queries are valid.
message FilterField {
    string path = 1;                            // dot-path: "metadata.labels", "status.phase"
    string display_name = 2;                    // human-readable: "Phase", "Node Name"
    string description = 3;                     // for AI context and MCP descriptions
    FilterFieldType type = 4;
    repeated FilterOperator operators = 5;      // valid ops for this field
    repeated string allowed_values = 6;         // for enum type: ["Running", "Pending", ...]
    bool required = 7;                          // must appear in query
}

// ─────────────────────────────────────────────────────────────────────
// Request Types (query — what the caller sends)
// ─────────────────────────────────────────────────────────────────────

// FilterPredicate is a single condition in a query.
message FilterPredicate {
    string field = 1;                           // must match a FilterField.path
    FilterOperator operator = 2;                // must be in the field's operators list
    google.protobuf.Value value = 3;            // scalar or ListValue for IN/NOT_IN
}

enum FilterLogic {
    FILTER_LOGIC_UNSPECIFIED = 0;               // default = AND
    FILTER_LOGIC_AND = 1;
    FILTER_LOGIC_OR = 2;
}

// FilterExpression combines predicates with a logical operator.
// Supports one level of nesting (AND of ORs, or OR of ANDs).
message FilterExpression {
    FilterLogic logic = 1;                      // default: AND
    repeated FilterPredicate predicates = 2;    // leaf conditions
    repeated FilterExpression groups = 3;       // nested groups (1 level max)
}

// ─────────────────────────────────────────────────────────────────────
// Ordering & Pagination
// ─────────────────────────────────────────────────────────────────────

// OrderField specifies a sort field. Multiple OrderFields = multi-column sort.
message OrderField {
    string path = 1;                            // dot-path field to sort by
    bool descending = 2;                        // false = ascending (default)
}

// PaginationParams supports both page-based and cursor-based pagination.
message PaginationParams {
    int32 page = 1;                             // 1-based page number
    int32 page_size = 2;                        // items per page (0 = backend default)
    string cursor = 3;                          // opaque continuation token
}
```

---

## 5. watch.proto — Watch/Informer Types

```protobuf
syntax = "proto3";
package omniview.sdk.resource.v1;

option go_package = "github.com/omniviewdev/plugin-sdk/proto/v1/resourcepb";

import "v1/common.proto";

// ─────────────────────────────────────────────────────────────────────
// Watch Events (server → engine via ListenForEvents stream)
// ─────────────────────────────────────────────────────────────────────

// WatchEvent is the unified event type sent over the ListenForEvents stream.
// Uses oneof to discriminate between event types.
message WatchEvent {
    string connection_id = 1;
    string resource_key = 2;                    // "group::version::kind"

    oneof event {
        WatchAddPayload add = 10;
        WatchUpdatePayload update = 11;
        WatchDeletePayload delete = 12;
        WatchStateEvent state = 13;
    }
}

// WatchAddPayload — a resource was added.
message WatchAddPayload {
    string id = 1;                              // resource identifier
    string namespace = 2;                       // empty for cluster-scoped
    bytes data = 3;                             // resource data as pre-serialized JSON
}

// WatchUpdatePayload — a resource was modified.
// Per doc 12: only carries the new state. OldData dropped to halve wire cost.
message WatchUpdatePayload {
    string id = 1;
    string namespace = 2;
    bytes data = 3;                             // new state only
}

// WatchDeletePayload — a resource was deleted.
message WatchDeletePayload {
    string id = 1;
    string namespace = 2;
    bytes data = 3;                             // last known state
}

// WatchState represents the synchronization state of a resource watch.
enum WatchState {
    WATCH_STATE_UNSPECIFIED = 0;
    WATCH_STATE_SYNCING = 1;                    // initial sync in progress
    WATCH_STATE_SYNCED = 2;                     // initial sync complete, incremental events flowing
    WATCH_STATE_ERROR = 3;                      // watch encountered a recoverable error
    WATCH_STATE_FAILED = 4;                     // watch failed permanently
    WATCH_STATE_STOPPED = 5;                    // watch intentionally stopped
}

// WatchStateEvent — watch state changed for a resource type.
message WatchStateEvent {
    WatchState state = 1;
    int32 resource_count = 2;                   // number of resources of this type (if known)
    string error_message = 3;                   // non-empty if state is ERROR or FAILED
}

// ─────────────────────────────────────────────────────────────────────
// Watch Control (engine → plugin RPCs)
// ─────────────────────────────────────────────────────────────────────

// ListenRequest starts the event stream. Empty — all events for all connections flow.
message ListenRequest {}

// WatchResourceRequest asks the plugin to start/stop watching a specific resource type.
message WatchResourceRequest {
    string connection_id = 1;
    string resource_key = 2;
}

// WatchResourceResponse confirms the watch operation.
message WatchResourceResponse {
    bool success = 1;
    string error_message = 2;
}
```

---

## 6. relationship.proto — Relationship Graph Types (Doc 18)

```protobuf
syntax = "proto3";
package omniview.sdk.resource.v1;

option go_package = "github.com/omniviewdev/plugin-sdk/proto/v1/resourcepb";

// ─────────────────────────────────────────────────────────────────────
// Relationship Types
// ─────────────────────────────────────────────────────────────────────

enum RelationshipType {
    RELATIONSHIP_TYPE_UNSPECIFIED = 0;
    RELATIONSHIP_TYPE_OWNS = 1;                 // parent-child ownership
    RELATIONSHIP_TYPE_RUNS_ON = 2;              // workload → infrastructure
    RELATIONSHIP_TYPE_USES = 3;                 // consumer → dependency
    RELATIONSHIP_TYPE_EXPOSES = 4;              // service → backend
    RELATIONSHIP_TYPE_MANAGES = 5;              // controller → target
    RELATIONSHIP_TYPE_MEMBER_OF = 6;            // resource → group
}

// RelationshipDescriptor declares a relationship from one resource type to another.
// Returned by RelationshipProvider.DeclareRelationships() — static, cached.
message RelationshipDescriptor {
    RelationshipType type = 1;
    string target_resource_key = 2;             // "group::version::kind"
    string label = 3;                           // "runs on", "uses secret"
    string inverse_label = 4;                   // "runs pods", "used by pod"
    string cardinality = 5;                     // "one" or "many"
    RelationshipExtractor extractor = 6;
}

// RelationshipExtractor defines how to find related resource IDs from source data.
message RelationshipExtractor {
    // method: "field", "ownerRef", "labelSelector", "fieldSelector", "custom"
    string method = 1;

    // For "field" and "fieldSelector" methods.
    // Dot-path to the field: "spec.nodeName", "spec.volumes[*].configMap.name"
    string field_path = 2;

    // For "ownerRef" method. Filter ownerReferences by kind (empty = all).
    string owner_ref_kind = 3;

    // For "labelSelector" method. Maps source label key → target label key.
    map<string, string> label_selector = 4;
}

// ResourceRef is a reference to a specific resource instance.
message ResourceRef {
    string plugin_id = 1;                       // empty = same plugin
    string connection_id = 2;                   // empty = same connection
    string resource_key = 3;                    // "group::version::kind"
    string id = 4;                              // resource identifier
    string namespace = 5;                       // empty for cluster-scoped
    string display_name = 6;                    // human-readable name
}

// ResolvedRelationship contains actual relationship instances for a resource.
message ResolvedRelationship {
    RelationshipDescriptor descriptor = 1;
    repeated ResourceRef targets = 2;
}

// RelationshipEdge is a single edge in the relationship graph.
message RelationshipEdge {
    ResourceRef source = 1;
    ResourceRef target = 2;
    RelationshipDescriptor descriptor = 3;
}

// DependencyTree is an ownership/dependency hierarchy rooted at a resource.
message DependencyTree {
    ResourceRef root = 1;
    repeated DependencyNode children = 2;
}

// DependencyNode is a node in the dependency tree (self-referencing).
message DependencyNode {
    RelationshipEdge edge = 1;
    repeated DependencyNode children = 2;
}

// ─────────────────────────────────────────────────────────────────────
// RPC Messages
// ─────────────────────────────────────────────────────────────────────

message RelationshipsRequest {
    string resource_key = 1;
}

message RelationshipsResponse {
    repeated RelationshipDescriptor relationships = 1;
}

message ResolveRelationshipsRequest {
    string connection_id = 1;
    string resource_key = 2;
    string id = 3;
    string namespace = 4;
}

message ResolveRelationshipsResponse {
    repeated ResolvedRelationship relationships = 1;
}
```

---

## 7. health.proto — Health & Diagnostic Types (Doc 19)

```protobuf
syntax = "proto3";
package omniview.sdk.resource.v1;

option go_package = "github.com/omniviewdev/plugin-sdk/proto/v1/resourcepb";

import "google/protobuf/timestamp.proto";
import "v1/common.proto";
import "v1/relationship.proto";

// ─────────────────────────────────────────────────────────────────────
// Health Types
// ─────────────────────────────────────────────────────────────────────

enum HealthStatus {
    HEALTH_STATUS_UNSPECIFIED = 0;
    HEALTH_STATUS_HEALTHY = 1;                  // fully operational
    HEALTH_STATUS_DEGRADED = 2;                 // partially operational
    HEALTH_STATUS_UNHEALTHY = 3;                // not operational
    HEALTH_STATUS_PENDING = 4;                  // not yet evaluated
    HEALTH_STATUS_UNKNOWN = 5;                  // cannot determine
}

// ResourceHealth is the normalized health assessment for a resource.
// Produced by HealthProvider.GetHealth() — a pure function on resource JSON data.
message ResourceHealth {
    HealthStatus status = 1;
    string reason = 2;                          // machine-readable: "CrashLoopBackOff", "Ready"
    string message = 3;                         // human-readable explanation
    optional google.protobuf.Timestamp since = 4; // when this status started
    repeated HealthCondition conditions = 5;    // detailed conditions (K8s-style)
}

// HealthCondition is a single health condition (Ready, Scheduled, etc.).
message HealthCondition {
    string type = 1;                            // "Ready", "ContainersReady", "Scheduled"
    string status = 2;                          // "True", "False", "Unknown"
    string reason = 3;                          // machine-readable code
    string message = 4;                         // human-readable explanation
    optional google.protobuf.Timestamp last_probe_time = 5;
    optional google.protobuf.Timestamp last_transition_time = 6;
}

// ─────────────────────────────────────────────────────────────────────
// Event Types
// ─────────────────────────────────────────────────────────────────────

enum EventSeverity {
    EVENT_SEVERITY_UNSPECIFIED = 0;
    EVENT_SEVERITY_NORMAL = 1;                  // informational
    EVENT_SEVERITY_WARNING = 2;                 // problem detected
    EVENT_SEVERITY_ERROR = 3;                   // critical failure
}

// ResourceEvent is a diagnostic event (K8s Events, CloudTrail, audit logs).
message ResourceEvent {
    EventSeverity type = 1;
    string reason = 2;                          // "Started", "Failed", "WarningImagePull"
    string message = 3;                         // human-readable description
    string source = 4;                          // "kubelet", "deployment-controller"
    int32 count = 5;                            // occurrence count
    google.protobuf.Timestamp first_seen = 6;
    google.protobuf.Timestamp last_seen = 7;
}

// ─────────────────────────────────────────────────────────────────────
// Diagnostic Graph Types (engine-side, exposed via adapter)
// ─────────────────────────────────────────────────────────────────────

// AnnotatedNode is a resource with health and event context.
message AnnotatedNode {
    ResourceRef ref = 1;
    ResourceHealth health = 2;                  // nil if HealthProvider not implemented
    repeated ResourceEvent events = 3;          // empty if not requested or not available
}

// AnnotatedEdge is a relationship edge with health on the target.
message AnnotatedEdge {
    RelationshipEdge edge = 1;
    AnnotatedNode target = 2;
}

// DiagnosticContext is the complete debugging context for a single resource.
// One call returns everything an AI agent needs to start investigating.
message DiagnosticContext {
    bytes resource = 1;                         // full resource data as JSON
    ResourceHealth health = 2;
    repeated ResourceEvent events = 3;
    repeated AnnotatedEdge related = 4;         // immediate relationships with health
    repeated AnnotatedNode owner_chain = 5;     // ownership hierarchy to root controller
}

// DiagnosticTree is a health-annotated dependency tree for UI rendering.
message DiagnosticTree {
    AnnotatedNode root = 1;
    repeated DiagnosticBranch children = 2;
}

// DiagnosticBranch is a node in the diagnostic tree (self-referencing).
message DiagnosticBranch {
    RelationshipEdge edge = 1;
    AnnotatedNode node = 2;
    repeated DiagnosticBranch children = 3;
}

// ImpactReport describes what would be affected by a resource failure/removal.
message ImpactReport {
    ResourceRef target = 1;
    // Direct dependents grouped by resource type.
    map<string, AnnotatedNodeList> direct_dependents = 2;
    // Transitive dependents grouped by resource type.
    map<string, AnnotatedNodeList> transitive_dependents = 3;
    string summary = 4;                         // human-readable impact summary
    map<string, int32> affected_counts = 5;     // resource_key → count
}

// AnnotatedNodeList wraps repeated AnnotatedNode for use as proto map value.
message AnnotatedNodeList {
    repeated AnnotatedNode nodes = 1;
}

// CommonCauseResult identifies shared dependencies between multiple resources.
message CommonCauseResult {
    repeated ResourceRef resources = 1;         // input resources
    repeated AnnotatedNode shared_dependencies = 2; // shared by ALL inputs
    repeated SharedDependency shared_by_count = 3;  // sorted by share count
}

// SharedDependency groups a dependency by how many input resources share it.
message SharedDependency {
    AnnotatedNode resource = 1;
    int32 shared_by = 2;                        // how many inputs share this
    int32 total_input = 3;                      // total number of input resources
    repeated ResourceRef depended_by = 4;       // which inputs depend on this
}

// ─────────────────────────────────────────────────────────────────────
// RPC Messages
// ─────────────────────────────────────────────────────────────────────

message HealthRequest {
    string connection_id = 1;
    string resource_key = 2;
    bytes data = 3;                             // resource JSON data to evaluate
}

message HealthResponse {
    ResourceHealth health = 1;
}

message ResourceEventsRequest {
    string connection_id = 1;
    string resource_key = 2;
    string id = 3;                              // resource identifier
    string namespace = 4;
    int32 limit = 5;                            // max events to return (0 = default)
}

message ResourceEventsResponse {
    repeated ResourceEvent events = 1;
}
```

---

## 8. Conversion Notes & Edge Cases

### 8.1 `bytes` Fields (json.RawMessage)

All resource data crosses the gRPC boundary as pre-serialized JSON bytes (doc 12
optimization). This eliminates the `Struct ↔ JSON ↔ Go map` round-trip that caused
4 deep copies in the current pipeline.

Fields using `bytes`:
- `GetResponse.data`, `CreateResponse.data`, `UpdateResponse.data`
- `ListResponse.items[]`, `FindResponse.items[]`
- `WatchAddPayload.data`, `WatchUpdatePayload.data`, `WatchDeletePayload.data`
- `CreateRequest.data`, `UpdateRequest.data`
- `DiagnosticContext.resource`
- `HealthRequest.data`
- `EditorSchema.schema`
- `ActionDescriptor.params_schema`, `ActionDescriptor.output_schema`
- `ResourceSchemaResponse.schema`

### 8.2 `google.protobuf.Value` Fields (interface{})

Used where the Go type is `interface{}` or `any` — a value that can be any JSON scalar
or array:

- `FilterPredicate.value` — scalar for most operators, `ListValue` for `IN`/`NOT_IN`

### 8.3 `google.protobuf.Struct` Fields (map[string]interface{})

Used where the Go type is `map[string]interface{}` — an arbitrary JSON object:

- `Connection.data` — connection metadata (kubeconfig path, etc.)
- `ConnectionStatus.metadata` — cluster info (K8s version, node count)
- `ActionInput.params` — action parameters
- `ActionResult.data` — action result data
- `StreamActionEvent.data` — streaming action event data
- `ResourceError.details` — extra error context

### 8.4 `google.protobuf.Timestamp` Fields (time.Time)

- `ResourceHealth.since` — optional (pointer in Go → `optional` in proto3)
- `HealthCondition.last_probe_time` — optional
- `HealthCondition.last_transition_time` — optional
- `ResourceEvent.first_seen` — required
- `ResourceEvent.last_seen` — required

### 8.5 Self-Referencing Messages

Three messages reference themselves for recursive structures:

- `FilterExpression.groups` — `repeated FilterExpression` (one nesting level max)
- `DependencyNode.children` — `repeated DependencyNode`
- `DiagnosticBranch.children` — `repeated DiagnosticBranch`

Proto3 handles this natively. The SDK enforces depth limits (1 level for filters,
unbounded for trees).

### 8.6 Map Value Wrappers

Proto3 `map<K, V>` requires `V` to be a scalar or message, not `repeated`. For
`ImpactReport.direct_dependents` (Go: `map[string][]AnnotatedNode`), we use a
wrapper message:

```protobuf
message AnnotatedNodeList {
    repeated AnnotatedNode nodes = 1;
}

// Then in ImpactReport:
map<string, AnnotatedNodeList> direct_dependents = 2;
```

---

## 9. Relationship to Other Documents

| Doc | How This Doc Relates |
|-----|---------------------|
| 09 | Go type definitions → proto messages. Every interface type from doc 09 §4 has a proto mapping here. |
| 12 | Serialization strategy (`bytes` fields) applied to all resource data messages. |
| 13 | Filter/query types (`FilterField`, `FilterPredicate`, `FilterExpression`) defined in `filter.proto`. |
| 18 | Relationship types (`RelationshipDescriptor`, `ResourceRef`, `DependencyTree`) defined in `relationship.proto`. |
| 19 | Health/diagnostic types (`ResourceHealth`, `DiagnosticContext`, `ImpactReport`) defined in `health.proto`. |
| 20 | Proto file organization (`proto/v1/`) and versioning strategy. This doc fills the message definitions that doc 20 §7.1 references but doesn't define. |
