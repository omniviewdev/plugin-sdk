package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	resourcepb "github.com/omniviewdev/plugin-sdk/proto/v1/resource"

	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// client implements resource.Provider by delegating to a gRPC stub.
type client struct {
	stub resourcepb.ResourcePluginClient
}

// NewClient creates a Provider backed by a gRPC connection.
func NewClient(stub resourcepb.ResourcePluginClient) resource.Provider {
	return &client{stub: stub}
}

// errNotAvailable is returned for Provider methods that have no gRPC RPC.
var errNotAvailable = fmt.Errorf("not available over gRPC")

// ============================================================================
// Connection Lifecycle
// ============================================================================

func (c *client) LoadConnections(ctx context.Context) ([]types.Connection, error) {
	resp, err := c.stub.LoadConnections(ctx, &resourcepb.LoadConnectionsRequest{})
	if err != nil {
		return nil, err
	}
	conns := make([]types.Connection, len(resp.GetConnections()))
	for i, pb := range resp.GetConnections() {
		conns[i] = connectionFromProto(pb)
	}
	return conns, nil
}

func (c *client) StartConnection(ctx context.Context, id string) (types.ConnectionStatus, error) {
	resp, err := c.stub.StartConnection(ctx, &resourcepb.ConnectionRequest{ConnectionId: id})
	if err != nil {
		return types.ConnectionStatus{}, err
	}
	return connectionStatusFromProto(resp.GetStatus()), nil
}

func (c *client) StopConnection(ctx context.Context, id string) (types.Connection, error) {
	resp, err := c.stub.StopConnection(ctx, &resourcepb.ConnectionRequest{ConnectionId: id})
	if err != nil {
		return types.Connection{}, err
	}
	return connectionFromProto(resp.GetConnection()), nil
}

func (c *client) GetConnectionNamespaces(ctx context.Context, id string) ([]string, error) {
	resp, err := c.stub.GetConnectionNamespaces(ctx, &resourcepb.ConnectionRequest{ConnectionId: id})
	if err != nil {
		return nil, err
	}
	return resp.GetNamespaces(), nil
}

func (c *client) ListConnections(_ context.Context) ([]types.Connection, error) {
	return nil, errNotAvailable
}

func (c *client) GetConnection(_ context.Context, _ string) (types.Connection, error) {
	return types.Connection{}, errNotAvailable
}

func (c *client) UpdateConnection(_ context.Context, _ types.Connection) (types.Connection, error) {
	return types.Connection{}, errNotAvailable
}

func (c *client) DeleteConnection(_ context.Context, _ string) error {
	return errNotAvailable
}

func (c *client) WatchConnections(ctx context.Context, stream chan<- []types.Connection) error {
	s, err := c.stub.WatchConnections(ctx, &resourcepb.WatchConnectionsRequest{})
	if err != nil {
		return err
	}
	defer close(stream)
	for {
		resp, err := s.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		conns := make([]types.Connection, len(resp.GetConnections()))
		for i, pb := range resp.GetConnections() {
			conns[i] = connectionFromProto(pb)
		}
		stream <- conns
	}
}

// ============================================================================
// CRUD Operations
// ============================================================================

func (c *client) Get(ctx context.Context, key string, input resource.GetInput) (*resource.GetResult, error) {
	resp, err := c.stub.Get(ctx, &resourcepb.GetRequest{
		ConnectionId: connectionIDFromCtx(ctx),
		ResourceKey:  key,
		Id:           input.ID,
		Namespace:    input.Namespace,
	})
	if err != nil {
		return nil, err
	}
	if resp.GetError() != nil {
		return nil, resourceErrorFromProto(resp.GetError())
	}
	return &resource.GetResult{
		Success: true,
		Result:  json.RawMessage(resp.GetData()),
	}, nil
}

