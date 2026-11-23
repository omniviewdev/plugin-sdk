package lifecycletest_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/lifecycletest"
	"github.com/omniviewdev/plugin-sdk/internal/session"
)

// fakeTB is the testing.TB the hammer is run with in the tests of the hammer: it records what
// the hammer reports instead of failing the real test. It embeds a nil testing.TB, which makes
// it one and makes any method the hammer should not call, and that it does not override, panic.
type fakeTB struct {
	testing.TB
	mu       sync.Mutex
	errors   []string
	fatal    string
	messages []string
}

func (f *fakeTB) Helper() {}

func (f *fakeTB) Logf(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, fmt.Sprintf(format, args...))
}

func (f *fakeTB) Errorf(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errors = append(f.errors, fmt.Sprintf(format, args...))
}

// Fatalf records the failure and stops the goroutine that called it, as the real one does.
func (f *fakeTB) Fatalf(format string, args ...any) {
	f.mu.Lock()
	f.fatal = fmt.Sprintf(format, args...)
	f.mu.Unlock()
	runtime.Goexit()
}

// reports returns everything the hammer reported as a failure.
func (f *fakeTB) reports() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]string(nil), f.errors...)
	if f.fatal != "" {
		out = append(out, f.fatal)
	}
	return out
}

func (f *fakeTB) logged() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.messages, "\n")
}

// hammerIn runs the hammer on its own goroutine, as a Fatalf that stops it needs, and returns
// when it is done.
func hammerIn(t *testing.T, newLane func(*lifecycletest.Recorder) lifecycletest.Lane, cfg lifecycletest.Config) *fakeTB {
	t.Helper()
	f := &fakeTB{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		lifecycletest.Hammer(f, newLane, cfg)
	}()
	select {
	case <-done:
	case <-time.After(time.Minute):
		t.Fatal("the hammer did not return")
	}
	return f
}

// quick is a config for a short run: a hammer that is expected to fail must not take long to.
func quick() lifecycletest.Config {
	return lifecycletest.Config{
		Workers:    4,
		Duration:   200 * time.Millisecond,
		CloseBound: 300 * time.Millisecond,
		Settle:     200 * time.Millisecond,
	}
}

// faults select how the test lane is broken. The zero value is a correct lane.
type faults struct {
	eventAfterEnd    bool          // an untracked goroutine emits an event once the end was emitted
	endTwice         bool          // the end is emitted twice
	neverEnd         bool          // the end of the first session to end is never emitted
	reopenAfterClose bool          // a Close that returned leaves the lane open
	liveStuck        bool          // Live reports one more session than the lane holds
	closeHangs       chan struct{} // Close ignores its context and waits for this to be closed
	closeTooSlow     chan struct{} // Close waits for this to be closed, and gives up with the context's error when its context ends
	leakPerSession   chan struct{} // a goroutine per session, left waiting for this to be closed
}

// testLane is a lane on session.Registry, correct unless faults says otherwise.
type testLane struct {
	rec    *lifecycletest.Recorder
	faults faults

	skipped atomic.Bool // the end that is never emitted has been picked
	current atomic.Pointer[session.Registry[*testState]]

	mu         sync.Mutex
	registries []*session.Registry[*testState] // every registry the lane made, to shut them all down
	stats
}

// stats are what the hammer made the lane do.
type stats struct {
	creates, refused, updates, ends, closes atomic.Int64
}

type testState struct {
	afterEnd chan struct{} // closed once the end was emitted, for the straggler of eventAfterEnd
}

func newTestLane(rec *lifecycletest.Recorder, f faults) *testLane {
	l := &testLane{rec: rec, faults: f}
	l.current.Store(l.newRegistry())
	return l
}

func (l *testLane) newRegistry() *session.Registry[*testState] {
	reg := session.New(session.Config[*testState]{OnEnd: l.end})
	l.mu.Lock()
	defer l.mu.Unlock()
	l.registries = append(l.registries, reg)
	return reg
}

func (l *testLane) end(e *session.Entry[*testState]) {
	if l.faults.neverEnd && l.skipped.CompareAndSwap(false, true) {
		return
	}
	l.rec.Ended(e.ID())
	if l.faults.endTwice {
		l.rec.Ended(e.ID())
	}
	if l.faults.eventAfterEnd {
		close(e.Value().afterEnd)
	}
}

