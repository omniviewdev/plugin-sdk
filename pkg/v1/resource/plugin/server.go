package plugin

import (
	"context"
	"encoding/json"

	commonpb "github.com/omniviewdev/plugin-sdk/proto/v1/common"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/v1/resource"

	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// server implements ResourcePluginServer by delegating to a resource.Provider.
type server struct {
	resourcepb.UnimplementedResourcePluginServer
	provider resource.Provider
}

// NewServer creates a gRPC server wrapping the given Provider.
func NewServer(provider resource.Provider) resourcepb.ResourcePluginServer {
	return &server{provider: provider}
}

// injectSession creates a context with session info from a connection ID.
func injectSession(ctx context.Context, connectionID string) context.Context {
	return resource.WithSession(ctx, &resource.Session{
		Connection: &types.Connection{ID: connectionID},
	})
}

// ============================================================================
// Connection Lifecycle
// ============================================================================

func (s *server) LoadConnections(ctx context.Context, _ *resourcepb.LoadConnectionsRequest) (*resourcepb.LoadConnectionsResponse, error) {
	conns, err := s.provider.LoadConnections(ctx)
	if err != nil {
		return nil, err
	}
	pbConns := make([]*commonpb.Connection, 0, len(conns))
	for _, c := range conns {
		pbConns = append(pbConns, connectionToProto(c))
	}
	return &resourcepb.LoadConnectionsResponse{Connections: pbConns}, nil
}

func (s *server) StartConnection(ctx context.Context, req *resourcepb.ConnectionRequest) (*resourcepb.ConnectionStatusResponse, error) {
	status, err := s.provider.StartConnection(ctx, req.GetConnectionId())
	if err != nil {
		return nil, err
	}
	return &resourcepb.ConnectionStatusResponse{Status: connectionStatusToProto(status)}, nil
}

func (s *server) StopConnection(ctx context.Context, req *resourcepb.ConnectionRequest) (*resourcepb.ConnectionResponse, error) {
	conn, err := s.provider.StopConnection(ctx, req.GetConnectionId())
	if err != nil {
		return nil, err
	}
	return &resourcepb.ConnectionResponse{Connection: connectionToProto(conn)}, nil
}

func (s *server) GetConnectionNamespaces(ctx context.Context, req *resourcepb.ConnectionRequest) (*resourcepb.NamespacesResponse, error) {
	ns, err := s.provider.GetConnectionNamespaces(ctx, req.GetConnectionId())
	if err != nil {
		return nil, err
	}
	return &resourcepb.NamespacesResponse{Namespaces: ns}, nil
}

// ============================================================================
// CRUD Operations
// ============================================================================

func (s *server) Get(ctx context.Context, req *resourcepb.GetRequest) (*resourcepb.GetResponse, error) {
	ctx = injectSession(ctx, req.GetConnectionId())
	input := resource.GetInput{
		ID:        req.GetId(),
		Namespace: req.GetNamespace(),
	}
	result, err := s.provider.Get(ctx, req.GetResourceKey(), input)
	if err != nil {
		return &resourcepb.GetResponse{Error: errorToProtoError(err)}, nil
	}
	resp := &resourcepb.GetResponse{}
	if result != nil {
		resp.Data = result.Result
	}
	return resp, nil
}

func (s *server) List(ctx context.Context, req *resourcepb.ListRequest) (*resourcepb.ListResponse, error) {
	ctx = injectSession(ctx, req.GetConnectionId())
	input := resource.ListInput{
		Namespaces: req.GetNamespaces(),
	}
	for _, o := range req.GetOrder() {
		input.Order = append(input.Order, orderFieldFromProto(o))
	}
	if req.GetPagination() != nil {
		input.Pagination = paginationFromProto(req.GetPagination())
	}
	result, err := s.provider.List(ctx, req.GetResourceKey(), input)
	if err != nil {
		return &resourcepb.ListResponse{Error: errorToProtoError(err)}, nil
	}
	resp := &resourcepb.ListResponse{}
	if result != nil {
		for _, item := range result.Result {
			resp.Items = append(resp.Items, []byte(item))
		}
		resp.Total = int32(result.TotalCount)
		resp.NextCursor = result.NextCursor
	}
	return resp, nil
}

func (s *server) Find(ctx context.Context, req *resourcepb.FindRequest) (*resourcepb.FindResponse, error) {
	ctx = injectSession(ctx, req.GetConnectionId())
	input := resource.FindInput{
		TextQuery:  req.GetTextQuery(),
		Namespaces: req.GetNamespaces(),
		Filters:    filterExpressionFromProto(req.GetFilters()),
	}
	for _, o := range req.GetOrder() {
		input.Order = append(input.Order, orderFieldFromProto(o))
	}
	if req.GetPagination() != nil {
		input.Pagination = paginationFromProto(req.GetPagination())
	}
	result, err := s.provider.Find(ctx, req.GetResourceKey(), input)
	if err != nil {
		return &resourcepb.FindResponse{Error: errorToProtoError(err)}, nil
	}
	resp := &resourcepb.FindResponse{}
	if result != nil {
		for _, item := range result.Result {
			resp.Items = append(resp.Items, []byte(item))
		}
		resp.Total = int32(result.TotalCount)
		resp.NextCursor = result.NextCursor
	}
	return resp, nil
}

func (s *server) Create(ctx context.Context, req *resourcepb.CreateRequest) (*resourcepb.CreateResponse, error) {
	ctx = injectSession(ctx, req.GetConnectionId())
	input := resource.CreateInput{
		Input:     json.RawMessage(req.GetData()),
		Namespace: req.GetNamespace(),
	}
	result, err := s.provider.Create(ctx, req.GetResourceKey(), input)
	if err != nil {
		return &resourcepb.CreateResponse{Error: errorToProtoError(err)}, nil
	}
	resp := &resourcepb.CreateResponse{}
	if result != nil {
		resp.Data = result.Result
	}
	return resp, nil
}

func (s *server) Update(ctx context.Context, req *resourcepb.UpdateRequest) (*resourcepb.UpdateResponse, error) {
	ctx = injectSession(ctx, req.GetConnectionId())
	input := resource.UpdateInput{
		Input:     json.RawMessage(req.GetData()),
		ID:        req.GetId(),
		Namespace: req.GetNamespace(),
	}
	result, err := s.provider.Update(ctx, req.GetResourceKey(), input)
	if err != nil {
		return &resourcepb.UpdateResponse{Error: errorToProtoError(err)}, nil
	}
	resp := &resourcepb.UpdateResponse{}
	if result != nil {
		resp.Data = result.Result
	}
	return resp, nil
}

func (s *server) Delete(ctx context.Context, req *resourcepb.DeleteRequest) (*resourcepb.DeleteResponse, error) {
	ctx = injectSession(ctx, req.GetConnectionId())
	input := resource.DeleteInput{
		ID:        req.GetId(),
		Namespace: req.GetNamespace(),
	}
	if req.GracePeriodSeconds != nil {
		gp := int64(req.GetGracePeriodSeconds())
		input.GracePeriodSeconds = &gp
	}
	result, err := s.provider.Delete(ctx, req.GetResourceKey(), input)
	if err != nil {
		return &resourcepb.DeleteResponse{Error: errorToProtoError(err)}, nil
	}
	resp := &resourcepb.DeleteResponse{}
	if result != nil {
		resp.Success = result.Success
	}
	return resp, nil
}

// ============================================================================
// Type Information
// ============================================================================

func (s *server) GetResourceGroups(ctx context.Context, req *resourcepb.ResourceGroupsRequest) (*resourcepb.ResourceGroupsResponse, error) {
	groups := s.provider.GetResourceGroups(ctx, req.GetConnectionId())
	pbGroups := make(map[string]*commonpb.ResourceGroup, len(groups))
	for k, g := range groups {
		pbGroups[k] = resourceGroupToProto(g)
	}
	return &resourcepb.ResourceGroupsResponse{Groups: pbGroups}, nil
}

func (s *server) GetResourceTypes(ctx context.Context, req *resourcepb.ResourceTypesRequest) (*resourcepb.ResourceTypesResponse, error) {
	types := s.provider.GetResourceTypes(ctx, req.GetConnectionId())
	pbTypes := make(map[string]*commonpb.ResourceMeta, len(types))
	for k, m := range types {
		pbTypes[k] = resourceMetaToProto(m)
	}
	return &resourcepb.ResourceTypesResponse{Types: pbTypes}, nil
}

func (s *server) GetResourceCapabilities(ctx context.Context, req *resourcepb.ResourceCapabilitiesRequest) (*resourcepb.ResourceCapabilitiesResponse, error) {
	caps, err := s.provider.GetResourceCapabilities(ctx, req.GetResourceKey())
	if err != nil {
		return nil, err
	}
	return &resourcepb.ResourceCapabilitiesResponse{Capabilities: capabilitiesToProto(caps)}, nil
}

func (s *server) GetFilterFields(ctx context.Context, req *resourcepb.FilterFieldsRequest) (*resourcepb.FilterFieldsResponse, error) {
	fields, err := s.provider.GetFilterFields(ctx, req.GetConnectionId(), req.GetResourceKey())
	if err != nil {
		return nil, err
	}
	pbFields := make([]*resourcepb.FilterField, len(fields))
	for i, f := range fields {
		pbFields[i] = filterFieldToProto(f)
	}
	return &resourcepb.FilterFieldsResponse{Fields: pbFields}, nil
}

func (s *server) GetResourceSchema(ctx context.Context, req *resourcepb.ResourceSchemaRequest) (*resourcepb.ResourceSchemaResponse, error) {
	schema, err := s.provider.GetResourceSchema(ctx, req.GetConnectionId(), req.GetResourceKey())
	if err != nil {
		return nil, err
	}
	return &resourcepb.ResourceSchemaResponse{Schema: schema}, nil
}

func (s *server) GetEditorSchemas(ctx context.Context, req *resourcepb.EditorSchemasRequest) (*resourcepb.EditorSchemasResponse, error) {
	schemas, err := s.provider.GetEditorSchemas(ctx, req.GetConnectionId())
	if err != nil {
		return nil, err
	}
	pbSchemas := make([]*commonpb.EditorSchema, len(schemas))
	for i, sc := range schemas {
		pbSchemas[i] = editorSchemaToProto(sc)
	}
	return &resourcepb.EditorSchemasResponse{Schemas: pbSchemas}, nil
}

// ============================================================================
// Actions
// ============================================================================

func (s *server) GetActions(ctx context.Context, req *resourcepb.GetActionsRequest) (*resourcepb.GetActionsResponse, error) {
	ctx = injectSession(ctx, req.GetConnectionId())
	actions, err := s.provider.GetActions(ctx, req.GetResourceKey())
	if err != nil {
		return nil, err
	}
	pbActions := make([]*resourcepb.ActionDescriptor, len(actions))
	for i, a := range actions {
		pbActions[i] = actionDescriptorToProto(a)
	}
	return &resourcepb.GetActionsResponse{Actions: pbActions}, nil
}

func (s *server) ExecuteAction(ctx context.Context, req *resourcepb.ExecuteActionRequest) (*resourcepb.ExecuteActionResponse, error) {
	ctx = injectSession(ctx, req.GetConnectionId())
	input := actionInputFromProto(req.GetInput())
	result, err := s.provider.ExecuteAction(ctx, req.GetResourceKey(), req.GetActionId(), input)
	if err != nil {
		return &resourcepb.ExecuteActionResponse{
			Result: &resourcepb.ActionResult{
				Error: errorToProtoError(err),
			},
		}, nil
	}
	return &resourcepb.ExecuteActionResponse{Result: actionResultToProto(result)}, nil
}

func (s *server) StreamAction(req *resourcepb.ExecuteActionRequest, stream resourcepb.ResourcePlugin_StreamActionServer) error {
	ctx := injectSession(stream.Context(), req.GetConnectionId())
	input := actionInputFromProto(req.GetInput())

	ch := make(chan resource.ActionEvent, 16)
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.provider.StreamAction(ctx, req.GetResourceKey(), req.GetActionId(), input, ch)
	}()

	for event := range ch {
		if err := stream.Send(actionEventToProto(event)); err != nil {
			return err
		}
	}
	return <-errCh
}

