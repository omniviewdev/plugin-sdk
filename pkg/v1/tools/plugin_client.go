package tools

import (
	"context"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	logging "github.com/omniviewdev/plugin-sdk/log"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
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
	resp, err := p.client.ListTools(
		types.WithPluginContext(grpcCtx, pctx),
		&toolspb.ListToolsRequest{ContractVersion: ContractVersion},
	)
	if err != nil {
		return nil, status.Error(status.Code(err), "Tool discovery failed.")
	}
	if resp.GetContractVersion() != ContractVersion {
		return nil, fmt.Errorf("unsupported tools contract")
	}
	out := make([]Descriptor, 0, len(resp.GetTools()))
	for _, d := range resp.GetTools() {
		if d == nil {
			return nil, fmt.Errorf("invalid tool descriptor")
		}
		if _, err := compileSchema(d.GetInputSchema(), true); err != nil {
			return nil, fmt.Errorf("invalid tool input schema: %w", err)
		}
		if len(d.GetOutputSchema()) != 0 {
			if _, err := compileSchema(d.GetOutputSchema(), true); err != nil {
				return nil, fmt.Errorf("invalid tool output schema: %w", err)
			}
		}
		out = append(out, descriptorFromProto(d))
	}
	return out, nil
}

// CallTool invokes a remote tool using the caller's cancellation and deadline.
func (p *PluginClient) CallTool(ctx *types.PluginContext, input CallInput) (*Result, error) {
	descriptors, err := p.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	var selected *Descriptor
	for i := range descriptors {
		if descriptors[i].Name == input.Name {
			selected = &descriptors[i]
			break
		}
	}
	if selected == nil {
		return nil, ErrToolNotFound
	}
	schema, err := compileSchema(selected.InputSchema, true)
	if err != nil {
		return nil, err
	}
	if err := validateJSON(schema, input.Arguments); err != nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid tool arguments.")
	}
	if ctx != nil && ctx.Connection != nil && ctx.Connection.ID == "" {
		return nil, resourcev2.NewError("INVALID_REQUEST", "Selected tool connection must not be empty.")
	}
	grpcCtx, pctx := rpcContext(ctx)
	request := &toolspb.CallToolRequest{Name: input.Name, Arguments: input.Arguments, ContractVersion: ContractVersion}
	if input.Route != nil {
		var err error
		request.Route, err = resourcev2.ToProtoAccessRoute(*input.Route)
		if err != nil {
			return nil, err
		}
	}
	resp, err := p.client.CallTool(types.WithPluginContext(grpcCtx, pctx), request)
	if err != nil {
		return nil, status.Error(status.Code(err), "Tool call failed.")
	}
	result := resultFromProto(resp)
	if result.IsError {
		return safeFailure(result), nil
	}
	if len(selected.OutputSchema) != 0 {
		schema, err := compileSchema(selected.OutputSchema, true)
		if err != nil {
			return nil, err
		}
		if err := validateJSON(schema, result.StructuredContent); err != nil {
			return safeFailure(nil), nil
		}
	} else if len(result.StructuredContent) != 0 {
		if _, err := decodeObject(result.StructuredContent); err != nil {
			return safeFailure(nil), nil
		}
	}
	return result, nil
}
