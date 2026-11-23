package exec_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/exectestkit"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// wireRig serves targets as a plugin over a real transport (a unix socket) behind the SDK's own middleware, with a fake
// connection registry, and dials them the way the host does: what a host and a plugin exchange for an interactive session,
// without the child process. The targets are the fake's: provider "fake", type "thing", resource "thing-1".
type wireRig struct {
	t      testing.TB
	conns  *exectestkit.Connections
	server *exectestkit.Server
	client exec.Client
}

// newWireRig serves targets. Everything is torn down when the test ends.
func newWireRig(t testing.TB, targets ...exec.Target) *wireRig {
	t.Helper()
	conns := exectestkit.NewConnectionsT(t, "fake")
	server := exectestkit.Serve(t, exectestkit.ServeConfig{Targets: targets, Connections: conns})
	return &wireRig{t: t, conns: conns, server: server, client: server.V2(t)}
}

// request is the context a host's call arrives with: the connection and the catalog pin.
func (r *wireRig) request(ctx context.Context) context.Context { return r.conns.Context(ctx) }

// openRequest is the first frame of an Open for the fake target's thing, a terminal by default.
func (r *wireRig) openRequest(id string, mods ...func(*execv2.Start)) *execv2.OpenRequest {
	start := &execv2.Start{Target: r.conns.WireTarget(r.t, "thing", "thing-1"), Tty: true}
	for _, mod := range mods {
		mod(start)
	}
	return &execv2.OpenRequest{SessionId: id, Kind: &execv2.OpenRequest_Start{Start: start}}
}

// open opens a session over the wire.
func (r *wireRig) open(ctx context.Context, id string, mods ...func(*execv2.Start)) (exec.Conn, error) {
	return r.client.Open(r.request(ctx), r.openRequest(id, mods...))
}

// mustOpen opens a session that has to be accepted, and closes its stream when the test ends.
func (r *wireRig) mustOpen(id string, mods ...func(*execv2.Start)) exec.Conn {
	r.t.Helper()
	conn, err := r.open(context.Background(), id, mods...)
	if err != nil {
		r.t.Fatalf("open %q: %v", id, err)
	}
	r.t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// sendInput sends bytes as one input frame.
func sendInput(t testing.TB, conn exec.Conn, data string) {
	t.Helper()
	if err := conn.Send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Input{Input: []byte(data)}}); err != nil {
		t.Fatalf("send input: %v", err)
	}
}

// sendClose asks for the end of the session.
func sendClose(t testing.TB, conn exec.Conn, mode execv2.CloseMode) {
	t.Helper()
	if err := conn.Send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Close{Close: &execv2.Close{Mode: mode}}}); err != nil {
		t.Fatalf("send close: %v", err)
	}
}

// frameKind names the kind of a frame, for a message.
func frameKind(f *execv2.ServerFrame) string {
	switch f.GetFrame().(type) {
	case *execv2.ServerFrame_Opened:
		return "opened"
	case *execv2.ServerFrame_Output:
		return "output"
	case *execv2.ServerFrame_Resized:
		return "resized"
	case *execv2.ServerFrame_Mark:
		return "mark"
	case *execv2.ServerFrame_Fact:
		return "fact"
	case *execv2.ServerFrame_Dropped:
		return "dropped"
	case *execv2.ServerFrame_Notice:
		return "notice"
	case *execv2.ServerFrame_Ended:
		return "ended"
	case *execv2.ServerFrame_Started:
		return "started"
	case *execv2.ServerFrame_Link:
		return "link"
	case *execv2.ServerFrame_Detached:
		return "detached"
	}
	return "unknown"
}

// recvFrame reads the next frame within the wait bound.
func recvFrame(t testing.TB, conn exec.Conn) (*execv2.ServerFrame, error) {
	t.Helper()
	type received struct {
		frame *execv2.ServerFrame
		err   error
	}
	got := make(chan received, 1)
	go func() {
		frame, err := conn.Recv()
		got <- received{frame, err}
	}()
	select {
	case r := <-got:
		return r.frame, r.err
	case <-time.After(engineWait):
		t.Fatalf("no frame arrived within %v", engineWait)
		return nil, nil
	}
}

// readUntilEnded reads every frame of the stream to its end: the frames in order, and the Ended they ended with. It fails
// when anything follows Ended, when the stream does not end with io.EOF, and when it ends without an Ended.
func readUntilEnded(t testing.TB, conn exec.Conn) ([]*execv2.ServerFrame, *execv2.Ended) {
	t.Helper()
	var frames []*execv2.ServerFrame
	for {
		frame, err := recvFrame(t, conn)
		if errors.Is(err, io.EOF) {
			t.Fatalf("the stream ended without an Ended after %d frames", len(frames))
		}
		if err != nil {
			t.Fatalf("recv after %d frames: %v", len(frames), err)
		}
		frames = append(frames, frame)
		if ended := frame.GetEnded(); ended != nil {
			if frame, err := recvFrame(t, conn); !errors.Is(err, io.EOF) {
				t.Fatalf("after Ended: %s frame and %v, want io.EOF: nothing follows Ended", frameKind(frame), err)
			}
			return frames, ended
		}
	}
}
