package exectest

import (
	"context"
	"fmt"
	"slices"
	"time"

	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The half of case 9 for a session that ends while no stream is attached. Such a session has no Ended frame, in any stream, because
// there is no stream to carry one: its end is the tombstone's, which the host reads with ListSessions (SessionInfo.ended_with) and the
// bytes it left with ReadOutput. The kinds of end are the ones the attached half checks, typed as the declaration says, with the same
// counts, completeness and survival, and one that only a session with no stream can have: the lease that runs out.
//
// The host leaves the session, and then the session ends. The order is the case's, and it is a barrier and not a delay: a function that
// ends by itself waits at a gate until the host has left, a session that is ended by the host is ended with EndSession, which needs a
// session with no stream, and the lease runs on the clock of the plugin (one millisecond, awaited as an event that has to happen).
//
// What a host reads of a session while it has no stream is read of a lease that outlives the case: the session's own, which is the
// target's default, or one that the host asks for, which is a minute. A lease of a millisecond is over before a host that is not
// hurried has asked, and for as long as the plugin takes to end the session its listing says that it has no stream, no lease and no
// end, whatever the pace of the machine decides. What the host holds such a plugin to is the end alone, read from the tombstone that
// the host awaits, and it reads nothing of the session before.

// leaving is how a host leaves a session that runs.
type leaving int

const (
	// leavesByDetach is a Detach frame: the stream ends with Detached{REQUESTED} as its last frame, which carries the lease applied.
	leavesByDetach leaving = iota
	// leavesByBreak is the host closing its stream: an implicit Detach with the session's own lease, which nothing on the stream
	// confirms. The listing says when nobody is attached.
	leavesByBreak
)

func (l leaving) String() string {
	if l == leavesByDetach {
		return "detaches"
	}
	return "breaks its stream"
}

// leaseKind is the lease a host leaves a session with.
type leaseKind int

const (
	// leaseOwn asks for none: the session's own, which is the target's default and never above its maximum, minutes that outlive the case.
	leaseOwn leaseKind = iota
	// leaseAsked is a lease that the host asks for, and that outlives the case as well: a minute, or the target's maximum when that is
	// shorter. It is Detach.lease_ms for a stream that leaves, and Start.detach_timeout_ms, the session's own, for one that breaks.
	leaseAsked
	// leaseRunsOut is the shortest lease that the wire carries, a millisecond: the scenario is the end of its lease. The lease is over
	// before a host can be counted on to have read the session, so the scenario holds the plugin to the end alone.
	leaseRunsOut
)

// askedLeaseMs is how long a lease that the host asks for and that outlives the case is, in milliseconds.
const askedLeaseMs = 60_000

// detachment is the form of a scenario that runs with the host gone.
type detachment struct {
	// runs says that the function writes "up" and then waits to be ended: the host reads "up" before it leaves, so that the start was
	// observed when the session ends. A function that does not is one that ends by itself, and it waits at a gate until the host has
	// left, so that it ends with nobody attached.
	runs bool
	// lease is the lease the host leaves the session with.
	lease leaseKind
	// end ends the session once nobody is attached; nil leaves it to the function (released from its gate), or to the lease.
	end func(r *wireRig, s *wireSession)
}

// retires retires the connection, which ends every session that runs through it.
func retires(r *wireRig, _ *wireSession) { r.conns.Retire() }

// shutsDown stops the plugin as the controller's Shutdown does.
func shutsDown(r *wireRig, _ *wireSession) { r.shutdown() }

// endSession asks the plugin to end a session that has no stream.
func (r *wireRig) endSession(id string, mode execv2.CloseMode) error {
	return r.client.EndSession(r.request(), &execv2.EndSessionRequest{SessionId: id, Mode: mode})
}

// endsSession is what ends a session the host has left: EndSession, which answers once the session has ended, its place is free and
// its tombstone is readable, so the host does not wait for either: the function has returned, and the listing has the tombstone, when
// the call returns. A function that takes its time to wind down after it is cancelled shows it: an answer that came before the end
// would find the function still running.
func endsSession(mode execv2.CloseMode) func(*wireRig, *wireSession) {
	return func(r *wireRig, s *wireSession) {
		r.t.Helper()
		if err := r.endSession(s.id, mode); err != nil {
			r.t.Fatalf("session %q: EndSession(%s) of a session that nobody holds: %v", s.id, mode, err)
		}
		if started, returned := r.started.Load(), r.returned.Load(); started != returned {
			r.t.Errorf("session %q: EndSession answered while %d of %d functions had returned: it answers once the session has ended and its place is free", s.id, returned, started)
		}
		if entry := r.sessionEntry(s.id); entry == nil || entry.GetEndedWith() == nil {
			r.t.Errorf("session %q: EndSession answered and the session has no tombstone (%v): it answers once the session has ended and its tombstone is readable", s.id, entry)
		}
	}
}

// windsDown is a step that waits until the command is cancelled, takes d to wind down, and then returns the context's error: a function
// that needs time to stop, as the process behind a session does. The time decides no outcome: what is held to it is the order of the
// end, which an end that did not wait for the function would get wrong whatever d is.
func windsDown(d time.Duration) Step {
	return stepFunc(func(ctx context.Context, _ *exec.Proc) (bool, exec.Exit, error) {
		<-ctx.Done()
		time.Sleep(d)
		return true, exec.Exit{}, ctx.Err()
	})
}

// applied waits until the plugin has applied every frame that the host sent so far. It applies them in the order they were sent, so the
// answer to a query comes after them, and the SDK answers the query for the integration tier at once, whatever the target: a host that is
// about to break its stream knows by it that the input it sent has reached the function.
func (s *wireSession) applied() {
	t := s.r.t
	t.Helper()
	const query = 1
	s.mustSend(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Query{Query: &execv2.Query{Id: query, Kind: execv2.FactKind_FACT_KIND_INTEGRATION_TIER}}})
	for {
		if fact := s.next("the answer to a query, which follows every frame that the host sent before it").GetFact(); fact.GetQueryId() == query {
			return
		}
	}
}

