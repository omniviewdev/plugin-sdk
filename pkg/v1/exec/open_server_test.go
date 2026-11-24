package exec_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	sdkdiag "github.com/omniviewdev/plugin-sdk/pkg/diagnostics"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The protocol of one Open stream as the server keeps it. The conformance cases (exectest) hold the contract from the
// author's side; these hold the server's own rules, frame by frame.

// rawCtx is ctx as the SDK's client sends it, with the call's connection and catalog pin in the outgoing metadata, for a test
// that calls the generated client directly.
func (r *wireRig) rawCtx(ctx context.Context) context.Context {
	r.t.Helper()
	out, err := resourceplugin.OutgoingSessionContext(r.conns.Context(ctx))
	if err != nil {
		r.t.Fatal(err)
	}
	return out
}

// rawOpen sends frames on a raw Open stream and returns the first thing the server answers: a frame, or its status.
func (r *wireRig) rawOpen(first *execv2.ClientFrame) (*execv2.ServerFrame, error) {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), engineWait)
	r.t.Cleanup(cancel)
	stream, err := r.server.Raw().Open(r.rawCtx(ctx))
	if err != nil {
		return nil, err
	}
	if err := stream.Send(first); err != nil {
		return nil, err
	}
	return stream.Recv()
}

// A request that is refused is a status and nothing else: the first thing a host reads is the status, never a frame, and
// the author's function never ran.
func TestARefusedOpenIsAStatusWithNoFrameAndNothingRan(t *testing.T) {
	var calls atomic.Int32
	rig := newWireRig(t, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
		calls.Add(1)
		return exec.ExitCode(0), nil
	}))
	start := func(mods ...func(*execv2.Start)) *execv2.ClientFrame {
		return &execv2.ClientFrame{Frame: &execv2.ClientFrame_Open{Open: rig.openRequest("s1", mods...)}}
	}
	unknownField := start()
	unknownField.ProtoReflect().SetUnknown([]byte{0xd8, 0x3e, 0x01}) // an unknown varint field, number 1003
	for name, tc := range map[string]struct {
		frame *execv2.ClientFrame
		code  codes.Code
		class string
	}{
		"the first frame is not an open frame":          {&execv2.ClientFrame{Frame: &execv2.ClientFrame_Input{Input: []byte("x")}}, codes.InvalidArgument, "INVALID_REQUEST"},
		"an empty frame":                                {&execv2.ClientFrame{}, codes.InvalidArgument, "INVALID_REQUEST"},
		"a frame with a field this build does not know": {unknownField, codes.Unimplemented, "UNSUPPORTED"},
		"an attach of an id that is not a session": {
			&execv2.ClientFrame{Frame: &execv2.ClientFrame_Open{Open: &execv2.OpenRequest{SessionId: "s1", Kind: &execv2.OpenRequest_Attach{Attach: &execv2.Attach{}}}}},
			codes.NotFound, "NOT_FOUND",
		},
		"an id that is not printable ASCII": {
			&execv2.ClientFrame{Frame: &execv2.ClientFrame_Open{Open: rig.openRequest("has a space")}}, codes.InvalidArgument, "INVALID_REQUEST",
		},
		"a target nothing is registered for": {
			start(func(s *execv2.Start) { s.Target = rig.conns.WireTarget(t, "nothing", "x-1") }), codes.Unimplemented, "UNSUPPORTED",
		},
	} {
		t.Run(name, func(t *testing.T) {
			frame, err := rig.rawOpen(tc.frame)
			if frame != nil {
				t.Fatalf("a refused Open sent a %s frame before its status", frameKind(frame))
			}
			st, ok := status.FromError(err)
			if !ok || st.Code() != tc.code {
				t.Fatalf("status %v, want code %v", err, tc.code)
			}
		})
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("the function ran %d times for requests that were refused", n)
	}
}

