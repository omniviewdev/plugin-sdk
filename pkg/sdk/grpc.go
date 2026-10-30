// Package sdk composes plugin capabilities, connection-context transport, and process serving.
package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	grpcMetadata "google.golang.org/grpc/metadata"
	"google.golang.org/grpc/stats"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// streamingMethodFilter returns an otelgrpc Filter that skips long-lived
// streaming RPCs. These streams can run for the entire plugin lifetime
// (hours/days) and would produce unusable, never-ending spans that bloat
// memory and Tempo storage. Short-lived unary RPCs are always traced.
func streamingMethodFilter() otelgrpc.Filter {
	// Streaming methods that should NOT get otelgrpc spans.
	// Matched by suffix so we don't need full package paths.
	skip := []string{
		"/WatchConnections",
		"/ListenForEvents",
		"/Stream",
		"/StreamMetrics",
		"/StreamAction",
		"/Run",
	}
	return func(info *stats.RPCTagInfo) bool {
		for _, suffix := range skip {
			if strings.HasSuffix(info.FullMethodName, suffix) {
				return false
			}
		}
		return true
	}
}

// ErrNoPluginContext is returned when no plugin context is found in the context.
var ErrNoPluginContext = errors.New("no plugin context in context")

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
		return ctx, ErrNoPluginContext
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

	current, _ := grpcMetadata.FromOutgoingContext(ctx)
	outgoing := current.Copy()
	for key, values := range grpcMetadata.Pairs(pairs...) {
		outgoing[key] = values
	}
	return grpcMetadata.NewOutgoingContext(ctx, outgoing), nil
}

// ClientPluginContextInterceptor propagates request diagnostics and selected connection
// metadata without granting resource admission. Missing plugin context leaves the call unchanged.
func ClientPluginContextInterceptor(log logging.Logger) grpc.UnaryClientInterceptor {
	if log == nil {
		log = logging.Default()
	}
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		ctx, err := UseClientPluginContext(ctx)
		if err != nil {
			if !errors.Is(err, ErrNoPluginContext) {
				return fmt.Errorf("%s: %w", method, err)
			}
			// Plugin context may not be present for all calls; proceed with original context.
			log.Debug(ctx, "UseClientPluginContext: no plugin context available")
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// StreamClientPluginContextInterceptor uses the same admitted utility context
// transport as unary RPCs. Opening a stream must not drop its selected connection
// or replace the outgoing catalog/session metadata already present.
func StreamClientPluginContextInterceptor(log logging.Logger) grpc.StreamClientInterceptor {
	if log == nil {
		log = logging.Default()
	}
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		next, err := UseClientPluginContext(ctx)
		if err != nil {
			if !errors.Is(err, ErrNoPluginContext) {
				return nil, fmt.Errorf("%s: %w", method, err)
			}
			log.Debug(ctx, "UseClientPluginContext: no plugin context available")
		}
		return streamer(next, desc, cc, method, opts...)
	}
}

func withClientOpts(opts []grpc.DialOption, log logging.Logger) []grpc.DialOption {
	if log == nil {
		log = logging.Default()
	}
	if opts == nil {
		opts = make([]grpc.DialOption, 0)
	}
	opts = append(opts, grpc.WithStatsHandler(otelgrpc.NewClientHandler(
		otelgrpc.WithFilter(streamingMethodFilter()),
	)))
	opts = append(opts, grpc.WithChainUnaryInterceptor(ClientPluginContextInterceptor(log)))
	opts = append(opts, grpc.WithChainStreamInterceptor(StreamClientPluginContextInterceptor(log)))
	return opts
}

// Deprecated: use ClientPluginContextInterceptor(log).
func LegacyClientPluginContextInterceptor(
	ctx context.Context,
	method string,
	req, reply any,
	cc *grpc.ClientConn,
	invoker grpc.UnaryInvoker,
	opts ...grpc.CallOption,
) error {
	return ClientPluginContextInterceptor(logging.Default())(ctx, method, req, reply, cc, invoker, opts...)
}
