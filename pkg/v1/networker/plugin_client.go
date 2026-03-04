package networker

import (
	"context"
	"time"

	"github.com/hashicorp/go-hclog"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	networkerpb "github.com/omniviewdev/plugin-sdk/proto/v1/networker"
)

type PluginClient struct {
	client networkerpb.NetworkerPluginClient
	log    hclog.Logger
}

var _ Provider = (*PluginClient)(nil)

// ensurePluginCtx returns a non-nil PluginContext to avoid nil dereference in
// types.WithPluginContext.
func ensurePluginCtx(ctx *types.PluginContext) *types.PluginContext {
	if ctx == nil {
		return &types.PluginContext{}
	}
	return ctx
}

// GetSupportedPortForwardTargets returns the list of targets that are supported
// by this plugin for port forwarding.
func (p *PluginClient) GetSupportedPortForwardTargets(ctx *types.PluginContext) ([]string, error) {
	resp, err := p.client.GetSupportedPortForwardTargets(
		types.WithPluginContext(context.Background(), ensurePluginCtx(ctx)),
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
	resp, err := p.client.GetPortForwardSession(
		types.WithPluginContext(context.Background(), ensurePluginCtx(ctx)),
		&networkerpb.PortForwardSessionByIdRequest{Id: sessionID},
	)
	if err != nil {
		return nil, err
	}

	return NewPortForwardSessionFromProto(resp.GetSession()), nil
}

// ListPortForwardSessions returns all of the port forward sessions.
func (p *PluginClient) ListPortForwardSessions(
	ctx *types.PluginContext,
) ([]*PortForwardSession, error) {
	resp, err := p.client.ListPortForwardSessions(
		types.WithPluginContext(context.Background(), ensurePluginCtx(ctx)),
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
	resp, err := p.client.FindPortForwardSessions(
		types.WithPluginContext(context.Background(), ensurePluginCtx(ctx)),
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
	resp, err := p.client.StartPortForwardSession(
		types.WithPluginContext(context.Background(), ensurePluginCtx(ctx)),
		opts.ToProto(),
	)
	if err != nil {
		return nil, err
	}

	return NewPortForwardSessionFromProto(resp.GetSession()), nil
}

func (p *PluginClient) ClosePortForwardSession(
	ctx *types.PluginContext,
	sessionID string,
) (*PortForwardSession, error) {
	resp, err := p.client.ClosePortForwardSession(
		types.WithPluginContext(context.Background(), ensurePluginCtx(ctx)),
		&networkerpb.PortForwardSessionByIdRequest{Id: sessionID},
	)
	if err != nil {
		return nil, err
	}

	return NewPortForwardSessionFromProto(resp.GetSession()), nil
}

// StopAll performs a best-effort shutdown of all remote sessions by listing
// them and closing each individually. Per-session close errors are logged
// but do not stop the loop.
func (p *PluginClient) StopAll() {
	log := p.log
	if log == nil {
		log = hclog.NewNullLogger()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := p.client.ListPortForwardSessions(ctx, &emptypb.Empty{})
	if err != nil {
		log.Warn("StopAll: failed to list sessions", "error", err)
		return
	}

	for _, s := range resp.GetSessions() {
		if s.GetId() == "" {
			continue
		}
		if _, closeErr := p.client.ClosePortForwardSession(ctx,
			&networkerpb.PortForwardSessionByIdRequest{Id: s.GetId()},
		); closeErr != nil {
			log.Warn("StopAll: failed to close session", "session_id", s.GetId(), "error", closeErr)
		}
	}
}