// ownLease is the lease a session gets when nobody asked for one, as Describe says it: the target's default, never above its maximum.
func (r *wireRig) ownLease() uint32 {
	r.t.Helper()
	limits := r.described().GetSession()
	lease := limits.GetDetachTimeoutMs()
	if most := limits.GetMaxDetachTimeoutMs(); most > 0 {
		lease = min(lease, most)
	}
	return lease
}

// leaseAskedFor is the lease, in milliseconds, that a host asks for when it leaves a session with a lease of the kind: none for the session's
// own (zero), a millisecond for the lease that runs out, and for the one that the host asks for a minute, or the target's maximum when that
// is shorter (a Start that asks for more is refused, and a Detach that asks for more is clamped).
func (r *wireRig) leaseAskedFor(kind leaseKind) uint32 {
	switch kind {
	case leaseRunsOut:
		return 1
	case leaseAsked:
		if most := r.described().GetSession().GetMaxDetachTimeoutMs(); most > 0 {
			return min(askedLeaseMs, most)
		}
		return askedLeaseMs
	}
	return 0
}

// leaseWindow says whether a lease of ms milliseconds that the plugin says ends at expires (Unix milliseconds) is one that the host's own
// clock allows: a lease applied at some moment between before and after ends between before and after, ms later. The plugin and the host
// share the clock here, and the host holds the plugin to what it asked for.
func leaseWindow(expires int64, before, after time.Time, ms uint32) bool {
	return expires >= before.UnixMilli()+int64(ms) && expires <= after.UnixMilli()+int64(ms)
}

