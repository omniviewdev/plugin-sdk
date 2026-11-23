package exectest

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/internal/exectestkit"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// A plugin that is wrong, for the bodies of the interactive session: the real plugin, served as it is, read through a client that
// changes what the host receives at the edge where it reads it. Each fault is one lie about what the plugin did (an Ended that says
// something else, a frame that is repeated or comes out of order, a refusal that carries what must stay local, an Opened that claims
// the capabilities of another mode), and each is meant to be found by the body of one case.

// faultyOpenClient is the client of a plugin whose frames and refusals say something else than the plugin did.
type faultyOpenClient struct {
	exec.Client
	// frame returns the frames the host receives in place of a frame the plugin sent: none drops it, two repeat it or add one. The frame
	// is a copy that the fault may change.
	frame func(f *execv2.ServerFrame) []*execv2.ServerFrame
	// opened changes the Opened the host reads, a copy.
	opened func(o *execv2.Opened)
	// end changes how a stream ends: the frames it carries before its last answer (nothing for a stream that ended as it did), and the
	// answer, io.EOF for a stream that ended with status OK.
	end func(err error) ([]*execv2.ServerFrame, error)
	// refuse changes the refusal of an Open.
	refuse func(err error) error
	// perStream makes the frame function of one stream, which sees its frames in order: a fault that needs to know what the stream
	// said before (the Started that came before this Ended) keeps it there. It replaces frame.
	perStream func() func(f *execv2.ServerFrame) []*execv2.ServerFrame
	// sessions changes the answer of ListSessions that the host reads, a copy, and read the answer of ReadOutput: the tombstone of a
	// session that ended while no stream was attached is read through them, and a plugin that is wrong about it is wrong here.
	sessions func(l *execv2.ListSessionsResponse)
	read     func(r *execv2.ReadOutputResponse)
}

func (c faultyOpenClient) ListSessions(ctx context.Context) (*execv2.ListSessionsResponse, error) {
	list, err := c.Client.ListSessions(ctx)
	if err != nil || c.sessions == nil {
		return list, err
	}
	list = proto.Clone(list).(*execv2.ListSessionsResponse)
	c.sessions(list)
	return list, nil
}

func (c faultyOpenClient) ReadOutput(ctx context.Context, req *execv2.ReadOutputRequest) (*execv2.ReadOutputResponse, error) {
	resp, err := c.Client.ReadOutput(ctx, req)
	if err != nil || c.read == nil {
		return resp, err
	}
	resp = proto.Clone(resp).(*execv2.ReadOutputResponse)
	c.read(resp)
	return resp, nil
}

func (c faultyOpenClient) Open(ctx context.Context, req *execv2.OpenRequest) (exec.Conn, error) {
	conn, err := c.Client.Open(ctx, req)
	if err != nil {
		if c.refuse != nil {
			err = c.refuse(err)
		}
		return nil, err
	}
	opened := proto.Clone(conn.Opened()).(*execv2.Opened)
	if c.opened != nil {
		c.opened(opened)
	}
	conn2 := &faultyOpenConn{Conn: conn, c: c, opened: opened, frameFn: c.frame}
	if c.perStream != nil {
		conn2.frameFn = c.perStream()
	}
	return conn2, nil
}

// faultyOpenConn is one stream read through the fault.
type faultyOpenConn struct {
	exec.Conn
	c       faultyOpenClient
	opened  *execv2.Opened
	frameFn func(f *execv2.ServerFrame) []*execv2.ServerFrame
	pending []*execv2.ServerFrame
	err     error // what the stream ended with, once the frames that came before it are out
	over    bool
}

func (c *faultyOpenConn) Opened() *execv2.Opened { return c.opened }

func (c *faultyOpenConn) Recv() (*execv2.ServerFrame, error) {
	for len(c.pending) == 0 {
		if c.over {
			return nil, c.err
		}
		frame, err := c.Conn.Recv()
		if err != nil {
			c.over, c.err = true, err
			if c.c.end != nil {
				c.pending, c.err = c.c.end(err)
			}
			continue
		}
		if c.frameFn == nil {
			return frame, nil
		}
		c.pending = c.frameFn(proto.Clone(frame).(*execv2.ServerFrame))
	}
	frame := c.pending[0]
	c.pending = c.pending[1:]
	return frame, nil
}

// faultyRawOpen is the generated client of a plugin whose Open statuses carry what must stay local.
type faultyRawOpen struct {
	execv2.ExecClient
	// status is what Recv returns in place of the plugin's error; trailer is what the stream says of its trailer.
	status  error
	trailer metadata.MD
}

