package lifecycletest

import (
	"context"
	"fmt"
	"math/rand/v2"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/producers"
)

const (
	// minPhaseA and maxPhaseA bound how long the workers run on one lane before it is
	// closed and checked: long enough for sessions to pile up and be updated, short enough
	// that many lanes, and so many moments of Close, are tried.
	minPhaseA = 50 * time.Millisecond
	maxPhaseA = 250 * time.Millisecond
	// poolLimit is how many sessions the workers hold, in all, before they end one instead of
	// creating another: the lane's sessions are bounded, and so is the hammer's memory.
	poolLimit = 256
)

// Lane is the manager under test: the operations the manager of every lane has. The lane's
// adapter reports what its sessions do to the [Recorder] it was built with.
type Lane interface {
	// Create starts a session. A non-nil error is a refusal (closed, exists, limit, ...): it is
	// legal at any time and is never an admission. A nil error means the session was
	// admitted: the lane must have called [Recorder.Admitted] for id before it returns, or
	// atomically with the admission, and must call [Recorder.Ended] for it exactly once
	// later.
	Create(ctx context.Context, id string) (Session, error)
	// Close shuts the lane down: it refuses later creates, ends every session and returns
	// when they are gone, or when ctx ends, and then returns ctx.Err(). It may be called by
	// many goroutines at once, and again.
	Close(ctx context.Context) error
	// Live returns how many sessions the lane still holds: zero at rest, after Close.
	Live() int
}

// Session is one session of the lane under test.
type Session interface {
	// Update is an operation on a live session: an options update, an input, a resize. It may
	// be refused once the session ended; it must never panic or hang.
	Update(ctx context.Context) error
	// End asks the session to end, as a close does. It is idempotent.
	End(ctx context.Context) error
}

// Hammer runs the generic lifecycle hammer on lanes made by newLane, which is called once for
// each round with a fresh [Recorder] that the lane's sessions must report to. A round is:
//
//   - Phase A. Workers create sessions with unique ids, update random live sessions, and end
//     random sessions, on the round's lane, for a slice of cfg.Duration.
//   - Phase B. At a random moment while the workers are still running, one to three
//     goroutines call Close at once. The workers go on creating: a create that is not refused
//     must be ended exactly once, and one that starts after a Close has returned must be
//     refused.
//   - Phase C. At rest, once the workers have stopped and every Close has returned: every
//     Close returned in time and without error, Live is zero, the recorder has found no
//     violation and every admitted session was ended, a Create after Close is refused and is
//     not an admission, and the goroutines are back at the baseline taken before the first
//     round within cfg.Settle.
//
// Rounds run for as long as cfg.Duration lasts, each with at least 50 ms of phase A, and at
// least one runs. The hammer stops at the first round that fails and reports its violations
// with t.Errorf; a Close that does not return and a worker that does not stop are reported with
// t.Fatalf, with the goroutines that are stuck. Workers recover nothing: a panic in the lane
// ends the test, which is how a misused WaitGroup shows.
//
// An invalid OMNIVIEW_LIFECYCLE_HAMMER fails the test at once.
func Hammer(t testing.TB, newLane func(*Recorder) Lane, cfg Config) {
	t.Helper()
	if _, err := DurationFromEnv(); err != nil {
		t.Fatalf("lifecycletest: %v", err)
	}
	cfg = cfg.WithDefaults()
	baseline := TakeBaseline()
	start := time.Now()
	var rounds, admitted int
	for n := 0; rounds == 0 || time.Since(start) < cfg.Duration; n++ {
		phaseA := min(max(cfg.Duration-time.Since(start), minPhaseA), maxPhaseA)
		r := newRound(t, newLane, cfg, n)
		failed := r.run(phaseA, baseline)
		rounds++
		admitted += r.rec.AdmittedCount()
		if failed {
			return
		}
	}
	t.Logf("lifecycletest: %d rounds, %d sessions admitted, in %v", rounds, admitted, time.Since(start).Round(time.Millisecond))
}

