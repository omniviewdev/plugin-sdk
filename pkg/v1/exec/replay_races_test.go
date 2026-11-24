package exec_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/internal/lifecycletest"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// Every operation of the host on a session, against itself and against every other, at the same moment: the plugin takes the two in
// some order, and whichever it is, the session is left in a state the contract allows, and every stream ends the way a stream may.
//
//	Close, Detach, a stream that breaks, an attach that takes the session over, EndSession, ReadOutput, ListSessions, and the control
//	frames (a resize, a signal, close_input, a query); each twice as well (Detach twice, Close twice: the second is never applied),
//	and each against each.
//
// What is held for every pair, whatever the order: every stream that was not cancelled ends with exactly one of Ended or Detached and
// then io.EOF; the session has at most one Ended across its streams, and it is the tombstone's; a session that ended ended by a
// request, and one that lives has a lease and no stream; nothing a stream was sent breaks the offsets or the bytes (the author wrote a
// pattern); an EndSession that answered OK ended the session and one that was refused was refused CONFLICT; an attach that was
// refused was refused because the session had ended. And for the pairs whose outcome does not depend on the order, the outcome: a Close
// against anything that cannot take the stream away ends the session; two operations that destroy nothing leave it alive. The control
// frames are the host's own, sent on the stream that held the session when the operations came: whatever they earn (Resized, Fact) goes
// to that stream and to no other, as many as were sent at most, with the seq and the query id they were sent with, and none after the
// stream's last frame.

type raceOp struct {
	name  string
	class string // "destroy", "end", "leave", "attach", "read", "control"
	run   func(w *raceWorld)
}

// raceWorld is one iteration: a session whose host has a stream attached (A), and what the two operations did to it.
type raceWorld struct {
	t     *testing.T
	rig   *proofRig
	id    string
	host  *ledgerHost   // only for its stream, recv and ledger helpers
	first *ledgerStream // stream A: set before any operation runs and never changed, so it is read without the lock

	mu       sync.Mutex
	streams  []*ledgerStream // every stream the host has: A, and the attaches
	killed   map[*ledgerStream]bool
	endOK    int // EndSession calls that answered nil
	endBusy  int // EndSession calls refused CONFLICT
	attachNo int // attaches refused
	reads    []*execv2.ReadOutputResponse
	problems []string

	resizesSent, queriesSent int // the control frames the operations sent on A that earn a frame
}

func (w *raceWorld) problem(format string, args ...any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.problems) < 8 {
		w.problems = append(w.problems, fmt.Sprintf(format, args...))
	}
}

func (w *raceWorld) a() *ledgerStream { return w.first }

func (w *raceWorld) sendOnA(frame *execv2.ClientFrame) { _ = w.a().conn.Send(frame) }

func closeFrame(mode execv2.CloseMode) *execv2.ClientFrame {
	return &execv2.ClientFrame{Frame: &execv2.ClientFrame_Close{Close: &execv2.Close{Mode: mode}}}
}

