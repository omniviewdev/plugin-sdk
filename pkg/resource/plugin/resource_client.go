package plugin

import (
	"context"
	"errors"
	"log"

	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/omniviewdev/plugin-sdk/pkg/resource/types"
	pkgtypes "github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/proto"
)

var ErrNoConnection = errors.New("no connection provided")

// ResourcePluginClient is the real client implementation for ResourcePlugin.
type ResourcePluginClient struct {
	client proto.ResourcePluginClient
}

var _ types.ResourceProvider = (*ResourcePluginClient)(nil)

func protoToConnection(conn *proto.Connection) pkgtypes.Connection {
	return pkgtypes.Connection{
		ID:          conn.GetId(),
		UID:         conn.GetUid(),
		Name:        conn.GetName(),
		Description: conn.GetDescription(),
		Avatar:      conn.GetAvatar(),
		ExpiryTime:  conn.GetExpiryTime().AsDuration(),
		LastRefresh: conn.GetLastRefresh().AsTime(),
		Data:        conn.GetData().AsMap(),
		Labels:      conn.GetLabels().AsMap(),
	}
}

func protoToResourceMeta(meta *proto.ResourceMeta) types.ResourceMeta {
	return types.ResourceMeta{
		Group:       meta.GetGroup(),
		Version:     meta.GetVersion(),
		Kind:        meta.GetKind(),
		Description: meta.GetDescription(),
		Category:    meta.GetCategory(),
	}
}

func protoToLayoutItem(item *proto.LayoutItem) types.LayoutItem {
	items := make([]types.LayoutItem, 0, len(item.GetItems()))
	for _, i := range item.GetItems() {
		items = append(items, protoToLayoutItem(i))
	}

	return types.LayoutItem{
		ID:          item.GetId(),
		Label:       item.GetLabel(),
		Description: item.GetDescription(),
		Icon:        item.GetIcon(),
		Items:       items,
	}
}

func layoutItemToProto(item types.LayoutItem) *proto.LayoutItem {
	items := make([]*proto.LayoutItem, 0, len(item.Items))
	for _, i := range item.Items {
		items = append(items, layoutItemToProto(i))
	}
	return &proto.LayoutItem{
		Id:          item.ID,
		Label:       item.Label,
		Description: item.Description,
		Icon:        item.Icon,
		Items:       items,
	}
}

func (r *ResourcePluginClient) GetResourceTypes() map[string]types.ResourceMeta {
	resp, err := r.client.GetResourceTypes(context.Background(), &emptypb.Empty{})
	if err != nil {
		log.Print("err", err)
		return nil
	}
	result := make(map[string]types.ResourceMeta)
	for id, t := range resp.GetTypes() {
		result[id] = protoToResourceMeta(t)
	}
	return result
}

func (r *ResourcePluginClient) GetResourceType(id string) (*types.ResourceMeta, error) {
	resp, err := r.client.GetResourceType(context.Background(), &proto.ResourceTypeRequest{Id: id})
	if err != nil {
		return nil, err
	}
	result := protoToResourceMeta(resp)
	return &result, nil
}

func (r *ResourcePluginClient) HasResourceType(id string) bool {
	resp, err := r.client.HasResourceType(context.Background(), &proto.ResourceTypeRequest{Id: id})
	if err != nil {
		return false
	}
	return resp.GetValue()
}

func (r *ResourcePluginClient) LoadConnections(
	ctx *pkgtypes.PluginContext,
) ([]pkgtypes.Connection, error) {
	connections, err := r.client.LoadConnections(ctx.Context, &emptypb.Empty{})
	if err != nil {
		return nil, err
	}
	returned := connections.GetConnections()

	result := make([]pkgtypes.Connection, 0, len(returned))
	for _, conn := range returned {
		result = append(result, protoToConnection(conn))
	}

	return result, nil
}

func (r *ResourcePluginClient) ListConnections(
	ctx *pkgtypes.PluginContext,
) ([]pkgtypes.Connection, error) {
	connections, err := r.client.ListConnections(ctx.Context, &emptypb.Empty{})
	if err != nil {
		return nil, err
	}
	returned := connections.GetConnections()
	result := make([]pkgtypes.Connection, 0, len(returned))
	for _, conn := range returned {
		result = append(result, protoToConnection(conn))
	}
	return result, nil
}

