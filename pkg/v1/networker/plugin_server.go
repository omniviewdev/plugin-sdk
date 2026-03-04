package networker

import (
	"context"
	"errors"

	"github.com/hashicorp/go-hclog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	networkerpb "github.com/omniviewdev/plugin-sdk/proto/v1/networker"
)

type PluginServer struct {
	networkerpb.UnimplementedNetworkerPluginServer
	log  hclog.Logger
	Impl Provider
}

// grpcCodeForError maps NetworkerError codes to gRPC status codes.
func grpcCodeForNetworkerError(err error) codes.Code {
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

func (s *PluginServer) GetPortForwardSession(
	ctx context.Context,
	in *networkerpb.PortForwardSessionByIdRequest,
) (*networkerpb.PortForwardSessionByIdResponse, error) {
	resp, err := s.Impl.GetPortForwardSession(types.PluginContextFromContext(ctx), in.GetId())
	if err != nil {
		return nil, status.Errorf(grpcCodeForNetworkerError(err), "%v", err)
	}

	return &networkerpb.PortForwardSessionByIdResponse{
		Session: resp.ToProto(),
	}, nil
}

func (s *PluginServer) ListPortForwardSessions(
	ctx context.Context,
	_ *emptypb.Empty,
) (*networkerpb.PortForwardSessionListResponse, error) {
	resp, err := s.Impl.ListPortForwardSessions(types.PluginContextFromContext(ctx))
	if err != nil {
		return nil, status.Errorf(grpcCodeForNetworkerError(err), "%v", err)
	}
	sessions := make([]*networkerpb.PortForwardSession, 0, len(resp))
	for _, session := range resp {
		sessions = append(sessions, session.ToProto())
	}

	return &networkerpb.PortForwardSessionListResponse{
		Sessions: sessions,
	}, nil
}

func (s *PluginServer) FindPortForwardSessions(
	ctx context.Context,
	in *networkerpb.FindPortForwardSessionRequest,
) (*networkerpb.PortForwardSessionListResponse, error) {
	resp, err := s.Impl.FindPortForwardSessions(
		types.PluginContextFromContext(ctx),
		NewFindPortForwardSessionRequestFromProto(in),
	)
	if err != nil {
		return nil, status.Errorf(grpcCodeForNetworkerError(err), "%v", err)
	}

	sessions := make([]*networkerpb.PortForwardSession, 0, len(resp))
	for _, session := range resp {
		sessions = append(sessions, session.ToProto())
	}

	return &networkerpb.PortForwardSessionListResponse{
		Sessions: sessions,
	}, nil
}

func (s *PluginServer) StartPortForwardSession(
	ctx context.Context,
	in *networkerpb.PortForwardSessionOptions,
) (*networkerpb.PortForwardSessionByIdResponse, error) {
	resp, err := s.Impl.StartPortForwardSession(
		types.PluginContextFromContext(ctx),
		*NewPortForwardSessionOptionsFromProto(in),
	)
	if err != nil {
		return nil, status.Errorf(grpcCodeForNetworkerError(err), "%v", err)
	}

	return &networkerpb.PortForwardSessionByIdResponse{
		Session: resp.ToProto(),
	}, nil
}

func (s *PluginServer) ClosePortForwardSession(
	ctx context.Context,
	in *networkerpb.PortForwardSessionByIdRequest,
) (*networkerpb.PortForwardSessionByIdResponse, error) {
	resp, err := s.Impl.ClosePortForwardSession(
		types.PluginContextFromContext(ctx),
		in.GetId(),
	)
	if err != nil {
		return nil, status.Errorf(grpcCodeForNetworkerError(err), "%v", err)
	}

	return &networkerpb.PortForwardSessionByIdResponse{
		Session: resp.ToProto(),
	}, nil
}
