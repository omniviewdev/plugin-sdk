package exec_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/internal/lifecycletest"
	logging "github.com/omniviewdev/plugin-sdk/log"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The defects of the legacy lane, rewritten as properties of the construction (plan section 3.4 and 3.6): output is queued in the
// session and read in order, whatever the author and the host do first; there is no attach step; a stream is the session's
// life; ids belong to the plugin and not to a connection.

// tailAuthor writes the bytes its label "bytes" names at once, and returns. It tells its worker, through a channel of its own,
// that its function has returned.
type tailAuthor struct{ returned []chan struct{} }

func (a *tailAuthor) run(_ context.Context, p *exec.Proc) (exec.Exit, error) {
	worker, _ := strconv.Atoi(p.Request.Labels["w"])
	size, _ := strconv.Atoi(p.Request.Labels["bytes"])
	defer func() { a.returned[worker] <- struct{}{} }()
	if _, err := p.Stdout.Write(proofChunk(0, size)); err != nil {
		return exec.Exit{}, err
	}
	return exec.ExitCode(0), nil
}

// commandsThatEndAtOnce runs sessions of a command that writes size bytes and exits at once, over the wire, from 8 workers, and
// returns what the hosts got: how many lost a byte, and why. waitForReturn makes the host start to read only after the function
// returned, which is the tail of the baseline; without it the host reads as soon as Open returns, which is the attach of the
// baseline.
func commandsThatEndAtOnce(t *testing.T, sessions, size int, waitForReturn func(i int) bool) (lost int, reasons []string) {
	t.Helper()
	const workers = 8
	author := &tailAuthor{returned: make([]chan struct{}, workers)}
	for i := range author.returned {
		author.returned[i] = make(chan struct{}, 1)
	}
	rig := newProofRig(t, proofTarget("tail", author.run))
	type verdict struct{ problem string }
	verdicts := make(chan verdict, sessions)
	for w := range workers {
		go func() {
			for i := w; i < sessions; i += workers {
				id := fmt.Sprintf("tail-%d", i)
				conn, err := rig.openKind(context.Background(), "tail", id, proofLabels("w", strconv.Itoa(w), "bytes", strconv.Itoa(size)))
				if err != nil {
					verdicts <- verdict{fmt.Sprintf("%s: open: %v", id, err)}
					continue
				}
				returned := false
				if waitForReturn(i) {
					select {
					case <-author.returned[w]:
						returned = true
					case <-time.After(engineWait):
						verdicts <- verdict{id + ": the function did not return"}
						_ = conn.Close()
						return
					}
				}
				audit := &proofAudit{verify: true}
				err = proofDrain(conn, audit, nil)
				if !returned {
					<-author.returned[w]
				}
				_ = conn.Close()
				var problem string
				switch {
				case !errors.Is(err, io.EOF):
					problem = fmt.Sprintf("the stream ended with %v", err)
				case len(audit.problems) > 0:
					problem = fmt.Sprint(audit.problems)
				case audit.read != uint64(size) || audit.ended.GetBytesOut() != uint64(size) || audit.ended.GetDroppedBytes() != 0:
					problem = fmt.Sprintf("read %d bytes, Ended says %d out and %d dropped, want %d", audit.read, audit.ended.GetBytesOut(), audit.ended.GetDroppedBytes(), size)
				case audit.ended.GetEnd().GetExited() == nil || audit.ended.GetOutput() != execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE:
					problem = fmt.Sprintf("ended %v with output %v, want exit 0 and COMPLETE", audit.ended.GetEnd(), audit.ended.GetOutput())
				}
				verdicts <- verdict{problem}
			}
		}()
	}
	for range sessions {
		select {
		case v := <-verdicts:
			if v.problem != "" {
				lost++
				if len(reasons) < 5 {
					reasons = append(reasons, v.problem)
				}
			}
		case <-time.After(engineWait):
			t.Fatalf("the sessions did not finish within %v", engineWait)
		}
	}
	return lost, reasons
}

// TAIL: a command writes 64 KiB and exits at once, and the host starts reading only after its function returned. The baseline lost
// bytes in 22 to 54 of 60 runs; here the output waits in the session, and all of it arrives, in order, before Ended, in every one.
func TestOpenConstructionTailOf64KiBAfterAnImmediateExitArrivesWholeInOrderBeforeEnded(t *testing.T) {
	sessions := proofScaled(1000, 100)
	lost, reasons := commandsThatEndAtOnce(t, sessions, 64<<10, func(int) bool { return true })
	t.Logf("%d of %d sessions lost bytes of 65,536 (the baseline: 22 to 54 of 60)", lost, sessions)
	if lost != 0 {
		t.Errorf("%d of %d sessions did not deliver all 65,536 bytes in order before Ended: %v", lost, sessions, reasons)
	}
}