func (r *ResourcePluginClient) GetConnection(
	ctx *pkgtypes.PluginContext,
	id string,
) (pkgtypes.Connection, error) {
	conn, err := r.client.GetConnection(ctx.Context, &proto.GetConnectionRequest{
		Id: id,
	})
	if err != nil {
		return pkgtypes.Connection{}, err
	}
	return protoToConnection(conn), nil
}

func (r *ResourcePluginClient) UpdateConnection(
	ctx *pkgtypes.PluginContext,
	conn pkgtypes.Connection,
) (pkgtypes.Connection, error) {
	labels, err := structpb.NewStruct(conn.Labels)
	if err != nil {
		return pkgtypes.Connection{}, err
	}

	resp, err := r.client.UpdateConnection(ctx.Context, &proto.UpdateConnectionRequest{
		Id:          conn.ID,
		Name:        wrapperspb.String(conn.Name),
		Description: wrapperspb.String(conn.Description),
		Avatar:      wrapperspb.String(conn.Avatar),
		Labels:      labels,
	})
	if err != nil {
		return pkgtypes.Connection{}, err
	}

	return protoToConnection(resp.GetConnection()), nil
}

func (r *ResourcePluginClient) DeleteConnection(
	ctx *pkgtypes.PluginContext,
	id string,
) error {
	_, err := r.client.DeleteConnection(ctx.Context, &proto.DeleteConnectionRequest{
		Id: id,
	})
	return err
}

func (r *ResourcePluginClient) Get(
	ctx *pkgtypes.PluginContext,
	key string,
	input types.GetInput,
) (*types.GetResult, error) {
	if ctx.Connection == nil {
		return nil, ErrNoConnection
	}

	resp, err := r.client.Get(ctx.Context, &proto.GetRequest{
		Key:       key,
		Context:   ctx.Connection.ID,
		Id:        input.ID,
		Namespace: input.Namespace,
	})
	if err != nil {
		return nil, err
	}

	return &types.GetResult{
		Result:  resp.GetData().AsMap(),
		Success: resp.GetSuccess(),
	}, nil
}

func (r *ResourcePluginClient) List(
	ctx *pkgtypes.PluginContext,
	key string,
	input types.ListInput,
) (*types.ListResult, error) {
	if ctx.Connection == nil {
		return nil, ErrNoConnection
	}

	resp, err := r.client.List(ctx.Context, &proto.ListRequest{
		Key:        key,
		Context:    ctx.Connection.ID,
		Namespaces: input.Namespaces,
	})
	if err != nil {
		return nil, err
	}

	result := &types.ListResult{
		Result:  resp.GetData().AsMap(),
		Success: resp.GetSuccess(),
	}

	return result, nil
}

func (r *ResourcePluginClient) Find(
	ctx *pkgtypes.PluginContext,
	key string,
	input types.FindInput,
) (*types.FindResult, error) {
	if ctx.Connection == nil {
		return nil, ErrNoConnection
	}

	resp, err := r.client.Find(ctx.Context, &proto.FindRequest{
		Key:        key,
		Context:    ctx.Connection.ID,
		Namespaces: input.Namespaces,
	})
	if err != nil {
		return nil, err
	}

	result := &types.FindResult{
		Result:  resp.GetData().AsMap(),
		Success: resp.GetSuccess(),
	}

	return result, nil
}

func (r *ResourcePluginClient) Create(
	ctx *pkgtypes.PluginContext,
	key string,
	input types.CreateInput,
) (*types.CreateResult, error) {
	if ctx.Connection == nil {
		return nil, ErrNoConnection
	}

	data, err := structpb.NewStruct(input.Input)
	if err != nil {
		return nil, err
	}

	resp, err := r.client.Create(ctx.Context, &proto.CreateRequest{
		Key:       key,
		Context:   ctx.Connection.ID,
		Namespace: input.Namespace,
		Data:      data,
	})
	if err != nil {
		return nil, err
	}

	return &types.CreateResult{
		Result:  resp.GetData().AsMap(),
		Success: resp.GetSuccess(),
	}, nil
}

func (r *ResourcePluginClient) Update(
	ctx *pkgtypes.PluginContext,
	key string,
	input types.UpdateInput,
) (*types.UpdateResult, error) {
	if ctx.Connection == nil {
		return nil, ErrNoConnection
	}

	data, err := structpb.NewStruct(input.Input)
	if err != nil {
		return nil, err
	}

	resp, err := r.client.Update(ctx.Context, &proto.UpdateRequest{
		Key:       key,
		Context:   ctx.Connection.ID,
		Id:        input.ID,
		Namespace: input.Namespace,
		Data:      data,
	})
	if err != nil {
		return nil, err
	}

	return &types.UpdateResult{
		Result:  resp.GetData().AsMap(),
		Success: resp.GetSuccess(),
	}, nil
}

