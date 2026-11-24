package exectest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The wire cases of the sessions that outlive their stream (FEATURE_SESSION_MANAGEMENT): detach and reattach (case 10), the lease
// (case 11), the atomic attach (case 21), Detach itself (case 22) and the pair P2 (attach after detach equals one uninterrupted
// Open). They run against the fake that carries the declared description, with scripts of their own, over a real transport. The
// lease runs on the real clock here (a lease of a millisecond, whose end is awaited as an event that has to happen, within the
// bound of a case); the engine's own tests drive a fake clock.

// pacedPattern is a script function that writes the pattern in the pieces a case grants it, one grant at a time, and then waits
// for its context: a session whose output is a function of the offset and whose pace the case decides, so that what a detached
// session holds in its ring can never overflow it.
func pacedPattern(grants <-chan int) exec.ExecFunc {
	return func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		var offset uint64
		for {
			select {
			case n := <-grants:
				if _, err := p.Stdout.Write(patternBytes(offset, n)); err != nil {
					return exec.Exit{}, err
				}
				offset += uint64(n)
			case <-ctx.Done():
				return exec.Exit{}, ctx.Err()
			}
		}
	}
}

// attach attaches a stream to a session from an offset; its stream is closed when the case ends.
func (r *wireRig) attach(id string, from uint64) (*wireSession, error) {
	req := &execv2.OpenRequest{SessionId: id, Kind: &execv2.OpenRequest_Attach{Attach: &execv2.Attach{FromOffset: from}}}
	conn, err := r.client.Open(r.request(), req)
	if err != nil {
		return nil, err
	}
	r.t.Cleanup(func() { _ = conn.Close() })
	return &wireSession{r: r, id: id, conn: conn, req: req}, nil
}

// mustAttach attaches a stream that has to be accepted.
func (r *wireRig) mustAttach(id string, from uint64) *wireSession {
	r.t.Helper()
	s, err := r.attach(id, from)
	if err != nil {
		r.t.Fatalf("attach to %q from %d: %v", id, from, err)
	}
	return s
}

// detach sends a Detach and reads the stream to its end: the last frame has to be Detached and the stream then ends with status OK.
func (s *wireSession) detach(leaseMs uint32) *execv2.Detached {
	t := s.r.t
	t.Helper()
	s.mustSend(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Detach{Detach: &execv2.Detach{LeaseMs: leaseMs}}})
	return s.untilDetached()
}

// untilDetached reads the stream to its end, which has to be a Detached frame and then io.EOF, and returns the Detached.
func (s *wireSession) untilDetached() *execv2.Detached {
	t := s.r.t
	t.Helper()
	var detached *execv2.Detached
	for {
		frame, err := s.recv()
		if errors.Is(err, io.EOF) {
			if detached == nil {
				t.Fatalf("session %q: the stream ended with status OK and no Detached", s.id)
			}
			return detached
		}
		if err != nil {
			t.Fatalf("session %q: the stream broke: %v", s.id, err)
		}
		if detached != nil {
			t.Fatalf("session %q: %s came after Detached: nothing follows it", s.id, frameName(frame))
		}
		if frame.GetEnded() != nil {
			t.Fatalf("session %q: the stream was sent Ended, want Detached: the session lives on", s.id)
		}
		detached = frame.GetDetached()
	}
}

// sessionEntry is the entry of a session in ListSessions, or nil.
func (r *wireRig) sessionEntry(id string) *execv2.SessionInfo {
	r.t.Helper()
	list, err := r.client.ListSessions(r.request())
	if err != nil {
		r.t.Fatalf("ListSessions: %v", err)
	}
	for _, info := range list.GetSessions() {
		if info.GetFacts().GetSessionId() == id {
			return info
		}
	}
	return nil
}

// readRing reads the ring of a session from an offset as a host does.
func (r *wireRig) readRing(id string, from uint64) *execv2.ReadOutputResponse {
	r.t.Helper()
	resp, err := r.client.ReadOutput(r.request(), &execv2.ReadOutputRequest{SessionId: id, FromOffset: from, MaxBytes: 1 << 20})
	if err != nil {
		r.t.Fatalf("ReadOutput(%q, %d): %v", id, from, err)
	}
	return resp
}

