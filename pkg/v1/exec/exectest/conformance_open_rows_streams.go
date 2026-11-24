package exectest

import (
	"errors"
	"io"
	"slices"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The half of case 25 for the streams of one session. A session has at most one stream attached, and the stream is one attachment of it:
// a Detach, a second stream that attaches, and a host that goes away each end a stream without ending the session. Every stream that can
// still receive frames ends with exactly one of two frames and nothing follows it: Ended, the end of the session, to the stream that is
// attached when the session ends, and Detached, the end of that stream alone, to the ones that left (REQUESTED with the lease that was
// applied, or SUPERSEDED with none). A session has at most one Ended frame, however many streams it had: the stream that was told Detached
// is never told Ended, whenever the session ends, and a session that ends while no stream is attached has none.
//
// What the cases hold is the end state of each stream and of the session, so the operations that race (an attach against an attach, a
// close against an attach, an EndSession against an attach) may come in either order and only the outcomes that the contract allows are
// accepted. The oracle is what the host did, never what the plugin answered.

// streamLast is how a stream that was read to its end ended: with an Ended, with a Detached, or, when the plugin broke the contract, with
// neither or both.
type streamLast struct {
	ended    *execv2.Ended
	detached *execv2.Detached
}

// last reads a stream to its end, which has to be exactly one last frame, Ended or Detached, with nothing after it, and then io.EOF. The
// other checks of a stream's frames (the order of the frames before it, the audit of its output) are made as they arrive and reported
// here. A stream that broke is a failure of the case, and a stream whose host broke it is not read with this.
func (s *wireSession) last() streamLast {
	t := s.r.t
	t.Helper()
	var out streamLast
	for {
		frame, err := s.recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("session %q: the stream broke after %d frames: %v", s.id, s.grammar.frames, err)
		}
		if e := frame.GetEnded(); e != nil {
			out.ended = e
		}
		if d := frame.GetDetached(); d != nil {
			out.detached = d
		}
	}
	for _, problem := range s.grammar.problems {
		t.Errorf("session %q: %s", s.id, problem)
	}
	if s.out != nil {
		for _, problem := range s.out.problems {
			t.Errorf("session %q: %s", s.id, problem)
		}
	}
	if g := &s.grammar; g.endeds+g.detacheds == 0 {
		t.Errorf("session %q: the stream ended with status OK after %d frames and no Ended or Detached: a stream that can still receive frames ends with exactly one of them", s.id, g.frames)
	}
	return out
}

// wantDetached holds the last frame of a stream that left to be Detached for the reason it left, and returns it: an Ended never goes to
// a stream that was detached or superseded, whenever the session ends.
func (s *wireSession) wantDetached(name string, last streamLast, reason execv2.DetachReason) *execv2.Detached {
	t := s.r.t
	t.Helper()
	switch {
	case last.ended != nil:
		t.Errorf("%s: session %q: the stream that left was sent Ended, want Detached (%s): an Ended goes only to the stream that is attached when the session ends, never to one that was detached or superseded", name, s.id, reason)
		return nil
	case last.detached == nil:
		t.Errorf("%s: session %q: the stream that left has no Detached, want one (%s): it ends with exactly one of Ended or Detached", name, s.id, reason)
		return nil
	case last.detached.GetReason() != reason:
		t.Errorf("%s: session %q: Detached has the reason %s, want %s", name, s.id, last.detached.GetReason(), reason)
	}
	return last.detached
}

// wantSuperseded holds the last frame of a stream that another stream took the session from to be Detached with the reason SUPERSEDED and
// no lease: the session is attached to the other stream, so none runs.
func (s *wireSession) wantSuperseded(name string, last streamLast) {
	t := s.r.t
	t.Helper()
	d := s.wantDetached(name, last, execv2.DetachReason_DETACH_REASON_SUPERSEDED)
	if d != nil && d.GetLeaseExpiresUnixMs() != 0 {
		t.Errorf("%s: session %q: Detached{SUPERSEDED} says the lease ends at %d, want no lease: the session is attached to the stream that superseded this one", name, s.id, d.GetLeaseExpiresUnixMs())
	}
}