func (r *ResourcePluginClient) Delete(
	ctx *pkgtypes.PluginContext,
	key string,
	input types.DeleteInput,
) (*types.DeleteResult, error) {
	if ctx.Connection == nil {
		return nil, ErrNoConnection
	}

	resp, err := r.client.Delete(ctx.Context, &proto.DeleteRequest{
		Key:       key,
		Context:   ctx.Connection.ID,
		Id:        input.ID,
		Namespace: input.Namespace,
	})
	if err != nil {
		return nil, err
	}

	return &types.DeleteResult{
		Result:  resp.GetData().AsMap(),
		Success: resp.GetSuccess(),
	}, nil
}

func (r *ResourcePluginClient) StartConnectionInformer(
	ctx *pkgtypes.PluginContext,
	connectionID string,
) error {
	_, err := r.client.StartConnectionInformer(
		ctx.Context,
		&proto.StartConnectionInformerRequest{
			Connection: connectionID,
		},
	)
	return err
}

func (r *ResourcePluginClient) StopConnectionInformer(
	ctx *pkgtypes.PluginContext,
	connectionID string,
) error {
	_, err := r.client.StopConnectionInformer(
		ctx.Context,
		&proto.StopConnectionInformerRequest{
			Connection: connectionID,
		},
	)
	return err
}

// ListenForEvents listens for events from the resource provider
// and pipes them back to the event subsystem, stopping when stopCh is closed.
// This method is blocking, and should be run as part of the resourcer
// controller's event loop.
func (r *ResourcePluginClient) ListenForEvents(
	ctx *pkgtypes.PluginContext,
	addStream chan types.InformerAddPayload,
	updateStream chan types.InformerUpdatePayload,
	deleteStream chan types.InformerDeletePayload,
) error {
	stream, err := r.client.ListenForEvents(ctx.Context, &emptypb.Empty{})
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Context.Done():
			return nil
		default:
			msg, msgErr := stream.Recv()
			if msgErr != nil {
				return msgErr
			}

			switch msg.GetAction().(type) {
			case *proto.InformerEvent_Add:
				add := msg.GetAdd()
				addStream <- types.InformerAddPayload{
					Key:        msg.GetKey(),
					Connection: msg.GetConnection(),
					ID:         msg.GetId(),
					Namespace:  msg.GetNamespace(),
					Data:       add.GetData().AsMap(),
				}
			case *proto.InformerEvent_Update:
				update := msg.GetUpdate()
				updateStream <- types.InformerUpdatePayload{
					Key:        msg.GetKey(),
					Connection: msg.GetConnection(),
					ID:         msg.GetId(),
					Namespace:  msg.GetNamespace(),
					OldData:    update.GetOldData().AsMap(),
					NewData:    update.GetNewData().AsMap(),
				}
			case *proto.InformerEvent_Delete:
				del := msg.GetDelete()
				deleteStream <- types.InformerDeletePayload{
					Key:        msg.GetKey(),
					Connection: msg.GetConnection(),
					ID:         msg.GetId(),
					Namespace:  msg.GetNamespace(),
					Data:       del.GetData().AsMap(),
				}
			}
		}
	}
}

func (r *ResourcePluginClient) GetLayout(layoutID string) ([]types.LayoutItem, error) {
	resp, err := r.client.GetLayout(context.Background(), &proto.GetLayoutRequest{
		Id: layoutID,
	})
	if err != nil {
		return nil, err
	}
	result := make([]types.LayoutItem, 0, len(resp.GetItems()))
	for _, item := range resp.GetItems() {
		result = append(result, protoToLayoutItem(item))
	}
	return result, nil
}

func (r *ResourcePluginClient) GetDefaultLayout() ([]types.LayoutItem, error) {
	resp, err := r.client.GetDefaultLayout(context.Background(), &emptypb.Empty{})
	if err != nil {
		return nil, err
	}
	result := make([]types.LayoutItem, 0, len(resp.GetItems()))
	for _, item := range resp.GetItems() {
		result = append(result, protoToLayoutItem(item))
	}
	return result, nil
}

func (r *ResourcePluginClient) SetLayout(id string, layout []types.LayoutItem) error {
	panic("not implemented")
}
