package networker

import (
	"context"

	"github.com/hashicorp/go-hclog"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	networkerpb "github.com/omniviewdev/plugin-sdk/proto/v1/networker"
)

type PluginServer struct {
	log  hclog.Logger
	Impl Provider
}

// ============================== PORT FORWARDING ============================== //

func (s *PluginServer) GetSupportedPortForwardTargets(
	ctx context.Context,
	_ *emptypb.Empty,
) (*networkerpb.GetSupportedPortForwardTargetsResponse, error) {
	resp := s.Impl.GetSupportedPortForwardTargets(types.PluginContextFromContext(ctx))

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
		return nil, err
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
		return nil, err
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
		return nil, err
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
		return nil, err
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
		return nil, err
	}

	return &networkerpb.PortForwardSessionByIdResponse{
		Session: resp.ToProto(),
	}, nil
}
