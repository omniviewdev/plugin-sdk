package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	logging "github.com/omniviewdev/plugin-sdk/log"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/v1/resource"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
)

// client implements resource.Provider by delegating to a gRPC stub.
type client struct {
	stub resourcepb.ResourcePluginClient
	log  logging.Logger
}

// NewClient creates a Provider backed by a gRPC connection.
func NewClient(stub resourcepb.ResourcePluginClient) resource.Provider {
	return &client{stub: stub, log: logging.Default().Named("resource.plugin.client")}
}

// errNotAvailable is returned for Provider methods that have no gRPC RPC.
var errNotAvailable = &resource.ResourceOperationError{Code: "UNSUPPORTED", Title: "Unsupported operation", Message: "This operation is not available over the resource transport."}

// ============================================================================
// Connection Lifecycle
// ============================================================================

func (c *client) LoadConnections(ctx context.Context) ([]types.Connection, error) {
	resp, err := c.stub.LoadConnections(outgoingSession(ctx), &resourcepb.LoadConnectionsRequest{})
	if err != nil {
		return nil, decodeRPCError(err)
	}
	conns := make([]types.Connection, len(resp.GetConnections()))
	for i, pb := range resp.GetConnections() {
		conns[i] = connectionFromProto(pb)
	}
	return conns, nil
}

func (c *client) StartConnection(ctx context.Context, id string) (types.ConnectionStatus, error) {
	resp, err := c.stub.StartConnection(outgoingSession(ctx), &resourcepb.ConnectionRequest{ConnectionId: id})
	if err != nil {
		return types.ConnectionStatus{}, decodeRPCError(err)
	}
	return connectionStatusFromProto(resp.GetStatus()), nil
}

func (c *client) CheckConnection(ctx context.Context, id string) (types.ConnectionStatus, error) {
	resp, err := c.stub.CheckConnection(outgoingSession(ctx), &resourcepb.CheckConnectionRequest{ConnectionId: id})
	if err != nil {
		return types.ConnectionStatus{}, decodeRPCError(err)
	}
	return connectionStatusFromProto(resp.GetStatus()), nil
}

func (c *client) StopConnection(ctx context.Context, id string) (types.Connection, error) {
	resp, err := c.stub.StopConnection(outgoingSession(ctx), &resourcepb.ConnectionRequest{ConnectionId: id})
	if err != nil {
		return types.Connection{}, decodeRPCError(err)
	}
	return connectionFromProto(resp.GetConnection()), nil
}

