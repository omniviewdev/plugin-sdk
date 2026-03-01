package resource

import (
	"encoding/json"
	"strings"
)

// ============================================================================
// Resource Metadata & Groups
// ============================================================================

// ResourceMeta contains information about the categorization of a resource.
// Used to identify resource types across plugins and route operations to the
// correct Resourcer implementation.
type ResourceMeta struct {
	// Group is the group of the resource (e.g., "core", "apps", "ec2").
	Group string `json:"group"`

	// Version is the version of the resource (e.g., "v1", "v1beta1").
	Version string `json:"version"`

	// Kind is the kind of the resource (e.g., "Pod", "Deployment", "EC2Instance").
	Kind string `json:"kind"`

	// Label is a human-readable label. Defaults to Kind if not provided.
	Label string `json:"label"`

	// Icon is an optional icon (icon name, data URI, or URL).
	Icon string `json:"icon"`

	// Description is a human-readable description of the resource.
	Description string `json:"description"`

	// Category is the category for grouping (e.g., "Workloads", "Networking").
	// Defaults to "Uncategorized" if empty.
	Category string `json:"category"`
}

// String returns the canonical string representation: "group::version::kind".
func (r ResourceMeta) String() string {
	return r.Group + "::" + r.Version + "::" + r.Kind
}

// Key is an alias for String — the canonical resource key.
func (r ResourceMeta) Key() string {
	return r.String()
}

// ResourceMetaFromString parses a "group::version::kind" string into a ResourceMeta.
func ResourceMetaFromString(s string) ResourceMeta {
	parts := strings.Split(s, "::")
	if len(parts) != 3 {
		return ResourceMeta{}
	}
	return ResourceMeta{
		Group:   parts[0],
		Version: parts[1],
		Kind:    parts[2],
	}
}

// ResourceGroup is a categorization of resources within a plugin.
type ResourceGroup struct {
	// ID is the unique identifier of the resource group.
	ID string `json:"id"`

	// Name is the display name of the resource group.
	Name string `json:"name"`

	// Description is a human-readable description.
	Description string `json:"description"`

	// Icon is an optional icon for the resource group.
	Icon string `json:"icon"`

	// Resources is a map of resource versions to the resources in that version.
	Resources map[string][]ResourceMeta `json:"resources"`
}

// ============================================================================
// Resource Definitions (column defs, accessors)
// ============================================================================

// ResourceDefinition describes the table rendering configuration for a resource type.
type ResourceDefinition struct {
	// IDAccessor is the JSON path to extract the resource ID.
	IDAccessor string `json:"id_accessor"`

	// NamespaceAccessor is the JSON path to extract the namespace.
	NamespaceAccessor string `json:"namespace_accessor"`

	// MemoizerAccessor is the JSON path to extract a memoization key.
	MemoizerAccessor string `json:"memoizer_accessor"`

	// ColumnDefs defines the table columns for this resource type.
	ColumnDefs []ColumnDefinition `json:"columnDefs"`
}

// ColumnDefinition describes a single table column for resource display.
type ColumnDefinition struct {
	// ID is the unique identifier for this column.
	ID string `json:"id"`

	// Header is the display header text.
	Header string `json:"header"`

	// Accessors is a comma-separated list of JSON path accessors.
	Accessors string `json:"accessor"`

	// AccessorPriority controls which value to return when multiple
	// accessors match. Values: "ALL", "FIRST", "LAST".
	AccessorPriority string `json:"accessorPriority,omitempty"`

	// ColorMap maps cell values to color variants.
	ColorMap map[string]string `json:"colorMap,omitempty"`

	// Color is the default color variant for the cell.
	Color string `json:"color,omitempty"`

	// Alignment is the column alignment: "LEFT", "CENTER", "RIGHT".
	Alignment string `json:"align,omitempty"`

	// Hidden controls whether the column is visible by default.
	Hidden bool `json:"hidden,omitempty"`

	// Width is the column width in pixels. 0 means auto-size.
	Width int `json:"width,omitempty"`

	// Formatter is the value formatter: "NONE", "BYTES", "DURATION", "AGE", etc.
	Formatter string `json:"formatter,omitempty"`

	// Component is the cell renderer component name.
	Component string `json:"component,omitempty"`

	// ComponentParams are parameters passed to the cell component.
	ComponentParams interface{} `json:"componentParams,omitempty"`

	// ResourceLink creates a link to another resource.
	ResourceLink *ResourceLink `json:"resourceLink,omitempty"`

	// ValueMap maps values via regex replacement.
	ValueMap map[string]string `json:"valueMap,omitempty"`
}

