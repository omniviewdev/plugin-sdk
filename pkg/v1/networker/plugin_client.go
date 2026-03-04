package networker

import (
	"context"
	"fmt"
	"time"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	networkerpb "github.com/omniviewdev/plugin-sdk/proto/v1/networker"
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
	resp, err := p.client.GetPortForwardSession(
		types.WithPluginContext(grpcCtx, pctx),
		&networkerpb.PortForwardSessionByIdRequest{Id: sessionID},
	)
	if err != nil {
		return nil, err
	}
	if resp.GetSession() == nil {
		return nil, fmt.Errorf("server returned nil session for %q", sessionID)
	}

	return NewPortForwardSessionFromProto(resp.GetSession()), nil
}

// ListPortForwardSessions returns all of the port forward sessions.
func (p *PluginClient) ListPortForwardSessions(
	ctx *types.PluginContext,
) ([]*PortForwardSession, error) {
	grpcCtx, pctx := rpcContext(ctx)
	resp, err := p.client.ListPortForwardSessions(
		types.WithPluginContext(grpcCtx, pctx),
		&emptypb.Empty{},
	)
	if err != nil {
		return nil, err
	}

	found := resp.GetSessions()

	sessions := make([]*PortForwardSession, 0, len(found))
	for _, s := range found {
		sessions = append(sessions, NewPortForwardSessionFromProto(s))
	}
	return sessions, nil
}

// FindPortForwardSessions returns all of the port forward sessions that match the given request.
func (p *PluginClient) FindPortForwardSessions(
	ctx *types.PluginContext,
	req FindPortForwardSessionRequest,
) ([]*PortForwardSession, error) {
	grpcCtx, pctx := rpcContext(ctx)
	resp, err := p.client.FindPortForwardSessions(
		types.WithPluginContext(grpcCtx, pctx),
		req.ToProto(),
	)
	if err != nil {
		return nil, err
	}

	found := resp.GetSessions()
	sessions := make([]*PortForwardSession, 0, len(found))
	for _, s := range found {
		sessions = append(sessions, NewPortForwardSessionFromProto(s))
	}
	return sessions, nil
}

func (p *PluginClient) StartPortForwardSession(
	ctx *types.PluginContext,
	opts PortForwardSessionOptions,
) (*PortForwardSession, error) {
	grpcCtx, pctx := rpcContext(ctx)
	resp, err := p.client.StartPortForwardSession(
		types.WithPluginContext(grpcCtx, pctx),
		opts.ToProto(),
	)
	if err != nil {
		return nil, err
	}
	if resp.GetSession() == nil {
		return nil, fmt.Errorf("server returned nil session after start")
	}

	return NewPortForwardSessionFromProto(resp.GetSession()), nil
}

func (p *PluginClient) ClosePortForwardSession(
	ctx *types.PluginContext,
	sessionID string,
) (*PortForwardSession, error) {
	grpcCtx, pctx := rpcContext(ctx)
	resp, err := p.client.ClosePortForwardSession(
		types.WithPluginContext(grpcCtx, pctx),
		&networkerpb.PortForwardSessionByIdRequest{Id: sessionID},
	)
	if err != nil {
		return nil, err
	}
	if resp.GetSession() == nil {
		return nil, fmt.Errorf("server returned nil session after close for %q", sessionID)
	}

	return NewPortForwardSessionFromProto(resp.GetSession()), nil
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

	resp, err := p.client.ListPortForwardSessions(ctx, &emptypb.Empty{})
	if err != nil {
		log.Warnw(ctx, "StopAll: failed to list sessions", "error", err)
		return
	}

	for _, s := range resp.GetSessions() {
		if s.GetId() == "" {
			continue
		}
		if _, closeErr := p.client.ClosePortForwardSession(ctx,
			&networkerpb.PortForwardSessionByIdRequest{Id: s.GetId()},
		); closeErr != nil {
			log.Warnw(ctx, "StopAll: failed to close session", "session_id", s.GetId(), "error", closeErr)
		}
	}
}
