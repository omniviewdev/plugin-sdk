package exec_test

// The rig behind the tests of Manager.Close. It accounts for everything a Manager and
// its handlers create, so that a test can say what is left after Close: the terminals
// the manager opened, the processes its handlers started, the goroutines it started
// for sessions, and the output it delivered.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	logging "github.com/omniviewdev/plugin-sdk/log"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
)

const (
	// settle is long enough for a goroutine that can make progress to have done so. A test
	// uses it to check that something has NOT happened, which waiting can miss but never fail.
	settle = 100 * time.Millisecond
	// mustHappen bounds the waits for something that has to happen: a correct run never
	// reaches it, so it is long enough for a loaded machine running under -race.
	mustHappen = 30 * time.Second
	// drainWait bounds the wait for a closed manager's leftovers to go: the processes its
	// handlers started end a moment after their sessions' contexts do.
	drainWait = 10 * time.Second
)

// isRefusal reports whether err is what a start gets from a manager that is closing or closed.
func isRefusal(err error) bool { return resourcev2.ErrorCode(err) == "CONNECTION_ERROR" }

// ---------------------------------------------------------------------------
// Terminals
// ---------------------------------------------------------------------------

// terminalLedger counts the terminals a manager gets from its factory and the ones it
// closes. A leaked terminal is a leaked descriptor.
type terminalLedger struct {
	created atomic.Int64
	closed  atomic.Int64
	byTTY   sync.Map // the side a handler receives (*os.File) -> *ledgerTerminal
	// gate, when set, holds every Close of a terminal created while it was set, until it is
	// closed: a session cannot finish while its terminal is held, which makes Manager.Close take
	// as long as the test wants.
	gate atomic.Pointer[chan struct{}]
}

type ledgerTerminal struct {
	*exectest.FakeTerminal

	ledger *terminalLedger
	gate   *chan struct{} // the ledger's gate when the terminal was created
	once   sync.Once
}

func (t *ledgerTerminal) Close() error {
	if t.gate != nil {
		<-*t.gate
	}
	err := t.FakeTerminal.Close()
	t.once.Do(func() {
		t.ledger.closed.Add(1)
		t.ledger.byTTY.Delete(t.SlaveFd())
	})
	return err
}

func (l *terminalLedger) factory() exec.TerminalFactory {
	return func() (exec.Terminal, error) {
		fake, err := exectest.NewFakeTerminal()
		if err != nil {
			return nil, err
		}
		l.created.Add(1)
		terminal := &ledgerTerminal{FakeTerminal: fake, ledger: l, gate: l.gate.Load()}
		l.byTTY.Store(fake.SlaveFd(), terminal)
		return terminal, nil
	}
}

// ---------------------------------------------------------------------------
// Processes
// ---------------------------------------------------------------------------

// processLedger counts the processes a handler starts and the ones that have exited. A
// process lives until its session's context ends, unless the test makes it leave.
type processLedger struct {
	started atomic.Int64
	exited  atomic.Int64

	mu   sync.Mutex
	byID map[string]*fakeProcess
}

type fakeProcess struct {
	exit     chan struct{}
	exitOnce sync.Once
	terminal *ledgerTerminal // the terminal the process writes to; nil when the factory is not the rig's
}

// leave makes the process end by itself: the handler reports it on the stop channel.
func (p *fakeProcess) leave() { p.exitOnce.Do(func() { close(p.exit) }) }

func (l *processLedger) add(id string, p *fakeProcess) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byID == nil {
		l.byID = make(map[string]*fakeProcess)
	}
	l.byID[id] = p
}

func (l *processLedger) get(id string) *fakeProcess {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.byID[id]
}

// ---------------------------------------------------------------------------
// Output
// ---------------------------------------------------------------------------

// auditSink records what the manager delivers, and notices output that follows a session's
// end: the Close signal is the last thing a session sends. Output names its session by ID
// only, so the order of a session's output can be audited for an ID only while no later
// session reuses it; an ID a test reuses is declared with allowReuse and is counted, not audited.
type auditSink struct {
	total        atomic.Int64
	closeSignals atomic.Int64
	// holdData, when set, holds every delivery of session data (not a signal) at the gate,
	// which stands for a consumer that is slow to take it.
	holdData atomic.Pointer[gate]

	mu         sync.Mutex
	closed     map[string]bool
	reused     map[string]bool
	afterClose []string
	twice      []string
}

func newAuditSink() *auditSink {
	return &auditSink{closed: make(map[string]bool), reused: make(map[string]bool)}
}

func (s *auditSink) allowReuse(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reused[id] = true
}