// ResourceLink creates links to other resources from table cells.
type ResourceLink struct {
	IDAccessor        string            `json:"idAccessor"`
	NamespaceAccessor string            `json:"namespaceAccessor"`
	Namespaced        bool              `json:"namespaced"`
	Key               string            `json:"resourceKey"`
	KeyAccessor       string            `json:"keyAccessor"`
	KeyMap            map[string]string `json:"keyMap"`
	DetailExtractors  map[string]string `json:"detailExtractors"`
	DisplayID         bool              `json:"displayId"`
}

// EditorSchema provides schema information for Monaco editor validation.
type EditorSchema struct {
	ResourceKey string `json:"resourceKey"`
	FileMatch   string `json:"fileMatch"`
	URI         string `json:"uri"`
	URL         string `json:"url,omitempty"`
	Content     []byte `json:"content,omitempty"`
	Language    string `json:"language"`
}

// ============================================================================
// CRUD Input/Result Types
// ============================================================================

// GetInput is the input to the Get operation.
type GetInput struct {
	// ID is the unique identifier of the resource.
	ID string `json:"id"`

	// Namespace is an optional namespace identifier.
	Namespace string `json:"namespace"`
}

// GetResult is the result of a Get operation.
type GetResult struct {
	// Result is the resource data as pre-serialized JSON.
	Result json.RawMessage `json:"result"`

	// Success indicates whether the operation succeeded.
	Success bool `json:"success"`
}

// ListInput is the input to the List operation.
type ListInput struct {
	// Namespaces limits listing to these namespaces.
	// Empty means all namespaces.
	Namespaces []string `json:"namespaces"`

	// Order specifies multi-field ordering.
	Order []OrderField `json:"order"`

	// Pagination controls pagination.
	Pagination PaginationParams `json:"pagination"`
}

// ListResult is the result of a List operation.
type ListResult struct {
	// Result is the list of resources as pre-serialized JSON.
	Result []json.RawMessage `json:"result"`

	// Success indicates whether the operation succeeded.
	Success bool `json:"success"`

	// TotalCount is the total number of resources (for pagination).
	// -1 if unknown.
	TotalCount int `json:"totalCount"`

	// NextCursor is the continuation token for cursor-based pagination.
	NextCursor string `json:"nextCursor,omitempty"`
}

// FindInput is the input to the Find operation.
type FindInput struct {
	// Filters is a typed filter expression replacing the old untyped Conditions.
	Filters *FilterExpression `json:"filters,omitempty"`

	// TextQuery is a free-text search string.
	TextQuery string `json:"textQuery,omitempty"`

	// Namespaces limits the search scope.
	Namespaces []string `json:"namespaces"`

	// Order specifies multi-field ordering.
	Order []OrderField `json:"order"`

	// Pagination controls pagination.
	Pagination PaginationParams `json:"pagination"`
}

// FindResult is the result of a Find operation.
type FindResult struct {
	// Result is the list of matching resources as pre-serialized JSON.
	Result []json.RawMessage `json:"result"`

	// Success indicates whether the operation succeeded.
	Success bool `json:"success"`

	// TotalCount is the total number of matching resources.
	// -1 if unknown.
	TotalCount int `json:"totalCount"`

	// NextCursor is the continuation token for cursor-based pagination.
	NextCursor string `json:"nextCursor,omitempty"`
}

