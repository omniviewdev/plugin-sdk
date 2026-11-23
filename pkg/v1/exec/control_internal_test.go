package exec

import (
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The pure parts of the control frames: the acknowledgement of the resizes (resize.go), the wire form of a fact (facts_query.go) and the
// command's queues (proc_control.go). The wire tests (exec_test, open_control_*_test.go) hold what a host reads; these hold the rules
// that the wire tests reach by many frames, one at a time.

func TestResizeAcksCoverEveryResizeAndTellItOnceAtItsDeadline(t *testing.T) {
	const unread = 5 * time.Second
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	var a resizeAcks
	a.accepted(now, unread, 1, Size{Cols: 81, Rows: 24})
	a.accepted(now.Add(4*time.Second), unread, 2, Size{Cols: 82, Rows: 24})
	if at, waiting := a.deadline(); !waiting || !at.Equal(now.Add(unread)) {
		t.Fatalf("deadline %v (waiting %v), want %v: it counts from the oldest resize that no ack covers", at, waiting, now.Add(unread))
	}
	if a.expire(now.Add(unread-time.Nanosecond), true) {
		t.Fatal("told FAILED before the deadline")
	}
	if !a.expire(now.Add(unread), true) {
		t.Fatal("not told at the deadline")
	}
	if ack, ok := a.take(); !ok || ack != (resizedAck{2, Size{Cols: 82, Rows: 24}, execv2.ResizeResult_RESIZE_RESULT_FAILED}) {
		t.Fatalf("the ack at the deadline is %+v (%v), want FAILED for the newest", ack, ok)
	}
	if a.expire(now.Add(time.Hour), true) {
		t.Fatal("told twice for one deadline")
	}
	a.applied(2, Size{Cols: 82, Rows: 24}) // the function takes it late
	a.applied(2, Size{Cols: 82, Rows: 24}) // a signal that raced the first: the same ack is not put twice
	if ack, ok := a.take(); !ok || ack.result != execv2.ResizeResult_RESIZE_RESULT_APPLIED || ack.seq != 2 {
		t.Fatalf("the ack after the function took the size is %+v (%v), want APPLIED for seq 2", ack, ok)
	}
	if _, ok := a.take(); ok {
		t.Fatal("a second ack for one application")
	}
	a.applied(2, Size{Cols: 82, Rows: 24}) // the signal that raced the first one arrives after its ack was taken: nothing new to tell
	if _, ok := a.take(); ok {
		t.Fatal("an ack was put again for an application that the host was already told")
	}
}

func TestResizeAcksAppliedOlderThanTheNewestKeepsTheNewestWaitingAndIgnoredClearsIt(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	var a resizeAcks
	a.accepted(now, time.Second, 3, Size{Cols: 80, Rows: 24})
	a.accepted(now, time.Second, 4, Size{Cols: 90, Rows: 24})
	a.applied(3, Size{Cols: 80, Rows: 24})
	if _, waiting := a.deadline(); !waiting {
		t.Error("the ack for seq 3 covered seq 4, which is newer and was not taken")
	}
	a.applied(4, Size{Cols: 91, Rows: 24}) // the same seq with another size is another resize
	if _, waiting := a.deadline(); !waiting {
		t.Error("a size that is not the one that waits cleared it")
	}
	a.applied(4, Size{Cols: 90, Rows: 24})
	if _, waiting := a.deadline(); waiting {
		t.Error("the size that waited was taken and still waits")
	}
	a.accepted(now, time.Second, 5, Size{Cols: 95, Rows: 24})
	a.ignored(6, Size{Cols: 96, Rows: 24}) // the session narrowed: what waited is covered by the newer ack
	if _, waiting := a.deadline(); waiting {
		t.Error("an ignored resize left an older one waiting")
	}
	a.accepted(now, time.Second, 7, Size{Cols: 97, Rows: 24})
	if !a.expire(now.Add(time.Second), false) {
		t.Fatal("no ack at the deadline")
	}
	if ack, _ := a.take(); ack.result != execv2.ResizeResult_RESIZE_RESULT_IGNORED {
		t.Errorf("a size that waited for a session that can no longer be resized is told %v, want IGNORED", ack.result)
	}
}

func TestAFactIsPutOnTheWireWithinItsBoundsAndWhatBreaksOneIsDropped(t *testing.T) {
	cwd := func(path string, reason execv2.UnknownReason) *execv2.Fact {
		return &execv2.Fact{QueryId: 9, Value: &execv2.Fact_Cwd{Cwd: &execv2.Directory{Path: path, Reason: reason}}}
	}
	fg := func(state execv2.ForegroundState, name string, pgid uint32, reason execv2.UnknownReason) *execv2.Fact {
		return &execv2.Fact{QueryId: 9, Value: &execv2.Fact_Foreground{Foreground: &execv2.Foreground{State: state, Name: name, Pgid: pgid, Reason: reason}}}
	}
	const failed = execv2.UnknownReason_UNKNOWN_REASON_QUERY_FAILED
	for _, tc := range []struct {
		name     string
		fact     Fact
		kind     FactKind
		want     *execv2.Fact
		problems int
	}{
		{"a directory", Fact{Cwd: "/srv"}, FactKindCwd, cwd("/srv", 0), 0},
		{"no directory, with its reason", Fact{Reason: UnknownReasonRemote}, FactKindCwd, cwd("", execv2.UnknownReason_UNKNOWN_REASON_REMOTE), 0},
		{"a directory with a control character", Fact{Cwd: "/a\x1b[31m"}, FactKindCwd, cwd("", failed), 1},
		{"a directory of exactly 4096 bytes", Fact{Cwd: "/" + strings.Repeat("d", 4095)}, FactKindCwd, cwd("/"+strings.Repeat("d", 4095), 0), 0},
		{"a directory of 4097 bytes", Fact{Cwd: "/" + strings.Repeat("d", 4096)}, FactKindCwd, cwd("", failed), 1},
		{"an idle foreground drops its name", Fact{Foreground: Foreground{State: ForegroundIdle, Name: "x", PGID: 3}}, FactKindForeground, fg(execv2.ForegroundState_FOREGROUND_STATE_IDLE, "", 0, 0), 0},
		{"a busy foreground", Fact{Foreground: Foreground{State: ForegroundBusy, Name: "vim", PGID: 7}}, FactKindForeground, fg(execv2.ForegroundState_FOREGROUND_STATE_BUSY, "vim", 7, 0), 0},
		{"a busy foreground with a name of 257 bytes", Fact{Foreground: Foreground{State: ForegroundBusy, Name: strings.Repeat("n", 257), PGID: 7}}, FactKindForeground, fg(execv2.ForegroundState_FOREGROUND_STATE_BUSY, "", 7, 0), 1},
		{"an unknown foreground with its reason", Fact{Reason: UnknownReasonUnsupported}, FactKindForeground, fg(0, "", 0, execv2.UnknownReason_UNKNOWN_REASON_UNSUPPORTED), 0},
		{"a state that is none", Fact{Foreground: Foreground{State: ForegroundState(9)}}, FactKindForeground, fg(0, "", 0, failed), 1},
		{"an answer of another kind", Fact{Kind: FactKindCwd, Cwd: "/x"}, FactKindForeground, fg(0, "", 0, failed), 1},
		{"a kind that is not a fact", Fact{Cwd: "/x"}, FactKindUnspecified, &execv2.Fact{QueryId: 9}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, problems := tc.fact.toWire(9, tc.kind)
			if !proto.Equal(got, tc.want) || len(problems) != tc.problems {
				t.Errorf("toWire = %v with %d problems %q, want %v with %d", got, len(problems), problems, tc.want, tc.problems)
			}
		})
	}
}

