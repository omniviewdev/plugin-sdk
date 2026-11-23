package exec_test

import (
	"context"
	"io"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/omniviewdev/plugin-sdk/internal/exectestkit"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The tests that need to hold the server between two events, or to see what it does while nothing happens, drive the handler of Open
// with a stream that lives in memory, inside a synctest bubble: no transport, no socket, and a clock that moves only when every
// goroutine of the bubble waits. What the server does without a goroutine running has been done when synctest.Wait returns, so a
// test asks whether something happened and is answered, and decides nothing by waiting.

// memoryStream is the server's side of an Open stream: what the host sends is what the test puts on in, and what the server sends is
// what the test finds on out. A stream made with holdEnded does not take the Ended frame until the test lets it, as a host whose
// window is full does not.
type memoryStream struct {
	grpc.ServerStream
	ctx    context.Context
	cancel context.CancelFunc
	in     chan *execv2.ClientFrame
	out    chan *execv2.ServerFrame
	// endedGate, when set, is what the stream's Send of an Ended waits for. outputGate, when set, is what its Send of an Output
	// frame waits for: a host that takes no output, as one whose window is full. startedGate is the same for a Started frame: the
	// goroutine that sends the session's events is held in it, and the frames that follow wait behind it. openedGate is the same for
	// the Opened frame, which is the first send of the stream: the handler is held in it before the stream's other goroutines exist.
	//
	// A send that is held holds the lock that all the sends of the stream share, and a goroutine that waits for a lock is not
	// durably blocked: synctest.Wait does not return while one does. So a test holds a send only when no other goroutine of the stream
	// has anything to send behind it (the events go out before the first output is held, see the tests that use outputGate), or while
	// no other goroutine of the stream exists (openedGate).
	endedGate   chan struct{}
	outputGate  chan struct{}
	startedGate chan struct{}
	openedGate  chan struct{}
	leaveOnce   sync.Once
	// returned receives what the handler of Open returned.
	returned chan error
}

func (s *memoryStream) Context() context.Context { return s.ctx }

func (s *memoryStream) Recv() (*execv2.ClientFrame, error) {
	select {
	case frame, ok := <-s.in:
		if !ok {
			return nil, io.EOF
		}
		return frame, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}

func (s *memoryStream) Send(frame *execv2.ServerFrame) error {
	if frame.GetOpened() != nil && s.openedGate != nil {
		select {
		case <-s.openedGate:
		case <-s.ctx.Done():
			return s.ctx.Err()
		}
	}
	if frame.GetEnded() != nil && s.endedGate != nil {
		select {
		case <-s.endedGate:
		case <-s.ctx.Done():
			return s.ctx.Err()
		}
	}
	if frame.GetOutput() != nil && s.outputGate != nil {
		select {
		case <-s.outputGate:
		case <-s.ctx.Done():
			return s.ctx.Err()
		}
	}
	if frame.GetStarted() != nil && s.startedGate != nil {
		select {
		case <-s.startedGate:
		case <-s.ctx.Done():
			return s.ctx.Err()
		}
	}
	select {
	case s.out <- frame:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

// hostLeaves ends the host's side of the stream: the server's Recv returns io.EOF, as it does for a host that half-closed.
func (s *memoryStream) hostLeaves() { s.leaveOnce.Do(func() { close(s.in) }) }

// opened waits for the first frame of the stream, which has to be Opened: a handler that returned before it refused the open.
func (s *memoryStream) opened(t testing.TB) {
	t.Helper()
	select {
	case frame := <-s.out:
		if frame.GetOpened() == nil {
			t.Fatalf("the first frame of the stream is %v, want Opened", frame)
		}
	case err := <-s.returned:
		t.Fatalf("the open was refused: %v", err)
	}
}

// memoryRig is a plugin with a fake connection registry whose Open handler the test calls itself.
type memoryRig struct {
	t      testing.TB
	conns  *exectestkit.Connections
	plugin *exec.Plugin
	server execv2.ExecServer
}

func newMemoryRig(t testing.TB, targets ...exec.Target) *memoryRig {
	t.Helper()
	conns := exectestkit.NewConnectionsT(t, "fake")
	capability, err := exec.NewTargetPlugin(targets)
	if err != nil {
		t.Fatalf("build the plugin: %v", err)
	}
	capability.BindConnectionContextProvider(conns)
	return &memoryRig{t: t, conns: conns, plugin: capability, server: exec.OpenServer(capability)}
}

// openFrame is the first frame of an Open for the fake target's thing, without a terminal.
func (r *memoryRig) openFrame(id string) *execv2.ClientFrame {
	start := &execv2.Start{Target: r.conns.WireTarget(r.t, "thing", "thing-1")}
	return &execv2.ClientFrame{Frame: &execv2.ClientFrame_Open{Open: &execv2.OpenRequest{
		SessionId: id, Kind: &execv2.OpenRequest_Start{Start: start},
	}}}
}

// stream makes a stream whose host has sent the open frame of the session, and calls the handler of Open with it in the background.
// The stream's context is the one a call arrives with at the plugin: the plugin context of the call (its connection), which the
// SDK's interceptors build from metadata, and the catalog pin, which the handler reads from the metadata the host's client sent.
func (r *memoryRig) stream(id string, endedGate chan struct{}) *memoryStream {
	r.t.Helper()
	call := r.conns.Context(context.Background())
	out, err := resourceplugin.OutgoingSessionContext(call)
	if err != nil {
		r.t.Fatalf("the host's context: %v", err)
	}
	md, _ := metadata.FromOutgoingContext(out)
	ctx, cancel := context.WithCancel(metadata.NewIncomingContext(call, md))
	s := &memoryStream{
		ctx: ctx, cancel: cancel, endedGate: endedGate,
		in: make(chan *execv2.ClientFrame, 16), out: make(chan *execv2.ServerFrame, 64), returned: make(chan error, 1),
	}
	s.in <- r.openFrame(id)
	go func() { s.returned <- r.server.Open(s) }()
	return s
}