// CreateInput is the input to the Create operation.
type CreateInput struct {
	// Input is the resource data to create.
	Input json.RawMessage `json:"input"`

	// Namespace is an optional namespace for the new resource.
	Namespace string `json:"namespace"`
}

// CreateResult is the result of a Create operation.
type CreateResult struct {
	// Result is the created resource as pre-serialized JSON.
	Result json.RawMessage `json:"result"`

	// Success indicates whether the operation succeeded.
	Success bool `json:"success"`
}

// UpdateInput is the input to the Update operation.
type UpdateInput struct {
	// Input is the updated resource data.
	Input json.RawMessage `json:"input"`

	// ID is the unique identifier of the resource to update.
	ID string `json:"id"`

	// Namespace is an optional namespace identifier.
	Namespace string `json:"namespace"`
}

// UpdateResult is the result of an Update operation.
type UpdateResult struct {
	// Result is the updated resource as pre-serialized JSON.
	Result json.RawMessage `json:"result"`

	// Success indicates whether the operation succeeded.
	Success bool `json:"success"`
}

// DeleteInput is the input to the Delete operation.
type DeleteInput struct {
	// ID is the unique identifier of the resource to delete.
	ID string `json:"id"`

	// Namespace is an optional namespace identifier.
	Namespace string `json:"namespace"`

	// GracePeriodSeconds is an optional grace period before deletion.
	// nil means use the default.
	GracePeriodSeconds *int64 `json:"gracePeriodSeconds,omitempty"`
}

// DeleteResult is the result of a Delete operation.
type DeleteResult struct {
	// Result is the deleted resource as pre-serialized JSON (if available).
	Result json.RawMessage `json:"result"`

	// Success indicates whether the operation succeeded.
	Success bool `json:"success"`
}

// ============================================================================
// Ordering & Pagination
// ============================================================================

// OrderField specifies a single field in a multi-field ordering clause.
type OrderField struct {
	// Field is the dot-separated path to the field to order by.
	Field string `json:"field"`

	// Descending controls sort direction. false = ascending (default).
	Descending bool `json:"descending"`
}

// PaginationParams controls pagination for List and Find operations.
type PaginationParams struct {
	// Page is the 1-based page number (for page-based pagination).
	Page int `json:"page"`

	// PageSize is the maximum number of results per page.
	// 0 means return all results.
	PageSize int `json:"pageSize"`

	// Cursor is the continuation token from a previous response
	// (for cursor-based pagination). Mutually exclusive with Page.
	Cursor string `json:"cursor,omitempty"`
}

// ============================================================================
// Filter Types (doc 13)
// ============================================================================

// FilterOperator is a comparison operator for filter predicates.
type FilterOperator string

const (
	// Equality.
	OpEqual    FilterOperator = "eq"
	OpNotEqual FilterOperator = "neq"

	// Comparison (numeric, time).
	OpGreaterThan    FilterOperator = "gt"
	OpGreaterOrEqual FilterOperator = "gte"
	OpLessThan       FilterOperator = "lt"
	OpLessOrEqual    FilterOperator = "lte"

	// String matching.
	OpContains FilterOperator = "contains"
	OpPrefix   FilterOperator = "prefix"
	OpSuffix   FilterOperator = "suffix"
	OpRegex    FilterOperator = "regex"

	// Set membership.
	OpIn    FilterOperator = "in"
	OpNotIn FilterOperator = "notin"

	// Existence.
	OpExists    FilterOperator = "exists"
	OpNotExists FilterOperator = "notexists"

	// Map/label-specific.
	OpHasKey FilterOperator = "haskey"
)

// FilterFieldType declares the value type for a filterable field.
type FilterFieldType string

const (
	FilterFieldString FilterFieldType = "string"
	FilterFieldInt    FilterFieldType = "integer"
	FilterFieldFloat  FilterFieldType = "number"
	FilterFieldBool   FilterFieldType = "boolean"
	FilterFieldTime   FilterFieldType = "datetime"
	FilterFieldEnum   FilterFieldType = "enum"
	FilterFieldMap    FilterFieldType = "map"
)

