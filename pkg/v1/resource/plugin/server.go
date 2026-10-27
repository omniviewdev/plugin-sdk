package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync"

	"google.golang.org/grpc/metadata"

	commonpb "github.com/omniviewdev/plugin-sdk/proto/v1/common"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/v1/resource"
	"github.com/omniviewdev/plugin-sdk/settings"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
)

// server implements ResourcePluginServer by delegating to a resource.Provider.
type server struct {
	resourcepb.UnimplementedResourcePluginServer
	provider resource.Provider
	settings settings.Provider
	log      logging.Logger
}

// NewServer creates a gRPC server wrapping the given Provider.
func NewServer(provider resource.Provider, sp settings.Provider) resourcepb.ResourcePluginServer {
	return &server{
		provider: provider,
		settings: sp,
		log:      logging.Default().Named("resource.plugin.server"),
	}
}

// clampInt32 safely converts an int to int32, clamping to math.MaxInt32 on overflow.
func clampInt32(v int) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < 0 {
		return 0
	}
	return int32(v)
}

// injectSession creates a context with session info from a connection ID.
func (s *server) injectSession(ctx context.Context, connectionID string) context.Context {
	session := &resource.Session{}
	if existing := resource.SessionFromContext(ctx); existing != nil {
		*session = *existing
	}
	session.Connection = &types.Connection{ID: connectionID}
	session.PluginConfig = s.settings
	return resource.WithSession(ctx, session)
}

// injectSettingsSession creates a session with only PluginConfig — no connection.
// Used for LoadConnections and WatchConnections which need settings but not a
// specific connection context.
func (s *server) injectSettingsSession(ctx context.Context) context.Context {
	session := &resource.Session{}
	if existing := resource.SessionFromContext(ctx); existing != nil {
		*session = *existing
	}
	session.PluginConfig = s.settings
	return resource.WithSession(ctx, session)
}

func (s *server) callContext(ctx context.Context) (context.Context, error) {
	session, err := incomingSession(ctx)
	if err != nil {
		return ctx, err
	}
	if err := ctx.Err(); err != nil {
		return ctx, err
	}
	session.PluginConfig = s.settings
	return resource.WithSession(ctx, session), nil
}

// ============================================================================
// Connection Lifecycle
// ============================================================================

func (s *server) LoadConnections(ctx context.Context, _ *resourcepb.LoadConnectionsRequest) (*resourcepb.LoadConnectionsResponse, error) {
	ctx, contextErr := s.callContext(ctx)
	if contextErr != nil {
		return nil, encodeRPCError(contextErr)
	}
	ctx = s.injectSettingsSession(ctx)
	conns, err := s.provider.LoadConnections(ctx)
	if err != nil {
		return nil, encodeRPCError(err)
	}
	pbConns := make([]*commonpb.Connection, 0, len(conns))
	for _, c := range conns {
		pb, err := connectionToProto(c)
		if err != nil {
			return nil, encodeRPCError(err)
		}
		pbConns = append(pbConns, pb)
	}
	return &resourcepb.LoadConnectionsResponse{Connections: pbConns}, nil

}

func (s *server) StartConnection(ctx context.Context, req *resourcepb.ConnectionRequest) (*resourcepb.ConnectionStatusResponse, error) {
	ctx, contextErr := s.callContext(ctx)
	if contextErr != nil {
		return nil, encodeRPCError(contextErr)
	}
	ctx = s.injectSession(ctx, req.GetConnectionId())
	status, err := s.provider.StartConnection(ctx, req.GetConnectionId())
	if err != nil {
		return nil, encodeRPCError(err)
	}
	pbStatus, err := connectionStatusToProto(status)
	if err != nil {
		return nil, encodeRPCError(err)
	}
	return &resourcepb.ConnectionStatusResponse{Status: pbStatus}, nil

}

func (s *server) CheckConnection(ctx context.Context, req *resourcepb.CheckConnectionRequest) (*resourcepb.CheckConnectionResponse, error) {
	ctx, contextErr := s.callContext(ctx)
	if contextErr != nil {
		return nil, encodeRPCError(contextErr)
	}
	ctx = s.injectSession(ctx, req.GetConnectionId())
	status, err := s.provider.CheckConnection(ctx, req.GetConnectionId())
	if err != nil {
		return nil, encodeRPCError(err)
	}
	pbStatus, err := connectionStatusToProto(status)
	if err != nil {
		return nil, encodeRPCError(err)
	}
	return &resourcepb.CheckConnectionResponse{Status: pbStatus}, nil

}