// wantEndedLast holds the last frame of the stream that was attached when its session ended to be Ended, and returns it: the end of the
// session goes to the stream that has it, and that stream is not told it left.
func (s *wireSession) wantEndedLast(name string, last streamLast) *execv2.Ended {
	t := s.r.t
	t.Helper()
	switch {
	case last.detached != nil:
		t.Errorf("%s: session %q: the stream that was attached when the session ended was sent Detached, want Ended: it did not leave", name, s.id)
		return nil
	case last.ended == nil:
		t.Errorf("%s: session %q: the stream that was attached when the session ended has no Ended, want one", name, s.id)
		return nil
	}
	return last.ended
}

// endedFrames adds up the Ended frames that the streams of one session were sent: a session has at most one, and the one it has is the
// stream that held it when it ended.
func endedFrames(streams ...*wireSession) int {
	n := 0
	for _, s := range streams {
		n += s.grammar.endeds
	}
	return n
}

// holdSession opens a session that writes "up" and waits to be ended, and reads "up": the function runs, and its first stream holds it.
func (r *wireRig) holdSession(mods ...func(*execv2.Start)) *wireSession {
	r.t.Helper()
	s := r.mustOpen(r.newID("stream"), "streams-hold", mods...)
	s.readBytes(len("up"))
	return s
}

// closedByRequest is the end of a session that a host closed, as the declared kind says: what a stream attached at the end is told, and
// what a tombstone says.
func (r *wireRig) closedByRequest() wantEnded {
	return wantEnded{end: endClosed(execv2.ClosedBy_CLOSED_BY_REQUEST, r.info.Capabilities.Kill), started: true, bytesOut: uint64(len("up"))}
}

// wantEndHeld holds the Ended that a stream was sent to the end of a session that was closed by its host, and the tombstone of the session
// to the same Ended: a session has one end, and it was told to one stream.
func (r *wireRig) wantEndHeld(name string, s *wireSession, ended *execv2.Ended) {
	t := r.t
	t.Helper()
	if ended == nil {
		return
	}
	for _, problem := range endedProblems(ended, r.tty(), r.closedByRequest(), nil) {
		t.Errorf("%s: session %q: %s", name, s.id, problem)
	}
	entry := r.sessionEntry(s.id)
	if entry == nil || !proto.Equal(entry.GetEndedWith(), ended) {
		t.Errorf("%s: session %q: the tombstone says %s, the stream was told %s: a session has one end, and the frame and the tombstone say it alike",
			name, s.id, endedText(entry.GetEndedWith()), endedText(ended))
	}
}

// checkStreamsOfOneSession is what case 25 holds of the streams of one session.
func (r *wireRig) checkStreamsOfOneSession() {
	r.t.Helper()
	r.script("streams-hold", WriteOut("up"), Block())
	r.checkDetachedStream()
	r.checkSupersededStream()
	r.checkAChainOfStreams()
	r.checkManyAttachesAtOnce()
	r.checkFramesOfOneStreamInOrder()
	r.checkStreamsThatCannotRead()
	r.checkEndsRaceAttaches()
}

