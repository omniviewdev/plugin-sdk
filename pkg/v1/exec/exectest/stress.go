package exectest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/internal/exectestkit"
	"github.com/omniviewdev/plugin-sdk/internal/lifecycletest"
	"github.com/omniviewdev/plugin-sdk/internal/producers"
	sdkdiag "github.com/omniviewdev/plugin-sdk/pkg/diagnostics"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// StressConfig tunes Stress. The zero value of every field means its default.
type StressConfig struct {
	// Workers is the number of goroutines that start, feed, resize and end commands. The default is 8.
	Workers int
	// Duration is how long the stress runs, in rounds of about a second, each on a fresh engine, and at least one round. The
	// default is one second under go test -short and five seconds otherwise, or what the environment variable
	// OMNIVIEW_LIFECYCLE_HAMMER says (CI sets it to 30s for the long run).
	Duration time.Duration
	// Seed seeds the random source of every goroutine, so that the choices a run makes are reproducible; how they interleave is
	// not. The default is 1.
	Seed uint64
}

// Stress runs the exec engine under load and fails t when the state it is left in at rest is wrong.
//
// Each round builds an engine over a set of scripted targets, one routing key each, behind a fake connection registry: an echo,
// a flood that a stalled reader slows and one that a stalled reader makes lose its oldest bytes (the byte at offset i of what
// they write is a pure function of i, so that a reader verifies every one), a command for every way a command ends (an exit
// with a code, a signal, a lost stream, a typed failure), one that waits to be asked to end, and the ones that break the
// contract: a function that panics, one that ignores its cancelled context and a goroutine that writes after its function
// returned. Workers start sessions and runs on them at random, send input, close it, resize, end a command gracefully or with
// a kill, cancel the context of a run and walk away from a command; each command has a reader that reads its output to the end
// and checks it as it goes. At a random moment one to three goroutines call Close at once and the workers go on starting.
//
// At rest, once every Close has returned, all of these hold, and each violation is reported with the id of the command:
//
//   - every Close returned within ten seconds, and a start after it is refused as UNAVAILABLE and admitted nowhere;
//   - Live is empty, and the engine gave up on no function once the harness let the ones that ignored their context go;
//   - every command that was admitted has ended, and its result has exactly one of the five arms, a cause that is one of the
//     engine's words and an arm that agrees with the cause, and did not change when it was asked for again;
//   - the reader of every command read its output to the end with contiguous offsets, no byte that was not delivered and not
//     reported dropped, and the bytes the target wrote at their offsets; what it read plus what it was told was lost is exactly
//     what the command wrote; an output reported COMPLETE lost nothing;
//   - each admission and each end was reported to a lifecycletest.Recorder exactly once, and nothing came after the end;
//   - the goroutines are back to what they were before the first round within two seconds.
//
// A panic in the engine is not recovered: it ends the test binary, which is how a panic shows. Stress logs what it counted, so
// that a run shows what it exercised: the commands admitted, the starts refused by class, the ends by kind and by cause.
func Stress(t *testing.T, cfg StressConfig) {
	t.Helper()
	runStress(t, cfg, stressHooks{})
}

// stressT is the part of testing.T that the stress uses: it calls Errorf and Logf from the goroutine of the test only, so that a
// recorder can stand in for it.
type stressT interface {
	Helper()
	Logf(format string, args ...any)
	Errorf(format string, args ...any)
}

// stressHooks are what the tests of the harness change about a run: the engine under test, and the bounds of the checks at rest.
// The zero value is the real engine and the bounds the harness states.
type stressHooks struct {
	engine       engineFactory
	closeBound   time.Duration // how long Close may take (default 10 s)
	settle       time.Duration // how long the goroutines get to return to the baseline (default 2 s)
	abandonBound time.Duration // how long the abandoned functions get to return once the gates are open (default the close bound)
}

const (
	// stressGrace is how long the engine waits for a function that was cancelled before it gives up on it.
	stressGrace = 20 * time.Millisecond
	// minRound is the shortest round: long enough for the commands to pile up and for the Close to land among them.
	minRound = 50 * time.Millisecond
)