func (s *server) StopConnection(ctx context.Context, req *resourcepb.ConnectionRequest) (*resourcepb.ConnectionResponse, error) {
	ctx, contextErr := s.callContext(ctx)
	if contextErr != nil {
		return nil, encodeRPCError(contextErr)
	}
	ctx = s.injectSession(ctx, req.GetConnectionId())
	conn, err := s.provider.StopConnection(ctx, req.GetConnectionId())
	if err != nil {
		return nil, encodeRPCError(err)
	}
	pbConn, err := connectionToProto(conn)
	if err != nil {
		return nil, encodeRPCError(err)
	}
	return &resourcepb.ConnectionResponse{Connection: pbConn}, nil

}

func (s *server) GetConnectionNamespaces(ctx context.Context, req *resourcepb.ConnectionRequest) (*resourcepb.NamespacesResponse, error) {
	ctx, contextErr := s.callContext(ctx)
	if contextErr != nil {
		return nil, encodeRPCError(contextErr)
	}
	ns, err := s.provider.GetConnectionNamespaces(ctx, req.GetConnectionId())
	if err != nil {
		return nil, encodeRPCError(err)
	}
	return &resourcepb.NamespacesResponse{Namespaces: ns}, nil

}

// ============================================================================
// CRUD Operations
// ============================================================================

func (s *server) Get(ctx context.Context, req *resourcepb.GetRequest) (*resourcepb.GetResponse, error) {
	ctx, contextErr := s.callContext(ctx)
	if contextErr != nil {
		return nil, encodeRPCError(contextErr)
	}
	ctx = s.injectSession(ctx, req.GetConnectionId())
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
	ctx, contextErr := s.callContext(ctx)
	if contextErr != nil {
		return nil, encodeRPCError(contextErr)
	}
	ctx = s.injectSession(ctx, req.GetConnectionId())
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
		resp.Total = clampInt32(result.TotalCount)
		resp.NextCursor = result.NextCursor
	}
	return resp, nil

}

func (s *server) Find(ctx context.Context, req *resourcepb.FindRequest) (*resourcepb.FindResponse, error) {
	ctx, contextErr := s.callContext(ctx)
	if contextErr != nil {
		return nil, encodeRPCError(contextErr)
	}
	ctx = s.injectSession(ctx, req.GetConnectionId())
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
		resp.Total = clampInt32(result.TotalCount)
		resp.NextCursor = result.NextCursor
	}
	return resp, nil

}

func (s *server) Create(ctx context.Context, req *resourcepb.CreateRequest) (*resourcepb.CreateResponse, error) {
	ctx, contextErr := s.callContext(ctx)
	if contextErr != nil {
		return nil, encodeRPCError(contextErr)
	}
	ctx = s.injectSession(ctx, req.GetConnectionId())
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
	ctx, contextErr := s.callContext(ctx)
	if contextErr != nil {
		return nil, encodeRPCError(contextErr)
	}
	ctx = s.injectSession(ctx, req.GetConnectionId())
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
	ctx, contextErr := s.callContext(ctx)
	if contextErr != nil {
		return nil, encodeRPCError(contextErr)
	}
	ctx = s.injectSession(ctx, req.GetConnectionId())
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
	ctx, contextErr := s.callContext(ctx)
	if contextErr != nil {
		return nil, encodeRPCError(contextErr)
	}
	ctx = s.injectSession(ctx, req.GetConnectionId())
	groups, err := s.provider.GetResourceGroups(ctx, req.GetConnectionId())
	if err != nil {
		return nil, encodeRPCError(err)
	}
	pbGroups := make(map[string]*commonpb.ResourceGroup, len(groups))
	for k, g := range groups {
		pbGroups[k] = resourceGroupToProto(g)
	}
	return &resourcepb.ResourceGroupsResponse{Groups: pbGroups}, nil

}

