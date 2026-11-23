package exec

import (
	"context"
	"errors"
	"io"
	"math"
	"sync"
	"time"

	"google.golang.org/grpc/status"

	"github.com/omniviewdev/plugin-sdk/internal/session"
	logging "github.com/omniviewdev/plugin-sdk/log"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The interactive session on the wire: one stream per session (Exec.Open). The engine owns the command; this file owns
// the stream. What a host sees, in order:
//
//	Opened, [Notice], [Started], Output / Dropped / Link ..., Ended
//
// and the stream then ends with status OK. A request that is refused is a status with no frame at all.
//
// Three goroutines serve a stream, besides the engine's own:
//
//   - the handler's goroutine sends the frames that are events (Started, Link) and the last one (Ended);
//   - the receiver reads the host's frames in order and applies each before it reads the next, so that a host which sends
//     faster than the command takes input waits in the transport (the stream's window closes): that is the backpressure;
//   - the pump takes the command's output as it is queued, up to 64 KiB of one stream at a time, and sends it. No timer
//     batches it: while a frame is being sent the queue grows, so an idle echo costs one byte a frame and a flood costs
//     64 KiB a frame.
//
// A stream allows one sender at a time, so the pump and the handler send under sendMu. That lock is held across a send that
// the host's window may block, and nothing else is held across it: the command's own locks are never taken inside it.
//
// This build keeps no session past its stream (there is no lease and no replay ring yet): a stream that ends before the
// session did ends the session, with the cause "cancelled".
const (
	// maxOutputFrame is the largest Output frame.
	maxOutputFrame = 64 << 10
	// minOutputBuffer is the buffer a stream's pump starts with: it doubles, up to a frame, while the queue fills it.
	minOutputBuffer = 4 << 10
	// defaultOpenFirstFrameTimeout is how long a stream may take to send its open frame: a stream that sends nothing holds a
	// goroutine of the plugin and must not do so for ever.
	defaultOpenFirstFrameTimeout = 10 * time.Second
)

// Frames of the host that this build does not serve. They are ignored and counted, not refused: a frame is lenient.
const (
	ignoredUnknown    = "unknown"
	ignoredCloseInput = "close_input"
	ignoredSignal     = "signal"
	ignoredQuery      = "query"
	ignoredDetach     = "detach"
)

// Open serves one interactive session on one stream.
func (s *serverV2) Open(stream execv2.Exec_OpenServer) error {
	ctx := stream.Context()
	first, err := s.receiveFirst(ctx, stream)
	if err != nil {
		return err
	}
	if refusal := rejectUnknownFields(first); refusal != nil {
		return s.refuse(ctx, "Open", refusal)
	}
	wire := first.GetOpen()
	if wire == nil {
		return s.refuse(ctx, "Open", invalidRequest("the first frame of an Open stream is an open frame"))
	}
	open, target, refusal := admitOpenRequest(wire, s.reg, newFeatureSet(servedFeatures()))
	if refusal != nil {
		return s.refuse(ctx, "Open", refusal)
	}
	ctx, err = resourceplugin.IncomingSessionContext(ctx)
	if err != nil {
		return resourceplugin.ResourceRPCError(err)
	}
	cmd, err := s.startSession(ctx, open, target)
	if err != nil {
		return s.startRefusal(ctx, "Open", err)
	}
	o := &openStream{
		s: s, stream: stream, cmd: cmd, req: open,
		log:      cmd.log,
		facts:    cmd.openedFacts(),
		maxInput: target.info.Session.MaxInputFrame,
		tty:      open.Start.TTY,
		outDone:  make(chan struct{}),
		failC:    make(chan error, 1),
	}
	return o.run(ctx)
}

// receiveFirst reads the open frame. The host has openFirstFrameTimeout to send it.
func (s *serverV2) receiveFirst(ctx context.Context, stream execv2.Exec_OpenServer) (*execv2.ClientFrame, error) {
	type received struct {
		frame *execv2.ClientFrame
		err   error
	}
	got := make(chan received, 1)
	go func() {
		frame, err := stream.Recv()
		got <- received{frame, err}
	}()
	timer := time.NewTimer(s.openFirstFrameTimeout)
	defer timer.Stop()
	select {
	case r := <-got:
		switch {
		case errors.Is(r.err, io.EOF):
			return nil, s.refuse(ctx, "Open", invalidRequest("the stream ended before its open frame"))
		case r.err != nil:
			return nil, r.err
		}
		return r.frame, nil
	case <-timer.C:
		return nil, s.refuse(ctx, "Open", &Error{Class: ErrorClassTimeout, Err: errors.New("no open frame arrived in time")})
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	}
}

// startSession hands an admitted open request to the engine. ctx carries the caller's connection and catalog pin.
func (s *serverV2) startSession(ctx context.Context, open openRequest, target *registeredTarget) (*command, error) {
	start := open.Start
	return s.engine.start(ctx, startParams{
		ID:          open.SessionID,
		Kind:        kindSession,
		Target:      target,
		Route:       start.Target.Route,
		Request:     start.request(target),
		Stdin:       true,
		ReplayBytes: start.ReplayBytes,
	})
}

// startRefusal turns what the engine refused with into a status: an *Error of the exec lane keeps its class, and the
// resource lane's own errors (the connection lease, the catalog pin) keep the classification the host already knows.
func (s *serverV2) startRefusal(ctx context.Context, method string, err error) error {
	var typed *Error
	if errors.As(err, &typed) {
		return s.refuse(ctx, method, typed)
	}
	return resourceplugin.ResourceRPCError(err)
}

// openStream is one served stream.
type openStream struct {
	s      *serverV2
	stream execv2.Exec_OpenServer
	cmd    *command
	req    openRequest
	log    logging.Logger
	// facts are what Opened carried; Started refines them.
	facts    *execv2.SessionFacts
	maxInput int
	tty      bool
	// ctx ends with the stream, and when run returns.
	ctx context.Context

	// sendMu is held for every send. startedSent and linkSent are guarded by it.
	sendMu      sync.Mutex
	startedSent bool
	linkSent    LinkState

	// outDone is closed when the pump has stopped: the command's output was read to its end (pumpErr is nil), or the stream
	// ended under it (pumpErr says how). pumpErr is written before outDone is closed and read after.
	outDone chan struct{}
	pumpErr error
	// failC receives the status that ends the stream when the host broke the protocol.
	failC chan error

	// ignored holds the kinds of host frame already logged as not served; only the receiver touches it.
	ignored map[string]bool
}

// run serves the stream until its last frame. It returns what the stream ends with: nil after Ended.
func (o *openStream) run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// However the stream ends, the session does not outlive it: this is a no-op for a session that has ended.
	defer o.hostGone()
	o.ctx = ctx

	if err := o.sendFrames(openedFrame(o.facts)); err != nil {
		return err
	}
	if o.tty && o.req.Start.Size.IsZero() {
		if err := o.sendFrames(noticeFrame(execv2.NoticeKind_NOTICE_KIND_SIZE_DEFAULTED)); err != nil {
			return err
		}
	}
	go o.receive()
	go o.pump()
	return o.serve()
}

