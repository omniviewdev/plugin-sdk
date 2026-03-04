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

// grpcCodeForError maps ExecSessionError codes to gRPC status codes.
// It also preserves context cancellation/deadline and existing gRPC statuses.
func grpcCodeForError(err error) codes.Code {
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
	var sessionErr *ExecSessionError
	if errors.As(err, &sessionErr) {
		switch sessionErr.Code {
		case ErrCodeSessionNotFound:
			return codes.NotFound
		case ErrCodeHandlerNotFound:
			return codes.NotFound
		case ErrCodeSessionClosed:
			return codes.FailedPrecondition
		case ErrCodeTerminalError:
			return codes.Internal
		case ErrCodeSessionExists:
			return codes.AlreadyExists
		}
	}
	return codes.Internal
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
		return nil, status.Errorf(grpcCodeForError(err), "%v", err)
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
		return nil, status.Errorf(grpcCodeForError(err), "%v", err)
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
		return nil, status.Errorf(grpcCodeForError(err), "%v", err)
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
		return nil, status.Errorf(grpcCodeForError(err), "%v", err)
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
		return nil, status.Errorf(grpcCodeForError(err), "%v", err)
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
		return nil, status.Errorf(grpcCodeForError(err), "%v", err)
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
		in.GetRows(),
		in.GetCols(),
	); err != nil {
		return nil, status.Errorf(grpcCodeForError(err), "%v", err)
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

	// handle the output — pass cancel so send errors break the recv loop
	go s.handleOut(ctx, cancel, out, stream)

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
		msg := NewStreamInputFromProto(in)
		select {
		case multiplexer <- msg:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *PluginServer) handleOut(
	ctx context.Context,
	cancel context.CancelFunc,
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
				cancel() // unblock the recv loop
				return
			}
		}
	}
}