// FilterField declares a field that can be used in filter predicates.
// This is the introspection/discovery type for AI agents and MCP tool generators.
type FilterField struct {
	// Path is the dot-separated field path (e.g., "metadata.name", "status.phase").
	Path string `json:"path"`

	// DisplayName is the human-readable name for UI and MCP tool descriptions.
	DisplayName string `json:"displayName"`

	// Description explains what this field represents.
	Description string `json:"description"`

	// Type declares the value type.
	Type FilterFieldType `json:"type"`

	// Operators lists valid comparison operators for this field.
	// If empty, defaults to [OpEqual, OpNotEqual].
	Operators []FilterOperator `json:"operators"`

	// AllowedValues is the fixed set of valid values (only for FilterFieldEnum).
	AllowedValues []string `json:"allowedValues,omitempty"`

	// Required indicates this field must be provided in filter queries.
	Required bool `json:"required,omitempty"`
}

// FilterPredicate is a single condition in a query.
type FilterPredicate struct {
	// Field is the filter field path (must match a FilterField.Path).
	Field string `json:"field"`

	// Operator is the comparison operator.
	Operator FilterOperator `json:"operator"`

	// Value is the comparison value. Type depends on field and operator:
	//   - OpIn/OpNotIn: []string
	//   - OpExists/OpNotExists: ignored (nil)
	//   - All others: scalar matching the field's FilterFieldType
	Value interface{} `json:"value,omitempty"`
}

// FilterLogic is the logical operator for combining predicates.
type FilterLogic string

const (
	FilterAnd FilterLogic = "and"
	FilterOr  FilterLogic = "or"
)

// FilterExpression combines predicates with a logical operator.
// Supports one level of nesting (AND of ORs, or OR of ANDs).
type FilterExpression struct {
	Logic      FilterLogic       `json:"logic,omitempty"`
	Predicates []FilterPredicate `json:"predicates,omitempty"`
	Groups     []FilterExpression `json:"groups,omitempty"`
}

// ============================================================================
// Action Types
// ============================================================================

// ActionScope defines whether an action operates on a specific resource
// instance or on the resource type as a whole.
type ActionScope string

const (
	ActionScopeInstance ActionScope = "instance"
	ActionScopeType    ActionScope = "type"
)

