package exec_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"

	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// What the in-memory streams of open_memory_rig_test.go lack for sessions that outlive their stream: an attach, a start with
// settings of its own, a call of the unary RPCs, and a way to read what the server has sent without waiting for it. They are used
// inside a synctest bubble, where the clock is the bubble's: the lease of a detached session and the age of a tombstone are then
// decided by time.Sleep, which moves the clock exactly when every goroutine waits, and no test decides anything by a real wait.

// startFrame is the first frame of an Open for the fake target's thing, without a terminal, with the settings the mods make.
func (r *memoryRig) startFrame(id string, mods ...func(*execv2.Start)) *execv2.ClientFrame {
	start := &execv2.Start{Target: r.conns.WireTarget(r.t, "thing", "thing-1")}
	for _, mod := range mods {
		mod(start)
	}
	return &execv2.ClientFrame{Frame: &execv2.ClientFrame_Open{Open: &execv2.OpenRequest{SessionId: id, Kind: &execv2.OpenRequest_Start{Start: start}}}}
}

// attachFrame is the first frame of an Open that attaches to a session from an offset.
func (r *memoryRig) attachFrame(id string, from uint64) *execv2.ClientFrame {
	return &execv2.ClientFrame{Frame: &execv2.ClientFrame_Open{Open: &execv2.OpenRequest{
		SessionId: id, Kind: &execv2.OpenRequest_Attach{Attach: &execv2.Attach{FromOffset: from}},
	}}}
}

// callContext is the context a unary call arrives with at the plugin: the plugin context of the call and the catalog pin, as the
// SDK's interceptors make them from the metadata the host's client sent.
func (r *memoryRig) callContext() context.Context {
	r.t.Helper()
	call := r.conns.Context(context.Background())
	out, err := resourceplugin.OutgoingSessionContext(call)
	if err != nil {
		r.t.Fatalf("the host's context: %v", err)
	}
	md, _ := metadata.FromOutgoingContext(out)
	return metadata.NewIncomingContext(call, md)
}

// streamOf is stream for any first frame: a start with settings, or an attach.
func (r *memoryRig) streamOf(first *execv2.ClientFrame, endedGate chan struct{}) *memoryStream {
	r.t.Helper()
	return r.streamGated(first, endedGate, nil)
}

// streamHeld is streamOf for a host that takes no output until the test opens the gate: as a host whose window is full.
func (r *memoryRig) streamHeld(first *execv2.ClientFrame, outputGate chan struct{}) *memoryStream {
	r.t.Helper()
	return r.streamGated(first, nil, outputGate)
}

func (r *memoryRig) streamGated(first *execv2.ClientFrame, endedGate, outputGate chan struct{}) *memoryStream {
	r.t.Helper()
	ctx, cancel := context.WithCancel(r.callContext())
	s := &memoryStream{
		ctx: ctx, cancel: cancel, endedGate: endedGate, outputGate: outputGate,
		in: make(chan *execv2.ClientFrame, 16), out: make(chan *execv2.ServerFrame, 256), returned: make(chan error, 1),
	}
	s.in <- first
	go func() { s.returned <- r.server.Open(s) }()
	return s
}

// streamOpenedHeld is a stream of a host that takes nothing, not even the Opened frame, until the test lets it: the handler waits in
// that first send, before the stream's pump, receiver and serve loop exist, so that the stream holds what it was given (a replay) and
// has not begun to read it, and no goroutine of the stream waits for the lock of the sends that the held one holds.
func (r *memoryRig) streamOpenedHeld(first *execv2.ClientFrame, openedGate chan struct{}) *memoryStream {
	r.t.Helper()
	ctx, cancel := context.WithCancel(r.callContext())
	s := &memoryStream{
		ctx: ctx, cancel: cancel, openedGate: openedGate,
		in: make(chan *execv2.ClientFrame, 16), out: make(chan *execv2.ServerFrame, 256), returned: make(chan error, 1),
	}
	s.in <- first
	go func() { s.returned <- r.server.Open(s) }()
	return s
}

// send puts a frame on the stream as the host.
func (s *memoryStream) send(frame *execv2.ClientFrame) { s.in <- frame }

// sendDetachFrame asks to leave for a lease.
func (s *memoryStream) sendDetachFrame(leaseMs uint32) {
	s.send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Detach{Detach: &execv2.Detach{LeaseMs: leaseMs}}})
}

// sendCloseFrame asks for the end of the session.
func (s *memoryStream) sendCloseFrame(mode execv2.CloseMode) {
	s.send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Close{Close: &execv2.Close{Mode: mode}}})
}

// frames returns what the server has sent and the test has not read, without waiting for more.
func (s *memoryStream) frames() []*execv2.ServerFrame {
	var frames []*execv2.ServerFrame
	for {
		select {
		case frame := <-s.out:
			frames = append(frames, frame)
		default:
			return frames
		}
	}
}

// handlerReturned reports, without waiting, whether the handler of Open has returned, and what it returned.
func (s *memoryStream) handlerReturned() (error, bool) {
	select {
	case err := <-s.returned:
		return err, true
	default:
		return nil, false
	}
}

// listSessions calls ListSessions as a host does.
func (r *memoryRig) listSessions(t testing.TB) []*execv2.SessionInfo {
	t.Helper()
	resp, err := r.server.ListSessions(r.callContext(), &execv2.ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	return resp.GetSessions()
}

// sessionInfo is the entry of a session in ListSessions, which has to be there.
func (r *memoryRig) sessionInfo(t testing.TB, id string) *execv2.SessionInfo {
	t.Helper()
	for _, info := range r.listSessions(t) {
		if info.GetFacts().GetSessionId() == id {
			return info
		}
	}
	t.Fatalf("ListSessions has no session %q", id)
	return nil
}

// hasSession reports whether ListSessions lists the session.
func (r *memoryRig) hasSession(t testing.TB, id string) bool {
	t.Helper()
	for _, info := range r.listSessions(t) {
		if info.GetFacts().GetSessionId() == id {
			return true
		}
	}
	return false
}

// live reports whether the engine holds the session as a live command (an ended session that a tombstone holds is not live).
func (r *memoryRig) live(id string) bool {
	for _, live := range exec.PluginLive(r.plugin) {
		if live == id {
			return true
		}
	}
	return false
}

// lastOf returns the last frame of a list of frames, or nil.
func lastOf(frames []*execv2.ServerFrame) *execv2.ServerFrame {
	if len(frames) == 0 {
		return nil
	}
	return frames[len(frames)-1]
}

// bubbleNow is the clock of the bubble in Unix milliseconds, which is the clock of the engine inside it.
func bubbleNow() int64 { return time.Now().UnixMilli() }
