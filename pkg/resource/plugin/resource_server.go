package plugin

import (
	"context"
	"log"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/omniviewdev/plugin-sdk/pkg/resource/types"
	pkgtypes "github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/proto"
)

// Here is the gRPC server that GRPCClient talks to.
type ResourcePluginServer struct {
	// This is the real implementation
	Impl types.ResourceProvider
}

func metaToProtoResourceMeta(meta types.ResourceMeta) *proto.ResourceMeta {
	return &proto.ResourceMeta{
		Group:       meta.Group,
		Version:     meta.Version,
		Kind:        meta.Kind,
		Description: meta.Description,
		Category:    meta.Category,
	}
}

func (s *ResourcePluginServer) GetResourceTypes(
	_ context.Context,
	_ *emptypb.Empty,
) (*proto.ResourceTypes, error) {
	resourceTypes := s.Impl.GetResourceTypes()

	mapped := make(map[string]*proto.ResourceMeta, len(resourceTypes))
	for id, t := range resourceTypes {
		mapped[id] = metaToProtoResourceMeta(t)
	}
	return &proto.ResourceTypes{
		Types: mapped,
	}, nil
}

func (s *ResourcePluginServer) GetResourceType(
	_ context.Context,
	in *proto.ResourceTypeRequest,
) (*proto.ResourceMeta, error) {
	resp, err := s.Impl.GetResourceType(in.GetId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get resource type: %s", err.Error())
	}
	return metaToProtoResourceMeta(*resp), nil
}

func (s *ResourcePluginServer) HasResourceType(
	_ context.Context,
	in *proto.ResourceTypeRequest,
) (*wrapperspb.BoolValue, error) {
	resp := s.Impl.HasResourceType(in.GetId())
	return &wrapperspb.BoolValue{Value: resp}, nil
}

func (s *ResourcePluginServer) LoadConnections(
	ctx context.Context,
	_ *emptypb.Empty,
) (*proto.LoadConnectionsResponse, error) {
	pluginCtx := pkgtypes.NewPluginContextFromCtx(ctx)

	connections, err := s.Impl.LoadConnections(pluginCtx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to load connections: %s", err.Error())
	}

	mappedConnections := make([]*proto.Connection, 0, len(connections))
	for _, conn := range connections {
		data, err := structpb.NewStruct(conn.Data)
		if err != nil {
			return nil, status.Errorf(
				codes.Internal,
				"failed to convert connection data to struct: %s",
				err.Error(),
			)
		}

		labels, err := structpb.NewStruct(conn.Labels)
		if err != nil {
			return nil, status.Errorf(
				codes.Internal,
				"failed to convert connection labels to struct: %s",
				err.Error(),
			)
		}

		mappedConnections = append(mappedConnections, &proto.Connection{
			Id:          conn.ID,
			Uid:         conn.UID,
			Name:        conn.Name,
			Description: conn.Description,
			Avatar:      conn.Avatar,
			ExpiryTime:  durationpb.New(conn.ExpiryTime),
			LastRefresh: timestamppb.New(conn.LastRefresh),
			Labels:      labels,
			Data:        data,
		})
	}

	return &proto.LoadConnectionsResponse{
		Connections: mappedConnections,
	}, nil
}

func (s *ResourcePluginServer) ListConnections(
	ctx context.Context,
	_ *emptypb.Empty,
) (*proto.ListConnectionsResponse, error) {
	pluginCtx := pkgtypes.NewPluginContextFromCtx(ctx)

	connections, err := s.Impl.ListConnections(pluginCtx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to load connections: %s", err.Error())
	}

	mappedConnections := make([]*proto.Connection, 0, len(connections))
	for _, conn := range connections {
		data, err := structpb.NewStruct(conn.Data)
		if err != nil {
			return nil, status.Errorf(
				codes.Internal,
				"failed to convert connection data to struct: %s",
				err.Error(),
			)
		}

		labels, err := structpb.NewStruct(conn.Labels)
		if err != nil {
			return nil, status.Errorf(
				codes.Internal,
				"failed to convert connection labels to struct: %s",
				err.Error(),
			)
		}

		mappedConnections = append(mappedConnections, &proto.Connection{
			Id:          conn.ID,
			Uid:         conn.UID,
			Name:        conn.Name,
			Description: conn.Description,
			Avatar:      conn.Avatar,
			ExpiryTime:  durationpb.New(conn.ExpiryTime),
			LastRefresh: timestamppb.New(conn.LastRefresh),
			Labels:      labels,
			Data:        data,
		})
	}

	return &proto.ListConnectionsResponse{
		Connections: mappedConnections,
	}, nil
}

