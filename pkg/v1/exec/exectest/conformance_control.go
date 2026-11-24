package exectest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync/atomic"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The wire cases of the control frames: a host sends a session its resizes (case 6), its signals and its Close (case 7) and its stdin
// EOF (case 8), and holds the plugin to what the contract says of each. The target is a fake that carries the description the author
// declared, so that what the author declared decides what the case expects: a resize is acknowledged APPLIED only by a target that
// declares Resize and a terminal, a signal is delivered only when it is declared, and close_input only by a target that declares
// StdinEOF; anything else the host sends is a violation of what Opened told it, and the stream ends with a typed status.
//
// A plugin that does not serve a frame leaves the host waiting for what the frame should have earned, so every wait here is bounded by
// controlBound, which is long for a frame the plugin answers at once, and fails the case with what the host was waiting for and which
// frame it had sent: a plugin that ignores close_input is told so in five seconds, and is not left to hang the run.

// controlBound is the longest a host waits for what a control frame earns (an acknowledgement, a signal reaching the function, the
// end of a session whose input was closed, a status), all of which the plugin does at once.
const controlBound = 5 * time.Second

// killBound is the longest a kill of a function that ignores its context may take to end the session: the SDK's kill wait is 50 ms and
// its grace is 5 s, so a bound between them tells a plugin that waited for the kill from one that waited for the grace.
const killBound = 2500 * time.Millisecond

// the frames a host sends

func controlSignalFrame(sig execv2.Signal) *execv2.ClientFrame {
	return &execv2.ClientFrame{Frame: &execv2.ClientFrame_Signal{Signal: sig}}
}

func controlCloseInputFrame() *execv2.ClientFrame {
	return &execv2.ClientFrame{Frame: &execv2.ClientFrame_CloseInput{CloseInput: &execv2.CloseInput{}}}
}

// controlWireSignal is the wire's name for a signal an author declares.
func controlWireSignal(sig exec.Signal) execv2.Signal {
	return execv2.Signal(execv2.Signal_value["SIGNAL_"+sig.String()])
}

// what a host reads

// recvWithin reads the next frame of the stream, within bound: a plugin that does not send what the host is waiting for fails the case
// with what it was, and does not hang it. The frame is held to the order of the stream and fed to the audit, as recv does.
func (s *wireSession) recvWithin(bound time.Duration, what string) (*execv2.ServerFrame, error) {
	t := s.r.t
	t.Helper()
	var timedOut atomic.Bool
	timer := time.AfterFunc(bound, func() { timedOut.Store(true); _ = s.conn.Close() })
	frame, err := s.conn.Recv()
	timer.Stop()
	if timedOut.Load() {
		t.Fatalf("session %q: %s: nothing arrived within %v", s.id, what, bound)
	}
	if err != nil {
		return nil, err
	}
	s.grammar.take(frame)
	if st := frame.GetStarted(); st != nil && s.started == nil {
		s.started = st
	}
	if s.out != nil {
		s.out.take(frame)
	}
	return frame, nil
}

// nextResized reads the stream until a Resized arrives, taking the frames of other kinds as they come; the end of the session before
// it is a failure of the case.
func (s *wireSession) nextResized(what string) *execv2.Resized {
	t := s.r.t
	t.Helper()
	for {
		frame, err := s.recvWithin(controlBound, "no Resized frame came for "+what+": the plugin does not acknowledge a resize")
		if err != nil {
			t.Fatalf("session %q: waiting for %s: %v", s.id, what, err)
		}
		if r := frame.GetResized(); r != nil {
			return r
		}
		if frame.GetEnded() != nil {
			t.Fatalf("session %q: the session ended before %s", s.id, what)
		}
	}
}

