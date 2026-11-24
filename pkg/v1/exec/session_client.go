package exec

import (
	"context"
	"errors"

	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The calls of the host that manage sessions without a stream (FEATURE_SESSION_MANAGEMENT): they are unary, they are made with
// the caller's plugin context as Options is (the catalog pin travels as metadata), and each answer is the plugin's own message.

// ListSessions lists the sessions of the plugin, live and ended: see Client.ListSessions.
func (c *clientV2) ListSessions(ctx context.Context) (*execv2.ListSessionsResponse, error) {
	ctx, cancel := c.withDefaultDeadline(ctx)
	defer cancel()
	ctx, err := resourceplugin.OutgoingSessionContext(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := c.rpc.ListSessions(ctx, &execv2.ListSessionsRequest{})
	if err != nil {
		return nil, errorFromStatus(ctx, err)
	}
	if resp == nil {
		return nil, c.badResponse(ctx, "ListSessions", errors.New("the plugin answered no list"))
	}
	return resp, nil
}

// ReadOutput is a cursor read of a session's replay ring: see Client.ReadOutput.
func (c *clientV2) ReadOutput(ctx context.Context, req *execv2.ReadOutputRequest) (*execv2.ReadOutputResponse, error) {
	if req == nil {
		return nil, &Error{Class: ErrorClassInvalidRequest, Err: errors.New("read output without a request")}
	}
	ctx, cancel := c.withDefaultDeadline(ctx)
	defer cancel()
	ctx, err := resourceplugin.OutgoingSessionContext(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := c.rpc.ReadOutput(ctx, req)
	if err != nil {
		return nil, errorFromStatus(ctx, err)
	}
	if resp == nil {
		return nil, c.badResponse(ctx, "ReadOutput", errors.New("the plugin answered no read"))
	}
	return resp, nil
}

// EndSession destroys a session that has no attached stream: see Client.EndSession.
func (c *clientV2) EndSession(ctx context.Context, req *execv2.EndSessionRequest) error {
	if req == nil {
		return &Error{Class: ErrorClassInvalidRequest, Err: errors.New("end session without a request")}
	}
	ctx, cancel := c.withDefaultDeadline(ctx)
	defer cancel()
	ctx, err := resourceplugin.OutgoingSessionContext(ctx)
	if err != nil {
		return err
	}
	if _, err := c.rpc.EndSession(ctx, req); err != nil {
		return errorFromStatus(ctx, err)
	}
	return nil
}