func (s *auditSink) OnOutput(o exec.StreamOutput) {
	if g := s.holdData.Load(); g != nil && o.Signal == exec.StreamSignalNone && len(o.Data) > 0 {
		g.hold()
	}
	s.total.Add(1)
	if o.Signal == exec.StreamSignalClose {
		s.closeSignals.Add(1)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reused[o.SessionID] {
		return
	}
	if o.Signal == exec.StreamSignalClose {
		if s.closed[o.SessionID] {
			s.twice = append(s.twice, o.SessionID)
		}
		s.closed[o.SessionID] = true
		return
	}
	if s.closed[o.SessionID] && len(s.afterClose) < 8 {
		s.afterClose = append(s.afterClose, fmt.Sprintf("%s: %s seq=%d %q", o.SessionID, o.Signal, o.Seq, o.Data))
	}
}

func (s *auditSink) problems() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, a := range s.afterClose {
		out = append(out, "output after the session's close signal: "+a)
	}
	for _, id := range s.twice {
		out = append(out, "a second close signal for session "+id)
	}
	return out
}

// ---------------------------------------------------------------------------
// Logger
// ---------------------------------------------------------------------------

// hookLogger runs a test's hook on every Debugw message. It is a seam to stop the manager at
// a place between two of its steps that nothing else exposes: "session created" is logged
// after a session is admitted and before its goroutines start.
type hookLogger struct {
	logging.Logger

	hook *atomic.Pointer[func(string)]
}

func (l hookLogger) Named(name string) logging.Logger {
	return hookLogger{Logger: l.Logger.Named(name), hook: l.hook}
}

func (l hookLogger) With(fields ...logging.Field) logging.Logger {
	return hookLogger{Logger: l.Logger.With(fields...), hook: l.hook}
}

func (l hookLogger) Debugw(ctx context.Context, msg string, kv ...any) {
	if fn := l.hook.Load(); fn != nil {
		(*fn)(msg)
	}
	l.Logger.Debugw(ctx, msg, kv...)
}

// ---------------------------------------------------------------------------
// The rig
// ---------------------------------------------------------------------------

type startKind int

const (
	// startLegacy is Manager.CreateSession: a session without a resource target.
	startLegacy startKind = iota
	// startResource is Manager.CreateResourceSession.
	startResource
)

func (k startKind) String() string {
	if k == startResource {
		return "resource"
	}
	return "legacy"
}

type rig struct {
	m     *exec.Manager
	sink  *auditSink
	terms *terminalLedger
	procs *processLedger

	// inHandler, when set, runs inside every handler call, after the manager created the
	// terminal and before the session is admitted.
	inHandler atomic.Pointer[func(exec.SessionOptions)]
	// onLog, when set, sees every debug message of the manager.
	onLog atomic.Pointer[func(string)]

	admitted   atomic.Int64 // starts that returned a session
	closeBegan atomic.Bool  // set before the first Close call: a refusal is only legitimate after it
}

// newRig returns a manager with one handler for legacy sessions and one for resource sessions.
// It closes the manager when the test ends.
func newRig(t testing.TB) *rig {
	t.Helper()
	r := newUnownedRig()
	t.Cleanup(r.m.Close)
	return r
}

// newUnownedRig is newRig for a test that makes many managers and closes each itself.
func newUnownedRig() *rig {
	r := &rig{sink: newAuditSink(), terms: &terminalLedger{}, procs: &processLedger{}}
	r.m = exec.NewManager(exec.ManagerConfig{
		Logger: hookLogger{Logger: logging.NewNop(), hook: &r.onLog},
		Handlers: map[string]exec.Handler{
			"/":        {TTYHandler: r.ttyHandler},
			"test/pod": {Plugin: "test", Resource: "pod", TTYHandler: r.ttyHandler},
		},
		Sink:            r.sink,
		TerminalFactory: r.terms.factory(),
	})
	return r
}

// ttyHandler starts the session's process. The process ends with the session's context, or
// when the test tells it to; a session labelled handler=chatty has it write output, and one
// labelled handler=fails is refused by the handler itself.
func (r *rig) ttyHandler(
	pc *types.PluginContext,
	opts exec.SessionOptions,
	tty *os.File,
	stopCh chan error,
	_ <-chan exec.SessionResizeInput,
) error {
	if hook := r.inHandler.Load(); hook != nil {
		(*hook)(opts)
	}
	mode := opts.Labels["handler"]
	if mode == "fails" {
		return errors.New("the handler refused the session")
	}
	terminal, _ := r.terms.byTTY.Load(tty)
	out, _ := terminal.(*ledgerTerminal)
	process := &fakeProcess{exit: make(chan struct{}), terminal: out}
	r.procs.add(opts.ID, process)
	r.procs.started.Add(1)
	go func() {
		defer r.procs.exited.Add(1)
		var tick <-chan time.Time
		if out != nil && mode == "chatty" {
			ticker := time.NewTicker(200 * time.Microsecond)
			defer ticker.Stop()
			tick = ticker.C
		}
		for {
			select {
			case <-pc.Context.Done():
				return
			case <-process.exit:
				select {
				case stopCh <- &exec.ExecError{Err: errors.New("process exited"), Title: "Exited", Message: "The process exited."}:
				default:
				}
				return
			case <-tick:
				_, _ = out.InjectOutput([]byte("output\n"))
			}
		}
	}()
	return nil
}