// resizedUpTo reads the acknowledgements of the resizes a host sent, with the seqs seq0 to last, until the one for last arrives, and
// returns them in the order they arrived: each is one the host can match to a resize it sent, and they do not go back.
func (s *wireSession) resizedUpTo(seq0, last uint32) []*execv2.Resized {
	t := s.r.t
	t.Helper()
	var acks []*execv2.Resized
	for {
		ack := s.nextResized(fmt.Sprintf("the acknowledgement of the resize with seq %d", last))
		acks = append(acks, ack)
		if ack.GetSeq() < seq0 || ack.GetSeq() > last || len(acks) > 1 && ack.GetSeq() < acks[len(acks)-2].GetSeq() {
			t.Errorf("session %q: an acknowledgement with seq %d after %d: the host sent the seqs %d to %d and the acknowledgements go forward", s.id, ack.GetSeq(), len(acks)-1, seq0, last)
		}
		if ack.GetSeq() >= last {
			return acks
		}
	}
}

// echoRound sends bytes as input and reads them back from an echo, and returns the Resized frames that arrived on the way, which a host
// that waits for its echo has also been sent: the echo is how a host knows that every frame it sent before it has been applied.
func (s *wireSession) echoRound(data []byte) []*execv2.Resized {
	t := s.r.t
	t.Helper()
	s.mustInput(data)
	var acks []*execv2.Resized
	var got []byte
	for len(got) < len(data) {
		frame, err := s.recvWithin(controlBound, fmt.Sprintf("no echo of the %d bytes sent (%d came): the plugin does not apply the frames of the host", len(data), len(got)))
		if err != nil {
			t.Fatalf("session %q: waiting for the echo: %v", s.id, err)
		}
		if frame.GetEnded() != nil {
			t.Fatalf("session %q: the session ended with %d of %d bytes echoed", s.id, len(got), len(data))
		}
		if r := frame.GetResized(); r != nil {
			acks = append(acks, r)
		}
		got = append(got, frame.GetOutput().GetData()...)
	}
	return acks
}

// readEcho reads n bytes of output from the stream, taking the frames of other kinds as they come.
func (s *wireSession) readEcho(n int, what string) []byte {
	t := s.r.t
	t.Helper()
	var got []byte
	for len(got) < n {
		frame, err := s.recvWithin(controlBound, fmt.Sprintf("%s: %d of %d bytes came", what, len(got), n))
		if err != nil {
			t.Fatalf("session %q: waiting for %s: %v", s.id, what, err)
		}
		if frame.GetEnded() != nil {
			t.Fatalf("session %q: the session ended with %d of %d bytes read", s.id, len(got), n)
		}
		got = append(got, frame.GetOutput().GetData()...)
	}
	return got
}

// finishWithin reads the stream to its end, as finish does, and fails the case with what when the end does not come within bound of the
// last frame: a session that ends only when the host is told to leave is one that did not take a frame it was sent.
func (s *wireSession) finishWithin(bound time.Duration, what string) *execv2.Ended {
	t := s.r.t
	t.Helper()
	for {
		frame, err := s.recvWithin(bound, what)
		if errors.Is(err, io.EOF) {
			t.Fatalf("session %q: the stream ended with status OK and no Ended after %d frames", s.id, s.grammar.frames)
		}
		if err != nil {
			t.Fatalf("session %q: the stream broke after %d frames: %v", s.id, s.grammar.frames, err)
		}
		ended := frame.GetEnded()
		if ended == nil {
			continue
		}
		if after, err := s.recvWithin(bound, "the stream did not end after its Ended"); !errors.Is(err, io.EOF) {
			t.Fatalf("session %q: after Ended came %s and %v, want io.EOF: nothing follows Ended", s.id, frameName(after), err)
		}
		s.ended = ended
		for _, problem := range s.grammar.problems {
			t.Errorf("session %q: %s", s.id, problem)
		}
		if s.out != nil {
			for _, problem := range s.out.problems {
				t.Errorf("session %q: %s", s.id, problem)
			}
		}
		return ended
	}
}

