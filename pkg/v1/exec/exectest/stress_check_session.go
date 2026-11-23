package exectest

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The checks of the session half of the stress, as plain functions of what the harness saw and what the engine said, so that each one
// is shown red on a synthetic violation and green on the same input without it (stress_check_session_test.go).

// ---------------------------------------------------------------------------------------------------------------
// an attach, a detach and the last frame of a stream
// ---------------------------------------------------------------------------------------------------------------

// checkReplay holds what an attach opened to what the contract says of it: the replay is exactly the bytes [Start, End), it begins no
// earlier than the offset the host asked from (a byte it already has is never sent again; one the ring lost is the gap between the two,
// which the host reads as dropped), and it ends where the live stream begins. A new session's replay is empty and at offset 0.
func checkReplay(from uint64, r exechook.Replay, newSession bool) []string {
	var found []string
	switch {
	case newSession && r != (exechook.Replay{}):
		found = append(found, fmt.Sprintf("the stream that started a session opens with the replay %+v, want an empty one at offset 0", r))
	case r.Start < from:
		found = append(found, fmt.Sprintf("an attach from offset %d opens with a replay that starts at %d: a byte the host has would be sent again", from, r.Start))
	}
	if r.Start > r.End {
		found = append(found, fmt.Sprintf("the replay starts at %d and ends at %d", r.Start, r.End))
	} else if r.Bytes != r.End-r.Start {
		found = append(found, fmt.Sprintf("the replay says %d bytes and is [%d, %d), which is %d", r.Bytes, r.Start, r.End, r.End-r.Start))
	}
	return found
}

// defaultLease and maxLease are the lease of a session that left and the most a host may ask for: the stress's targets declare no
// other (a target that sets one of them is given the default of the other).
var (
	defaultLease = exec.DefaultSessionLimits().DetachTimeout
	maxLease     = exec.DefaultSessionLimits().MaxDetachTimeout
)

// effectiveLease is the lease that a Detach of requested gives a session that was started asking for own: what the host asked for, or
// the session's own, or the target's default, and at most the target's maximum.
func effectiveLease(requested, own time.Duration) time.Duration {
	d := requested
	if d <= 0 {
		d = own
	}
	if d <= 0 {
		d = defaultLease
	}
	return min(d, maxLease)
}

// checkLease holds when a lease ends, as the engine reported it, to what the stress asked for: the engine read its clock between
// the two readings the harness made around the call, and added the lease that applied.
func checkLease(got, before, after time.Time, want time.Duration) string {
	if got.Before(before.Add(want)) || got.After(after.Add(want)) {
		return fmt.Sprintf("the lease ends at %v, want the clock plus %v, which is between %v and %v", got.UTC(), want, before.Add(want).UTC(), after.Add(want).UTC())
	}
	return ""
}

// streamFacts is what the harness did to a stream and to its session before the stream's last frame, counted before each call, so that
// every last frame is one a call can explain.
type streamFacts struct {
	detachCalls, leaveCalls int32 // calls on this stream
	laterAttaches           bool  // an attach call began after this stream's own attach returned
}

// checkFinal holds the last frame of a stream that left to a call that explains it: Detached for REQUESTED only after a Detach, for
// SUPERSEDED only after another attach, and with no reason only after the stream was released; a lease for the one that asked to leave and
// none for a stream that another took the session from (a stream released while its session was ending has none either: no lease runs).
func checkFinal(f exechook.Final, facts streamFacts) []string {
	var found []string
	if !f.Detached {
		return []string{"a stream that left the session has Ended as its last frame: only the stream attached when the session ends is told Ended"}
	}
	switch f.Reason {
	case execv2.DetachReason_DETACH_REASON_REQUESTED:
		if facts.detachCalls == 0 {
			found = append(found, "the stream is told Detached for REQUESTED and the host sent no Detach on it")
		}
	case execv2.DetachReason_DETACH_REASON_SUPERSEDED:
		if !facts.laterAttaches {
			found = append(found, "the stream is told Detached for SUPERSEDED and no attach came after it")
		}
		if !f.Lease.IsZero() {
			found = append(found, fmt.Sprintf("the stream that was superseded is told a lease of %v: the session is attached, and no lease runs", f.Lease.UTC()))
		}
	case execv2.DetachReason_DETACH_REASON_UNSPECIFIED:
		if facts.leaveCalls == 0 {
			found = append(found, "the stream is told Detached with no reason and nobody released it")
		}
	}
	if f.Reason == execv2.DetachReason_DETACH_REASON_REQUESTED && f.Lease.IsZero() {
		found = append(found, "the stream is told Detached for REQUESTED with no lease: the session lives for one")
	}
	return found
}