// A second open frame is a protocol violation: the stream ends with a typed status and the session ends with it.
func TestASecondOpenFrameEndsTheStreamAndTheSession(t *testing.T) {
	cancelled := exectest.NewGate()
	rig := newWireRig(t, engTarget(func(ctx context.Context, _ *exec.Proc) (exec.Exit, error) {
		<-ctx.Done()
		cancelled.Open()
		return exec.Exit{}, ctx.Err()
	}))
	conn := rig.mustOpen("s1", withoutTTY)
	if err := conn.Send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Open{Open: rig.openRequest("s2")}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	var last error
	for {
		frame, err := recvFrame(t, conn)
		if err != nil {
			last = err
			break
		}
		if frame.GetEnded() != nil {
			t.Fatal("a stream that broke the protocol received an Ended")
		}
	}
	var typed *exec.Error
	if !errors.As(last, &typed) || typed.Class != exec.ErrorClassInvalidRequest {
		t.Fatalf("the stream ended with %v, want a typed INVALID_REQUEST", last)
	}
	awaitChannel(t, cancelled.Opened(), "the session to be ended with its stream")
}

// An input frame over the target's maximum is the host's mistake: the stream ends LIMIT / INPUT_TOO_LARGE, the session ends
// with it, and the bytes of the frame never reach the function; the frame of exactly the maximum before it was accepted, and is
// never discarded by the end: the function reads all of it, and then io.EOF.
func TestAnInputFrameLargerThanTheTargetsMaximumEndsTheStreamTyped(t *testing.T) {
	read := make(chan []byte, 1)
	rig := newWireRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		// It reads only after the SDK has ended the session's input and closed Proc.Signals and Proc.Resize, which it does one after
		// the other when a session ends, so that what it finds is what the SDK kept of the input, and not what it was quick to read.
		for range p.Signals {
		}
		for range p.Resize {
		}
		data, _ := io.ReadAll(p.Stdin) // ends at once: the input then reports EOF
		read <- data
		<-ctx.Done()
		return exec.Exit{}, ctx.Err()
	}, func(i *exec.TargetInfo) { i.Session.MaxInputFrame = 4096 }))
	conn := rig.mustOpen("s1", withoutTTY)
	sendInput(t, conn, string(bytes.Repeat([]byte{'a'}, 4096))) // exactly the maximum is accepted
	sendInput(t, conn, string(bytes.Repeat([]byte{'b'}, 4097)))
	var last error
	for {
		if _, err := recvFrame(t, conn); err != nil {
			last = err
			break
		}
	}
	var typed *exec.Error
	if !errors.As(last, &typed) || typed.Class != exec.ErrorClassLimit || typed.Reason != exec.ErrorReasonInputTooLarge {
		t.Fatalf("the stream ended with %v, want LIMIT / INPUT_TOO_LARGE", last)
	}
	select {
	case data := <-read:
		// The frame of exactly the maximum was accepted before the session ended, and what the SDK accepted is still there to read
		// after it, so the function reads all of it; and not one byte of the frame over the maximum, which was refused.
		if want := bytes.Repeat([]byte{'a'}, 4096); !bytes.Equal(data, want) {
			t.Errorf("the function read %d bytes, want the %d of the frame of the maximum and none of the frame over it", len(data), len(want))
		}
	case <-time.After(engineWait):
		t.Fatal("the session did not end with its stream")
	}
}