// endsWithStatus reads the stream to its end, which has to be a status: a host that broke the protocol is told so with the status it
// earned, and not with an Ended. It returns the status, or nil after it has said what was wrong.
func (s *wireSession) endsWithStatus(what string) error {
	t := s.r.t
	t.Helper()
	for {
		frame, err := s.recvWithin(controlBound, "the stream did not end with a status after "+what+": the plugin took the frame")
		switch {
		case errors.Is(err, io.EOF):
			t.Errorf("session %q: the stream ended with status OK after %s: it ends with a typed status", s.id, what)
			return nil
		case err != nil:
			return err
		case frame.GetEnded() != nil:
			t.Errorf("session %q: an Ended came after %s: a stream that broke the protocol ends with a status, and no Ended", s.id, what)
			return nil
		}
	}
}

// wantRefusal holds the status a stream ended with to the class that was expected. A nil status was already reported.
func (s *wireSession) wantRefusal(err error, class exec.ErrorClass, what string) {
	t := s.r.t
	t.Helper()
	if err != nil && !isRefusal(err, class, exec.ErrorReasonUnspecified) {
		t.Errorf("session %q: %s ended the stream with %v, want a status of class %s", s.id, what, err, class)
	}
}

// awaitSignal takes the next signal the author was handed, within the bound of a control frame.
func awaitSignal(t checkT, signals <-chan exec.Signal) exec.Signal {
	t.Helper()
	select {
	case sig := <-signals:
		return sig
	case <-time.After(controlBound):
		t.Fatalf("the author was handed no signal within %v of the frames that carried them: the plugin does not deliver the signals it declares", controlBound)
		return exec.SignalUnspecified
	}
}

// awaitAllReturned waits until every function the rig started has returned: a session whose stream broke the protocol has ended when its
// function has.
func (r *wireRig) awaitAllReturned(what string) {
	r.t.Helper()
	r.await(what, func() bool { return r.returned.Load() == r.started.Load() })
}

// mustOpenEventually starts a session that has to be admitted, once the place of the session that a status ended is free: the plugin
// answers a stream that broke the protocol with its status, and the place is released when the function has returned and the session
// has been removed, which is a moment after the host reads the status. A target that serves one session at a time refuses the next
// open until then, with LIMIT.
func (r *wireRig) mustOpenEventually(id, script string, mods ...func(*execv2.Start)) *wireSession {
	r.t.Helper()
	var s *wireSession
	r.await("the place of a session whose stream ended with a status to be free", func() bool {
		var err error
		if s, err = r.open(id, script, mods...); err != nil {
			if !isRefusal(err, exec.ErrorClassLimit, exec.ErrorReasonTooManySessions) {
				r.t.Fatalf("open %q for the script %q: %v", id, script, err)
			}
			return false
		}
		return true
	})
	return s
}

// ---------------------------------------------------------------------------------------------------------------
// case 6: resize
// ---------------------------------------------------------------------------------------------------------------