var raceOps = []raceOp{
	{"Close", "destroy", func(w *raceWorld) { w.sendOnA(closeFrame(execv2.CloseMode_CLOSE_MODE_GRACEFUL)) }},
	{"Close(kill)", "destroy", func(w *raceWorld) { w.sendOnA(closeFrame(execv2.CloseMode_CLOSE_MODE_KILL)) }},
	{"Close twice", "destroy", func(w *raceWorld) {
		w.sendOnA(closeFrame(execv2.CloseMode_CLOSE_MODE_GRACEFUL))
		w.sendOnA(closeFrame(execv2.CloseMode_CLOSE_MODE_GRACEFUL))
	}},
	{"EndSession", "end", func(w *raceWorld) { w.endSession(execv2.CloseMode_CLOSE_MODE_GRACEFUL) }},
	{"EndSession(kill)", "end", func(w *raceWorld) { w.endSession(execv2.CloseMode_CLOSE_MODE_KILL) }},
	{"Detach", "leave", func(w *raceWorld) { w.sendOnA(detachFrame(0)) }},
	{"Detach twice", "leave", func(w *raceWorld) { w.sendOnA(detachFrame(0)); w.sendOnA(detachFrame(0)) }},
	{"the stream breaks", "leave", func(w *raceWorld) {
		w.mu.Lock()
		w.killed[w.a()] = true
		w.mu.Unlock()
		w.a().retire()
	}},
	{"Attach", "attach", func(w *raceWorld) { w.attach() }},
	{"ReadOutput", "read", func(w *raceWorld) { w.readOutput() }},
	{"ListSessions", "read", func(w *raceWorld) { w.listSessions() }},
	{"Resize", "control", func(w *raceWorld) {
		w.mu.Lock()
		w.resizesSent++
		w.mu.Unlock()
		w.sendOnA(ctlResize(raceSeq, raceCols, raceRows))
	}},
	{"Resize twice", "control", func(w *raceWorld) {
		w.mu.Lock()
		w.resizesSent += 2
		w.mu.Unlock()
		w.sendOnA(ctlResize(raceSeq, raceCols, raceRows))
		w.sendOnA(ctlResize(raceSeq, raceCols, raceRows))
	}},
	{"Signal", "control", func(w *raceWorld) { w.sendOnA(ctlSignal(execv2.Signal_SIGNAL_INT)) }},
	{"CloseInput", "control", func(w *raceWorld) { w.sendOnA(ctlCloseInput()) }},
	{"CloseInput twice", "control", func(w *raceWorld) { w.sendOnA(ctlCloseInput()); w.sendOnA(ctlCloseInput()) }},
	{"Query", "control", func(w *raceWorld) {
		w.mu.Lock()
		w.queriesSent++
		w.mu.Unlock()
		w.sendOnA(ctlQuery(raceQuery, execv2.FactKind_FACT_KIND_CWD))
	}},
}

func (w *raceWorld) endSession(mode execv2.CloseMode) {
	err := w.rig.client.EndSession(context.Background(), &execv2.EndSessionRequest{SessionId: w.id, Mode: mode})
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case err == nil:
		w.endOK++
	case isRefusalOf(err, exec.ErrorClassConflict, exec.ErrorReasonUnspecified):
		w.endBusy++
	default:
		w.problems = append(w.problems, fmt.Sprintf("EndSession: %v, want OK or CONFLICT", err))
	}
}

func (w *raceWorld) attach() {
	conn, err := w.rig.attach(context.Background(), w.id, 0)
	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		if !isRefusalOf(err, exec.ErrorClassNotFound, exec.ErrorReasonSessionUnknown) {
			w.problems = append(w.problems, fmt.Sprintf("an attach was refused with %v, want NOT_FOUND / SESSION_UNKNOWN, and only because the session had ended", err))
		}
		w.attachNo++
		return
	}
	w.streams = append(w.streams, newLedgerStream(conn, len(w.streams)+1, 0))
}

func (w *raceWorld) readOutput() {
	resp, err := w.rig.client.ReadOutput(context.Background(), &execv2.ReadOutputRequest{SessionId: w.id, FromOffset: 0, MaxBytes: 1 << 20})
	if err != nil {
		w.problem("ReadOutput: %v: a session that has been started is read, live or tombstoned", err)
		return
	}
	w.mu.Lock()
	w.reads = append(w.reads, resp)
	w.mu.Unlock()
}

func (w *raceWorld) listSessions() {
	list, err := w.rig.client.ListSessions(context.Background())
	if err != nil {
		w.problem("ListSessions: %v", err)
		return
	}
	n := 0
	for _, info := range list.GetSessions() {
		if info.GetFacts().GetSessionId() == w.id {
			n++
		}
	}
	if n != 1 {
		w.problem("ListSessions lists the session %d times, want once", n)
	}
}