func TestTheSignalQueueKeepsTheNewestEightAndClosesWithTheCommand(t *testing.T) {
	var p procControl
	signals, queries, fact := p.init(&command{}, Capabilities{})
	if signals == nil || fact == nil || queries != nil {
		t.Fatalf("init gave signals %v, queries %v, fact %v: signals and fact are never nil, queries only for a target that provides a fact", signals != nil, queries != nil, fact != nil)
	}
	all := []Signal{SignalInt, SignalTerm, SignalKill, SignalHup, SignalUsr1, SignalUsr2, SignalQuit}
	var sent []Signal
	dropped := 0
	for i := range 20 {
		sig := all[i%len(all)]
		sent = append(sent, sig)
		d, open := p.queueSignal(sig)
		if !open {
			t.Fatal("the queue was closed before the command ended")
		}
		if d {
			dropped++
		}
	}
	if dropped != 12 {
		t.Errorf("%d signals dropped of 20 for a queue of %d, want 12", dropped, signalQueue)
	}
	p.close()
	var got []Signal
	for sig := range signals {
		got = append(got, sig)
	}
	if want := sent[len(sent)-signalQueue:]; !slices.Equal(got, want) {
		t.Errorf("the function reads %v, want the newest %d: %v", got, signalQueue, want)
	}
	if _, open := p.queueSignal(SignalInt); open {
		t.Error("a signal was queued after the command ended")
	}
	p.close() // idempotent
}

// fakeSink records the facts a stream was given.
type fakeSink struct{ got []*execv2.Fact }

func (s *fakeSink) pushFact(_ FactKind, f *execv2.Fact) { s.got = append(s.got, f) }

func TestAStreamThatAttachesIsGivenTheNewestFactOfEachKindAndOnlyItsOwnDetachClears(t *testing.T) {
	var p procControl
	p.init(&command{}, Capabilities{CwdFact: FactModePush})
	a, b, c := &execv2.Fact{Value: &execv2.Fact_Cwd{Cwd: &execv2.Directory{Path: "/a"}}}, &execv2.Fact{Value: &execv2.Fact_Cwd{Cwd: &execv2.Directory{Path: "/b"}}}, &execv2.Fact{Value: &execv2.Fact_Cwd{Cwd: &execv2.Directory{Path: "/c"}}}
	p.push(FactKindCwd, a)
	p.push(FactKindCwd, b) // nobody is attached: the newest is kept
	first := &fakeSink{}
	releaseFirst := p.attach(first)
	if len(first.got) != 1 || first.got[0] != b {
		t.Fatalf("the stream that attached was given %v, want the newest value, /b", first.got)
	}
	p.push(FactKindCwd, c)
	if len(first.got) != 2 || first.got[1] != c {
		t.Fatalf("the attached stream was given %v after a push, want /c next", first.got)
	}
	second := &fakeSink{}
	releaseSecond := p.attach(second) // another stream takes the session over
	releaseFirst()                    // the first stream ends: it must not detach the second
	p.push(FactKindCwd, a)
	if len(second.got) != 2 || second.got[1] != a {
		t.Errorf("the second stream was given %v, want /c at attach and /a pushed: the first stream's end cleared it", second.got)
	}
	releaseSecond()
	p.push(FactKindCwd, b)
	if len(second.got) != 2 {
		t.Errorf("a fact was pushed to a stream that detached")
	}
}