// caseWireResize: a resize for a session that cannot be resized (no terminal, or a target that declared no Resize) is acknowledged
// IGNORED, with its seq and size, and never delivered; a resize with a zero or an oversize dimension ends the stream INVALID_REQUEST
// and the session with it; and for a session that can be resized a storm costs the author the size it was reading and the newest, and
// the newest seq is acknowledged APPLIED with the newest size, the size the session was opened with being the first the author is
// handed.
func caseWireResize(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	info := target.Info
	if info.Session.IsZero() {
		t.Skipf(noSessions)
	}
	rig := newWireRig(t, env, target)
	maxDimension := rig.described().GetSession().GetMaxDimension()
	small, medium, large := resizeSizes(maxDimension)
	rig.script("idle", Block())

	// A session without a terminal has no size: a resize is ignored, and the host is told so.
	plain := rig.mustOpenEventually(rig.newID("plain"), "idle", func(st *execv2.Start) { st.Tty = false })
	if err := plain.resize(11, uint32(medium.Cols), uint32(medium.Rows)); err != nil {
		t.Fatalf("send a resize: %v", err)
	}
	for _, ack := range plain.resizedUpTo(11, 11) {
		wantControlAck(t, plain, ack, 11, medium, execv2.ResizeResult_RESIZE_RESULT_IGNORED)
	}
	_ = plain.closeSession(execv2.CloseMode_CLOSE_MODE_GRACEFUL)
	plain.checkEnded("a session without a terminal", plain.finish(), wantEnded{end: endClosed(execv2.ClosedBy_CLOSED_BY_REQUEST, info.Capabilities.Kill)})

	if !rig.tty() {
		return
	}
	startSize := func(st *execv2.Start) { st.Size = &execv2.Size{Cols: uint32(small.Cols), Rows: uint32(small.Rows)} }

	// A malformed size is the host's mistake, whatever the target can do: the stream ends INVALID_REQUEST and the function returns.
	for _, bad := range []struct {
		name       string
		cols, rows uint32
	}{
		{"no columns", 0, 24}, {"no rows", 80, 0}, {"neither", 0, 0},
		{"one column too many", maxDimension + 1, 24}, {"one row too many", 80, maxDimension + 1},
	} {
		s := rig.mustOpenEventually(rig.newID("malformed"), "idle", startSize)
		if err := s.resize(5, bad.cols, bad.rows); err != nil {
			t.Fatalf("send a resize: %v", err)
		}
		s.wantRefusal(s.endsWithStatus("a resize with "+bad.name), exec.ErrorClassInvalidRequest, "a resize with "+bad.name)
		rig.awaitAllReturned("the function of a session whose stream ended with a malformed resize to return")
	}

	// A storm of resizes sent while the author does not read Proc.Resize. The author is an echo, so that the host knows its frames
	// were applied when its input has been echoed, and reads its sizes when the case lets it.
	const storm = 8
	stormSize := func(seq int) exec.Size { // alternating, so that the newest is not the one before it
		if seq%2 == 0 {
			return large
		}
		return medium
	}
	newest := stormSize(storm)
	sizes := make(chan exec.Size, 64)
	gate := NewGate()
	t.Cleanup(gate.Open)
	if info.Capabilities.Resize {
		rig.scriptFunc("storm", func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			go func() {
				select {
				case <-gate.Opened():
				case <-ctx.Done():
					return
				}
				for size := range p.Resize {
					sizes <- size
				}
			}()
			_, _ = io.Copy(p.Stdout, p.Stdin)
			<-ctx.Done()
			return exec.Exit{}, ctx.Err()
		})
	} else {
		rig.script("storm", observeSizes(sizes), CopyInput(), Block())
	}
	s := rig.mustOpenEventually(rig.newID("storm"), "storm", startSize)
	for seq := 1; seq <= storm; seq++ {
		size := stormSize(seq)
		if err := s.resize(uint32(seq), uint32(size.Cols), uint32(size.Rows)); err != nil {
			t.Fatalf("send a resize: %v", err)
		}
	}
	acks := s.echoRound(patternBytes(0, 16)) // every resize was applied before the echo
	if info.Capabilities.Resize {
		gate.Open() // the author reads its sizes now
	}
	if first := awaitControlSize(t, sizes); first != small {
		t.Errorf("the author's first size is %v, want the one the session was opened with, %v", first, small)
	}
	if len(acks) == 0 || acks[len(acks)-1].GetSeq() < storm {
		acks = append(acks, s.resizedUpTo(1, storm)...)
	}
	last := acks[len(acks)-1]
	if !info.Capabilities.Resize {
		// A target that cannot resize: every resize is told ignored, and the author is handed the size the session was opened with
		// and nothing after it.
		for _, ack := range acks {
			if ack.GetResult() != execv2.ResizeResult_RESIZE_RESULT_IGNORED || ack.GetSeq() < 1 || ack.GetSeq() > storm {
				t.Errorf("an acknowledgement %v from a target that declared no Resize, want IGNORED for a seq the host sent", ack)
			}
		}
		wantControlAck(t, s, last, storm, newest, execv2.ResizeResult_RESIZE_RESULT_IGNORED)
		select {
		case size := <-sizes:
			t.Errorf("the author was handed the size %v by a target that declares no resize", size)
		default:
		}
	} else {
		if got := awaitControlSize(t, sizes); got != newest {
			t.Errorf("the author was handed %v after the size the session was opened with, want the newest of the storm, %v", got, newest)
		}
		wantControlAck(t, s, last, storm, newest, execv2.ResizeResult_RESIZE_RESULT_APPLIED)
		for _, ack := range acks[:len(acks)-1] {
			// An acknowledgement before the last is for a resize that a newer one replaced before the author took it.
			if ack.GetSeq() < 1 || ack.GetSeq() >= storm {
				t.Errorf("an acknowledgement with seq %d before the last, which is %d", ack.GetSeq(), storm)
			}
		}
		select {
		case extra := <-sizes:
			t.Errorf("the author was handed a third size, %v: a storm costs it the size it was reading and the newest", extra)
		default:
		}
	}
	_ = s.closeSession(execv2.CloseMode_CLOSE_MODE_GRACEFUL)
	s.checkEnded("the resize storm", s.finish(), wantEnded{end: endClosed(execv2.ClosedBy_CLOSED_BY_REQUEST, info.Capabilities.Kill), started: true, bytesIn: 16, bytesOut: 16})
}