// ---------------------------------------------------------------------------------------------------------------
// why a session ended
// ---------------------------------------------------------------------------------------------------------------

// causeFacts is what the harness did before a session ended, counted before each call: the causes that need one are the ones a call can
// make.
type causeFacts struct {
	endRequests int32 // calls that can end the session: a Close, EndSession, a Close frame of a stream that had not left
	leases      int32 // calls that leave the session with no stream: a Detach, a stream released
	cancels     int32 // calls that end the context of a run
	endedFrames int32 // streams that were told Ended
	closing     bool  // the engine's Close had begun
	run         bool
}

// checkCause holds the cause a command ended with to the calls that can explain it: a close by request needs a request (a Close frame
// of a stream that had left is not one), the end of a lease needs a session that had no stream and tells none Ended, a shutdown needs the
// engine to be closing, a run is cancelled by the end of its context and a session is never.
func checkCause(cause string, f causeFacts) string {
	switch cause {
	case "requested":
		if f.endRequests == 0 {
			return "the command ended with the cause \"requested\" and nothing that can end it was asked of it: the Close of a stream that had left does not"
		}
	case "expired":
		switch {
		case f.run:
			return "a run ended with the cause \"expired\": only the lease of a detached session expires"
		case f.leases == 0:
			return "the session ended with the cause \"expired\" and no stream of it ever left: no lease ran"
		case f.endedFrames > 0:
			return "a stream was told Ended for a session that ended with the cause \"expired\": a lease ends only a session that nobody is attached to"
		}
	case "shutdown":
		if !f.closing {
			return "the command ended with the cause \"shutdown\" and the engine was not closing"
		}
	case "cancelled":
		if !f.run || f.cancels == 0 {
			return "the command ended with the cause \"cancelled\" and no run's context was ended"
		}
	case "retired", "timeout":
		return fmt.Sprintf("the command ended with the cause %q: the stress retires no connection and sets no timeout", cause)
	}
	return ""
}

// ---------------------------------------------------------------------------------------------------------------
// what a refused attach, a refused end and a refused frame may be
// ---------------------------------------------------------------------------------------------------------------

// attachAsk is what an attach asked, and what is known of the session it asked for.
type attachAsk struct {
	beyond bool // the offset is one the session cannot have reached
	mayEnd bool // the session may have ended by now: it was asked to, its lease may have run out, the engine was closing, or it ends by itself
}

// checkAttachRefusal holds the refusal of an attach to what the contract allows: a session that does not live (it ended, or its end is
// under way) is NOT_FOUND / SESSION_UNKNOWN, and only a session that something could have ended; an offset beyond the output is
// INVALID_REQUEST. An attach that was admitted, for an offset that cannot be reached, is a violation too.
func checkAttachRefusal(err error, a attachAsk) string {
	if err == nil {
		if a.beyond {
			return "an attach from an offset the session cannot have reached was admitted: it is INVALID_REQUEST"
		}
		return ""
	}
	name := refusalName(err)
	switch {
	case name == "NOT_FOUND/SESSION_UNKNOWN" && a.mayEnd:
		return ""
	case name == "NOT_FOUND/SESSION_UNKNOWN":
		return "an attach to a session that lives was refused as NOT_FOUND / SESSION_UNKNOWN: nothing had ended it"
	case name == "INVALID_REQUEST" && a.beyond:
		return ""
	}
	return fmt.Sprintf("an attach was refused as %s (%v), beyond the offset %v and the session may have ended %v", name, err, a.beyond, a.mayEnd)
}