// checkDetachedStream: a Detach ends the stream, and not the session: the stream's last frame is Detached{REQUESTED} with the lease that was
// applied (the one the host asked for, the session's own when it asked for none, never above the target's maximum), and the session lives
// with no stream until it is ended by something else, which ends it with no Ended anywhere.
func (r *wireRig) checkDetachedStream() {
	t := r.t
	t.Helper()
	limits := r.described().GetSession()
	own, most := r.ownLease(), limits.GetMaxDetachTimeoutMs()
	// Leases other than the target's default that the target allows, to tell what was asked for from what the target gives anyway.
	var others []uint32
	for _, lease := range []uint32{most, most / 2, most / 3, most / 5} {
		if lease >= 2 && lease != own && lease <= most && !slices.Contains(others, lease) {
			others = append(others, lease)
		}
	}
	type detach struct {
		name       string
		start, ask uint32
		applied    uint32
	}
	cases := []detach{
		{"a Detach that asks for no lease", 0, 0, own},
		{"a Detach that asks for more than the target allows", 0, most + 3_600_000, most},
	}
	if len(others) > 0 {
		cases = append(cases,
			detach{"a Detach that asks for a lease", 0, others[0], others[0]},
			detach{"a Detach that asks for no lease, of a session started with its own", others[0], 0, others[0]})
	}
	if len(others) > 1 {
		cases = append(cases, detach{"a Detach that asks for a lease, of a session started with its own", others[0], others[1], others[1]})
	}
	for _, tc := range cases {
		s := r.holdSession(func(st *execv2.Start) { st.DetachTimeoutMs = tc.start })
		before := time.Now()
		s.mustSend(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Detach{Detach: &execv2.Detach{LeaseMs: tc.ask}}})
		last := s.last()
		after := time.Now()
		if d := s.wantDetached(tc.name, last, execv2.DetachReason_DETACH_REASON_REQUESTED); d != nil && !leaseWindow(d.GetLeaseExpiresUnixMs(), before, after, tc.applied) {
			t.Errorf("%s: session %q: Detached says the lease ends at %d, want it %d ms after the Detach (asked for %d, started with %d), between %d and %d", tc.name, s.id,
				d.GetLeaseExpiresUnixMs(), tc.applied, tc.ask, tc.start, before.UnixMilli()+int64(tc.applied), after.UnixMilli()+int64(tc.applied))
		}
		// The session lives, with no stream, until the lease runs out: a host that has read the lease can rely on it.
		entry := r.sessionEntry(s.id)
		if d := last.detached; d != nil && time.Now().UnixMilli() < d.GetLeaseExpiresUnixMs() &&
			(entry == nil || entry.GetEndedWith() != nil || entry.GetAttached() || entry.GetLeaseExpiresUnixMs() != d.GetLeaseExpiresUnixMs()) {
			t.Errorf("%s: session %q: the listing says %v of a session that was detached with a lease that runs until %d: it lives with no stream, for its lease", tc.name, s.id, entry, d.GetLeaseExpiresUnixMs())
		}
		// Ending it with no stream attached has no Ended to go to: the host that left is not told, and the tombstone has the end.
		endsSession(execv2.CloseMode_CLOSE_MODE_GRACEFUL)(r, s)
		r.checkTombstone(tc.name, s, endScenario{name: tc.name, end: r.closedByRequest().end, started: true, output: "up"}, r.sessionEntry(s.id))
	}
}

// checkSupersededStream: a second stream that attaches takes the session from the first, which is told Detached{SUPERSEDED}, with no lease,
// as its last frame; the session is attached to the second, and what the first sends afterwards is not the session's: it does not end it.
// The end then goes to the second stream alone.
func (r *wireRig) checkSupersededStream() {
	t := r.t
	t.Helper()
	const name = "a stream superseded by another"
	a := r.holdSession()
	b := r.mustAttach(a.id, 0)
	// What a stream that is superseded sends is not the session's: a Close from it, which comes after the attach that took the session
	// from it, ends nothing.
	_ = a.closeSession(execv2.CloseMode_CLOSE_MODE_KILL)
	a.wantSuperseded(name, a.last())
	b.readBytes(len("up")) // the replay
	if entry := r.sessionEntry(a.id); entry == nil || entry.GetEndedWith() != nil || !entry.GetAttached() || entry.GetLeaseExpiresUnixMs() != 0 {
		t.Errorf("%s: session %q: the listing says %v after another stream took the session over: it lives, attached to that stream, with no lease", name, a.id, entry)
	}
	if err := r.endSession(a.id, execv2.CloseMode_CLOSE_MODE_GRACEFUL); !isRefusal(err, exec.ErrorClassConflict, exec.ErrorReasonUnspecified) {
		t.Errorf("%s: session %q: EndSession of a session whose stream is attached answered %v, want CONFLICT: the host closes it on its stream", name, a.id, err)
	}
	_ = b.closeSession(execv2.CloseMode_CLOSE_MODE_GRACEFUL)
	ended := b.wantEndedLast(name, b.last())
	r.wantEndHeld(name, b, ended)
	if got := endedFrames(a, b); got != 1 {
		t.Errorf("%s: session %q: its streams were sent %d Ended frames, want one: the stream it ended on, and not the one it left", name, a.id, got)
	}
}