// awaitControlSize takes the next size the author was handed, within the bound of a control frame.
func awaitControlSize(t checkT, sizes <-chan exec.Size) exec.Size {
	t.Helper()
	select {
	case size := <-sizes:
		return size
	case <-time.After(controlBound):
		t.Fatalf("the author was handed no size within %v of the resizes: the plugin does not deliver them", controlBound)
		return exec.Size{}
	}
}

// wantControlAck holds an acknowledgement to the seq, size and result it has to carry.
func wantControlAck(t checkT, s *wireSession, got *execv2.Resized, seq uint32, size exec.Size, result execv2.ResizeResult) {
	t.Helper()
	if got.GetSeq() != seq || got.GetSize().GetCols() != uint32(size.Cols) || got.GetSize().GetRows() != uint32(size.Rows) || got.GetResult() != result {
		t.Errorf("session %q: the acknowledgement is {seq %d, %dx%d, %v}, want {seq %d, %dx%d, %v}", s.id,
			got.GetSeq(), got.GetSize().GetCols(), got.GetSize().GetRows(), got.GetResult(), seq, size.Cols, size.Rows, result)
	}
}

// ---------------------------------------------------------------------------------------------------------------
// case 7: signals, Close graceful and kill
// ---------------------------------------------------------------------------------------------------------------

