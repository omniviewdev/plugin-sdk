package command

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	commandpb "github.com/omniviewdev/plugin-sdk/proto/v1/command"
)

// PluginServer adapts command RPCs to Impl without retaining request payloads.
type PluginServer struct {
	Impl Provider
}

// ============================== GENERAL COMMANDER ============================== //

// Call requires plugin context and dispatches the requested path. Handler failures
// become Internal RPC errors; missing context becomes InvalidArgument.
func (s *PluginServer) Call(
	ctx context.Context,
	req *commandpb.CallCommandRequest,
) (*commandpb.CallCommandResponse, error) {
	pctx := types.PluginContextFromContext(ctx)
	if pctx == nil {
		return nil, status.Errorf(codes.InvalidArgument, "missing plugin context")
	}
	resp, err := s.Impl.Call(pctx, req.GetPath(), req.GetBody())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to call command: %v", err)
	}

	return &commandpb.CallCommandResponse{
		Body: resp,
	}, nil
}
