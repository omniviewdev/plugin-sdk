package logs

import (
	"context"
	"errors"
	"io"
	"log"

	"github.com/hashicorp/go-hclog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	logspb "github.com/omniviewdev/plugin-sdk/proto/v1/logs"
)

type PluginServer struct {
	log  hclog.Logger
	Impl Provider
}

func (s *PluginServer) GetSupportedResources(
	ctx context.Context,
	_ *emptypb.Empty,
) (*logspb.GetSupportedLogResourcesResponse, error) {
	resp := s.Impl.GetSupportedResources(types.PluginContextFromContext(ctx))
	handlers := make([]*logspb.LogHandler, 0, len(resp))
	for _, h := range resp {
		handlers = append(handlers, h.ToProto())
	}
	return &logspb.GetSupportedLogResourcesResponse{
		Handlers: handlers,
	}, nil
}

func (s *PluginServer) CreateSession(
	ctx context.Context,
	in *logspb.CreateLogSessionRequest,
) (*logspb.CreateLogSessionResponse, error) {
	if in == nil {
		return nil, status.Errorf(codes.InvalidArgument, "request is nil")
	}

	opts := CreateSessionOptions{
		ResourceKey:  in.GetResourceKey(),
		ResourceID:   in.GetResourceId(),
		ResourceData: in.GetResourceData().AsMap(),
		Options:      LogSessionOptionsFromProto(in.GetOptions()),
	}

	resp, err := s.Impl.CreateSession(types.PluginContextFromContext(ctx), opts)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create session: %v", err)
	}

	return &logspb.CreateLogSessionResponse{
		Session: resp.ToProto(),
		Success: true,
	}, nil
}

func (s *PluginServer) GetSession(
	ctx context.Context,
	in *logspb.LogSessionByIdRequest,
) (*logspb.LogSessionByIdResponse, error) {
	if in == nil {
		return nil, status.Errorf(codes.InvalidArgument, "request is nil")
	}

	resp, err := s.Impl.GetSession(types.PluginContextFromContext(ctx), in.GetSessionId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get session: %v", err)
	}

	return &logspb.LogSessionByIdResponse{
		Session: resp.ToProto(),
		Success: true,
	}, nil
}

func (s *PluginServer) ListSessions(
	ctx context.Context,
	_ *emptypb.Empty,
) (*logspb.ListLogSessionsResponse, error) {
	resp, err := s.Impl.ListSessions(types.PluginContextFromContext(ctx))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list sessions: %v", err)
	}

	sessions := make([]*logspb.LogSession, 0, len(resp))
	for _, session := range resp {
		sessions = append(sessions, session.ToProto())
	}

	return &logspb.ListLogSessionsResponse{
		Sessions: sessions,
		Success:  true,
	}, nil
}

func (s *PluginServer) CloseSession(
	ctx context.Context,
	in *logspb.LogSessionByIdRequest,
) (*logspb.CloseLogSessionResponse, error) {
	if in == nil {
		return nil, status.Errorf(codes.InvalidArgument, "request is nil")
	}

	if err := s.Impl.CloseSession(types.PluginContextFromContext(ctx), in.GetSessionId()); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to close session: %v", err)
	}

	return &logspb.CloseLogSessionResponse{
		Success: true,
	}, nil
}

func (s *PluginServer) UpdateSessionOptions(
	ctx context.Context,
	in *logspb.UpdateLogSessionOptionsRequest,
) (*logspb.LogSessionByIdResponse, error) {
	if in == nil {
		return nil, status.Errorf(codes.InvalidArgument, "request is nil")
	}

	resp, err := s.Impl.UpdateSessionOptions(
		types.PluginContextFromContext(ctx),
		in.GetSessionId(),
		LogSessionOptionsFromProto(in.GetOptions()),
	)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update session options: %v", err)
	}

	return &logspb.LogSessionByIdResponse{
		Session: resp.ToProto(),
		Success: true,
	}, nil
}

func (s *PluginServer) Stream(stream logspb.LogPlugin_StreamServer) error {
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()

	multiplexer := make(chan StreamInput)
	out, err := s.Impl.Stream(ctx, multiplexer)
	if err != nil {
		return err
	}

	// handle the output
	go s.handleOut(ctx, out, stream)

	// handle the input
	for {
		var in *logspb.LogStreamInput
		in, err = stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			log.Printf("failed to receive log stream: %v", err)
			continue
		}
		multiplexer <- StreamInputFromProto(in)
	}
}

func (s *PluginServer) handleOut(
	ctx context.Context,
	out <-chan StreamOutput,
	stream logspb.LogPlugin_StreamServer,
) {
	for {
		select {
		case <-ctx.Done():
			return
		case output := <-out:
			msg := &logspb.LogStreamOutput{
				SessionId: output.SessionID,
			}
			if output.Line != nil {
				msg.Payload = &logspb.LogStreamOutput_Line{
					Line: output.Line.ToProto(),
				}
			} else if output.Event != nil {
				msg.Payload = &logspb.LogStreamOutput_Event{
					Event: output.Event.ToProto(),
				}
			}

			if err := stream.Send(msg); err != nil {
				if ctx.Err() == context.Canceled {
					return
				}
			}
		}
	}
}