// endAsk is what an EndSession asked, and what is known of the session.
type endAsk struct {
	streamMayBeAttached bool // a stream was attached at some moment of the call
	expired             bool // the tombstone may be gone
	ctxEnded            bool // the call's own context ended
}

// checkEndSession holds the answer of EndSession to the contract: none is an end (and so is an answer for a session that ended), CONFLICT is
// a stream that is attached, and NOT_FOUND an id that has no session and no tombstone; a call whose context ended may be a timeout.
func checkEndSession(err error, a endAsk) string {
	if err == nil {
		return ""
	}
	name := refusalName(err)
	class, typed := refusalClass(err)
	switch {
	case typed && class == exec.ErrorClassConflict && a.streamMayBeAttached:
		return ""
	case typed && class == exec.ErrorClassConflict:
		return "EndSession was refused as CONFLICT for a session that no stream was attached to"
	case name == "NOT_FOUND/SESSION_UNKNOWN" && a.expired:
		return ""
	case name == "NOT_FOUND/SESSION_UNKNOWN":
		return "EndSession was refused as NOT_FOUND / SESSION_UNKNOWN for a session whose tombstone had not gone"
	case typed && (class == exec.ErrorClassTimeout || class == exec.ErrorClassCancelled) && a.ctxEnded:
		return ""
	}
	return fmt.Sprintf("EndSession was refused as %s (%v)", name, err)
}

// ---------------------------------------------------------------------------------------------------------------
// the id of a session that ended, and its tombstone
// ---------------------------------------------------------------------------------------------------------------

// reuseFacts is what the harness knows of the session whose id a start asks for again, as readings of the stress's clock.
type reuseFacts struct {
	admitAt time.Time // before the session was started: it cannot have ended earlier
	aliveAt time.Time // before a look that found it not ended, or zero: its tombstone was made no earlier than this, less the error of the look
	doneAt  time.Time // after a look that found it ended, or zero: its tombstone was made no later than this
}

// putNoEarlier is when the tombstone of the session was made at the earliest: when it was started, or, if later, when the harness saw it
// not ended, less one tick, because the end may come between the engine's clock and the look.
func (f reuseFacts) putNoEarlier() time.Time {
	if f.aliveAt.IsZero() {
		return f.admitAt
	}
	return laterOf(f.admitAt, f.aliveAt.Add(-maxTick))
}