// raceClass says what a pair must leave: "ended", "live", or "" for either.
func raceOutcome(x, y raceOp) string {
	harmless := func(o raceOp) bool { return o.class == "read" || o.class == "attach" || o.class == "control" }
	switch {
	case x.class == "destroy" && (y.class == "destroy" || y.class == "read" || y.class == "control" || y.class == "end"),
		y.class == "destroy" && (x.class == "destroy" || x.class == "read" || x.class == "control" || x.class == "end"):
		return "ended" // nothing in the pair can take the stream from the session or its Close, so the Close is applied
	case (harmless(x) || x.class == "leave") && (harmless(y) || y.class == "leave"),
		(x.class == "end" && (harmless(y) || y.class == "end")), (y.class == "end" && harmless(x)):
		return "live" // nothing in the pair destroys the session: a stream is attached whenever EndSession looks, so it is refused
	}
	return ""
}

// sessionRacePair runs one iteration of a pair of operations.
func sessionRacePair(t *testing.T, rig *proofRig, n int, x, y raceOp) {
	t.Helper()
	id := fmt.Sprintf("race-%d", n)
	where := fmt.Sprintf("iteration %d: %s against %s", n, x.name, y.name)
	conn, err := rig.openKind(context.Background(), "race", id, proofTTY)
	if err != nil {
		t.Fatalf("%s: open: %v", where, err)
	}
	h := &ledgerHost{t: t, rig: rig, id: id}
	w := &raceWorld{t: t, rig: rig, id: id, host: h, killed: map[*ledgerStream]bool{}}
	first := newLedgerStream(conn, 1, 0)
	w.streams = append(w.streams, first)
	w.first = first
	defer func() {
		for _, s := range w.streams {
			s.retire()
		}
	}()
	// The session has written its output when the two operations come.
	if !proofPoll(engineWait, func() bool {
		info := proofListed(rig.client, id)
		return info != nil && info.GetNextOffset() == raceOutput
	}) {
		t.Fatalf("%s: the session did not write its %d bytes", where, raceOutput)
	}

	gate := make(chan struct{})
	done := make(chan struct{}, 2)
	for _, op := range []raceOp{x, y} {
		go func() {
			<-gate
			op.run(w)
			done <- struct{}{}
		}()
	}
	close(gate)
	for range 2 {
		select {
		case <-done:
		case <-time.After(engineWait):
			t.Fatalf("%s: the operations did not return within %v", where, engineWait)
		}
	}

	// Every stream is asked to leave (harmless to a stream that has left, or whose session ended) and read to its last frame.
	ended, detached, resizedSeen, factsSeen := 0, 0, 0, 0
	var endedFrame *execv2.Ended
	for _, s := range w.streams {
		if w.killed[s] {
			continue
		}
		_ = s.conn.Send(detachFrame(0))
		shadow := newLedger(true, false, true, ledgerRing)
		shadow.begin(s.opened, 0)
		var last *execv2.ServerFrame
		for last == nil {
			s.timer.Reset(engineWait)
			var r ledgerRecv
			select {
			case r = <-s.out:
			case <-s.timer.C:
				t.Fatalf("%s: stream %d: no frame within %v: a stream that is not the session's must be told it was superseded, and one that is must answer Detach", where, s.n, engineWait)
			}
			s.timer.Stop()
			if r.err != nil {
				t.Fatalf("%s: stream %d broke before its last frame: %v", where, s.n, r.err)
			}
			if isLast(r.frame) {
				last = r.frame
				break
			}
			if resized, fact := r.frame.GetResized(), r.frame.GetFact(); resized != nil || fact != nil {
				// What a control frame earned belongs to the stream that sent it: A, which is stream 1. It is held to what A sent.
				switch {
				case s.n != 1:
					t.Errorf("%s: stream %d was sent %s: only stream 1 sent control frames, and what a frame earns goes to the stream that sent it", where, s.n, frameKind(r.frame))
				case resized != nil:
					if resized.GetSeq() != raceSeq || resized.GetSize().GetCols() != raceCols || resized.GetSize().GetRows() != raceRows {
						t.Errorf("%s: stream 1 was sent Resized %v, want seq %d and size %dx%d, the only resize it sent", where, resized, raceSeq, raceCols, raceRows)
					}
					resizedSeen++
				default:
					if fact.GetQueryId() != raceQuery {
						t.Errorf("%s: stream 1 was sent Fact %v, want the answer to query %d, the only one it asked", where, fact, raceQuery)
					}
					factsSeen++
				}
				continue
			}
			shadow.frame(r.frame)
		}
		s.timer.Reset(engineWait)
		select {
		case r := <-s.out:
			if !errors.Is(r.err, io.EOF) {
				t.Fatalf("%s: stream %d: after its last frame (%s): %v, want io.EOF", where, s.n, frameKind(last), r.err)
			}
		case <-s.timer.C:
			t.Fatalf("%s: stream %d did not end after its last frame", where, s.n)
		}
		s.timer.Stop()
		if len(shadow.problems) > 0 {
			t.Fatalf("%s: stream %d: %s", where, s.n, strings.Join(shadow.problems, "\n\t"))
		}
		switch {
		case last.GetEnded() != nil:
			ended++
			endedFrame = last.GetEnded()
		case last.GetDetached() != nil:
			detached++
		}
	}
	if ended > 1 {
		t.Fatalf("%s: %d Ended frames across the streams of one session, want at most one", where, ended)
	}
	w.mu.Lock()
	resizes, queries := w.resizesSent, w.queriesSent
	w.mu.Unlock()
	if resizedSeen > resizes || factsSeen > queries {
		t.Errorf("%s: stream 1 was sent %d Resized and %d Fact frames for the %d resizes and %d queries it sent: nothing is answered twice", where, resizedSeen, factsSeen, resizes, queries)
	}

	// Every stream has left or broke: the plugin notices the ones that broke. A session whose Close was applied is ending for a moment
	// (no stream, no lease, not yet ended: its function is told); it settles as ended, or, when no Close was applied, as detached with a lease.
	var info *execv2.SessionInfo
	if !proofPoll(engineWait, func() bool {
		info = proofListed(rig.client, id)
		return info != nil && (info.GetEndedWith() != nil || (!info.GetAttached() && info.GetLeaseExpiresUnixMs() != 0))
	}) {
		t.Fatalf("%s: the session is %v after every stream left", where, info)
	}
	live := info.GetEndedWith() == nil
	switch want := raceOutcome(x, y); {
	case want == "ended" && live, want == "live" && !live:
		t.Errorf("%s: the session ended: %v, want it to be %s whatever the order", where, !live, want)
	}
	if !live {
		if closed := info.GetEndedWith().GetEnd().GetClosed(); closed.GetBy() != execv2.ClosedBy_CLOSED_BY_REQUEST {
			t.Errorf("%s: the session ended %v, want Closed by REQUEST: nothing else ended it", where, info.GetEndedWith().GetEnd())
		}
		if info.GetAttached() || info.GetLeaseExpiresUnixMs() != 0 || info.GetNextOffset() != raceOutput || info.GetEndedWith().GetLastOffset() != raceOutput {
			t.Errorf("%s: the tombstone says %v, want no stream, no lease and the output to %d", where, info, raceOutput)
		}
		if endedFrame != nil && !proto.Equal(endedFrame, info.GetEndedWith()) {
			t.Errorf("%s: the Ended a stream was sent is %v, the tombstone says %v", where, endedFrame, info.GetEndedWith())
		}
		if w.endBusy > 0 && w.endOK == 0 && w.attachNo == 0 && endedFrame == nil && detached == 0 {
			t.Errorf("%s: an EndSession was refused and no stream ever left, yet the session ended", where)
		}
	} else {
		if info.GetAttached() || info.GetLeaseExpiresUnixMs() == 0 {
			t.Errorf("%s: the session lives: %v, want no stream and a lease running", where, info)
		}
		if endedFrame != nil {
			t.Errorf("%s: a stream was sent Ended %v and the session lives", where, endedFrame)
		}
		if w.endOK > 0 {
			t.Errorf("%s: an EndSession answered OK and the session lives", where)
		}
		if w.attachNo > 0 {
			t.Errorf("%s: an attach was refused as unknown and the session lives", where)
		}
	}
	for _, p := range w.problems {
		t.Errorf("%s: %s", where, p)
	}
	// What ReadOutput said is the ring: contiguous from 0, the pattern, ended as the session was when it was read or later.
	for _, resp := range w.reads {
		l := newLedger(true, false, true, ledgerRing)
		l.read(resp, 0, 1<<20)
		if len(l.problems) > 0 {
			t.Errorf("%s: ReadOutput: %s", where, strings.Join(l.problems, "\n\t"))
		}
		if resp.GetEnded() && live {
			t.Errorf("%s: ReadOutput said the session ended and it lives", where)
		}
		if resp.GetNextOffset() > raceOutput || (resp.GetEnded() && resp.GetNextOffset() != raceOutput) {
			t.Errorf("%s: ReadOutput: next_offset %d, ended %v, want at most %d, and exactly that when ended", where, resp.GetNextOffset(), resp.GetEnded(), raceOutput)
		}
	}
	if live {
		if err := rig.client.EndSession(context.Background(), &execv2.EndSessionRequest{SessionId: id, Mode: execv2.CloseMode_CLOSE_MODE_KILL}); err != nil {
			t.Fatalf("%s: EndSession of a session that lives and has no stream: %v", where, err)
		}
	}
}