// caseWireSignals: a signal the target declares reaches the author, in the order the host sent it, each time it is sent; a signal it
// does not declare (or none at all, the unspecified signal, a number nobody knows) ends the stream UNSUPPORTED and the session with it,
// and reaches nobody; and Close ends the session Closed by request, gracefully when the function honors its context and at the kill
// wait, never at the grace, for a kill of one that does not, with remote_may_survive false only when the target declared that ending a
// session terminates the process.
func caseWireSignals(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	info := target.Info
	if info.Session.IsZero() {
		t.Skipf(noSessions)
	}
	rig := newWireRig(t, env, target)
	declared := info.Capabilities.Signals
	signals := make(chan exec.Signal, 64)
	rig.scriptFunc("signals", func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		go func() {
			for sig := range p.Signals {
				signals <- sig
			}
		}()
		_, _ = io.Copy(p.Stdout, p.Stdin)
		<-ctx.Done()
		return exec.Exit{}, ctx.Err()
	})
	rig.script("idle", Block())

	if len(declared) > 0 {
		s := rig.mustOpenEventually(rig.newID("signals"), "signals")
		// Each declared signal, and the first once more (a repeated signal is delivered each time), in groups of four: a burst of the
		// host is delivered in order, and a group is read before the next is sent, so that what is asserted is the order and not how
		// many signals the plugin queues for a function that does not read.
		order := append(slices.Clone(declared), declared[0])
		for start := 0; start < len(order); start += 4 {
			group := order[start:min(start+4, len(order))]
			for _, sig := range group {
				s.mustSend(controlSignalFrame(controlWireSignal(sig)))
			}
			for i, want := range group {
				if got := awaitSignal(t, signals); got != want {
					t.Errorf("the signal at place %d is %v, the host sent %v: signals are delivered in the order they were sent", start+i, got, want)
				}
			}
		}
		s.echoRound(patternBytes(0, 8)) // the frames before the input have been applied
		select {
		case extra := <-signals:
			t.Errorf("the author was handed the signal %v that the host did not send", extra)
		default:
		}
		_ = s.closeSession(execv2.CloseMode_CLOSE_MODE_GRACEFUL)
		s.checkEnded("the signals", s.finish(), wantEnded{end: endClosed(execv2.ClosedBy_CLOSED_BY_REQUEST, info.Capabilities.Kill), started: true, bytesIn: 8, bytesOut: 8})
	}

	// What the target does not declare: the first signal of the contract that is not in its set, or the one that is nothing at all.
	for _, undeclared := range controlUndeclaredSignals(declared) {
		s := rig.mustOpenEventually(rig.newID("undeclared"), "signals")
		s.mustSend(controlSignalFrame(undeclared))
		s.wantRefusal(s.endsWithStatus("the signal "+undeclared.String()), exec.ErrorClassUnsupported, "the signal "+undeclared.String()+", which the target does not declare,")
		rig.awaitAllReturned("the function of a session whose stream ended with a signal that is not declared to return")
		select {
		case got := <-signals:
			t.Errorf("the author was handed the signal %v, which the target does not declare", got)
		default:
		}
	}

	// Close, graceful and kill, on a function that honors its context: the end of a request, and what it says of the process.
	for _, mode := range []execv2.CloseMode{execv2.CloseMode_CLOSE_MODE_GRACEFUL, execv2.CloseMode_CLOSE_MODE_KILL} {
		s := rig.mustOpenEventually(rig.newID("close"), "idle")
		_ = s.closeSession(mode)
		s.checkEnded("a "+mode.String()+" close", s.finish(), wantEnded{end: endClosed(execv2.ClosedBy_CLOSED_BY_REQUEST, info.Capabilities.Kill)})
	}

	// A kill ends a function that ignores its context at the kill wait: the host is not held for the grace.
	gate, running := NewGate(), NewGate()
	t.Cleanup(gate.Open)
	rig.scriptFunc("stuck", func(context.Context, *exec.Proc) (exec.Exit, error) {
		running.Open()
		<-gate.Opened() // ignores its context
		return exec.ExitCode(0), nil
	})
	s := rig.mustOpenEventually(rig.newID("stuck"), "stuck")
	select {
	case <-running.Opened():
	case <-time.After(caseBound):
		t.Fatalf("the stuck function did not start within %v", caseBound)
	}
	began := time.Now()
	_ = s.closeSession(execv2.CloseMode_CLOSE_MODE_KILL)
	ended := s.finish()
	if took := time.Since(began); took > killBound {
		t.Errorf("a kill of a function that ignores its context took %v to end the session, want the kill wait (50 ms) and not the grace (5 s): the bound here is %v", took, killBound)
	}
	s.checkEnded("a kill of a function that ignores its context", ended, wantEnded{end: endClosed(execv2.ClosedBy_CLOSED_BY_REQUEST, info.Capabilities.Kill)})
}

// controlUndeclaredSignals are the signals the host sends that the target does not declare: the first of the contract's signals that is
// not in the set, when there is one, the unspecified signal, and a number nobody knows.
func controlUndeclaredSignals(declared []exec.Signal) []execv2.Signal {
	var out []execv2.Signal
	for _, sig := range []exec.Signal{exec.SignalInt, exec.SignalQuit, exec.SignalTerm, exec.SignalKill, exec.SignalHup, exec.SignalUsr1, exec.SignalUsr2} {
		if !slices.Contains(declared, sig) {
			out = append(out, controlWireSignal(sig))
			break
		}
	}
	return append(out, execv2.Signal_SIGNAL_UNSPECIFIED, execv2.Signal(99))
}

