package exectest

import (
	"errors"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The checks of the session half of the stress are plain functions, so each is shown red on a synthetic violation and green on the same
// input without it: a check that cannot fail would make the invariant a hope.

var stressT0 = time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)

func TestAReplayThatBreaksTheContractIsFound(t *testing.T) {
	mustFindNothing(t, checkReplay(40, exechook.Replay{Start: 40, End: 160, Bytes: 120}, false))
	mustFindNothing(t, checkReplay(40, exechook.Replay{Start: 100, End: 160, Bytes: 60}, false)) // a gap: what the ring lost
	mustFindNothing(t, checkReplay(0, exechook.Replay{}, true))
	mustFind(t, checkReplay(40, exechook.Replay{Start: 39, End: 160, Bytes: 121}, false), "starts at 39", "sent again")
	mustFind(t, checkReplay(40, exechook.Replay{Start: 40, End: 160, Bytes: 100}, false), "says 100 bytes")
	mustFind(t, checkReplay(40, exechook.Replay{Start: 170, End: 160}, false), "starts at 170 and ends at 160")
	mustFind(t, checkReplay(0, exechook.Replay{Start: 0, End: 5, Bytes: 5}, true), "empty one at offset 0")
}

func TestALeaseIsWhatWasAskedForAsFarAsTheMaximumAndIsHeldToIt(t *testing.T) {
	for _, tc := range []struct{ asked, own, want time.Duration }{
		{0, 0, defaultLease}, {0, 7 * time.Millisecond, 7 * time.Millisecond}, {50 * time.Millisecond, 7 * time.Millisecond, 50 * time.Millisecond},
		{24 * time.Hour, 0, maxLease},
	} {
		if got := effectiveLease(tc.asked, tc.own); got != tc.want {
			t.Errorf("a lease of %v for a session whose own is %v is %v, want %v", tc.asked, tc.own, got, tc.want)
		}
	}
	before, after := stressT0, stressT0.Add(time.Millisecond)
	if v := checkLease(before.Add(time.Minute), before, after, time.Minute); v != "" {
		t.Errorf("a lease that is the clock plus the lease: %s", v)
	}
	if v := checkLease(after.Add(time.Minute), before, after, time.Minute); v != "" {
		t.Errorf("a lease read from the engine's clock at the end of the call: %s", v)
	}
	for name, got := range map[string]time.Time{"a lease that is not clamped": before.Add(24 * time.Hour), "a lease that ends too soon": before.Add(time.Second)} {
		if v := checkLease(got, before, after, time.Minute); v == "" {
			t.Errorf("%s was not found", name)
		}
	}
}

func TestALastFrameThatNoCallExplainsIsFound(t *testing.T) {
	lease := stressT0.Add(time.Minute)
	requested := exechook.Final{Detached: true, Reason: execv2.DetachReason_DETACH_REASON_REQUESTED, Lease: lease}
	superseded := exechook.Final{Detached: true, Reason: execv2.DetachReason_DETACH_REASON_SUPERSEDED}
	released := exechook.Final{Detached: true, Reason: execv2.DetachReason_DETACH_REASON_UNSPECIFIED, Lease: lease}
	mustFindNothing(t, checkFinal(requested, streamFacts{detachCalls: 1}))
	mustFindNothing(t, checkFinal(superseded, streamFacts{laterAttaches: true}))
	mustFindNothing(t, checkFinal(released, streamFacts{leaveCalls: 1}))
	mustFindNothing(t, checkFinal(exechook.Final{Detached: true, Reason: execv2.DetachReason_DETACH_REASON_UNSPECIFIED}, streamFacts{leaveCalls: 1})) // released as the session ended: no lease runs
	mustFind(t, checkFinal(exechook.Final{}, streamFacts{}), "has Ended as its last frame")
	mustFind(t, checkFinal(requested, streamFacts{}), "REQUESTED", "no Detach")
	mustFind(t, checkFinal(exechook.Final{Detached: true, Reason: execv2.DetachReason_DETACH_REASON_REQUESTED}, streamFacts{detachCalls: 1}), "no lease")
	mustFind(t, checkFinal(superseded, streamFacts{}), "SUPERSEDED", "no attach came after")
	mustFind(t, checkFinal(exechook.Final{Detached: true, Reason: execv2.DetachReason_DETACH_REASON_SUPERSEDED, Lease: lease}, streamFacts{laterAttaches: true}), "no lease runs")
	mustFind(t, checkFinal(released, streamFacts{}), "nobody released it")
}

