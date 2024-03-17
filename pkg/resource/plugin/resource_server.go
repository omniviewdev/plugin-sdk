package plugin

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/omniviewdev/plugin-sdk/pkg/resource/types"
	pkgtypes "github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/proto"
)

// Here is the gRPC server that GRPCClient talks to.
type ResourcePluginServer struct {
	// This is the real implementation
	Impl types.ResourceProvider
}

func (s *ResourcePluginServer) Get(
	ctx context.Context,
	in *proto.GetRequest,
) (*proto.GetResponse, error) {
	pluginCtx := pkgtypes.NewPluginContextFromCtx(ctx)

	resp, err := s.Impl.Get(pluginCtx, in.GetKey(), types.GetInput{
		ID:        in.GetId(),
		Namespace: in.GetNamespace(),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get resource: %w", err)
	}

	data, err := structpb.NewStruct(resp.Result)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to convert resource to struct: %w", err)
	}

	return &proto.GetResponse{
		Success: true,
		Data:    data,
	}, nil
}

func (s *ResourcePluginServer) List(
	context.Context,
	*proto.ListRequest,
) (*proto.ListResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method List not implemented")
}

func (s *ResourcePluginServer) Find(
	context.Context,
	*proto.FindRequest,
) (*proto.FindResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Find not implemented")
}

func (s *ResourcePluginServer) Create(
	context.Context,
	*proto.CreateRequest,
) (*proto.CreateResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Create not implemented")
}

func (s *ResourcePluginServer) Update(
	context.Context,
	*proto.UpdateRequest,
) (*proto.UpdateResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Update not implemented")
}

func (s *ResourcePluginServer) Delete(
	context.Context,
	*proto.DeleteRequest,
) (*proto.DeleteResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Delete not implemented")
}

func (s *ResourcePluginServer) StartContextInformer(
	context.Context,
	*proto.StartContextInformerRequest,
) (*emptypb.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "method StartContextInformer not implemented")
}

func (s *ResourcePluginServer) StopContextInformer(
	context.Context,
	*proto.StopContextInformerRequest,
) (*emptypb.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "method StopContextInformer not implemented")
}

func (s *ResourcePluginServer) ListenForEvents(
	*emptypb.Empty,
	proto.ResourcePlugin_ListenForEventsServer,
) error {
	return status.Errorf(codes.Unimplemented, "method ListenForEvents not implemented")
}