func (s *server) GetResourceTypes(ctx context.Context, req *resourcepb.ResourceTypesRequest) (*resourcepb.ResourceTypesResponse, error) {
	ctx, contextErr := s.callContext(ctx)
	if contextErr != nil {
		return nil, encodeRPCError(contextErr)
	}
	ctx = s.injectSession(ctx, req.GetConnectionId())
	types, err := s.provider.GetResourceTypes(ctx, req.GetConnectionId())
	if err != nil {
		return nil, encodeRPCError(err)
	}
	pbTypes := make(map[string]*commonpb.ResourceMeta, len(types))
	for k, m := range types {
		pbTypes[k] = resourceMetaToProto(m)
	}
	return &resourcepb.ResourceTypesResponse{Types: pbTypes}, nil

}

func (s *server) GetResourceCapabilities(ctx context.Context, req *resourcepb.ResourceCapabilitiesRequest) (*resourcepb.ResourceCapabilitiesResponse, error) {
	ctx, contextErr := s.callContext(ctx)
	if contextErr != nil {
		return nil, encodeRPCError(contextErr)
	}
	caps, err := s.provider.GetResourceCapabilities(ctx, req.GetResourceKey())
	if err != nil {
		return nil, encodeRPCError(err)
	}
	return &resourcepb.ResourceCapabilitiesResponse{Capabilities: capabilitiesToProto(caps)}, nil

}

func (s *server) GetFilterFields(ctx context.Context, req *resourcepb.FilterFieldsRequest) (*resourcepb.FilterFieldsResponse, error) {
	ctx, contextErr := s.callContext(ctx)
	if contextErr != nil {
		return nil, encodeRPCError(contextErr)
	}
	ctx = s.injectSession(ctx, req.GetConnectionId())
	fields, err := s.provider.GetFilterFields(ctx, req.GetConnectionId(), req.GetResourceKey())
	if err != nil {
		return nil, encodeRPCError(err)
	}
	pbFields := make([]*resourcepb.FilterField, len(fields))
	for i, f := range fields {
		pbFields[i] = filterFieldToProto(f)
	}
	return &resourcepb.FilterFieldsResponse{Fields: pbFields}, nil

}

func (s *server) GetResourceSchema(ctx context.Context, req *resourcepb.ResourceSchemaRequest) (*resourcepb.ResourceSchemaResponse, error) {
	ctx, contextErr := s.callContext(ctx)
	if contextErr != nil {
		return nil, encodeRPCError(contextErr)
	}
	ctx = s.injectSession(ctx, req.GetConnectionId())
	schema, err := s.provider.GetResourceSchema(ctx, req.GetConnectionId(), req.GetResourceKey())
	if err != nil {
		return nil, encodeRPCError(err)
	}
	return &resourcepb.ResourceSchemaResponse{Schema: schema}, nil

}

