package tools

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	toolspb "github.com/omniviewdev/plugin-sdk/proto/v1/tools"
)

// PluginServer serves a tools provider over gRPC.
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
		switch resourcev2.ErrorCode(err) {
		case "INVALID_REQUEST":
			return codes.InvalidArgument
		case "NOT_FOUND":
			return codes.NotFound
		case "UNSUPPORTED", "RESET_REQUIRED":
			return codes.FailedPrecondition
		}
		return codes.Internal
	}
}

// ListTools returns the provider's tool descriptors for the caller's plugin context.
func (s *PluginServer) ListTools(
	ctx context.Context,
	in *toolspb.ListToolsRequest,
) (*toolspb.ListToolsResponse, error) {
	if in.GetContractVersion() != ContractVersion {
		return nil, status.Error(codes.FailedPrecondition, "Unsupported tools contract.")
	}
	descriptors, err := s.Impl.ListTools(types.PluginContextFromContext(ctx))
	if err != nil {
		return nil, status.Error(grpcCodeForToolsError(err), "Tool discovery failed.")
	}
	out := &toolspb.ListToolsResponse{
		ContractVersion: ContractVersion,
		Tools:           make([]*toolspb.ToolDescriptor, 0, len(descriptors)),
	}
	for _, d := range descriptors {
		out.Tools = append(out.Tools, descriptorToProto(d))
	}
	return out, nil
}

// CallTool invokes the provider and maps provider errors to gRPC status codes.
func (s *PluginServer) CallTool(
	ctx context.Context,
	in *toolspb.CallToolRequest,
) (*toolspb.CallToolResponse, error) {
	if in.GetContractVersion() != ContractVersion {
		return nil, status.Error(codes.FailedPrecondition, "Unsupported tools contract.")
	}
	// Empty metadata selectors must not disappear during context extraction.
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if ids, present := md["omniview-connection-id"]; present && (len(ids) != 1 || ids[0] == "") {
			return nil, status.Error(codes.InvalidArgument, "Selected tool connection must be nonempty and singular.")
		}
	}
	input := CallInput{Name: in.GetName(), Arguments: in.GetArguments()}
	if in.GetRoute() != nil {
		route, err := resourcev2.FromProtoAccessRoute(in.GetRoute())
		if err != nil {
			return nil, status.Error(grpcCodeForToolsError(err), "Invalid tool route.")
		}
		input.Route = &route
	}
	pc := types.PluginContextFromContext(ctx)
	if pc == nil {
		// Raw RPC callers need no identity metadata, but retain RPC lifetime.
		pc = &types.PluginContext{Context: ctx}
	}
	result, err := s.Impl.CallTool(pc, input)
	if err != nil {
		return nil, status.Error(grpcCodeForToolsError(err), "Tool call failed.")
	}
	if result == nil || result.IsError {
		result = safeFailure(result)
	}
	return resultToProto(result), nil
}
