package lifecycletest

import (
	"fmt"
	"slices"
	"sync"
)

const (
	// maxViolations bounds the violations a recorder keeps: a lane that breaks a rule breaks it
	// for every session, and the first ones say what is wrong.
	maxViolations = 50
	// recentEnded is how many ended ids a recorder remembers, to tell an event or a second end
	// for a session that ended from one for a session that never was. Its memory is bounded by
	// this and by the sessions that are live, however long the hammer runs.
	recentEnded = 1 << 12
)

// Recorder is handed to the lane under test, whose adapter reports to it what its sessions do,
// and it checks the per-session rules as the reports arrive:
//
//   - a session is admitted once, and not again while its admission is live;
//   - nothing is emitted for a session that is not live: not before it was admitted, and not
//     after its end was emitted;
//   - the end of a session is emitted once, and only for an admitted session.
//
// An id may be admitted again after its end: that is a new admission, and the recorder tells
// admissions of one id apart by generation, the number the admission was counted as. It cannot
// tell an event of the old session from one of the new one, which carry the same id, so a lane
// that reuses ids is checked by the session it reuses, not by the straggler it may emit.
//
// A violation is kept, up to a bound, and reported by [Recorder.Violations]; the recorder never
// fails a test itself, and it is safe for concurrent use.
type Recorder struct {
	mu       sync.Mutex
	admitted int                      // admissions so far, over every id
	live     map[string]int           // id to the generation of its live admission
	ended    map[string]int           // id to the generation of its last admission to end, while it is among the recent
	recent   [recentEnded]endedRecord // ring of the most recent ends, to bound ended
	oldest   int                      // index in recent of the next record to overwrite
	found    []string                 // the first violations, in the order they were found
	foundAll int                      // every violation found
}

// endedRecord names an ended admission in the ring of recent ends.
type endedRecord struct {
	id         string
	generation int
}

// NewRecorder returns an empty recorder.
func NewRecorder() *Recorder {
	return &Recorder{
		live:  make(map[string]int),
		ended: make(map[string]int),
	}
}

// Admitted reports that a session was admitted: that Create succeeded. A second Admitted for
// an id whose admission is still live is a violation.
func (r *Recorder) Admitted(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if generation, live := r.live[id]; live {
		r.violate("admitted twice: %q was admitted again while its admission #%d was live", id, generation)
	}
	r.admitted++
	r.live[id] = r.admitted
	delete(r.ended, id) // the new admission is the one that counts
}

// Event reports that the lane emitted anything for the session: output, a line, a frame. An
// event for a session that is not live is a violation, named by what is known of it: after its
// end, or for an id that was never admitted.
func (r *Recorder) Event(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, live := r.live[id]; live {
		return
	}
	if generation, was := r.ended[id]; was {
		r.violate("event after the end: %q emitted something after the end of its admission #%d", id, generation)
		return
	}
	r.violate("event for a session that was never admitted: %q", id)
}

// Ended reports that the lane emitted the session's end. A second Ended for the same
// admission is a violation, and so is an Ended for an id that was never admitted.
func (r *Recorder) Ended(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	generation, live := r.live[id]
	if !live {
		if _, was := r.ended[id]; was {
			r.violate("ended twice: %q had its end emitted again", id)
		} else {
			r.violate("ended without an admission: %q had an end emitted and was never admitted", id)
		}
		return
	}
	delete(r.live, id)
	r.remember(id, generation)
}

// remember records that an admission ended, forgetting the oldest end when the ring is full.
// The caller holds r.mu.
func (r *Recorder) remember(id string, generation int) {
	if old := r.recent[r.oldest]; old.id != "" && r.ended[old.id] == old.generation {
		delete(r.ended, old.id)
	}
	r.recent[r.oldest] = endedRecord{id: id, generation: generation}
	r.oldest = (r.oldest + 1) % recentEnded
	r.ended[id] = generation
}

// violate keeps a violation, up to the bound, and counts it. The caller holds r.mu.
func (r *Recorder) violate(format string, args ...any) {
	r.foundAll++
	if len(r.found) < maxViolations {
		r.found = append(r.found, fmt.Sprintf(format, args...))
	}
}

// AdmittedCount returns how many admissions were reported, over every id and generation.
func (r *Recorder) AdmittedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.admitted
}

// Violations returns the violations found so far, sorted, at most the first 50 of them; when
// there are more, the last element says how many were left out. It returns nil when there is
// none.
func (r *Recorder) Violations() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bounded(slices.Clone(r.found), r.foundAll)
}

// CheckAtRest returns a violation for every admitted session whose end was not emitted, sorted
// and bounded as [Recorder.Violations] are. Call it once the lane is closed: a session that is
// still live then was never ended. It returns nil when every admission was ended.
func (r *Recorder) CheckAtRest() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	missing := make([]string, 0, min(len(r.live), maxViolations))
	for id, generation := range r.live {
		missing = append(missing, fmt.Sprintf("never ended: %q (admission #%d) has no end emitted", id, generation))
	}
	slices.Sort(missing)
	total := len(missing)
	return r.bounded(missing[:min(total, maxViolations)], total)
}

// bounded sorts the violations and appends the count of those left out. The caller holds
// r.mu.
func (r *Recorder) bounded(kept []string, total int) []string {
	if total == 0 {
		return nil
	}
	slices.Sort(kept)
	if total > len(kept) {
		kept = append(kept, fmt.Sprintf("... and %d more violations not listed", total-len(kept)))
	}
	return kept
}
