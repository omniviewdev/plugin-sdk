package exec_test

// Every operation the exec Manager has, as a function a test can run from many goroutines,
// and the checks each one makes on what it gets back. The matrix and the stress of
// Manager.Close are built from them.
//
// The Manager's operations are:
//
//	starts      CreateSession, CreateResourceSession (one fresh ID per call, and one ID shared by all calls)
//	reads       GetSession, ListSessions
//	attach      AttachSession, DetachSession
//	stop        CloseSession (the only way to stop a session from outside: there is no Stop or Kill)
//	input       ResizeSession, and the bytes of a Stream's input (written to the terminal)
//	stream      Stream, which opens a multiplexed stream
//	shutdown    Close
//
// and three things end a session without a call: its process leaves (the handler reports
// it on the stop channel), its terminal hangs up (the reader sees EOF), and its context
// ends. The first two are operations here, so are raced like the others; the third is what
// Close and CloseSession do.

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
)

// operation is one thing a caller can do to a Manager.
type operation struct {
	name string
	run  func(e *env, rng *rand.Rand)
}

// env is the Manager under test, and the state the operations share.
type env struct {
	t testing.TB
	r *rig

	streamCtx context.Context
	input     chan exec.StreamInput // the input of the stream every env opens

	// closeReturned is set once a Close has returned. A start that begins after that must
	// be refused, and no session can exist.
	closeReturned atomic.Bool
	// outputAtClose is the number of outputs delivered when the first Close returned.
	outputAtClose atomic.Int64

	fresh atomic.Int64

	outcomes sync.Map // "operation: outcome" -> *atomic.Int64, see saw

	mu       sync.Mutex
	recent   []string // IDs of sessions that were admitted, newest last, for the operations that act on one
	failures []string
}

// newEnv returns a manager with a stream open on it. The cancel function ends the stream.
func newEnv(t testing.TB) (*env, context.CancelFunc) {
	t.Helper()
	return openEnv(t, newRig(t))
}

// newUnownedEnv is newEnv for a test that makes many of them and closes each itself.
func newUnownedEnv(t testing.TB) (*env, context.CancelFunc) {
	t.Helper()
	return openEnv(t, newUnownedRig())
}

func openEnv(t testing.TB, r *rig) (*env, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	e := &env{t: t, r: r, streamCtx: ctx, input: make(chan exec.StreamInput)}
	r.sink.allowReuse(theOneID)
	if _, err := r.m.Stream(ctx, e.input); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	return e, cancel
}

// fail records a violation. It is safe to call from any goroutine; the test reports them.
func (e *env) fail(format string, args ...any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.failures) < 12 {
		e.failures = append(e.failures, fmt.Sprintf(format, args...))
	}
}

func (e *env) remember(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.recent = append(e.recent, id)
	if len(e.recent) > 24 {
		e.recent = e.recent[len(e.recent)-24:]
	}
}

// target picks the ID of a session that was admitted, or one that never existed.
func (e *env) target(rng *rand.Rand) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.recent) == 0 || rng.IntN(12) == 0 {
		return "no-such-session"
	}
	return e.recent[rng.IntN(len(e.recent))]
}

func (e *env) freshID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, e.fresh.Add(1))
}

// saw counts what an operation returned, so that a test can show which outcomes it covered.
func (e *env) saw(op, outcome string) {
	counter, _ := e.outcomes.LoadOrStore(op+": "+outcome, new(atomic.Int64))
	counter.(*atomic.Int64).Add(1)
}