// Frames of a kind this build does not know (a newer peer's) are ignored, counted by kind, and do not disturb the session. The frames
// it serves (input, resize, signal, close_input, query, close and detach) are not among them: the tests of the control frames
// (open_control_*_test.go) and of the detach (detach_memory_test.go) hold those, and that none is counted as ignored.
func TestFramesThisBuildDoesNotServeAreIgnoredAndCounted(t *testing.T) {
	rig := newWireRig(t, engTarget(echoFunction))
	count := func(kind string) float64 {
		metrics, _ := sdkdiag.Default().Snapshot()
		for _, m := range metrics {
			if m.GetName() != "omniview.plugin.self.sdk.exec.frames.ignored" {
				continue
			}
			for _, p := range m.GetPoints() {
				for _, a := range p.GetAttrs() {
					if a.GetValue() == kind {
						return p.GetValue()
					}
				}
			}
		}
		return 0
	}
	before := map[string]float64{}
	kinds := []string{"unknown"}
	for _, k := range kinds {
		before[k] = count(k)
	}
	conn := rig.mustOpen("s1", withoutTTY)
	for _, frame := range []*execv2.ClientFrame{
		{}, // a frame with no kind: one of a newer peer's, which this build does not know
	} {
		if err := conn.Send(frame); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	sendInput(t, conn, "still here") // a frame after the ignored ones is applied: they did not end the stream or the input
	var got []byte
	for len(got) < len("still here") {
		frame, err := recvFrame(t, conn)
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		if out := frame.GetOutput(); out != nil {
			got = append(got, out.GetData()...)
		}
	}
	if string(got) != "still here" {
		t.Fatalf("output %q", got)
	}
	for _, k := range kinds {
		if after := count(k); after-before[k] != 1 {
			t.Errorf("frames ignored of kind %s: %v before, %v after, want one more", k, before[k], after)
		}
	}
	sendClose(t, conn, execv2.CloseMode_CLOSE_MODE_KILL)
	_, ended := readUntilEnded(t, conn)
	if ended.GetEnd().GetClosed() == nil {
		t.Errorf("end %v, want closed", ended.GetEnd())
	}
}

// A terminal that was asked for with no size gets the SDK's 80 x 24, and the host is told it was a default; a size that was
// asked for, and a session with no terminal, get no such notice.
func TestTheSizeDefaultedNoticeIsSentForATerminalWithNoSizeOnly(t *testing.T) {
	rig := newWireRig(t, engTarget(echoFunction))
	firstFrame := func(id string, mods ...func(*execv2.Start)) *execv2.ServerFrame {
		conn := rig.mustOpen(id, mods...)
		sendInput(t, conn, "x")
		frame, err := recvFrame(t, conn)
		if err != nil {
			t.Fatalf("%s: recv: %v", id, err)
		}
		return frame
	}
	if notice := firstFrame("no-size").GetNotice(); notice.GetKind() != execv2.NoticeKind_NOTICE_KIND_SIZE_DEFAULTED {
		t.Errorf("a terminal with no size: the first frame after Opened is not the size-defaulted notice")
	}
	if frame := firstFrame("sized", withSize(100, 30)); frame.GetNotice() != nil {
		t.Errorf("a terminal with a size got a %s frame first, want no notice", frameKind(frame))
	}
	if frame := firstFrame("no-tty", withoutTTY); frame.GetNotice() != nil {
		t.Errorf("a session with no terminal got a %s frame first, want no notice", frameKind(frame))
	}
}

// What the function reports at Started refines Opened's facts: its program, pid, terminal, size, TERM and transport, and the
// capabilities it narrowed; Opened's own facts are not changed.
func TestStartedCarriesWhatTheFunctionReportedAndOpenedDoesNot(t *testing.T) {
	rig := newWireRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		narrowed := p.Capabilities
		narrowed.Resize = false
		p.Started(exec.SessionFacts{
			PID: 4242, Program: exec.Program{Argv: []string{"/bin/bash", "-l"}, Name: "bash", Path: "/bin/bash"},
			TTY: new(true), Size: exec.Size{Cols: 100, Rows: 30}, Term: "xterm-256color", Transport: "test-transport",
			Capabilities: &narrowed,
		})
		<-ctx.Done()
		return exec.Exit{}, ctx.Err()
	}, func(i *exec.TargetInfo) { i.Capabilities.Resize = true; i.Capabilities.TTY = true }))
	conn := rig.mustOpen("s1", withSize(120, 40))
	opened := conn.Opened().GetFacts()
	if opened.Tty != nil || opened.GetPid() != 0 || opened.GetTerm() != "" || opened.GetTransport() != "" {
		t.Errorf("Opened carries facts that only the target knows: %v", opened)
	}
	if opened.GetSize().GetCols() != 120 || opened.GetSize().GetRows() != 40 || !opened.GetCapabilities().GetResize() {
		t.Errorf("Opened facts %v: want the requested size and the opened capabilities", opened)
	}
	var started *execv2.SessionFacts
	for started == nil {
		frame, err := recvFrame(t, conn)
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		started = frame.GetStarted().GetFacts()
	}
	if started.GetPid() != 4242 || started.GetProgram().GetName() != "bash" || started.GetProgram().GetPath() != "/bin/bash" ||
		len(started.GetProgram().GetArgv()) != 2 || started.GetTty() != true || started.Tty == nil ||
		started.GetSize().GetCols() != 100 || started.GetSize().GetRows() != 30 || started.GetTerm() != "xterm-256color" || started.GetTransport() != "test-transport" {
		t.Errorf("Started facts %v: want what the function reported", started)
	}
	if started.GetCapabilities().GetResize() {
		t.Error("Started capabilities still say Resize: the function narrowed it")
	}
	if started.GetSessionId() != "s1" || started.GetTarget().GetProvider() != "fake" || started.GetLimits() == nil {
		t.Errorf("Started facts %v: want the session, target and limits of Opened", started)
	}
}

