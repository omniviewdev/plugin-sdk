package exec_test

import (
	"context"
	"testing"
	"testing/synctest"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The control frames of a session (resize, signal, close_input, query) and the frames they earn (Resized, Fact) are held, frame by
// frame, with a stream that lives in memory inside a synctest bubble: the clock is the bubble's, so that a deadline of seconds is
// passed by sleeping through it and costs nothing, and what the server does without a goroutine running has been done when
// synctest.Wait returns, so a test asks what happened and is answered, and decides nothing by waiting. The rig is the one of
// open_memory_rig_test.go; what is here is what the control frames need besides it.

// ctlOpenFrame builds the first frame of an Open of the fake target's thing: with a terminal of a size, or without one.
func (r *memoryRig) ctlOpenFrame(id string, tty bool, cols, rows uint32) *execv2.ClientFrame {
	start := &execv2.Start{Target: r.conns.WireTarget(r.t, "thing", "thing-1"), Tty: tty}
	if tty {
		start.Size = &execv2.Size{Cols: cols, Rows: rows}
	}
	return &execv2.ClientFrame{Frame: &execv2.ClientFrame_Open{Open: &execv2.OpenRequest{
		SessionId: id, Kind: &execv2.OpenRequest_Start{Start: start},
	}}}
}

// ctlStream starts the handler of Open with a stream whose host has sent first, and returns once the stream is the handler's: its
// context is the one a call arrives with at the plugin.
func (r *memoryRig) ctlStream(first *execv2.ClientFrame) *memoryStream {
	r.t.Helper()
	return r.ctlStreamSized(first, 4096)
}

// ctlStreamSized is ctlStream for a host that takes frames through a buffer of outCap frames: a host that does not read holds the
// server's sender once the buffer is full, as one whose window is full does.
func (r *memoryRig) ctlStreamSized(first *execv2.ClientFrame, outCap int) *memoryStream {
	r.t.Helper()
	call := r.conns.Context(context.Background())
	out, err := resourceplugin.OutgoingSessionContext(call)
	if err != nil {
		r.t.Fatalf("the host's context: %v", err)
	}
	md, _ := metadata.FromOutgoingContext(out)
	ctx, cancel := context.WithCancel(metadata.NewIncomingContext(call, md))
	s := &memoryStream{
		ctx: ctx, cancel: cancel,
		in: make(chan *execv2.ClientFrame, 16), out: make(chan *execv2.ServerFrame, outCap), returned: make(chan error, 1),
	}
	s.in <- first
	go func() { s.returned <- r.server.Open(s) }()
	// However the test ends, a failed one included, the host leaves and the server winds the session down: a goroutine left in the
	// bubble is a deadlock that stops the whole run, and hides every other test's result. A host that is no longer there takes no frame,
	// so a sender that its buffer holds is let go too.
	r.t.Cleanup(func() { s.ctlLeave(); s.cancel(); synctest.Wait() })
	return s
}

// ctlSession opens a session with a terminal of a size and waits for its Opened.
func (r *memoryRig) ctlSession(id string, cols, rows uint32) *memoryStream {
	r.t.Helper()
	s := r.ctlStream(r.ctlOpenFrame(id, true, cols, rows))
	s.opened(r.t)
	return s
}

// ctlPlainSession opens a session without a terminal and waits for its Opened.
func (r *memoryRig) ctlPlainSession(id string) *memoryStream {
	r.t.Helper()
	s := r.ctlStream(r.ctlOpenFrame(id, false, 0, 0))
	s.opened(r.t)
	return s
}

// ctlSend puts frames on the stream the host sends. The stream takes sixteen; the server's receiver takes them as it runs, so a
// test that sends more waits for it in synctest.Wait.
func (s *memoryStream) ctlSend(frames ...*execv2.ClientFrame) {
	for _, f := range frames {
		s.in <- f
	}
}

// ctlFrames returns what the server has sent and the test has not read, and does not wait for more.
func (s *memoryStream) ctlFrames() []*execv2.ServerFrame {
	var frames []*execv2.ServerFrame
	for {
		select {
		case f := <-s.out:
			frames = append(frames, f)
		default:
			return frames
		}
	}
}

// ctlStatus returns the status the handler of Open ended the stream with, or nil when it has not returned or returned none: a stream
// that ended with Ended returns nil, as the server's handler does.
func (s *memoryStream) ctlStatus() (returned bool, err error) {
	select {
	case err := <-s.returned:
		return true, err
	default:
		return false, nil
	}
}

// ctlClass names the class of the refusal a status carries ("INVALID_REQUEST") and its reason, if there is one: what a host reads
// from the stream's last word.
func ctlClass(err error) (class, reason string) {
	st, ok := status.FromError(err)
	if !ok {
		return "not a status: " + err.Error(), ""
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info.GetReason(), info.GetMetadata()["reason"]
		}
	}
	return "a status with no ErrorInfo: " + st.Code().String(), ""
}