// raceOutput is what the session of a race has written when the operations come.
const raceOutput = 100_000

// What the control operations of a race send: the seq and the size of a resize, and the id of a query, the same for every operation
// so that whatever a stream is sent for them is told from what it should not have been sent.
const (
	raceSeq, raceCols, raceRows = 77, 90, 30
	raceQuery                   = 88
)

// raceFunction writes its pattern and then takes every size it is sent until its session ends, so that a resize is acknowledged.
func raceFunction(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
	buf := make([]byte, raceOutput)
	ledgerFill(buf, 0)
	if _, err := p.Stdout.Write(buf); err != nil {
		return exec.Exit{}, err
	}
	sizes := p.Resize
	for {
		select {
		case _, ok := <-sizes:
			if !ok {
				sizes = nil
			}
		case <-ctx.Done():
			return exec.Exit{}, ctx.Err()
		}
	}
}

func TestEveryPairOfOperationsOnASessionLeavesItInALegalStateWithAtMostOneEnd(t *testing.T) {
	rounds := proofScaled(6, 2)
	rig := newProofRig(t, proofTarget("race", raceFunction, func(i *exec.TargetInfo) {
		i.Session = exec.DefaultSessionLimits()
		i.Session.MaxSessions = 64
	}), proofTarget("warm", proofEchoFunction))
	warm := rig.mustOpenKind("warm", "warm", proofTTY)
	if _, err := newProofEchoer(warm).measure(3); err != nil {
		t.Fatalf("warm-up: %v", err)
	}
	_ = warm.Close()
	proofEndDetached(t, rig.client, "warm")
	baseline := lifecycletest.TakeBaseline()

	n := 0
	began := time.Now()
	for _, x := range raceOps {
		for _, y := range raceOps {
			t.Run(x.name+" against "+y.name, func(t *testing.T) {
				for range rounds {
					n++
					sessionRacePair(t, rig, n, x, y)
				}
			})
		}
	}
	t.Logf("%d pairs of operations, %d iterations in %v", len(raceOps)*len(raceOps), n, time.Since(began).Round(time.Millisecond))

	plugin := rig.server.Plugin
	if !proofPoll(engineWait, func() bool { return len(exec.PluginLive(plugin)) == 0 }) {
		t.Errorf("the plugin still holds %v", exec.PluginLive(plugin))
	}
	if count, retained := exec.PluginTombstones(plugin); count > 256 || retained > 32<<20 {
		t.Errorf("the plugin holds %d tombstones keeping %d bytes: over the bounds", count, retained)
	}
	if err := baseline.Check(10 * time.Second); err != nil {
		t.Errorf("goroutines at rest: %v", err)
	}
}