// ---------------------------------------------------------------------------------------------------------------
// case 8: stdin EOF
// ---------------------------------------------------------------------------------------------------------------

// caseWireStdinEOF: for a target that declares StdinEOF, close_input ends the author's input after every byte the host sent before it:
// a command that is a cat is sent "abc\n" and then close_input, reads those bytes, then io.EOF, returns, and its session ends Exited by
// itself, in the time a frame takes and not when the host leaves (a plugin that ignores the frame leaves it running, and the case says
// so after controlBound); a second close_input is the same EOF, and an input frame after it ends the stream INVALID_REQUEST and the
// session with it. For a target that declares none, close_input ends the stream UNSUPPORTED and the session with it.
func caseWireStdinEOF(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	info := target.Info
	if info.Session.IsZero() {
		t.Skipf(noSessions)
	}
	rig := newWireRig(t, env, target)
	exit, end := processExit(info.Capabilities, 0)
	rig.script("cat", CopyInput(), EndWith(exit))

	if !info.Capabilities.StdinEOF {
		s := rig.mustOpenEventually(rig.newID("no-eof"), "cat")
		s.mustSend(controlCloseInputFrame())
		s.wantRefusal(s.endsWithStatus("a close_input"), exec.ErrorClassUnsupported, "a close_input for a target that declares no StdinEOF")
		rig.awaitAllReturned("the function of a session whose stream ended with a close_input the target cannot honor to return")
		return
	}
	const noEnd = "the session did not end after close_input: the function's input did not end, so the plugin did not serve the frame"

	// The shape of a cat: "abc\n", then close_input. The function reads the bytes, then io.EOF, and returns; the session ends Exited.
	s := rig.mustOpenEventually(rig.newID("cat"), "cat")
	s.mustInput([]byte("abc\n"))
	s.mustSend(controlCloseInputFrame())
	if got := s.readEcho(4, "the echo of the bytes sent before close_input"); string(got) != "abc\n" {
		t.Errorf("the cat echoed %q, want %q: the function reads every byte sent before close_input", got, "abc\n")
	}
	s.checkEnded("a cat that was sent abc and close_input", s.finishWithin(controlBound, noEnd), wantEnded{end: end, started: true, bytesIn: 4, bytesOut: 4})

	// Many frames, and the EOF once and twice: the echo ends at the offset the host sent, whatever the frames' sizes.
	for _, closes := range []int{1, 2} {
		s := rig.mustOpenEventually(rig.newID("eof"), "cat")
		audit := s.audit(patternByte)
		var total uint64
		for _, n := range []int{1, 7, 100, 4000} {
			s.mustInput(patternBytes(total, n))
			total += uint64(n)
		}
		for range closes {
			s.mustSend(controlCloseInputFrame())
		}
		ended := s.finishWithin(controlBound, noEnd)
		s.checkEnded(fmt.Sprintf("%d close_input frames", closes), ended, wantEnded{end: end, started: true, bytesIn: total})
		if audit.next != total {
			t.Errorf("the echo ended at offset %d, the host sent %d bytes before close_input: the author's input ends after every byte sent before it", audit.next, total)
		}
	}

	// An input frame after the EOF is the host's mistake. The function of this session waits to be ended and does not exit on its EOF, as a
	// cat does: a function that ended by itself would end the session, and send the host its Ended, whenever it got there before the plugin
	// applied the late frame, which is the order of two goroutines and no breach of the contract.
	rig.script("cat-held", CopyInput(), Block())
	late := rig.mustOpenEventually(rig.newID("late"), "cat-held")
	late.mustInput(patternBytes(0, 4))
	late.mustSend(controlCloseInputFrame())
	late.mustInput(patternBytes(4, 4))
	late.wantRefusal(late.endsWithStatus("an input frame after close_input"), exec.ErrorClassInvalidRequest, "an input frame after close_input")
	rig.awaitAllReturned("the function of a session whose stream ended with an input frame after close_input to return")
}
