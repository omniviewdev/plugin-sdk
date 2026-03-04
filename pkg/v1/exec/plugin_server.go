package exec

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/hashicorp/go-hclog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	execpb "github.com/omniviewdev/plugin-sdk/proto/v1/exec"
)

type PluginServer struct {
	execpb.UnimplementedExecPluginServer
	log  hclog.Logger
	Impl Provider
}

func (s *PluginServer) GetSupportedResources(
	ctx context.Context,
	_ *emptypb.Empty,
) (*execpb.GetSupportedResourcesResponse, error) {
	resp := s.Impl.GetSupportedResources(types.PluginContextFromContext(ctx))
	handlers := make([]*execpb.ExecHandler, 0, len(resp))
	for _, h := range resp {
		handlers = append(handlers, h.ToProto())
	}
	return &execpb.GetSupportedResourcesResponse{
		Handlers: handlers,
	}, nil
}

func (s *PluginServer) GetSession(
	ctx context.Context,
	in *execpb.GetSessionRequest,
) (*execpb.GetSessionResponse, error) {
	if in == nil {
		return nil, status.Errorf(codes.InvalidArgument, "request is nil")
	}
	resp, err := s.Impl.GetSession(types.PluginContextFromContext(ctx), in.GetId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get session: %v", err)
	}

	return &execpb.GetSessionResponse{
		Session: resp.ToProto(),
		Success: true,
	}, nil
}

func (s *PluginServer) ListSessions(
	ctx context.Context,
	in *emptypb.Empty,
) (*execpb.ListSessionsResponse, error) {
	if in == nil {
		return nil, status.Errorf(codes.InvalidArgument, "request is nil")
	}
	resp, err := s.Impl.ListSessions(types.PluginContextFromContext(ctx))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list sessions: %v", err)
	}

	sessions := make([]*execpb.Session, 0, len(resp))
	for _, session := range resp {
		sessions = append(sessions, session.ToProto())
	}

	return &execpb.ListSessionsResponse{
		Sessions: sessions,
		Success:  true,
	}, nil
}

func (s *PluginServer) CreateSession(
	ctx context.Context,
	in *execpb.SessionOptions,
) (*execpb.CreateSessionResponse, error) {
	if in == nil {
		return nil, status.Errorf(codes.InvalidArgument, "request is nil")
	}
	resp, err := s.Impl.CreateSession(
		types.PluginContextFromContext(ctx),
		*NewSessionOptionsFromProto(in),
	)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create session: %v", err)
	}

	return &execpb.CreateSessionResponse{
		Session: resp.ToProto(),
		Success: true,
	}, nil
}

func (s *PluginServer) AttachSession(
	ctx context.Context,
	in *execpb.AttachSessionRequest,
) (*execpb.AttachSessionResponse, error) {
	if in == nil {
		return nil, status.Errorf(codes.InvalidArgument, "request is nil")
	}
	resp, buffer, err := s.Impl.AttachSession(types.PluginContextFromContext(ctx), in.GetId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to attach session: %v", err)
	}

	return &execpb.AttachSessionResponse{
		Session: resp.ToProto(),
		Buffer:  buffer,
	}, nil
}

func (s *PluginServer) DetachSession(
	ctx context.Context,
	in *execpb.AttachSessionRequest,
) (*execpb.AttachSessionResponse, error) {
	if in == nil {
		return nil, status.Errorf(codes.InvalidArgument, "request is nil")
	}
	resp, err := s.Impl.DetachSession(types.PluginContextFromContext(ctx), in.GetId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to detach session: %v", err)
	}

	return &execpb.AttachSessionResponse{
		Session: resp.ToProto(),
	}, nil
}

func (s *PluginServer) CloseSession(
	ctx context.Context,
	in *execpb.CloseSessionRequest,
) (*execpb.CloseSessionResponse, error) {
	if in == nil {
		return nil, status.Errorf(codes.InvalidArgument, "request is nil")
	}
	if err := s.Impl.CloseSession(types.PluginContextFromContext(ctx), in.GetId()); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to close session: %v", err)
	}
	return &execpb.CloseSessionResponse{
		Success: true,
	}, nil
}

func (s *PluginServer) ResizeSession(
	ctx context.Context,
	in *execpb.ResizeSessionRequest,
) (*execpb.ResizeSessionResponse, error) {
	if in == nil {
		return nil, status.Errorf(codes.InvalidArgument, "request is nil")
	}
	if err := s.Impl.ResizeSession(
		types.PluginContextFromContext(ctx),
		in.GetId(),
		in.GetCols(),
		in.GetRows(),
	); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to resize session: %v", err)
	}

	return &execpb.ResizeSessionResponse{
		Success: true,
	}, nil
}

func (s *PluginServer) Stream(stream execpb.ExecPlugin_StreamServer) error {
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()

	multiplexer := make(chan StreamInput)
	defer close(multiplexer)
	out, err := s.Impl.Stream(ctx, multiplexer)
	if err != nil {
		return err
	}

	// handle the output
	go s.handleOut(ctx, out, stream)

	// handle the input
	for {
		var in *execpb.StreamInput
		in, err = stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("failed to receive stream: %w", err)
		}
		multiplexer <- NewStreamInputFromProto(in)
	}
}

func (s *PluginServer) handleOut(
	ctx context.Context,
	out <-chan StreamOutput,
	stream execpb.ExecPlugin_StreamServer,
) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-out:
			if !ok {
				return
			}
			if err := stream.Send(msg.ToProto()); err != nil {
				return
			}
		}
	}
}
