package networker

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	networkerpb "github.com/omniviewdev/plugin-sdk/proto/v1/networker"
)

// PluginServer exposes a forwarding provider over gRPC with resource wire admission.
type PluginServer struct {
	networkerpb.UnimplementedNetworkerPluginServer
	Impl Provider
}

// grpcCodeForNetworkerError maps NetworkerError codes to gRPC status codes.
// It also preserves context cancellation/deadline and existing gRPC statuses.
func grpcCodeForNetworkerError(err error) codes.Code {
	// Preserve existing gRPC status codes.
	if s, ok := status.FromError(err); ok && s.Code() != codes.OK {
		return s.Code()
	}
	// Map context errors.
	if errors.Is(err, context.Canceled) {
		return codes.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return codes.DeadlineExceeded
	}
	var nerr *NetworkerError
	if errors.As(err, &nerr) {
		switch nerr.Code {
		case ErrCodeSessionNotFound:
			return codes.NotFound
		case ErrCodeNoHandlerFound:
			return codes.NotFound
		case ErrCodePortUnavailable:
			return codes.FailedPrecondition
		case ErrCodeForwarderFailed:
			return codes.Internal
		case ErrCodeInvalidStateTransition:
			return codes.FailedPrecondition
		case ErrCodeInvalidConnectionType:
			return codes.InvalidArgument
		}
	}
	return codes.Internal
}

// GetSupportedPortForwardTargets lists resource keys with registered forwarders.
func (s *PluginServer) GetSupportedPortForwardTargets(
	ctx context.Context,
	_ *emptypb.Empty,
) (*networkerpb.GetSupportedPortForwardTargetsResponse, error) {
	resp, err := s.Impl.GetSupportedPortForwardTargets(types.PluginContextFromContext(ctx))
	if err != nil {
		return nil, status.Errorf(grpcCodeForNetworkerError(err), "%v", err)
	}

	return &networkerpb.GetSupportedPortForwardTargetsResponse{
		Resources: resp,
	}, nil
}

// GetPortForwardSession returns a bounded wire snapshot for the requested session.
func (s *PluginServer) GetPortForwardSession(
	ctx context.Context,
	in *networkerpb.PortForwardSessionByIdRequest,
) (*networkerpb.PortForwardSessionByIdResponse, error) {
	resp, err := s.Impl.GetPortForwardSession(types.PluginContextFromContext(ctx), in.GetId())
	if err != nil {
		return nil, networkRPCError(err)
	}
	if resp == nil {
		return &networkerpb.PortForwardSessionByIdResponse{Session: nil}, nil
	}
	if err := preflightSessions([]*PortForwardSession{resp}, false); err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}

	session, err := resp.ToProto()
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	return &networkerpb.PortForwardSessionByIdResponse{Session: session}, nil
}

// ListPortForwardSessions returns session snapshots within the aggregate wire limit.
func (s *PluginServer) ListPortForwardSessions(
	ctx context.Context,
	_ *emptypb.Empty,
) (*networkerpb.PortForwardSessionListResponse, error) {
	resp, err := s.Impl.ListPortForwardSessions(types.PluginContextFromContext(ctx))
	if err != nil {
		return nil, networkRPCError(err)
	}
	if err := preflightSessions(resp, false); err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	sessions := make([]*networkerpb.PortForwardSession, 0, len(resp))
	for _, session := range resp {
		if session == nil {
			continue
		}
		p, err := session.ToProto()
		if err != nil {
			return nil, resourceplugin.ResourceRPCError(err)
		}
		sessions = append(sessions, p)
	}

	out := &networkerpb.PortForwardSessionListResponse{Sessions: sessions}
	if err := validateFrame(out); err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	return out, nil
}