// ============================================================================
// Watch
// ============================================================================

// grpcWatchSink adapts WatchEventSink to send via the ListenForEvents stream.
type grpcWatchSink struct {
	stream resourcepb.ResourcePlugin_ListenForEventsServer
}

func (s *grpcWatchSink) OnAdd(payload resource.WatchAddPayload) {
	_ = s.stream.Send(&resourcepb.WatchEvent{
		ConnectionId: payload.Connection,
		ResourceKey:  payload.Key,
		Event: &resourcepb.WatchEvent_Add{
			Add: &resourcepb.WatchAddPayload{
				Id:        payload.ID,
				Namespace: payload.Namespace,
				Data:      payload.Data,
			},
		},
	})
}

func (s *grpcWatchSink) OnUpdate(payload resource.WatchUpdatePayload) {
	_ = s.stream.Send(&resourcepb.WatchEvent{
		ConnectionId: payload.Connection,
		ResourceKey:  payload.Key,
		Event: &resourcepb.WatchEvent_Update{
			Update: &resourcepb.WatchUpdatePayload{
				Id:        payload.ID,
				Namespace: payload.Namespace,
				Data:      payload.Data,
			},
		},
	})
}

func (s *grpcWatchSink) OnDelete(payload resource.WatchDeletePayload) {
	_ = s.stream.Send(&resourcepb.WatchEvent{
		ConnectionId: payload.Connection,
		ResourceKey:  payload.Key,
		Event: &resourcepb.WatchEvent_Delete{
			Delete: &resourcepb.WatchDeletePayload{
				Id:        payload.ID,
				Namespace: payload.Namespace,
				Data:      payload.Data,
			},
		},
	})
}