func (c *client) List(ctx context.Context, key string, input resource.ListInput) (*resource.ListResult, error) {
	req := &resourcepb.ListRequest{
		ConnectionId: connectionIDFromCtx(ctx),
		ResourceKey:  key,
		Namespaces:   input.Namespaces,
	}
	for _, o := range input.Order {
		req.Order = append(req.Order, orderFieldToProto(o))
	}
	if input.Pagination != (resource.PaginationParams{}) {
		req.Pagination = paginationToProto(input.Pagination)
	}
	resp, err := c.stub.List(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.GetError() != nil {
		return nil, resourceErrorFromProto(resp.GetError())
	}
	items := make([]json.RawMessage, len(resp.GetItems()))
	for i, item := range resp.GetItems() {
		items[i] = json.RawMessage(item)
	}
	return &resource.ListResult{
		Success:    true,
		Result:     items,
		TotalCount: int(resp.GetTotal()),
		NextCursor: resp.GetNextCursor(),
	}, nil
}

func (c *client) Find(ctx context.Context, key string, input resource.FindInput) (*resource.FindResult, error) {
	req := &resourcepb.FindRequest{
		ConnectionId: connectionIDFromCtx(ctx),
		ResourceKey:  key,
		TextQuery:    input.TextQuery,
		Namespaces:   input.Namespaces,
		Filters:      filterExpressionToProto(input.Filters),
	}
	for _, o := range input.Order {
		req.Order = append(req.Order, orderFieldToProto(o))
	}
	if input.Pagination != (resource.PaginationParams{}) {
		req.Pagination = paginationToProto(input.Pagination)
	}
	resp, err := c.stub.Find(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.GetError() != nil {
		return nil, resourceErrorFromProto(resp.GetError())
	}
	items := make([]json.RawMessage, len(resp.GetItems()))
	for i, item := range resp.GetItems() {
		items[i] = json.RawMessage(item)
	}
	return &resource.FindResult{
		Success:    true,
		Result:     items,
		TotalCount: int(resp.GetTotal()),
		NextCursor: resp.GetNextCursor(),
	}, nil
}

func (c *client) Create(ctx context.Context, key string, input resource.CreateInput) (*resource.CreateResult, error) {
	resp, err := c.stub.Create(ctx, &resourcepb.CreateRequest{
		ConnectionId: connectionIDFromCtx(ctx),
		ResourceKey:  key,
		Data:         input.Input,
		Namespace:    input.Namespace,
	})
	if err != nil {
		return nil, err
	}
	if resp.GetError() != nil {
		return nil, resourceErrorFromProto(resp.GetError())
	}
	return &resource.CreateResult{
		Success: true,
		Result:  json.RawMessage(resp.GetData()),
	}, nil
}

func (c *client) Update(ctx context.Context, key string, input resource.UpdateInput) (*resource.UpdateResult, error) {
	resp, err := c.stub.Update(ctx, &resourcepb.UpdateRequest{
		ConnectionId: connectionIDFromCtx(ctx),
		ResourceKey:  key,
		Data:         input.Input,
		Id:           input.ID,
		Namespace:    input.Namespace,
	})
	if err != nil {
		return nil, err
	}
	if resp.GetError() != nil {
		return nil, resourceErrorFromProto(resp.GetError())
	}
	return &resource.UpdateResult{
		Success: true,
		Result:  json.RawMessage(resp.GetData()),
	}, nil
}

func (c *client) Delete(ctx context.Context, key string, input resource.DeleteInput) (*resource.DeleteResult, error) {
	req := &resourcepb.DeleteRequest{
		ConnectionId: connectionIDFromCtx(ctx),
		ResourceKey:  key,
		Id:           input.ID,
		Namespace:    input.Namespace,
	}
	if input.GracePeriodSeconds != nil {
		gp := int32(*input.GracePeriodSeconds)
		req.GracePeriodSeconds = &gp
	}
	resp, err := c.stub.Delete(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.GetError() != nil {
		return nil, resourceErrorFromProto(resp.GetError())
	}
	return &resource.DeleteResult{
		Success: resp.GetSuccess(),
	}, nil
}

// ============================================================================
// Type Information
// ============================================================================

func (c *client) GetResourceGroups(ctx context.Context, connectionID string) map[string]resource.ResourceGroup {
	resp, err := c.stub.GetResourceGroups(ctx, &resourcepb.ResourceGroupsRequest{ConnectionId: connectionID})
	if err != nil {
		return nil
	}
	groups := make(map[string]resource.ResourceGroup, len(resp.GetGroups()))
	for k, g := range resp.GetGroups() {
		groups[k] = resourceGroupFromProto(g)
	}
	return groups
}

func (c *client) GetResourceTypes(ctx context.Context, connectionID string) map[string]resource.ResourceMeta {
	resp, err := c.stub.GetResourceTypes(ctx, &resourcepb.ResourceTypesRequest{ConnectionId: connectionID})
	if err != nil {
		return nil
	}
	types := make(map[string]resource.ResourceMeta, len(resp.GetTypes()))
	for k, m := range resp.GetTypes() {
		types[k] = resourceMetaFromProto(m)
	}
	return types
}

func (c *client) GetResourceCapabilities(ctx context.Context, key string) (*resource.ResourceCapabilities, error) {
	resp, err := c.stub.GetResourceCapabilities(ctx, &resourcepb.ResourceCapabilitiesRequest{ResourceKey: key})
	if err != nil {
		return nil, err
	}
	return capabilitiesFromProto(resp.GetCapabilities()), nil
}

func (c *client) GetFilterFields(ctx context.Context, connectionID string, key string) ([]resource.FilterField, error) {
	resp, err := c.stub.GetFilterFields(ctx, &resourcepb.FilterFieldsRequest{ConnectionId: connectionID, ResourceKey: key})
	if err != nil {
		return nil, err
	}
	fields := make([]resource.FilterField, len(resp.GetFields()))
	for i, f := range resp.GetFields() {
		fields[i] = filterFieldFromProto(f)
	}
	return fields, nil
}

func (c *client) GetResourceSchema(ctx context.Context, connectionID string, key string) (json.RawMessage, error) {
	resp, err := c.stub.GetResourceSchema(ctx, &resourcepb.ResourceSchemaRequest{ConnectionId: connectionID, ResourceKey: key})
	if err != nil {
		return nil, err
	}
	return json.RawMessage(resp.GetSchema()), nil
}

func (c *client) GetEditorSchemas(ctx context.Context, connectionID string) ([]resource.EditorSchema, error) {
	resp, err := c.stub.GetEditorSchemas(ctx, &resourcepb.EditorSchemasRequest{ConnectionId: connectionID})
	if err != nil {
		return nil, err
	}
	schemas := make([]resource.EditorSchema, len(resp.GetSchemas()))
	for i, s := range resp.GetSchemas() {
		schemas[i] = editorSchemaFromProto(s)
	}
	return schemas, nil
}

func (c *client) GetResourceGroup(_ context.Context, _ string) (resource.ResourceGroup, error) {
	return resource.ResourceGroup{}, errNotAvailable
}

func (c *client) GetResourceType(_ context.Context, _ string) (*resource.ResourceMeta, error) {
	return nil, errNotAvailable
}

func (c *client) HasResourceType(_ context.Context, _ string) bool {
	return false
}

func (c *client) GetResourceDefinition(_ context.Context, _ string) (resource.ResourceDefinition, error) {
	return resource.ResourceDefinition{}, errNotAvailable
}

// ============================================================================
// Actions
// ============================================================================

func (c *client) GetActions(ctx context.Context, key string) ([]resource.ActionDescriptor, error) {
	resp, err := c.stub.GetActions(ctx, &resourcepb.GetActionsRequest{
		ConnectionId: connectionIDFromCtx(ctx),
		ResourceKey:  key,
	})
	if err != nil {
		return nil, err
	}
	actions := make([]resource.ActionDescriptor, len(resp.GetActions()))
	for i, a := range resp.GetActions() {
		actions[i] = actionDescriptorFromProto(a)
	}
	return actions, nil
}

func (c *client) ExecuteAction(ctx context.Context, key string, actionID string, input resource.ActionInput) (*resource.ActionResult, error) {
	resp, err := c.stub.ExecuteAction(ctx, &resourcepb.ExecuteActionRequest{
		ConnectionId: connectionIDFromCtx(ctx),
		ResourceKey:  key,
		ActionId:     actionID,
		Input:        actionInputToProto(input),
	})
	if err != nil {
		return nil, err
	}
	if resp.GetResult().GetError() != nil {
		return nil, resourceErrorFromProto(resp.GetResult().GetError())
	}
	return actionResultFromProto(resp.GetResult()), nil
}

func (c *client) StreamAction(ctx context.Context, key string, actionID string, input resource.ActionInput, stream chan<- resource.ActionEvent) error {
	s, err := c.stub.StreamAction(ctx, &resourcepb.ExecuteActionRequest{
		ConnectionId: connectionIDFromCtx(ctx),
		ResourceKey:  key,
		ActionId:     actionID,
		Input:        actionInputToProto(input),
	})
	if err != nil {
		return err
	}
	defer close(stream)
	for {
		event, err := s.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		stream <- actionEventFromProto(event)
	}
}

// ============================================================================
// Watch
// ============================================================================

func (c *client) ListenForEvents(ctx context.Context, sink resource.WatchEventSink) error {
	s, err := c.stub.ListenForEvents(ctx, &resourcepb.ListenRequest{})
	if err != nil {
		return err
	}
	for {
		event, err := s.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		dispatchWatchEvent(event, sink)
	}
}

// dispatchWatchEvent routes a proto WatchEvent to the appropriate sink method.
func dispatchWatchEvent(event *resourcepb.WatchEvent, sink resource.WatchEventSink) {
	switch e := event.GetEvent().(type) {
	case *resourcepb.WatchEvent_Add:
		sink.OnAdd(resource.WatchAddPayload{
			Connection: event.GetConnectionId(),
			Key:        event.GetResourceKey(),
			ID:         e.Add.GetId(),
			Namespace:  e.Add.GetNamespace(),
			Data:       e.Add.GetData(),
		})
	case *resourcepb.WatchEvent_Update:
		sink.OnUpdate(resource.WatchUpdatePayload{
			Connection: event.GetConnectionId(),
			Key:        event.GetResourceKey(),
			ID:         e.Update.GetId(),
			Namespace:  e.Update.GetNamespace(),
			Data:       e.Update.GetData(),
		})
	case *resourcepb.WatchEvent_Delete:
		sink.OnDelete(resource.WatchDeletePayload{
			Connection: event.GetConnectionId(),
			Key:        event.GetResourceKey(),
			ID:         e.Delete.GetId(),
			Namespace:  e.Delete.GetNamespace(),
			Data:       e.Delete.GetData(),
		})
	case *resourcepb.WatchEvent_State:
		var watchErr error
		if e.State.GetErrorMessage() != "" {
			watchErr = fmt.Errorf("%s", e.State.GetErrorMessage())
		}
		sink.OnStateChange(resource.WatchStateEvent{
			ResourceKey:   event.GetResourceKey(),
			State:         watchStateFromProto[e.State.GetState()],
			ResourceCount: int(e.State.GetResourceCount()),
			Error:         watchErr,
		})
	}
}

func (c *client) EnsureResourceWatch(ctx context.Context, connectionID string, key string) error {
	resp, err := c.stub.EnsureResourceWatch(ctx, &resourcepb.WatchResourceRequest{
		ConnectionId: connectionID,
		ResourceKey:  key,
	})
	if err != nil {
		return err
	}
	if !resp.GetSuccess() {
		return fmt.Errorf("%s", resp.GetErrorMessage())
	}
	return nil
}

func (c *client) StopResourceWatch(ctx context.Context, connectionID string, key string) error {
	resp, err := c.stub.StopResourceWatch(ctx, &resourcepb.WatchResourceRequest{
		ConnectionId: connectionID,
		ResourceKey:  key,
	})
	if err != nil {
		return err
	}
	if !resp.GetSuccess() {
		return fmt.Errorf("%s", resp.GetErrorMessage())
	}
	return nil
}

func (c *client) GetWatchState(ctx context.Context, connectionID string) (*resource.WatchConnectionSummary, error) {
	resp, err := c.stub.GetWatchState(ctx, &resourcepb.GetWatchStateRequest{ConnectionId: connectionID})
	if err != nil {
		return nil, err
	}
	return watchConnectionSummaryFromProto(resp), nil
}

func (c *client) StartConnectionWatch(_ context.Context, _ string) error {
	return errNotAvailable
}

func (c *client) StopConnectionWatch(_ context.Context, _ string) error {
	return errNotAvailable
}

func (c *client) HasWatch(_ context.Context, _ string) bool {
	return false
}

func (c *client) RestartResourceWatch(_ context.Context, _ string, _ string) error {
	return errNotAvailable
}

func (c *client) IsResourceWatchRunning(_ context.Context, _ string, _ string) (bool, error) {
	return false, nil
}

// ============================================================================
// Relationships
// ============================================================================

func (c *client) GetRelationships(ctx context.Context, key string) ([]resource.RelationshipDescriptor, error) {
	resp, err := c.stub.GetRelationships(ctx, &resourcepb.RelationshipsRequest{ResourceKey: key})
	if err != nil {
		return nil, err
	}
	rels := make([]resource.RelationshipDescriptor, len(resp.GetRelationships()))
	for i, r := range resp.GetRelationships() {
		rels[i] = relationshipDescriptorFromProto(r)
	}
	return rels, nil
}

func (c *client) ResolveRelationships(ctx context.Context, connectionID string, key string, id string, namespace string) ([]resource.ResolvedRelationship, error) {
	resp, err := c.stub.ResolveRelationships(ctx, &resourcepb.ResolveRelationshipsRequest{
		ConnectionId: connectionID,
		ResourceKey:  key,
		Id:           id,
		Namespace:    namespace,
	})
	if err != nil {
		return nil, err
	}
	resolved := make([]resource.ResolvedRelationship, len(resp.GetRelationships()))
	for i, r := range resp.GetRelationships() {
		resolved[i] = resolvedRelationshipFromProto(r)
	}
	return resolved, nil
}

// ============================================================================
// Health
// ============================================================================

func (c *client) GetHealth(ctx context.Context, connectionID string, key string, data json.RawMessage) (*resource.ResourceHealth, error) {
	resp, err := c.stub.GetHealth(ctx, &resourcepb.HealthRequest{
		ConnectionId: connectionID,
		ResourceKey:  key,
		Data:         data,
	})
	if err != nil {
		return nil, err
	}
	return resourceHealthFromProto(resp.GetHealth()), nil
}

func (c *client) GetResourceEvents(ctx context.Context, connectionID string, key string, id string, namespace string, limit int32) ([]resource.ResourceEvent, error) {
	resp, err := c.stub.GetResourceEvents(ctx, &resourcepb.ResourceEventsRequest{
		ConnectionId: connectionID,
		ResourceKey:  key,
		Id:           id,
		Namespace:    namespace,
		Limit:        limit,
	})
	if err != nil {
		return nil, err
	}
	events := make([]resource.ResourceEvent, len(resp.GetEvents()))
	for i, e := range resp.GetEvents() {
		events[i] = resourceEventFromProto(e)
	}
	return events, nil
}

// ============================================================================
// Helpers
// ============================================================================

// connectionIDFromCtx extracts the connection ID from the session in context.
func connectionIDFromCtx(ctx context.Context) string {
	sess := resource.SessionFromContext(ctx)
	if sess == nil || sess.Connection == nil {
		return ""
	}
	return sess.Connection.ID
}

// Compile-time check that client satisfies Provider.
var _ resource.Provider = (*client)(nil)