func (s *server) GetEditorSchemas(ctx context.Context, req *resourcepb.EditorSchemasRequest) (*resourcepb.EditorSchemasResponse, error) {
	ctx, contextErr := s.callContext(ctx)
	if contextErr != nil {
		return nil, encodeRPCError(contextErr)
	}
	ctx = s.injectSession(ctx, req.GetConnectionId())
	schemas, err := s.provider.GetEditorSchemas(ctx, req.GetConnectionId())
	if err != nil {
		return nil, encodeRPCError(err)
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
	ctx, contextErr := s.callContext(ctx)
	if contextErr != nil {
		return nil, encodeRPCError(contextErr)
	}
	ctx = s.injectSession(ctx, req.GetConnectionId())
	actions, err := s.provider.GetActions(ctx, req.GetResourceKey())
	if err != nil {
		return nil, encodeRPCError(err)
	}
	pbActions := make([]*resourcepb.ActionDescriptor, len(actions))
	for i, a := range actions {
		pb, err := actionDescriptorToProto(a)
		if err != nil {
			return nil, encodeRPCError(err)
		}
		pbActions[i] = pb
	}
	return &resourcepb.GetActionsResponse{Actions: pbActions}, nil

}

func (s *server) ExecuteAction(ctx context.Context, req *resourcepb.ExecuteActionRequest) (*resourcepb.ExecuteActionResponse, error) {
	ctx, contextErr := s.callContext(ctx)
	if contextErr != nil {
		return nil, encodeRPCError(contextErr)
	}
	ctx = s.injectSession(ctx, req.GetConnectionId())
	input := actionInputFromProto(req.GetInput())
	result, err := s.provider.ExecuteAction(ctx, req.GetResourceKey(), req.GetActionId(), input)
	if err != nil {
		return &resourcepb.ExecuteActionResponse{
			Result: &resourcepb.ActionResult{
				Error: errorToProtoError(err),
			},
		}, nil
	}
	pbResult, err := actionResultToProto(result)
	if err != nil {
		return nil, encodeRPCError(err)
	}
	return &resourcepb.ExecuteActionResponse{Result: pbResult}, nil

}

// safeClose closes a channel, recovering from the panic if it was already closed.
func safeClose[T any](ch chan T) {
	defer func() { recover() }()
	close(ch)
}

func (s *server) StreamAction(req *resourcepb.ExecuteActionRequest, stream resourcepb.ResourcePlugin_StreamActionServer) error {
	base, err := s.callContext(stream.Context())
	if err != nil {
		return encodeRPCError(err)
	}
	ctx, cancel := context.WithCancel(s.injectSession(base, req.GetConnectionId()))
	defer cancel()
	ch := make(chan resource.ActionEvent, 16)
	result := make(chan error, 1)
	go func() {
		result <- s.provider.StreamAction(ctx, req.GetResourceKey(), req.GetActionId(), actionInputFromProto(req.GetInput()), ch)
	}()
	var completed bool
	var readySeen bool
	var resultErr error
	for {
		if completed && len(ch) == 0 {
			if resultErr == nil && !readySeen {
				return encodeRPCError(&resource.ResourceOperationError{Code: "PROVIDER_ERROR", Title: "Provider operation failed", Message: "The provider completed the action before it became ready."})
			}
			return encodeRPCError(resultErr)
		}
		select {
		case <-ctx.Done():
			return encodeRPCError(ctx.Err())
		case resultErr = <-result:
			completed = true
			result = nil
		case event, ok := <-ch:
			if !ok {
				ch = nil
				continue
			}
			if event.Type == "error" {
				if event.Error == nil {
					event.Error = structuredError(errors.New("unclassified action event"))
				}
				event.Data = nil
			}
			pbEvent, err := actionEventToProto(event)
			if err != nil {
				return encodeRPCError(err)
			}
			if err := stream.Send(pbEvent); err != nil {
				return encodeRPCError(err)
			}
			if event.Type == "ready" && event.Error == nil {
				readySeen = true
			}
			if event.Type == "error" {
				return encodeRPCError(event.Error)
			}
		}
	}
}

// ============================================================================
// Watch
// ============================================================================

// grpcWatchSink adapts WatchEventSink to send via the ListenForEvents stream.
// A mutex serializes Send() calls because gRPC stream.Send() is not safe for
// concurrent use, and multiple watch goroutines call OnAdd/OnUpdate/OnDelete
// concurrently via fanOutSink's shared RLock.
type grpcWatchSink struct {
	mu     sync.Mutex
	stream resourcepb.ResourcePlugin_ListenForEventsServer
	log    logging.Logger
	err    error // first Send error; once set, all further sends are skipped
}

func (s *grpcWatchSink) OnAdd(payload resource.WatchAddPayload) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return
	}
	if err := s.stream.Send(&resourcepb.WatchEvent{
		ConnectionId: payload.Connection,
		ResourceKey:  payload.Key,
		Event: &resourcepb.WatchEvent_Add{
			Add: &resourcepb.WatchAddPayload{
				Id:        payload.ID,
				Namespace: payload.Namespace,
				Data:      payload.Data,
			},
		},
	}); err != nil {
		s.err = err
		s.log.Error(s.stream.Context(), "watch grpc send failed", logging.Error(err))
	}
}

func (s *grpcWatchSink) OnUpdate(payload resource.WatchUpdatePayload) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return
	}
	if err := s.stream.Send(&resourcepb.WatchEvent{
		ConnectionId: payload.Connection,
		ResourceKey:  payload.Key,
		Event: &resourcepb.WatchEvent_Update{
			Update: &resourcepb.WatchUpdatePayload{
				Id:        payload.ID,
				Namespace: payload.Namespace,
				Data:      payload.Data,
			},
		},
	}); err != nil {
		s.err = err
		s.log.Error(s.stream.Context(), "watch grpc send failed", logging.Error(err))
	}
}

func (s *grpcWatchSink) OnDelete(payload resource.WatchDeletePayload) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return
	}
	if err := s.stream.Send(&resourcepb.WatchEvent{
		ConnectionId: payload.Connection,
		ResourceKey:  payload.Key,
		Event: &resourcepb.WatchEvent_Delete{
			Delete: &resourcepb.WatchDeletePayload{
				Id:        payload.ID,
				Namespace: payload.Namespace,
				Data:      payload.Data,
			},
		},
	}); err != nil {
		s.err = err
		s.log.Error(s.stream.Context(), "watch grpc send failed", logging.Error(err))
	}
}