func (f faultyRawOpen) Open(ctx context.Context, opts ...grpc.CallOption) (execv2.Exec_OpenClient, error) {
	stream, err := f.ExecClient.Open(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return &faultyRawStream{Exec_OpenClient: stream, f: f}, nil
}

type faultyRawStream struct {
	execv2.Exec_OpenClient
	f faultyRawOpen
}

func (s *faultyRawStream) Recv() (*execv2.ServerFrame, error) {
	frame, err := s.Exec_OpenClient.Recv()
	if err != nil && s.f.status != nil {
		return nil, s.f.status
	}
	return frame, err
}

func (s *faultyRawStream) Trailer() metadata.MD {
	if s.f.trailer != nil {
		return s.f.trailer
	}
	return s.Exec_OpenClient.Trailer()
}

// serveFaultyOpen serves the real plugin and has the host read it through a client with the fault, and, when raw is set, through a
// generated client with the fault of raw.
func serveFaultyOpen(c faultyOpenClient, raw *faultyRawOpen) serveFunc {
	return func(t checkT, targets []exec.Target, conns *exectestkit.Connections) *wireServer {
		server := serveReal(t, targets, conns)
		c.Client = server.client
		server.client = c
		if raw != nil {
			faulty := *raw
			faulty.ExecClient = server.raw
			server.raw = faulty
		}
		return server
	}
}

// endedFault changes the Ended of every stream.
func endedFault(change func(e *execv2.Ended)) faultyOpenClient {
	return faultyOpenClient{frame: func(f *execv2.ServerFrame) []*execv2.ServerFrame {
		if e := f.GetEnded(); e != nil {
			change(e)
		}
		return []*execv2.ServerFrame{f}
	}}
}

// endFault changes the end of every Ended that has one.
func endFault(change func(end *execv2.End)) faultyOpenClient {
	return endedFault(func(e *execv2.Ended) {
		if e.GetEnd() != nil {
			change(e.GetEnd())
		}
	})
}

// framesFault changes the frames that a kind of frame becomes: the function returns the frames for any frame it is given.
func framesFault(change func(f *execv2.ServerFrame) []*execv2.ServerFrame) faultyOpenClient {
	return faultyOpenClient{frame: change}
}

// refusalFault changes the refusals of an Open that are the exec lane's.
func refusalFault(change func(e *exec.Error)) faultyOpenClient {
	return faultyOpenClient{refuse: func(err error) error {
		var refusal *exec.Error
		if !errors.As(err, &refusal) {
			return err
		}
		changed := *refusal
		change(&changed)
		return &changed
	}}
}

// listingFault changes every entry of ListSessions, the live sessions and the tombstones, as the host reads them.
func listingFault(change func(info *execv2.SessionInfo)) faultyOpenClient {
	return faultyOpenClient{sessions: func(l *execv2.ListSessionsResponse) {
		for _, info := range l.GetSessions() {
			change(info)
		}
	}}
}

// tombstoneFault changes the Ended of every tombstone that ListSessions lists: the end of a session that ended while no stream was
// attached, which is all the host has of it.
func tombstoneFault(change func(e *execv2.Ended)) faultyOpenClient {
	return listingFault(func(info *execv2.SessionInfo) {
		if e := info.GetEndedWith(); e != nil {
			change(e)
		}
	})
}

// tombstoneEndFault changes the end of every tombstone that has one.
func tombstoneEndFault(change func(end *execv2.End)) faultyOpenClient {
	return tombstoneFault(func(e *execv2.Ended) {
		if e.GetEnd() != nil {
			change(e.GetEnd())
		}
	})
}

// detachedFault changes the Detached of every stream that carries one.
func detachedFault(change func(d *execv2.Detached)) faultyOpenClient {
	return framesFault(func(f *execv2.ServerFrame) []*execv2.ServerFrame {
		if d := f.GetDetached(); d != nil {
			change(d)
		}
		return []*execv2.ServerFrame{f}
	})
}

func TestEveryOpenRowBodyFailsWhenThePluginIsWrong(t *testing.T) {
	shell, echoHost, linkHost := 0, 1, len(shapes())
	pod, noStatus := 2, 3 // CLOSES_STREAM_ONLY with ExitReport CODE, and BEST_EFFORT with ExitReport NONE
	shapesOf := openShapes()
	pass := func(f *execv2.ServerFrame) []*execv2.ServerFrame { return []*execv2.ServerFrame{f} }
	var everyThird, everySeventh atomic.Int64 // count the Ended frames that a fault that is wrong only now and then has seen
	var partA atomic.Int64                    // counts the Ended frames of the sessions that case 25 opens before it opens the streams of one session
	for _, tc := range []struct {
		name   string
		body   string
		shape  int
		fault  faultyOpenClient
		raw    *faultyRawOpen
		phrase string // what a failure of the body says
	}{
		// case 9, and the cases that read an Ended, which hold it to the same counts
		{"an Ended whose end has no arm", "case 09", shell, endFault(func(end *execv2.End) { end.Kind = nil }), nil, "the end has no arm"},
		{"a bytes_out that is one over", "case 09", shell, endedFault(func(e *execv2.Ended) { e.BytesOut++ }), nil, "bytes_out is"},
		{"a bytes_in that is one short", "case 09", shell, endedFault(func(e *execv2.Ended) {
			if e.BytesIn > 0 {
				e.BytesIn--
			}
		}), nil, "bytes_in is"},
		{"a last_offset that is not bytes_out", "case 09", shell, endedFault(func(e *execv2.Ended) { e.LastOffset++ }), nil, "last_offset is"},
		{"an Ended that is repeated", "case 09", shell, framesFault(func(f *execv2.ServerFrame) []*execv2.ServerFrame {
			if f.GetEnded() != nil {
				return []*execv2.ServerFrame{f, proto.Clone(f).(*execv2.ServerFrame)}
			}
			return pass(f)
		}), nil, "nothing follows Ended"},
		{"an Output after the Ended", "case 09", shell, framesFault(func(f *execv2.ServerFrame) []*execv2.ServerFrame {
			if f.GetEnded() != nil {
				return []*execv2.ServerFrame{f, {Frame: &execv2.ServerFrame_Output{Output: &execv2.Output{Data: []byte("late")}}}}
			}
			return pass(f)
		}), nil, "nothing follows Ended"},
		{"a stream that ends with an error after its Ended", "case 09", shell, faultyOpenClient{end: func(err error) ([]*execv2.ServerFrame, error) {
			if errors.Is(err, io.EOF) {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}}, nil, "nothing follows Ended"},
		{"a stream that ends with status OK and no Ended", "case 09", shell, framesFault(func(f *execv2.ServerFrame) []*execv2.ServerFrame {
			if f.GetEnded() != nil {
				return nil
			}
			return pass(f)
		}), nil, "no Ended"},
		{"a lost end that says it exited with 0", "case 09", shell, endFault(func(end *execv2.End) {
			if end.GetLost() != nil {
				end.Kind, end.RemoteMaySurvive = &execv2.End_Exited{Exited: &execv2.Exited{Source: execv2.ExitSource_EXIT_SOURCE_PROCESS}}, new(false)
			}
		}), nil, "the end is"},
		{"a close by the host that says it was cancelled", "case 09", shell, endFault(func(end *execv2.End) {
			if end.GetClosed().GetBy() == execv2.ClosedBy_CLOSED_BY_REQUEST {
				end.GetClosed().By = execv2.ClosedBy_CLOSED_BY_CANCELLED
			}
		}), nil, "the end is"},
		{"a session that says its start was observed when nothing showed it", "case 09", shell, endedFault(func(e *execv2.Ended) { e.StartObserved = true }), nil, "start_observed is true, want false"},
		{"a lost end that claims its output is complete", "case 09", shell, endedFault(func(e *execv2.Ended) {
			if e.GetEnd().GetLost() != nil {
				e.Output = execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE
			}
		}), nil, "output is"},
		{"a session without a terminal that has no mode reset", "case 09", echoHost, endedFault(func(e *execv2.Ended) { e.ModeReset = nil }), nil, "mode_reset is"},
		{"a failed end at the wrong stage", "case 09", shell, endFault(func(end *execv2.End) {
			if end.GetFailed().GetStage() == execv2.FailureStage_FAILURE_STAGE_RUN {
				end.GetFailed().Stage = execv2.FailureStage_FAILURE_STAGE_STREAM
			}
		}), nil, "the end is"},

		// case 09, a session that ended while no stream was attached: its end is the tombstone's, which the host reads with ListSessions and ReadOutput
		{"a tombstone whose end has no arm", "case 09", shell, tombstoneEndFault(func(end *execv2.End) { end.Kind = nil }), nil, "the end has no arm"},
		{"a tombstone that counts a byte more than the function wrote", "case 09", shell, tombstoneFault(func(e *execv2.Ended) { e.BytesOut++ }), nil, "the tombstone: bytes_out is"},
		{"a tombstone that forgot the input the host sent", "case 09", shell, tombstoneFault(func(e *execv2.Ended) { e.BytesIn = 0 }), nil, "the tombstone: bytes_in is"},
		{"a tombstone whose last_offset is not bytes_out", "case 09", shell, tombstoneFault(func(e *execv2.Ended) { e.LastOffset++ }), nil, "the tombstone: last_offset is"},
		{"a tombstone that lists a next_offset beyond the end", "case 09", shell, listingFault(func(info *execv2.SessionInfo) {
			if info.GetEndedWith() != nil {
				info.NextOffset++
			}
		}), nil, "next_offset is"},
		{"a tombstone that lists a stream attached", "case 09", shell, listingFault(func(info *execv2.SessionInfo) {
			if info.GetEndedWith() != nil {
				info.Attached = true
			}
		}), nil, "a stream is attached to a session that ended"},
		{"a lost end that the tombstone reads as an exit", "case 09", shell, tombstoneEndFault(func(end *execv2.End) {
			if end.GetLost() != nil {
				end.Kind, end.RemoteMaySurvive = &execv2.End_Exited{Exited: &execv2.Exited{Source: execv2.ExitSource_EXIT_SOURCE_PROCESS}}, new(false)
			}
		}), nil, "the tombstone: the end is"},
		{"a lease that ran out, which the tombstone reads as a close by the host", "case 09", shell, tombstoneEndFault(func(end *execv2.End) {
			if end.GetClosed().GetBy() == execv2.ClosedBy_CLOSED_BY_DETACH_TIMEOUT {
				end.GetClosed().By = execv2.ClosedBy_CLOSED_BY_REQUEST
			}
		}), nil, "the tombstone: the end is"},
		{"a close by the host, which the tombstone reads as a lease that ran out", "case 09", shell, tombstoneEndFault(func(end *execv2.End) {
			if end.GetClosed().GetBy() == execv2.ClosedBy_CLOSED_BY_REQUEST {
				end.GetClosed().By = execv2.ClosedBy_CLOSED_BY_DETACH_TIMEOUT
			}
		}), nil, "the tombstone: the end is"},
		{"a lease that ran out, whose tombstone says the process is gone, for a kind that cannot promise it", "case 09", pod, tombstoneEndFault(func(end *execv2.End) {
			if end.GetClosed().GetBy() == execv2.ClosedBy_CLOSED_BY_DETACH_TIMEOUT {
				end.RemoteMaySurvive = new(false)
			}
		}), nil, "the tombstone says remote_may_survive is false, want true"},
		{"a lease that ran out, whose tombstone says nothing of the survival, for a kind that cannot promise it", "case 09", pod, tombstoneEndFault(func(end *execv2.End) {
			if end.GetClosed().GetBy() == execv2.ClosedBy_CLOSED_BY_DETACH_TIMEOUT {
				end.RemoteMaySurvive = nil
			}
		}), nil, "the tombstone says remote_may_survive is absent, want true"},
		{"a lease that ran out on a kind that terminates, whose tombstone says the process may survive", "case 09", shell, tombstoneEndFault(func(end *execv2.End) {
			if end.GetClosed().GetBy() == execv2.ClosedBy_CLOSED_BY_DETACH_TIMEOUT {
				end.RemoteMaySurvive = new(true)
			}
		}), nil, "the tombstone says remote_may_survive is true, want false"},
		{"a code of the transport whose tombstone says the process is gone", "case 09", noStatus, tombstoneEndFault(func(end *execv2.End) {
			if end.GetExited().GetSource() == execv2.ExitSource_EXIT_SOURCE_TRANSPORT {
				end.RemoteMaySurvive = new(false)
			}
		}), nil, "the tombstone says remote_may_survive is false, want absent"},
		{"a code of the transport whose tombstone says its start was not observed", "case 09", noStatus, tombstoneFault(func(e *execv2.Ended) {
			if e.GetEnd().GetExited().GetSource() == execv2.ExitSource_EXIT_SOURCE_TRANSPORT {
				e.StartObserved = false
			}
		}), nil, "the tombstone: start_observed is false, want true"},
		{"a lost end whose tombstone claims the output is complete", "case 09", shell, tombstoneFault(func(e *execv2.Ended) {
			if e.GetEnd().GetLost() != nil {
				e.Output = execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE
			}
		}), nil, "the tombstone: output is"},
		{"a tombstone of a session without a terminal that has no mode reset", "case 09", echoHost, tombstoneFault(func(e *execv2.Ended) { e.ModeReset = nil }), nil, "the tombstone: mode_reset is"},
		{"a tombstone whose output is not said to have ended", "case 09", shell, faultyOpenClient{read: func(r *execv2.ReadOutputResponse) { r.Ended = false }}, nil, "ended is false, want true"},
		{"a tombstone whose output has a wrong byte", "case 09", shell, faultyOpenClient{read: func(r *execv2.ReadOutputResponse) {
			for _, c := range r.GetChunks() {
				if len(c.Data) > 0 {
					c.Data[0] ^= 0x20
				}
			}
		}}, nil, "ReadOutput of the tombstone gave"},
		{"a tombstone whose output ends later than the session", "case 09", shell, faultyOpenClient{read: func(r *execv2.ReadOutputResponse) { r.NextOffset++ }}, nil, "ReadOutput of the tombstone gave"},
		{"a stream that detached and is told Ended", "case 09", shell, framesFault(func(f *execv2.ServerFrame) []*execv2.ServerFrame {
			if f.GetDetached() != nil {
				return []*execv2.ServerFrame{{Frame: &execv2.ServerFrame_Ended{Ended: &execv2.Ended{End: endClosed(execv2.ClosedBy_CLOSED_BY_REQUEST, exec.KillTerminates)}}}}
			}
			return pass(f)
		}), nil, "want Detached"},
		{"a stream that detached and is told nothing", "case 09", shell, framesFault(func(f *execv2.ServerFrame) []*execv2.ServerFrame {
			if f.GetDetached() != nil {
				return nil
			}
			return pass(f)
		}), nil, "has no Detached"},
		{"a stream that detached and is then told Ended", "case 09", shell, framesFault(func(f *execv2.ServerFrame) []*execv2.ServerFrame {
			if f.GetDetached() != nil {
				return []*execv2.ServerFrame{f, {Frame: &execv2.ServerFrame_Ended{Ended: &execv2.Ended{End: endClosed(execv2.ClosedBy_CLOSED_BY_REQUEST, exec.KillTerminates)}}}}
			}
			return pass(f)
		}), nil, "nothing follows"},
		{"a session whose tombstone says another end than its stream was told", "case 09", shell, tombstoneFault(func(e *execv2.Ended) { e.DurationMs += 1_000 }), nil, "the stream was told"},
		{"a Detached for a Detach whose lease is an hour too long", "case 09", shell, detachedFault(func(d *execv2.Detached) {
			if d.GetReason() == execv2.DetachReason_DETACH_REASON_REQUESTED {
				d.LeaseExpiresUnixMs += 3_600_000
			}
		}), nil, "Detached says the lease ends at"},
		{"a detached session whose listing says another lease than the host was told", "case 09", shell, listingFault(func(info *execv2.SessionInfo) {
			if info.GetEndedWith() == nil && !info.GetAttached() {
				info.LeaseExpiresUnixMs += 3_600_000
			}
		}), nil, "the listing says the lease ends at"},
		{"a session whose stream broke and whose listing says an hour more than its own lease", "case 09", shell, listingFault(func(info *execv2.SessionInfo) {
			if info.GetEndedWith() == nil && !info.GetAttached() && info.GetLeaseExpiresUnixMs() != 0 {
				info.LeaseExpiresUnixMs += 3_600_000
			}
		}), nil, "the listing says the lease of a session whose stream broke"},

		// case 12
		{"a refusal for too many sessions that is INTERNAL", "case 12", shell, refusalFault(func(e *exec.Error) {
			if e.Reason == exec.ErrorReasonTooManySessions {
				e.Class, e.Reason = exec.ErrorClassInternal, exec.ErrorReasonUnspecified
			}
		}), nil, "want LIMIT/TOO_MANY_SESSIONS"},
		{"a refusal for the id of a live session that is INVALID_REQUEST", "case 12", shell, refusalFault(func(e *exec.Error) {
			if e.Reason == exec.ErrorReasonSessionExists {
				e.Class, e.Reason = exec.ErrorClassInvalidRequest, exec.ErrorReasonUnspecified
			}
		}), nil, "want CONFLICT/SESSION_EXISTS"},
		{"an input over the maximum that ends the stream with status OK", "case 12", shell, faultyOpenClient{end: func(err error) ([]*execv2.ServerFrame, error) {
			if refusalName(err) == "LIMIT/INPUT_TOO_LARGE" {
				return nil, io.EOF
			}
			return nil, err
		}}, nil, "ended the stream with status OK"},
		{"an input over the maximum that is answered with an Ended", "case 12", shell, faultyOpenClient{end: func(err error) ([]*execv2.ServerFrame, error) {
			if refusalName(err) == "LIMIT/INPUT_TOO_LARGE" {
				return []*execv2.ServerFrame{{Frame: &execv2.ServerFrame_Ended{Ended: &execv2.Ended{}}}}, err
			}
			return nil, err
		}}, nil, "was answered with an Ended"},
		{"an input over the maximum that is another refusal", "case 12", shell, faultyOpenClient{end: func(err error) ([]*execv2.ServerFrame, error) {
			var refusal *exec.Error
			if errors.As(err, &refusal) && refusal.Reason == exec.ErrorReasonInputTooLarge {
				return nil, &exec.Error{Class: exec.ErrorClassInvalidRequest}
			}
			return nil, err
		}}, nil, "ended the stream with"},
		{"a size at the largest dimension that is opened as another", "case 12", shell, faultyOpenClient{opened: func(o *execv2.Opened) {
			if o.GetFacts().GetSize() != nil {
				o.Facts.Size = &execv2.Size{Cols: 1, Rows: 1}
			}
		}}, nil, "was opened as"},
		{"an id of 128 bytes that is opened as another", "case 12", shell, faultyOpenClient{opened: func(o *execv2.Opened) { o.Facts.SessionId = "changed" }}, nil, "was opened as"},

		// case 13
		{"a failure that lost its facts", "case 13", shell, endFault(func(end *execv2.End) {
			if end.GetFailed() != nil {
				end.GetFailed().Facts = nil
			}
		}), nil, "the end is"},
		{"a failure whose public message was dropped", "case 13", shell, endFault(func(end *execv2.End) {
			if end.GetFailed() != nil {
				end.GetFailed().PublicMessage = ""
			}
		}), nil, "the end is"},
		{"a failure whose public message carries the canary", "case 13", shell, endFault(func(end *execv2.End) {
			if end.GetFailed() != nil {
				end.GetFailed().PublicMessage = runCanary
			}
		}), nil, "carries the canary"},
		{"a failure of another class", "case 13", shell, endFault(func(end *execv2.End) {
			if end.GetFailed().GetClass() == execv2.ErrorClass_ERROR_CLASS_NOT_FOUND {
				end.GetFailed().Class = execv2.ErrorClass_ERROR_CLASS_UNAVAILABLE
			}
		}), nil, "the end is"},
		{"a refusal that lost its facts", "case 13", shell, refusalFault(func(e *exec.Error) { e.Facts = exec.ErrorFacts{} }), nil, "the refusal is"},
		{"a refusal that carries a local cause", "case 13", shell, refusalFault(func(e *exec.Error) { e.Err = errors.New("a local cause") }), nil, "carries a local cause"},
		{"a refusal whose status carries the canary", "case 13", shell, faultyOpenClient{}, &faultyRawOpen{status: statusWithCanary()}, "carries the canary"},
		{"a refusal whose trailer carries the canary", "case 13", shell, faultyOpenClient{}, &faultyRawOpen{trailer: metadata.Pairs("detail", runCanary)}, "carries the canary"},

		// case 14
		{"a retired session that says it was cancelled", "case 14", shell, endFault(func(end *execv2.End) {
			if end.GetClosed().GetBy() == execv2.ClosedBy_CLOSED_BY_CONNECTION_RETIRED {
				end.GetClosed().By = execv2.ClosedBy_CLOSED_BY_CANCELLED
			}
		}), nil, "the end is"},
		{"a retired session whose stream ends with no Ended", "case 14", shell, framesFault(func(f *execv2.ServerFrame) []*execv2.ServerFrame {
			if f.GetEnded().GetEnd().GetClosed().GetBy() == execv2.ClosedBy_CLOSED_BY_CONNECTION_RETIRED {
				return nil
			}
			return pass(f)
		}), nil, "no Ended"},

		// case 16
		{"a lost end that says the process is gone", "case 16", shell, endFault(func(end *execv2.End) {
			if end.GetLost() != nil {
				end.RemoteMaySurvive = new(false)
			}
		}), nil, "the end is"},
		{"a lost end that says a code of 0", "case 16", shell, endFault(func(end *execv2.End) {
			if end.GetLost() != nil {
				end.Kind, end.RemoteMaySurvive = &execv2.End_Exited{Exited: &execv2.Exited{Source: execv2.ExitSource_EXIT_SOURCE_TRANSPORT}}, nil
			}
		}), nil, "the end is"},
		{"a link that gave up that says the stream had no status", "case 16", shell, endFault(func(end *execv2.End) {
			if end.GetLost().GetCause() == execv2.LostCause_LOST_CAUSE_LINK_DEAD {
				end.GetLost().Cause = execv2.LostCause_LOST_CAUSE_NO_STATUS
			}
		}), nil, "the end is"},

		// case 17
		{"a code narrowed to 32 bits", "case 17", shell, endFault(func(end *execv2.End) {
			if end.GetExited() != nil {
				end.GetExited().ExitCode = int64(int32(end.GetExited().GetExitCode()))
			}
		}), nil, "the end is"},
		{"every exit says it is the process's", "case 17", shell, endFault(func(end *execv2.End) {
			if end.GetExited() != nil {
				end.GetExited().Source = execv2.ExitSource_EXIT_SOURCE_PROCESS
			}
		}), nil, "the end is"},
		{"a folded code that says it is the process's", "case 17", shell, endFault(func(end *execv2.End) {
			if end.GetExited().GetSource() == execv2.ExitSource_EXIT_SOURCE_SIGNAL_FOLDED {
				end.GetExited().Source = execv2.ExitSource_EXIT_SOURCE_PROCESS
			}
		}), nil, "the end is"},
		{"an Exit that says nothing, reported as a signal", "case 17", shell, endFault(func(end *execv2.End) {
			if end.GetFailed() != nil {
				end.Kind = &execv2.End_Signaled{Signaled: &execv2.Signaled{Signal: "SIGKILL", SignalNumber: 9, Source: execv2.ExitSource_EXIT_SOURCE_PROCESS}}
			}
		}), nil, "want a failure of the target"},
		{"a signal whose number was dropped", "case 17", shell, endFault(func(end *execv2.End) {
			if end.GetSignaled() != nil {
				end.GetSignaled().SignalNumber = 0
			}
		}), nil, "the end is"},
		{"a signal from a kind that reports none, reported as an exit", "case 17", 2, endFault(func(end *execv2.End) {
			if end.GetFailed() != nil {
				end.Kind = &execv2.End_Exited{Exited: &execv2.Exited{ExitCode: 137, Source: execv2.ExitSource_EXIT_SOURCE_SIGNAL_FOLDED}}
			}
		}), nil, "the end is"},

		// case 23
		{"a Link frame that is repeated", "case 23", linkHost, framesFault(func(f *execv2.ServerFrame) []*execv2.ServerFrame {
			if f.GetLink() != nil {
				return []*execv2.ServerFrame{f, proto.Clone(f).(*execv2.ServerFrame)}
			}
			return pass(f)
		}), nil, "the host was told the link states"},
		{"a Link frame for another state", "case 23", linkHost, framesFault(func(f *execv2.ServerFrame) []*execv2.ServerFrame {
			if f.GetLink().GetState() == execv2.LinkState_LINK_STATE_GOOD {
				f.GetLink().State = execv2.LinkState_LINK_STATE_DEGRADED
			}
			return pass(f)
		}), nil, "the host was told the link states"},
		{"a Link frame that says how long the link was silent", "case 23", linkHost, framesFault(func(f *execv2.ServerFrame) []*execv2.ServerFrame {
			if f.GetLink() != nil {
				f.GetLink().SilentMs = 5
			}
			return pass(f)
		}), nil, "silent_ms"},
		{"a Link frame for a kind that declares no link health", "case 23", shell, framesFault(func(f *execv2.ServerFrame) []*execv2.ServerFrame {
			if f.GetEnded() != nil {
				return []*execv2.ServerFrame{{Frame: &execv2.ServerFrame_Link{Link: &execv2.Link{State: execv2.LinkState_LINK_STATE_GOOD}}}, f}
			}
			return pass(f)
		}), nil, "the host was told the link states"},
		{"a status that was returned while the link was stalled, read as the process's", "case 23", linkHost, endFault(func(end *execv2.End) {
			if end.GetExited().GetSource() == execv2.ExitSource_EXIT_SOURCE_TRANSPORT {
				end.GetExited().Source, end.RemoteMaySurvive = execv2.ExitSource_EXIT_SOURCE_PROCESS, new(false)
			}
		}), nil, "the end is"},
		{"a stalled link that gave up, read as an exit", "case 23", linkHost, endFault(func(end *execv2.End) {
			if end.GetLost() != nil {
				end.Kind, end.RemoteMaySurvive = &execv2.End_Exited{Exited: &execv2.Exited{Source: execv2.ExitSource_EXIT_SOURCE_PROCESS}}, new(false)
			}
		}), nil, "the end is"},

		// case 25
		{"an Ended that carries the counts of another session", "case 25", shell, endedFault(func(e *execv2.Ended) { e.BytesIn++ }), nil, "bytes_in is"},
		{"a stream that carries the output of another session", "case 25", shell, framesFault(func(f *execv2.ServerFrame) []*execv2.ServerFrame {
			if o := f.GetOutput(); o != nil && len(o.Data) > 0 {
				o.Data[0] ^= 0x20
			}
			return pass(f)
		}), nil, "the output is"},
		{"a Detached frame that no Detach asked for", "case 25", shell, framesFault(func(f *execv2.ServerFrame) []*execv2.ServerFrame {
			if f.GetEnded() != nil {
				return []*execv2.ServerFrame{{Frame: &execv2.ServerFrame_Detached{Detached: &execv2.Detached{Reason: execv2.DetachReason_DETACH_REASON_REQUESTED}}}, f}
			}
			return pass(f)
		}), nil, "Detached"},
		{"an Ended that is repeated on one stream of many", "case 25", shell, framesFault(func(f *execv2.ServerFrame) []*execv2.ServerFrame {
			if f.GetEnded() != nil && everyThird.Add(1)%3 == 0 {
				return []*execv2.ServerFrame{f, proto.Clone(f).(*execv2.ServerFrame)}
			}
			return pass(f)
		}), nil, "nothing follows Ended"},
		// case 25, the streams of one session: each ends with exactly one of Ended or Detached, and an Ended never goes to one that left
		{"a Detached for a Detach that has the reason SUPERSEDED", "case 25", shell, detachedFault(func(d *execv2.Detached) {
			if d.GetReason() == execv2.DetachReason_DETACH_REASON_REQUESTED {
				d.Reason = execv2.DetachReason_DETACH_REASON_SUPERSEDED
			}
		}), nil, "Detached has the reason"},
		{"a Detached for a superseded stream that has the reason REQUESTED", "case 25", shell, detachedFault(func(d *execv2.Detached) {
			if d.GetReason() == execv2.DetachReason_DETACH_REASON_SUPERSEDED {
				d.Reason = execv2.DetachReason_DETACH_REASON_REQUESTED
			}
		}), nil, "Detached has the reason"},
		{"a Detached for a Detach that says no lease", "case 25", shell, detachedFault(func(d *execv2.Detached) {
			if d.GetReason() == execv2.DetachReason_DETACH_REASON_REQUESTED {
				d.LeaseExpiresUnixMs = 0
			}
		}), nil, "the lease ends at"},
		{"a Detached for a Detach whose lease is an hour too long", "case 25", shell, detachedFault(func(d *execv2.Detached) {
			if d.GetReason() == execv2.DetachReason_DETACH_REASON_REQUESTED {
				d.LeaseExpiresUnixMs += 3_600_000
			}
		}), nil, "the lease ends at"},
		{"a Detached for a superseded stream that carries a lease", "case 25", shell, detachedFault(func(d *execv2.Detached) {
			if d.GetReason() == execv2.DetachReason_DETACH_REASON_SUPERSEDED {
				d.LeaseExpiresUnixMs = time.Now().Add(time.Hour).UnixMilli()
			}
		}), nil, "want no lease"},
		{"a superseded stream that is told Ended", "case 25", shell, framesFault(func(f *execv2.ServerFrame) []*execv2.ServerFrame {
			if f.GetDetached().GetReason() == execv2.DetachReason_DETACH_REASON_SUPERSEDED {
				return []*execv2.ServerFrame{{Frame: &execv2.ServerFrame_Ended{Ended: &execv2.Ended{End: endClosed(execv2.ClosedBy_CLOSED_BY_REQUEST, exec.KillTerminates)}}}}
			}
			return pass(f)
		}), nil, "want Detached"},
		{"a stream that detached and is told Ended", "case 25", shell, framesFault(func(f *execv2.ServerFrame) []*execv2.ServerFrame {
			if f.GetDetached().GetReason() == execv2.DetachReason_DETACH_REASON_REQUESTED {
				return []*execv2.ServerFrame{{Frame: &execv2.ServerFrame_Ended{Ended: &execv2.Ended{End: endClosed(execv2.ClosedBy_CLOSED_BY_REQUEST, exec.KillTerminates)}}}}
			}
			return pass(f)
		}), nil, "want Detached"},
		{"a stream that left and is told nothing", "case 25", shell, framesFault(func(f *execv2.ServerFrame) []*execv2.ServerFrame {
			if f.GetDetached() != nil {
				return nil
			}
			return pass(f)
		}), nil, "has no Detached"},
		{"a Detached that an Ended follows", "case 25", shell, framesFault(func(f *execv2.ServerFrame) []*execv2.ServerFrame {
			if f.GetDetached() != nil {
				return []*execv2.ServerFrame{f, {Frame: &execv2.ServerFrame_Ended{Ended: &execv2.Ended{End: endClosed(execv2.ClosedBy_CLOSED_BY_REQUEST, exec.KillTerminates)}}}}
			}
			return pass(f)
		}), nil, "nothing follows"},
		{"a Detached that is repeated", "case 25", shell, framesFault(func(f *execv2.ServerFrame) []*execv2.ServerFrame {
			if f.GetDetached() != nil {
				return []*execv2.ServerFrame{f, proto.Clone(f).(*execv2.ServerFrame)}
			}
			return pass(f)
		}), nil, "nothing follows"},
		{"output after a Detached", "case 25", shell, framesFault(func(f *execv2.ServerFrame) []*execv2.ServerFrame {
			if f.GetDetached() != nil {
				return []*execv2.ServerFrame{f, {Frame: &execv2.ServerFrame_Output{Output: &execv2.Output{Data: []byte("late")}}}}
			}
			return pass(f)
		}), nil, "nothing follows"},
		{"a stream that held its session when it ended and is told Detached", "case 25", shell, framesFault(func(f *execv2.ServerFrame) []*execv2.ServerFrame {
			if f.GetEnded() != nil && partA.Add(1) > int64(2*min(effectiveLimit(shapesOf[shell].info()), 12)) {
				return []*execv2.ServerFrame{{Frame: &execv2.ServerFrame_Detached{Detached: &execv2.Detached{Reason: execv2.DetachReason_DETACH_REASON_REQUESTED}}}}
			}
			return pass(f)
		}), nil, "want Ended"},
		{"a session that two of its streams are told the end of", "case 25", shell, framesFault(func(f *execv2.ServerFrame) []*execv2.ServerFrame {
			if f.GetDetached().GetReason() == execv2.DetachReason_DETACH_REASON_SUPERSEDED {
				return []*execv2.ServerFrame{{Frame: &execv2.ServerFrame_Ended{Ended: &execv2.Ended{End: endClosed(execv2.ClosedBy_CLOSED_BY_REQUEST, exec.KillTerminates)}}}}
			}
			return pass(f)
		}), nil, "Ended frames, want one"},
		{"a detached session that the listing says has no lease", "case 25", shell, listingFault(func(info *execv2.SessionInfo) {
			if info.GetEndedWith() == nil && !info.GetAttached() {
				info.LeaseExpiresUnixMs = 0
			}
		}), nil, "the listing says"},
		{"a session that was superseded and is listed with no stream", "case 25", shell, listingFault(func(info *execv2.SessionInfo) {
			if info.GetEndedWith() == nil && info.GetAttached() {
				info.Attached = false
			}
		}), nil, "attached to that stream"},

		// case 26
		{"an Opened that carries the maximum ExitReport of every mode", "case 26", shell, faultyOpenClient{opened: func(o *execv2.Opened) {
			o.GetFacts().GetCapabilities().ExitReport = execv2.ExitReport_EXIT_REPORT_CODE_AND_SIGNAL
		}}, nil, "Opened: exit_report is"},
		{"an Opened that carries the narrowest Kill of every mode", "case 26", shell, faultyOpenClient{opened: func(o *execv2.Opened) {
			o.GetFacts().GetCapabilities().Kill = execv2.KillBehavior_KILL_BEHAVIOR_CLOSES_STREAM_ONLY
		}}, nil, "Opened: kill is"},
		{"a Started that carries no capabilities", "case 26", shell, framesFault(func(f *execv2.ServerFrame) []*execv2.ServerFrame {
			if f.GetStarted() != nil {
				f.GetStarted().GetFacts().Capabilities = nil
			}
			return pass(f)
		}), nil, "Started: no capabilities"},
		{"a Started whose resize is not the session's", "case 26", shell, framesFault(func(f *execv2.ServerFrame) []*execv2.ServerFrame {
			if c := f.GetStarted().GetFacts().GetCapabilities(); c != nil && c.Resize != nil {
				c.Resize = new(!*c.Resize)
			}
			return pass(f)
		}), nil, "Started: resize is"},

		// case 28
		{"a closed end that says the process may survive a kind that terminates it", "case 28", shell, endFault(func(end *execv2.End) {
			if end.GetClosed() != nil {
				end.RemoteMaySurvive = new(true)
			}
		}), nil, "remote_may_survive is true, want false"},
		{"a transport exit that says the process is gone", "case 28", shell, endFault(func(end *execv2.End) {
			if end.GetExited().GetSource() == execv2.ExitSource_EXIT_SOURCE_TRANSPORT {
				end.RemoteMaySurvive = new(false)
			}
		}), nil, "remote_may_survive is false, want absent"},
		{"a failure that says the process is gone", "case 28", shell, endFault(func(end *execv2.End) {
			if end.GetFailed() != nil {
				end.RemoteMaySurvive = new(false)
			}
		}), nil, "remote_may_survive is false, want absent"},
		{"a status of the process that says nothing of the survival", "case 28", shell, endFault(func(end *execv2.End) {
			if end.GetExited().GetSource() == execv2.ExitSource_EXIT_SOURCE_PROCESS {
				end.RemoteMaySurvive = nil
			}
		}), nil, "remote_may_survive is absent, want false"},
		{"a lost end that says nothing of the survival", "case 28", shell, endFault(func(end *execv2.End) {
			if end.GetLost() != nil {
				end.RemoteMaySurvive = nil
			}
		}), nil, "remote_may_survive is absent, want true"},
		{"a closed end that says the process is gone for a session that cannot promise it", "case 28", shell, endFault(func(end *execv2.End) {
			if end.GetClosed() != nil {
				end.RemoteMaySurvive = new(false)
			}
		}), nil, "remote_may_survive is false, want true"},
		{"a closed end that says nothing of the survival", "case 28", shell, endFault(func(end *execv2.End) {
			if end.GetClosed() != nil {
				end.RemoteMaySurvive = nil
			}
		}), nil, "remote_may_survive is absent, want"},
		{"a closed end of a kind that cannot promise it, that says nothing of the survival", "case 28", 2, endFault(func(end *execv2.End) {
			if end.GetClosed() != nil {
				end.RemoteMaySurvive = nil
			}
		}), nil, "remote_may_survive is absent, want true"},
		{"a closed end of a kind that cannot promise it, that says the process is gone", "case 28", 2, endFault(func(end *execv2.End) {
			if end.GetClosed() != nil {
				end.RemoteMaySurvive = new(false)
			}
		}), nil, "remote_may_survive is false, want true"},
		{"a code of the transport that says its start was not observed", "case 28", shell, endedFault(func(e *execv2.Ended) {
			if e.GetEnd().GetExited().GetSource() == execv2.ExitSource_EXIT_SOURCE_TRANSPORT {
				e.StartObserved = false
			}
		}), nil, "start_observed is false, want true"},
		{"a code of the transport that says its start was not observed", "case 17", shell, endedFault(func(e *execv2.Ended) {
			if e.GetEnd().GetExited().GetSource() == execv2.ExitSource_EXIT_SOURCE_TRANSPORT {
				e.StartObserved = false
			}
		}), nil, "start_observed is false, want true"},
		{"a code of the transport, 0, that is read as the process's own", "case 17", shell, endFault(func(end *execv2.End) {
			if end.GetExited().GetSource() == execv2.ExitSource_EXIT_SOURCE_TRANSPORT && end.GetExited().GetExitCode() == 0 {
				end.GetExited().Source, end.RemoteMaySurvive = execv2.ExitSource_EXIT_SOURCE_PROCESS, new(false)
			}
		}), nil, "the end is"},
		{"a break of the stream that is read as an exit 0 of the transport", "case 17", shell, endFault(func(end *execv2.End) {
			if end.GetLost() != nil {
				end.Kind, end.RemoteMaySurvive = &execv2.End_Exited{Exited: &execv2.Exited{Source: execv2.ExitSource_EXIT_SOURCE_TRANSPORT}}, nil
			}
		}), nil, "want Lost"},

		// pair P5
		{"an end that is not the same when it was repeated", "pair P5", shell, endedFault(func(e *execv2.Ended) {
			if everySeventh.Add(1)%7 == 0 {
				e.BytesOut++
			}
		}), nil, "bytes_out is"},
	} {
		t.Run(tc.body+" with "+tc.name, func(t *testing.T) {
			var calls atomic.Int64
			rec := &recordT{}
			env := &wireEnv{resource: "thing-1", serve: serveFaultyOpen(tc.fault, tc.raw)}
			body := openRowBodyNamed(t, tc.body)
			rec.run(func(r checkT) { body(r, declared(t, shapesOf[tc.shape], &calls), env) })
			if !rec.said(tc.phrase) {
				t.Errorf("the body did not fail with %q on a plugin with the fault %q: it failed with %q (skipped: %q)", tc.phrase, tc.name, rec.failed(), rec.skips)
			}
		})
	}
}