func (s *ResourcePluginServer) GetConnection(
	ctx context.Context,
	in *proto.GetConnectionRequest,
) (*proto.Connection, error) {
	pluginCtx := pkgtypes.NewPluginContextFromCtx(ctx)

	conn, err := s.Impl.GetConnection(pluginCtx, in.GetId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get connection: %s", err.Error())
	}
	data, err := structpb.NewStruct(conn.Data)
	if err != nil {
		return nil, status.Errorf(
			codes.Internal,
			"failed to convert connection data to struct: %s",
			err.Error(),
		)
	}
	labels, err := structpb.NewStruct(conn.Labels)
	if err != nil {
		return nil, status.Errorf(
			codes.Internal,
			"failed to convert connection labels to struct: %s",
			err.Error(),
		)
	}
	return &proto.Connection{
		Id:          conn.ID,
		Uid:         conn.UID,
		Name:        conn.Name,
		Description: conn.Description,
		Avatar:      conn.Avatar,
		ExpiryTime:  durationpb.New(conn.ExpiryTime),
		LastRefresh: timestamppb.New(conn.LastRefresh),
		Labels:      labels,
		Data:        data,
	}, nil
}

func (s *ResourcePluginServer) UpdateConnection(
	ctx context.Context,
	in *proto.UpdateConnectionRequest,
) (*proto.UpdateConnectionResponse, error) {
	pluginCtx := pkgtypes.NewPluginContextFromCtx(ctx)

	conn := pkgtypes.Connection{
		ID: in.GetId(),
	}
	if in.GetName() != nil {
		conn.Name = in.GetName().GetValue()
	}
	if in.GetDescription() != nil {
		conn.Description = in.GetDescription().GetValue()
	}
	if in.GetAvatar() != nil {
		conn.Avatar = in.GetAvatar().GetValue()
	}

	labels := in.GetLabels()
	if labels != nil {
		conn.Labels = labels.AsMap()
	}

	conn, err := s.Impl.UpdateConnection(pluginCtx, conn)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update connection: %s", err.Error())
	}
	return &proto.UpdateConnectionResponse{
		Connection: &proto.Connection{
			Id:          conn.ID,
			Uid:         conn.UID,
			Name:        conn.Name,
			Description: conn.Description,
			Avatar:      conn.Avatar,
			ExpiryTime:  durationpb.New(conn.ExpiryTime),
			LastRefresh: timestamppb.New(conn.LastRefresh),
			Labels:      labels,
		},
	}, nil
}

func (s *ResourcePluginServer) DeleteConnection(
	ctx context.Context,
	in *proto.DeleteConnectionRequest,
) (*emptypb.Empty, error) {
	pluginCtx := pkgtypes.NewPluginContextFromCtx(ctx)
	if err := s.Impl.DeleteConnection(pluginCtx, in.GetId()); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete connection: %s", err.Error())
	}
	return &emptypb.Empty{}, nil
}

func (s *ResourcePluginServer) Get(
	ctx context.Context,
	in *proto.GetRequest,
) (*proto.GetResponse, error) {
	pluginCtx := pkgtypes.NewPluginContextFromCtx(ctx)

	conn, err := s.Impl.GetConnection(pluginCtx, in.GetContext())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get connection: %s", err.Error())
	}
	pluginCtx.SetConnection(&conn)

	resp, err := s.Impl.Get(pluginCtx, in.GetKey(), types.GetInput{
		ID:        in.GetId(),
		Namespace: in.GetNamespace(),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get resource: %s", err.Error())
	}

	data, err := structpb.NewStruct(resp.Result)
	if err != nil {
		return nil, status.Errorf(
			codes.Internal,
			"failed to convert resource to struct: %s",
			err.Error(),
		)
	}

	return &proto.GetResponse{
		Success: true,
		Data:    data,
	}, nil
}

func (s *ResourcePluginServer) List(
	ctx context.Context,
	in *proto.ListRequest,
) (*proto.ListResponse, error) {
	pluginCtx := pkgtypes.NewPluginContextFromCtx(ctx)

	conn, err := s.Impl.GetConnection(pluginCtx, in.GetContext())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get connection: %s", err.Error())
	}
	pluginCtx.SetConnection(&conn)

	resp, err := s.Impl.List(pluginCtx, in.GetKey(), types.ListInput{
		Namespaces: in.GetNamespaces(),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list resources: %s", err.Error())
	}

	data, err := structpb.NewStruct(resp.Result)
	if err != nil {
		return nil, status.Errorf(
			codes.Internal,
			"failed to convert resources to struct: %s",
			err.Error(),
		)
	}

	return &proto.ListResponse{
		Success: true,
		Data:    data,
	}, nil
}

