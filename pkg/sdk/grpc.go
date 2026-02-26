package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/grpc-ecosystem/go-grpc-middleware/v2/metadata"
	"google.golang.org/grpc"
	grpcMetadata "google.golang.org/grpc/metadata"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// Individual metadata keys for the new format.
const (
	MDKeyRequestID       = "omniview-request-id"
	MDKeyRequesterID     = "omniview-requester-id"
	MDKeyConnectionID    = "omniview-connection-id"
	MDKeyConnectionData  = "omniview-connection-data"
	MDKeyResourceKey     = "omniview-resource-key"
	MDKeyProtocolVersion = "omniview-protocol-version"
)

// UseClientPluginContext serializes the plugin context and injects it into
// gRPC metadata using individual keys.
func UseClientPluginContext(ctx context.Context) (context.Context, error) {
	pc := types.PluginContextFromContext(ctx)
	if pc == nil {
		return ctx, errors.New("no plugin context in context")
	}

	pairs := []string{
		MDKeyRequestID, pc.RequestID,
		MDKeyRequesterID, pc.RequesterID,
		MDKeyProtocolVersion, "2",
	}

	if pc.Connection != nil {
		pairs = append(pairs, MDKeyConnectionID, pc.Connection.ID)
		if pc.Connection.Data != nil {
			encoded, err := json.Marshal(pc.Connection.Data)
			if err != nil {
				return ctx, fmt.Errorf("failed to encode connection data: %w", err)
			}
			pairs = append(pairs, MDKeyConnectionData, string(encoded))
		}
	}
	if pc.ResourceContext != nil && pc.ResourceContext.Key != "" {
		pairs = append(pairs, MDKeyResourceKey, pc.ResourceContext.Key)
	}

	md := metadata.MD(grpcMetadata.Pairs(pairs...))
	return md.ToOutgoing(ctx), nil
}

func ClientPluginContextInterceptor(
	ctx context.Context,
	method string,
	req, reply any,
	cc *grpc.ClientConn,
	invoker grpc.UnaryInvoker,
	opts ...grpc.CallOption,
) error {
	ctx, err := UseClientPluginContext(ctx)
	if err != nil {
		log.Println("error:", err)
	}
	return invoker(ctx, method, req, reply, cc, opts...)
}

func withClientOpts(opts []grpc.DialOption) []grpc.DialOption {
	if opts == nil {
		opts = make([]grpc.DialOption, 0)
	}
	opts = append(opts, grpc.WithUnaryInterceptor(ClientPluginContextInterceptor))
	return opts
}