// replayOf takes the Opened of an attach and holds it to the contract: a replay of exactly end - start bytes that begins no earlier
// than the offset asked for.
func replayOf(t checkT, s *wireSession, from uint64) *execv2.Replay {
	t.Helper()
	r := s.conn.Opened().GetReplay()
	if r == nil || r.GetBytes() != r.GetEnd()-r.GetStart() || r.GetStart() < from || r.GetStart() > r.GetEnd() {
		t.Fatalf("session %q: the attach from %d was answered with replay %v, want bytes = end - start and a start no earlier than the offset asked for", s.id, from, r)
	}
	return r
}

// takePattern reads n bytes of the pattern that start at offset from a stream, holding every frame to its offset and its bytes.
func takePattern(t checkT, s *wireSession, offset uint64, n int) {
	t.Helper()
	for got := 0; got < n; {
		frame := s.next(fmt.Sprintf("%d more bytes at offset %d", n-got, offset+uint64(got)))
		out := frame.GetOutput()
		if out == nil {
			if frame.GetStarted() != nil || frame.GetNotice() != nil || frame.GetLink() != nil {
				continue
			}
			t.Fatalf("session %q: %s while waiting for output at %d", s.id, frameName(frame), offset+uint64(got))
		}
		if out.GetOffset() != offset+uint64(got) || !bytes.Equal(out.GetData(), patternBytes(out.GetOffset(), len(out.GetData()))) {
			t.Fatalf("session %q: an Output frame of %d bytes at offset %d, want the pattern from %d: a byte lost, repeated or wrong", s.id, len(out.GetData()), out.GetOffset(), offset+uint64(got))
		}
		got += len(out.GetData())
	}
}

// caseWireDetachReattach: a host that detaches and attaches again finds one uninterrupted transcript (what it had, then the replay
// from where it stopped, then the live stream), and a session that ended is readable from its tombstone.
func caseWireDetachReattach(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	if target.Info.Session.IsZero() {
		t.Skipf(noSessions)
	}
	rig := newWireRig(t, env, target)
	grants := make(chan int, 4)
	rig.scriptFunc("detach", pacedPattern(grants))
	id := rig.newID("detach")
	a := rig.mustOpen(id, "detach")
	grants <- 5000
	takePattern(t, a, 0, 5000)
	detached := a.detach(0)
	if detached.GetReason() != execv2.DetachReason_DETACH_REASON_REQUESTED || detached.GetLeaseExpiresUnixMs() <= time.Now().UnixMilli() {
		t.Errorf("the answer to Detach is %v, want reason REQUESTED and a lease that ends in the future", detached)
	}
	// The session lives on, detached, and goes on writing into its ring.
	grants <- 7000
	rig.await("the ring to hold what the detached session wrote", func() bool { return rig.readRing(id, 5000).GetNextOffset() == 12_000 })
	if entry := rig.sessionEntry(id); entry == nil || entry.GetAttached() || entry.GetEndedWith() != nil || entry.GetNextOffset() != 12_000 {
		t.Fatalf("ListSessions says %v of a detached session that wrote 12000 bytes", entry)
	}
	b := rig.mustAttach(id, 5000)
	if r := replayOf(t, b, 5000); r.GetStart() != 5000 || r.GetEnd() != 12_000 {
		t.Fatalf("the replay is %v, want [5000, 12000): what the host had ends at 5000", r)
	}
	takePattern(t, b, 5000, 7000)
	grants <- 3000 // the live stream goes on where the replay ended
	takePattern(t, b, 12_000, 3000)

	_ = b.closeSession(execv2.CloseMode_CLOSE_MODE_GRACEFUL)
	ended := b.finish()
	entry := rig.sessionEntry(id)
	if entry == nil || !proto.Equal(entry.GetEndedWith(), ended) {
		t.Fatalf("the tombstone is %v, want the Ended that the stream was sent: %v", entry, ended)
	}
	resp := rig.readRing(id, 0)
	var all []byte
	for _, c := range resp.GetChunks() {
		all = append(all, c.GetData()...)
	}
	if !resp.GetEnded() || !bytes.Equal(all, patternBytes(0, 15_000)) || resp.GetNextOffset() != 15_000 {
		t.Fatalf("ReadOutput of the tombstone: %d bytes, next %d, ended %v, want the 15000 bytes of the transcript", len(all), resp.GetNextOffset(), resp.GetEnded())
	}
}