// SHORT COMMAND: 2 KiB and an immediate exit. There is no attach step: Open returns Opened, the session exists, and the output is
// queued until it is read, whether the host reads at once or after the function returned. The baseline lost everything in 47 to 51
// of 60 runs.
func TestOpenConstructionShortCommandOf2KiBLosesNoByteWhateverTheHostDoesFirst(t *testing.T) {
	sessions := proofScaled(1000, 100)
	lost, reasons := commandsThatEndAtOnce(t, sessions, 2<<10, func(i int) bool { return i%2 == 0 })
	t.Logf("%d of %d sessions lost a byte of 2,048 (the baseline: 47 to 51 of 60 gone before the attach)", lost, sessions)
	if lost != 0 {
		t.Errorf("%d of %d sessions did not deliver all 2,048 bytes: %v", lost, sessions, reasons)
	}
}

// secondConnection dials the served plugin again, as a second host connection, and returns its client and the connection. The options that are
// given are added to the dial options, and one that sets the same thing replaces it.
func secondConnection(t *testing.T, rig *proofRig, extra ...grpc.DialOption) (exec.Client, *grpc.ClientConn) {
	t.Helper()
	options := append(sdk.GRPCDialOptionsWithLogger(logging.NewNop()), grpc.WithTransportCredentials(insecure.NewCredentials()))
	conn, err := grpc.NewClient(rig.server.Conn().Target(), append(options, extra...)...)
	if err != nil {
		t.Fatalf("dial the second connection: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client, err := (&exec.Plugin{}).GRPCClient(context.Background(), nil, conn)
	if err != nil {
		t.Fatalf("build the second client: %v", err)
	}
	return client.(*exec.PluginClient).V2(), conn
}

// SECOND CONNECTION: two hosts' connections to one plugin run sessions at once and independently, and ids belong to the plugin: a
// live id is refused on the other connection; breaking one connection ends its sessions and frees their ids and leaves the other
// connection's sessions running.
func TestOpenConstructionTwoConnectionsRunSessionsIndependentlyAndIdsArePerPlugin(t *testing.T) {
	rig := newProofRig(t, proofTarget("echo", proofEchoFunction))
	second, secondConn := secondConnection(t, rig)
	// Both connections are up before the baseline: a connection is dialed by its first call, and its goroutines are not a leak.
	for i, client := range []exec.Client{rig.client, second} {
		id := fmt.Sprintf("warm-%d", i) // the id of a session that ended is held by its tombstone: one for each connection
		warm, err := rig.openOn(context.Background(), client, "echo", id, proofTTY)
		if err != nil {
			t.Fatalf("warm-up: %v", err)
		}
		if _, err := newProofEchoer(warm).measure(2); err != nil {
			t.Fatalf("warm-up: %v", err)
		}
		_ = warm.Close() // a host that closes its stream detaches: the session is ended here, and its function returns
		proofEndDetached(t, client, id)
	}
	baseline := lifecycletest.TakeBaseline()

	first1, err := rig.openKind(context.Background(), "echo", "shared", proofTTY)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first1.Close() }()
	if _, err := rig.openOn(context.Background(), second, "echo", "shared"); proofRefusal(err) != "CONFLICT/SESSION_EXISTS" {
		t.Fatalf("a live id opened on the other connection: %v, want CONFLICT/SESSION_EXISTS", err)
	}

	// Many sessions on each connection at once, each echoing its own bytes.
	const perConnection = 12
	type echoes struct {
		conn exec.Conn
		echo *proofEchoer
	}
	var onFirst, onSecond []echoes
	for i := range perConnection {
		c1, err := rig.openKind(context.Background(), "echo", fmt.Sprintf("one-%d", i), proofTTY)
		if err != nil {
			t.Fatal(err)
		}
		c2, err := rig.openOn(context.Background(), second, "echo", fmt.Sprintf("two-%d", i), proofTTY)
		if err != nil {
			t.Fatal(err)
		}
		onFirst, onSecond = append(onFirst, echoes{c1, newProofEchoer(c1)}), append(onSecond, echoes{c2, newProofEchoer(c2)})
	}
	roundTrips := proofScaled(200, 40)
	results := make(chan error, 2*perConnection)
	for _, group := range [][]echoes{onFirst, onSecond} {
		for _, e := range group {
			go func() {
				_, err := e.echo.measure(roundTrips)
				results <- err
			}()
		}
	}
	for range 2 * perConnection {
		if err := <-results; err != nil {
			t.Errorf("a session on one of two connections: %v", err)
		}
	}

	// The second connection breaks: a host that leaves detaches, so its sessions live on with no stream attached (they are ended
	// here, from the first connection, so that nothing is left behind), and the first connection's sessions go on.
	if err := secondConn.Close(); err != nil {
		t.Fatal(err)
	}
	for i, e := range onSecond {
		if _, err := e.conn.Recv(); err == nil {
			t.Errorf("session two-%d: a frame arrived on a broken connection", i)
		}
	}
	for i := range perConnection {
		proofEndDetached(t, rig.client, fmt.Sprintf("two-%d", i))
	}
	for i, e := range onFirst {
		if _, err := e.echo.measure(3); err != nil {
			t.Errorf("session one-%d, on the connection that did not break, stopped echoing: %v", i, err)
		}
	}
	for _, e := range onFirst {
		_ = e.conn.Close()
	}
	_ = first1.Close()
	for i := range perConnection {
		proofEndDetached(t, rig.client, fmt.Sprintf("one-%d", i))
	}
	proofEndDetached(t, rig.client, "shared")
	if err := baseline.Check(10 * time.Second); err != nil {
		t.Errorf("goroutines after two connections: %v", err)
	}
}

// watcher is a function that tells when its context ended: what a host that leaves is seen to do to the function of its session.
type watcher struct{ cancelled chan string }

func (w *watcher) run(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
	id := p.Request.Labels["sid"]
	if _, err := p.Stdout.Write([]byte("up")); err != nil {
		return exec.Exit{}, err
	}
	<-ctx.Done()
	w.cancelled <- id
	return exec.Exit{}, ctx.Err()
}

// proofListed is the entry of a session in ListSessions, or nil.
func proofListed(client exec.Client, id string) *execv2.SessionInfo {
	list, err := client.ListSessions(context.Background())
	if err != nil {
		return nil
	}
	for _, info := range list.GetSessions() {
		if info.GetFacts().GetSessionId() == id {
			return info
		}
	}
	return nil
}

// proofDetached waits for the session to be live with no stream attached.
func proofDetached(client exec.Client, id string) bool {
	return proofPoll(engineWait, func() bool {
		info := proofListed(client, id)
		return info != nil && info.GetEndedWith() == nil && !info.GetAttached()
	})
}

// proofEndDetached ends a session whose host left, with a kill: what a test does so that no function or goroutine outlives it.
func proofEndDetached(t *testing.T, client exec.Client, id string) {
	t.Helper()
	if !proofDetached(client, id) {
		t.Errorf("the session %q is not live with no stream attached after its host left", id)
		return
	}
	if err := client.EndSession(context.Background(), &execv2.EndSessionRequest{SessionId: id, Mode: execv2.CloseMode_CLOSE_MODE_KILL}); err != nil {
		t.Errorf("EndSession(%q): %v", id, err)
	}
}

// STREAM END: a host that leaves detaches, it does not end its session. A host that cancels, closes its stream, closes its sending
// side or loses its connection leaves the session running for its lease: its function is not told, ListSessions shows it with no
// stream attached, and EndSession is what cancels the function, frees its place and leaves nothing behind. A host that loses its
// connection in the middle of a flood does not leave the function held in a write: a session with no stream drops what no one reads.
func TestOpenConstructionAHostThatLeavesDetachesItsSessionAndEndSessionLeavesNoGoroutine(t *testing.T) {
	w := &watcher{cancelled: make(chan string, 8)}
	returned := make(chan struct{}, 4)
	rig := newProofRig(t,
		proofTarget("watch", w.run),
		proofTarget("flood", func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			defer func() { returned <- struct{}{} }()
			for offset := uint64(0); ; offset += floodChunk {
				if err := ctx.Err(); err != nil {
					return exec.Exit{}, err
				}
				if _, err := p.Stdout.Write(proofChunk(offset, floodChunk)); err != nil {
					return exec.Exit{}, err
				}
			}
		}),
	)
	second, secondConn := secondConnection(t, rig)
	warm := rig.mustOpenKind("watch", "warm", proofLabels("sid", "warm"))
	if _, err := recvFrame(t, warm); err != nil {
		t.Fatal(err)
	}
	_ = warm.Close()
	proofEndDetached(t, rig.client, "warm")
	<-w.cancelled
	baseline := lifecycletest.TakeBaseline()

	// ended checks what a host that left did to its session: it is detached and its function was not told, and EndSession ends it.
	ended := func(id string) {
		t.Helper()
		if !proofDetached(rig.client, id) {
			t.Fatalf("the session %q is not live with no stream attached after its host left", id)
		}
		select {
		case got := <-w.cancelled:
			t.Fatalf("the function of %q saw its context end when its host left: a host that leaves detaches", got)
		default:
		}
		proofEndDetached(t, rig.client, id)
		select {
		case got := <-w.cancelled:
			if got != id {
				t.Errorf("the function of %q saw its context end, want %q", got, id)
			}
		case <-time.After(engineWait):
			t.Fatalf("the function of %q never saw its context end after EndSession", id)
		}
	}

	leave := map[string]func(t *testing.T, id string){
		"the host cancels its context": func(t *testing.T, id string) {
			ctx, cancel := context.WithCancel(context.Background())
			conn, err := rig.openKind(ctx, "watch", id, proofLabels("sid", id))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := recvFrame(t, conn); err != nil {
				t.Fatal(err)
			}
			cancel()
		},
		"the host closes its stream": func(t *testing.T, id string) {
			conn, err := rig.openKind(context.Background(), "watch", id, proofLabels("sid", id))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := recvFrame(t, conn); err != nil {
				t.Fatal(err)
			}
			_ = conn.Close()
		},
		"the host closes only its sending side": func(t *testing.T, id string) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			rctx, err := resourceplugin.OutgoingSessionContext(rig.request(ctx))
			if err != nil {
				t.Fatal(err)
			}
			stream, err := rig.server.Raw().Open(rctx)
			if err != nil {
				t.Fatal(err)
			}
			start := &execv2.Start{Target: proto.Clone(rig.refs["watch"]).(*execv2.TargetRef), Labels: []*execv2.NameValue{{Name: "sid", Value: id}}}
			open := &execv2.OpenRequest{SessionId: id, Kind: &execv2.OpenRequest_Start{Start: start}}
			if err := stream.Send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Open{Open: open}}); err != nil {
				t.Fatal(err)
			}
			if frame, err := stream.Recv(); err != nil || frame.GetOpened() == nil {
				t.Fatalf("the first frame is %v, %v, want Opened", frame, err)
			}
			if err := stream.CloseSend(); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, how := range leave {
		t.Run(name, func(t *testing.T) {
			id := "left-" + strconv.Itoa(len(name))
			how(t, id)
			ended(id)
		})
	}

	t.Run("the host's connection breaks", func(t *testing.T) {
		conn, err := rig.openOn(context.Background(), second, "watch", "broken", proofLabels("sid", "broken"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := recvFrame(t, conn); err != nil {
			t.Fatal(err)
		}
		_ = secondConn.Close()
		ended("broken")
	})

	t.Run("the host's connection breaks in the middle of a flood", func(t *testing.T) {
		third, thirdConn := secondConnection(t, rig)
		conn, err := rig.openOn(context.Background(), third, "flood", "flooded")
		if err != nil {
			t.Fatal(err)
		}
		for range 10 {
			if _, err := recvFrame(t, conn); err != nil {
				t.Fatal(err)
			}
		}
		_ = thirdConn.Close()
		if !proofDetached(rig.client, "flooded") {
			t.Fatal("the session of the flood is not live with no stream attached after its host's connection broke")
		}
		// Nothing reads the output and nothing holds the function in a write: the output of the session goes on growing.
		from := proofListed(rig.client, "flooded").GetNextOffset()
		if !proofPoll(engineWait, func() bool { return proofListed(rig.client, "flooded").GetNextOffset() > from+4<<20 }) {
			t.Fatal("the function of the flood is held in a write after its host's connection broke")
		}
		proofEndDetached(t, rig.client, "flooded")
		awaitChannel(t, func() <-chan struct{} {
			c := make(chan struct{})
			go func() { <-returned; close(c) }()
			return c
		}(), "the function of the flood to return after EndSession")
	})

	if err := baseline.Check(10 * time.Second); err != nil {
		t.Errorf("goroutines after the hosts left: %v", err)
	}
}