// serve sends the events of the session as they happen and, once its output has been read to the end, its Ended.
func (o *openStream) serve() error {
	startedC, linkC := o.cmd.StartedC(), o.cmd.LinkChanged()
	for {
		select {
		case <-startedC:
			startedC = nil
			if err := o.flushEvents(); err != nil {
				return err
			}
		case <-linkC:
			if err := o.flushEvents(); err != nil {
				return err
			}
		case err := <-o.failC:
			return err
		case <-o.outDone:
			return o.finish()
		case <-o.ctx.Done():
			return status.FromContextError(o.ctx.Err()).Err()
		}
	}
}

// finish sends the end of the session. The pump read the output to its end, so the command has ended; its result is final
// once Done is closed, and its id is free once it is gone, so a host that reads Ended and opens the same id again is not
// refused for the session that has just ended. A host that broke the protocol is told so with the status it earned, not with
// an Ended: the status wins whenever it was decided.
func (o *openStream) finish() error {
	if o.pumpErr != nil {
		return o.pumpErr
	}
	if err := o.failed(); err != nil {
		return err
	}
	select {
	case <-o.cmd.Done():
	case <-o.ctx.Done():
		return status.FromContextError(o.ctx.Err()).Err()
	}
	res := o.cmd.Result()
	select {
	case <-o.cmd.Gone():
	case <-o.ctx.Done():
		return status.FromContextError(o.ctx.Err()).Err()
	}
	if err := o.failed(); err != nil {
		return err
	}
	o.sendMu.Lock()
	defer o.sendMu.Unlock()
	if err := o.sendStartedLocked(); err != nil {
		return err
	}
	if err := o.sendLinkLocked(); err != nil {
		return err
	}
	return o.stream.Send(endedFrame(res, o.tty))
}

// failed returns the status that the host's protocol violation earned the stream, if there is one.
func (o *openStream) failed() error {
	select {
	case err := <-o.failC:
		return err
	default:
		return nil
	}
}

// pump sends the command's output: what the queue holds, up to maxOutputFrame bytes of one stream, with the ranges the
// queue lost before it.
func (o *openStream) pump() {
	defer close(o.outDone)
	// The buffer starts small and grows to a frame's size as the output fills it: a session that is idle, an echo, costs
	// a few KiB, and a flood reaches whole frames after four reads.
	buf := make([]byte, minOutputBuffer)
	for {
		chunk, err := o.cmd.Read(o.ctx, buf)
		if chunk.N > 0 || len(chunk.Dropped) > 0 {
			if sendErr := o.sendOutput(chunk, buf[:chunk.N]); sendErr != nil {
				o.pumpErr = sendErr
				return
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				o.pumpErr = err
			}
			return
		}
		if chunk.N == len(buf) && len(buf) < maxOutputFrame {
			buf = make([]byte, min(2*len(buf), maxOutputFrame)) // the queue had more than the buffer held: take more next time
		}
	}
}