// An Output frame is at most 64 KiB and carries one stream; stdout and stderr share one sequence of offsets, in the order the
// function wrote them, with no gap and no byte twice.
func TestOutputFramesAreAtMost64KiBOfOneStreamWithOneSequenceOfOffsets(t *testing.T) {
	const big = 300 << 10
	rig := newWireRig(t, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		_, _ = p.Stdout.Write(bytes.Repeat([]byte{'o'}, big))
		_, _ = p.Stderr.Write(bytes.Repeat([]byte{'e'}, 1000))
		_, _ = p.Stdout.Write(bytes.Repeat([]byte{'O'}, 10))
		return exec.ExitCode(0), nil
	}, func(i *exec.TargetInfo) {
		i.Capabilities.Stall = exec.StallPropagates
		i.Capabilities.SeparateStderr = true
	}))
	conn := rig.mustOpen("s1", withoutTTY)
	frames, ended := readUntilEnded(t, conn)
	var next uint64
	perStream := map[execv2.Stream]int{}
	var order []execv2.Stream
	for _, f := range frames {
		out := f.GetOutput()
		if out == nil {
			continue
		}
		if len(out.GetData()) == 0 || len(out.GetData()) > 64<<10 {
			t.Fatalf("an Output frame of %d bytes, want 1 to 65,536", len(out.GetData()))
		}
		if out.GetOffset() != next {
			t.Fatalf("an Output frame at offset %d, want %d: one sequence, no gap, no byte twice", out.GetOffset(), next)
		}
		next += uint64(len(out.GetData()))
		perStream[out.GetStream()] += len(out.GetData())
		if len(order) == 0 || order[len(order)-1] != out.GetStream() {
			order = append(order, out.GetStream())
		}
	}
	if perStream[execv2.Stream_STREAM_STDOUT] != big+10 || perStream[execv2.Stream_STREAM_STDERR] != 1000 {
		t.Errorf("stdout %d stderr %d bytes, want %d and 1000", perStream[execv2.Stream_STREAM_STDOUT], perStream[execv2.Stream_STREAM_STDERR], big+10)
	}
	if fmt.Sprint(order) != fmt.Sprint([]execv2.Stream{execv2.Stream_STREAM_STDOUT, execv2.Stream_STREAM_STDERR, execv2.Stream_STREAM_STDOUT}) {
		t.Errorf("the streams arrived in the order %v, want stdout, stderr, stdout: the order the function wrote them", order)
	}
	if ended.GetLastOffset() != next || ended.GetBytesOut() != next || ended.GetOutput() != execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE {
		t.Errorf("Ended last_offset %d bytes_out %d output %v, want %d, %d, COMPLETE", ended.GetLastOffset(), ended.GetBytesOut(), ended.GetOutput(), next, next)
	}
}

