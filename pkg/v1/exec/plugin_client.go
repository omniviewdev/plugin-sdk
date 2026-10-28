package exec

import (
	"context"
	"errors"
	"fmt"
	"io"

	logging "github.com/omniviewdev/plugin-sdk/log"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	execpb "github.com/omniviewdev/plugin-sdk/proto/v1/exec"
)

type PluginClient struct {
	client execpb.ExecPluginClient
	log    logging.Logger
}

var _ Provider = (*PluginClient)(nil)

func (c *PluginClient) GetSupportedResources(ctx *types.PluginContext) []Handler {
	resp, err := c.client.GetSupportedResources(
		types.WithPluginContext(context.Background(), ctx),
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

func (c *PluginClient) GetSession(
	ctx *types.PluginContext,
	sessionID string,
) (*Session, error) {
	resp, err := c.client.GetSession(
		types.WithPluginContext(context.Background(), ctx),
		&execpb.GetSessionRequest{Id: sessionID},
	)
	if err != nil {
		return nil, err
	}
	return NewSessionFromProto(resp.GetSession()), nil
}

func (c *PluginClient) ListSessions(ctx *types.PluginContext) ([]*Session, error) {
	resp, err := c.client.ListSessions(
		types.WithPluginContext(context.Background(), ctx),
		&emptypb.Empty{},
	)
	if err != nil {
		return nil, err
	}
	protos := resp.GetSessions()
	sessions := make([]*Session, 0, len(protos))
	for _, s := range protos {
		sessions = append(sessions, NewSessionFromProto(s))
	}
	return sessions, nil
}

func (c *PluginClient) CreateSession(
	ctx *types.PluginContext,
	opts SessionOptions,
) (*Session, error) {
	if opts.HasResourceIntent() {
		return c.CreateResourceSession(ctx, opts)
	}
	proto, err := opts.ToProto()
	if err != nil {
		return nil, fmt.Errorf("failed to convert session options: %w", err)
	}
	resp, err := c.client.CreateSession(
		types.WithPluginContext(context.Background(), ctx),
		proto,
	)
	if err != nil {
		return nil, err
	}
	return NewSessionFromProto(resp.GetSession()), nil
}

func (c *PluginClient) CreateResourceSession(ctx *types.PluginContext, opts SessionOptions) (*Session, error) {
	if err := ValidateResourceSessionOptions(opts); err != nil {
		return nil, err
	}
	request, err := opts.ToProto()
	if err != nil {
		return nil, err
	}
	base := context.Background()
	if ctx != nil && ctx.Context != nil {
		base = ctx.Context
	}
	rpcContext, err := resourceplugin.OutgoingSessionContext(base)
	if err != nil {
		return nil, err
	}
	admission, admissionError := resourceplugin.ResponseAdmission()
	response, err := c.client.CreateResourceSession(types.WithPluginContext(rpcContext, ctx), request,
		grpc.MaxCallSendMsgSize(resourceplugin.MaxWireBytes), grpc.MaxCallRecvMsgSize(resourceplugin.MaxWireBytes), admission)
	if err != nil {
		decoded := resourceplugin.ResourceDomainError(err)
		if status.Code(err) == codes.Unimplemented && resourcev2.ErrorCode(decoded) == "INTERNAL" {
			return nil, resourcev2.NewError("UNSUPPORTED", "resource exec RPC unavailable")
		}
		return nil, decoded
	}
	if err := admissionError(); err != nil {
		return nil, err
	}
	if response == nil || response.ResourceWireVersion != ResourceSessionWireVersion || response.Session == nil {
		return nil, resourcev2.NewError("INVALID_REQUEST", "invalid resource exec response")
	}
	if response.Session.CreatedAt == nil || response.Session.CreatedAt.CheckValid() != nil {
		return nil, resourcev2.NewError("INVALID_REQUEST", "Invalid resource session creation timestamp.")
	}
	session := NewSessionFromProto(response.Session)
	if err := validateResourceSessionResult(session, opts.ID); err != nil {
		return nil, err
	}
	return session, nil
}

func (c *PluginClient) AttachSession(
	ctx *types.PluginContext,
	sessionID string,
) (*Session, []byte, error) {
	resp, err := c.client.AttachSession(
		types.WithPluginContext(context.Background(), ctx),
		&execpb.AttachSessionRequest{Id: sessionID},
	)
	if err != nil {
		return nil, nil, err
	}
	return NewSessionFromProto(resp.GetSession()), resp.GetBuffer(), nil
}

func (c *PluginClient) DetachSession(
	ctx *types.PluginContext,
	sessionID string,
) (*Session, error) {
	resp, err := c.client.DetachSession(
		types.WithPluginContext(context.Background(), ctx),
		&execpb.AttachSessionRequest{Id: sessionID},
	)
	if err != nil {
		return nil, err
	}
	return NewSessionFromProto(resp.GetSession()), nil
}

func (c *PluginClient) CloseSession(ctx *types.PluginContext, sessionID string) error {
	_, err := c.client.CloseSession(
		types.WithPluginContext(context.Background(), ctx),
		&execpb.CloseSessionRequest{Id: sessionID},
	)
	return err
}

func (c *PluginClient) ResizeSession(
	ctx *types.PluginContext,
	sessionID string,
	rows, cols int32,
) error {
	_, err := c.client.ResizeSession(
		types.WithPluginContext(context.Background(), ctx),
		&execpb.ResizeSessionRequest{Id: sessionID, Cols: cols, Rows: rows},
	)
	return err
}

// Close is a no-op on the client side — the server manages session lifecycle.
func (c *PluginClient) Close() {}

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
				c.log.Error(ctx, "failed to close send stream", logging.Error(err))
			}
		}()
		for i := range in {
			if err := stream.Send(i.ToProto()); err != nil {
				c.log.Error(ctx, "failed to send stream input", logging.Error(err))
				return
			}
		}
	}()

	// receiver — owns close(out) so sender never writes to a closed channel
	go func() {
		defer close(out)
		for {
			var resp *execpb.StreamOutput
			resp, err = stream.Recv()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				c.log.Error(ctx, "failed to receive stream output", logging.Error(err))
				return
			}
			out <- NewStreamOutputFromProto(resp)
		}
	}()
	return out, nil
}
