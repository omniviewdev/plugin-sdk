package tools

import (
	"context"

	"google.golang.org/protobuf/types/known/emptypb"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	toolspb "github.com/omniviewdev/plugin-sdk/proto/v1/tools"
)

// PluginClient calls a remote tools provider over gRPC.
type PluginClient struct {
	client toolspb.ToolsPluginClient
	log    logging.Logger
}

var _ Provider = (*PluginClient)(nil)

// rpcContext extracts the caller's context for gRPC and returns a shallow copy
// of the PluginContext with its Context field cleared (to avoid redundant storage).
// This preserves the caller's cancellation/deadline on the gRPC call.
func rpcContext(ctx *types.PluginContext) (context.Context, *types.PluginContext) {
	if ctx == nil {
		return context.Background(), &types.PluginContext{}
	}
	grpcCtx := ctx.Context
	if grpcCtx == nil {
		grpcCtx = context.Background()
	}
	cp := *ctx
	cp.Context = nil
	return grpcCtx, &cp
}

// ListTools retrieves the remote tool descriptors using the caller's context.
func (p *PluginClient) ListTools(ctx *types.PluginContext) ([]Descriptor, error) {
	grpcCtx, pctx := rpcContext(ctx)
	resp, err := p.client.ListTools(types.WithPluginContext(grpcCtx, pctx), &emptypb.Empty{})
	if err != nil {
		return nil, err
	}
	out := make([]Descriptor, 0, len(resp.GetTools()))
	for _, d := range resp.GetTools() {
		out = append(out, descriptorFromProto(d))
	}
	return out, nil
}

// CallTool invokes a remote tool using the caller's cancellation and deadline.
func (p *PluginClient) CallTool(ctx *types.PluginContext, input CallInput) (*Result, error) {
	grpcCtx, pctx := rpcContext(ctx)
	resp, err := p.client.CallTool(types.WithPluginContext(grpcCtx, pctx), &toolspb.CallToolRequest{
		Name:      input.Name,
		Arguments: input.Arguments,
	})
	if err != nil {
		return nil, err
	}
	return resultFromProto(resp), nil
}
