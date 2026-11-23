package session

import (
	"context"
	"fmt"
	"math/rand/v2"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/producers"
)

// mustHappen bounds the waits for something that has to happen. It turns a hang into a failure;
// no test relies on it elapsing, and none waits for it to show that something has not happened.
const mustHappen = 5 * time.Second

// The frames of the goroutines the tests wait for, as the runtime prints them in a stack.
const (
	admitFrame        = "(*Registry[...]).Admit("
	closeFrame        = "(*Registry[...]).Close("
	endFrame          = "(*Entry[...]).End("
	groupWaitFrame    = "(*Group).Wait("
	groupWaitCtxFrame = "(*Group).WaitContext("
)

// state is the session state of the test lane: what a lane's producers and end function share.
type state struct {
	active atomic.Int32  // producers of the test that are running, for the end function to check
	over   chan struct{} // closed by the end function: the end was emitted
	once   sync.Once
}

// lane is a minimal lane on a Registry. Its end function records every end, checks the rules an
// end must keep, and can be made to wait; its seams hold the registry at the crossings a test
// forces.
type lane struct {
	eventLog // the events of the test, in the order they happened
	t        *testing.T
	reg      *Registry[*state]
	pauses   pauses

	mu       sync.Mutex
	admitted map[string]int // admissions that succeeded, by id
	ends     map[string]int // end function calls, by id
	problems []string       // rules an end broke
	endHook  func(*Entry[*state])
}

func newLane(t *testing.T) *lane {
	t.Helper()
	l := &lane{t: t, admitted: make(map[string]int), ends: make(map[string]int)}
	l.reg = New(Config[*state]{OnEnd: l.end})
	l.reg.seams = seams{
		admitDecided: func(id string) { l.pauses.hit(atAdmitDecided, id) },
		endRecorded:  func(id string) { l.pauses.hit(atEndRecorded, id) },
		goChecked:    func(id string) { l.pauses.hit(atGoChecked, id) },
	}
	t.Cleanup(l.cleanup)
	return l
}

// end is the lane's end function.
func (l *lane) end(e *Entry[*state]) {
	st := e.Value()
	id := e.ID()
	select {
	case <-e.Done():
	default:
		l.problem("%s: the end function ran before Done was closed", id)
	}
	if n := st.active.Load(); n != 0 {
		l.problem("%s: the end function ran with %d producers running", id, n)
	}
	if e.Context().Err() == nil {
		l.problem("%s: the end function ran with the context not cancelled", id)
	}
	if e.Cause() == CauseNone {
		l.problem("%s: the end function ran with no cause recorded", id)
	}
	l.mu.Lock()
	l.ends[id]++
	hook := l.endHook
	l.mu.Unlock()
	l.event("end:" + id)
	if hook != nil {
		hook(e)
	}
	st.once.Do(func() { close(st.over) })
}

func (l *lane) problem(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.problems = append(l.problems, fmt.Sprintf(format, args...))
}

// problemsSoFar returns the rules an end has broken.
func (l *lane) problemsSoFar() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.problems...)
}

// eventLog is the ordered record of what happened in a test, for the order of events to be
// checked once the goroutines that made them have finished.
type eventLog struct {
	logMu sync.Mutex
	order []string
}

// event appends to the order of events.
func (l *eventLog) event(name string) {
	l.logMu.Lock()
	defer l.logMu.Unlock()
	l.order = append(l.order, name)
}

// events returns the order of events so far.
func (l *eventLog) events() []string {
	l.logMu.Lock()
	defer l.logMu.Unlock()
	return append([]string(nil), l.order...)
}

// indexOf returns the position of the event in the order, or -1.
func (l *eventLog) indexOf(name string) int {
	for i, e := range l.events() {
		if e == name {
			return i
		}
	}
	return -1
}

func (l *lane) endCount(id string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ends[id]
}

// admit admits a session that no context can end, and fails the test when it is refused.
func (l *lane) admit(id string) *Entry[*state] {
	l.t.Helper()
	e, err := l.admitWith(context.Background(), Spec{ID: id})
	if err != nil {
		l.t.Fatalf("Admit(%q) was refused: %v", id, err)
	}
	return e
}