// caseWireLease: a session that no stream is attached to ends when its lease runs out, closed by DETACH_TIMEOUT, and no stream is
// sent an Ended: the end is in the tombstone. A stream that just breaks is a detach with the session's own lease.
func caseWireLease(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	if target.Info.Session.IsZero() {
		t.Skipf(noSessions)
	}
	rig := newWireRig(t, env, target)
	rig.script("idle", Block())
	id := rig.newID("lease")
	s := rig.mustOpen(id, "idle", func(st *execv2.Start) { st.DetachTimeoutMs = 1 })
	_ = s.conn.Close() // the stream breaks: an implicit detach, with the lease the session was started with (1 ms)
	rig.await("the lease to end the session", func() bool {
		entry := rig.sessionEntry(id)
		return entry != nil && entry.GetEndedWith() != nil
	})
	entry := rig.sessionEntry(id)
	if closed := entry.GetEndedWith().GetEnd().GetClosed(); closed.GetBy() != execv2.ClosedBy_CLOSED_BY_DETACH_TIMEOUT {
		t.Fatalf("the session ended %v, want Closed by DETACH_TIMEOUT", entry.GetEndedWith().GetEnd())
	}
	if _, err := rig.attach(id, 0); err == nil {
		t.Fatalf("an attach to a session whose lease ran out was accepted, want NOT_FOUND / SESSION_UNKNOWN: a tombstone is not a session")
	}
	// A lease runs from the moment of the detach, and an attach before it ends it keeps the session. The tombstone of the first session is made
	// before its place is freed, so a target that serves one session at a time may refuse the next open with LIMIT for a moment after the host
	// has read the tombstone: the second session is opened once the place is free.
	again := rig.newID("lease")
	rig.mustOpenEventually(again, "idle", func(st *execv2.Start) { st.DetachTimeoutMs = 60_000 }).detach(0)
	live := rig.mustAttach(again, 0)
	live.detach(0)
	if entry := rig.sessionEntry(again); entry == nil || entry.GetEndedWith() != nil || entry.GetLeaseExpiresUnixMs() == 0 {
		t.Fatalf("a session that was detached for 60 s is %v after an attach and a detach, want live with a lease running", entry)
	}
}

// caseWireAtomicAttach: an attach in the middle of a flood answers with a replay of exactly end - start bytes, which are the bytes of
// the transcript from start, and the live stream goes on at end with no byte lost and none repeated.
func caseWireAtomicAttach(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	if target.Info.Session.IsZero() {
		t.Skipf(noSessions)
	}
	rig := newWireRig(t, env, target)
	rig.scriptFunc("flood", func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		for offset := uint64(0); ; offset += 16 << 10 {
			if _, err := p.Stdout.Write(patternBytes(offset, 16<<10)); err != nil {
				return exec.Exit{}, err
			}
			if ctx.Err() != nil {
				return exec.Exit{}, ctx.Err()
			}
		}
	})
	id := rig.newID("atomic")
	rig.mustOpen(id, "flood")
	var have uint64
	for i := 0; i < 20; i++ {
		s := rig.mustAttach(id, have)
		r := replayOf(t, s, have)
		takePattern(t, s, r.GetStart(), int(r.GetBytes())) // exactly the bytes Opened describes, and then the live stream
		have = r.GetEnd()
		for live := 0; live < 40<<10; {
			frame := s.next("the live stream")
			switch {
			case frame.GetOutput() != nil:
				out := frame.GetOutput()
				if out.GetOffset() < have || !bytes.Equal(out.GetData(), patternBytes(out.GetOffset(), len(out.GetData()))) {
					t.Fatalf("attach %d: an Output frame at %d after the replay ended at %d, or with the wrong bytes", i, out.GetOffset(), have)
				}
				if out.GetOffset() > have && rig.info.Capabilities.Stall != exec.StallLossy {
					t.Fatalf("attach %d: the live stream begins at %d, the replay ended at %d: a byte was lost", i, out.GetOffset(), have)
				}
				have = out.GetOffset() + uint64(len(out.GetData()))
				live += len(out.GetData())
			case frame.GetDropped() != nil:
				if rig.info.Capabilities.Stall != exec.StallLossy {
					t.Fatalf("attach %d: a target that does not stall lossily dropped %v", i, frame.GetDropped())
				}
				have = frame.GetDropped().GetFromOffset() + frame.GetDropped().GetBytes()
			case frame.GetEnded() != nil:
				t.Fatalf("attach %d: the session ended", i)
			}
		}
	}
}

