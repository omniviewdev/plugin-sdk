package exec

import (
	"errors"
	"slices"
	"sync"
	"time"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// A session that ended leaves a tombstone: its facts, its Ended and, for a while, the bytes its ring held. A host that was away
// when the session ended (its stream broke, its window reloaded, its lease ran out) finds the end and the last of the output
// there, with ListSessions and ReadOutput, instead of "no such session". A tombstone also holds the session's id: a session
// that has ended cannot be started again under the same id until its tombstone is gone, because a late reader of the old id
// would otherwise read the new session's output as the old one's. (A run is not a session: it has no tombstone and its id is
// free the moment it has ended.)
//
// The store is bounded, so that a flood of short sessions cannot grow it:
//
//   - a tombstone lives tombstoneTTL (60 seconds);
//   - at most maxTombstones are kept: when one more arrives the oldest goes, with its id;
//   - the output that tombstones keep is at most tombstoneBytes in all: when the budget is spent the oldest tombstones lose their
//     output and keep their end, so a reader finds how the session ended and the bytes only if the budget held them.
//
// A tombstone is made by the engine before it releases the session's id, and so before the session's stream is told Ended: a host
// that has read Ended finds the tombstone. The store reads the clock of the engine, so a test that drives a fake clock drives the
// tombstones with it; expiry is decided at each use, and a timer releases the memory of a store that nobody uses.
const (
	defaultTombstoneTTL   = 60 * time.Second
	defaultMaxTombstones  = 256
	defaultTombstoneBytes = 32 << 20
)

// tombstone is one ended session.
type tombstone struct {
	id    string
	facts *execv2.SessionFacts
	ended *execv2.Ended
	link  execv2.LinkState
	// lastActivity is when the session last wrote output or took input, in Unix milliseconds; zero for none.
	lastActivity int64
	// ring is what the session's output was, closed and read-only; nil once the budget for output was spent.
	ring     *ring
	retained int // bytes of output the ring holds, which the budget counts
	// replayStart is the oldest offset a reader can still ask for: the ring's front, or the end of the output when the ring is gone.
	replayStart uint64
	expires     time.Time
}

// info is the tombstone as ListSessions reports it.
func (t *tombstone) info() *execv2.SessionInfo {
	return &execv2.SessionInfo{
		Facts:              t.facts,
		EndedWith:          t.ended,
		NextOffset:         t.ended.GetLastOffset(),
		ReplayStart:        t.replayStart,
		LastActivityUnixMs: t.lastActivity,
		Link:               t.link,
	}
}

// tombstones is the store of the tombstones of an engine.
type tombstones struct {
	ttl      time.Duration
	maxCount int
	maxBytes int
	now      func() time.Time
	// timed says that a timer releases the memory of expired tombstones: it is the real clock's, and a store driven by a test's
	// clock is not timed (the test moves the clock, and the next use finds what has expired).
	timed func() bool

	mu     sync.Mutex
	byID   map[string]*tombstone
	order  []*tombstone // oldest first: every tombstone lives as long as the same ttl, so this is also the order of their expiry
	bytes  int
	timer  *time.Timer
	closed bool // the engine is closed: no timer is armed, and what is held stays readable until it expires
}

func newTombstones(ttl time.Duration, maxCount, maxBytes int, now func() time.Time, timed func() bool) *tombstones {
	return &tombstones{ttl: ttl, maxCount: maxCount, maxBytes: maxBytes, now: now, timed: timed, byID: map[string]*tombstone{}}
}

// put keeps the tombstone of an ended session, and makes room: the ones that expired go, then the oldest while there are too many,
// and then the output of the oldest while the output kept is over its budget.
func (s *tombstones) put(t *tombstone) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	t.expires = now.Add(s.ttl)
	s.sweepLocked(now)
	if old, ok := s.byID[t.id]; ok {
		s.removeLocked(old) // the id of a tombstone is never made twice while it lives; a restarted engine in a test could
	}
	s.byID[t.id] = t
	s.order = append(s.order, t)
	s.bytes += t.retained
	for len(s.order) > s.maxCount {
		s.removeLocked(s.order[0])
	}
	for i := range s.order {
		if s.bytes <= s.maxBytes {
			break
		}
		s.releaseOutputLocked(i)
	}
	s.armLocked(now)
}

