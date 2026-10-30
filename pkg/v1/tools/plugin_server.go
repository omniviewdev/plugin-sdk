package tools

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	toolspb "github.com/omniviewdev/plugin-sdk/proto/v1/tools"
)

type PluginServer struct {
	toolspb.UnimplementedToolsPluginServer
	Impl Provider
}

func grpcCodeForToolsError(err error) codes.Code {
	if s, ok := status.FromError(err); ok && s.Code() != codes.OK {
		return s.Code()
	}
	switch {
	case errors.Is(err, context.Canceled):
		return codes.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return codes.DeadlineExceeded
	case errors.Is(err, ErrToolNotFound):
		return codes.NotFound
	case errors.Is(err, ErrConnectionRequired):
		return codes.FailedPrecondition
	default:
		return codes.Internal
	}
}

func (s *PluginServer) ListTools(
	ctx context.Context,
	_ *emptypb.Empty,
) (*toolspb.ListToolsResponse, error) {
	descriptors, err := s.Impl.ListTools(types.PluginContextFromContext(ctx))
	if err != nil {
		return nil, status.Errorf(grpcCodeForToolsError(err), "%v", err)
	}
	out := &toolspb.ListToolsResponse{Tools: make([]*toolspb.ToolDescriptor, 0, len(descriptors))}
	for _, d := range descriptors {
		out.Tools = append(out.Tools, descriptorToProto(d))
	}
	return out, nil
}

func (s *PluginServer) CallTool(
	ctx context.Context,
	in *toolspb.CallToolRequest,
) (*toolspb.CallToolResponse, error) {
	result, err := s.Impl.CallTool(types.PluginContextFromContext(ctx), CallInput{
		Name:      in.GetName(),
		Arguments: in.GetArguments(),
	})
	if err != nil {
		return nil, status.Errorf(grpcCodeForToolsError(err), "%v", err)
	}
	return resultToProto(result), nil
}