// caseWireDetach: Detach ends the stream, and not the session; Detached is its last frame; a lease above the maximum is clamped and
// the answer says what was applied; the stream that is superseded ends with Detached too; and every stream ends with exactly one of
// Ended or Detached, the session having one Ended.
func caseWireDetach(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	if target.Info.Session.IsZero() {
		t.Skipf(noSessions)
	}
	rig := newWireRig(t, env, target)
	rig.script("idle", Block())
	maxLease := rig.described().GetSession().GetMaxDetachTimeoutMs()
	id := rig.newID("detach")
	a := rig.mustOpen(id, "idle")
	before := time.Now().UnixMilli()
	detached := a.detach(maxLease + 3_600_000) // an hour more than the target allows
	after := time.Now().UnixMilli()
	if got := detached.GetLeaseExpiresUnixMs(); got < before+int64(maxLease) || got > after+int64(maxLease) {
		t.Errorf("a lease above the maximum ends at %d, want it clamped to the maximum of %d ms (between %d and %d)", got, maxLease, before+int64(maxLease), after+int64(maxLease))
	}
	b := rig.mustAttach(id, 0)
	c := rig.mustAttach(id, 0) // supersedes b
	superseded := b.untilDetached()
	if superseded.GetReason() != execv2.DetachReason_DETACH_REASON_SUPERSEDED || superseded.GetLeaseExpiresUnixMs() != 0 {
		t.Errorf("the superseded stream was sent %v, want Detached{SUPERSEDED} with no lease", superseded)
	}
	_ = c.closeSession(execv2.CloseMode_CLOSE_MODE_GRACEFUL)
	if ended := c.finish(); ended.GetEnd().GetClosed().GetBy() != execv2.ClosedBy_CLOSED_BY_REQUEST {
		t.Errorf("the session ended with %v, want Closed by REQUEST on the stream that closed it", ended.GetEnd())
	}
}

// pairReplayName is the name of the pair P2.
const pairReplayName = "pair P2: Open, detach, attach(from_offset) equals one uninterrupted Open"

// casePairReplay: a session that is detached and attached again at many moments delivers the transcript that an uninterrupted
// Open delivers: the same bytes at the same offsets (a SHA-256 over each), with replay.bytes = end - start every time.
func casePairReplay(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	if target.Info.Session.IsZero() {
		t.Skipf(noSessions)
	}
	rig := newWireRig(t, env, target)
	const steps, piece = 24, 20_000
	run := func(name string, leave func(i int, s *wireSession, have uint64) *wireSession) [32]byte {
		grants := make(chan int, 1)
		rig.scriptFunc(name, pacedPattern(grants))
		id := rig.newID(name)
		s := rig.mustOpen(id, name)
		digest, have := sha256.New(), uint64(0)
		for i := 0; i < steps; i++ {
			grants <- piece
			takePattern(t, s, have, piece)
			digest.Write(patternBytes(have, piece))
			have += piece
			s = leave(i, s, have)
		}
		var sum [32]byte
		copy(sum[:], digest.Sum(nil))
		// The session ends before the next one opens, and its place is free by the time the host has its Ended: a target that serves one
		// session at a time admits the next, where a session that was left running would refuse it with LIMIT / TOO_MANY_SESSIONS.
		_ = s.closeSession(execv2.CloseMode_CLOSE_MODE_GRACEFUL)
		s.finish()
		return sum
	}
	whole := run("whole", func(_ int, s *wireSession, _ uint64) *wireSession { return s })
	var attaches int
	pieced := run("pieced", func(i int, s *wireSession, have uint64) *wireSession {
		switch i % 3 {
		case 0: // Detach, then attach
			s.detach(0)
		case 1: // the stream breaks
			_ = s.conn.Close()
		default: // supersede it
			next := rig.mustAttach(s.id, have)
			replayOf(t, next, have)
			attaches++
			return next
		}
		next := rig.mustAttach(s.id, have)
		if r := replayOf(t, next, have); r.GetStart() != have || r.GetEnd() != have {
			t.Fatalf("an attach at %d of a session that wrote nothing since: replay %v, want an empty replay at %d", have, r, have)
		}
		attaches++
		return next
	})
	if whole != pieced {
		t.Fatalf("the transcript of the session that was detached and attached %d times differs from the one of an uninterrupted Open (SHA-256 %x and %x)", attaches, pieced[:6], whole[:6])
	}
}
