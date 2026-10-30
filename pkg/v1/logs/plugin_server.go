package logs

import (
	"context"
	"errors"
	"fmt"
	"io"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	logging "github.com/omniviewdev/plugin-sdk/log"

	"google.golang.org/protobuf/proto"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	logspb "github.com/omniviewdev/plugin-sdk/proto/v1/logs"
)

// PluginServer adapts a log provider to gRPC and preserves resource error classification.
type PluginServer struct {
	log  logging.Logger
	Impl Provider
}

// logsPluginContext binds native calls to the actual RPC lifetime, including
// callers that do not send serialized plugin metadata.
func logsPluginContext(ctx context.Context) *types.PluginContext {
	pc := types.PluginContextFromContext(ctx)
	if pc == nil {
		return &types.PluginContext{Context: ctx}
	}
	bound := *pc
	bound.Context = ctx
	return &bound
}

// GetSupportedResources returns the provider's registered log handlers.
func (s *PluginServer) GetSupportedResources(
	ctx context.Context,
	_ *emptypb.Empty,
) (*logspb.GetSupportedLogResourcesResponse, error) {
	resp := s.Impl.GetSupportedResources(logsPluginContext(ctx))
	handlers := make([]*logspb.LogHandler, 0, len(resp))
	for _, h := range resp {
		handlers = append(handlers, h.ToProto())
	}
	return &logspb.GetSupportedLogResourcesResponse{
		Handlers: handlers,
	}, nil
}

// CreateSession serves legacy requests and rejects resource intent without fallback.
func (s *PluginServer) CreateSession(
	ctx context.Context,
	in *logspb.CreateLogSessionRequest,
) (*logspb.CreateLogSessionResponse, error) {
	if in == nil {
		return nil, status.Errorf(codes.InvalidArgument, "request is nil")
	}

	opts, err := CreateSessionOptionsFromProto(in)
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	if opts.HasResourceIntent() {
		return nil, resourceplugin.ResourceRPCError(
			resourceError("UNSUPPORTED", "Resource logs require CreateResourceSession."),
		)
	}

	resp, err := s.Impl.CreateSession(logsPluginContext(ctx), opts)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create session: %v", err)
	}

	return &logspb.CreateLogSessionResponse{
		Session: resp.ToProto(),
		Success: true,
	}, nil
}

// CreateResourceSession validates wire version, trusted route, and provider result before returning a session.
func (s *PluginServer) CreateResourceSession(
	ctx context.Context,
	in *logspb.CreateLogSessionRequest,
) (*logspb.ResourceLogSessionResponse, error) {
	ctx, err := resourceplugin.IncomingSessionContext(ctx)
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	if in == nil || in.ResourceWireVersion != ResourceSessionWireVersion {
		return nil, resourceplugin.ResourceRPCError(
			resourceError("UNSUPPORTED", "Unsupported logs resource wire version."),
		)
	}
	opts, err := CreateSessionOptionsFromProto(in)
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	pctx := logsPluginContext(ctx)
	if routeErr := validateRoute(pctx, opts); routeErr != nil {
		return nil, resourceplugin.ResourceRPCError(routeErr)
	}
	provider, ok := s.Impl.(ResourceSessionProvider)
	if !ok {
		return nil, resourceplugin.ResourceRPCError(resourceError("UNSUPPORTED", "Resource logs provider unavailable."))
	}
	session, err := provider.CreateResourceSession(pctx, opts)
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	if resultErr := validateResourceSessionResult(session); resultErr != nil {
		if session != nil && session.ID != "" {
			if closeErr := s.Impl.CloseSession(pctx, session.ID); closeErr != nil {
				if s.log != nil {
					s.log.Errorw(ctx, "rejected log session cleanup failed", "error", closeErr)
				}
				return nil, resourceplugin.ResourceRPCError(resourceError("INTERNAL", "Log session cleanup failed."))
			}
		}
		return nil, resourceplugin.ResourceRPCError(resultErr)
	}
	response := &logspb.ResourceLogSessionResponse{
		ResourceWireVersion: ResourceSessionWireVersion,
		Session:             session.ToProto(),
	}
	if proto.Size(response) > resourcev2.MaxMessageBytes {
		if closeErr := s.Impl.CloseSession(pctx, session.ID); closeErr != nil {
			if s.log != nil {
				s.log.Errorw(ctx, "oversized log session cleanup failed", "error", closeErr)
			}
			return nil, resourceplugin.ResourceRPCError(resourceError("INTERNAL", "Log session cleanup failed."))
		}
		return nil, resourceplugin.ResourceRPCError(
			resourceError("RESOURCE_LIMIT", "Logs response exceeds wire limit."),
		)
	}
	return response, nil
}

// GetSession retrieves one session by ID through the provider.
func (s *PluginServer) GetSession(
	ctx context.Context,
	in *logspb.LogSessionByIdRequest,
) (*logspb.LogSessionByIdResponse, error) {
	if in == nil {
		return nil, status.Errorf(codes.InvalidArgument, "request is nil")
	}

	resp, err := s.Impl.GetSession(logsPluginContext(ctx), in.GetSessionId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get session: %v", err)
	}

	return &logspb.LogSessionByIdResponse{
		Session: resp.ToProto(),
		Success: true,
	}, nil
}

// ListSessions returns the provider's current sessions.
func (s *PluginServer) ListSessions(
	ctx context.Context,
	_ *emptypb.Empty,
) (*logspb.ListLogSessionsResponse, error) {
	resp, err := s.Impl.ListSessions(logsPluginContext(ctx))
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

// CloseSession requests provider cleanup for the named session.
func (s *PluginServer) CloseSession(
	ctx context.Context,
	in *logspb.LogSessionByIdRequest,
) (*logspb.CloseLogSessionResponse, error) {
	if in == nil {
		return nil, status.Errorf(codes.InvalidArgument, "request is nil")
	}

	if err := s.Impl.CloseSession(logsPluginContext(ctx), in.GetSessionId()); err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}

	return &logspb.CloseLogSessionResponse{
		Success: true,
	}, nil
}

// UpdateSessionOptions applies streaming options to an existing session.
func (s *PluginServer) UpdateSessionOptions(
	ctx context.Context,
	in *logspb.UpdateLogSessionOptionsRequest,
) (*logspb.LogSessionByIdResponse, error) {
	if in == nil {
		return nil, status.Errorf(codes.InvalidArgument, "request is nil")
	}

	resp, err := s.Impl.UpdateSessionOptions(
		logsPluginContext(ctx),
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

// Stream forwards bidirectional log traffic until the RPC ends or receiving fails.
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
			return fmt.Errorf("failed to receive log stream: %w", err)
		}
		select {
		case multiplexer <- StreamInputFromProto(in):
		case <-ctx.Done():
			return ctx.Err()
		}
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
		case output, ok := <-out:
			if !ok {
				return
			}
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
				s.log.Warnw(ctx, "failed to send LogStreamOutput", "session_id", output.SessionID, "error", err)
				return
			}
		}
	}
}
