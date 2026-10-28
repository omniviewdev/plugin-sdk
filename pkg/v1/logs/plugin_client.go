package logs

import (
	"context"
	"errors"
	"io"

	logging "github.com/omniviewdev/plugin-sdk/log"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	logspb "github.com/omniviewdev/plugin-sdk/proto/v1/logs"
)

type PluginClient struct {
	client logspb.LogPluginClient
	log    logging.Logger
}

var _ Provider = (*PluginClient)(nil)

func logsClientContext(pc *types.PluginContext) context.Context {
	if pc == nil {
		return context.Background()
	}
	ctx := pc.Context
	if ctx == nil {
		ctx = context.Background()
	}
	copy := *pc
	return types.WithPluginContext(ctx, &copy)
}

func (c *PluginClient) GetSupportedResources(ctx *types.PluginContext) []Handler {
	resp, err := c.client.GetSupportedResources(
		logsClientContext(ctx),
		&emptypb.Empty{},
	)
	if err != nil {
		return nil
	}
	handlers := resp.GetHandlers()

	result := make([]Handler, 0, len(handlers))
	for _, h := range handlers {
		result = append(result, HandlerFromProto(h))
	}
	return result
}

func (c *PluginClient) CreateSession(
	ctx *types.PluginContext,
	opts CreateSessionOptions,
) (*LogSession, error) {
	if opts.HasResourceIntent() {
		return c.CreateResourceSession(ctx, opts)
	}
	request, err := opts.ToProto()
	if err != nil {
		return nil, err
	}
	resp, err := c.client.CreateSession(logsClientContext(ctx), request)
	if err != nil {
		return nil, err
	}
	return LogSessionFromProto(resp.GetSession()), nil
}

func (c *PluginClient) CreateResourceSession(ctx *types.PluginContext, opts CreateSessionOptions) (*LogSession, error) {
	if err := validateRoute(ctx, opts); err != nil {
		return nil, err
	}
	request, err := opts.ToProto()
	if err != nil {
		return nil, err
	}
	request.ResourceWireVersion = ResourceSessionWireVersion
	rpcContext, err := resourceplugin.OutgoingSessionContext(logsClientContext(ctx))
	if err != nil {
		return nil, err
	}
	admission, admissionError := resourceplugin.ResponseAdmission()
	resp, err := c.client.CreateResourceSession(rpcContext, request,
		grpc.MaxCallSendMsgSize(resourceplugin.MaxWireBytes), grpc.MaxCallRecvMsgSize(resourceplugin.MaxWireBytes), admission)
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			return nil, resourceError("UNSUPPORTED", "Resource logs RPC unavailable.")
		}
		return nil, resourceplugin.ResourceDomainError(err)
	}
	if err := admissionError(); err != nil {
		return nil, err
	}
	if resp == nil || resp.ResourceWireVersion != ResourceSessionWireVersion || resp.Session == nil {
		return nil, resourceError("INVALID_REQUEST", "Invalid resource logs response.")
	}
	if resp.Session.CreatedAt == nil || resp.Session.CreatedAt.CheckValid() != nil {
		return nil, resourceError("INVALID_REQUEST", "Invalid logs session timestamp.")
	}
	session := LogSessionFromProto(resp.Session)
	if err := validateResourceSessionResult(session); err != nil {
		return nil, err
	}
	return session, nil
}

func (c *PluginClient) GetSession(
	ctx *types.PluginContext,
	sessionID string,
) (*LogSession, error) {
	resp, err := c.client.GetSession(
		logsClientContext(ctx),
		&logspb.LogSessionByIdRequest{SessionId: sessionID},
	)
	if err != nil {
		return nil, err
	}
	return LogSessionFromProto(resp.GetSession()), nil
}

func (c *PluginClient) ListSessions(ctx *types.PluginContext) ([]*LogSession, error) {
	resp, err := c.client.ListSessions(
		logsClientContext(ctx),
		&emptypb.Empty{},
	)
	if err != nil {
		return nil, err
	}
	protos := resp.GetSessions()
	sessions := make([]*LogSession, 0, len(protos))
	for _, s := range protos {
		sessions = append(sessions, LogSessionFromProto(s))
	}
	return sessions, nil
}

func (c *PluginClient) CloseSession(ctx *types.PluginContext, sessionID string) error {
	_, err := c.client.CloseSession(
		logsClientContext(ctx),
		&logspb.LogSessionByIdRequest{SessionId: sessionID},
	)
	return err
}

func (c *PluginClient) UpdateSessionOptions(
	ctx *types.PluginContext,
	sessionID string,
	opts LogSessionOptions,
) (*LogSession, error) {
	resp, err := c.client.UpdateSessionOptions(
		logsClientContext(ctx),
		&logspb.UpdateLogSessionOptionsRequest{
			SessionId: sessionID,
			Options:   opts.ToProto(),
		},
	)
	if err != nil {
		return nil, err
	}
	return LogSessionFromProto(resp.GetSession()), nil
}

func (c *PluginClient) Stream(
	ctx context.Context,
	in chan StreamInput,
) (chan StreamOutput, error) {
	stream, err := c.client.Stream(ctx)
	if err != nil {
		return nil, err
	}

	out := make(chan StreamOutput)

	// sender
	go func() {
		defer func() {
			if err := stream.CloseSend(); err != nil {
				c.log.Error(ctx, "failed to close send log stream", logging.Error(err))
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case i, ok := <-in:
				if !ok {
					return
				}
				if err := stream.Send(i.ToProto()); err != nil {
					c.log.Error(ctx, "failed to send log stream input", logging.Error(err))
					return
				}
			}
		}
	}()

	// receiver
	go func() {
		defer close(out)
		for {
			resp, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				c.log.Error(ctx, "failed to receive log stream output", logging.Error(err))
				return
			}

			output := StreamOutput{
				SessionID: resp.GetSessionId(),
			}

			switch payload := resp.GetPayload().(type) {
			case *logspb.LogStreamOutput_Line:
				line := LogLineFromProto(payload.Line)
				output.Line = &line
			case *logspb.LogStreamOutput_Event:
				event := LogStreamEventFromProto(payload.Event)
				output.Event = &event
			}

			select {
			case out <- output:
			case <-ctx.Done():
				return
			}
		}
	}()

	return out, nil
}
