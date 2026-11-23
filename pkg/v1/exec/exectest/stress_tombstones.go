package exectest

import (
	"time"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// checkTombstones is what is left of the sessions at rest: the engine is closed and every session has ended, so the store holds what its
// rules say. A store that keeps everything (the clock stood still and its bounds were never reached) lists exactly the sessions that were
// started, each ended, with the end the command reported; any store is within its bounds. Then the harness moves the clock to where the last
// tombstone's time to live has run out, and the store must be empty: a tombstone is gone when its time has run out, and ReadOutput and the list
// agree.
func (r *stressRound) checkTombstones() {
	recs := r.ledger.snapshot(r.clock.Now(), r.regime.ttl, false)
	ctx, cancel := r.opCtx()
	defer cancel()
	infos, err := r.eng.ListSessions(ctx)
	if err != nil {
		r.rest.add("ListSessions at rest failed: %v", err)
		return
	}
	known := make([]listFacts, len(recs))
	var lastEnd int64
	for i, rec := range recs {
		rec.look(r.clock)
		known[i] = rec.listFacts(true, true)
		lastEnd = max(lastEnd, rec.doneAt.Load())
	}
	check := checkListing
	if r.regime.keeps() {
		check = checkListingAtRest
	}
	for _, v := range check(infos, known, r.listRule(r.clock.Now())) {
		r.rest.add("ListSessions at rest: %s", v)
	}
	tombstones := 0
	for _, info := range infos {
		if info.GetEndedWith() != nil {
			tombstones++
		}
	}
	r.tally.addN("tombstones at rest", tombstones)
	if len(recs) == 0 {
		return
	}

	// The clock moves to the end of the last time to live: every tombstone was made no later than the last end the harness saw, so none is
	// left, and none can be read.
	if gone := time.Unix(0, lastEnd).UTC().Add(r.regime.ttl); r.clock.Now().Before(gone) {
		r.clock.Advance(gone.Sub(r.clock.Now()))
	}
	infos, err = r.eng.ListSessions(ctx)
	if err != nil {
		r.rest.add("ListSessions after the time to live of every tombstone failed: %v", err)
		return
	}
	if len(infos) != 0 {
		r.rest.add("the store still lists %d tombstones (%s) once the clock is past the time to live of the last session to end, %v: a tombstone is gone when its time to live has run out",
			len(infos), listSessionIDs(infos, 5), r.regime.ttl)
	}
	probe := recs[0].id
	if _, err := r.eng.ReadOutput(ctx, &execv2.ReadOutputRequest{SessionId: probe, MaxBytes: 1}); refusalName(err) != "NOT_FOUND/SESSION_UNKNOWN" {
		r.rest.add("ReadOutput of %q after the time to live of its tombstone answered %v, want NOT_FOUND / SESSION_UNKNOWN", probe, err)
	}
}

// listSessionIDs names the sessions of a list, at most limit of them.
func listSessionIDs(infos []*execv2.SessionInfo, limit int) string {
	ids := make([]string, len(infos))
	for i, info := range infos {
		ids[i] = info.GetFacts().GetSessionId()
	}
	return listIDs(ids, limit)
}
