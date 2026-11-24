package exec

import (
	"context"
	"time"

	logging "github.com/omniviewdev/plugin-sdk/log"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// defaultCallTimeout bounds a unary call whose context carries no deadline, so that a wedged plugin cannot hold a
// caller for ever.
const defaultCallTimeout = 30 * time.Second

// clientV2 is the host's gRPC client of the exec v2 service of one plugin.
//
// This build calls Describe and Options (this file), Open (open_client.go), Run and RunStream (run_client.go), and ListSessions,
// ReadOutput and EndSession (session_client.go).
type clientV2 struct {
	rpc execv2.ExecClient
	log logging.Logger
	// callTimeout is the deadline of a unary call whose context has none, the wait of an Open for Opened, and the time a
	// Run or RunStream is allowed beyond its own timeout. It is a field, and not the constant, so that a test can prove it
	// is applied.
	callTimeout time.Duration
}

func newClientV2(rpc execv2.ExecClient, log logging.Logger) *clientV2 {
	if log == nil {
		log = logging.NewNop()
	}
	return &clientV2{rpc: rpc, log: log, callTimeout: defaultCallTimeout}
}

// V2 returns the exec v2 client of the plugin. Its calls on a plugin that does not serve the lane (every plugin
// built before the lane existed) fail with an *Error whose class is ErrorClassUnsupported, and the host stays on the
// legacy lane.
func (c *PluginClient) V2() Client { return c.v2 }

var _ Client = (*clientV2)(nil)

// withDefaultDeadline returns ctx unchanged when it has a deadline, and otherwise ctx with a deadline of the
// client's call timeout. The caller's own deadline is never replaced, shorter or longer.
func (c *clientV2) withDefaultDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.callTimeout)
}

// Describe asks what the plugin can run commands in and what its SDK build serves. The answer is the plugin's
// message itself once it is validated: an answer that breaks the contract is an error and is never partially used.
func (c *clientV2) Describe(ctx context.Context) (*execv2.DescribeResponse, error) {
	ctx, cancel := c.withDefaultDeadline(ctx)
	defer cancel()
	resp, err := c.rpc.Describe(ctx, &execv2.DescribeRequest{})
	if err != nil {
		return nil, errorFromStatus(ctx, err)
	}
	if err := validateDescribeResponse(resp); err != nil {
		return nil, c.badResponse(ctx, "Describe", err)
	}
	return resp, nil
}

// Options asks what a user can choose for one resource. ctx carries the call's plugin context (the caller's
// connection) and the catalog pin, as for every resource utility call; the answer is validated against the
// bounds of the contract.
func (c *clientV2) Options(ctx context.Context, ref TargetRef) (*OptionsResult, error) {
	request, err := optionsRequestToProto(ref)
	if err != nil {
		return nil, err
	}
	ctx, cancel := c.withDefaultDeadline(ctx)
	defer cancel()
	ctx, err = resourceplugin.OutgoingSessionContext(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := c.rpc.Options(ctx, request)
	if err != nil {
		return nil, errorFromStatus(ctx, err)
	}
	options, err := optionsFromProto(resp.GetOptions())
	if err != nil {
		return nil, c.badResponse(ctx, "Options", err)
	}
	result := &OptionsResult{Options: options}
	if ms := resp.GetObservedAtUnixMs(); ms != 0 {
		result.ObservedAt = time.UnixMilli(ms)
	}
	return result, nil
}

// optionsRequestToProto validates and encodes an Options request: the reference and route must be canonical.
func optionsRequestToProto(ref TargetRef) (*execv2.OptionsRequest, error) {
	wire, err := targetRefToProto(ref)
	if err != nil {
		return nil, err
	}
	return &execv2.OptionsRequest{Target: wire}, nil
}

// badResponse reports a plugin that answered outside the contract: its answer is not used, and the cause is
// logged locally.
func (c *clientV2) badResponse(ctx context.Context, method string, cause error) error {
	c.log.Warnw(ctx, "exec v2 plugin answered outside the contract", "method", method, "cause", cause.Error())
	return &Error{Class: ErrorClassInternal, Err: cause}
}
