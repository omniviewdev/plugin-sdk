package interceptors

import (
	"context"
	"log"

	"github.com/grpc-ecosystem/go-grpc-middleware/v2/metadata"
	"google.golang.org/grpc"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

const (
	// Legacy single JSON blob key.
	pluginContextMDKey = "plugin_context"

	// New individual metadata keys.
	mdKeyRequestID       = "omniview-request-id"
	mdKeyRequesterID     = "omniview-requester-id"
	mdKeyConnectionID    = "omniview-connection-id"
	mdKeyResourceKey     = "omniview-resource-key"
	mdKeyProtocolVersion = "omniview-protocol-version"
)

// useServerPluginContext extracts the plugin context from gRPC metadata and
// attaches it to ctx. It tries the new individual keys first, then falls back
// to the legacy JSON blob for backward compatibility.
func useServerPluginContext(ctx context.Context) (context.Context, error) {
	incoming := metadata.ExtractIncoming(ctx)

	// Try new individual keys first.
	requestID := incoming.Get(mdKeyRequestID)
	if requestID != "" {
		pc := &types.PluginContext{
			RequestID:   requestID,
			RequesterID: incoming.Get(mdKeyRequesterID),
		}

		connID := incoming.Get(mdKeyConnectionID)
		if connID != "" {
			pc.Connection = &types.Connection{ID: connID}
		}

		resourceKey := incoming.Get(mdKeyResourceKey)
		if resourceKey != "" {
			pc.ResourceContext = &types.ResourceContext{Key: resourceKey}
		}

		return types.WithPluginContext(ctx, pc), nil
	}

	// Fall back to legacy JSON blob.
	serialized := incoming.Get(pluginContextMDKey)
	if serialized == "" {
		return ctx, nil
	}
	deserialized, err := types.DeserializePluginContext(serialized)
	if err != nil {
		return ctx, err
	}
	return types.WithPluginContext(ctx, deserialized), nil
}

// UnaryPluginContext returns a unary server interceptor that extracts PluginContext
// from gRPC metadata and attaches it to the context.
func UnaryPluginContext() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		ctx, err := useServerPluginContext(ctx)
		if err != nil {
			log.Println("error:", err)
		}
		return handler(ctx, req)
	}
}

// StreamPluginContext returns a stream server interceptor that extracts PluginContext
// from gRPC metadata and attaches it to the context.
func StreamPluginContext() grpc.StreamServerInterceptor {
	return func(
		srv interface{},
		ss grpc.ServerStream,
		_ *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		ctx, err := useServerPluginContext(ss.Context())
		if err != nil {
			log.Println("error:", err)
		}
		wrapped := &wrappedServerStream{ServerStream: ss, ctx: ctx}
		return handler(srv, wrapped)
	}
}

// wrappedServerStream wraps a grpc.ServerStream to override the context.
type wrappedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedServerStream) Context() context.Context {
	return w.ctx
}
