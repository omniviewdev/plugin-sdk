package exec

import (
	"context"
	"errors"
	"io"
	"time"

	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// runCallBound is how long a Run or a RunStream whose context has no deadline may take: the run's own timeout, and after it
// the SDK's call timeout, which is the time the plugin has to end a run that outlasted its timeout (it ends one within 100 ms,
// and a function that ignores its context is given up on) and to deliver the end. A request that leaves timeout_ms at zero
// gets the target's default timeout, which is at most an hour (the longest timeout any target may allow), so an hour is what
// the client waits for it.
func (c *clientV2) runCallBound(req *execv2.RunRequest) time.Duration {
	timeout := maxRunTimeoutCap
	if ms := req.GetTimeoutMs(); ms != 0 {
		timeout = time.Duration(ms) * time.Millisecond
	}
	return timeout + c.callTimeout
}

// runContext returns the context of a run's call, which its end releases: ctx as the caller gave it when it has a deadline (the
// caller's own is never replaced, shorter or longer), and otherwise ctx with the bound of the run.
func (c *clientV2) runContext(ctx context.Context, req *execv2.RunRequest) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, c.runCallBound(req))
}

// Run runs one bounded command: see Client.Run. The call is made with the caller's plugin context (the connection and the
// catalog pin travel as metadata, as for Options), and a result of any outcome is returned as it came: the outcome is the
// plugin's word on how the command ended, and only a status is an error.
func (c *clientV2) Run(ctx context.Context, req *execv2.RunRequest) (*execv2.RunResult, error) {
	if req == nil {
		return nil, &Error{Class: ErrorClassInvalidRequest, Err: errors.New("run without a request")}
	}
	ctx, err := resourceplugin.OutgoingSessionContext(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := c.runContext(ctx, req)
	defer cancel()
	result, err := c.rpc.Run(ctx, req)
	if err != nil {
		return nil, errorFromStatus(ctx, err)
	}
	if result == nil {
		return nil, c.badResponse(ctx, "Run", errors.New("the plugin answered no result"))
	}
	return result, nil
}

// RunStream runs one bounded command with its output as it happens: see Client.RunStream. The stream is made with the
// caller's plugin context, as Open's is. It returns when the first event, or the status of a refusal, has arrived: the stream
// of a run that was taken is bounded by the run's own timeout, so a caller that set no deadline gets the bound of Run for the
// whole stream, and not the call timeout of Open, which would end a silent command whose first event is its end.
func (c *clientV2) RunStream(ctx context.Context, req *execv2.RunRequest) (RunConn, error) {
	if req == nil {
		return nil, &Error{Class: ErrorClassInvalidRequest, Err: errors.New("run stream without a request")}
	}
	ctx, err := resourceplugin.OutgoingSessionContext(ctx)
	if err != nil {
		return nil, err
	}
	streamCtx, cancel := c.runContext(ctx, req)
	conn, err := c.openRunStream(streamCtx, cancel, req)
	if err != nil {
		cancel()
		return nil, err
	}
	return conn, nil
}

func (c *clientV2) openRunStream(ctx context.Context, cancel context.CancelFunc, req *execv2.RunRequest) (RunConn, error) {
	stream, err := c.rpc.RunStream(ctx, req)
	if err != nil {
		return nil, errorFromStatus(ctx, err)
	}
	first, err := stream.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, c.badResponse(ctx, "RunStream", errors.New("the stream ended before its first event"))
		}
		return nil, errorFromStatus(ctx, err)
	}
	return &runConn{c: c, ctx: ctx, cancel: cancel, stream: stream, first: first}, nil
}

// runConn is the RunConn of a RunStream. Recv is called from one goroutine at a time, so what it keeps (first, ended, over)
// needs no lock; Close only cancels the stream.
type runConn struct {
	c      *clientV2
	ctx    context.Context
	cancel context.CancelFunc
	stream execv2.Exec_RunStreamClient

	// first is the event RunStream read to know the run was taken; Recv returns it first.
	first *execv2.RunEvent
	// ended is true once RunEnded was returned: only then may the stream end with status OK.
	ended bool
	// over is what Recv returns from the moment the stream is over, io.EOF or the error that ended it.
	over error
}

var _ RunConn = (*runConn)(nil)

func (r *runConn) Recv() (*execv2.RunEvent, error) {
	if r.over != nil {
		return nil, r.over
	}
	event := r.first
	r.first = nil
	if event == nil {
		var err error
		if event, err = r.stream.Recv(); err != nil {
			return nil, r.finish(r.streamError(err))
		}
	}
	if event.GetEnded() != nil {
		r.ended = true
	}
	return event, nil
}

// streamError types what ended the stream. A stream that ended with status OK ends the run only after RunEnded: one that
// did not is the plugin's breach of the contract, an error, because the host would otherwise read the end of a run it never
// saw as one that ended well.
func (r *runConn) streamError(err error) error {
	if !errors.Is(err, io.EOF) {
		return errorFromStatus(r.ctx, err)
	}
	if r.ended {
		return io.EOF
	}
	return r.c.badResponse(r.ctx, "RunStream", errors.New("the stream ended with status OK before RunEnded"))
}

// finish records how the stream ended and releases it.
func (r *runConn) finish(err error) error {
	r.over = err
	r.cancel()
	return err
}

func (r *runConn) Close() error {
	r.cancel()
	return nil
}