func (c *client) GetConnectionNamespaces(ctx context.Context, id string) ([]string, error) {
	resp, err := c.stub.GetConnectionNamespaces(outgoingSession(ctx), &resourcepb.ConnectionRequest{ConnectionId: id})
	if err != nil {
		return nil, decodeRPCError(err)
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
	s, err := c.stub.WatchConnections(outgoingSession(ctx), &resourcepb.WatchConnectionsRequest{})
	if err != nil {
		return decodeRPCError(err)
	}
	defer close(stream)
	for {
		resp, err := s.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return decodeRPCError(err)
		}
		conns := make([]types.Connection, len(resp.GetConnections()))
		for i, pb := range resp.GetConnections() {
			conns[i] = connectionFromProto(pb)
		}
		select {
		case stream <- conns:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// ============================================================================
// CRUD Operations
// ============================================================================

func (c *client) Get(ctx context.Context, key string, input resource.GetInput) (*resource.GetResult, error) {
	connID, err := connectionIDFromCtx(ctx)
	if err != nil {
		return nil, decodeRPCError(err)
	}
	resp, err := c.stub.Get(outgoingSession(ctx), &resourcepb.GetRequest{
		ConnectionId: connID,
		ResourceKey:  key,
		Id:           input.ID,
		Namespace:    input.Namespace,
	})
	if err != nil {
		return nil, decodeRPCError(err)
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
	connID, err := connectionIDFromCtx(ctx)
	if err != nil {
		return nil, decodeRPCError(err)
	}
	req := &resourcepb.ListRequest{
		ConnectionId: connID,
		ResourceKey:  key,
		Namespaces:   input.Namespaces,
	}
	for _, o := range input.Order {
		req.Order = append(req.Order, orderFieldToProto(o))
	}
	if input.Pagination != (resource.PaginationParams{}) {
		req.Pagination = paginationToProto(input.Pagination)
	}
	resp, err := c.stub.List(outgoingSession(ctx), req)
	if err != nil {
		return nil, decodeRPCError(err)
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
	connID, err := connectionIDFromCtx(ctx)
	if err != nil {
		return nil, decodeRPCError(err)
	}
	filters, err := filterExpressionToProto(input.Filters)
	if err != nil {
		return nil, decodeRPCError(err)
	}
	req := &resourcepb.FindRequest{
		ConnectionId: connID,
		ResourceKey:  key,
		TextQuery:    input.TextQuery,
		Namespaces:   input.Namespaces,
		Filters:      filters,
	}
	for _, o := range input.Order {
		req.Order = append(req.Order, orderFieldToProto(o))
	}
	if input.Pagination != (resource.PaginationParams{}) {
		req.Pagination = paginationToProto(input.Pagination)
	}
	resp, err := c.stub.Find(outgoingSession(ctx), req)
	if err != nil {
		return nil, decodeRPCError(err)
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
	connID, err := connectionIDFromCtx(ctx)
	if err != nil {
		return nil, decodeRPCError(err)
	}
	resp, err := c.stub.Create(outgoingSession(ctx), &resourcepb.CreateRequest{
		ConnectionId: connID,
		ResourceKey:  key,
		Data:         input.Input,
		Namespace:    input.Namespace,
	})
	if err != nil {
		return nil, decodeRPCError(err)
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
	connID, err := connectionIDFromCtx(ctx)
	if err != nil {
		return nil, decodeRPCError(err)
	}
	resp, err := c.stub.Update(outgoingSession(ctx), &resourcepb.UpdateRequest{
		ConnectionId: connID,
		ResourceKey:  key,
		Data:         input.Input,
		Id:           input.ID,
		Namespace:    input.Namespace,
	})
	if err != nil {
		return nil, decodeRPCError(err)
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
	connID, err := connectionIDFromCtx(ctx)
	if err != nil {
		return nil, decodeRPCError(err)
	}
	req := &resourcepb.DeleteRequest{
		ConnectionId: connID,
		ResourceKey:  key,
		Id:           input.ID,
		Namespace:    input.Namespace,
	}
	if input.GracePeriodSeconds != nil {
		gp := int32(*input.GracePeriodSeconds)
		req.GracePeriodSeconds = &gp
	}
	resp, err := c.stub.Delete(outgoingSession(ctx), req)
	if err != nil {
		return nil, decodeRPCError(err)
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

func (c *client) GetResourceGroups(ctx context.Context, connectionID string) (map[string]resource.ResourceGroup, error) {
	resp, err := c.stub.GetResourceGroups(outgoingSession(ctx), &resourcepb.ResourceGroupsRequest{ConnectionId: connectionID})
	if err != nil {
		return nil, decodeRPCError(err)
	}
	groups := make(map[string]resource.ResourceGroup, len(resp.GetGroups()))
	for k, g := range resp.GetGroups() {
		groups[k] = resourceGroupFromProto(g)
	}
	return groups, nil
}

func (c *client) GetResourceTypes(ctx context.Context, connectionID string) (map[string]resource.ResourceMeta, error) {
	resp, err := c.stub.GetResourceTypes(outgoingSession(ctx), &resourcepb.ResourceTypesRequest{ConnectionId: connectionID})
	if err != nil {
		return nil, decodeRPCError(err)
	}
	types := make(map[string]resource.ResourceMeta, len(resp.GetTypes()))
	for k, m := range resp.GetTypes() {
		types[k] = resourceMetaFromProto(m)
	}
	return types, nil
}

func (c *client) GetResourceCapabilities(ctx context.Context, key string) (*resource.ResourceCapabilities, error) {
	resp, err := c.stub.GetResourceCapabilities(outgoingSession(ctx), &resourcepb.ResourceCapabilitiesRequest{ResourceKey: key})
	if err != nil {
		return nil, decodeRPCError(err)
	}
	return capabilitiesFromProto(resp.GetCapabilities()), nil
}

func (c *client) GetFilterFields(ctx context.Context, connectionID string, key string) ([]resource.FilterField, error) {
	resp, err := c.stub.GetFilterFields(outgoingSession(ctx), &resourcepb.FilterFieldsRequest{ConnectionId: connectionID, ResourceKey: key})
	if err != nil {
		return nil, decodeRPCError(err)
	}
	fields := make([]resource.FilterField, len(resp.GetFields()))
	for i, f := range resp.GetFields() {
		fields[i] = filterFieldFromProto(f)
	}
	return fields, nil
}

func (c *client) GetResourceSchema(ctx context.Context, connectionID string, key string) (json.RawMessage, error) {
	resp, err := c.stub.GetResourceSchema(outgoingSession(ctx), &resourcepb.ResourceSchemaRequest{ConnectionId: connectionID, ResourceKey: key})
	if err != nil {
		return nil, decodeRPCError(err)
	}
	return json.RawMessage(resp.GetSchema()), nil
}

func (c *client) GetEditorSchemas(ctx context.Context, connectionID string) ([]resource.EditorSchema, error) {
	resp, err := c.stub.GetEditorSchemas(outgoingSession(ctx), &resourcepb.EditorSchemasRequest{ConnectionId: connectionID})
	if err != nil {
		return nil, decodeRPCError(err)
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

// No HasResourceType RPC exists; unsupported is not authoritative absence.
func (c *client) HasResourceType(_ context.Context, _ string) (bool, error) {
	return false, errNotAvailable
}

func (c *client) GetResourceDefinition(_ context.Context, _ string) (resource.ResourceDefinition, error) {
	return resource.ResourceDefinition{}, errNotAvailable
}

// ============================================================================
// Actions
// ============================================================================

func (c *client) GetActions(ctx context.Context, key string) ([]resource.ActionDescriptor, error) {
	connID, err := connectionIDFromCtx(ctx)
	if err != nil {
		return nil, decodeRPCError(err)
	}
	resp, err := c.stub.GetActions(outgoingSession(ctx), &resourcepb.GetActionsRequest{
		ConnectionId: connID,
		ResourceKey:  key,
	})
	if err != nil {
		return nil, decodeRPCError(err)
	}
	actions := make([]resource.ActionDescriptor, len(resp.GetActions()))
	for i, a := range resp.GetActions() {
		ad, err := actionDescriptorFromProto(a)
		if err != nil {
			return nil, decodeRPCError(err)
		}
		actions[i] = ad
	}
	return actions, nil
}

func (c *client) ExecuteAction(ctx context.Context, key string, actionID string, input resource.ActionInput) (*resource.ActionResult, error) {
	connID, err := connectionIDFromCtx(ctx)
	if err != nil {
		return nil, decodeRPCError(err)
	}
	pbInput, err := actionInputToProto(input)
	if err != nil {
		return nil, decodeRPCError(err)
	}
	resp, err := c.stub.ExecuteAction(outgoingSession(ctx), &resourcepb.ExecuteActionRequest{
		ConnectionId: connID,
		ResourceKey:  key,
		ActionId:     actionID,
		Input:        pbInput,
	})
	if err != nil {
		return nil, decodeRPCError(err)
	}
	if resp.GetResult().GetError() != nil {
		return nil, resourceErrorFromProto(resp.GetResult().GetError())
	}
	return actionResultFromProto(resp.GetResult()), nil
}

func (c *client) StreamAction(ctx context.Context, key string, actionID string, input resource.ActionInput, stream chan<- resource.ActionEvent) error {
	connID, err := connectionIDFromCtx(ctx)
	if err != nil {
		return decodeRPCError(err)
	}
	pbInput, err := actionInputToProto(input)
	if err != nil {
		return decodeRPCError(err)
	}
	s, err := c.stub.StreamAction(outgoingSession(ctx), &resourcepb.ExecuteActionRequest{
		ConnectionId: connID,
		ResourceKey:  key,
		ActionId:     actionID,
		Input:        pbInput,
	})
	if err != nil {
		return decodeRPCError(err)
	}
	defer close(stream)
	for {
		event, err := s.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return decodeRPCError(err)
		}
		select {
		case stream <- actionEventFromProto(event):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// ============================================================================
// Watch
// ============================================================================

func (c *client) ListenForEvents(ctx context.Context, sink resource.WatchEventSink, onReady func() error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s, err := c.stub.ListenForEvents(outgoingSession(ctx), &resourcepb.ListenRequest{})
	if err != nil {
		return decodeRPCError(err)
	}
	headers, err := s.Header()
	if err != nil {
		return decodeRPCError(err)
	}
	if values := headers.Get(watchReadyHeader); len(values) != 1 || values[0] != "1" {
		// A trailers-only failure may return no headers without a Header error.
		// Receive its status rather than hiding the actual startup failure.
		if _, recvErr := s.Recv(); recvErr != nil && recvErr != io.EOF {
			return decodeRPCError(recvErr)
		}
		return fmt.Errorf("watch event stream did not acknowledge listener registration")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if onReady != nil {
		if err := onReady(); err != nil {
			return err
		}
	}
	for {
		event, err := s.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return decodeRPCError(err)
		}
		dispatchWatchEvent(event, sink, c.log)
	}
}

// dispatchWatchEvent routes a proto WatchEvent to the appropriate sink method.
func dispatchWatchEvent(event *resourcepb.WatchEvent, sink resource.WatchEventSink, log logging.Logger) {
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
		goState, ok := watchStateFromProto[e.State.GetState()]
		if !ok {
			goState = resource.WatchStateIdle
			log.Warnw(context.Background(), "unknown proto watch state, falling back to idle",
				"proto_state", e.State.GetState())
		}
		log.Debugw(context.Background(), "received watch state",
			"connection_id", event.GetConnectionId(),
			"resource_key", event.GetResourceKey(),
			"proto_state", e.State.GetState(),
			"go_state", goState,
			"count", e.State.GetResourceCount(),
			"error_code", e.State.GetErrorCode(),
		)
		var watchErr error
		if e.State.GetErrorMessage() != "" {
			watchErr = fmt.Errorf("%s", e.State.GetErrorMessage())
		}
		sink.OnStateChange(resource.WatchStateEvent{
			Connection:    event.GetConnectionId(),
			ResourceKey:   event.GetResourceKey(),
			State:         goState,
			ResourceCount: int(e.State.GetResourceCount()),
			Error:         watchErr,
			ErrorCode:     e.State.GetErrorCode(),
		})
	}
}

func (c *client) EnsureResourceWatch(ctx context.Context, connectionID string, key string) error {
	resp, err := c.stub.EnsureResourceWatch(outgoingSession(ctx), &resourcepb.WatchResourceRequest{
		ConnectionId: connectionID,
		ResourceKey:  key,
	})
	if err != nil {
		return decodeRPCError(err)
	}
	if !resp.GetSuccess() {
		return fmt.Errorf("%s", resp.GetErrorMessage())
	}
	return nil
}

func (c *client) StopResourceWatch(ctx context.Context, connectionID string, key string) error {
	resp, err := c.stub.StopResourceWatch(outgoingSession(ctx), &resourcepb.WatchResourceRequest{
		ConnectionId: connectionID,
		ResourceKey:  key,
	})
	if err != nil {
		return decodeRPCError(err)
	}
	if !resp.GetSuccess() {
		return fmt.Errorf("%s", resp.GetErrorMessage())
	}
	return nil
}

func (c *client) GetWatchState(ctx context.Context, connectionID string) (*resource.WatchConnectionSummary, error) {
	resp, err := c.stub.GetWatchState(outgoingSession(ctx), &resourcepb.GetWatchStateRequest{ConnectionId: connectionID})
	if err != nil {
		return nil, decodeRPCError(err)
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
	return false, errNotAvailable
}

// ============================================================================
// Relationships
// ============================================================================

func (c *client) GetRelationships(ctx context.Context, key string) ([]resource.RelationshipDescriptor, error) {
	resp, err := c.stub.GetRelationships(outgoingSession(ctx), &resourcepb.RelationshipsRequest{ResourceKey: key})
	if err != nil {
		return nil, decodeRPCError(err)
	}
	rels := make([]resource.RelationshipDescriptor, len(resp.GetRelationships()))
	for i, r := range resp.GetRelationships() {
		rel, err := relationshipDescriptorFromProto(r)
		if err != nil {
			return nil, decodeRPCError(err)
		}
		rels[i] = rel
	}
	return rels, nil
}

func (c *client) ResolveRelationships(ctx context.Context, connectionID string, key string, id string, namespace string) ([]resource.ResolvedRelationship, error) {
	resp, err := c.stub.ResolveRelationships(outgoingSession(ctx), &resourcepb.ResolveRelationshipsRequest{
		ConnectionId: connectionID,
		ResourceKey:  key,
		Id:           id,
		Namespace:    namespace,
	})
	if err != nil {
		return nil, decodeRPCError(err)
	}
	resolved := make([]resource.ResolvedRelationship, len(resp.GetRelationships()))
	for i, r := range resp.GetRelationships() {
		rr, err := resolvedRelationshipFromProto(r)
		if err != nil {
			return nil, decodeRPCError(err)
		}
		resolved[i] = rr
	}
	return resolved, nil
}

// ============================================================================
// Health
// ============================================================================

func (c *client) GetHealth(ctx context.Context, connectionID string, key string, data json.RawMessage) (*resource.ResourceHealth, error) {
	resp, err := c.stub.GetHealth(outgoingSession(ctx), &resourcepb.HealthRequest{
		ConnectionId: connectionID,
		ResourceKey:  key,
		Data:         data,
	})
	if err != nil {
		return nil, decodeRPCError(err)
	}
	return resourceHealthFromProto(resp.GetHealth()), nil
}

func (c *client) GetResourceEvents(ctx context.Context, connectionID string, key string, id string, namespace string, limit int32) ([]resource.ResourceEvent, error) {
	resp, err := c.stub.GetResourceEvents(outgoingSession(ctx), &resourcepb.ResourceEventsRequest{
		ConnectionId: connectionID,
		ResourceKey:  key,
		Id:           id,
		Namespace:    namespace,
		Limit:        limit,
	})
	if err != nil {
		return nil, decodeRPCError(err)
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
func connectionIDFromCtx(ctx context.Context) (string, error) {
	sess := resource.SessionFromContext(ctx)
	if sess == nil || sess.Connection == nil {
		return "", nil
	}
	return sess.Connection.ID, nil
}

// Compile-time check that client satisfies Provider.
var _ resource.Provider = (*client)(nil)