// round is one lane's life under the hammer.
type round struct {
	t    testing.TB
	cfg  Config
	n    int
	seed uint64
	rec  *Recorder
	lane Lane

	// pool holds the sessions the workers have created, for them to update and end.
	pool struct {
		sync.Mutex
		sessions []Session
	}
	// closeReturned is set once any Close has returned: a Create that starts afterwards must
	// be refused.
	closeReturned atomic.Bool

	// foundMu guards found, late and firstLate.
	foundMu   sync.Mutex
	found     []string // violations found at rest, in the order they were found; each source of them is bounded
	late      int      // creates that were admitted after a Close had returned
	firstLate string   // the id of the first of them
}

func newRound(t testing.TB, newLane func(*Recorder) Lane, cfg Config, n int) *round {
	rec := NewRecorder()
	return &round{t: t, cfg: cfg, n: n, seed: cfg.Seed + uint64(n), rec: rec, lane: newLane(rec)}
}

// run is one round: its phases, and its checks at rest. It reports whether the round failed.
func (r *round) run(phaseA time.Duration, baseline GoroutineBaseline) (failed bool) {
	t := r.t
	t.Helper()
	rng := rand.New(rand.NewPCG(r.seed, 0))
	closeAt := phaseA/5 + time.Duration(rng.Int64N(int64(phaseA*3/5)))

	stop := make(chan struct{})
	var workers producers.Group
	workers.Start(r.cfg.Workers)
	for w := range r.cfg.Workers {
		go func() {
			defer workers.Done()
			r.work(w, stop)
		}()
	}

	pause(closeAt)
	closers := r.startClosers(1 + rng.IntN(3))
	pause(phaseA - closeAt)
	close(stop)

	if !r.within(&workers, r.cfg.CloseBound) {
		t.Fatalf("lifecycletest: round %d: the workers did not stop within %v of being told to: an operation of the lane is stuck\n%s",
			r.n, r.cfg.CloseBound, stuckGoroutines())
	}
	closers.wait(t, r)
	r.atRest(closers, baseline)

	violations := r.violations()
	for _, v := range violations {
		t.Errorf("lifecycletest: round %d: %s", r.n, v)
	}
	return len(violations) > 0
}

// within reports whether the group of goroutines ended within d.
func (r *round) within(g *producers.Group, d time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return g.WaitContext(ctx) == nil
}

// work is one worker: it creates, updates and ends sessions at random until stop is closed.
func (r *round) work(w int, stop <-chan struct{}) {
	rng := rand.New(rand.NewPCG(r.seed, uint64(w)+1))
	ctx := context.Background()
	for seq := 0; ; seq++ {
		select {
		case <-stop:
			return
		default:
		}
		switch p := rng.IntN(100); {
		case p < 40 && r.poolSize() < poolLimit:
			r.create(ctx, "r"+strconv.Itoa(r.n)+"-w"+strconv.Itoa(w)+"-"+strconv.Itoa(seq))
		case p < 75:
			if s := r.pick(rng, false); s != nil {
				_ = s.Update(ctx) // refused once the session ended: that is legal, a panic or a hang is not
			}
		default:
			if s := r.pick(rng, rng.IntN(4) != 0); s != nil {
				_ = s.End(ctx)
			}
		}
	}
}

// create starts a session and keeps it for the workers to use.
func (r *round) create(ctx context.Context, id string) {
	afterClose := r.closeReturned.Load()
	s, err := r.lane.Create(ctx, id)
	if err != nil {
		return
	}
	if afterClose {
		r.lateAdmit(id)
	}
	r.pool.Lock()
	r.pool.sessions = append(r.pool.sessions, s)
	r.pool.Unlock()
}

func (r *round) poolSize() int {
	r.pool.Lock()
	defer r.pool.Unlock()
	return len(r.pool.sessions)
}

// pick returns a random session of the pool, or nil when it is empty, and takes it out of the
// pool when remove is set.
func (r *round) pick(rng *rand.Rand, remove bool) Session {
	r.pool.Lock()
	defer r.pool.Unlock()
	last := len(r.pool.sessions) - 1
	if last < 0 {
		return nil
	}
	i := rng.IntN(last + 1)
	s := r.pool.sessions[i]
	if remove {
		r.pool.sessions[i] = r.pool.sessions[last]
		r.pool.sessions[last] = nil
		r.pool.sessions = r.pool.sessions[:last]
	}
	return s
}

// closeResult is what one Close call did.
type closeResult struct {
	err     error
	elapsed time.Duration
}

