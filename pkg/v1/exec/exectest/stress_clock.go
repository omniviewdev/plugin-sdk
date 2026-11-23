package exectest

import (
	"sync/atomic"
	"time"
)

// stressClock is the clock the stress gives the engine: it stands still until the harness moves it, so that the age of a tombstone is a
// number the harness decides and reads, and never a race with the wall clock. The engine's timers (a lease, a grace) stay real; what it
// asks the clock is when a tombstone ends, when a lease ends as applied and when a session last wrote.
type stressClock struct {
	base time.Time
	off  atomic.Int64 // nanoseconds the clock has been moved
}

func newStressClock() *stressClock {
	return &stressClock{base: time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)}
}

// Now is the time, which only moves when the harness moves it: it never goes back, so a value read before a call is no later than
// what the engine reads inside it, and one read after is no earlier.
func (c *stressClock) Now() time.Time { return c.base.Add(time.Duration(c.off.Load())) }

// Advance moves the clock on by d.
func (c *stressClock) Advance(d time.Duration) { c.off.Add(int64(d)) }

// tombstoneRegime is how a round sets up the store of tombstones and who moves its clock. The two regimes of the stress look at
// opposite ends of the same rules: one holds still the clock and the bounds, so that every session that ended is there to be found,
// the other moves the clock and shrinks the bounds, so that tombstones expire and are displaced while the workers run.
type tombstoneRegime struct {
	name string
	// ttl is how long a tombstone holds its id and its place in the list, on the stress's clock.
	ttl time.Duration
	// count and bytes bound the store: the tombstones it keeps, and the bytes of output it keeps in all.
	count, bytes int
	// ticks says that the workers move the clock while they run, a few milliseconds at a time.
	ticks bool
	// sessions is the most sessions a round starts, so that a store that keeps all of them stays small; zero is no bound.
	sessions int
}

// keeps says that no tombstone expires or is displaced while the workers run: the clock stands still and the bounds are far above what a round
// makes.
func (t tombstoneRegime) keeps() bool { return !t.ticks }

// maxTick is the most a worker moves the clock at once. It is also the error of the harness's reading of when a session ended: a
// session whose end the harness saw is known to have ended no later than the clock read after, and, from the moment it saw the
// session not ended, no earlier than the clock read before it less one tick, which is the time another worker may have moved the
// clock between the end and the look.
const maxTick = 40 * time.Millisecond

var (
	// regimeKeeps never moves the clock, and the bounds are far above what a round makes: no tombstone expires, none is displaced,
	// so every session that ended is listed, readable, and holds its id, for the whole round.
	regimeKeeps = tombstoneRegime{name: "keeps", ttl: time.Hour, count: 1 << 13, bytes: 64 << 20, sessions: 4096}
	// regimeBounded moves the clock and has a store so small that it is always full: a tombstone expires after a few ticks or is
	// displaced by a newer one, the bounds hold at every look, and an id comes free when its tombstone goes.
	regimeBounded = tombstoneRegime{name: "bounded", ttl: 8 * maxTick, count: 24, bytes: 48 << 10, ticks: true}
)

// regimeOf is the regime of round n: the two in turn, the first being the one that keeps.
func regimeOf(n int) tombstoneRegime {
	if n%2 == 0 {
		return regimeKeeps
	}
	return regimeBounded
}