// ActionDescriptor describes an available action on a resource type.
type ActionDescriptor struct {
	ID          string          `json:"id"`
	Label       string          `json:"label"`
	Description string          `json:"description"`
	Icon        string          `json:"icon"`
	Scope       ActionScope     `json:"scope"`
	Streaming   bool            `json:"streaming"`

	// ParamsSchema is a JSON Schema describing the action's parameters.
	// Used by MCP tool generators as inputSchema.
	ParamsSchema json.RawMessage `json:"paramsSchema,omitempty"`

	// OutputSchema is a JSON Schema describing the action's result.
	// Used by MCP tool generators as outputSchema.
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`

	// Dangerous indicates this action has destructive side effects.
	// AI agents should confirm before executing.
	Dangerous bool `json:"dangerous,omitempty"`
}

// ActionInput contains the parameters for executing an action.
type ActionInput struct {
	ID        string                 `json:"id"`
	Namespace string                 `json:"namespace"`
	Params    map[string]interface{} `json:"params"`
}

// ActionResult contains the result of executing an action.
type ActionResult struct {
	Success bool                   `json:"success"`
	Data    map[string]interface{} `json:"data"`
	Message string                 `json:"message"`
}

// ActionEvent represents a streaming event from a long-running action.
type ActionEvent struct {
	Type string                 `json:"type"` // "progress", "output", "error", "complete"
	Data map[string]interface{} `json:"data"`
}

// ============================================================================
// Resource Capabilities (auto-derived, doc 13)
// ============================================================================

// ResourceCapabilities describes what operations and features a resource
// type supports. Auto-derived by the SDK from type assertions on registered
// Resourcers at registration time.
type ResourceCapabilities struct {
	// CRUD flags.
	CanGet    bool `json:"canGet"`
	CanList   bool `json:"canList"`
	CanFind   bool `json:"canFind"`
	CanCreate bool `json:"canCreate"`
	CanUpdate bool `json:"canUpdate"`
	CanDelete bool `json:"canDelete"`

	// Extended capabilities.
	Watchable       bool `json:"watchable"`
	Filterable      bool `json:"filterable"`
	Searchable      bool `json:"searchable"`
	HasActions      bool `json:"hasActions"`
	HasSchema       bool `json:"hasSchema"`
	NamespaceScoped bool `json:"namespaceScoped"`
	HasRelationships bool `json:"hasRelationships"`
	HasHealth       bool `json:"hasHealth"`
	HasEvents       bool `json:"hasEvents"`

	// ScaleHint indicates expected cardinality.
	Scale *ScaleHint `json:"scaleHint,omitempty"`
}

// ScaleHint indicates the expected cardinality of a resource type.
type ScaleHint struct {
	// Level is the expected scale: "few", "moderate", "many".
	Level ScaleLevel `json:"level"`

	// ExpectedCount is the approximate expected count (optional).
	ExpectedCount int `json:"expectedCount,omitempty"`

	// DefaultPageSize is the recommended page size for this resource.
	DefaultPageSize int `json:"defaultPageSize,omitempty"`
}

// ScaleLevel represents expected resource cardinality.
type ScaleLevel string

const (
	// ScaleFew means <100 resources (list-all is fine).
	ScaleFew ScaleLevel = "few"
	// ScaleModerate means 100-10K resources (pagination recommended).
	ScaleModerate ScaleLevel = "moderate"
	// ScaleMany means 10K+ resources (filter-first approach required).
	ScaleMany ScaleLevel = "many"
)

// ============================================================================
// Errors
// ============================================================================

// ResourceOperationError is a structured error for resource operations.
// Its Error() method returns a JSON string so the structured fields survive
// the Wails IPC boundary.
type ResourceOperationError struct {
	Err         error    `json:"-"`
	Code        string   `json:"code"`
	Title       string   `json:"title"`
	Message     string   `json:"message"`
	Suggestions []string `json:"suggestions,omitempty"`
}

// Error returns a JSON-encoded representation of the structured error.
func (e *ResourceOperationError) Error() string {
	b, _ := json.Marshal(struct {
		Code        string   `json:"code"`
		Title       string   `json:"title"`
		Message     string   `json:"message"`
		Suggestions []string `json:"suggestions,omitempty"`
	}{e.Code, e.Title, e.Message, e.Suggestions})
	return string(b)
}

// Unwrap returns the underlying error for use with errors.Is / errors.As.
func (e *ResourceOperationError) Unwrap() error { return e.Err }

// ErrFilterUnknownField returns a structured error for an unknown filter field path.
func ErrFilterUnknownField(field string) *ResourceOperationError {
	return &ResourceOperationError{
		Code:    "FILTER_UNKNOWN_FIELD",
		Title:   "Unknown filter field",
		Message: "field " + field + " is not a valid filter field",
		Suggestions: []string{
			"Check FilterFields() for the list of supported filter paths",
		},
	}
}

// ErrFilterInvalidOperator returns a structured error for an unsupported operator on a field.
func ErrFilterInvalidOperator(field string, op FilterOperator, allowed []FilterOperator) *ResourceOperationError {
	ops := make([]string, len(allowed))
	for i, a := range allowed {
		ops[i] = string(a)
	}
	return &ResourceOperationError{
		Code:    "FILTER_INVALID_OPERATOR",
		Title:   "Invalid filter operator",
		Message: "operator " + string(op) + " is not valid for field " + field,
		Suggestions: []string{
			"Allowed operators: " + strings.Join(ops, ", "),
		},
	}
}