// checkAChainOfStreams: a session that goes from stream to stream, left by a Detach, taken over by an attach, abandoned by a host that
// broke its stream, and attached again, tells each stream that left how it left and the last stream the end, once.
func (r *wireRig) checkAChainOfStreams() {
	t := r.t
	t.Helper()
	const name = "a session that changes hands"
	// The first stream leaves with a Detach.
	a := r.holdSession()
	a.mustSend(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Detach{Detach: &execv2.Detach{}}})
	a.wantDetached(name, a.last(), execv2.DetachReason_DETACH_REASON_REQUESTED)
	// The second takes the session from nobody, and the third takes it from the second.
	b := r.mustAttach(a.id, 0)
	b.readBytes(len("up"))
	c := r.mustAttach(a.id, 0)
	b.wantSuperseded(name, b.last())
	c.readBytes(len("up"))
	// The third breaks: nobody holds the session.
	_ = c.conn.Close()
	r.await("the session to have no stream after the third stream broke", func() bool {
		entry := r.sessionEntry(a.id)
		return entry != nil && !entry.GetAttached()
	})
	d := r.mustAttach(a.id, 0)
	d.readBytes(len("up"))
	_ = d.closeSession(execv2.CloseMode_CLOSE_MODE_GRACEFUL)
	ended := d.wantEndedLast(name, d.last())
	r.wantEndHeld(name, d, ended)
	if got := endedFrames(a, b, c, d); got != 1 {
		t.Errorf("%s: session %q: its streams were sent %d Ended frames, want one: it has one end, and it goes to the stream that held the session", name, a.id, got)
	}
	if a.grammar.detacheds != 1 || b.grammar.detacheds != 1 || d.grammar.detacheds != 0 {
		t.Errorf("%s: session %q: the streams that left were sent %d, %d Detached frames and the stream that held it %d, want 1, 1 and 0", name, a.id, a.grammar.detacheds, b.grammar.detacheds, d.grammar.detacheds)
	}
}

// attachResult is what one host saw of an attach that raced others.
type attachResult struct {
	s   *wireSession
	err error
}

// checkManyAttachesAtOnce: several hosts attach to one session at the same moment. Each attach takes the session from the stream before it,
// so one stream is left with it and the others were superseded: whichever the order, every stream but one is told Detached{SUPERSEDED}, one
// is told the end when the session is closed, and the session has one Ended. The streams are closed from every host at once, and only the
// stream that holds the session is heard.
func (r *wireRig) checkManyAttachesAtOnce() {
	t := r.t
	t.Helper()
	const name = "attaches at the same moment"
	const hosts = 4
	first := r.holdSession()
	start := make(chan struct{})
	results := make(chan attachResult, hosts)
	for range hosts {
		go func() {
			<-start
			s, err := r.attach(first.id, 0)
			results <- attachResult{s, err}
		}()
	}
	close(start)
	streams := []*wireSession{first}
	for range hosts {
		select {
		case res := <-results:
			if res.err != nil {
				t.Fatalf("%s: an attach to a session that lives was refused with %v", name, res.err)
			}
			streams = append(streams, res.s)
		case <-time.After(caseBound):
			t.Fatalf("%s: an attach did not answer within %v", name, caseBound)
		}
	}
	// Every attach has its Opened, so each has taken the session and none of the streams before the last of them holds it. Every host
	// closes the session on its stream; the plugin hears the one that holds it.
	for _, s := range streams {
		_ = s.closeSession(execv2.CloseMode_CLOSE_MODE_GRACEFUL)
	}
	var held []*wireSession
	for _, s := range streams {
		last := s.last()
		if last.ended != nil {
			held = append(held, s)
			r.wantEndHeld(name, s, last.ended)
			continue
		}
		s.wantSuperseded(name, last)
	}
	if len(held) != 1 || endedFrames(streams...) != 1 {
		t.Errorf("%s: session %q: %d of its %d streams were sent Ended, want one: every stream but the one that holds the session is told it was superseded", name, first.id, len(held), len(streams))
	}
}