// summary lists the outcomes saw counted, one per line.
func (e *env) summary() string {
	var lines []string
	e.outcomes.Range(func(key, counter any) bool {
		lines = append(lines, fmt.Sprintf("%-40s %d", key, counter.(*atomic.Int64).Load()))
		return true
	})
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

// closeBound is how long a Close may take before a test calls it hung. Close waits for the
// sessions to end, which takes microseconds; with starts that keep coming and nothing to
// refuse them it can wait for as long as they come.
const closeBound = 20 * time.Second

// close calls Manager.Close and checks what must hold once it returns.
func (e *env) close() {
	select {
	case <-e.r.closeAsync():
	case <-time.After(closeBound):
		e.r.closeHung.Store(true) // the rig's cleanup does not wait for the same sessions again
		e.fail("Close did not return within %s", closeBound)
		return
	}
	e.saw("Close", "returned")
	if e.closeReturned.CompareAndSwap(false, true) {
		e.outputAtClose.Store(e.r.sink.total.Load())
	}
	if sessions, err := e.r.m.ListSessions(nil); err != nil || len(sessions) != 0 {
		e.fail("%d sessions were registered when Close returned (err=%v)", len(sessions), err)
	}
}

// finish ends the test's use of the Manager and reports every violation. Call it once,
// after the goroutines that run operations have returned.
func (e *env) finish(cancelStream context.CancelFunc) {
	e.t.Helper()
	if !e.r.closeHung.Load() {
		e.close()
	}
	cancelStream()
	if !e.r.closeHung.Load() {
		if at, now := e.outputAtClose.Load(), e.r.sink.total.Load(); now != at {
			e.fail("%d outputs were delivered after Close returned", now-at)
		}
		e.r.assertDrained(e.t)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, failure := range e.failures {
		e.t.Error(failure)
	}
	if e.r.closeHung.Load() {
		e.t.FailNow() // the manager is still running sessions: nothing after this can be trusted
	}
}

// ---------------------------------------------------------------------------
// The operations
// ---------------------------------------------------------------------------

var handlerModes = []string{"", "", "", "", "", "", "chatty", "chatty", "fails"}

// theOneID is the ID the CreateSameID operation starts every time: whoever is first admits it,
// the others find it taken, and once its session has ended a later start may admit it again.
const theOneID = "the-one-id"

// maxSessions bounds the sessions admitted in one env. Starts are fast, so while a Close waits
// for the sessions of a manager that does not refuse starts, callers can admit them faster
// than they end; a bound turns that into a failing test instead of an exhausted machine.
const maxSessions = 400

func allOperations() []operation {
	return []operation{
		{"Create", func(e *env, rng *rand.Rand) { e.create(startLegacy, e.freshID("legacy"), rng) }},
		{"CreateResource", func(e *env, rng *rand.Rand) { e.create(startResource, e.freshID("resource"), rng) }},
		{"CreateSameID", func(e *env, rng *rand.Rand) { e.create(startLegacy, theOneID, rng) }},
		{"Get", opGet},
		{"List", opList},
		{"Attach", opAttach},
		{"Detach", opDetach},
		{"CloseSession", opCloseSession},
		{"Resize", opResize},
		{"Write", opWrite},
		{"Stream", opStream},
		{"StreamEnd", opStreamEnd},
		{"Close", func(e *env, _ *rand.Rand) { e.close() }},
		{"ProcessExit", opProcessExit},
		{"TerminalHangup", opTerminalHangup},
	}
}

// create starts a session and checks the outcome. A start is admitted, or refused for one
// of the reasons it may be: the ID exists, the handler fails, or the manager is closing.
// Once a Close has returned, only the last holds.
func (e *env) create(kind startKind, id string, rng *rand.Rand) {
	if e.r.admitted.Load() >= maxSessions {
		return
	}
	closedBefore := e.closeReturned.Load()
	labels := map[string]string{"handler": handlerModes[rng.IntN(len(handlerModes))]}
	op := "start " + kind.String()
	if id == theOneID {
		op += " (one ID)"
	}
	session, err := e.r.start(kind, id, labels)
	switch {
	case err == nil:
		if closedBefore {
			e.fail("a %s start that began after Close returned was admitted as %q", kind, id)
		}
		if session == nil || session.ID != id {
			e.fail("a %s start returned %+v for %q", kind, session, id)
		}
		e.remember(id)
		e.saw(op, "admitted")
	case isRefusal(err):
		if !e.r.closeBegan.Load() {
			e.fail("a %s start was refused while the manager was open: %v", kind, err)
		}
		e.saw(op, "refused")
	case closedBefore:
		e.fail("a %s start that began after Close returned got %v; want the refusal", kind, err)
	case errors.Is(err, exec.ErrSessionExists):
		if id != theOneID {
			e.fail("a start of the fresh ID %q found it taken: %v", id, err)
		}
		e.saw(op, "ID taken")
	case errors.Is(err, exec.ErrTerminalError) && labels["handler"] == "fails":
		e.saw(op, "handler failed")
	default:
		e.fail("a %s start of %q failed unexpectedly: %v", kind, id, err)
	}
}

// settled checks what a call on an existing session may return: it worked, or the session
// is gone. After a Close has returned, only the second.
func (e *env) settled(op string, closedBefore bool, err error) {
	switch {
	case err == nil && closedBefore:
		e.fail("%s found a session after Close returned", op)
	case err == nil:
		e.saw(op, "worked")
	case errors.Is(err, exec.ErrSessionNotFound):
		e.saw(op, "session gone")
	default:
		e.fail("%s failed unexpectedly: %v", op, err)
	}
}

func opGet(e *env, rng *rand.Rand) {
	closedBefore := e.closeReturned.Load()
	_, err := e.r.m.GetSession(nil, e.target(rng))
	e.settled("GetSession", closedBefore, err)
}

func opList(e *env, _ *rand.Rand) {
	closedBefore := e.closeReturned.Load()
	sessions, err := e.r.m.ListSessions(nil)
	if err != nil || (closedBefore && len(sessions) != 0) {
		e.fail("ListSessions returned %d sessions (err=%v) after Close returned=%v", len(sessions), err, closedBefore)
	}
	if len(sessions) > 0 {
		e.saw("ListSessions", "some sessions")
	} else {
		e.saw("ListSessions", "none")
	}
}

func opAttach(e *env, rng *rand.Rand) {
	closedBefore := e.closeReturned.Load()
	_, _, err := e.r.m.AttachSession(nil, e.target(rng))
	e.settled("AttachSession", closedBefore, err)
}

func opDetach(e *env, rng *rand.Rand) {
	closedBefore := e.closeReturned.Load()
	_, err := e.r.m.DetachSession(nil, e.target(rng))
	e.settled("DetachSession", closedBefore, err)
}

func opCloseSession(e *env, rng *rand.Rand) {
	closedBefore := e.closeReturned.Load()
	e.settled("CloseSession", closedBefore, e.r.m.CloseSession(nil, e.target(rng)))
}

func opResize(e *env, rng *rand.Rand) {
	closedBefore := e.closeReturned.Load()
	err := e.r.m.ResizeSession(nil, e.target(rng), int32(1+rng.IntN(60)), int32(1+rng.IntN(200)))
	if err != nil && strings.Contains(err.Error(), "timeout sending resize") {
		e.saw("ResizeSession", "timed out")
		return // the stream was busy for ResizeTimeout: not a violation of anything Close promises
	}
	e.settled("ResizeSession", closedBefore, err)
}

func opWrite(e *env, rng *rand.Rand) {
	timer := time.NewTimer(mustHappen)
	defer timer.Stop()
	select {
	case e.input <- exec.StreamInput{SessionID: e.target(rng), Data: []byte("x")}:
		e.saw("Write", "sent")
	case <-e.streamCtx.Done():
		e.saw("Write", "stream ended")
	case <-timer.C:
		e.fail("the input of the stream stalled")
	}
}

// opStream opens a stream and closes its input, which ends it, as the host's closing its side of
// the RPC does. A stream that ends cancels the sessions it served, by this exit as by the other one that
// opStreamEnd takes; a stream that a later call of opStream or opStreamEnd replaced served none.
func opStream(e *env, _ *rand.Rand) {
	input := make(chan exec.StreamInput)
	if _, err := e.r.m.Stream(e.streamCtx, input); err != nil {
		e.fail("Stream failed: %v", err)
	}
	close(input)
	e.saw("Stream", "opened and closed")
}

// opStreamEnd opens a stream and ends its context. A stream that ends cancels the sessions it served,
// because they can no longer deliver their output; one that a later stream replaced served none.
func opStreamEnd(e *env, _ *rand.Rand) {
	ctx, cancel := context.WithCancel(e.streamCtx)
	if _, err := e.r.m.Stream(ctx, make(chan exec.StreamInput)); err != nil {
		e.fail("Stream failed: %v", err)
	}
	cancel()
	e.saw("StreamEnd", "ended")
}

func opProcessExit(e *env, rng *rand.Rand) {
	if p := e.r.procs.get(e.target(rng)); p != nil {
		p.leave()
		e.saw("ProcessExit", "process left")
		return
	}
	e.saw("ProcessExit", "no process")
}

func opTerminalHangup(e *env, rng *rand.Rand) {
	if p := e.r.procs.get(e.target(rng)); p != nil && p.terminal != nil {
		_ = p.terminal.Close()
		e.saw("TerminalHangup", "terminal hung up")
		return
	}
	e.saw("TerminalHangup", "no terminal")
}