func (s *grpcWatchSink) OnStateChange(event resource.WatchStateEvent) {
	protoState, ok := watchStateToProto[event.State]
	if !ok {
		protoState = resourcepb.WatchState_WATCH_STATE_UNSPECIFIED
		s.log.Warnw(context.Background(), "unknown watch state, falling back to unspecified", "state", event.State)
	}
	s.log.Debugw(context.Background(), "sending watch state",
		"connection_id", event.Connection,
		"resource_key", event.ResourceKey,
		"state", event.State,
		"proto_state", protoState,
		"count", event.ResourceCount,
		"error_code", event.ErrorCode,
	)
	var errMsg string
	if event.Error != nil {
		errMsg = event.Error.Error()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return
	}
	if err := s.stream.Send(&resourcepb.WatchEvent{
		ConnectionId: event.Connection,
		ResourceKey:  event.ResourceKey,
		Event: &resourcepb.WatchEvent_State{
			State: &resourcepb.WatchStateEvent{
				State:         protoState,
				ResourceCount: clampInt32(event.ResourceCount),
				ErrorMessage:  errMsg,
				ErrorCode:     event.ErrorCode,
			},
		},
	}); err != nil {
		s.err = err
		s.log.Error(s.stream.Context(), "watch grpc send failed", logging.Error(err))
	}
}

const watchReadyHeader = "omniview-watch-ready"

func (s *server) ListenForEvents(_ *resourcepb.ListenRequest, stream resourcepb.ResourcePlugin_ListenForEventsServer) error {
	ctx, err := s.callContext(stream.Context())
	if err != nil {
		return encodeRPCError(err)
	}
	sink := &grpcWatchSink{stream: stream, log: s.log.Named("watch_sink")}
	// An already-running source may emit as soon as registration becomes
	// visible. Fence Send so it cannot implicitly publish empty headers before
	// the explicit registration acknowledgement. No manager lock is held.
	sink.mu.Lock()
	ready := false
	err = s.provider.ListenForEvents(ctx, sink, func() error {
		ready = true
		headerErr := stream.SendHeader(metadata.Pairs(watchReadyHeader, "1"))
		sink.err = headerErr
		sink.mu.Unlock()
		return headerErr
	})
	if !ready {
		if err == nil {
			err = errors.New("watch event listener returned before registration")
		}
		sink.err = err
		sink.mu.Unlock()
	}
	return encodeRPCError(err)
}

func (s *server) EnsureResourceWatch(ctx context.Context, req *resourcepb.WatchResourceRequest) (*resourcepb.WatchResourceResponse, error) {
	ctx, contextErr := s.callContext(ctx)
	if contextErr != nil {
		return nil, encodeRPCError(contextErr)
	}
	err := s.provider.EnsureResourceWatch(ctx, req.GetConnectionId(), req.GetResourceKey())
	if err != nil {
		return nil, encodeRPCError(err)
	}
	return &resourcepb.WatchResourceResponse{Success: true}, nil

}

func (s *server) StopResourceWatch(ctx context.Context, req *resourcepb.WatchResourceRequest) (*resourcepb.WatchResourceResponse, error) {
	ctx, contextErr := s.callContext(ctx)
	if contextErr != nil {
		return nil, encodeRPCError(contextErr)
	}
	err := s.provider.StopResourceWatch(ctx, req.GetConnectionId(), req.GetResourceKey())
	if err != nil {
		return nil, encodeRPCError(err)
	}
	return &resourcepb.WatchResourceResponse{Success: true}, nil

}

func (s *server) WatchConnections(_ *resourcepb.WatchConnectionsRequest, stream resourcepb.ResourcePlugin_WatchConnectionsServer) error {
	base, err := s.callContext(stream.Context())
	if err != nil {
		return encodeRPCError(err)
	}
	ctx, cancel := context.WithCancel(s.injectSettingsSession(base))
	defer cancel()

	ch := make(chan []types.Connection, 16)
	errCh := make(chan error, 1)
	go func() {
		err := s.provider.WatchConnections(ctx, ch)
		safeClose(ch)
		errCh <- err
	}()

	for conns := range ch {
		pbConns := make([]*commonpb.Connection, len(conns))
		for i, c := range conns {
			pb, err := connectionToProto(c)
			if err != nil {
				cancel()
				return encodeRPCError(err)
			}
			pbConns[i] = pb
		}
		if err := stream.Send(&resourcepb.WatchConnectionsResponse{Connections: pbConns}); err != nil {
			cancel()
			return encodeRPCError(err)
		}
	}
	return encodeRPCError(<-errCh)
}