func (s *ResourcePluginServer) Find(
	ctx context.Context,
	in *proto.FindRequest,
) (*proto.FindResponse, error) {
	pluginCtx := pkgtypes.NewPluginContextFromCtx(ctx)

	conn, err := s.Impl.GetConnection(pluginCtx, in.GetContext())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get connection: %s", err.Error())
	}
	pluginCtx.SetConnection(&conn)

	resp, err := s.Impl.Find(pluginCtx, in.GetKey(), types.FindInput{
		Namespaces: in.GetNamespaces(),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to find resources: %s", err.Error())
	}

	data, err := structpb.NewStruct(resp.Result)
	if err != nil {
		return nil, status.Errorf(
			codes.Internal,
			"failed to convert resources to struct: %s",
			err.Error(),
		)
	}

	return &proto.FindResponse{
		Success: true,
		Data:    data,
	}, nil
}

func (s *ResourcePluginServer) Create(
	ctx context.Context,
	in *proto.CreateRequest,
) (*proto.CreateResponse, error) {
	pluginCtx := pkgtypes.NewPluginContextFromCtx(ctx)

	conn, err := s.Impl.GetConnection(pluginCtx, in.GetContext())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get connection: %s", err.Error())
	}
	pluginCtx.SetConnection(&conn)

	resp, err := s.Impl.Create(pluginCtx, in.GetKey(), types.CreateInput{
		Namespace: in.GetNamespace(),
		Input:     in.GetData().AsMap(),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create resources: %s", err.Error())
	}

	data, err := structpb.NewStruct(resp.Result)
	if err != nil {
		return nil, status.Errorf(
			codes.Internal,
			"failed to convert resources to struct: %s",
			err.Error(),
		)
	}

	return &proto.CreateResponse{
		Success: true,
		Data:    data,
	}, nil
}

func (s *ResourcePluginServer) Update(
	ctx context.Context,
	in *proto.UpdateRequest,
) (*proto.UpdateResponse, error) {
	pluginCtx := pkgtypes.NewPluginContextFromCtx(ctx)

	conn, err := s.Impl.GetConnection(pluginCtx, in.GetContext())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get connection: %s", err.Error())
	}
	pluginCtx.SetConnection(&conn)

	return nil, status.Errorf(codes.Unimplemented, "method Update not implemented")
}

func (s *ResourcePluginServer) Delete(
	ctx context.Context,
	in *proto.DeleteRequest,
) (*proto.DeleteResponse, error) {
	pluginCtx := pkgtypes.NewPluginContextFromCtx(ctx)

	conn, err := s.Impl.GetConnection(pluginCtx, in.GetContext())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get connection: %s", err.Error())
	}
	pluginCtx.SetConnection(&conn)

	return nil, status.Errorf(codes.Unimplemented, "method Delete not implemented")
}

func (s *ResourcePluginServer) StartConnectionInformer(
	ctx context.Context,
	in *proto.StartConnectionInformerRequest,
) (*emptypb.Empty, error) {
	pluginCtx := pkgtypes.NewPluginContextFromCtx(ctx)
	if err := s.Impl.StartConnectionInformer(pluginCtx, in.GetConnection()); err != nil {
		return nil, status.Errorf(
			codes.Internal,
			"failed to start connection informer: %s",
			err.Error(),
		)
	}
	return &emptypb.Empty{}, nil
}

func (s *ResourcePluginServer) StopConnectionInformer(
	ctx context.Context,
	in *proto.StopConnectionInformerRequest,
) (*emptypb.Empty, error) {
	pluginCtx := pkgtypes.NewPluginContextFromCtx(ctx)
	if err := s.Impl.StopConnectionInformer(pluginCtx, in.GetConnection()); err != nil {
		return nil, status.Errorf(
			codes.Internal,
			"failed to start connection informer: %s",
			err.Error(),
		)
	}
	return &emptypb.Empty{}, nil
}