// checkFramesOfOneStreamInOrder: the frames of one stream are applied in the order they were sent, so what a Close and a Detach that are sent
// one behind the other do is decided by their order. A Close and then a Detach end the session, and the stream is told Ended and not
// Detached; a Detach and then a Close leave the session running, for the stream has left and what it sends then is not the session's, and
// the stream is told Detached and never Ended; a second Detach is moot.
func (r *wireRig) checkFramesOfOneStreamInOrder() {
	t := r.t
	t.Helper()
	detach := &execv2.ClientFrame{Frame: &execv2.ClientFrame_Detach{Detach: &execv2.Detach{}}}

	const closeFirst = "a Close and then a Detach"
	s := r.holdSession()
	_ = s.closeSession(execv2.CloseMode_CLOSE_MODE_GRACEFUL)
	_ = s.send(detach)
	ended := s.wantEndedLast(closeFirst, s.last())
	r.wantEndHeld(closeFirst, s, ended)

	const detachFirst = "a Detach and then a Close"
	s = r.holdSession()
	s.mustSend(detach)
	_ = s.closeSession(execv2.CloseMode_CLOSE_MODE_GRACEFUL)
	s.wantDetached(detachFirst, s.last(), execv2.DetachReason_DETACH_REASON_REQUESTED)
	// The session lives: a stream can still attach to it, which a session that was ending would refuse, and the end goes to that stream.
	b, err := r.attach(s.id, 0)
	if err != nil {
		t.Errorf("%s: session %q: an attach to the session of a stream that detached and then closed was refused with %v, want it admitted: a stream that left does not end the session", detachFirst, s.id, err)
	} else {
		b.readBytes(len("up"))
		_ = b.closeSession(execv2.CloseMode_CLOSE_MODE_GRACEFUL)
		r.wantEndHeld(detachFirst, b, b.wantEndedLast(detachFirst, b.last()))
		if got := endedFrames(s, b); got != 1 {
			t.Errorf("%s: session %q: its streams were sent %d Ended frames, want one", detachFirst, s.id, got)
		}
	}

	const twice = "a Detach and then another"
	s = r.holdSession()
	s.mustSend(detach)
	_ = s.send(detach)
	s.wantDetached(twice, s.last(), execv2.DetachReason_DETACH_REASON_REQUESTED)
	endsSession(execv2.CloseMode_CLOSE_MODE_GRACEFUL)(r, s)
	if s.grammar.endeds != 0 {
		t.Errorf("%s: session %q: the stream that left was sent an Ended", twice, s.id)
	}
}