// leave has the host leave a session that runs, and holds what the host reads of leaving to the contract: a Detach is answered by a
// stream that ends with Detached{REQUESTED} carrying the lease that was applied, and a stream that broke is followed by a listing that
// says nobody is attached. Either way the session lives, for its lease, with no stream. A lease that runs out is over before the host can
// be counted on to have read the session, so of it the host reads what its stream was told, which is the same whatever the pace, and
// nothing of the listing, which is not: the end is read from the tombstone, which the scenario awaits.
func (r *wireRig) leave(s *wireSession, how leaving, lease leaseKind, name string) {
	t := r.t
	t.Helper()
	asked := r.leaseAskedFor(lease)
	applied := asked
	if applied == 0 {
		applied = r.ownLease()
	}
	before := time.Now()
	var expires int64 // when the lease ends, Unix milliseconds, as the plugin says
	switch how {
	case leavesByDetach:
		s.mustSend(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Detach{Detach: &execv2.Detach{LeaseMs: asked}}})
		last := s.last()
		after := time.Now()
		detached := s.wantDetached(name, last, execv2.DetachReason_DETACH_REASON_REQUESTED)
		if detached == nil {
			return
		}
		// The plugin applied the lease at some moment between the host's two readings of the clock, which is so for a lease that has run out as well.
		expires = detached.GetLeaseExpiresUnixMs()
		if !leaseWindow(expires, before, after, applied) {
			t.Errorf("%s: session %q: Detached says the lease ends at %d, want it %d ms after the Detach, between %d and %d", name, s.id, expires,
				applied, before.UnixMilli()+int64(applied), after.UnixMilli()+int64(applied))
		}
	case leavesByBreak:
		_ = s.conn.Close()
	}
	if lease == leaseRunsOut {
		return
	}
	var entry *execv2.SessionInfo
	r.await(name+": the session to have no stream attached after its host left it", func() bool {
		entry = r.sessionEntry(s.id)
		return entry != nil && !entry.GetAttached()
	})
	after := time.Now()
	if entry.GetEndedWith() != nil {
		// A lease that the host's own clock says cannot have run out has not: the session ended when its host left it, for no reason a lease
		// gives. (A target that declares a lease of a few milliseconds of its own may have run out of it already, which is no failure.)
		if after.UnixMilli() < before.UnixMilli()+int64(applied) {
			t.Errorf("%s: session %q: the listing says the session ended (%s) before the %d ms of its lease had passed: it lives with no stream, for its lease",
				name, s.id, endedText(entry.GetEndedWith()), applied)
		}
		return
	}
	if how == leavesByBreak {
		// The session's own lease runs from the moment its stream broke, which is between the close and the moment the listing says so.
		expires = entry.GetLeaseExpiresUnixMs()
		if !leaseWindow(expires, before, after, applied) {
			t.Errorf("%s: session %q: the listing says the lease of a session whose stream broke ends at %d, want its own lease (%d ms) from the break, between %d and %d",
				name, s.id, expires, applied, before.UnixMilli()+int64(applied), after.UnixMilli()+int64(applied))
		}
	}
	// A listing read before the lease can have run out must show the session alive with no stream, and the lease that was applied.
	if time.Now().UnixMilli() < expires && entry.GetEndedWith() == nil && entry.GetLeaseExpiresUnixMs() != expires {
		t.Errorf("%s: session %q: the listing says the lease ends at %d, the host was told %d", name, s.id, entry.GetLeaseExpiresUnixMs(), expires)
	}
}

// playEndDetached opens the session of a scenario, sends it its input, has the host leave it as how says, ends it with nobody attached
// (the function by itself after it was let go, what the scenario ends it with, or its lease) and holds what the host reads of the end
// to the contract: there is no Ended frame to read, and the end is the tombstone's. It returns that Ended.
func (r *wireRig) playEndDetached(sc endScenario, how leaving) *execv2.Ended {
	t := r.t
	t.Helper()
	d := sc.detached
	name := fmt.Sprintf("%s, the host %s", sc.name, how)
	steps := sc.steps
	var held *Gate
	if !d.runs {
		held = NewGate()
		t.Cleanup(held.Open) // a function that waits for a gate that a failed case never opened would outlive the case
		steps = append([]Step{WaitGate(held)}, steps...)
	}
	script := r.newID("script")
	r.script(script, steps...)
	mods := slices.Clone(sc.mods)
	if asked := r.leaseAskedFor(d.lease); how == leavesByBreak && asked > 0 {
		mods = append(mods, func(st *execv2.Start) { st.DetachTimeoutMs = asked })
	}
	// The place of the session before is free once it has ended, which is a moment after its tombstone is readable.
	s := r.mustOpenEventually(r.newID("tombstone"), script, mods...)
	for i := 0; i < len(sc.input); i += 2 {
		s.mustInput(sc.input[i:min(i+2, len(sc.input))])
	}
	if d.runs {
		s.readBytes(len(sc.output))
	}
	s.applied() // a stream that breaks takes its frames with it: the input has to have reached the function first
	r.leave(s, how, d.lease, name)
	switch {
	case held != nil:
		held.Open()
	case d.end != nil:
		d.end(r, s)
	}
	var entry *execv2.SessionInfo
	r.await(name+": the tombstone of a session that ended with nobody attached", func() bool {
		entry = r.sessionEntry(s.id)
		return entry != nil && entry.GetEndedWith() != nil
	})
	r.checkTombstone(name, s, sc, entry)
	return entry.GetEndedWith()
}

// endedText writes an Ended on one line, for the message of a failure.
func endedText(e *execv2.Ended) string { return prototext.MarshalOptions{}.Format(e) }

