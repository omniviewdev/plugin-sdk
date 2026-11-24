package exec_test

import (
	"context"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// A Close and a Detach, in either order, leave the session in the same place: ended by a request, its end the tombstone's, its output
// readable, its place free. Over the wire, each way the two can be sent:
//
//	(a) Close, then Detach, on one stream: the Close decides the end, the Detach after it is moot, the stream is told Ended;
//	(b) Detach, then Close from the stream that attached after it: the first stream is told Detached, the second Ended;
//	(c) Detach, then EndSession: the stream is told Detached and nobody is told Ended;
//	(d) Detach, then Close, pipelined on ONE stream: the Detach makes the stream leave, so the Close behind it is a frame of a stream
//	    that is no longer the session's and is not applied (as a superseded stream's is not): the session lives for its lease, and what
//	    ends it then (EndSession, or an attach and a Close) leaves the same final state.
//
// The end is the same in every case: the same End, the same counts; only the stream that was sent it differs.
func TestCloseAndDetachInEitherOrderLeaveTheSessionInTheSamePlace(t *testing.T) {
	rig := newProofRig(t, proofTarget("race", raceFunction, func(i *exec.TargetInfo) {
		i.Session = exec.DefaultSessionLimits()
		i.Session.MaxSessions = 16
	}))
	open := func(t *testing.T, id string) *ledgerStream {
		t.Helper()
		conn, err := rig.openKind(context.Background(), "race", id, proofTTY)
		if err != nil {
			t.Fatalf("open %s: %v", id, err)
		}
		s := newLedgerStream(conn, 1, 0)
		t.Cleanup(s.retire)
		if !proofPoll(engineWait, func() bool {
			info := proofListed(rig.client, id)
			return info != nil && info.GetNextOffset() == raceOutput
		}) {
			t.Fatalf("%s: the session did not write its output", id)
		}
		return s
	}
	// last reads a stream to its last frame and to io.EOF, and returns the frame.
	last := func(t *testing.T, s *ledgerStream) *execv2.ServerFrame {
		t.Helper()
		h := &ledgerHost{t: t, rig: rig}
		var frame *execv2.ServerFrame
		for frame == nil {
			f, err := h.recv(s)
			if err != nil {
				t.Fatalf("stream %d broke before its last frame: %v", s.n, err)
			}
			if isLast(f) {
				frame = f
			}
		}
		if _, err := h.recv(s); err == nil {
			t.Fatalf("stream %d: a frame came after its last frame", s.n)
		}
		return frame
	}
	tombstone := func(t *testing.T, id string) *execv2.Ended {
		t.Helper()
		info := proofListed(rig.client, id)
		if info == nil || info.GetEndedWith() == nil || info.GetAttached() || info.GetLeaseExpiresUnixMs() != 0 {
			t.Fatalf("%s: ListSessions says %v, want the tombstone: ended, no stream, no lease", id, info)
		}
		resp, err := rig.client.ReadOutput(context.Background(), &execv2.ReadOutputRequest{SessionId: id, FromOffset: 0, MaxBytes: 1 << 20})
		if err != nil || !resp.GetEnded() || resp.GetNextOffset() != raceOutput {
			t.Fatalf("%s: ReadOutput of the tombstone: %v (%v), want ended at %d", id, resp, err, raceOutput)
		}
		if _, err := rig.attach(context.Background(), id, 0); !isRefusalOf(err, exec.ErrorClassNotFound, exec.ErrorReasonSessionUnknown) {
			t.Fatalf("%s: an attach to the tombstone: %v, want NOT_FOUND / SESSION_UNKNOWN", id, err)
		}
		if _, err := rig.openKind(context.Background(), "race", id, proofTTY); !isRefusalOf(err, exec.ErrorClassConflict, exec.ErrorReasonSessionExists) {
			t.Fatalf("%s: a start of the id of the tombstone: %v, want CONFLICT / SESSION_EXISTS", id, err)
		}
		return info.GetEndedWith()
	}
	live := func(t *testing.T, id string) {
		t.Helper()
		if !proofPoll(engineWait, func() bool {
			info := proofListed(rig.client, id)
			return info != nil && info.GetEndedWith() == nil && !info.GetAttached() && info.GetLeaseExpiresUnixMs() != 0
		}) {
			t.Fatalf("%s: the session is %v, want it alive, with no stream and a lease running", id, proofListed(rig.client, id))
		}
	}
	endSession := func(t *testing.T, id string) {
		t.Helper()
		if err := rig.client.EndSession(context.Background(), &execv2.EndSessionRequest{SessionId: id}); err != nil {
			t.Fatalf("EndSession(%s): %v", id, err)
		}
	}

	ends := map[string]*execv2.Ended{}

	t.Run("(a) Close then Detach on one stream", func(t *testing.T) {
		a := open(t, "a")
		_ = a.conn.Send(closeFrame(execv2.CloseMode_CLOSE_MODE_GRACEFUL))
		_ = a.conn.Send(detachFrame(0))
		ended := last(t, a).GetEnded()
		if ended == nil {
			t.Fatalf("the stream that sent Close and then Detach was told Detached, want Ended: the Detach after a Close is moot")
		}
		if got := tombstone(t, "a"); !proto.Equal(got, ended) {
			t.Fatalf("the tombstone %v is not the Ended the stream was sent, %v", got, ended)
		}
		ends["a"] = ended
	})

	t.Run("(b) Detach then Close from the stream that attached after it", func(t *testing.T) {
		a := open(t, "b")
		_ = a.conn.Send(detachFrame(0))
		if d := last(t, a).GetDetached(); d == nil || d.GetReason() != execv2.DetachReason_DETACH_REASON_REQUESTED {
			t.Fatalf("the stream that sent Detach was not told Detached{REQUESTED}")
		}
		live(t, "b")
		conn, err := rig.attach(context.Background(), "b", 0)
		if err != nil {
			t.Fatalf("attach: %v", err)
		}
		s := newLedgerStream(conn, 2, 0)
		t.Cleanup(s.retire)
		_ = s.conn.Send(closeFrame(execv2.CloseMode_CLOSE_MODE_GRACEFUL))
		ended := last(t, s).GetEnded()
		if ended == nil {
			t.Fatalf("the stream that closed the session was told Detached, want Ended")
		}
		if got := tombstone(t, "b"); !proto.Equal(got, ended) {
			t.Fatalf("the tombstone %v is not the Ended the stream was sent, %v", got, ended)
		}
		ends["b"] = ended
	})

	t.Run("(c) Detach then EndSession", func(t *testing.T) {
		a := open(t, "c")
		_ = a.conn.Send(detachFrame(0))
		if last(t, a).GetDetached() == nil {
			t.Fatalf("the stream that sent Detach was not told Detached")
		}
		live(t, "c")
		endSession(t, "c")
		ends["c"] = tombstone(t, "c")
	})

	t.Run("(d) Detach then Close, pipelined on one stream", func(t *testing.T) {
		a := open(t, "d")
		_ = a.conn.Send(detachFrame(0))
		_ = a.conn.Send(closeFrame(execv2.CloseMode_CLOSE_MODE_GRACEFUL))
		if d := last(t, a).GetDetached(); d == nil || d.GetReason() != execv2.DetachReason_DETACH_REASON_REQUESTED {
			t.Fatalf("the stream that sent Detach and then Close was not told Detached{REQUESTED}")
		}
		// The Close was behind the Detach on a stream that had left: not applied. The session lives for its lease.
		live(t, "d")
		endSession(t, "d")
		ends["d"] = tombstone(t, "d")
	})

	// The same end, whichever way: the same End, the same counts. (The duration is the session's own.)
	normalize := func(e *execv2.Ended) *execv2.Ended {
		c := proto.Clone(e).(*execv2.Ended)
		c.DurationMs = 0
		return c
	}
	var first *execv2.Ended
	for _, name := range []string{"a", "b", "c", "d"} {
		e, ok := ends[name]
		if !ok {
			t.Fatalf("case (%s) did not run to its end", name)
		}
		if first == nil {
			first = normalize(e)
			continue
		}
		if got := normalize(e); !proto.Equal(first, got) {
			t.Errorf("case (%s) ended %v, case (a) ended %v: a Close and a Detach in either order leave the session in the same place", name, got, first)
		}
	}
	if first != nil {
		if closed := first.GetEnd().GetClosed(); closed.GetBy() != execv2.ClosedBy_CLOSED_BY_REQUEST || first.GetBytesOut() != raceOutput || first.GetDroppedBytes() != 0 {
			t.Errorf("the end is %v, want Closed by REQUEST with %d bytes out and none dropped", first, raceOutput)
		}
	}
}
