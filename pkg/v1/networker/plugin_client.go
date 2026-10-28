package networker

import (
	"context"
	"fmt"
	"time"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"google.golang.org/protobuf/types/known/emptypb"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	networkerpb "github.com/omniviewdev/plugin-sdk/proto/v1/networker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type PluginClient struct {
	client networkerpb.NetworkerPluginClient
	log    logging.Logger
}

var _ Provider = (*PluginClient)(nil)

// rpcContext extracts the caller's context for gRPC and returns a shallow copy
// of the PluginContext with its Context field cleared (to avoid redundant storage).
// This preserves the caller's cancellation/deadline on the gRPC call.
func rpcContext(ctx *types.PluginContext) (context.Context, *types.PluginContext) {
	if ctx == nil {
		return context.Background(), &types.PluginContext{}
	}
	grpcCtx := ctx.Context
	if grpcCtx == nil {
		grpcCtx = context.Background()
	}
	cp := *ctx
	cp.Context = nil
	return grpcCtx, &cp
}

// GetSupportedPortForwardTargets returns the list of targets that are supported
// by this plugin for port forwarding.
func (p *PluginClient) GetSupportedPortForwardTargets(ctx *types.PluginContext) ([]string, error) {
	grpcCtx, pctx := rpcContext(ctx)
	resp, err := p.client.GetSupportedPortForwardTargets(
		types.WithPluginContext(grpcCtx, pctx),
		&emptypb.Empty{},
	)
	if err != nil {
		return nil, err
	}

	return resp.GetResources(), nil
}

// GetPortForwardSession returns a port forward session by ID.
func (p *PluginClient) GetPortForwardSession(
	ctx *types.PluginContext,
	sessionID string,
) (*PortForwardSession, error) {
	grpcCtx, pctx := rpcContext(ctx)
	options, admissionError := networkResponseOptions()
	resp, err := p.client.GetPortForwardSession(
		types.WithPluginContext(grpcCtx, pctx),
		&networkerpb.PortForwardSessionByIdRequest{Id: sessionID},
		options...,
	)
	if err != nil {
		return nil, resourceplugin.ResourceDomainError(err)
	}
	if err := admissionError(); err != nil {
		return nil, err
	}
	if resp.GetSession() == nil {
		return nil, fmt.Errorf("server returned nil session for %q", sessionID)
	}

	return NewPortForwardSessionFromProto(resp.GetSession())
}

