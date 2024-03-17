package plugin

import (
	"context"
	"errors"

	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"

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
		result = append(result, pkgtypes.Connection{
			ID:          conn.GetId(),
			UID:         conn.GetUid(),
			Name:        conn.GetName(),
			Description: conn.GetDescription(),
			Avatar:      conn.GetAvatar(),
			ExpiryTime:  conn.GetExpiryTime().AsDuration(),
			LastRefresh: conn.GetLastRefresh().AsTime(),
			Data:        conn.GetData().AsMap(),
		})
	}

	return result, nil
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

func (r *ResourcePluginClient) StartContextInformer(
	ctx *pkgtypes.PluginContext,
	contextID string,
) error {
	if ctx.Connection == nil {
		return ErrNoConnection
	}

	_, err := r.client.StartContextInformer(
		context.Background(),
		&proto.StartContextInformerRequest{
			Key:     "",
			Context: contextID,
		},
	)
	return err
}

func (r *ResourcePluginClient) StopContextInformer(
	ctx *pkgtypes.PluginContext,
	contextID string,
) error {
	if ctx.Connection == nil {
		return ErrNoConnection
	}

	_, err := r.client.StopContextInformer(
		ctx.Context,
		&proto.StopContextInformerRequest{
			Key:     "",
			Context: contextID,
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
	if ctx.Connection == nil {
		return ErrNoConnection
	}

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
					Key:       msg.GetKey(),
					Context:   msg.GetContext(),
					ID:        add.GetId(),
					Namespace: add.GetNamespace(),
					Data:      add.GetData().AsMap(),
				}
			case *proto.InformerEvent_Update:
				update := msg.GetUpdate()
				updateStream <- types.InformerUpdatePayload{
					Key:       msg.GetKey(),
					Context:   msg.GetContext(),
					ID:        update.GetId(),
					Namespace: update.GetNamespace(),
					OldData:   update.GetOldData().AsMap(),
					NewData:   update.GetNewData().AsMap(),
				}
			case *proto.InformerEvent_Delete:
				del := msg.GetDelete()
				deleteStream <- types.InformerDeletePayload{
					Key:       msg.GetKey(),
					Context:   msg.GetContext(),
					ID:        del.GetId(),
					Namespace: del.GetNamespace(),
				}
			}
		}
	}
}