// checkTombstone holds the tombstone of a session that ended with nobody attached to what the scenario expects of its end: the end, the
// counts, the completeness and the survival, as the Ended of a stream would carry them; that the listing does not say a stream is
// attached or the output further than the end; that the bytes the ring left are the function's; and that no stream can still be told: a
// session that ended is not one that an attach finds.
func (r *wireRig) checkTombstone(name string, s *wireSession, sc endScenario, entry *execv2.SessionInfo) {
	t := r.t
	t.Helper()
	ended := entry.GetEndedWith()
	want := wantEnded{end: sc.end, started: sc.started, bytesIn: uint64(len(sc.input)), bytesOut: uint64(len(sc.output))}
	for _, problem := range endedProblems(ended, s.tty(), want, nil) {
		t.Errorf("%s: session %q: the tombstone: %s", name, s.id, problem)
	}
	got, wantSurvival := ended.GetEnd().RemoteMaySurvive, sc.end.RemoteMaySurvive
	if (got == nil) != (wantSurvival == nil) || (got != nil && *got != *wantSurvival) {
		t.Errorf("%s: session %q: the tombstone says remote_may_survive is %s, want %s (the declared Kill is %s, ExitReport %s): a host that reads the end of a session it was not there for decides from it whether the process is gone",
			name, s.id, survivalText(got), survivalText(wantSurvival), r.info.Capabilities.Kill, r.info.Capabilities.ExitReport)
	}
	if entry.GetAttached() {
		t.Errorf("%s: session %q: the listing says a stream is attached to a session that ended with none", name, s.id)
	}
	if entry.GetNextOffset() != ended.GetLastOffset() {
		t.Errorf("%s: session %q: next_offset is %d, the end says last_offset %d: for an ended session they are the same offset", name, s.id, entry.GetNextOffset(), ended.GetLastOffset())
	}
	r.checkTombstoneOutput(name, s, sc.output)
	if sc.fresh {
		return // the connection or the plugin is gone: an attach is refused for that, and not for the session
	}
	if _, err := r.attach(s.id, 0); err == nil {
		t.Errorf("%s: session %q: an attach to a session that ended was admitted: a tombstone is not a session, and no stream is sent an Ended for it", name, s.id)
	} else if !isRefusal(err, exec.ErrorClassNotFound, exec.ErrorReasonSessionUnknown) {
		t.Errorf("%s: session %q: an attach to a session that ended was refused with %v, want NOT_FOUND / SESSION_UNKNOWN", name, s.id, err)
	}
}

// checkTombstoneOutput reads the ring of a session that ended and holds it to the bytes its function wrote: contiguous from offset 0, each
// run of the stream a command can have, ending where the session's output ended, with the answer saying that nothing more will come.
func (r *wireRig) checkTombstoneOutput(name string, s *wireSession, output string) {
	t := r.t
	t.Helper()
	resp := r.readRing(s.id, 0)
	var got []byte
	for _, chunk := range resp.GetChunks() {
		if chunk.GetOffset() != uint64(len(got)) {
			t.Errorf("%s: session %q: ReadOutput of the tombstone has a run at offset %d after %d bytes: the chunks are contiguous", name, s.id, chunk.GetOffset(), len(got))
		}
		if !streamFits(s.tty(), chunk.GetStream()) {
			t.Errorf("%s: session %q: ReadOutput of the tombstone has a run of the stream %s from a command %s", name, s.id, chunk.GetStream(), streamRule(s.tty()))
		}
		got = append(got, chunk.GetData()...)
	}
	switch {
	case !resp.GetEnded():
		t.Errorf("%s: session %q: ReadOutput of the tombstone says the session has not ended: ended is false, want true", name, s.id)
	case string(got) != output || resp.GetNextOffset() != uint64(len(output)):
		t.Errorf("%s: session %q: ReadOutput of the tombstone gave %q and next_offset %d, want %q and %d: the output is the bytes the function wrote, byte for byte",
			name, s.id, got, resp.GetNextOffset(), output, len(output))
	}
}

// wantTombstoneIs holds the tombstone of a session whose stream was told Ended to that same end: a session has one end, the frame and the
// tombstone are two places that say it, and a host that reads both finds one. The bytes the tombstone keeps are the ones the stream
// delivered.
func (r *wireRig) wantTombstoneIs(sc endScenario, s *wireSession, ended *execv2.Ended) {
	t := r.t
	t.Helper()
	entry := r.sessionEntry(s.id)
	switch {
	case entry == nil:
		t.Errorf("%s: session %q: the stream was told Ended and ListSessions does not list the session: the tombstone exists before the Ended is sent", sc.name, s.id)
		return
	case !proto.Equal(entry.GetEndedWith(), ended):
		t.Errorf("%s: session %q: the tombstone says %s, the stream was told %s: a session has one end, and the frame and the tombstone say it alike",
			sc.name, s.id, endedText(entry.GetEndedWith()), endedText(ended))
	}
	if entry.GetAttached() {
		t.Errorf("%s: session %q: the listing says a stream is attached to a session that ended", sc.name, s.id)
	}
	r.checkTombstoneOutput(sc.name, s, sc.output)
}