func (r *rig) pluginContext() *types.PluginContext {
	return &types.PluginContext{Context: context.Background()}
}

// start asks the manager for a session, and counts it when the manager admits it.
func (r *rig) start(kind startKind, id string, labels map[string]string) (*exec.Session, error) {
	var (
		session *exec.Session
		err     error
	)
	if kind == startResource {
		session, err = r.m.CreateResourceSession(r.pluginContext(), exec.SessionOptions{
			ID: id, Labels: labels, ResourcePlugin: "test", ResourceKey: "pod",
		})
	} else {
		session, err = r.m.CreateSession(r.pluginContext(), exec.SessionOptions{ID: id, Labels: labels, Command: []string{"sh"}})
	}
	if err == nil {
		r.admitted.Add(1)
	}
	return session, err
}

type startResult struct {
	session *exec.Session
	err     error
}

func (r *rig) startAsync(kind startKind, id string) <-chan startResult {
	done := make(chan startResult, 1)
	go func() {
		session, err := r.start(kind, id, nil)
		done <- startResult{session, err}
	}()
	return done
}

// closeManager is Manager.Close, noting first that it has begun.
func (r *rig) closeManager() {
	r.closeBegan.Store(true)
	r.m.Close()
}

// closeAsync runs closeManager in a goroutine and returns a channel closed when it returns.
func (r *rig) closeAsync() <-chan struct{} {
	done := make(chan struct{})
	go func() {
		r.closeManager()
		close(done)
	}()
	return done
}

// ---------------------------------------------------------------------------
// What is left
// ---------------------------------------------------------------------------

// sessionGoroutines returns the stacks of the goroutines the manager started for sessions
// (the two workers and the one that waits for them) that are still alive.
func sessionGoroutines() []string {
	var dump string
	for size := 1 << 20; ; size *= 2 {
		buf := make([]byte, size)
		if n := runtime.Stack(buf, true); n < size {
			dump = string(buf[:n])
			break
		}
	}
	var out []string
	for _, g := range strings.Split(dump, "\n\n") {
		if strings.Contains(g, "created by github.com/omniviewdev/plugin-sdk/pkg/v1/exec.(*Manager).createSession in goroutine") ||
			strings.Contains(g, "exec.(*Manager).createSession.func") {
			out = append(out, g)
		}
	}
	return out
}

// awaitClosed fails the test when c is not closed within mustHappen.
func awaitClosed(t testing.TB, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(mustHappen):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// assertStillBlocked fails the test when c is closed: it gives what should be blocked a
// moment to show that it is not. It can miss a goroutine that returns late, and it never
// fails a correct one.
func assertStillBlocked(t testing.TB, c <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-c:
		t.Fatal(msg)
	case <-time.After(settle):
	}
}

// drained reports what is still alive of a manager's sessions, empty when it is all gone:
// goroutines of sessions, terminals that were opened and not closed, processes that were
// started and have not exited.
func (r *rig) drained() (left []string) {
	if goroutines := sessionGoroutines(); len(goroutines) > 0 {
		left = append(left, fmt.Sprintf("%d goroutines of sessions are alive:\n%s", len(goroutines), strings.Join(goroutines, "\n\n")))
	}
	if created, closed := r.terms.created.Load(), r.terms.closed.Load(); created != closed {
		left = append(left, fmt.Sprintf("terminals: %d created, %d closed", created, closed))
	}
	if started, exited := r.procs.started.Load(), r.procs.exited.Load(); started != exited {
		left = append(left, fmt.Sprintf("processes: %d started, %d exited", started, exited))
	}
	return left
}

// assertDrained checks the state a manager must be left in once Close has returned and
// every start has returned: nothing registered, no goroutine of a session, every terminal
// closed, every process gone, and every admitted session has said goodbye, once and last.
// The processes a handler started, and the last instructions of a session's goroutines,
// end a moment after Close returns, so those are awaited; the rest must hold at once.
func (r *rig) assertDrained(t testing.TB) {
	t.Helper()
	if sessions, err := r.m.ListSessions(nil); err != nil || len(sessions) != 0 {
		t.Errorf("sessions left after Close: %d (err=%v)", len(sessions), err)
	}
	deadline := time.Now().Add(drainWait)
	left := r.drained()
	for len(left) > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		left = r.drained()
	}
	for _, l := range left {
		t.Errorf("after Close: %s", l)
	}
	if signals, admitted := r.sink.closeSignals.Load(), r.admitted.Load(); signals != admitted {
		t.Errorf("close signals: %d delivered for %d admitted sessions", signals, admitted)
	}
	for _, problem := range r.sink.problems() {
		t.Error(problem)
	}
}