// runStress is Stress for any t, with the engine and the bounds hooks name. It returns what it counted.
func runStress(t stressT, cfg StressConfig, hooks stressHooks) *tally {
	t.Helper()
	totals := newTally()
	if _, err := lifecycletest.DurationFromEnv(); err != nil {
		t.Errorf("exectest.Stress: %v", err)
		return totals
	}
	lc := lifecycletest.Config{
		Workers: cfg.Workers, Duration: cfg.Duration, Seed: cfg.Seed, CloseBound: hooks.closeBound, Settle: hooks.settle,
	}.WithDefaults()
	if hooks.engine == nil {
		hooks.engine = realEngine
	}
	if hooks.abandonBound <= 0 {
		hooks.abandonBound = lc.CloseBound
	}
	rounds := max(1, int((lc.Duration+time.Second/2)/time.Second))
	span := max(lc.Duration/time.Duration(rounds), minRound)

	baseline := lifecycletest.TakeBaseline()
	began := time.Now()
	for n := range rounds {
		r := newStressRound(t, hooks, lc, n, span)
		failed := r.execute(baseline)
		totals.merge(r.tally)
		totals.add("rounds")
		if failed {
			break
		}
	}
	totals.log(t, lc, time.Since(began))
	return totals
}

// engineFactory builds the engine that a case or the stress drives. The real one is exechook.NewEngine; the tests of the suite
// give a wrong one, to see the suite fail.
type engineFactory func(targets []exec.Target, cfg exechook.EngineConfig) (exechook.Engine, error)

func realEngine(targets []exec.Target, cfg exechook.EngineConfig) (exechook.Engine, error) {
	return exechook.NewEngine(targets, cfg)
}

// wireTarget returns the wire form of the target that addresses the thing with the id through the connection: what a host puts
// in the first frame of an Open or in a RunRequest.
func wireTarget(c *exectestkit.Connections, resourceType, id string) (*execv2.TargetRef, error) {
	reference, err := resourcev2.ToProtoResourceReference(c.Reference(resourceType, id))
	if err != nil {
		return nil, fmt.Errorf("encode the reference: %w", err)
	}
	route, err := resourcev2.ToProtoAccessRoute(c.Route())
	if err != nil {
		return nil, fmt.Errorf("encode the route: %w", err)
	}
	return &execv2.TargetRef{Reference: reference, Route: route}, nil
}

// ---------------------------------------------------------------------------------------------------------------
// one round: an engine of its own, its workers, its Close and what is left at rest
// ---------------------------------------------------------------------------------------------------------------

// stressRound is one engine's life under the stress.
type stressRound struct {
	t     stressT
	hooks stressHooks
	cfg   lifecycletest.Config
	n     int
	seed  uint64
	span  time.Duration

	conns *exectestkit.Connections
	gates *stressGates
	eng   exechook.Engine
	rec   *lifecycletest.Recorder
	found findings // what the workers and the readers found while the round ran
	rest  findings // what the checks at rest found: kept apart, so that a flood of the first cannot hide the second
	tally *tally

	// closing is set before the first Close is called, closed once one has returned: a start that finds closing set may be
	// refused as UNAVAILABLE, and one that began after closed was set must be.
	closing atomic.Bool
	closed  atomic.Bool
	// lateAdmits counts the starts admitted after a Close had returned, and firstLate names the first.
	lateAdmits atomic.Int64
	firstLate  atomic.Value
	// seq numbers the admissions: the recorder's key of a command is its id and its number, because a session id may be used
	// again once its command is gone.
	seq atomic.Uint64
	// budgets bound the commands of the behaviours that hold a goroutine or a function until the end of the round.
	budgets map[string]*atomic.Int64

	readCtx     context.Context
	stopReaders context.CancelFunc
	readers     producers.Group
	outstanding outstanding

	// left is what the workers still held when they stopped: the commands that were not disposed of.
	left struct {
		sync.Mutex
		commands []*stressCommand
	}
}

func newStressRound(t stressT, hooks stressHooks, cfg lifecycletest.Config, n int, span time.Duration) *stressRound {
	r := &stressRound{
		t: t, hooks: hooks, cfg: cfg, n: n, seed: cfg.Seed + uint64(n), span: span,
		rec: lifecycletest.NewRecorder(), tally: newTally(), budgets: map[string]*atomic.Int64{},
	}
	for name, budget := range stressBudget {
		left := new(atomic.Int64)
		left.Store(int64(budget))
		r.budgets[name] = left
	}
	r.readCtx, r.stopReaders = context.WithCancel(context.Background())
	return r
}