// sendOutput sends one chunk of output. The Started that precedes the first output is sent before it.
func (o *openStream) sendOutput(chunk outputChunk, data []byte) error {
	o.sendMu.Lock()
	defer o.sendMu.Unlock()
	if err := o.sendStartedLocked(); err != nil {
		return err
	}
	if len(chunk.Dropped) > 0 {
		reason := o.cmd.dropReason()
		for _, d := range chunk.Dropped {
			if err := o.stream.Send(droppedFrame(d, reason)); err != nil {
				return err
			}
		}
	}
	if len(data) == 0 {
		return nil
	}
	return o.stream.Send(outputFrame(chunk.Stream, chunk.Offset, data))
}

// flushEvents sends the Started and the link state that the host has not been told.
func (o *openStream) flushEvents() error {
	o.sendMu.Lock()
	defer o.sendMu.Unlock()
	if err := o.sendStartedLocked(); err != nil {
		return err
	}
	return o.sendLinkLocked()
}

// sendFrames sends frames in order, one send at a time.
func (o *openStream) sendFrames(frames ...*execv2.ServerFrame) error {
	o.sendMu.Lock()
	defer o.sendMu.Unlock()
	for _, f := range frames {
		if err := o.stream.Send(f); err != nil {
			return err
		}
	}
	return nil
}

// sendStartedLocked sends the Started once there is one and it has not been sent. The caller holds sendMu.
func (o *openStream) sendStartedLocked() error {
	if o.startedSent {
		return nil
	}
	facts, ok := o.cmd.startedFacts(o.facts)
	if !ok {
		return nil
	}
	if err := o.stream.Send(startedFrame(facts)); err != nil {
		return err
	}
	o.startedSent = true
	return nil
}

// sendLinkLocked sends the link state when it changed since the last one sent. The caller holds sendMu.
func (o *openStream) sendLinkLocked() error {
	state, ok := o.cmd.Link()
	if !ok || state == o.linkSent {
		return nil
	}
	if err := o.stream.Send(linkFrame(state)); err != nil {
		return err
	}
	o.linkSent = state
	return nil
}

// receive applies the host's frames in the order they were sent, until the stream ends.
func (o *openStream) receive() {
	for {
		frame, err := o.stream.Recv()
		if err != nil {
			// The host ended its side, or the stream broke: nobody is left to receive the session.
			o.hostGone()
			return
		}
		if !o.apply(frame) {
			return
		}
	}
}

// apply does what one frame of the host asks. It reports false when the stream is to end.
func (o *openStream) apply(frame *execv2.ClientFrame) bool {
	switch f := frame.GetFrame().(type) {
	case *execv2.ClientFrame_Input:
		if len(f.Input) > o.maxInput {
			o.fail(&Error{Class: ErrorClassLimit, Reason: ErrorReasonInputTooLarge, Err: errors.New("an input frame is larger than the target's maximum")})
			return false
		}
		// It waits while the command's input queue is full, which is the point: the next frame is not read meanwhile. A
		// command that has ended takes no input, and the frame is dropped.
		if _, err := o.cmd.WriteInput(o.ctx, f.Input); err != nil && o.ctx.Err() != nil {
			return false
		}
	case *execv2.ClientFrame_Resize:
		size := f.Resize.GetSize()
		o.cmd.Resize(f.Resize.GetSeq(), Size{Cols: dimension(size.GetCols()), Rows: dimension(size.GetRows())})
	case *execv2.ClientFrame_Close:
		o.cmd.Close(session.CauseRequested, f.Close.GetMode() == execv2.CloseMode_CLOSE_MODE_KILL)
	case *execv2.ClientFrame_Open:
		o.fail(invalidRequest("a second open frame on one stream"))
		return false
	case *execv2.ClientFrame_CloseInput:
		o.ignore(ignoredCloseInput)
	case *execv2.ClientFrame_Signal:
		o.ignore(ignoredSignal)
	case *execv2.ClientFrame_Query:
		o.ignore(ignoredQuery)
	case *execv2.ClientFrame_Detach:
		o.ignore(ignoredDetach)
	default:
		o.ignore(ignoredUnknown)
	}
	return true
}

// dimension is a terminal dimension as the engine takes it; a value the wire can carry and the engine cannot is the
// largest it can, which every limit refuses.
func dimension(v uint32) uint16 {
	if v > math.MaxUint16 {
		return math.MaxUint16
	}
	return uint16(v)
}

// ignore counts a frame of a kind this build does not serve, and says so once for the stream.
func (o *openStream) ignore(kind string) {
	o.s.engine.tel.frameIgnored(kind)
	if o.ignored == nil {
		o.ignored = map[string]bool{}
	}
	if !o.ignored[kind] {
		o.ignored[kind] = true
		o.log.Debugw(context.Background(), "exec stream: a frame this build does not serve was ignored", "frame", kind)
	}
}

// fail ends the stream with the status of e, because the host broke the protocol, and ends the session with it.
func (o *openStream) fail(e *Error) {
	select {
	case o.failC <- o.s.refuse(o.ctx, "Open", e):
	default:
	}
	o.hostGone()
}

// hostGone ends the session of a stream that is over: this build keeps no session past its stream. Graceful, and a no-op
// for a session that has ended.
func (o *openStream) hostGone() { o.cmd.Close(session.CauseCancelled, false) }