// ctlLeave ends the host's side of the stream and lets the server wind the session down: the way every bubble test ends, so that no
// goroutine is left in the bubble.
func (s *memoryStream) ctlLeave() {
	s.hostLeaves()
	synctest.Wait()
}

// the host's frames

func ctlResize(seq, cols, rows uint32) *execv2.ClientFrame {
	return &execv2.ClientFrame{Frame: &execv2.ClientFrame_Resize{Resize: &execv2.Resize{Seq: seq, Size: &execv2.Size{Cols: cols, Rows: rows}}}}
}

func ctlSignal(sig execv2.Signal) *execv2.ClientFrame {
	return &execv2.ClientFrame{Frame: &execv2.ClientFrame_Signal{Signal: sig}}
}

func ctlCloseInput() *execv2.ClientFrame {
	return &execv2.ClientFrame{Frame: &execv2.ClientFrame_CloseInput{CloseInput: &execv2.CloseInput{}}}
}

func ctlInput(data string) *execv2.ClientFrame {
	return &execv2.ClientFrame{Frame: &execv2.ClientFrame_Input{Input: []byte(data)}}
}

func ctlQuery(id uint32, kind execv2.FactKind) *execv2.ClientFrame {
	return &execv2.ClientFrame{Frame: &execv2.ClientFrame_Query{Query: &execv2.Query{Id: id, Kind: kind}}}
}

func ctlClose(mode execv2.CloseMode) *execv2.ClientFrame {
	return &execv2.ClientFrame{Frame: &execv2.ClientFrame_Close{Close: &execv2.Close{Mode: mode}}}
}

// the frames of the server, by kind

// ctlResized returns the Resized frames among frames, in order.
func ctlResized(frames []*execv2.ServerFrame) []*execv2.Resized {
	var out []*execv2.Resized
	for _, f := range frames {
		if r := f.GetResized(); r != nil {
			out = append(out, r)
		}
	}
	return out
}

// ctlFacts returns the Fact frames among frames, in order.
func ctlFacts(frames []*execv2.ServerFrame) []*execv2.Fact {
	var out []*execv2.Fact
	for _, f := range frames {
		if fact := f.GetFact(); fact != nil {
			out = append(out, fact)
		}
	}
	return out
}

// ctlEnded returns the Ended among frames, or nil.
func ctlEnded(frames []*execv2.ServerFrame) *execv2.Ended {
	for _, f := range frames {
		if e := f.GetEnded(); e != nil {
			return e
		}
	}
	return nil
}

// ctlTarget is the fake target with its capabilities changed by mods, whose function is fn.
func ctlTarget(fn exec.ExecFunc, mods ...func(*exec.Capabilities)) exec.Target {
	return engTarget(fn, func(i *exec.TargetInfo) {
		for _, mod := range mods {
			mod(&i.Capabilities)
		}
	})
}

// ctlHold is a function that ends when its session does, after it ran fn.
func ctlHold(fn func(p *exec.Proc)) exec.ExecFunc {
	return func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		fn(p)
		<-ctx.Done()
		return exec.Exit{}, ctx.Err()
	}
}

// ctlGate returns a gate that is open when the test ends, so that no function of the bubble waits for it for ever.
func ctlGate(t testing.TB) *exectest.Gate {
	g := exectest.NewGate()
	t.Cleanup(g.Open)
	return g
}