func laterOf(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// checkReuse holds the answer to a start of an id that an earlier session used, which the tombstone of that session decides: the id is held
// until the tombstone is gone, which is at its time to live, or earlier when the store displaces it for a newer one. Admitted before the time
// to live ran out, and with no displacement possible, is a start that took an id a late reader would read the old session's output
// under. Refused as held when the tombstone must be gone is an id that is never let go.
func checkReuse(admitted, held bool, before, after time.Time, prev reuseFacts, ttl time.Duration, displacementPossible bool) string {
	switch {
	case admitted && !displacementPossible && after.Before(prev.putNoEarlier().Add(ttl)):
		return fmt.Sprintf("the id was admitted again at %v, before the tombstone of the session that held it ran out its time to live of %v (it ended no earlier than %v), and no tombstone had been displaced",
			after.UTC(), ttl, prev.putNoEarlier().UTC())
	case held && !prev.doneAt.IsZero() && !before.Before(prev.doneAt.Add(ttl)):
		return fmt.Sprintf("the id was refused as held at %v, when the tombstone of the session that held it (made no later than %v) had run out its time to live of %v: the id is never let go",
			before.UTC(), prev.doneAt.UTC(), ttl)
	}
	return ""
}

// ---------------------------------------------------------------------------------------------------------------
// ListSessions and ReadOutput
// ---------------------------------------------------------------------------------------------------------------

// listFacts is what the harness knows of one session when it looks at a list: whether it had ended before the call and after it, and
// the readings of the clock around its end.
type listFacts struct {
	id          string
	endedBefore bool
	endedAfter  bool
	reuse       reuseFacts
	res         *exechook.Result // its result, once the harness saw it
}

// listRule is how a list is read: what the store may hold, and whether a tombstone can be gone.
type listRule struct {
	ttl          time.Duration
	count, bytes int
	// keeps says that no tombstone expires or is displaced during the round: every session that ended is listed.
	keeps bool
	// displacementPossible says that more sessions ended than the store holds.
	displacementPossible bool
	// after is the clock when the call returned.
	after time.Time
}

// checkListing holds a ListSessions answer to the store's rules: the ids are in order and none is twice, a tombstone is one session's end
// that agrees with what the harness saw of that end, the store is within its bounds, and every session that was started before the call is
// there: live while it had not ended, as a tombstone after it, and a tombstone until its time to live runs out unless the store displaced it.
func checkListing(infos []*execv2.SessionInfo, known []listFacts, rule listRule) []string {
	var found []string
	byID := make(map[string]*execv2.SessionInfo, len(infos))
	tombstones, retained := 0, uint64(0)
	prev := ""
	for i, info := range infos {
		id := info.GetFacts().GetSessionId()
		switch {
		case id == "":
			found = append(found, fmt.Sprintf("listed session #%d has no id", i))
			continue
		case i > 0 && strings.Compare(prev, id) >= 0:
			found = append(found, fmt.Sprintf("session %q is listed after %q: the list is by id, and no id is listed twice", id, prev))
		}
		prev = id
		byID[id] = info
		if info.GetReplayStart() > info.GetNextOffset() {
			found = append(found, fmt.Sprintf("%s: the replay starts at %d, after the next offset %d", id, info.GetReplayStart(), info.GetNextOffset()))
		}
		if ended := info.GetEndedWith(); ended != nil {
			tombstones++
			retained += info.GetNextOffset() - min(info.GetReplayStart(), info.GetNextOffset())
			if _, ok := endArm(ended.GetEnd()); !ok {
				found = append(found, fmt.Sprintf("%s: the tombstone's end has no arm", id))
			}
			if info.GetNextOffset() != ended.GetLastOffset() || ended.GetLastOffset() != ended.GetBytesOut() {
				found = append(found, fmt.Sprintf("%s: the tombstone's next offset is %d, its last offset %d and its bytes out %d: all three are the end of the output", id, info.GetNextOffset(), ended.GetLastOffset(), ended.GetBytesOut()))
			}
			if info.GetAttached() || info.GetLeaseExpiresUnixMs() != 0 {
				found = append(found, fmt.Sprintf("%s: a session that ended is listed attached or with a lease", id))
			}
		} else if info.GetAttached() && info.GetLeaseExpiresUnixMs() != 0 {
			found = append(found, fmt.Sprintf("%s: a session with a stream attached is listed with a lease", id))
		}
	}
	if tombstones > rule.count {
		found = append(found, fmt.Sprintf("the store lists %d tombstones, over its bound of %d", tombstones, rule.count))
	}
	if retained > uint64(rule.bytes) {
		found = append(found, fmt.Sprintf("the tombstones keep %d bytes of output, over the store's bound of %d", retained, rule.bytes))
	}
	for _, f := range known {
		info := byID[f.id]
		switch {
		case !f.endedAfter && info == nil:
			found = append(found, fmt.Sprintf("%s: a session that lives is not listed", f.id))
		// A session whose Done is still open may be listed with its end: the tombstone is made before Done is closed.
		case info == nil:
			// It had ended by the end of the call, or during it (the list reads the live sessions and then the tombstones, so that
			// a session that ends between the two is listed once, with its end): its tombstone has to be there while its time to live
			// has not run out, unless the store displaced it.
			mustBeThere := rule.keeps || (!rule.displacementPossible && rule.after.Before(f.reuse.putNoEarlier().Add(rule.ttl)))
			if mustBeThere {
				found = append(found, fmt.Sprintf("%s: a session that ended is not listed: its tombstone is held until its time to live of %v runs out", f.id, rule.ttl))
			}
		case f.endedBefore && info.GetEndedWith() == nil && !rule.displacementPossible:
			// A store that displaced tombstones may have dropped this one before the session's id was released, which lists the session
			// from the registry, as live, until then; a store that displaced none made it, and lists it.
			found = append(found, fmt.Sprintf("%s: a session that ended before the call is listed as live", f.id))
		case info.GetEndedWith() != nil && f.res != nil:
			found = append(found, checkTombstoneAgrees(f.id, info.GetEndedWith(), *f.res)...)
		}
	}
	return found
}

// checkTombstoneAgrees holds the Ended that a tombstone keeps to the result the harness read from the command: it is the same end, and the
// same accounts.
func checkTombstoneAgrees(id string, ended *execv2.Ended, res exechook.Result) []string {
	var found []string
	if !proto.Equal(ended.GetEnd(), res.End) {
		found = append(found, fmt.Sprintf("%s: the tombstone says the session ended %v and the command's result says %v", id, ended.GetEnd(), res.End))
	}
	if ended.GetBytesOut() != res.BytesOut || ended.GetDroppedBytes() != res.DroppedBytes || ended.GetLastOffset() != res.BytesOut || ended.GetOutput() != res.Output {
		found = append(found, fmt.Sprintf("%s: the tombstone counts %d bytes out, %d dropped, last offset %d and output %s; the result counts %d, %d, %d and %s", id,
			ended.GetBytesOut(), ended.GetDroppedBytes(), ended.GetLastOffset(), ended.GetOutput(), res.BytesOut, res.DroppedBytes, res.BytesOut, res.Output))
	}
	return found
}

// checkListingAtRest holds the list of a store that keeps everything (no tombstone expires or is displaced during the round) to exactly
// the sessions that were started: no session missing and none that nobody started, each one ended and in agreement with its result.
func checkListingAtRest(infos []*execv2.SessionInfo, all []listFacts, rule listRule) []string {
	found := checkListing(infos, all, rule)
	want := make(map[string]bool, len(all))
	for _, f := range all {
		want[f.id] = true
	}
	var extra []string
	for _, info := range infos {
		id := info.GetFacts().GetSessionId()
		if !want[id] {
			extra = append(extra, id)
		}
		if info.GetEndedWith() == nil {
			found = append(found, fmt.Sprintf("%s: a session is still listed as live at rest, after the engine closed", id))
		}
	}
	if len(extra) > 0 {
		slices.Sort(extra)
		found = append(found, fmt.Sprintf("sessions that nobody started are listed: %s", listIDs(extra, 5)))
	}
	return found
}

// checkReadOutput holds a ReadOutput answer to the contract: the chunks are contiguous from the later of the offset asked for and the front
// of the ring, each is a run of one stream, the bytes are what the target wrote, the next offset is where the last chunk ends, and the end
// of a session that ended is said.
func checkReadOutput(resp *execv2.ReadOutputResponse, from uint64, tty, endedBefore bool) []string {
	var found []string
	if resp.GetReplayStart() > resp.GetNextOffset() {
		found = append(found, fmt.Sprintf("the answer's replay start %d is after its next offset %d", resp.GetReplayStart(), resp.GetNextOffset()))
	}
	next := max(from, resp.GetReplayStart())
	for i, c := range resp.GetChunks() {
		switch {
		case c.GetOffset() != next:
			found = append(found, fmt.Sprintf("chunk %d is at offset %d, want %d: the chunks are contiguous from the later of the offset asked for (%d) and the replay start (%d)", i, c.GetOffset(), next, from, resp.GetReplayStart()))
		case len(c.GetData()) == 0:
			found = append(found, fmt.Sprintf("chunk %d at offset %d is empty", i, c.GetOffset()))
		case !streamFits(tty, c.GetStream()):
			found = append(found, fmt.Sprintf("chunk %d is of the stream %s from a command %s", i, c.GetStream(), streamRule(tty)))
		}
		for j, b := range c.GetData() {
			if want := patternByte(c.GetOffset() + uint64(j)); b != want {
				found = append(found, fmt.Sprintf("the byte at offset %d is %d, not what was written there (%d)", c.GetOffset()+uint64(j), b, want))
				break
			}
		}
		next = c.GetOffset() + uint64(len(c.GetData()))
	}
	if resp.GetNextOffset() != next {
		found = append(found, fmt.Sprintf("the next offset is %d, and the chunks end at %d", resp.GetNextOffset(), next))
	}
	if endedBefore && !resp.GetEnded() {
		found = append(found, "the session had ended before the call, and the answer does not say so")
	}
	return found
}