func (s *server) GetWatchState(ctx context.Context, req *resourcepb.GetWatchStateRequest) (*resourcepb.GetWatchStateResponse, error) {
	ctx, contextErr := s.callContext(ctx)
	if contextErr != nil {
		return nil, encodeRPCError(contextErr)
	}
	summary, err := s.provider.GetWatchState(ctx, req.GetConnectionId())
	if err != nil {
		return nil, encodeRPCError(err)
	}
	return watchConnectionSummaryToProto(summary), nil

}

// ============================================================================
// Relationships
// ============================================================================

func (s *server) GetRelationships(ctx context.Context, req *resourcepb.RelationshipsRequest) (*resourcepb.RelationshipsResponse, error) {
	ctx, contextErr := s.callContext(ctx)
	if contextErr != nil {
		return nil, encodeRPCError(contextErr)
	}
	rels, err := s.provider.GetRelationships(ctx, req.GetResourceKey())
	if err != nil {
		return nil, encodeRPCError(err)
	}
	pbRels := make([]*resourcepb.RelationshipDescriptor, len(rels))
	for i, r := range rels {
		pb, err := relationshipDescriptorToProto(r)
		if err != nil {
			return nil, encodeRPCError(err)
		}
		pbRels[i] = pb
	}
	return &resourcepb.RelationshipsResponse{Relationships: pbRels}, nil

}

func (s *server) ResolveRelationships(ctx context.Context, req *resourcepb.ResolveRelationshipsRequest) (*resourcepb.ResolveRelationshipsResponse, error) {
	ctx, contextErr := s.callContext(ctx)
	if contextErr != nil {
		return nil, encodeRPCError(contextErr)
	}
	ctx = s.injectSession(ctx, req.GetConnectionId())
	resolved, err := s.provider.ResolveRelationships(ctx, req.GetConnectionId(), req.GetResourceKey(), req.GetId(), req.GetNamespace())
	if err != nil {
		return nil, encodeRPCError(err)
	}
	pbResolved := make([]*resourcepb.ResolvedRelationship, len(resolved))
	for i, r := range resolved {
		pb, err := resolvedRelationshipToProto(r)
		if err != nil {
			return nil, encodeRPCError(err)
		}
		pbResolved[i] = pb
	}
	return &resourcepb.ResolveRelationshipsResponse{Relationships: pbResolved}, nil

}

// ============================================================================
// Health
// ============================================================================

func (s *server) GetHealth(ctx context.Context, req *resourcepb.HealthRequest) (*resourcepb.HealthResponse, error) {
	ctx, contextErr := s.callContext(ctx)
	if contextErr != nil {
		return nil, encodeRPCError(contextErr)
	}
	ctx = s.injectSession(ctx, req.GetConnectionId())
	health, err := s.provider.GetHealth(ctx, req.GetConnectionId(), req.GetResourceKey(), json.RawMessage(req.GetData()))
	if err != nil {
		return nil, encodeRPCError(err)
	}
	return &resourcepb.HealthResponse{Health: resourceHealthToProto(health)}, nil

}

func (s *server) GetResourceEvents(ctx context.Context, req *resourcepb.ResourceEventsRequest) (*resourcepb.ResourceEventsResponse, error) {
	ctx, contextErr := s.callContext(ctx)
	if contextErr != nil {
		return nil, encodeRPCError(contextErr)
	}
	ctx = s.injectSession(ctx, req.GetConnectionId())
	events, err := s.provider.GetResourceEvents(ctx, req.GetConnectionId(), req.GetResourceKey(), req.GetId(), req.GetNamespace(), req.GetLimit())
	if err != nil {
		return nil, encodeRPCError(err)
	}
	pbEvents := make([]*resourcepb.ResourceEvent, len(events))
	for i, e := range events {
		pbEvents[i] = resourceEventToProto(e)
	}
	return &resourcepb.ResourceEventsResponse{Events: pbEvents}, nil

}