// ListPortForwardSessions returns all of the port forward sessions.
func (p *PluginClient) ListPortForwardSessions(
	ctx *types.PluginContext,
) ([]*PortForwardSession, error) {
	grpcCtx, pctx := rpcContext(ctx)
	options, admissionError := networkResponseOptions()
	resp, err := p.client.ListPortForwardSessions(
		types.WithPluginContext(grpcCtx, pctx),
		&emptypb.Empty{},
		options...,
	)
	if err != nil {
		return nil, resourceplugin.ResourceDomainError(err)
	}
	if err := admissionError(); err != nil {
		return nil, err
	}

	found := resp.GetSessions()

	sessions := make([]*PortForwardSession, 0, len(found))
	for _, s := range found {
		session, err := NewPortForwardSessionFromProto(s)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, nil
}

// FindPortForwardSessions returns all of the port forward sessions that match the given request.
func (p *PluginClient) FindPortForwardSessions(
	ctx *types.PluginContext,
	req FindPortForwardSessionRequest,
) ([]*PortForwardSession, error) {
	grpcCtx, pctx := rpcContext(ctx)
	options, admissionError := networkResponseOptions()
	resp, err := p.client.FindPortForwardSessions(
		types.WithPluginContext(grpcCtx, pctx),
		req.ToProto(),
		options...,
	)
	if err != nil {
		return nil, resourceplugin.ResourceDomainError(err)
	}
	if err := admissionError(); err != nil {
		return nil, err
	}

	found := resp.GetSessions()
	sessions := make([]*PortForwardSession, 0, len(found))
	for _, s := range found {
		session, err := NewPortForwardSessionFromProto(s)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, nil
}

func (p *PluginClient) StartPortForwardSession(
	ctx *types.PluginContext,
	opts PortForwardSessionOptions,
) (*PortForwardSession, error) {
	if _, ok := resourceConnection(opts.Connection); ok || opts.ConnectionType == PortForwardConnectionTypeResource {
		return p.StartResourcePortForwardSession(ctx, opts)
	}
	wire, err := opts.ToProto()
	if err != nil {
		return nil, err
	}
	grpcCtx, pctx := rpcContext(ctx)
	options, admissionError := networkResponseOptions()
	resp, err := p.client.StartPortForwardSession(
		types.WithPluginContext(grpcCtx, pctx),
		wire,
		options...,
	)
	if err != nil {
		return nil, resourceplugin.ResourceDomainError(err)
	}
	if err := admissionError(); err != nil {
		return nil, err
	}
	if resp.GetSession() == nil {
		return nil, fmt.Errorf("server returned nil session after start")
	}

	return NewPortForwardSessionFromProto(resp.GetSession())
}

func (p *PluginClient) ClosePortForwardSession(
	ctx *types.PluginContext,
	sessionID string,
) (*PortForwardSession, error) {
	grpcCtx, pctx := rpcContext(ctx)
	options, admissionError := networkResponseOptions()
	resp, err := p.client.ClosePortForwardSession(
		types.WithPluginContext(grpcCtx, pctx),
		&networkerpb.PortForwardSessionByIdRequest{Id: sessionID},
		options...,
	)
	if err != nil {
		return nil, resourceplugin.ResourceDomainError(err)
	}
	if err := admissionError(); err != nil {
		return nil, err
	}
	if resp.GetSession() == nil {
		return nil, fmt.Errorf("server returned nil session after close for %q", sessionID)
	}

	return NewPortForwardSessionFromProto(resp.GetSession())
}

// StopAll performs a best-effort shutdown of all remote sessions by listing
// them and closing each individually. Per-session close errors are logged
// but do not stop the loop.
func (p *PluginClient) StopAll() {
	log := p.log
	if log == nil {
		log = logging.NewNop()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Wrap with an empty PluginContext so gRPC interceptors see consistent metadata.
	ctx = types.WithPluginContext(ctx, &types.PluginContext{})

	options, admissionError := networkResponseOptions()
	resp, err := p.client.ListPortForwardSessions(ctx, &emptypb.Empty{}, options...)
	if err == nil {
		err = admissionError()
	}
	if err != nil {
		log.Warnw(ctx, "StopAll: failed to list sessions", "error", err)
		return
	}

	for _, s := range resp.GetSessions() {
		if s.GetId() == "" {
			continue
		}
		options, admissionError := networkResponseOptions()
		if _, closeErr := p.client.ClosePortForwardSession(ctx,
			&networkerpb.PortForwardSessionByIdRequest{Id: s.GetId()},
			options...,
		); closeErr != nil {
			log.Warnw(ctx, "StopAll: failed to close session", "session_id", s.GetId(), "error", closeErr)
		} else if err := admissionError(); err != nil {
			log.Warnw(ctx, "StopAll: failed to admit closed session", "session_id", s.GetId(), "error", err)
		}
	}
}

func (p *PluginClient) StartResourcePortForwardSession(ctx *types.PluginContext, opts PortForwardSessionOptions) (*PortForwardSession, error) {
	if err := validateResourceContext(ctx, opts); err != nil {
		return nil, err
	}
	wire, err := opts.ToProto()
	if err != nil {
		return nil, err
	}
	grpcCtx, pctx := rpcContext(ctx)
	grpcCtx, err = resourceplugin.OutgoingSessionContext(grpcCtx)
	if err != nil {
		return nil, err
	}
	options, admissionError := networkResponseOptions()
	response, err := p.client.StartResourcePortForwardSession(types.WithPluginContext(grpcCtx, pctx), wire, options...)
	if err != nil {
		decoded := resourceplugin.ResourceDomainError(err)
		if status.Code(err) == codes.Unimplemented && resourcev2.ErrorCode(decoded) == "INTERNAL" {
			return nil, resourceUnsupported()
		}
		return nil, decoded
	}
	if err := admissionError(); err != nil {
		return nil, err
	}
	if response.GetResourceWireVersion() != 2 {
		return nil, resourcev2.NewError("UNSUPPORTED", "Resource wire version 2 is required.")
	}
	if response.GetSession() == nil || response.GetSession().GetResourceConnection() == nil {
		return nil, resourceInvalidResponse()
	}
	return NewPortForwardSessionFromProto(response.GetSession())
}

func networkResponseOptions() ([]grpc.CallOption, func() error) {
	admission, admissionError := resourceplugin.ResponseAdmission()
	return []grpc.CallOption{grpc.MaxCallSendMsgSize(resourceplugin.MaxWireBytes), grpc.MaxCallRecvMsgSize(resourceplugin.MaxWireBytes), admission}, admissionError
}