func (l *lane) admitWith(parent context.Context, spec Spec) (*Entry[*state], error) {
	return l.admitInit(parent, spec, nil)
}

// admitInit is admitWith with a function that runs inside init, under the registry's lock.
func (l *lane) admitInit(parent context.Context, spec Spec, inInit func()) (*Entry[*state], error) {
	return l.reg.Admit(parent, spec, func(context.Context) (*state, error) {
		l.mu.Lock()
		l.admitted[spec.ID]++
		l.mu.Unlock()
		if inInit != nil {
			inInit()
		}
		return &state{over: make(chan struct{})}, nil
	})
}

// awaitEnd returns once the end function of the entry has run, and fails the test if it does
// not in time.
func (l *lane) awaitEnd(e *Entry[*state]) {
	l.t.Helper()
	awaitClosed(l.t, e.Value().over, "the end function of "+e.ID())
}

// awaitRemoved returns once the registry no longer holds id.
func (l *lane) awaitRemoved(id string) {
	l.t.Helper()
	removed := func() bool {
		for _, info := range l.reg.Snapshot() {
			if info.ID == id {
				return false
			}
		}
		return true
	}
	if !poll(mustHappen, removed) {
		l.t.Fatalf("timed out waiting for the registry to remove %q", id)
	}
}

// freeze holds the registry's admission lock until the returned function is called.
func (l *lane) freeze() (thaw func()) {
	l.reg.mu.Lock()
	return l.reg.mu.Unlock
}

// cleanup closes the registry, which fails the test if the entries do not end, and reports the
// rules an end broke.
func (l *lane) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), mustHappen)
	defer cancel()
	if err := l.reg.Close(ctx); err != nil {
		l.t.Errorf("closing the registry at the end of the test: %v", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, p := range l.problems {
		l.t.Errorf("an end broke a rule: %s", p)
	}
	for id, admitted := range l.admitted {
		if n := l.ends[id]; n != admitted {
			l.t.Errorf("the end function ran %d times for %q, which was admitted %d times", n, id, admitted)
		}
	}
}

// point names a crossing the seams of a Registry hold.
type point int

const (
	atAdmitDecided point = iota
	atEndRecorded
	atGoChecked
)

func (p point) String() string {
	return [...]string{"admit decided", "end recorded", "go checked"}[p]
}

// pauses are the seams of a test: a pause is armed for a crossing and an id, and the first
// call that reaches it parks there until the test releases it.
type pauses struct {
	mu    sync.Mutex
	armed map[pauseKey]*pause
}

type pauseKey struct {
	at point
	id string
}

type pause struct {
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

// arm makes the next call that reaches the crossing for id park there. reached is closed when
// one has, and release lets it go.
func (p *pauses) arm(at point, id string) (reached <-chan struct{}, release func()) {
	pa := &pause{reached: make(chan struct{}), release: make(chan struct{})}
	p.mu.Lock()
	if p.armed == nil {
		p.armed = make(map[pauseKey]*pause)
	}
	p.armed[pauseKey{at, id}] = pa
	p.mu.Unlock()
	return pa.reached, func() { pa.once.Do(func() { close(pa.release) }) }
}

// hit is what a seam calls: it parks the first call that finds a pause armed for the crossing.
func (p *pauses) hit(at point, id string) {
	key := pauseKey{at, id}
	p.mu.Lock()
	pa := p.armed[key]
	delete(p.armed, key)
	p.mu.Unlock()
	if pa == nil {
		return
	}
	close(pa.reached)
	<-pa.release
}

// crew runs the goroutines of a test and joins them. This package has no use for a
// sync.WaitGroup, so it is a producers.Group.
type crew struct{ group producers.Group }

func (c *crew) run(fn func()) {
	c.group.Start(1)
	go func() {
		defer c.group.Done()
		fn()
	}()
}

// wait returns once every goroutine of the crew returned, and fails the test when they do not
// in time.
func (c *crew) wait(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), mustHappen)
	defer cancel()
	if err := c.group.WaitContext(ctx); err != nil {
		t.Fatalf("the goroutines of the test did not return within %v", mustHappen)
	}
}