func (l *testLane) Create(ctx context.Context, id string) (lifecycletest.Session, error) {
	e, err := l.current.Load().Admit(ctx, session.Spec{ID: id}, func(context.Context) (*testState, error) {
		l.rec.Admitted(id) // atomic with the admission
		return &testState{afterEnd: make(chan struct{})}, nil
	})
	if err != nil {
		l.refused.Add(1)
		return nil, err
	}
	l.creates.Add(1)
	if l.faults.eventAfterEnd {
		go func() { // not a producer of the entry: nothing waits for it
			<-e.Value().afterEnd
			l.rec.Event(id)
		}()
	}
	if gate := l.faults.leakPerSession; gate != nil {
		go leakedGoroutine(gate)
	}
	e.Go(func() {
		l.rec.Event(id)
		<-e.Context().Done()
	})
	return &testSession{l: l, e: e}, nil
}

// leakedGoroutine is the goroutine a leaking lane leaves behind for each session.
func leakedGoroutine(gate <-chan struct{}) { <-gate }

func (l *testLane) Close(ctx context.Context) error {
	l.closes.Add(1)
	if gate := l.faults.closeHangs; gate != nil {
		<-gate // ignores ctx
		return nil
	}
	if gate := l.faults.closeTooSlow; gate != nil {
		select {
		case <-gate:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	err := l.current.Load().Close(ctx)
	if err == nil && l.faults.reopenAfterClose {
		l.current.Store(l.newRegistry())
	}
	return err
}

func (l *testLane) Live() int {
	n := l.current.Load().Len()
	if l.faults.liveStuck {
		n++
	}
	return n
}

// shutdown closes every registry the lane made, so that nothing a broken lane left running
// outlives the test.
func (l *testLane) shutdown(t *testing.T) {
	t.Helper()
	l.mu.Lock()
	registries := append([]*session.Registry[*testState](nil), l.registries...)
	l.mu.Unlock()
	for _, reg := range registries {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := reg.Close(ctx); err != nil {
			t.Errorf("shutting down a registry of the lane: %v", err)
		}
		cancel()
	}
}

type testSession struct {
	l *testLane
	e *session.Entry[*testState]
}

var errEnded = errors.New("the session ended")

func (s *testSession) Update(context.Context) error {
	s.l.updates.Add(1)
	if !s.e.Go(func() { s.l.rec.Event(s.e.ID()) }) {
		return errEnded
	}
	return nil
}

func (s *testSession) End(context.Context) error {
	s.l.ends.Add(1)
	s.e.End(session.CauseRequested)
	return nil
}

// lanes collects the lanes a hammer made, to inspect and shut down afterwards.
type lanes struct {
	mu   sync.Mutex
	made []*testLane
	f    faults
}

func (ls *lanes) newLane(rec *lifecycletest.Recorder) lifecycletest.Lane {
	l := newTestLane(rec, ls.f)
	ls.mu.Lock()
	defer ls.mu.Unlock()
	ls.made = append(ls.made, l)
	return l
}

func (ls *lanes) shutdown(t *testing.T) {
	t.Helper()
	ls.mu.Lock()
	defer ls.mu.Unlock()
	for _, l := range ls.made {
		l.shutdown(t)
	}
}

func (ls *lanes) total(count func(*testLane) int64) int64 {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	var n int64
	for _, l := range ls.made {
		n += count(l)
	}
	return n
}

// A correct lane passes the hammer, and the hammer exercised it: it created, updated, ended
// and closed, and refused what came after the Close.
func TestHammerPassesACorrectLaneOnTheRegistry(t *testing.T) {
	t.Setenv(envName, "")
	var ls lanes
	f := hammerIn(t, ls.newLane, lifecycletest.Config{Workers: 8, Duration: 2 * time.Second, CloseBound: 5 * time.Second, Settle: 2 * time.Second})
	ls.shutdown(t)
	if reports := f.reports(); len(reports) != 0 {
		t.Fatalf("the hammer failed a correct lane: %q", reports)
	}
	rounds := int64(len(ls.made))
	if rounds < 2 {
		t.Fatalf("the hammer ran %d rounds in 2 s, want several", rounds)
	}
	if !strings.Contains(f.logged(), "rounds") {
		t.Errorf("the hammer logged %q, want a summary", f.logged())
	}
	for name, n := range map[string]int64{
		"creates":  ls.total(func(l *testLane) int64 { return l.creates.Load() }),
		"updates":  ls.total(func(l *testLane) int64 { return l.updates.Load() }),
		"ends":     ls.total(func(l *testLane) int64 { return l.ends.Load() }),
		"closes":   ls.total(func(l *testLane) int64 { return l.closes.Load() }),
		"refusals": ls.total(func(l *testLane) int64 { return l.refused.Load() }),
	} {
		if n == 0 {
			t.Errorf("the hammer made no %s", name)
		}
	}
	if closes := ls.total(func(l *testLane) int64 { return l.closes.Load() }); closes < rounds || closes > 3*rounds {
		t.Errorf("%d Close calls in %d rounds, want one to three a round", closes, rounds)
	}
	// The workers go on creating while Close runs and after it: far more creates are refused
	// than the one a round makes at rest.
	if refused := ls.total(func(l *testLane) int64 { return l.refused.Load() }); refused < 2*rounds {
		t.Errorf("%d creates were refused over %d rounds: the workers did not go on creating once Close began", refused, rounds)
	}
}

func TestHammerRunsAtLeastOneRoundWhateverItsDuration(t *testing.T) {
	t.Setenv(envName, "")
	var ls lanes
	f := hammerIn(t, ls.newLane, lifecycletest.Config{Workers: 2, Duration: time.Nanosecond, CloseBound: 5 * time.Second})
	ls.shutdown(t)
	if reports := f.reports(); len(reports) != 0 {
		t.Fatalf("the hammer failed: %q", reports)
	}
	if len(ls.made) != 1 {
		t.Fatalf("the hammer ran %d rounds for a duration of 1ns, want 1", len(ls.made))
	}
}

func TestHammerRunsForTheDurationItIsGiven(t *testing.T) {
	t.Setenv(envName, "")
	var ls lanes
	began := time.Now()
	f := hammerIn(t, ls.newLane, lifecycletest.Config{Workers: 2, Duration: 300 * time.Millisecond})
	ls.shutdown(t)
	if reports := f.reports(); len(reports) != 0 {
		t.Fatalf("the hammer failed: %q", reports)
	}
	if elapsed := time.Since(began); elapsed < 300*time.Millisecond {
		t.Fatalf("the hammer returned after %v, before the 300ms it was given", elapsed)
	}
}

// The environment sets the duration when the config does not.
func TestHammerTakesItsDurationFromTheEnvironment(t *testing.T) {
	t.Setenv(envName, "300ms")
	var ls lanes
	began := time.Now()
	f := hammerIn(t, ls.newLane, lifecycletest.Config{Workers: 2})
	ls.shutdown(t)
	if reports := f.reports(); len(reports) != 0 {
		t.Fatalf("the hammer failed: %q", reports)
	}
	if elapsed := time.Since(began); elapsed < 300*time.Millisecond {
		t.Fatalf("the hammer returned after %v, before the 300ms of %s", elapsed, envName)
	}
}

func TestHammerFailsAtOnceOnAnInvalidDurationInTheEnvironment(t *testing.T) {
	t.Setenv(envName, "banana")
	var made atomic.Int32
	f := hammerIn(t, func(rec *lifecycletest.Recorder) lifecycletest.Lane {
		made.Add(1)
		return newTestLane(rec, faults{})
	}, lifecycletest.Config{Workers: 2, Duration: 50 * time.Millisecond})
	if f.fatal == "" || !strings.Contains(f.fatal, envName) {
		t.Fatalf("the hammer did not fail on %s=banana: %q", envName, f.reports())
	}
	if made.Load() != 0 {
		t.Fatal("the hammer made a lane before it failed on the environment")
	}
}

// Every round gets a lane of its own, with a recorder of its own.
func TestHammerMakesAFreshLaneAndRecorderForEveryRound(t *testing.T) {
	t.Setenv(envName, "")
	var (
		mu        sync.Mutex
		recorders = make(map[*lifecycletest.Recorder]bool)
		reused    bool
		ls        lanes
	)
	f := hammerIn(t, func(rec *lifecycletest.Recorder) lifecycletest.Lane {
		mu.Lock()
		reused = reused || recorders[rec]
		recorders[rec] = true
		mu.Unlock()
		return ls.newLane(rec)
	}, lifecycletest.Config{Workers: 2, Duration: 2 * time.Second})
	ls.shutdown(t)
	if reports := f.reports(); len(reports) != 0 {
		t.Fatalf("the hammer failed: %q", reports)
	}
	if reused || len(recorders) < 2 {
		t.Fatalf("%d recorders for %d rounds (reused: %v), want a fresh one for each", len(recorders), len(ls.made), reused)
	}
}

// The hammer is sensitive: each of these lanes breaks one rule, and the hammer reports it, in
// words that name that rule.
func TestHammerReportsEachKindOfBrokenLane(t *testing.T) {
	for _, tc := range []struct {
		name string
		// make returns the faults and a function that undoes what they leave running.
		make func() (faults, func())
		want string
	}{
		{
			name: "an event after the end",
			make: func() (faults, func()) { return faults{eventAfterEnd: true}, func() {} },
			want: "event after the end",
		},
		{
			name: "an end emitted twice",
			make: func() (faults, func()) { return faults{endTwice: true}, func() {} },
			want: "ended twice",
		},
		{
			name: "a session whose end is never emitted",
			make: func() (faults, func()) { return faults{neverEnd: true}, func() {} },
			want: "never ended",
		},
		{
			name: "a session admitted after Close returned",
			make: func() (faults, func()) { return faults{reopenAfterClose: true}, func() {} },
			want: "after Close returned",
		},
		{
			name: "a Live that stays above zero at rest",
			make: func() (faults, func()) { return faults{liveStuck: true}, func() {} },
			want: "Live() is 1 at rest",
		},
		{
			name: "a Close that ignores its context and hangs",
			make: func() (faults, func()) {
				gate := make(chan struct{})
				return faults{closeHangs: gate}, func() { close(gate) }
			},
			want: "ignores its context",
		},
		{
			name: "a Close that honours its context and does not finish in time",
			make: func() (faults, func()) {
				gate := make(chan struct{})
				return faults{closeTooSlow: gate}, func() { close(gate) }
			},
			want: "did not finish within the bound",
		},
		{
			name: "a goroutine leaked for each session",
			make: func() (faults, func()) {
				gate := make(chan struct{})
				return faults{leakPerSession: gate}, func() { close(gate) }
			},
			want: "goroutines",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envName, "")
			before := lifecycletest.TakeBaseline()
			f, release := tc.make()
			ls := lanes{f: f}
			got := hammerIn(t, ls.newLane, quick())
			release()
			ls.shutdown(t)
			if err := before.Check(10 * time.Second); err != nil {
				t.Errorf("the broken lane left goroutines behind after it was released and shut down: %v", err)
			}

			reports := got.reports()
			if len(reports) == 0 {
				t.Fatalf("the hammer passed a lane with %s", tc.name)
			}
			var hit string
			for _, r := range reports {
				if strings.Contains(r, tc.want) {
					hit = r
					break
				}
			}
			if hit == "" {
				t.Fatalf("no report says %q for a lane with %s:\n%s", tc.want, tc.name, strings.Join(reports, "\n"))
			}
			t.Logf("reported: %.200s", hit)
		})
	}
}

// A broken lane is reported at the round it breaks in: the hammer does not go on to bury the
// first failure under the ones that follow.
func TestHammerStopsAtTheFirstRoundThatFails(t *testing.T) {
	t.Setenv(envName, "")
	ls := lanes{f: faults{endTwice: true}}
	cfg := quick()
	cfg.Duration = 2 * time.Second
	got := hammerIn(t, ls.newLane, cfg)
	ls.shutdown(t)
	if len(got.reports()) == 0 {
		t.Fatal("the hammer passed a lane that ends every session twice")
	}
	if len(ls.made) != 1 {
		t.Fatalf("the hammer went on for %d rounds after the first failed", len(ls.made))
	}
}
