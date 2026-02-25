package sdk

import (
	"context"
	"errors"
	"log"

	"github.com/grpc-ecosystem/go-grpc-middleware/v2/metadata"
	"google.golang.org/grpc"
	grpcMetadata "google.golang.org/grpc/metadata"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/utils"
)

// Individual metadata keys for the new format.
const (
	MDKeyRequestID       = "omniview-request-id"
	MDKeyRequesterID     = "omniview-requester-id"
	MDKeyConnectionID    = "omniview-connection-id"
	MDKeyResourceKey     = "omniview-resource-key"
	MDKeyProtocolVersion = "omniview-protocol-version"
)

// UseClientPluginContext serializes the plugin context and injects it into
// gRPC metadata using both the new individual keys and the legacy JSON blob
// for backward compatibility with older plugin binaries.
func UseClientPluginContext(ctx context.Context) (context.Context, error) {
	pc := types.PluginContextFromContext(ctx)
	if pc == nil {
		return ctx, errors.New("no plugin context in context")
	}

	// Legacy JSON blob (backward compat with older plugin binaries).
	serialized, err := types.SerializePluginContext(pc)
	if err != nil {
		return ctx, err
	}

	pairs := []string{
		utils.PluginContextMDKey, serialized,
		MDKeyRequestID, pc.RequestID,
		MDKeyRequesterID, pc.RequesterID,
		MDKeyProtocolVersion, "1",
	}

	if pc.Connection != nil {
		pairs = append(pairs, MDKeyConnectionID, pc.Connection.ID)
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