func awaitClosed(t *testing.T, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(mustHappen):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// awaitValue receives from c, and fails the test when nothing arrives in time.
func awaitValue[T any](t *testing.T, c <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-time.After(mustHappen):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

// closeInBackground calls Close on a goroutine and returns the channel its result arrives on.
func closeInBackground(r *Registry[*state], ctx context.Context) <-chan error {
	result := make(chan error, 1)
	go func() { result <- r.Close(ctx) }()
	return result
}

// rngFor returns a random source for one goroutine of a test, seeded by the test's parameters.
func rngFor(seed, stream uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed, stream))
}

// shuffle yields the processor a random number of times, to vary where in its work a
// goroutine is when the others reach theirs.
func shuffle(rng *rand.Rand, most int) {
	for range rng.IntN(most + 1) {
		runtime.Gosched()
	}
}

// recovered runs fn and returns what it panicked with, or nil.
func recovered(fn func()) (panicked any) {
	defer func() { panicked = recover() }()
	fn()
	return nil
}

// awaitBlocked returns once exactly n goroutines are blocked with every one of frames in their
// stack, and fails when that does not happen in time. A goroutine the runtime has parked has
// not returned from the function it is parked in, which no amount of waiting can show.
func awaitBlocked(t *testing.T, n int, frames ...string) {
	t.Helper()
	if !poll(mustHappen, func() bool { return blockedIn(frames...) == n }) {
		t.Fatalf("timed out waiting for %d goroutines blocked in %v; %d are", n, frames, blockedIn(frames...))
	}
}

// awaitGoroutines returns once exactly n goroutines, in any state, have every one of frames in
// their stack. With n zero it is the proof that the goroutines that ran those functions have
// returned, and so that everything they deferred has run.
func awaitGoroutines(t *testing.T, n int, frames ...string) {
	t.Helper()
	if !poll(mustHappen, func() bool { return goroutinesIn(false, frames...) == n }) {
		t.Fatalf("timed out waiting for %d goroutines in %v; %d are", n, frames, goroutinesIn(false, frames...))
	}
}

// poll calls cond until it reports true, pausing a little longer between the calls, and
// reports whether that happened before the limit. It watches a state; it never decides by
// waiting.
func poll(limit time.Duration, cond func() bool) bool {
	end := time.Now().Add(limit)
	wait := 10 * time.Microsecond
	for !cond() {
		if time.Now().After(end) {
			return false
		}
		runtime.Gosched()
		timer := time.NewTimer(wait)
		<-timer.C
		if wait < 2*time.Millisecond {
			wait *= 2
		}
	}
	return true
}

// blockedIn counts the goroutines that the runtime has parked (in a select, on a channel, on a
// lock) and that have every one of frames in their stack.
func blockedIn(frames ...string) int { return goroutinesIn(true, frames...) }

// goroutinesIn counts the goroutines that have every one of frames in their stack: those the
// runtime has parked, or with blockedOnly false all of them. The caller is not counted.
func goroutinesIn(blockedOnly bool, frames ...string) int {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	count := 0
	for i, goroutine := range strings.Split(string(buf), "\n\n") {
		head, _, _ := strings.Cut(goroutine, "\n") // goroutine 7 [select]:
		open := strings.IndexByte(head, '[')
		if i == 0 || open < 0 || !containsAll(goroutine, frames) {
			continue // the first goroutine listed is the caller
		}
		switch state := head[open+1:]; {
		case blockedOnly && (strings.HasPrefix(state, "running") || strings.HasPrefix(state, "runnable") || strings.HasPrefix(state, "syscall")):
		default:
			count++
		}
	}
	return count
}

func containsAll(s string, parts []string) bool {
	for _, part := range parts {
		if !strings.Contains(s, part) {
			return false
		}
	}
	return true
}

// findings collects what the goroutines of a test found wrong, for the test to report once they
// have returned: a goroutine other than the test's own must not stop the test.
type findings struct {
	mu   sync.Mutex
	list []string
}

func (f *findings) add(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.list = append(f.list, fmt.Sprintf(format, args...))
}

// report fails the test for each finding.
func (f *findings) report(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.list {
		t.Error(s)
	}
}