// build makes the connection registry, the targets and the engine.
func (r *stressRound) build() error {
	r.conns = exectestkit.NewConnections(stressProvider)
	r.gates = newStressGates()
	targets := make([]exec.Target, len(stressBehaviours))
	for i, b := range stressBehaviours {
		targets[i] = b.target(r.gates)
	}
	eng, err := r.hooks.engine(targets, exechook.EngineConfig{
		Connections: r.conns, Diagnostics: sdkdiag.NewRegistry(), CloseGrace: stressGrace,
	})
	if err != nil {
		return fmt.Errorf("build the engine: %w", err)
	}
	r.eng = eng
	return nil
}

// execute is the round: workers, the Close at a random moment while they go on, and the checks at rest. It reports whether the
// round found a violation.
func (r *stressRound) execute(baseline lifecycletest.GoroutineBaseline) (failed bool) {
	t := r.t
	t.Helper()
	if err := r.build(); err != nil {
		t.Errorf("exectest.Stress: round %d: %v", r.n, err)
		return true
	}
	defer r.conns.Retire()
	rng := rand.New(rand.NewPCG(r.seed, 0))
	closeAt := r.span/5 + time.Duration(rng.Int64N(int64(r.span*3/5)))

	stop := make(chan struct{})
	var workers producers.Group
	workers.Start(r.cfg.Workers)
	for w := range r.cfg.Workers {
		go func() {
			defer workers.Done()
			r.work(w, stop)
		}()
	}
	pauseFor(closeAt)
	closers := r.startClosers(1 + rng.IntN(3))
	pauseFor(r.span - closeAt)
	close(stop)

	if !within(&workers, r.cfg.CloseBound) {
		r.stopReaders()
		t.Errorf("exectest.Stress: round %d: the workers did not stop within %v of being told to: an operation of the engine is stuck\n%s",
			r.n, r.cfg.CloseBound, goroutineDump(6<<10))
		return true
	}
	if !closers.wait(r.cfg.CloseBound + max(r.cfg.CloseBound/2, 250*time.Millisecond)) {
		r.stopReaders()
		t.Errorf("exectest.Stress: round %d: Close did not return within %v: it ignores its context (bound %v)\n%s",
			r.n, r.cfg.CloseBound, r.cfg.CloseBound, goroutineDump(6<<10))
		return true
	}
	r.atRest(closers, baseline)
	violations := append(r.found.list(), r.rest.list()...)
	for _, v := range violations {
		t.Errorf("exectest.Stress: round %d: %s", r.n, v)
	}
	return len(violations) > 0
}

// closeResult is what one Close call did.
type closeResult struct {
	err     error
	elapsed time.Duration
}

// closers are the Close calls of a round, made by goroutines of their own.
type closers struct {
	running producers.Group
	results []closeResult // result i is written before its goroutine is done with running
}

// startClosers calls Close from n goroutines at once: each takes a few turns of the scheduler first, so that they do not all
// arrive together.
func (r *stressRound) startClosers(n int) *closers {
	c := &closers{results: make([]closeResult, n)}
	c.running.Start(n)
	for i := range n {
		go func() {
			defer c.running.Done()
			rng := rand.New(rand.NewPCG(r.seed, uint64(r.cfg.Workers+i)+1))
			for range rng.IntN(32) {
				runtime.Gosched()
			}
			ctx, cancel := context.WithTimeout(context.Background(), r.cfg.CloseBound)
			defer cancel()
			r.closing.Store(true)
			began := time.Now()
			err := r.eng.Close(ctx)
			c.results[i] = closeResult{err: err, elapsed: time.Since(began)}
			r.closed.Store(true)
		}()
	}
	return c
}

// wait returns once every Close returned, or false when one has not within d.
func (c *closers) wait(d time.Duration) bool { return within(&c.running, d) }

// within reports whether the group of goroutines ended within d.
func within(g *producers.Group, d time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return g.WaitContext(ctx) == nil
}

