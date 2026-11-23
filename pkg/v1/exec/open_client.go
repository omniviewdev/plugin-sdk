package exec

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// Open starts a session: see Client.Open. The stream is made with the caller's plugin context (the connection and the
// catalog pin travel as metadata, as for Options), the first frame is the request, and the answer is Opened or a status.
func (c *clientV2) Open(ctx context.Context, req *execv2.OpenRequest) (Conn, error) {
	if req == nil {
		return nil, &Error{Class: ErrorClassInvalidRequest, Err: errors.New("open without a request")}
	}
	ctx, err := resourceplugin.OutgoingSessionContext(ctx)
	if err != nil {
		return nil, err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	// The stream outlives this call, so the SDK's call timeout bounds the wait for Opened and nothing after it: it is
	// applied only to a caller that set no deadline of its own.
	var giveUp *time.Timer
	if _, ok := ctx.Deadline(); !ok {
		giveUp = time.AfterFunc(c.callTimeout, cancel)
	}
	conn, err := c.openStream(streamCtx, cancel, req)
	if giveUp != nil {
		giveUp.Stop()
	}
	if err != nil {
		cancel()
		return nil, err
	}
	return conn, nil
}

func (c *clientV2) openStream(ctx context.Context, cancel context.CancelFunc, req *execv2.OpenRequest) (Conn, error) {
	stream, err := c.rpc.Open(ctx)
	if err != nil {
		return nil, errorFromStatus(ctx, err)
	}
	if err := stream.Send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Open{Open: req}}); err != nil {
		// A stream that refused the first frame has ended: its status is the answer.
		if _, recvErr := stream.Recv(); recvErr != nil && !errors.Is(recvErr, io.EOF) {
			return nil, errorFromStatus(ctx, recvErr)
		}
		return nil, errorFromStatus(ctx, err)
	}
	first, err := stream.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, c.badResponse(ctx, "Open", errors.New("the stream ended before Opened"))
		}
		return nil, errorFromStatus(ctx, err)
	}
	opened := first.GetOpened()
	if opened == nil || opened.GetFacts() == nil {
		return nil, c.badResponse(ctx, "Open", errors.New("the first frame of the stream is not Opened with facts"))
	}
	return &openConn{ctx: ctx, cancel: cancel, stream: stream, opened: opened}, nil
}

// openConn is the Conn of an Open stream.
type openConn struct {
	ctx    context.Context
	cancel context.CancelFunc
	stream execv2.Exec_OpenClient
	opened *execv2.Opened

	// sendMu orders the frames of concurrent senders; the stream allows one sender at a time.
	sendMu sync.Mutex
}

var _ Conn = (*openConn)(nil)

func (o *openConn) Opened() *execv2.Opened { return o.opened }

func (o *openConn) Recv() (*execv2.ServerFrame, error) {
	frame, err := o.stream.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		return nil, errorFromStatus(o.ctx, err)
	}
	return frame, nil
}

func (o *openConn) Send(frame *execv2.ClientFrame) error {
	o.sendMu.Lock()
	defer o.sendMu.Unlock()
	err := o.stream.Send(frame)
	if err != nil && !errors.Is(err, io.EOF) {
		return errorFromStatus(o.ctx, err)
	}
	return err
}

func (o *openConn) Close() error {
	o.cancel()
	return nil
}