// has reports whether the id is held by a tombstone.
func (s *tombstones) has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(s.now())
	_, ok := s.byID[id]
	return ok
}

// get returns the tombstone of an id.
func (s *tombstones) get(id string) (*tombstone, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(s.now())
	t, ok := s.byID[id]
	return t, ok
}

// list returns the tombstones, oldest first.
func (s *tombstones) list() []*tombstone {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(s.now())
	return slices.Clone(s.order)
}

// len returns how many tombstones the store holds.
func (s *tombstones) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(s.now())
	return len(s.order)
}

// retainedBytes returns how many bytes of output the store keeps.
func (s *tombstones) retainedBytes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(s.now())
	return s.bytes
}

// sweepLocked drops the tombstones that have expired.
func (s *tombstones) sweepLocked(now time.Time) {
	for len(s.order) > 0 && !s.order[0].expires.After(now) {
		s.removeLocked(s.order[0])
	}
}

func (s *tombstones) removeLocked(t *tombstone) {
	delete(s.byID, t.id)
	s.bytes -= t.retained
	if i := slices.Index(s.order, t); i >= 0 {
		s.order = slices.Delete(s.order, i, i+1)
	}
	if len(s.order) == 0 && s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

// releaseOutputLocked drops the output of the tombstone at i of the order, which keeps its end. The tombstone is not changed: what
// the store handed out (list, get) is read without its lock, so the store replaces it with a copy that has no output, and whoever
// holds the one it handed out holds what it was when it was handed out.
func (s *tombstones) releaseOutputLocked(i int) {
	old := s.order[i]
	if old.ring == nil {
		return
	}
	s.bytes -= old.retained
	released := *old
	released.ring, released.retained, released.replayStart = nil, 0, old.ended.GetLastOffset()
	s.order[i] = &released
	s.byID[released.id] = &released
}

// armLocked arms the timer that releases what expires, when the store is timed and none is armed.
func (s *tombstones) armLocked(now time.Time) {
	if s.closed || s.timer != nil || len(s.order) == 0 || s.timed == nil || !s.timed() {
		return
	}
	s.timer = time.AfterFunc(max(s.order[0].expires.Sub(now), time.Millisecond), s.onTimer)
}

func (s *tombstones) onTimer() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timer = nil
	now := s.now()
	s.sweepLocked(now)
	s.armLocked(now)
}

// close stops the store's timer. What it holds stays readable until it expires: a plugin that is shutting down has ended its
// sessions, and a host that is still connected may read how they ended.
func (s *tombstones) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

// errIDTombstoned is why a start is refused for an id that the tombstone of a session that ended still holds.
var errIDTombstoned = errors.New("exec: the id is held by the tombstone of a session that ended")

// realClock says that the engine runs on the real clock: it has none of the clock seams a test sets. A store that does is timed.
func (e *engine) realClock() bool { return e.now == nil && e.newLeaseTimer == nil }

// set changes the bounds of the store; a zero keeps the one it has. The engine's configuration of a test uses it.
func (s *tombstones) set(ttl time.Duration, maxCount, maxBytes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ttl > 0 {
		s.ttl = ttl
	}
	if maxCount > 0 {
		s.maxCount = maxCount
	}
	if maxBytes > 0 {
		s.maxBytes = maxBytes
	}
}

// sealSession is the end of a session as the engine records it, at the one moment its end is emitted: the tombstone is made, and
// then the session is sealed, so that nobody who can read the tombstone finds the session still attachable. The stream that is
// attached at this moment is told Ended and the others are not.
func (c *command) sealSession() {
	r := c.ring
	facts, _ := c.sessionFacts()
	start, end := r.window()
	t := &tombstone{
		id: c.id, facts: facts, ended: endedFrame(c.result, c.request.TTY).GetEnded(),
		lastActivity: r.lastActivity.Load(), ring: r, retained: int(end - start), replayStart: start,
	}
	if state, ok := c.Link(); ok && c.gate.effective().LinkHealth {
		t.link = state.toProto()
	}
	c.e.tombs.put(t)
	r.seal()
}