// atRest checks what is left once every Close has returned and the workers have stopped.
func (r *stressRound) atRest(c *closers, baseline lifecycletest.GoroutineBaseline) {
	for i, res := range c.results {
		switch {
		case res.err != nil:
			r.rest.add("Close #%d returned %v after %v: it did not finish within the bound %v", i, res.err, res.elapsed.Round(time.Millisecond), r.cfg.CloseBound)
		case res.elapsed > r.cfg.CloseBound:
			r.rest.add("Close #%d took %v, over the bound %v", i, res.elapsed.Round(time.Millisecond), r.cfg.CloseBound)
		}
	}
	if live := r.eng.Live(); len(live) != 0 {
		r.rest.add("Live() is %s at rest, after Close returned: it must be empty", listIDs(live, 10))
	}
	r.probeAfterClose()
	if n := r.lateAdmits.Load(); n > 0 {
		first, _ := r.firstLate.Load().(string)
		r.rest.add("%d starts were admitted after a Close had returned, the first %q: a closed engine must refuse every start", n, first)
	}

	r.gates.open() // the functions that ignored their context return, and the goroutines that outlived theirs write
	r.waitReaders()
	r.disposeLeft()
	r.checkAbandoned()
	r.checkLateWriters()
	for _, v := range r.rec.Violations() {
		r.rest.add("%s", v)
	}
	for _, v := range r.rec.CheckAtRest() {
		r.rest.add("%s", v)
	}
	if err := baseline.Check(r.cfg.Settle); err != nil {
		r.rest.add("%v", err)
	}
}

// probeAfterClose starts a session and a run on the closed engine: both are refused as UNAVAILABLE.
func (r *stressRound) probeAfterClose() {
	b := stressBehaviours[0]
	target, err := wireTarget(r.conns, b.name, stressThing)
	if err != nil {
		r.rest.add("%v", err)
		return
	}
	ctx := r.conns.Context(context.Background())
	session, err := r.eng.Open(ctx, &execv2.OpenRequest{
		SessionId: "after-close-s", Kind: &execv2.OpenRequest_Start{Start: &execv2.Start{Target: target}},
	})
	if v := checkRefusedAfterClose(err); v != "" {
		r.rest.add("an Open: %s", v)
	}
	if err == nil {
		session.Close(true)
	}
	run, err := r.eng.Run(ctx, &execv2.RunRequest{RequestId: "after-close-r", Target: target, Argv: []string{"stress"}})
	if v := checkRefusedAfterClose(err); v != "" {
		r.rest.add("a Run: %s", v)
	}
	if err == nil {
		run.Close(true)
	}
}

// waitReaders waits for the reader of every command to be done: the output read to its end and the end seen. A reader that is
// not done within the bound names a command whose output never ended or that never ended; the round then ends the reads, so
// that nothing outlives it.
func (r *stressRound) waitReaders() {
	ctx, cancel := context.WithTimeout(context.Background(), r.cfg.CloseBound)
	defer cancel()
	if r.readers.WaitContext(ctx) == nil {
		return
	}
	for _, line := range r.outstanding.describe(10) {
		r.rest.add("%s", line)
	}
	r.stopReaders()
	again, stopWaiting := context.WithTimeout(context.Background(), r.cfg.CloseBound)
	defer stopWaiting()
	if r.readers.WaitContext(again) != nil {
		r.rest.add("the readers did not return even when their context ended\n%s", goroutineDump(6<<10))
	}
}

// disposeLeft disposes of the commands the workers still held when they stopped.
func (r *stressRound) disposeLeft() {
	r.left.Lock()
	commands := r.left.commands
	r.left.commands = nil
	r.left.Unlock()
	for _, c := range commands {
		r.dispose(c)
	}
}

// checkAbandoned waits for the engine to forget the functions it gave up on: the harness opened every gate they wait for, so
// each returns, and the count falls to zero.
func (r *stressRound) checkAbandoned() {
	if !poll(r.hooks.abandonBound, func() bool { return r.eng.Abandoned() == 0 }) {
		r.rest.add("Abandoned() is %d, %v after the harness opened every gate: a function the engine gave up on never returned, or was never forgotten",
			r.eng.Abandoned(), r.hooks.abandonBound)
	}
}

// checkLateWriters holds the goroutines that outlived their function to what the contract says of them: once the gate opened
// each one wrote, and its write was refused, its read of the input returned io.EOF, and none of that became output.
func (r *stressRound) checkLateWriters() {
	records := r.gates.lates.all()
	r.tally.addN("late writers", len(records))
	deadline := time.NewTimer(r.hooks.abandonBound)
	defer deadline.Stop()
	for i, record := range records {
		select {
		case <-record.Done():
		case <-deadline.C:
			r.rest.add("late writer #%d did not use its stdio within %v of the harness opening its gate", i, r.hooks.abandonBound)
			return
		}
		if n, err := record.Stdout(); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
			r.rest.add("late writer #%d: a write to stdout after the function returned took %d bytes and returned %v, want 0 and io.ErrClosedPipe", i, n, err)
		}
		if n, err := record.Stderr(); n != 0 || (err != nil && !errors.Is(err, io.ErrClosedPipe)) {
			r.rest.add("late writer #%d: a write to stderr after the function returned took %d bytes and returned %v, want 0 and io.ErrClosedPipe", i, n, err)
		}
		if err := record.Stdin(); !errors.Is(err, io.EOF) {
			r.rest.add("late writer #%d: a read of stdin after the function returned gave %v, want io.EOF", i, err)
		}
	}
}