// When Ended arrives the session's place is free and its id is held by its tombstone: a host that reads Ended and opens another
// id is never refused for the session that has just ended (the engine's Done comes a moment before its place is released; the
// server waits for Gone), and the same id is refused as SESSION_EXISTS (gRPC Aborted) until the tombstone expires.
func TestThePlaceOfASessionIsFreeAndItsIdHeldWhenItsEndedArrives(t *testing.T) {
	rig := newWireRig(t, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) { return exec.ExitCode(0), nil }))
	for i := range 100 {
		id := fmt.Sprintf("id-%d", i)
		conn, err := rig.open(context.Background(), id, withoutTTY)
		if err != nil {
			t.Fatalf("round %d: a new id was refused after the previous session's Ended: %v", i, err)
		}
		readUntilEnded(t, conn)
		_ = conn.Close()
		again, err := rig.open(context.Background(), id, withoutTTY)
		if err == nil {
			_ = again.Close()
			t.Fatalf("round %d: the id of a session that ended was admitted while its tombstone holds it", i)
		}
		if proofRefusal(err) != "CONFLICT/SESSION_EXISTS" {
			t.Fatalf("round %d: the id of a session that ended is refused with %v, want CONFLICT/SESSION_EXISTS", i, err)
		}
	}
}

// Ended is the end of the session and its place is free when it arrives. The engine's Done comes before its place is released, so
// the server waits for Gone: this test holds the engine between the two and requires that Ended is not sent in that window.
// (The wait is a negative one, so it can only miss a fault on a slow machine, never invent one.)
func TestEndedIsNotSentWhileTheSessionsIdIsStillHeld(t *testing.T) {
	rig := newWireRig(t, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) { return exec.ExitCode(0), nil }))
	entered, release := exectest.NewGate(), exectest.NewGate()
	exec.SetPluginEngineSeams(rig.server.Plugin, exec.EngineSeams{EndEmitted: func() {
		entered.Open()
		<-release.Opened()
	}})
	t.Cleanup(release.Open)
	conn := rig.mustOpen("s1", withoutTTY)
	awaitChannel(t, entered.Opened(), "the end to be emitted: Done is closed and the id is still held")
	frames := make(chan *execv2.ServerFrame, 16)
	go func() {
		defer close(frames)
		for {
			frame, err := conn.Recv()
			if err != nil {
				return
			}
			frames <- frame
		}
	}()
	window := time.After(300 * time.Millisecond)
	for held := true; held; {
		select {
		case frame, ok := <-frames:
			if !ok {
				t.Fatal("the stream ended while the id was held")
			}
			if frame.GetEnded() != nil {
				t.Fatal("Ended was sent while the session's id was still held")
			}
		case <-window:
			held = false
		}
	}
	release.Open()
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				t.Fatal("the stream ended without an Ended once the id was released")
			}
			if frame.GetEnded() != nil {
				return
			}
		case <-time.After(engineWait):
			t.Fatal("no Ended after the id was released")
		}
	}
}

// A session without a terminal needs no mode reset: present and empty. With a terminal the exact reset is the scanner's (not
// served yet), so it is absent, which the host reads as "apply your own full reset".
func TestModeResetIsPresentAndEmptyWithoutATerminalAndAbsentWithOne(t *testing.T) {
	rig := newWireRig(t, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) { return exec.ExitCode(0), nil }))
	for _, tty := range []bool{false, true} {
		conn := rig.mustOpen(fmt.Sprintf("tty-%v", tty), func(s *execv2.Start) { s.Tty = tty })
		_, ended := readUntilEnded(t, conn)
		if present := ended.ModeReset != nil; present == tty || len(ended.ModeReset) != 0 {
			t.Errorf("tty %v: mode_reset present %v (%d bytes), want present and empty without a terminal and absent with one", tty, present, len(ended.ModeReset))
		}
	}
}