// checkStreamsThatCannotRead: a host whose stream is held in a send, because it reads nothing, is still told how its stream ended when it
// reads again, however long ago the session ended or changed hands: Detached when it left or was superseded, never Ended. The transport
// holds the frames of such a stream back, so the plugin has to decide at the moment its stream leaves and not at the moment the frame is
// sent.
func (r *wireRig) checkStreamsThatCannotRead() {
	t := r.t
	t.Helper()
	var accepted atomic.Int64
	const total = 768 << 10
	r.script("streams-flood", floodPattern(total, &accepted), Block())
	fill := func(name string) *wireSession {
		accepted.Store(0)
		s := r.mustOpenOn(r.slow(), r.newID("slow"), "streams-flood")
		s.audit(patternByte)
		r.await(name+": the flood to fill the queue and the window of a host that reads nothing", func() bool { return accepted.Load() >= 320<<10 })
		return s
	}

	const detached = "a stream held in a send that detaches, and then its session ends"
	a := fill(detached)
	a.mustSend(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Detach{Detach: &execv2.Detach{}}})
	r.await(detached+": the session to have no stream, though its stream is held in a send", func() bool {
		entry := r.sessionEntry(a.id)
		return entry != nil && !entry.GetAttached()
	})
	if err := r.endSession(a.id, execv2.CloseMode_CLOSE_MODE_GRACEFUL); err != nil {
		t.Fatalf("%s: session %q: EndSession of a session that nobody holds: %v", detached, a.id, err)
	}
	a.wantDetached(detached, a.last(), execv2.DetachReason_DETACH_REASON_REQUESTED)

	const superseded = "a stream held in a send that is superseded, and then its session ends"
	a = fill(superseded)
	b := r.mustAttach(a.id, 0)
	_ = b.closeSession(execv2.CloseMode_CLOSE_MODE_GRACEFUL)
	if ended := b.wantEndedLast(superseded, b.last()); ended != nil {
		if entry := r.sessionEntry(b.id); entry == nil || !proto.Equal(entry.GetEndedWith(), ended) {
			t.Errorf("%s: session %q: the tombstone says %s, the stream was told %s", superseded, b.id, endedText(entry.GetEndedWith()), endedText(ended))
		}
	}
	a.wantSuperseded(superseded, a.last())
	if got := endedFrames(a, b); got != 1 {
		t.Errorf("%s: session %q: its streams were sent %d Ended frames, want one", superseded, a.id, got)
	}
}