// poll waits until cond holds, for at most bound. The condition is the oracle; the wait only keeps a bug from hanging the run.
func poll(bound time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(bound)
	for spins := 0; !cond(); spins++ {
		if time.Now().After(deadline) {
			return false
		}
		if spins < 100 {
			runtime.Gosched()
		} else {
			time.Sleep(time.Millisecond)
		}
	}
	return true
}

// pauseFor waits for d: the schedule of the workload, never the decision of a check.
func pauseFor(d time.Duration) {
	timer := time.NewTimer(d)
	<-timer.C
}

// listIDs lists at most limit ids, and how many were left out.
func listIDs(ids []string, limit int) string {
	if len(ids) <= limit {
		return fmt.Sprintf("%q", ids)
	}
	return fmt.Sprintf("%q and %d more", ids[:limit], len(ids)-limit)
}

// goroutineDump describes every goroutine, cut to limit bytes: what a hang leaves to find it by.
func goroutineDump(limit int) string {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	if len(buf) > limit {
		return string(buf[:limit]) + "\n... (cut)"
	}
	return string(buf)
}

// ---------------------------------------------------------------------------------------------------------------
// what the run counted
// ---------------------------------------------------------------------------------------------------------------

// tally counts what a run did, by a name with a prefix: "admitted/session", "refused/LIMIT/TOO_MANY_SESSIONS", "ended/exited".
type tally struct {
	mu sync.Mutex
	m  map[string]int
}

func newTally() *tally { return &tally{m: map[string]int{}} }

func (t *tally) add(key string) { t.addN(key, 1) }

func (t *tally) addN(key string, n int) {
	t.mu.Lock()
	t.m[key] += n
	t.mu.Unlock()
}

// get returns what was counted under the key.
func (t *tally) get(key string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.m[key]
}

// merge adds what another tally counted.
func (t *tally) merge(other *tally) {
	other.mu.Lock()
	counts := make(map[string]int, len(other.m))
	for key, n := range other.m {
		counts[key] = n
	}
	other.mu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()
	for key, n := range counts {
		t.m[key] += n
	}
}

// group lists what was counted under a prefix as "name n", sorted by name, and the sum of it.
func (t *tally) group(prefix string) (text string, sum int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var parts []string
	for key, n := range t.m {
		if name, ok := strings.CutPrefix(key, prefix+"/"); ok {
			parts = append(parts, fmt.Sprintf("%s %d", name, n))
			sum += n
		}
	}
	slices.Sort(parts)
	if len(parts) == 0 {
		return "none", 0
	}
	return strings.Join(parts, ", "), sum
}

// log tells the test what the run counted, so that a run shows what it exercised.
func (t *tally) log(log stressT, cfg lifecycletest.Config, took time.Duration) {
	log.Helper()
	admitted, total := t.group("admitted")
	refused, refusals := t.group("refused")
	ended, ends := t.group("ended")
	causes, _ := t.group("cause")
	behaviours, _ := t.group("behaviour")
	output, _ := t.group("output")
	violations, _ := t.group("contract")
	ops, _ := t.group("op")
	log.Logf("exectest.Stress: %d rounds, %d workers, seed %d, in %v: %d commands admitted (%s), %d starts refused (%s)",
		t.get("rounds"), cfg.Workers, cfg.Seed, took.Round(time.Millisecond), total, admitted, refusals, refused)
	log.Logf("exectest.Stress: %d ends by kind (%s); by cause (%s); output (%s)", ends, ended, causes, output)
	log.Logf("exectest.Stress: by behaviour (%s)", behaviours)
	log.Logf("exectest.Stress: operations (%s)", ops)
	log.Logf("exectest.Stress: read %d bytes and was told %d were dropped; %d functions abandoned; %d late writers refused; what the engine says its functions broke (%s)",
		t.get("bytes read"), t.get("bytes dropped"), t.get("abandoned"), t.get("late writers"), violations)
}
