package plugin

import (
	"errors"
	"fmt"
	"time"

	commonpb "github.com/omniviewdev/plugin-sdk/proto/v1/common"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/v1/resource"

	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
	"github.com/omniviewdev/plugin-sdk/pkg/types"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ============================================================================
// Connection converters
// ============================================================================

func connectionToProto(c types.Connection) *commonpb.Connection {
	labels := make(map[string]string, len(c.Labels))
	for k, v := range c.Labels {
		labels[k] = fmt.Sprintf("%v", v)
	}
	var data *structpb.Struct
	if c.Data != nil {
		// Convert map[string]any -> Struct (best-effort).
		data, _ = structpb.NewStruct(c.Data)
	}
	return &commonpb.Connection{
		Id:          c.ID,
		Name:        c.Name,
		Description: c.Description,
		Avatar:      c.Avatar,
		Labels:      labels,
		Data:        data,
	}
}

func connectionFromProto(pb *commonpb.Connection) types.Connection {
	if pb == nil {
		return types.Connection{}
	}
	labels := make(map[string]any, len(pb.GetLabels()))
	for k, v := range pb.GetLabels() {
		labels[k] = v
	}
	var data map[string]any
	if pb.GetData() != nil {
		data = pb.GetData().AsMap()
	}
	return types.Connection{
		ID:          pb.GetId(),
		Name:        pb.GetName(),
		Description: pb.GetDescription(),
		Avatar:      pb.GetAvatar(),
		Labels:      labels,
		Data:        data,
	}
}

// connectionStatusCodeToProto maps Go status string to proto enum.
var connectionStatusCodeToProto = map[types.ConnectionStatusCode]commonpb.ConnectionState{
	types.ConnectionStatusConnected:    commonpb.ConnectionState_CONNECTION_STATE_CONNECTED,
	types.ConnectionStatusDisconnected: commonpb.ConnectionState_CONNECTION_STATE_DISCONNECTED,
	types.ConnectionStatusError:        commonpb.ConnectionState_CONNECTION_STATE_ERROR,
}

// connectionStatusCodeFromProto maps proto enum to Go status string.
var connectionStatusCodeFromProto = map[commonpb.ConnectionState]types.ConnectionStatusCode{
	commonpb.ConnectionState_CONNECTION_STATE_CONNECTED:    types.ConnectionStatusConnected,
	commonpb.ConnectionState_CONNECTION_STATE_DISCONNECTED: types.ConnectionStatusDisconnected,
	commonpb.ConnectionState_CONNECTION_STATE_ERROR:        types.ConnectionStatusError,
	commonpb.ConnectionState_CONNECTION_STATE_RECONNECTING: types.ConnectionStatusConnected, // closest mapping
}

func connectionStatusToProto(cs types.ConnectionStatus) *commonpb.ConnectionStatus {
	var conn *commonpb.Connection
	if cs.Connection != nil {
		c := connectionToProto(*cs.Connection)
		conn = c
	}
	state, ok := connectionStatusCodeToProto[cs.Status]
	if !ok {
		state = commonpb.ConnectionState_CONNECTION_STATE_UNSPECIFIED
	}
	return &commonpb.ConnectionStatus{
		Connection: conn,
		State:      state,
		Message:    cs.Details,
		Error:      cs.Error,
	}
}

func connectionStatusFromProto(pb *commonpb.ConnectionStatus) types.ConnectionStatus {
	if pb == nil {
		return types.ConnectionStatus{}
	}
	var conn *types.Connection
	if pb.GetConnection() != nil {
		c := connectionFromProto(pb.GetConnection())
		conn = &c
	}
	status := connectionStatusCodeFromProto[pb.GetState()]
	if status == "" {
		status = types.ConnectionStatusUnknown
	}
	return types.ConnectionStatus{
		Connection: conn,
		Status:     status,
		Error:      pb.GetError(),
		Details:    pb.GetMessage(),
	}
}

// ============================================================================
// Resource metadata converters
// ============================================================================

func resourceMetaToProto(m resource.ResourceMeta) *commonpb.ResourceMeta {
	return &commonpb.ResourceMeta{
		Group:       m.Group,
		Version:     m.Version,
		Kind:        m.Kind,
		DisplayName: m.Label,
		Description: m.Description,
	}
}

func resourceMetaFromProto(pb *commonpb.ResourceMeta) resource.ResourceMeta {
	if pb == nil {
		return resource.ResourceMeta{}
	}
	return resource.ResourceMeta{
		Group:       pb.GetGroup(),
		Version:     pb.GetVersion(),
		Kind:        pb.GetKind(),
		Label:       pb.GetDisplayName(),
		Description: pb.GetDescription(),
	}
}

func resourceGroupToProto(g resource.ResourceGroup) *commonpb.ResourceGroup {
	// Flatten map[version][]ResourceMeta -> repeated ResourceMeta.
	var metas []*commonpb.ResourceMeta
	for _, ms := range g.Resources {
		for _, m := range ms {
			metas = append(metas, resourceMetaToProto(m))
		}
	}
	return &commonpb.ResourceGroup{
		Id:          g.ID,
		Name:        g.Name,
		Icon:        g.Icon,
		Description: g.Description,
		Resources:   metas,
	}
}

func resourceGroupFromProto(pb *commonpb.ResourceGroup) resource.ResourceGroup {
	if pb == nil {
		return resource.ResourceGroup{}
	}
	// Reconstruct map[version][]ResourceMeta from flat list.
	resources := make(map[string][]resource.ResourceMeta)
	for _, m := range pb.GetResources() {
		meta := resourceMetaFromProto(m)
		resources[meta.Version] = append(resources[meta.Version], meta)
	}
	return resource.ResourceGroup{
		ID:          pb.GetId(),
		Name:        pb.GetName(),
		Icon:        pb.GetIcon(),
		Description: pb.GetDescription(),
		Resources:   resources,
	}
}

// ============================================================================
// Error converters
// ============================================================================

// Error code mapping: Go string codes → proto int32.
var errorCodeToProto = map[string]int32{
	"NOT_FOUND":              1,
	"ALREADY_EXISTS":         2,
	"PERMISSION_DENIED":      3,
	"INVALID_INPUT":          4,
	"CONFLICT":               5,
	"INTERNAL":               6,
	"UNAVAILABLE":            7,
	"TIMEOUT":                8,
	"FILTER_UNKNOWN_FIELD":   9,
	"FILTER_INVALID_OPERATOR": 10,
}

var errorCodeFromProto = map[int32]string{
	1:  "NOT_FOUND",
	2:  "ALREADY_EXISTS",
	3:  "PERMISSION_DENIED",
	4:  "INVALID_INPUT",
	5:  "CONFLICT",
	6:  "INTERNAL",
	7:  "UNAVAILABLE",
	8:  "TIMEOUT",
	9:  "FILTER_UNKNOWN_FIELD",
	10: "FILTER_INVALID_OPERATOR",
}

func resourceErrorToProto(e *resource.ResourceOperationError) *commonpb.ResourceError {
	if e == nil {
		return nil
	}
	code, ok := errorCodeToProto[e.Code]
	if !ok {
		code = 0 // UNSPECIFIED
	}
	return &commonpb.ResourceError{
		Code:        code,
		Title:       e.Title,
		Message:     e.Message,
		Suggestions: e.Suggestions,
	}
}

func resourceErrorFromProto(pb *commonpb.ResourceError) *resource.ResourceOperationError {
	if pb == nil {
		return nil
	}
	code := errorCodeFromProto[pb.GetCode()]
	if code == "" {
		code = fmt.Sprintf("UNKNOWN_%d", pb.GetCode())
	}
	return &resource.ResourceOperationError{
		Code:        code,
		Title:       pb.GetTitle(),
		Message:     pb.GetMessage(),
		Suggestions: pb.GetSuggestions(),
	}
}

// errorToProtoError converts a Go error to a proto ResourceError.
// If the error is a *ResourceOperationError, it's converted directly.
// Otherwise, a generic INTERNAL error is created.
func errorToProtoError(err error) *commonpb.ResourceError {
	if err == nil {
		return nil
	}
	var roe *resource.ResourceOperationError
	if errors.As(err, &roe) {
		return resourceErrorToProto(roe)
	}
	return &commonpb.ResourceError{
		Code:    6, // INTERNAL
		Title:   "Internal error",
		Message: err.Error(),
	}
}

// ============================================================================
// Filter converters
// ============================================================================

var filterOpToProto = map[resource.FilterOperator]resourcepb.FilterOperator{
	resource.OpEqual:          resourcepb.FilterOperator_FILTER_OP_EQ,
	resource.OpNotEqual:       resourcepb.FilterOperator_FILTER_OP_NEQ,
	resource.OpGreaterThan:    resourcepb.FilterOperator_FILTER_OP_GT,
	resource.OpGreaterOrEqual: resourcepb.FilterOperator_FILTER_OP_GTE,
	resource.OpLessThan:       resourcepb.FilterOperator_FILTER_OP_LT,
	resource.OpLessOrEqual:    resourcepb.FilterOperator_FILTER_OP_LTE,
	resource.OpContains:       resourcepb.FilterOperator_FILTER_OP_CONTAINS,
	resource.OpPrefix:         resourcepb.FilterOperator_FILTER_OP_PREFIX,
	resource.OpSuffix:         resourcepb.FilterOperator_FILTER_OP_SUFFIX,
	resource.OpRegex:          resourcepb.FilterOperator_FILTER_OP_REGEX,
	resource.OpIn:             resourcepb.FilterOperator_FILTER_OP_IN,
	resource.OpNotIn:          resourcepb.FilterOperator_FILTER_OP_NOT_IN,
	resource.OpExists:         resourcepb.FilterOperator_FILTER_OP_EXISTS,
	resource.OpNotExists:      resourcepb.FilterOperator_FILTER_OP_NOT_EXISTS,
	resource.OpHasKey:         resourcepb.FilterOperator_FILTER_OP_HAS_KEY,
}

var filterOpFromProto = map[resourcepb.FilterOperator]resource.FilterOperator{
	resourcepb.FilterOperator_FILTER_OP_EQ:         resource.OpEqual,
	resourcepb.FilterOperator_FILTER_OP_NEQ:        resource.OpNotEqual,
	resourcepb.FilterOperator_FILTER_OP_GT:         resource.OpGreaterThan,
	resourcepb.FilterOperator_FILTER_OP_GTE:        resource.OpGreaterOrEqual,
	resourcepb.FilterOperator_FILTER_OP_LT:         resource.OpLessThan,
	resourcepb.FilterOperator_FILTER_OP_LTE:        resource.OpLessOrEqual,
	resourcepb.FilterOperator_FILTER_OP_CONTAINS:   resource.OpContains,
	resourcepb.FilterOperator_FILTER_OP_PREFIX:     resource.OpPrefix,
	resourcepb.FilterOperator_FILTER_OP_SUFFIX:     resource.OpSuffix,
	resourcepb.FilterOperator_FILTER_OP_REGEX:      resource.OpRegex,
	resourcepb.FilterOperator_FILTER_OP_IN:         resource.OpIn,
	resourcepb.FilterOperator_FILTER_OP_NOT_IN:     resource.OpNotIn,
	resourcepb.FilterOperator_FILTER_OP_EXISTS:     resource.OpExists,
	resourcepb.FilterOperator_FILTER_OP_NOT_EXISTS: resource.OpNotExists,
	resourcepb.FilterOperator_FILTER_OP_HAS_KEY:    resource.OpHasKey,
}

var filterFieldTypeToProto = map[resource.FilterFieldType]resourcepb.FilterFieldType{
	resource.FilterFieldString: resourcepb.FilterFieldType_FILTER_FIELD_TYPE_STRING,
	resource.FilterFieldInt:    resourcepb.FilterFieldType_FILTER_FIELD_TYPE_INTEGER,
	resource.FilterFieldFloat:  resourcepb.FilterFieldType_FILTER_FIELD_TYPE_NUMBER,
	resource.FilterFieldBool:   resourcepb.FilterFieldType_FILTER_FIELD_TYPE_BOOLEAN,
	resource.FilterFieldTime:   resourcepb.FilterFieldType_FILTER_FIELD_TYPE_DATETIME,
	resource.FilterFieldEnum:   resourcepb.FilterFieldType_FILTER_FIELD_TYPE_ENUM,
	resource.FilterFieldMap:    resourcepb.FilterFieldType_FILTER_FIELD_TYPE_MAP,
}

var filterFieldTypeFromProto = map[resourcepb.FilterFieldType]resource.FilterFieldType{
	resourcepb.FilterFieldType_FILTER_FIELD_TYPE_STRING:   resource.FilterFieldString,
	resourcepb.FilterFieldType_FILTER_FIELD_TYPE_INTEGER:  resource.FilterFieldInt,
	resourcepb.FilterFieldType_FILTER_FIELD_TYPE_NUMBER:   resource.FilterFieldFloat,
	resourcepb.FilterFieldType_FILTER_FIELD_TYPE_BOOLEAN:  resource.FilterFieldBool,
	resourcepb.FilterFieldType_FILTER_FIELD_TYPE_DATETIME: resource.FilterFieldTime,
	resourcepb.FilterFieldType_FILTER_FIELD_TYPE_ENUM:     resource.FilterFieldEnum,
	resourcepb.FilterFieldType_FILTER_FIELD_TYPE_MAP:      resource.FilterFieldMap,
}

var filterLogicToProto = map[resource.FilterLogic]resourcepb.FilterLogic{
	resource.FilterAnd: resourcepb.FilterLogic_FILTER_LOGIC_AND,
	resource.FilterOr:  resourcepb.FilterLogic_FILTER_LOGIC_OR,
}

var filterLogicFromProto = map[resourcepb.FilterLogic]resource.FilterLogic{
	resourcepb.FilterLogic_FILTER_LOGIC_AND: resource.FilterAnd,
	resourcepb.FilterLogic_FILTER_LOGIC_OR:  resource.FilterOr,
}

func filterFieldToProto(f resource.FilterField) *resourcepb.FilterField {
	ops := make([]resourcepb.FilterOperator, len(f.Operators))
	for i, op := range f.Operators {
		ops[i] = filterOpToProto[op]
	}
	return &resourcepb.FilterField{
		Path:          f.Path,
		DisplayName:   f.DisplayName,
		Description:   f.Description,
		Type:          filterFieldTypeToProto[f.Type],
		Operators:     ops,
		AllowedValues: f.AllowedValues,
		Required:      f.Required,
	}
}

func filterFieldFromProto(pb *resourcepb.FilterField) resource.FilterField {
	if pb == nil {
		return resource.FilterField{}
	}
	ops := make([]resource.FilterOperator, len(pb.GetOperators()))
	for i, op := range pb.GetOperators() {
		ops[i] = filterOpFromProto[op]
	}
	return resource.FilterField{
		Path:          pb.GetPath(),
		DisplayName:   pb.GetDisplayName(),
		Description:   pb.GetDescription(),
		Type:          filterFieldTypeFromProto[pb.GetType()],
		Operators:     ops,
		AllowedValues: pb.GetAllowedValues(),
		Required:      pb.GetRequired(),
	}
}

func filterPredicateToProto(p resource.FilterPredicate) *resourcepb.FilterPredicate {
	var val *structpb.Value
	if p.Value != nil {
		val, _ = structpb.NewValue(p.Value)
	}
	return &resourcepb.FilterPredicate{
		Field:    p.Field,
		Operator: filterOpToProto[p.Operator],
		Value:    val,
	}
}

func filterPredicateFromProto(pb *resourcepb.FilterPredicate) resource.FilterPredicate {
	if pb == nil {
		return resource.FilterPredicate{}
	}
	var val interface{}
	if pb.GetValue() != nil {
		val = pb.GetValue().AsInterface()
	}
	return resource.FilterPredicate{
		Field:    pb.GetField(),
		Operator: filterOpFromProto[pb.GetOperator()],
		Value:    val,
	}
}

func filterExpressionToProto(e *resource.FilterExpression) *resourcepb.FilterExpression {
	if e == nil {
		return nil
	}
	preds := make([]*resourcepb.FilterPredicate, len(e.Predicates))
	for i, p := range e.Predicates {
		preds[i] = filterPredicateToProto(p)
	}
	groups := make([]*resourcepb.FilterExpression, len(e.Groups))
	for i, g := range e.Groups {
		groups[i] = filterExpressionToProto(&g)
	}
	return &resourcepb.FilterExpression{
		Logic:      filterLogicToProto[e.Logic],
		Predicates: preds,
		Groups:     groups,
	}
}

func filterExpressionFromProto(pb *resourcepb.FilterExpression) *resource.FilterExpression {
	if pb == nil {
		return nil
	}
	preds := make([]resource.FilterPredicate, len(pb.GetPredicates()))
	for i, p := range pb.GetPredicates() {
		preds[i] = filterPredicateFromProto(p)
	}
	groups := make([]resource.FilterExpression, len(pb.GetGroups()))
	for i, g := range pb.GetGroups() {
		groups[i] = *filterExpressionFromProto(g)
	}
	return &resource.FilterExpression{
		Logic:      filterLogicFromProto[pb.GetLogic()],
		Predicates: preds,
		Groups:     groups,
	}
}

func orderFieldToProto(o resource.OrderField) *resourcepb.OrderField {
	return &resourcepb.OrderField{
		Path:       o.Field,
		Descending: o.Descending,
	}
}

func orderFieldFromProto(pb *resourcepb.OrderField) resource.OrderField {
	if pb == nil {
		return resource.OrderField{}
	}
	return resource.OrderField{
		Field:      pb.GetPath(),
		Descending: pb.GetDescending(),
	}
}

func paginationToProto(p resource.PaginationParams) *resourcepb.PaginationParams {
	return &resourcepb.PaginationParams{
		Page:     int32(p.Page),
		PageSize: int32(p.PageSize),
		Cursor:   p.Cursor,
	}
}

func paginationFromProto(pb *resourcepb.PaginationParams) resource.PaginationParams {
	if pb == nil {
		return resource.PaginationParams{}
	}
	return resource.PaginationParams{
		Page:     int(pb.GetPage()),
		PageSize: int(pb.GetPageSize()),
		Cursor:   pb.GetCursor(),
	}
}

// ============================================================================
// Watch event converters
// ============================================================================

var watchStateToProto = map[resource.WatchState]resourcepb.WatchState{
	resource.WatchStateIdle:    resourcepb.WatchState_WATCH_STATE_STOPPED,
	resource.WatchStateSyncing: resourcepb.WatchState_WATCH_STATE_SYNCING,
	resource.WatchStateSynced:  resourcepb.WatchState_WATCH_STATE_SYNCED,
	resource.WatchStateError:   resourcepb.WatchState_WATCH_STATE_ERROR,
	resource.WatchStateStopped: resourcepb.WatchState_WATCH_STATE_STOPPED,
	resource.WatchStateFailed:  resourcepb.WatchState_WATCH_STATE_FAILED,
}

var watchStateFromProto = map[resourcepb.WatchState]resource.WatchState{
	resourcepb.WatchState_WATCH_STATE_UNSPECIFIED: resource.WatchStateIdle,
	resourcepb.WatchState_WATCH_STATE_SYNCING:     resource.WatchStateSyncing,
	resourcepb.WatchState_WATCH_STATE_SYNCED:      resource.WatchStateSynced,
	resourcepb.WatchState_WATCH_STATE_ERROR:       resource.WatchStateError,
	resourcepb.WatchState_WATCH_STATE_FAILED:      resource.WatchStateFailed,
	resourcepb.WatchState_WATCH_STATE_STOPPED:     resource.WatchStateStopped,
}

func watchConnectionSummaryToProto(s *resource.WatchConnectionSummary) *resourcepb.GetWatchStateResponse {
	if s == nil {
		return &resourcepb.GetWatchStateResponse{}
	}
	resources := make(map[string]resourcepb.WatchState, len(s.Resources))
	for k, v := range s.Resources {
		resources[k] = watchStateToProto[v]
	}
	return &resourcepb.GetWatchStateResponse{
		ConnectionId: s.ConnectionID,
		Resources:    resources,
	}
}

func watchConnectionSummaryFromProto(pb *resourcepb.GetWatchStateResponse) *resource.WatchConnectionSummary {
	if pb == nil {
		return nil
	}
	resources := make(map[string]resource.WatchState, len(pb.GetResources()))
	for k, v := range pb.GetResources() {
		resources[k] = watchStateFromProto[v]
	}
	return &resource.WatchConnectionSummary{
		ConnectionID: pb.GetConnectionId(),
		Resources:    resources,
	}
}

// ============================================================================
// Action converters
// ============================================================================

var actionScopeToProto = map[resource.ActionScope]resourcepb.ActionScope{
	resource.ActionScopeInstance: resourcepb.ActionScope_ACTION_SCOPE_INSTANCE,
	resource.ActionScopeType:    resourcepb.ActionScope_ACTION_SCOPE_COLLECTION,
}

var actionScopeFromProto = map[resourcepb.ActionScope]resource.ActionScope{
	resourcepb.ActionScope_ACTION_SCOPE_INSTANCE:   resource.ActionScopeInstance,
	resourcepb.ActionScope_ACTION_SCOPE_COLLECTION: resource.ActionScopeType,
	resourcepb.ActionScope_ACTION_SCOPE_GLOBAL:     resource.ActionScopeType,
}

func actionDescriptorToProto(d resource.ActionDescriptor) *resourcepb.ActionDescriptor {
	return &resourcepb.ActionDescriptor{
		Id:           d.ID,
		Label:        d.Label,
		Description:  d.Description,
		Icon:         d.Icon,
		Scope:        actionScopeToProto[d.Scope],
		Streaming:    d.Streaming,
		ParamsSchema: d.ParamsSchema,
		OutputSchema: d.OutputSchema,
		Dangerous:    d.Dangerous,
	}
}

func actionDescriptorFromProto(pb *resourcepb.ActionDescriptor) resource.ActionDescriptor {
	if pb == nil {
		return resource.ActionDescriptor{}
	}
	return resource.ActionDescriptor{
		ID:           pb.GetId(),
		Label:        pb.GetLabel(),
		Description:  pb.GetDescription(),
		Icon:         pb.GetIcon(),
		Scope:        actionScopeFromProto[pb.GetScope()],
		Streaming:    pb.GetStreaming(),
		ParamsSchema: pb.GetParamsSchema(),
		OutputSchema: pb.GetOutputSchema(),
		Dangerous:    pb.GetDangerous(),
	}
}

func actionInputToProto(in resource.ActionInput) *resourcepb.ActionInput {
	var params *structpb.Struct
	if in.Params != nil {
		params, _ = structpb.NewStruct(in.Params)
	}
	return &resourcepb.ActionInput{
		ResourceId: in.ID,
		Namespace:  in.Namespace,
		Params:     params,
	}
}

func actionInputFromProto(pb *resourcepb.ActionInput) resource.ActionInput {
	if pb == nil {
		return resource.ActionInput{}
	}
	var params map[string]interface{}
	if pb.GetParams() != nil {
		params = pb.GetParams().AsMap()
	}
	return resource.ActionInput{
		ID:        pb.GetResourceId(),
		Namespace: pb.GetNamespace(),
		Params:    params,
	}
}

func actionResultToProto(r *resource.ActionResult) *resourcepb.ActionResult {
	if r == nil {
		return nil
	}
	var data *structpb.Struct
	if r.Data != nil {
		data, _ = structpb.NewStruct(r.Data)
	}
	return &resourcepb.ActionResult{
		Success: r.Success,
		Message: r.Message,
		Data:    data,
	}
}

func actionResultFromProto(pb *resourcepb.ActionResult) *resource.ActionResult {
	if pb == nil {
		return nil
	}
	var data map[string]interface{}
	if pb.GetData() != nil {
		data = pb.GetData().AsMap()
	}
	return &resource.ActionResult{
		Success: pb.GetSuccess(),
		Message: pb.GetMessage(),
		Data:    data,
	}
}

func actionEventToProto(e resource.ActionEvent) *resourcepb.StreamActionEvent {
	var data *structpb.Struct
	if e.Data != nil {
		data, _ = structpb.NewStruct(e.Data)
	}
	return &resourcepb.StreamActionEvent{
		Type: e.Type,
		Data: data,
	}
}

func actionEventFromProto(pb *resourcepb.StreamActionEvent) resource.ActionEvent {
	if pb == nil {
		return resource.ActionEvent{}
	}
	var data map[string]interface{}
	if pb.GetData() != nil {
		data = pb.GetData().AsMap()
	}
	return resource.ActionEvent{
		Type: pb.GetType(),
		Data: data,
	}
}

// ============================================================================
// Capabilities converters
// ============================================================================

var scaleLevelToProto = map[resource.ScaleLevel]resourcepb.ScaleCategory{
	resource.ScaleFew:      resourcepb.ScaleCategory_SCALE_CATEGORY_FEW,
	resource.ScaleModerate: resourcepb.ScaleCategory_SCALE_CATEGORY_MODERATE,
	resource.ScaleMany:     resourcepb.ScaleCategory_SCALE_CATEGORY_MANY,
}

var scaleLevelFromProto = map[resourcepb.ScaleCategory]resource.ScaleLevel{
	resourcepb.ScaleCategory_SCALE_CATEGORY_FEW:      resource.ScaleFew,
	resourcepb.ScaleCategory_SCALE_CATEGORY_MODERATE: resource.ScaleModerate,
	resourcepb.ScaleCategory_SCALE_CATEGORY_MANY:     resource.ScaleMany,
}

func capabilitiesToProto(c *resource.ResourceCapabilities) *resourcepb.ResourceCapabilities {
	if c == nil {
		return nil
	}
	pb := &resourcepb.ResourceCapabilities{
		CanGet:           c.CanGet,
		CanList:          c.CanList,
		CanFind:          c.CanFind,
		CanCreate:        c.CanCreate,
		CanUpdate:        c.CanUpdate,
		CanDelete:        c.CanDelete,
		Watchable:        c.Watchable,
		Filterable:       c.Filterable,
		Searchable:       c.Searchable,
		HasActions:       c.HasActions,
		HasSchema:        c.HasSchema,
		NamespaceScoped:  c.NamespaceScoped,
		HasRelationships: c.HasRelationships,
		HasHealth:        c.HasHealth,
		HasEvents:        c.HasEvents,
	}
	if c.Scale != nil {
		pb.ScaleHint = &resourcepb.ScaleHint{
			ExpectedCount:  scaleLevelToProto[c.Scale.Level],
			DefaultPageSize: int32(c.Scale.DefaultPageSize),
		}
	}
	return pb
}

func capabilitiesFromProto(pb *resourcepb.ResourceCapabilities) *resource.ResourceCapabilities {
	if pb == nil {
		return nil
	}
	c := &resource.ResourceCapabilities{
		CanGet:           pb.GetCanGet(),
		CanList:          pb.GetCanList(),
		CanFind:          pb.GetCanFind(),
		CanCreate:        pb.GetCanCreate(),
		CanUpdate:        pb.GetCanUpdate(),
		CanDelete:        pb.GetCanDelete(),
		Watchable:        pb.GetWatchable(),
		Filterable:       pb.GetFilterable(),
		Searchable:       pb.GetSearchable(),
		HasActions:       pb.GetHasActions(),
		HasSchema:        pb.GetHasSchema(),
		NamespaceScoped:  pb.GetNamespaceScoped(),
		HasRelationships: pb.GetHasRelationships(),
		HasHealth:        pb.GetHasHealth(),
		HasEvents:        pb.GetHasEvents(),
	}
	if pb.GetScaleHint() != nil {
		c.Scale = &resource.ScaleHint{
			Level:           scaleLevelFromProto[pb.GetScaleHint().GetExpectedCount()],
			DefaultPageSize: int(pb.GetScaleHint().GetDefaultPageSize()),
		}
	}
	return c
}

// ============================================================================
// Editor schema converters
// ============================================================================

func editorSchemaToProto(s resource.EditorSchema) *commonpb.EditorSchema {
	return &commonpb.EditorSchema{
		Uri:       s.URI,
		FileMatch: []string{s.FileMatch},
		Language:  s.Language,
		Schema:    s.Content,
	}
}

func editorSchemaFromProto(pb *commonpb.EditorSchema) resource.EditorSchema {
	if pb == nil {
		return resource.EditorSchema{}
	}
	var fileMatch string
	if len(pb.GetFileMatch()) > 0 {
		fileMatch = pb.GetFileMatch()[0]
	}
	return resource.EditorSchema{
		URI:       pb.GetUri(),
		FileMatch: fileMatch,
		Language:  pb.GetLanguage(),
		Content:   pb.GetSchema(),
	}
}

// ============================================================================
// Relationship converters
// ============================================================================

var relTypeToProto = map[resource.RelationshipType]resourcepb.RelationshipType{
	resource.RelOwns:     resourcepb.RelationshipType_RELATIONSHIP_TYPE_OWNS,
	resource.RelRunsOn:   resourcepb.RelationshipType_RELATIONSHIP_TYPE_RUNS_ON,
	resource.RelUses:     resourcepb.RelationshipType_RELATIONSHIP_TYPE_USES,
	resource.RelExposes:  resourcepb.RelationshipType_RELATIONSHIP_TYPE_EXPOSES,
	resource.RelManages:  resourcepb.RelationshipType_RELATIONSHIP_TYPE_MANAGES,
	resource.RelMemberOf: resourcepb.RelationshipType_RELATIONSHIP_TYPE_MEMBER_OF,
}

var relTypeFromProto = map[resourcepb.RelationshipType]resource.RelationshipType{
	resourcepb.RelationshipType_RELATIONSHIP_TYPE_OWNS:      resource.RelOwns,
	resourcepb.RelationshipType_RELATIONSHIP_TYPE_RUNS_ON:   resource.RelRunsOn,
	resourcepb.RelationshipType_RELATIONSHIP_TYPE_USES:      resource.RelUses,
	resourcepb.RelationshipType_RELATIONSHIP_TYPE_EXPOSES:   resource.RelExposes,
	resourcepb.RelationshipType_RELATIONSHIP_TYPE_MANAGES:   resource.RelManages,
	resourcepb.RelationshipType_RELATIONSHIP_TYPE_MEMBER_OF: resource.RelMemberOf,
}

func relationshipExtractorToProto(e *resource.RelationshipExtractor) *resourcepb.RelationshipExtractor {
	if e == nil {
		return nil
	}
	return &resourcepb.RelationshipExtractor{
		Method:        e.Method,
		FieldPath:     e.FieldPath,
		OwnerRefKind:  e.OwnerRefKind,
		LabelSelector: e.LabelSelector,
	}
}

func relationshipExtractorFromProto(pb *resourcepb.RelationshipExtractor) *resource.RelationshipExtractor {
	if pb == nil {
		return nil
	}
	return &resource.RelationshipExtractor{
		Method:        pb.GetMethod(),
		FieldPath:     pb.GetFieldPath(),
		OwnerRefKind:  pb.GetOwnerRefKind(),
		LabelSelector: pb.GetLabelSelector(),
	}
}

func relationshipDescriptorToProto(d resource.RelationshipDescriptor) *resourcepb.RelationshipDescriptor {
	return &resourcepb.RelationshipDescriptor{
		Type:              relTypeToProto[d.Type],
		TargetResourceKey: d.TargetResourceKey,
		Label:             d.Label,
		InverseLabel:      d.InverseLabel,
		Cardinality:       d.Cardinality,
		Extractor:         relationshipExtractorToProto(d.Extractor),
	}
}

func relationshipDescriptorFromProto(pb *resourcepb.RelationshipDescriptor) resource.RelationshipDescriptor {
	if pb == nil {
		return resource.RelationshipDescriptor{}
	}
	return resource.RelationshipDescriptor{
		Type:              relTypeFromProto[pb.GetType()],
		TargetResourceKey: pb.GetTargetResourceKey(),
		Label:             pb.GetLabel(),
		InverseLabel:      pb.GetInverseLabel(),
		Cardinality:       pb.GetCardinality(),
		Extractor:         relationshipExtractorFromProto(pb.GetExtractor()),
	}
}

func resourceRefToProto(r resource.ResourceRef) *resourcepb.ResourceRef {
	return &resourcepb.ResourceRef{
		PluginId:     r.PluginID,
		ConnectionId: r.ConnectionID,
		ResourceKey:  r.ResourceKey,
		Id:           r.ID,
		Namespace:    r.Namespace,
		DisplayName:  r.DisplayName,
	}
}

func resourceRefFromProto(pb *resourcepb.ResourceRef) resource.ResourceRef {
	if pb == nil {
		return resource.ResourceRef{}
	}
	return resource.ResourceRef{
		PluginID:     pb.GetPluginId(),
		ConnectionID: pb.GetConnectionId(),
		ResourceKey:  pb.GetResourceKey(),
		ID:           pb.GetId(),
		Namespace:    pb.GetNamespace(),
		DisplayName:  pb.GetDisplayName(),
	}
}

func resolvedRelationshipToProto(r resource.ResolvedRelationship) *resourcepb.ResolvedRelationship {
	targets := make([]*resourcepb.ResourceRef, len(r.Targets))
	for i, t := range r.Targets {
		targets[i] = resourceRefToProto(t)
	}
	desc := relationshipDescriptorToProto(r.Descriptor)
	return &resourcepb.ResolvedRelationship{
		Descriptor_: desc,
		Targets:     targets,
	}
}

func resolvedRelationshipFromProto(pb *resourcepb.ResolvedRelationship) resource.ResolvedRelationship {
	if pb == nil {
		return resource.ResolvedRelationship{}
	}
	targets := make([]resource.ResourceRef, len(pb.GetTargets()))
	for i, t := range pb.GetTargets() {
		targets[i] = resourceRefFromProto(t)
	}
	return resource.ResolvedRelationship{
		Descriptor: relationshipDescriptorFromProto(pb.GetDescriptor_()),
		Targets:    targets,
	}
}

// ============================================================================
// Health converters
// ============================================================================

var healthStatusToProto = map[resource.HealthStatus]resourcepb.HealthStatus{
	resource.HealthHealthy:   resourcepb.HealthStatus_HEALTH_STATUS_HEALTHY,
	resource.HealthDegraded:  resourcepb.HealthStatus_HEALTH_STATUS_DEGRADED,
	resource.HealthUnhealthy: resourcepb.HealthStatus_HEALTH_STATUS_UNHEALTHY,
	resource.HealthPending:   resourcepb.HealthStatus_HEALTH_STATUS_PENDING,
	resource.HealthUnknown:   resourcepb.HealthStatus_HEALTH_STATUS_UNKNOWN,
}

var healthStatusFromProto = map[resourcepb.HealthStatus]resource.HealthStatus{
	resourcepb.HealthStatus_HEALTH_STATUS_HEALTHY:   resource.HealthHealthy,
	resourcepb.HealthStatus_HEALTH_STATUS_DEGRADED:  resource.HealthDegraded,
	resourcepb.HealthStatus_HEALTH_STATUS_UNHEALTHY: resource.HealthUnhealthy,
	resourcepb.HealthStatus_HEALTH_STATUS_PENDING:   resource.HealthPending,
	resourcepb.HealthStatus_HEALTH_STATUS_UNKNOWN:   resource.HealthUnknown,
}

var eventSeverityToProto = map[resource.EventSeverity]resourcepb.EventSeverity{
	resource.SeverityNormal:  resourcepb.EventSeverity_EVENT_SEVERITY_NORMAL,
	resource.SeverityWarning: resourcepb.EventSeverity_EVENT_SEVERITY_WARNING,
	resource.SeverityError:   resourcepb.EventSeverity_EVENT_SEVERITY_ERROR,
}

var eventSeverityFromProto = map[resourcepb.EventSeverity]resource.EventSeverity{
	resourcepb.EventSeverity_EVENT_SEVERITY_NORMAL:  resource.SeverityNormal,
	resourcepb.EventSeverity_EVENT_SEVERITY_WARNING: resource.SeverityWarning,
	resourcepb.EventSeverity_EVENT_SEVERITY_ERROR:   resource.SeverityError,
}

func timeToTimestamp(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

func timestampToTime(ts *timestamppb.Timestamp) *time.Time {
	if ts == nil {
		return nil
	}
	t := ts.AsTime()
	return &t
}

func healthConditionToProto(c resource.HealthCondition) *resourcepb.HealthCondition {
	return &resourcepb.HealthCondition{
		Type:               c.Type,
		Status:             c.Status,
		Reason:             c.Reason,
		Message:            c.Message,
		LastProbeTime:      timeToTimestamp(c.LastProbeTime),
		LastTransitionTime: timeToTimestamp(c.LastTransitionTime),
	}
}

func healthConditionFromProto(pb *resourcepb.HealthCondition) resource.HealthCondition {
	if pb == nil {
		return resource.HealthCondition{}
	}
	return resource.HealthCondition{
		Type:               pb.GetType(),
		Status:             pb.GetStatus(),
		Reason:             pb.GetReason(),
		Message:            pb.GetMessage(),
		LastProbeTime:      timestampToTime(pb.GetLastProbeTime()),
		LastTransitionTime: timestampToTime(pb.GetLastTransitionTime()),
	}
}

func resourceHealthToProto(h *resource.ResourceHealth) *resourcepb.ResourceHealth {
	if h == nil {
		return nil
	}
	conds := make([]*resourcepb.HealthCondition, len(h.Conditions))
	for i, c := range h.Conditions {
		conds[i] = healthConditionToProto(c)
	}
	return &resourcepb.ResourceHealth{
		Status:     healthStatusToProto[h.Status],
		Reason:     h.Reason,
		Message:    h.Message,
		Since:      timeToTimestamp(h.Since),
		Conditions: conds,
	}
}

func resourceHealthFromProto(pb *resourcepb.ResourceHealth) *resource.ResourceHealth {
	if pb == nil {
		return nil
	}
	conds := make([]resource.HealthCondition, len(pb.GetConditions()))
	for i, c := range pb.GetConditions() {
		conds[i] = healthConditionFromProto(c)
	}
	return &resource.ResourceHealth{
		Status:     healthStatusFromProto[pb.GetStatus()],
		Reason:     pb.GetReason(),
		Message:    pb.GetMessage(),
		Since:      timestampToTime(pb.GetSince()),
		Conditions: conds,
	}
}

func resourceEventToProto(e resource.ResourceEvent) *resourcepb.ResourceEvent {
	return &resourcepb.ResourceEvent{
		Type:      eventSeverityToProto[e.Type],
		Reason:    e.Reason,
		Message:   e.Message,
		Source:    e.Source,
		Count:     e.Count,
		FirstSeen: timestamppb.New(e.FirstSeen),
		LastSeen:  timestamppb.New(e.LastSeen),
	}
}

func resourceEventFromProto(pb *resourcepb.ResourceEvent) resource.ResourceEvent {
	if pb == nil {
		return resource.ResourceEvent{}
	}
	var firstSeen, lastSeen time.Time
	if pb.GetFirstSeen() != nil {
		firstSeen = pb.GetFirstSeen().AsTime()
	}
	if pb.GetLastSeen() != nil {
		lastSeen = pb.GetLastSeen().AsTime()
	}
	return resource.ResourceEvent{
		Type:      eventSeverityFromProto[pb.GetType()],
		Reason:    pb.GetReason(),
		Message:   pb.GetMessage(),
		Source:    pb.GetSource(),
		Count:     pb.GetCount(),
		FirstSeen: firstSeen,
		LastSeen:  lastSeen,
	}
}