// checkEndsRaceAttaches: a Close from the host that holds a session and an attach by another host that wants it, and an EndSession against an
// attach, are sent at the same moment, many times, and either may come first. The outcomes that the contract allows are two for each: the
// attach is admitted, so the stream that held the session was superseded and is told Detached and the session lives (its Close was not the
// session's, and EndSession is refused for the stream that is attached); or the end came first, so the attach is refused as a session that
// does not exist, and the session ended with one Ended on the stream that held it, or none anywhere for a session that nobody held. The
// outcomes that are never allowed are the ones where both succeeded, where a stream that left was told Ended, and where the session has two
// ends.
func (r *wireRig) checkEndsRaceAttaches() {
	t := r.t
	t.Helper()
	const rounds = 24
	wonByClose, wonByEndSession := 0, 0 // in how many rounds the attach came first, which the order of the race decides and the contract allows
	for round := range rounds {
		const closeName = "a Close and an attach at the same moment"
		a := r.holdSession()
		start := make(chan struct{})
		sent, attached := make(chan struct{}), make(chan attachResult, 1)
		go func() {
			<-start
			_ = a.conn.Send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Close{Close: &execv2.Close{Mode: execv2.CloseMode_CLOSE_MODE_GRACEFUL}}})
			close(sent)
		}()
		go func() {
			<-start
			s, err := r.attach(a.id, 0)
			attached <- attachResult{s, err}
		}()
		close(start)
		var res attachResult
		select {
		case res = <-attached:
		case <-time.After(caseBound):
			t.Fatalf("%s: round %d: the attach did not answer within %v", closeName, round, caseBound)
		}
		select {
		case <-sent:
		case <-time.After(caseBound):
			t.Fatalf("%s: round %d: the plugin took no Close frame within %v", closeName, round, caseBound)
		}
		lastA := a.last()
		if res.err == nil {
			// The attach came first: the Close of the stream that was superseded is not the session's.
			wonByClose++
			a.wantSuperseded(closeName, lastA)
			_ = res.s.closeSession(execv2.CloseMode_CLOSE_MODE_GRACEFUL)
			r.wantEndHeld(closeName, res.s, res.s.wantEndedLast(closeName, res.s.last()))
			if got := endedFrames(a, res.s); got != 1 {
				t.Errorf("%s: round %d: session %q: its streams were sent %d Ended frames, want one", closeName, round, a.id, got)
			}
			continue
		}
		// The Close came first: the session ended, with the Ended on the stream that held it, and the attach is a refusal.
		if !isRefusal(res.err, exec.ErrorClassNotFound, exec.ErrorReasonSessionUnknown) {
			t.Errorf("%s: round %d: the attach was refused with %v, want NOT_FOUND / SESSION_UNKNOWN", closeName, round, res.err)
		}
		r.wantEndHeld(closeName, a, a.wantEndedLast(closeName, lastA))
		if got := endedFrames(a); got != 1 {
			t.Errorf("%s: round %d: session %q: its stream was sent %d Ended frames, want one", closeName, round, a.id, got)
		}
	}

	for round := range rounds {
		const endName = "an EndSession and an attach at the same moment"
		a := r.holdSession()
		_ = a.conn.Close()
		r.await(endName+": the session to have no stream after its host left it", func() bool {
			entry := r.sessionEntry(a.id)
			return entry != nil && !entry.GetAttached()
		})
		start := make(chan struct{})
		ended, attached := make(chan error, 1), make(chan attachResult, 1)
		go func() {
			<-start
			ended <- r.endSession(a.id, execv2.CloseMode_CLOSE_MODE_GRACEFUL)
		}()
		go func() {
			<-start
			s, err := r.attach(a.id, 0)
			attached <- attachResult{s, err}
		}()
		close(start)
		var res attachResult
		var endErr error
		for range 2 {
			select {
			case res = <-attached:
			case endErr = <-ended:
			case <-time.After(caseBound):
				t.Fatalf("%s: round %d: an operation did not answer within %v", endName, round, caseBound)
			}
		}
		switch {
		case res.err == nil && endErr == nil:
			t.Errorf("%s: round %d: session %q: both the attach and EndSession succeeded: a session that a stream holds is not ended by EndSession, and one that EndSession ended is not attached", endName, round, a.id)
		case res.err == nil:
			// The attach came first, and EndSession is refused for the stream that is attached.
			wonByEndSession++
			if !isRefusal(endErr, exec.ErrorClassConflict, exec.ErrorReasonUnspecified) {
				t.Errorf("%s: round %d: EndSession of a session with a stream attached answered %v, want CONFLICT", endName, round, endErr)
			}
			_ = res.s.closeSession(execv2.CloseMode_CLOSE_MODE_GRACEFUL)
			r.wantEndHeld(endName, res.s, res.s.wantEndedLast(endName, res.s.last()))
		default:
			// EndSession came first: the attach is a refusal, and the session ended with nobody to tell.
			if endErr != nil || !isRefusal(res.err, exec.ErrorClassNotFound, exec.ErrorReasonSessionUnknown) {
				t.Errorf("%s: round %d: EndSession answered %v and the attach %v, want EndSession to have ended the session and the attach to be NOT_FOUND / SESSION_UNKNOWN", endName, round, endErr, res.err)
			}
			entry := r.sessionEntry(a.id)
			if entry == nil || entry.GetEndedWith() == nil {
				t.Errorf("%s: round %d: session %q: EndSession answered and the session has no tombstone (%v)", endName, round, a.id, entry)
			}
		}
	}
	t.Logf("of %d rounds, the attach came first in %d of a Close against an attach and in %d of an EndSession against an attach: either order is allowed", rounds, wonByClose, wonByEndSession)
}