func TestACauseThatNoCallExplainsIsFound(t *testing.T) {
	mustFind := func(want string, cause string, f causeFacts) {
		t.Helper()
		if v := checkCause(cause, f); v == "" || !containsAll(v, want) {
			t.Errorf("checkCause(%q, %+v) = %q, want it to say %q", cause, f, v, want)
		}
	}
	mustBeNone := func(cause string, f causeFacts) {
		t.Helper()
		if v := checkCause(cause, f); v != "" {
			t.Errorf("checkCause(%q, %+v) = %q, want none", cause, f, v)
		}
	}
	mustBeNone("finished", causeFacts{})
	mustBeNone("requested", causeFacts{endRequests: 1})
	mustBeNone("expired", causeFacts{leases: 1})
	mustBeNone("shutdown", causeFacts{closing: true})
	mustBeNone("cancelled", causeFacts{run: true, cancels: 1})
	mustFind("nothing that can end it", "requested", causeFacts{})
	mustFind("no stream of it ever left", "expired", causeFacts{})
	mustFind("told Ended", "expired", causeFacts{leases: 1, endedFrames: 1})
	mustFind("only the lease of a detached session", "expired", causeFacts{run: true, leases: 1})
	mustFind("was not closing", "shutdown", causeFacts{})
	mustFind("no run's context", "cancelled", causeFacts{})
	mustFind("no run's context", "cancelled", causeFacts{run: true})
	mustFind("retires no connection", "retired", causeFacts{})
}