// closers are the Close calls of a round.
type closers struct {
	running producers.Group
	results []closeResult // result i is written before its goroutine is done with running
}

// startClosers calls Close from n goroutines at once.
func (r *round) startClosers(n int) *closers {
	c := &closers{results: make([]closeResult, n)}
	c.running.Start(n)
	for i := range n {
		go func() {
			defer c.running.Done()
			rng := rand.New(rand.NewPCG(r.seed, uint64(r.cfg.Workers+i)+1))
			for range rng.IntN(32) {
				runtime.Gosched() // the callers do not all arrive together
			}
			ctx, cancel := context.WithTimeout(context.Background(), r.cfg.CloseBound)
			defer cancel()
			began := time.Now()
			err := r.lane.Close(ctx)
			c.results[i] = closeResult{err: err, elapsed: time.Since(began)}
			r.closeReturned.Store(true)
		}()
	}
	return c
}

// wait returns once every Close returned, and fails the test when one does not, after the
// bound plus the grace a Close that honours its context has to return in.
func (c *closers) wait(t testing.TB, r *round) {
	t.Helper()
	grace := max(r.cfg.CloseBound/2, 250*time.Millisecond)
	if !r.within(&c.running, r.cfg.CloseBound+grace) {
		t.Fatalf("lifecycletest: round %d: Close did not return within %v: it ignores its context (bound %v)\n%s",
			r.n, r.cfg.CloseBound+grace, r.cfg.CloseBound, stuckGoroutines())
	}
}

// atRest checks what is left once the lane is closed and the workers have stopped.
func (r *round) atRest(c *closers, baseline GoroutineBaseline) {
	for i, res := range c.results {
		switch {
		case res.err != nil:
			r.violate("Close #%d returned %v after %v: it did not finish within the bound %v", i, res.err, res.elapsed.Round(time.Millisecond), r.cfg.CloseBound)
		case res.elapsed > r.cfg.CloseBound:
			r.violate("Close #%d took %v, over the bound %v", i, res.elapsed.Round(time.Millisecond), r.cfg.CloseBound)
		}
	}
	if n := r.lane.Live(); n != 0 {
		r.violate("Live() is %d at rest, after Close returned; it must be 0", n)
	}
	for _, v := range r.rec.Violations() {
		r.violate("%s", v)
	}
	for _, v := range r.rec.CheckAtRest() {
		r.violate("%s", v)
	}

	before := r.rec.AdmittedCount()
	ctx := context.Background()
	if s, err := r.lane.Create(ctx, "r"+strconv.Itoa(r.n)+"-after-close"); err == nil {
		r.violate("Create after Close returned succeeded: a closed lane must refuse it")
		_ = s.End(ctx)
	}
	if after := r.rec.AdmittedCount(); after != before {
		r.violate("Create after Close returned was recorded as an admission: %d admissions became %d", before, after)
	}

	if err := baseline.Check(r.cfg.Settle); err != nil {
		r.violate("%s", strings.TrimPrefix(err.Error(), "lifecycletest: "))
	}
}

// violate records a violation found at rest.
func (r *round) violate(format string, args ...any) {
	r.foundMu.Lock()
	defer r.foundMu.Unlock()
	r.found = append(r.found, fmt.Sprintf(format, args...))
}

// lateAdmit records that a Create the lane did not refuse started after a Close had returned.
// The workers keep creating, so a lane that admits then does it again and again: the round
// counts them and names the first.
func (r *round) lateAdmit(id string) {
	r.foundMu.Lock()
	defer r.foundMu.Unlock()
	if r.late == 0 {
		r.firstLate = id
	}
	r.late++
}

// violations returns what the round found: the creates that were admitted after Close
// returned, then what it found at rest.
func (r *round) violations() []string {
	r.foundMu.Lock()
	defer r.foundMu.Unlock()
	if r.late == 0 {
		return r.found
	}
	late := fmt.Sprintf("%d creates were admitted after Close returned, the first %q: a closed lane must refuse every Create", r.late, r.firstLate)
	return append([]string{late}, r.found...)
}

// stuckGoroutines describes every goroutine, grouped, for the failure of a hang.
func stuckGoroutines() string {
	return groupStacks(goroutineStacks(), dumpLimit)
}