func (s *grpcWatchSink) OnStateChange(event resource.WatchStateEvent) {
	var errMsg string
	if event.Error != nil {
		errMsg = event.Error.Error()
	}
	_ = s.stream.Send(&resourcepb.WatchEvent{
		ResourceKey: event.ResourceKey,
		Event: &resourcepb.WatchEvent_State{
			State: &resourcepb.WatchStateEvent{
				State:         watchStateToProto[event.State],
				ResourceCount: int32(event.ResourceCount),
				ErrorMessage:  errMsg,
			},
		},
	})
}

func (s *server) ListenForEvents(_ *resourcepb.ListenRequest, stream resourcepb.ResourcePlugin_ListenForEventsServer) error {
	sink := &grpcWatchSink{stream: stream}
	return s.provider.ListenForEvents(stream.Context(), sink)
}

func (s *server) EnsureResourceWatch(ctx context.Context, req *resourcepb.WatchResourceRequest) (*resourcepb.WatchResourceResponse, error) {
	err := s.provider.EnsureResourceWatch(ctx, req.GetConnectionId(), req.GetResourceKey())
	if err != nil {
		return &resourcepb.WatchResourceResponse{Success: false, ErrorMessage: err.Error()}, nil
	}
	return &resourcepb.WatchResourceResponse{Success: true}, nil
}