func containsAll(s string, phrases ...string) bool {
	for _, p := range phrases {
		if !contains(s, p) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// some is the violations among the answers of checks that say one thing or none.
func some(answers ...string) []string {
	var found []string
	for _, a := range answers {
		if a != "" {
			found = append(found, a)
		}
	}
	return found
}

func TestTheRefusalOfAnAttachAndOfEndSessionIsHeldToWhatExplainsIt(t *testing.T) {
	unknown := &exec.Error{Class: exec.ErrorClassNotFound, Reason: exec.ErrorReasonSessionUnknown}
	invalid := &exec.Error{Class: exec.ErrorClassInvalidRequest}
	conflict := &exec.Error{Class: exec.ErrorClassConflict}
	mustFindNothing(t, some(checkAttachRefusal(nil, attachAsk{})))
	mustFindNothing(t, some(checkAttachRefusal(unknown, attachAsk{mayEnd: true}), checkAttachRefusal(invalid, attachAsk{beyond: true})))
	mustFind(t, some(checkAttachRefusal(nil, attachAsk{beyond: true})), "was admitted")
	mustFind(t, some(checkAttachRefusal(unknown, attachAsk{})), "nothing had ended it")
	mustFind(t, some(checkAttachRefusal(invalid, attachAsk{mayEnd: true})), "was refused as INVALID_REQUEST")
	mustFind(t, some(checkAttachRefusal(errors.New("boom"), attachAsk{mayEnd: true})), "unknown")

	mustFindNothing(t, some(checkEndSession(nil, endAsk{}), checkEndSession(conflict, endAsk{streamMayBeAttached: true}),
		checkEndSession(unknown, endAsk{expired: true}), checkEndSession(&exec.Error{Class: exec.ErrorClassTimeout}, endAsk{ctxEnded: true})))
	mustFind(t, some(checkEndSession(conflict, endAsk{})), "no stream was attached")
	mustFind(t, some(checkEndSession(unknown, endAsk{})), "had not gone")
	mustFind(t, some(checkEndSession(&exec.Error{Class: exec.ErrorClassTimeout}, endAsk{})), "was refused as TIMEOUT")
}

func TestTheReuseOfAnIdIsHeldToTheTombstoneThatHoldsIt(t *testing.T) {
	prev := reuseFacts{admitAt: stressT0, aliveAt: stressT0.Add(10 * time.Millisecond), doneAt: stressT0.Add(20 * time.Millisecond)}
	ttl := 100 * time.Millisecond
	at := func(d time.Duration) time.Time { return stressT0.Add(d) }
	mustFindNothing(t, some(
		checkReuse(true, false, at(150*time.Millisecond), at(200*time.Millisecond), prev, ttl, false), // admitted once the time to live has run out
		checkReuse(true, false, at(10*time.Millisecond), at(50*time.Millisecond), prev, ttl, true),    // or displaced
		checkReuse(false, true, at(10*time.Millisecond), at(50*time.Millisecond), prev, ttl, false),   // held while the tombstone lives
		checkReuse(false, true, at(100*time.Millisecond), at(110*time.Millisecond), prev, ttl, false), // and up to the last moment it may
	))
	mustFind(t, some(checkReuse(true, false, at(10*time.Millisecond), at(50*time.Millisecond), prev, ttl, false)), "admitted again", "before the tombstone")
	mustFind(t, some(checkReuse(false, true, at(150*time.Millisecond), at(160*time.Millisecond), prev, ttl, false)), "refused as held", "never let go")
}

func sessionInfo(id string, ended *execv2.Ended, next, replayStart uint64) *execv2.SessionInfo {
	return &execv2.SessionInfo{Facts: &execv2.SessionFacts{SessionId: id}, EndedWith: ended, NextOffset: next, ReplayStart: replayStart}
}

func endedWith(last uint64) *execv2.Ended {
	return &execv2.Ended{End: exitedEnd(0), BytesOut: last, LastOffset: last, Output: execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE}
}

func TestAListIsHeldToTheStore(t *testing.T) {
	rule := listRule{ttl: time.Minute, count: 2, bytes: 100, keeps: true, after: stressT0}
	ended := listFacts{id: "a", endedBefore: true, endedAfter: true}
	lives := listFacts{id: "b"}
	good := []*execv2.SessionInfo{sessionInfo("a", endedWith(10), 10, 0), sessionInfo("b", nil, 5, 0)}
	mustFindNothing(t, checkListing(good, []listFacts{ended, lives}, rule))

	mustFind(t, checkListing([]*execv2.SessionInfo{good[1], good[0]}, nil, rule), "is listed after")
	three := []*execv2.SessionInfo{sessionInfo("a", endedWith(1), 1, 0), sessionInfo("b", endedWith(1), 1, 0), sessionInfo("c", endedWith(1), 1, 0)}
	mustFind(t, checkListing(three, nil, rule), "3 tombstones", "bound of 2")
	mustFind(t, checkListing([]*execv2.SessionInfo{sessionInfo("a", endedWith(150), 150, 0)}, nil, rule), "150 bytes")
	mustFind(t, checkListing([]*execv2.SessionInfo{sessionInfo("a", endedWith(10), 9, 0)}, nil, rule), "all three are the end of the output")
	mustFind(t, checkListing(good[:1], []listFacts{ended, lives}, rule), "b: a session that lives is not listed")
	mustFind(t, checkListing(good[1:], []listFacts{ended, lives}, rule), "a: a session that ended is not listed")
	mustFind(t, checkListing([]*execv2.SessionInfo{sessionInfo("a", nil, 10, 0), good[1]}, []listFacts{ended, lives}, rule), "ended before the call is listed as live")

	// A store that displaced tombstones, or whose clock moved, may not have it: it is no violation there.
	bounded := listRule{ttl: time.Minute, count: 2, bytes: 100, displacementPossible: true, after: stressT0}
	mustFindNothing(t, checkListing(good[1:], []listFacts{ended, lives}, bounded))
	mustFindNothing(t, checkListing([]*execv2.SessionInfo{sessionInfo("a", nil, 10, 0), good[1]}, []listFacts{ended, lives}, bounded))

	// A tombstone says what the command's result says.
	closed := exechook.Result{End: closedEnd(execv2.ClosedBy_CLOSED_BY_REQUEST), BytesOut: 10, Output: execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE}
	disagree := ended
	disagree.res = &closed
	mustFind(t, checkListing(good, []listFacts{disagree, lives}, rule), "the tombstone says the session ended")
	agree := ended
	agreed := exechook.Result{End: exitedEnd(0), BytesOut: 10, Output: execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE}
	agree.res = &agreed
	mustFindNothing(t, checkListing(good, []listFacts{agree, lives}, rule))
}

func TestAListAtRestHoldsExactlyTheSessionsThatWereStarted(t *testing.T) {
	rule := listRule{ttl: time.Minute, count: 8, bytes: 100, keeps: true, after: stressT0}
	all := []listFacts{{id: "a", endedBefore: true, endedAfter: true}, {id: "b", endedBefore: true, endedAfter: true}}
	good := []*execv2.SessionInfo{sessionInfo("a", endedWith(1), 1, 0), sessionInfo("b", endedWith(1), 1, 0)}
	mustFindNothing(t, checkListingAtRest(good, all, rule))
	ghost := append(append([]*execv2.SessionInfo(nil), good...), sessionInfo("c", endedWith(1), 1, 0))
	mustFind(t, checkListingAtRest(ghost, all, rule), "nobody started", `"c"`)
	mustFind(t, checkListingAtRest(good[:1], all, rule), "b: a session that ended is not listed")
	mustFind(t, checkListingAtRest([]*execv2.SessionInfo{good[0], sessionInfo("b", nil, 1, 0)}, all, rule), "still listed as live")
}

func TestAReadOutputIsHeldToTheRing(t *testing.T) {
	chunk := func(offset uint64, n int) *execv2.Output {
		return &execv2.Output{Stream: execv2.Stream_STREAM_STDOUT, Offset: offset, Data: patternBytes(offset, n)}
	}
	mustFindNothing(t, checkReadOutput(&execv2.ReadOutputResponse{Chunks: []*execv2.Output{chunk(0, 10)}, NextOffset: 10, Ended: true}, 0, false, true))
	mustFindNothing(t, checkReadOutput(&execv2.ReadOutputResponse{ReplayStart: 5, Chunks: []*execv2.Output{chunk(5, 5)}, NextOffset: 10}, 0, false, false)) // a gap before the replay start
	mustFind(t, checkReadOutput(&execv2.ReadOutputResponse{Chunks: []*execv2.Output{chunk(6, 4)}, NextOffset: 10}, 0, false, false), "contiguous")
	bad := chunk(0, 10)
	bad.Data[3] ^= 0xff
	mustFind(t, checkReadOutput(&execv2.ReadOutputResponse{Chunks: []*execv2.Output{bad}, NextOffset: 10}, 0, false, false), "offset 3", "not what was written")
	mustFind(t, checkReadOutput(&execv2.ReadOutputResponse{Chunks: []*execv2.Output{chunk(0, 10)}, NextOffset: 12}, 0, false, false), "next offset is 12")
	mustFind(t, checkReadOutput(&execv2.ReadOutputResponse{Chunks: []*execv2.Output{chunk(0, 10)}, NextOffset: 10}, 0, false, true), "does not say so")
	mustFind(t, checkReadOutput(&execv2.ReadOutputResponse{Chunks: []*execv2.Output{chunk(0, 10)}, NextOffset: 10}, 0, true, false), "terminal")
}