// FindPortForwardSessions returns bounded snapshots matching the supplied filters.
func (s *PluginServer) FindPortForwardSessions(
	ctx context.Context,
	in *networkerpb.FindPortForwardSessionRequest,
) (*networkerpb.PortForwardSessionListResponse, error) {
	resp, err := s.Impl.FindPortForwardSessions(
		types.PluginContextFromContext(ctx),
		NewFindPortForwardSessionRequestFromProto(in),
	)
	if err != nil {
		return nil, networkRPCError(err)
	}

	if err := preflightSessions(resp, false); err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	sessions := make([]*networkerpb.PortForwardSession, 0, len(resp))
	for _, session := range resp {
		if session == nil {
			continue
		}
		p, err := session.ToProto()
		if err != nil {
			return nil, resourceplugin.ResourceRPCError(err)
		}
		sessions = append(sessions, p)
	}

	out := &networkerpb.PortForwardSessionListResponse{Sessions: sessions}
	if err := validateFrame(out); err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	return out, nil
}

// StartPortForwardSession starts a legacy static forward and rejects resource payloads.
func (s *PluginServer) StartPortForwardSession(
	ctx context.Context,
	in *networkerpb.PortForwardSessionOptions,
) (*networkerpb.PortForwardSessionByIdResponse, error) {
	if in.GetResourceConnection() != nil {
		return nil, resourceplugin.ResourceRPCError(resourceUnsupported())
	}
	opts, err := NewPortForwardSessionOptionsFromProto(in)
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	resp, err := s.Impl.StartPortForwardSession(types.PluginContextFromContext(ctx), *opts)
	if err != nil {
		return nil, networkRPCError(err)
	}
	if resp == nil {
		return &networkerpb.PortForwardSessionByIdResponse{Session: nil}, nil
	}
	if err := preflightSessions([]*PortForwardSession{resp}, false); err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}

	session, err := resp.ToProto()
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	return &networkerpb.PortForwardSessionByIdResponse{Session: session}, nil
}

// ClosePortForwardSession closes the requested forward and returns its final snapshot.
func (s *PluginServer) ClosePortForwardSession(
	ctx context.Context,
	in *networkerpb.PortForwardSessionByIdRequest,
) (*networkerpb.PortForwardSessionByIdResponse, error) {
	resp, err := s.Impl.ClosePortForwardSession(
		types.PluginContextFromContext(ctx),
		in.GetId(),
	)
	if err != nil {
		return nil, networkRPCError(err)
	}
	if resp == nil {
		return &networkerpb.PortForwardSessionByIdResponse{Session: nil}, nil
	}
	if err := preflightSessions([]*PortForwardSession{resp}, false); err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}

	session, err := resp.ToProto()
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	return &networkerpb.PortForwardSessionByIdResponse{Session: session}, nil
}

// StartResourcePortForwardSession admits resource-v2 context and starts a forward.
// Caller cancellation remains attached through readiness; accepted session lifetime
// and its retained connection-generation lease belong to the forwarding provider.
func (s *PluginServer) StartResourcePortForwardSession(
	ctx context.Context,
	in *networkerpb.PortForwardSessionOptions,
) (*networkerpb.ResourcePortForwardSessionResponse, error) {
	var err error
	ctx, err = resourceplugin.IncomingSessionContext(ctx)
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	opts, err := NewPortForwardSessionOptionsFromProto(in)
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	pctx := types.PluginContextFromContext(ctx)
	if pctx == nil {
		pctx = &types.PluginContext{Context: ctx}
	}
	if err := validateResourceContext(pctx, *opts); err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	provider, ok := s.Impl.(ResourcePortForwardProvider)
	if !ok {
		return nil, resourceplugin.ResourceRPCError(resourceUnsupported())
	}
	session, err := provider.StartResourcePortForwardSession(pctx, *opts)
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	if session == nil {
		return nil, resourceplugin.ResourceRPCError(resourceInvalidResponse())
	}
	if _, ok := resourceConnection(session.Connection); !ok {
		return nil, resourceplugin.ResourceRPCError(resourceInvalidResponse())
	}
	if err := preflightSessions([]*PortForwardSession{session}, true); err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	wire, err := session.ToProto()
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	out := &networkerpb.ResourcePortForwardSessionResponse{ResourceWireVersion: 2, Session: wire}
	if err := validateFrame(out); err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	return out, nil
}

func networkRPCError(err error) error {
	var domain *resourcev2.ResourceOperationError
	if errors.As(err, &domain) {
		return resourceplugin.ResourceRPCError(err)
	}
	return status.Errorf(grpcCodeForNetworkerError(err), "%v", err)
}