// Namespaceless and connectionless.
func (s *ResourcePluginServer) ListenForEvents(
	_ *emptypb.Empty,
	stream proto.ResourcePlugin_ListenForEventsServer,
) error {
	log.Printf("ListenForEvents")
	pluginCtx := pkgtypes.NewPluginContextFromCtx(stream.Context())

	addChan := make(chan types.InformerAddPayload)
	updateChan := make(chan types.InformerUpdatePayload)
	deleteChan := make(chan types.InformerDeletePayload)

	go func() {
		if err := s.Impl.ListenForEvents(pluginCtx, addChan, updateChan, deleteChan); err != nil {
			log.Printf("failed to listen for events: %s", err.Error())
			return
		}
	}()

	for {
		select {
		case <-stream.Context().Done():
			log.Printf("Context Done")
			return status.Errorf(codes.Canceled, "context canceled")
		case event := <-addChan:
			data, err := structpb.NewStruct(event.Data)
			if err != nil {
				log.Printf("failed to convert data to struct: %s", err.Error())
				continue
			}
			if err = stream.SendMsg(&proto.InformerEvent{
				Key:        event.Key,
				Connection: event.Connection,
				Id:         event.ID,
				Namespace:  event.Namespace,
				Action: &proto.InformerEvent_Add{
					Add: &proto.InformerAddEvent{
						Data: data,
					},
				},
			}); err != nil {
				// do nothing for now
				log.Printf("failed to send add event: %s", err.Error())
			}
		case event := <-updateChan:
			olddata, err := structpb.NewStruct(event.OldData)
			if err != nil {
				log.Printf("failed to convert olddata to struct: %s", err.Error())
				continue
			}
			newdata, err := structpb.NewStruct(event.NewData)
			if err != nil {
				log.Printf("failed to convert newdata to struct: %s", err.Error())
				continue
			}

			if err = stream.SendMsg(&proto.InformerEvent{
				Key:        event.Key,
				Connection: event.Connection,
				Id:         event.ID,
				Namespace:  event.Namespace,
				Action: &proto.InformerEvent_Update{
					Update: &proto.InformerUpdateEvent{
						OldData: olddata,
						NewData: newdata,
					},
				},
			}); err != nil {
				// do nothing for now
				log.Printf("failed to send add event: %s", err.Error())
			}
		case event := <-deleteChan:
			data, err := structpb.NewStruct(event.Data)
			if err != nil {
				log.Printf("failed to convert data to struct: %s", err.Error())
				continue
			}
			if err = stream.SendMsg(&proto.InformerEvent{
				Key:        event.Key,
				Connection: event.Connection,
				Id:         event.ID,
				Namespace:  event.Namespace,
				Action: &proto.InformerEvent_Delete{
					Delete: &proto.InformerDeleteEvent{
						Data: data,
					},
				},
			}); err != nil {
				// do nothing for now
				log.Printf("failed to send add event: %s", err.Error())
			}
		}
	}
}

func (s *ResourcePluginServer) GetLayout(
	ctx context.Context,
	in *proto.GetLayoutRequest,
) (*proto.Layout, error) {
	layout, err := s.Impl.GetLayout(in.GetId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get layout: %s", err.Error())
	}
	items := make([]*proto.LayoutItem, 0, len(layout))
	for _, item := range layout {
		items = append(items, &proto.LayoutItem{
			Id:          item.ID,
			Label:       item.Label,
			Description: item.Description,
		})
	}
	return &proto.Layout{
		Items: items,
	}, nil
}

func (s *ResourcePluginServer) GetDefaultLayout(
	ctx context.Context,
	_ *emptypb.Empty,
) (*proto.Layout, error) {
	layout, err := s.Impl.GetDefaultLayout()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get default layout: %s", err.Error())
	}
	items := make([]*proto.LayoutItem, 0, len(layout))
	for _, item := range layout {
		items = append(items, &proto.LayoutItem{
			Id:          item.ID,
			Label:       item.Label,
			Description: item.Description,
		})
	}
	return &proto.Layout{
		Items: items,
	}, nil
}

func (s *ResourcePluginServer) SetLayout(
	ctx context.Context,
	in *proto.SetLayoutRequest,
) (*emptypb.Empty, error) {
	inlayout := in.GetLayout()

	layout := make([]types.LayoutItem, 0, len(inlayout.GetItems()))
	for _, item := range inlayout.GetItems() {
		layout = append(layout, protoToLayoutItem(item))
	}
	if err := s.Impl.SetLayout(in.GetId(), layout); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to set layout: %s", err.Error())
	}
	return &emptypb.Empty{}, nil
}