func (s *server) StopResourceWatch(ctx context.Context, req *resourcepb.WatchResourceRequest) (*resourcepb.WatchResourceResponse, error) {
	err := s.provider.StopResourceWatch(ctx, req.GetConnectionId(), req.GetResourceKey())
	if err != nil {
		return &resourcepb.WatchResourceResponse{Success: false, ErrorMessage: err.Error()}, nil
	}
	return &resourcepb.WatchResourceResponse{Success: true}, nil
}

func (s *server) WatchConnections(_ *resourcepb.WatchConnectionsRequest, stream resourcepb.ResourcePlugin_WatchConnectionsServer) error {
	ch := make(chan []types.Connection, 16)
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.provider.WatchConnections(stream.Context(), ch)
	}()

	for conns := range ch {
		pbConns := make([]*commonpb.Connection, len(conns))
		for i, c := range conns {
			pbConns[i] = connectionToProto(c)
		}
		if err := stream.Send(&resourcepb.WatchConnectionsResponse{Connections: pbConns}); err != nil {
			return err
		}
	}
	return <-errCh
}

func (s *server) GetWatchState(ctx context.Context, req *resourcepb.GetWatchStateRequest) (*resourcepb.GetWatchStateResponse, error) {
	summary, err := s.provider.GetWatchState(ctx, req.GetConnectionId())
	if err != nil {
		return nil, err
	}
	return watchConnectionSummaryToProto(summary), nil
}

// ============================================================================
// Relationships
// ============================================================================

func (s *server) GetRelationships(ctx context.Context, req *resourcepb.RelationshipsRequest) (*resourcepb.RelationshipsResponse, error) {
	rels, err := s.provider.GetRelationships(ctx, req.GetResourceKey())
	if err != nil {
		return nil, err
	}
	pbRels := make([]*resourcepb.RelationshipDescriptor, len(rels))
	for i, r := range rels {
		pbRels[i] = relationshipDescriptorToProto(r)
	}
	return &resourcepb.RelationshipsResponse{Relationships: pbRels}, nil
}

func (s *server) ResolveRelationships(ctx context.Context, req *resourcepb.ResolveRelationshipsRequest) (*resourcepb.ResolveRelationshipsResponse, error) {
	resolved, err := s.provider.ResolveRelationships(ctx, req.GetConnectionId(), req.GetResourceKey(), req.GetId(), req.GetNamespace())
	if err != nil {
		return nil, err
	}
	pbResolved := make([]*resourcepb.ResolvedRelationship, len(resolved))
	for i, r := range resolved {
		pbResolved[i] = resolvedRelationshipToProto(r)
	}
	return &resourcepb.ResolveRelationshipsResponse{Relationships: pbResolved}, nil
}

// ============================================================================
// Health
// ============================================================================

func (s *server) GetHealth(ctx context.Context, req *resourcepb.HealthRequest) (*resourcepb.HealthResponse, error) {
	health, err := s.provider.GetHealth(ctx, req.GetConnectionId(), req.GetResourceKey(), json.RawMessage(req.GetData()))
	if err != nil {
		return nil, err
	}
	return &resourcepb.HealthResponse{Health: resourceHealthToProto(health)}, nil
}

func (s *server) GetResourceEvents(ctx context.Context, req *resourcepb.ResourceEventsRequest) (*resourcepb.ResourceEventsResponse, error) {
	events, err := s.provider.GetResourceEvents(ctx, req.GetConnectionId(), req.GetResourceKey(), req.GetId(), req.GetNamespace(), req.GetLimit())
	if err != nil {
		return nil, err
	}
	pbEvents := make([]*resourcepb.ResourceEvent, len(events))
	for i, e := range events {
		pbEvents[i] = resourceEventToProto(e)
	}
	return &resourcepb.ResourceEventsResponse{Events: pbEvents}, nil
}

