package exec

import (
	"context"
	"errors"
	"io"
	"math"
	"sync"
	"time"

	"google.golang.org/grpc/status"

	logging "github.com/omniviewdev/plugin-sdk/log"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The interactive session on the wire: one stream at a time (Exec.Open). The engine owns the command and its output (the replay
// ring); this file owns the stream, which is one attachment of the session. What a host sees, in order:
//
//	Opened, [Notice], [Started], Output / Dropped / Link / Resized / Fact ..., Ended | Detached
//
// and the stream then ends with status OK. A request that is refused is a status with no frame at all. A stream either starts a
// session (the first frame is a Start) or attaches to one that lives (an Attach: the reply describes the replay that follows, which
// is exactly what the ring holds from the offset the host has, and the live stream goes on where it ends).
//
// The last frame of a stream is Ended when the session ended while the stream was attached to it, and Detached when the stream left
// and the session lives on: because the host asked (Detach), or because another stream attached and took the session over, or
// because the host closed its side of the stream. A stream that breaks has no last frame, and leaves the same way. A session has at
// most one Ended, however many streams it had, and the stream that was told Detached is never told Ended.
//
// Three goroutines serve a stream, besides the engine's own:
//
//   - the handler's goroutine sends the frames that are events (Started, Link, and what the host's control frames earn: Resized and
//     Fact, see open_control.go) and the last one (Ended or Detached);
//   - the receiver reads the host's frames in order and applies each before it reads the next, so that a host which sends
//     faster than the command takes input waits in the transport (the stream's window closes): that is the backpressure;
//   - the pump takes the command's output from the stream's cursor in the ring, up to 64 KiB of one stream at a time, and sends
//     it. No timer batches it: while a frame is being sent the ring grows, so an idle echo costs one byte a frame and a flood
//     costs 64 KiB a frame.
//
// A stream allows one sender at a time, so the pump and the handler send under sendMu. That lock is held across a send that
// the host's window may block, and nothing else is held across it: the command's own locks are never taken inside it.
//
// A session does not end with its stream. A stream that is over without having left (the host cancelled, lost its connection or
// closed its side) is an implicit Detach: the session lives for its lease, and its output goes to the ring, where the host finds it
// when it attaches again. What does end a session with its stream is a host that broke the protocol (a second open frame, an input
// frame over the maximum): the stream ends with the status it earned and the session with it. So does a stream that broke before
// the SDK could send Opened, for a session it started: the host was never told that the session exists.
const (
	// maxOutputFrame is the largest Output frame.
	maxOutputFrame = 64 << 10
	// minOutputBuffer is the buffer a stream's pump starts with: it doubles, up to a frame, while the ring fills it.
	minOutputBuffer = 4 << 10
	// defaultOpenFirstFrameTimeout is how long a stream may take to send its open frame: a stream that sends nothing holds a
	// goroutine of the plugin and must not do so for ever.
	defaultOpenFirstFrameTimeout = 10 * time.Second
)

// Frames of the host that this build does not serve. They are ignored and counted, not refused: a frame is lenient. Every frame
// kind of the contract is served (resize, signal, close_input, query, detach, close): only a kind this build does not know is ignored.
const (
	ignoredUnknown = "unknown"
)

// Open serves one interactive session on one stream. The handler is in flight, for Plugin.Shutdown, from before it starts the
// session (or finds it, for an attach) until it returns: its last act is to send Ended or Detached.
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
	defer s.inflight.begin()()
	if open.Attach != nil {
		return s.attachSession(ctx, stream, open)
	}
	cmd, err := s.startSession(ctx, open, target)
	if err != nil {
		return s.startRefusal(ctx, "Open", err)
	}
	o := s.newOpenStream(stream, cmd, cmd.creator, open)
	o.opened = &execv2.Opened{Facts: o.facts, Replay: &execv2.Replay{}} // a new session: start = end = bytes = 0
	return o.run(ctx)
}

// attachSession serves an Open that attaches to a session that lives. The older stream of the session, if there is one, is
// superseded. A session that does not exist, one that has ended (its tombstone is not a session to attach to) and one whose end is
// under way are the same refusal, NOT_FOUND / SESSION_UNKNOWN; an offset the session has not reached is INVALID_REQUEST. A stream that
// has ended when the engine would register it (the host cancelled it) is refused as cancelled and registers nothing: it supersedes no one.
func (s *serverV2) attachSession(ctx context.Context, stream execv2.Exec_OpenServer, open openRequest) error {
	cmd, att, opened, refusal := s.engine.attach(ctx, open.SessionID, open.Attach.FromOffset)
	if refusal != nil {
		return s.refuse(ctx, "Open", refusal)
	}
	o := s.newOpenStream(stream, cmd, att, open)
	o.opened = opened
	return o.run(ctx)
}

// attach is the engine's side of an attach: it finds the session that lives, attaches a stream to it from the offset (the older stream, if
// there is one, is superseded) and says what the stream is opened with, or why it is refused. The served Open and the in-process engine
// both attach here, so that what a test drives is what a host's stream gets. ctx is the stream's: an attach whose stream has ended when it
// would be registered is refused as a cancelled (or timed out) call and registers nothing (see ring.attach).
func (e *engine) attach(ctx context.Context, id string, from uint64) (*command, *attachment, *execv2.Opened, *Error) {
	if hook := e.beforeAttach; hook != nil {
		hook(ctx, id, from)
	}
	if _, ended := e.tombs.get(id); ended {
		return nil, nil, nil, unknownSession()
	}
	cmd, ok := e.session(id)
	if !ok {
		return nil, nil, nil, unknownSession()
	}
	att, opened, err := cmd.attach(ctx, from)
	switch {
	case errors.Is(err, errSessionGone):
		return nil, nil, nil, unknownSession()
	case errors.Is(err, errBeyondOutput):
		return nil, nil, nil, invalidRequest("from_offset %d is beyond the end of the session's output", from)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return nil, nil, nil, contextRefusal(err)
	case err != nil:
		return nil, nil, nil, &Error{Class: ErrorClassInternal, Err: err}
	}
	return cmd, att, opened, nil
}

// newOpenStream makes the stream of a session's attachment.
func (s *serverV2) newOpenStream(stream execv2.Exec_OpenServer, cmd *command, att *attachment, open openRequest) *openStream {
	o := &openStream{
		s: s, stream: stream, cmd: cmd, att: att, req: open,
		log:      cmd.log,
		facts:    cmd.openedFacts(),
		maxInput: cmd.target.info.Session.MaxInputFrame,
		tty:      cmd.request.TTY,
		outDone:  make(chan struct{}),
		failC:    make(chan error, 1),
	}
	o.ctl = newStreamControl(o)
	return o
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
		ID:            open.SessionID,
		Kind:          kindSession,
		Target:        target,
		Route:         start.Target.Route,
		Request:       start.request(target),
		Stdin:         true,
		ReplayBytes:   start.ReplayBytes,
		DetachTimeout: start.DetachTimeout,
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

// openStream is one served stream: one attachment of a session.
type openStream struct {
	s      *serverV2
	stream execv2.Exec_OpenServer
	cmd    *command
	// att is the stream's attachment: what it reads, and how it leaves.
	att *attachment
	req openRequest
	log logging.Logger
	// opened is what Opened carries. facts are what a session of this request was opened with, which Started refines.
	opened   *execv2.Opened
	facts    *execv2.SessionFacts
	maxInput int
	tty      bool
	// ctx ends with the stream, and when run returns.
	ctx context.Context
	// ctl holds what the host's control frames earn it: the acknowledgement of its resizes, the answers to its queries and the facts
	// the function pushes (open_control.go).
	ctl *streamControl

	// sendMu is held for every send. startedSent and linkSent are guarded by it.
	sendMu      sync.Mutex
	startedSent bool
	linkSent    LinkState

	// outDone is closed when the pump has stopped: the command's output was read to its end (pumpErr is nil and left false), the
	// stream left the session (left is true), or the stream ended under it (pumpErr says how). pumpErr and left are written before
	// outDone is closed and read after.
	outDone chan struct{}
	pumpErr error
	left    bool
	// failC receives the status that ends the stream when the host broke the protocol.
	failC chan error

	// ignored holds the kinds of host frame already logged as not served; only the receiver touches it.
	ignored map[string]bool
}

// run serves the stream until its last frame. It returns what the stream ends with: nil after Ended or Detached.
func (o *openStream) run(ctx context.Context) error {
	o.att.bindStream(ctx) // the stream's own context: once it has ended the host has left, whenever this handler notices
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// However the stream ends, it leaves its session: a stream that is over without having left is an implicit detach, and the
	// session lives for its lease. This is a no-op for a stream that left (Detach, supersede), for a session that has ended, and for
	// a session that a protocol violation destroyed; and what the stream still held of its replay goes with it.
	defer o.leave()
	o.ctx = ctx
	defer o.cmd.ctl.attach(o.ctl)() // the facts the function pushes come to this stream
	defer o.ctl.stopTimer()

	if err := o.sendFrames(openedFrame(o.opened)); err != nil {
		if o.req.Start != nil {
			// The host never learned that this session exists, and nobody can come back for it: it ends with the stream.
			o.destroy()
		}
		return err
	}
	if o.tty && o.req.Start != nil && o.req.Start.Size.IsZero() {
		if err := o.sendFrames(noticeFrame(execv2.NoticeKind_NOTICE_KIND_SIZE_DEFAULTED)); err != nil {
			return err
		}
	}
	go o.receive()
	go o.pump()
	return o.serve()
}

// leave is the last act of the handler: the stream is over, and it leaves its session (see command.leave).
func (o *openStream) leave() {
	o.overNoticed()
	o.cmd.leave(o.att)
}

// overNoticed is the moment at which the handler has noticed that its stream is over and has not yet told the session: a test holds the
// handler here (EngineSeams.StreamLeaving), which is how the host's view of the stream and the engine's come apart for a moment.
func (o *openStream) overNoticed() {
	if hook := o.s.engine.beforeStreamLeaves; hook != nil {
		hook()
	}
}

// serve sends the events of the session as they happen and, once the stream's reading of the output is over, its last frame.
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
		case <-o.ctl.wake:
			if err := o.ctl.flush(); err != nil {
				return err
			}
		case <-o.ctl.timer():
			o.ctl.timerFired()
			if err := o.ctl.flush(); err != nil {
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

// finish sends the last frame of the stream. The pump has stopped, so nothing else is sent after it. Either the output was read to
// its end, which means the command has ended, or the stream left the session.
//
// For a command that has ended: its result is final once Done is closed, and its place is free once it is gone, so a host that
// reads Ended and starts another session at once is not refused for the session that has just ended. The session's end was
// decided before Done was closed, and the stream that was attached then is the one that is told Ended: a stream that left before is
// told Detached, whatever the pump saw. A host that broke the protocol is told so with the status it earned, not with a last frame:
// the status wins whenever it was decided.
func (o *openStream) finish() error {
	if o.pumpErr != nil {
		return o.pumpErr
	}
	if err := o.failed(); err != nil {
		return err
	}
	if !o.left {
		select {
		case <-o.cmd.Done():
		case <-o.ctx.Done():
			return status.FromContextError(o.ctx.Err()).Err()
		}
		select {
		case <-o.cmd.Gone():
		case <-o.ctx.Done():
			return status.FromContextError(o.ctx.Err()).Err()
		}
		if err := o.failed(); err != nil {
			return err
		}
	}
	state, lease := o.att.outcome()
	if o.left {
		// The stream left a session that lives on: what its frames earned before it left (the answer to a query, the ack of a size)
		// is sent before its last frame, as it is for a stream that stays. What the session's end leaves unsent is moot (flush).
		if err := o.ctl.flush(); err != nil {
			return err
		}
	}
	if fin := lastFrameOf(state, lease); fin.detached {
		return o.sendDetached(fin.reason, fin.lease)
	}
	if o.left {
		return nil // not reachable: a stream leaves by one of the states above
	}
	res := o.cmd.Result()
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

// sendDetached sends the last frame of a stream whose session lives on.
func (o *openStream) sendDetached(reason execv2.DetachReason, lease time.Time) error {
	o.sendMu.Lock()
	defer o.sendMu.Unlock()
	return o.stream.Send(detachedFrame(reason, lease))
}

// streamFinal is the last frame of a stream, as its attachment's state decides it.
type streamFinal struct {
	// detached is false for Ended: the session ended while the stream was attached, or the stream has not left.
	detached bool
	reason   execv2.DetachReason
	// lease is when the session ends if nobody attaches; zero for a stream that another took the session from.
	lease time.Time
}

// lastFrameOf decides what the stream of an attachment that is in state is told last: Detached when the stream left, for the reason it left
// (the host asked, another stream took the session over, or the host closed its side of the stream: a stream that broke cannot take a
// frame, and this send fails with it), and Ended for a stream that stayed to the end. It is the one place that decides, for the served
// stream and for the in-process one.
func lastFrameOf(state attState, lease time.Time) streamFinal {
	switch state {
	case attDetached:
		return streamFinal{detached: true, reason: execv2.DetachReason_DETACH_REASON_REQUESTED, lease: lease}
	case attSuperseded:
		return streamFinal{detached: true, reason: execv2.DetachReason_DETACH_REASON_SUPERSEDED}
	case attReleased:
		return streamFinal{detached: true, reason: execv2.DetachReason_DETACH_REASON_UNSPECIFIED, lease: lease}
	case attLive, attEnded:
	}
	return streamFinal{}
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

// pump sends the command's output: what the ring holds from the stream's cursor, up to maxOutputFrame bytes of one stream, with
// the range the ring lost before it.
func (o *openStream) pump() {
	defer close(o.outDone)
	// The buffer starts small and grows to a frame's size as the output fills it: a session that is idle, an echo, costs
	// a few KiB, and a flood reaches whole frames after four reads.
	buf := make([]byte, minOutputBuffer)
	for {
		chunk, err := o.att.Read(o.ctx, buf)
		if chunk.N > 0 || len(chunk.Dropped) > 0 {
			if sendErr := o.sendOutput(chunk, buf[:chunk.N]); sendErr != nil {
				o.pumpErr = sendErr
				return
			}
		}
		if err != nil {
			switch {
			case errors.Is(err, io.EOF):
			case errors.Is(err, errStreamLeft):
				o.left = true // detached, superseded or released: finish says which
			default:
				o.pumpErr = err
			}
			return
		}
		if chunk.N == len(buf) && len(buf) < maxOutputFrame {
			buf = make([]byte, min(2*len(buf), maxOutputFrame)) // the ring had more than the buffer held: take more next time
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

// sendStartedLocked sends the Started once there is one and it has not been sent. A session has at most one: the stream that is
// attached when it is made is sent it, and a stream that attaches after it has its facts in Opened (and the stream that was
// superseded is not sent it). The caller holds sendMu.
func (o *openStream) sendStartedLocked() error {
	if o.startedSent {
		return nil
	}
	facts, ok := o.cmd.startedFacts(o.facts)
	if !ok {
		return nil
	}
	o.startedSent = true
	if !o.cmd.ring.claimStarted(o.att) {
		return nil
	}
	return o.stream.Send(startedFrame(facts))
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
			// The host closed its side, or the stream broke: this stream leaves the session, which lives on for its lease.
			o.overNoticed()
			o.cmd.release(o.att)
			return
		}
		if !o.apply(frame) {
			return
		}
	}
}

// apply does what one frame of the host asks. It reports false when the stream is to end.
func (o *openStream) apply(frame *execv2.ClientFrame) bool {
	if o.att.over.Load() {
		// Another stream took the session over, or this one left it: what this stream sends is not the session's host's.
		return false
	}
	switch f := frame.GetFrame().(type) {
	case *execv2.ClientFrame_Input:
		if o.ctl.inputClosed {
			o.fail(invalidRequest("an input frame after close_input"))
			return false
		}
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
		return o.applyResize(f.Resize)
	case *execv2.ClientFrame_Close:
		// Close destroys the session. It is applied only if this stream is the session's: a stream that was superseded is not asked.
		o.cmd.ring.closeFrom(o.att, f.Close.GetMode() == execv2.CloseMode_CLOSE_MODE_KILL)
	case *execv2.ClientFrame_Open:
		o.fail(invalidRequest("a second open frame on one stream"))
		return false
	case *execv2.ClientFrame_Detach:
		// Detach leaves the session running for the lease and ends this stream, which is told Detached once the pump has
		// stopped. A Detach that cannot be applied (the session is ending, or has ended) is moot: the stream is told how the
		// session ended.
		if _, ok := o.cmd.detach(o.att, time.Duration(f.Detach.GetLeaseMs())*time.Millisecond); ok {
			return false
		}
	case *execv2.ClientFrame_CloseInput:
		return o.applyCloseInput()
	case *execv2.ClientFrame_Signal:
		return o.applySignal(f.Signal)
	case *execv2.ClientFrame_Query:
		return o.applyQuery(f.Query)
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

// fail ends the stream with the status of e, because the host broke the protocol, and ends the session with it, if the stream is
// still the session's: a stream that another took the session from, or that left, has no session to end. The status is published
// first, so that the stream's last word is the status whatever else is decided (finish reads it before it reads the end of the
// session); the stream may then return and leave its session before the session is ended, which destroy allows.
func (o *openStream) fail(e *Error) {
	select {
	case o.failC <- o.s.refuse(o.ctx, "Open", e):
	default:
	}
	o.destroy()
}

// destroy ends the session of a stream that must not leave it running: a host that broke the protocol, and a host that was never
// told that the session exists. Graceful, and a no-op for a session that has ended, and for one that the stream is no longer the
// session's host of (a violation of a stream that was superseded does not end the session its successor holds): see ring.destroyFrom.
func (o *openStream) destroy() { o.cmd.ring.destroyFrom(o.att) }
