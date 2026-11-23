package exec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/internal/obs"
	"github.com/omniviewdev/plugin-sdk/internal/session"
	logging "github.com/omniviewdev/plugin-sdk/log"
	sdkdiag "github.com/omniviewdev/plugin-sdk/pkg/diagnostics"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The engine runs the commands of a plugin's targets. It is the part of the exec lane that the interactive session
// (X3a) and the bounded run (X2b) are built on, and it is driven in process: what it takes are admitted requests, and
// what it gives are a handle to the running command (its pipes, its facts, its end) and the one typed End.
//
// A command is the author's ExecFunc running with a Proc. The engine owns everything around it:
//
//   - the lifecycle, in one place (internal/session): a command is admitted under the registry's lock (typed refusals for
//     a closed plugin, a live id and a full target), every goroutine of a command is started through its entry, the End
//     is emitted once and last, and shutdown is a state. The author's function is the one goroutine of a command that
//     the registry does not wait for beyond a bound: a function that ignores its cancelled context must not hold a
//     session, a shutdown or a connection for ever, so the engine gives it a grace and then gives up on it, loudly;
//   - the connection: the command holds the lease of its connection for as long as it runs, so a retired connection ends
//     the command with a typed cause (Closed, connection-retired) instead of leaving it on a dead client;
//   - the stdio: bounded queues between the host and the function (pipes.go) with the stated blocking and dropping rules;
//   - the facts: the capabilities the command was opened with (the kind's, narrowed by the mode its selection names),
//     the single Started that precedes the first output (start_gate.go), the link state;
//   - the end: classify.go.
//
// What the engine does with a function that breaks the contract is decided in one place and tested as a set: it returns
// before the command ended (the session ends at the return and later writes fail), writes after it returned (refused,
// counted, logged once), ignores its context (abandoned after the grace, counted in the abandoned gauge), panics (a
// Failed end of ClassInternal, never the plugin), never reads Proc.Resize (nothing blocks; a warning after a wait),
// returns an exit 0 for a connection it lost (read as the transport's word where the SDK has evidence), or reports its
// start twice or too late (refused). Each is either loud (a log line, a diagnostics count, a violation on the result) or
// contained, and most are both.

const (
	// defaultCloseGrace is how long a graceful end waits for the function to return after its context was cancelled before
	// the engine gives up on it. A kill does not wait.
	defaultCloseGrace = 5 * time.Second
	// defaultResizeUnreadAfter is how long a size may wait for a function that declared Resize and does not read it before
	// the engine says so in the log.
	defaultResizeUnreadAfter = 5 * time.Second
	// defaultMetricsEvery is how often a running session reports its traffic to the diagnostics.
	defaultMetricsEvery = 10 * time.Second
)

// commandKind is the shape a command was started for.
type commandKind int

const (
	// kindSession is an interactive session: it counts against Session.MaxSessions and outlives its request.
	kindSession commandKind = iota
	// kindRun is a bounded run: it counts against Run.MaxConcurrent and ends with the call that owns it.
	kindRun
)

func (k commandKind) String() string {
	if k == kindRun {
		return "run"
	}
	return "session"
}

// engineConfig is what an engine is built from.
type engineConfig struct {
	log     logging.Logger
	targets *targetRegistry
	// connections returns the registry of the plugin's connections, or nil when none is bound. It is read at each start:
	// the SDK binds it after the capability is registered.
	connections func() resourcev2.ConnectionContextProvider
	// diagnostics is the registry the engine publishes to; nil is the process registry.
	diagnostics *sdkdiag.Registry
	// closeGrace, resizeUnreadAfter and metricsEvery are the engine's timings; zero is the default. They are fields so that
	// a test can shorten them.
	closeGrace        time.Duration
	resizeUnreadAfter time.Duration
	metricsEvery      time.Duration
}

// engine runs the commands of one plugin.
type engine struct {
	cfg engineConfig
	log logging.Logger
	reg *session.Registry[*command]
	tel *telemetry

	// afterFunctionEnded is a test seam, nil in production: it runs on the function's goroutine right after the function
	// ended its session and before it reports what it returned, which is the one moment at which the order of the function's
	// own bookkeeping and the supervisor's decision about it can be forced. A test sets it before it starts a command.
	afterFunctionEnded func()

	// afterAbandonDecision is a test seam, nil in production: it runs on the supervisor's goroutine right after it decided,
	// by the compare-and-swap that the function's own return also makes, whether it gave up on the function.
	afterAbandonDecision func(abandoned bool)
}

// newEngine returns an engine for the targets of cfg. It runs nothing until a command starts.
func newEngine(cfg engineConfig) *engine {
	if cfg.log == nil {
		cfg.log = logging.NewNop()
	}
	if cfg.connections == nil {
		cfg.connections = func() resourcev2.ConnectionContextProvider { return nil }
	}
	if cfg.closeGrace <= 0 {
		cfg.closeGrace = defaultCloseGrace
	}
	if cfg.resizeUnreadAfter <= 0 {
		cfg.resizeUnreadAfter = defaultResizeUnreadAfter
	}
	if cfg.metricsEvery <= 0 {
		cfg.metricsEvery = defaultMetricsEvery
	}
	e := &engine{cfg: cfg, log: cfg.log, tel: newTelemetry(cfg.diagnostics)}
	e.reg = session.New(session.Config[*command]{OnEnd: e.onEnd})
	return e
}

// setLogger replaces the engine's logger. The SDK names the logger after the plugin when it registers the capability,
// before anything serves, so it is not guarded.
func (e *engine) setLogger(log logging.Logger) {
	if log == nil {
		log = logging.NewNop()
	}
	e.log = log
}

// close refuses every later start, ends every command (graceful: each function is cancelled and given the grace) and waits
// until each has ended and emitted its end, or until ctx ends. It may be called again and by many goroutines.
func (e *engine) close(ctx context.Context) error { return e.reg.Close(ctx) }

// live returns the ids of the commands the engine holds, sorted.
func (e *engine) live() []string {
	var ids []string
	for _, info := range e.reg.Snapshot() {
		ids = append(ids, info.ID)
	}
	return ids
}

// startParams is one command to start. The request is validated: admission has checked it against the target and the
// build (admission_request_v2.go), and the engine checks what only a live target can tell (its options).
type startParams struct {
	// ID is the command's id, unique among the commands the engine holds: the host's session id, or a run's request id.
	ID   string
	Kind commandKind
	// Target is the registered target the reference names.
	Target *registeredTarget
	// Route is the connection that reaches the target; it is leased for as long as the command runs.
	Route resourcev2.AccessRoute
	// Request is what the function receives, a copy of it.
	Request Request
	// Stdin says the command has an input. A session always has one; a run has one only when its request carries stdin.
	Stdin bool
	// ReplayBytes is the output queue of a session whose target stalls lossily, in bytes; zero is the target's default.
	ReplayBytes int
	// Owner, when set, is the context of the call that owns the command: when it ends, so does the command (CauseCancelled).
	// A session has none: it outlives the request that opened it.
	Owner context.Context
}

// start runs a command. ctx carries what the request arrived with (the plugin context, the connection, the catalog
// pin) and bounds the admission only: once the command has been handed to its function it no longer depends on ctx.
//
// A refusal means nothing ran: *Error for a refusal the exec lane classifies (a closed plugin, a live id, a full target, a
// selection the options do not allow, a failure of the target's own Options), the resource lane's own
// *resourcev2.ResourceOperationError for the connection and catalog admission (RESET_REQUIRED, a connection that is not
// started), and an *Error of ClassCancelled or ClassTimeout when ctx ended first. Everything after the hand-over is the
// command's End.
func (e *engine) start(ctx context.Context, p startParams) (*command, error) {
	if p.ID == "" || p.Target == nil {
		return nil, &Error{Class: ClassInternal, Err: errors.New("start without an id or a target")}
	}
	if p.Kind == kindRun && !p.Target.servesRuns() || p.Kind == kindSession && !p.Target.servesSessions() {
		return nil, unsupportedRequest("the target does not serve %ss", p.Kind)
	}
	group, limit, tooMany := admissionGroup(p)
	parent := context.WithoutCancel(ctx)
	if p.Owner != nil {
		parent = p.Owner
	}
	var c *command
	entry, err := e.reg.Admit(parent, session.Spec{ID: p.ID, Group: group, Limit: limit}, func(context.Context) (*command, error) {
		c = e.newCommand(p)
		return c, nil
	})
	if err != nil {
		return nil, admissionRefusal(err, tooMany)
	}
	c.entry = entry
	pc := types.PluginContextFromContext(ctx)
	if pc == nil {
		pc = &types.PluginContext{Context: ctx}
	}
	supervised := *pc
	supervised.Context = entry.Context() // the lease, the options and the function all end with the entry
	if err := ctx.Err(); err != nil {
		// The caller gave up before the command could begin: nothing runs for it, and the entry is gone before it hears.
		entry.End(session.CauseCancelled)
		<-entry.Removed()
		return nil, contextRefusal(err)
	}
	if !entry.Go(func() { c.supervise(supervised) }) {
		<-entry.Removed() // the engine closed under this start: nothing ran, and the entry is gone before the caller hears
		return nil, refusalForCause(entry.Cause(), nil)
	}
	select {
	case err := <-c.admitted:
		if err != nil {
			// A refusal leaves nothing behind: the entry is removed before the caller hears of it, so that a retry of the
			// same id is not refused for the command that has just been refused.
			<-entry.Removed()
			return nil, err
		}
		return c, nil
	case <-ctx.Done():
		if c.giveUp() {
			entry.End(session.CauseCancelled)
			return nil, contextRefusal(ctx.Err())
		}
		// The command was handed to its function while ctx ended: it is a command, and the caller gets it.
		if err := <-c.admitted; err != nil {
			return nil, err
		}
		return c, nil
	}
}

// admissionGroup is the group a command is counted in and the limit and refusal of that group.
func admissionGroup(p startParams) (group string, limit int, reason ErrorReason) {
	info := &p.Target.info
	// The key is the pair, not Provider + "/" + Type, because a resource type may itself hold a slash: two targets must
	// not share a group by the way their names run together. A NUL cannot appear in either (both are plain text).
	key := info.Provider + "\x00" + info.Type
	if p.Kind == kindRun {
		return "run\x00" + key, info.Run.MaxConcurrent, ReasonTooManyRuns
	}
	return "session\x00" + key, info.Session.MaxSessions, ReasonTooManySessions
}

// admissionRefusal types an error of the registry's admission.
func admissionRefusal(err error, tooMany ErrorReason) error {
	switch {
	case errors.Is(err, session.ErrClosed):
		return &Error{Class: ClassUnavailable, Err: err}
	case errors.Is(err, session.ErrExists):
		return NewError(ClassConflict, ReasonSessionExists, err)
	case errors.Is(err, session.ErrLimit):
		return NewError(ClassLimit, tooMany, err)
	}
	return &Error{Class: ClassInternal, Err: err}
}

// contextRefusal is the refusal of a start whose context ended: ClassTimeout for a deadline, ClassCancelled otherwise.
func contextRefusal(err error) *Error { return contextError(err) }

// refusalForCause is the refusal of a start that the SDK ended before the command was handed to its function. cause is
// the registry's; err is what the lease or the options said, when they said anything.
func refusalForCause(cause session.Cause, err error) error {
	switch cause {
	case session.CauseShutdown:
		return &Error{Class: ClassUnavailable, Err: err}
	case session.CauseCancelled:
		return &Error{Class: ClassCancelled, Err: err}
	case session.CauseRetired:
		return NewError(ClassConnection, ReasonConnectionRetired, err)
	case session.CauseNone, session.CauseFinished, session.CauseRequested, session.CauseExpired, session.CauseTimeout:
	}
	if err != nil {
		return err
	}
	return &Error{Class: ClassCancelled}
}

// phase is how far a command got in its start.
type phase int

const (
	phaseAdmitting phase = iota // the lease and the options are being taken
	phaseRunning                // handed to the function
	phaseRefused                // never handed over: nothing ran
)

// Function states: the function runs, it returned, or the engine gave up on it and it has not returned yet.
const (
	fnRunning int32 = iota
	fnReturned
	fnAbandoned
)

// command is one command: the author's function running with a Proc, its pipes and its end.
type command struct {
	e       *engine
	id      string
	kind    commandKind
	target  *registeredTarget
	route   resourcev2.AccessRoute
	request Request
	// replayBytes is the output queue the host asked for a lossy session; zero is the target's default.
	replayBytes int
	created     time.Time
	log         logging.Logger

	// entry is set by start before any goroutine of the command exists, and read only by them and by what start hands out.
	entry    *session.Entry[*command]
	admitted chan error // the answer to start: nil once handed over, an error for a refusal

	in  *inputPipe // nil: the command has no input
	out *outputPipe
	rz  *resizer

	// opened is what Opened carries: the kind's capabilities narrowed by the mode the selection names. It and gate are set
	// once, at the hand-over, under mu.
	opened Capabilities
	gate   *startGate

	mu           sync.Mutex
	phase        phase
	started      *SessionFacts // what the host receives in Started, once
	link         LinkState
	linkSet      bool
	applied      resizeRequest // the last size the function took that a host sent
	appliedSet   bool
	violations   []violation
	violationSet map[string]bool

	fnReturned atomic.Bool // the function returned: whatever it does afterwards is late
	outputSeen atomic.Bool // the first byte of output has gone through the start gate
	startedC   chan struct{}
	linkC      chan struct{} // capacity 1: the link state changed
	appliedC   chan struct{} // capacity 1: the function took a size a host sent
	killC      chan struct{} // closed by a kill: the end does not wait for the function
	killOnce   sync.Once

	span trace.Span
	// flushed is what the traffic metrics have been told so far; only the supervisor reads or writes it.
	flushed struct{ out, in, dropped, stalls uint64 }

	doneC  chan struct{}
	result commandResult
}

// commandResult is the end of a command and its accounts, what the engine's consumer sends as Ended or as a run's result.
// It is final: Done is closed after it is set.
type commandResult struct {
	// End is the typed end. Exactly one arm is set.
	End *execv2.End
	// Cause is why the session ended, the first of the causes that asked.
	Cause session.Cause
	// StartObserved is true once the function called Started, the first output arrived, or the command ended with a
	// status; false means the start was not observed, not that the command never ran.
	StartObserved bool
	Duration      time.Duration
	// BytesOut is what the function wrote and the queue accepted (the output offset at the end), BytesIn what the host sent,
	// DroppedBytes what the queue overwrote before its reader took it, and LateWrites and LateBytes the writes the function
	// made after the end, which were refused.
	BytesOut, BytesIn, DroppedBytes uint64
	LateWrites, LateBytes           uint64
	// Output is the most the engine can say about the output: COMPLETE only for a status the target relayed with nothing
	// lost or refused; a consumer that did not deliver every byte says less.
	Output execv2.OutputCompleteness
	// Abandoned is true when the engine did not wait for the function to return: it ignored its cancelled context for the
	// grace, or the end was a kill.
	Abandoned bool
	// Violations are the ways the function broke the contract, one for each kind, in the order they happened.
	Violations []string
}

// newCommand builds the command of an admitted start. It runs under the registry's lock: it allocates and nothing else.
// The output queue is made at the hand-over, when the capabilities that decide its policy are known.
func (e *engine) newCommand(p startParams) *command {
	c := &command{
		e: e, id: p.ID, kind: p.Kind, target: p.Target, route: p.Route, replayBytes: p.ReplayBytes,
		request:      cloneRequest(p.Request),
		created:      time.Now(),
		admitted:     make(chan error, 1),
		violationSet: map[string]bool{},
		startedC:     make(chan struct{}),
		linkC:        make(chan struct{}, 1),
		appliedC:     make(chan struct{}, 1),
		killC:        make(chan struct{}),
		doneC:        make(chan struct{}),
	}
	c.log = e.log.With(
		logging.String("session", p.ID), logging.String("provider", p.Target.info.Provider),
		logging.String("type", p.Target.info.Type), logging.String("kind", p.Kind.String()),
	)
	if p.Stdin {
		c.in = newInputPipe(stdioQueueBytes)
	}
	c.rz = newResizer(c.initialSize(), c.resized, e.cfg.resizeUnreadAfter, c.resizeUnread)
	return c
}

// initialSize is the size the first value of Proc.Resize carries: the request's, for a terminal.
func (c *command) initialSize() Size {
	if c.request.TTY {
		return c.request.Size
	}
	return Size{}
}

// cloneRequest returns a copy of r that shares no storage with it: the function may change its own.
func cloneRequest(r Request) Request {
	r.Ref = cloneRequestReference(r.Ref)
	r.Selection = maps.Clone(r.Selection)
	r.Argv = slices.Clone(r.Argv)
	r.Env = maps.Clone(r.Env)
	r.Labels = maps.Clone(r.Labels)
	return r
}

// ---------------------------------------------------------------------------------------------------------------
// the supervisor: one goroutine per command, a producer of its entry
// ---------------------------------------------------------------------------------------------------------------

// supervise takes the lease, resolves the options, hands the command to its function and watches it until it has ended.
// It is the producer that keeps the command's entry alive: the entry's end (and so the End the consumer receives) follows
// it, not the function.
func (c *command) supervise(pc types.PluginContext) {
	leaseErr := resourcev2.WithUtilityConnection(&pc, c.e.cfg.connections(), &c.request.Ref, &c.route, c.runLeased)
	if c.handedOver() {
		// What the lease says after the command ran (a catalog check at its end, a context that ended) is not the command's.
		if leaseErr != nil {
			c.log.Debugw(context.Background(), "exec lease released", "error_code", obs.ErrorCode(leaseErr, resourcev2.ErrorCode(leaseErr)))
		}
		return
	}
	c.refuse(leaseErr)
}

// refuse is the end of a start that never handed the command to its function: the caller gets the refusal and the
// entry ends with no End to emit.
func (c *command) refuse(err error) {
	c.mu.Lock()
	if c.phase == phaseAdmitting {
		c.phase = phaseRefused
	}
	c.mu.Unlock()
	// Ended by the SDK (a shutdown, a caller that gave up) before the hand-over: the refusal says so, whatever the lease
	// or the options were doing when their context ended.
	cause := c.entry.Cause()
	if err == nil || (cause != session.CauseNone && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))) {
		err = refusalForCause(cause, err)
	}
	c.admitted <- err
	c.entry.End(session.CauseFinished)
}

// handedOver reports whether the command was handed to its function.
func (c *command) handedOver() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.phase == phaseRunning
}

// giveUp is the caller's last word before the hand-over: if the command has not been handed over, it never will be.
func (c *command) giveUp() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.phase != phaseAdmitting {
		return false
	}
	c.phase = phaseRefused
	return true
}

// handOver moves the command to the function: from here on the start is no longer a refusal. It reports false when the
// command was refused, or the SDK ended it, while the lease and the options were taken.
func (c *command) handOver(opened Capabilities) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.phase != phaseAdmitting || c.entry.Cause() != session.CauseNone {
		return false
	}
	c.phase = phaseRunning
	c.opened = opened
	c.gate = newStartGate(c.request, opened)
	c.out = c.newOutput(opened)
	return true
}

// runLeased runs inside the connection's lease for as long as the command runs. It returns when the command has ended
// (or was never handed over); the lease is released when it returns.
func (c *command) runLeased(bound *types.PluginContext) error {
	opened, refusal := c.resolveOptions(bound)
	if refusal != nil {
		return refusal
	}
	if !c.handOver(opened) {
		return nil
	}

	authorCtx, cancelAuthor := context.WithCancel(bound.Context)
	defer cancelAuthor()
	author := *bound
	author.Context = authorCtx
	proc := &Proc{
		Context:      &author,
		Request:      cloneRequest(c.request),
		Capabilities: cloneCapabilities(opened),
		Stdout:       commandWriter{c: c, stream: c.stdoutStream()},
		Resize:       c.rz.channel(),
		Started:      c.reportStarted,
		Link:         c.reportLink,
	}
	if c.in != nil {
		proc.Stdin = c.in
	}
	if !c.request.TTY {
		proc.Stderr = commandWriter{c: c, stream: execv2.Stream_STREAM_STDERR}
	}

	c.beginObservation(bound.Context)
	if !c.entry.Go(c.rz.run) {
		// The entry ended before the resizer could start: run it to its end here, which closes Proc.Resize.
		c.rz.stop()
		c.rz.run()
	}
	var state atomic.Int32
	returned := make(chan functionOutcome, 1)
	// The function's goroutine is the one goroutine of a command that the registry does not wait for beyond the grace:
	// see abandon.
	go c.callFunction(authorCtx, proc, &state, returned)
	c.admitted <- nil

	outcome, gaveUp := c.await(bound.Context, returned, &state, cancelAuthor)
	c.closeStdio()
	c.conclude(outcome, gaveUp)
	return nil
}

// stdoutStream is the stream the function's Stdout writes: the terminal's, or standard output.
func (c *command) stdoutStream() execv2.Stream {
	if c.request.TTY {
		return execv2.Stream_STREAM_TTY
	}
	return execv2.Stream_STREAM_STDOUT
}

// newOutput makes the output queue for the capabilities the command was opened with: a target that stalls lossily gets
// the lossy policy and, for a session, a queue of the replay ring's size; any other gets a queue that blocks.
func (c *command) newOutput(opened Capabilities) *outputPipe {
	if opened.Stall != StallLossy {
		return newOutputPipe(stdioQueueBytes, false)
	}
	capacity := stdioQueueBytes
	if c.kind == kindSession {
		capacity = c.target.info.Session.ReplayBytes
		if c.replayBytes > 0 {
			capacity = c.replayBytes
		}
	}
	return newOutputPipe(capacity, true)
}

// callFunction runs the author's function and reports how it ended. It decides who ended the session first: if the
// function's context was cancelled before it returned, the SDK ended it (whoever asked), and what the function returned
// is the aftermath; otherwise the function ended it. It closes the stdio before it reports, so that a goroutine the
// function left behind is refused at once.
func (c *command) callFunction(ctx context.Context, proc *Proc, state *atomic.Int32, returned chan<- functionOutcome) {
	var outcome functionOutcome
	defer func() {
		if recovered := recover(); recovered != nil {
			outcome = panicOutcome(recovered)
		}
		c.fnReturned.Store(true)
		c.closeStdio()
		// Whether the engine gave up on this function is decided here, once and before the end is recorded: the engine's
		// grace and kill paths decide it with the same compare-and-swap, so a function that has returned is never taken for
		// an abandoned one, whatever order its return and the end reached the supervisor in.
		gaveUpOn := !state.CompareAndSwap(fnRunning, fnReturned)
		cause := session.CauseFinished
		if ctx.Err() != nil {
			// The function's context ended before it returned and the entry had not ended it (End records its cause before it
			// cancels, so the entry's own end would have been first): the connection's lease was cancelled, which is its
			// retirement.
			cause = session.CauseRetired
		}
		c.entry.End(cause)
		if hook := c.e.afterFunctionEnded; hook != nil {
			hook()
		}
		returned <- outcome
		if gaveUpOn {
			// The engine had given up on this function: it has returned, so it is no longer abandoned.
			c.e.tel.abandonedChanged(-1)
		}
	}()
	outcome.exit, outcome.err = c.target.target.Exec(ctx, proc)
}

// await watches the function until it has returned or the engine gave up on it. It reports what the function returned
// and whether the engine gave up.
func (c *command) await(lease context.Context, returned <-chan functionOutcome, state *atomic.Int32, cancelAuthor func()) (outcome functionOutcome, gaveUp bool) {
	flush := time.NewTicker(c.e.cfg.metricsEvery)
	defer flush.Stop()
	leaseDone := lease.Done()
	for {
		select {
		case out := <-returned:
			return out, false
		case <-c.entry.Ended():
			return c.drain(returned, state, cancelAuthor)
		case <-leaseDone:
			c.entry.End(session.CauseRetired)
			leaseDone = nil
		case <-flush.C:
			c.flushTraffic()
		}
	}
}

// drain is the wait for a function whose session the SDK has ended: it cancels the function's context, releases a function
// that a full queue holds in Write, and waits for it to return. A graceful end waits for the grace; a kill does not. A
// function that has not returned by then is abandoned: the command ends without it.
func (c *command) drain(returned <-chan functionOutcome, state *atomic.Int32, cancelAuthor func()) (functionOutcome, bool) {
	c.out.beginEnding()
	if c.in != nil {
		// The session has ended: the host can send no more, and a goroutine of the function blocked reading Stdin returns
		// with io.EOF, as the contract says. Output stays open for the function to flush until it returns.
		c.in.Close()
	}
	cancelAuthor()
	var graceOver <-chan time.Time
	if !c.killed() {
		timer := time.NewTimer(c.e.cfg.closeGrace)
		defer timer.Stop()
		graceOver = timer.C
	}
	select {
	case out := <-returned:
		return out, false
	case <-c.killC:
	case <-graceOver:
		c.violate(violation{violationIgnoredCancel, "the function did not return within the grace after its context was cancelled: the command ends without it"})
	}
	abandon := state.CompareAndSwap(fnRunning, fnAbandoned)
	if hook := c.e.afterAbandonDecision; hook != nil {
		hook(abandon)
	}
	if abandon {
		c.e.tel.abandonedChanged(1)
		return functionOutcome{}, true
	}
	return <-returned, false
}

// killed reports whether the command was ended with a kill.
func (c *command) killed() bool {
	select {
	case <-c.killC:
		return true
	default:
		return false
	}
}

// closeStdio closes the pipes between the function and the host: further output is refused, the function's input ends
// (a goroutine of the function that reads Stdin returns), and Proc.Resize closes. It may be called again.
func (c *command) closeStdio() {
	c.out.CloseWrite()
	if c.in != nil {
		c.in.Close()
	}
	c.rz.stop()
}

// conclude decides the end of the command and publishes it to the entry: the supervisor's last act. The result is read by
// finish, which the registry calls once every goroutine of the command has returned.
func (c *command) conclude(outcome functionOutcome, gaveUp bool) {
	cause := c.entry.Cause()
	stats := c.out.Stats()
	decision := decideEnd(endInputs{
		outcome:  outcome,
		cause:    cause,
		caps:     c.gate.effective(),
		started:  c.startObserved(),
		link:     c.linkState(),
		classify: c.target.target.Classify,
	})
	for _, v := range decision.violations {
		c.violate(v)
	}
	if stats.Late > 0 {
		c.violate(violation{violationLateWrite, fmt.Sprintf("%d writes (%d bytes) came after the function returned or the session ended and were refused", stats.Late, stats.LateB)})
	}
	result := commandResult{
		End:           decision.end,
		Cause:         cause,
		StartObserved: c.startObserved() || decision.processStatus,
		Duration:      time.Since(c.created),
		BytesOut:      stats.Written,
		DroppedBytes:  stats.Dropped,
		LateWrites:    stats.Late,
		LateBytes:     stats.LateB,
		Output:        outputCompleteness(decision, stats.Dropped, stats.Late, gaveUp),
		Abandoned:     gaveUp,
	}
	if c.in != nil {
		result.BytesIn = c.in.Accepted()
	}
	c.mu.Lock()
	for _, v := range c.violations {
		result.Violations = append(result.Violations, v.String())
	}
	c.mu.Unlock()
	c.result = result
	c.flushTraffic()
	if outcome.panicked && cause == session.CauseFinished {
		// The panic value and the stack are the plugin's own business: they go to its log and nowhere else.
		c.log.Errorw(context.Background(), "an exec function panicked", "panic", fmt.Sprint(outcome.panicValue), "stack", string(outcome.stack))
	}
	if decision.failure != nil && decision.failure.Err != nil {
		// The raw cause of a failure may carry what the target put in its error: it goes to the plugin's own log at Error and
		// to no other level, so that an operator finds why the command failed and nothing else carries it.
		c.log.Errorw(context.Background(), "an exec command failed", "class", decision.failure.Class.String(), "cause", decision.failure.Err.Error())
	}
}

// onEnd is the one place a command's end is emitted: the registry calls it once, after the command's last goroutine
// returned, so the result the supervisor concluded is final. A command that was never handed to its function has nothing
// to emit.
func (e *engine) onEnd(entry *session.Entry[*command]) {
	c := entry.Value()
	if !c.handedOver() {
		close(c.doneC)
		return
	}
	c.endObservation()
	close(c.doneC)
}

// ---------------------------------------------------------------------------------------------------------------
// options, the mode of the session
// ---------------------------------------------------------------------------------------------------------------

// resolveOptions asks the target's options for the resource (inside the lease, as the Options RPC does), checks the
// request's selection against them, and returns the capabilities the session is opened with. A selection that names
// something the options do not offer, or omits a required option that has no default, is a refusal: Request.Selection
// holds exactly the choices the options declared.
func (c *command) resolveOptions(bound *types.PluginContext) (Capabilities, error) {
	info := &c.target.info
	var options []Option
	if fn := c.target.target.Options; fn != nil {
		var err error
		options, err = callOptions(bound.Context, c.e.log, fn, bound, c.request.Ref)
		if err != nil {
			failure, classifyPanic := classifyErrorSafely(c.target.target.Classify, err)
			if classifyPanic != nil {
				c.violate(violation{violationClassifyPanic, "the target's Classify panicked on an error of Options: the error is classified as INTERNAL"})
			}
			return Capabilities{}, failure
		}
		if err := validateOptions(options); err != nil {
			c.violate(violation{violationOptionsContract, "the options callback broke its contract: " + err.Error()})
			return Capabilities{}, &Error{Class: ClassInternal, Err: err}
		}
		var widened []choiceViolation
		options, widened = clampChoiceCapabilities(info.Capabilities, options)
		for _, v := range widened {
			c.violate(violation{violationWidened, fmt.Sprintf("option %q choice %q: %s", v.option, v.choice, v.text)})
		}
	}
	if refusal := checkSelection(options, c.request.Selection); refusal != nil {
		return Capabilities{}, refusal
	}
	return openedCapabilities(info.Capabilities, options, c.request.Selection), nil
}

// callOptions runs the author's callback; a panic in it is the author's bug and must not take the plugin down.
func callOptions(ctx context.Context, log logging.Logger, fn OptionsFunc, pc *types.PluginContext, ref resourcev2.ResourceReference) (options []Option, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Errorw(ctx, "exec options callback panicked", "panic", fmt.Sprint(recovered))
			options, err = nil, &Error{Class: ClassInternal, Err: fmt.Errorf("options callback panicked: %v", recovered)}
		}
	}()
	return fn(pc, ref)
}

// checkSelection holds a selection to the options of the resource: every name is an option, every value a choice of it,
// and every required option is selected or has a default choice.
func checkSelection(options []Option, selection map[string]string) *Error {
	for name, value := range selection {
		i := slices.IndexFunc(options, func(o Option) bool { return o.Name == name })
		if i < 0 {
			return &Error{Class: ClassInvalidRequest, Facts: ErrorFacts{SelectionName: name}, Err: fmt.Errorf("the resource offers no option %q", name)}
		}
		if !slices.ContainsFunc(options[i].Choices, func(c Choice) bool { return c.Value == value }) {
			return &Error{Class: ClassInvalidRequest, Facts: ErrorFacts{SelectionName: name, SelectionValue: value}, Err: fmt.Errorf("option %q has no such choice", name)}
		}
	}
	for i := range options {
		o := &options[i]
		if _, chosen := selection[o.Name]; chosen || !o.Required {
			continue
		}
		if !slices.ContainsFunc(o.Choices, func(c Choice) bool { return c.IsDefault }) {
			return &Error{Class: ClassInvalidRequest, Facts: ErrorFacts{SelectionName: o.Name}, Err: fmt.Errorf("option %q is required and has no default", o.Name)}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------------------------------------------
// what the function reports: Started, Link, Resize
// ---------------------------------------------------------------------------------------------------------------

// callsRefused is true once the function has returned or the session has ended: Proc.Started and Proc.Link no longer change
// anything, and the call is the author's bug.
func (c *command) callsRefused() bool {
	return c.fnReturned.Load() || c.entry.Cause() != session.CauseNone
}

// reportStarted is Proc.Started. The start gate takes a report that comes before the first output, once; the facts are
// bounded before the host sees them.
func (c *command) reportStarted(facts SessionFacts) {
	if c.callsRefused() {
		c.violate(violation{violationLateCall, "Proc.Started was called after the function returned or the session ended: ignored"})
		return
	}
	for _, text := range c.gate.report(facts, c.recordStarted) {
		kind := violationStartedLate
		if text != startedTwice && text != startedAfterOutput {
			kind = violationWidened
		}
		c.violate(violation{kind, text})
	}
	if c.gate.effective().Stall == StallLossy {
		// The function narrowed the session to a lossy stall: the pace of the reader no longer reaches it either.
		c.out.makeLossy()
	}
}

// recordStarted keeps what the host receives in Started. The gate calls it with its lock held, before the output that
// follows it can be queued.
func (c *command) recordStarted(facts SessionFacts) {
	bounded, dropped := boundSessionFacts(facts, c.target.info.Session.MaxDimension)
	for _, text := range dropped {
		c.violate(violation{violationStartedFacts, text})
	}
	c.mu.Lock()
	c.started = &bounded
	c.mu.Unlock()
	close(c.startedC)
}

// startObserved is true once the host has a Started: the function reported, or output arrived and the SDK made one.
func (c *command) startObserved() bool {
	select {
	case <-c.startedC:
		return true
	default:
		return false
	}
}

// reportLink is Proc.Link. Only a target that declared LinkHealth reports it; the SDK keeps the last state and tells the
// consumer on a change.
func (c *command) reportLink(state LinkState) {
	if c.callsRefused() {
		c.violate(violation{violationLateCall, "Proc.Link was called after the function returned or the session ended: ignored"})
		return
	}
	if !c.gate.effective().LinkHealth {
		c.violate(violation{violationLinkUndeclared, "Proc.Link on a target that did not declare LinkHealth: ignored"})
		return
	}
	if state != LinkGood && state != LinkDegraded && state != LinkStalled {
		c.violate(violation{violationLinkInvalid, fmt.Sprintf("Proc.Link with %d, which is not a state of the link: ignored", int(state))})
		return
	}
	c.mu.Lock()
	changed := !c.linkSet || c.link != state
	c.link, c.linkSet = state, true
	c.mu.Unlock()
	if changed {
		select {
		case c.linkC <- struct{}{}:
		default:
		}
	}
}

// linkState returns the last state the function reported, LinkUnspecified when there was none.
func (c *command) linkState() LinkState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.link
}

// resized is what the resizer tells the command when the function took a size the host sent: the consumer reads it with
// AppliedResize, and ResizeApplied wakes it.
func (c *command) resized(seq uint32, size Size) {
	c.mu.Lock()
	c.applied, c.appliedSet = resizeRequest{seq: seq, size: size}, true
	c.mu.Unlock()
	select {
	case c.appliedC <- struct{}{}:
	default:
	}
}

// resizeUnread is what the resizer tells the command when a size has waited for a function that does not read Proc.Resize.
// It is a violation only for a target that declared Resize: a target that cannot resize has nothing to read the sizes for.
func (c *command) resizeUnread() {
	if !c.gate.effective().Resize {
		return
	}
	c.violate(violation{violationResizeUnread, "the function declared Resize and did not read Proc.Resize for a size it was sent: sizes wait and the newest replaces them"})
}

// commandWriter is the function's Stdout and Stderr: the first byte of output goes through the start gate, and bytes after
// the end are refused.
type commandWriter struct {
	c      *command
	stream execv2.Stream
}

// Write queues p as output. It fails with io.ErrClosedPipe once the function has returned or the session has ended, and
// says so in the log once: a write after the end is a function that returned before its command did, or a goroutine that
// outlived it.
func (w commandWriter) Write(p []byte) (int, error) {
	c := w.c
	if len(p) > 0 && !c.outputSeen.Load() && !c.out.isClosed() {
		c.gate.output(c.recordStarted)
		c.outputSeen.Store(true)
	}
	n, err := c.out.Write(w.stream, p)
	if errors.Is(err, io.ErrClosedPipe) {
		c.violate(violation{violationLateWrite, "output was written after the function returned or the session ended: refused"})
	}
	return n, err
}

// ---------------------------------------------------------------------------------------------------------------
// violations, telemetry
// ---------------------------------------------------------------------------------------------------------------

// violate records a way the function broke the contract. A kind is logged and counted once for a command: the log says
// what to fix, the count says how many commands broke the rule, and a function in a loop cannot flood either.
func (c *command) violate(v violation) {
	c.mu.Lock()
	first := !c.violationSet[v.kind]
	if first {
		c.violationSet[v.kind] = true
		c.violations = append(c.violations, v)
	}
	c.mu.Unlock()
	if !first {
		return
	}
	c.e.tel.violation(v.kind)
	level := c.log.Warnw
	if v.kind == violationPanic || v.kind == violationIgnoredCancel {
		level = c.log.Errorw
	}
	level(context.Background(), "an exec function broke the contract", "violation", v.kind, "detail", v.detail)
}

// beginObservation starts the command's span and counts it. The span lasts as long as the command: its outcome, exit,
// duration and counts are set when it ends.
func (c *command) beginObservation(ctx context.Context) {
	c.e.tel.sessionStarted()
	_, c.span = obs.Tracer().Start(ctx, "sdk.exec."+c.kind.String(), trace.WithAttributes(
		attribute.String("session_id", c.id),
		attribute.String("resource_type", c.target.info.Type),
		attribute.String("connection_id", c.route.Connection),
		attribute.Bool("tty", c.request.TTY),
	))
	c.log.Debugw(context.Background(), "exec command started")
}

// endObservation closes the span, counts the end and logs it. It never records an error text or an argument: the outcome
// is the kind of the end, the counts are numbers.
func (c *command) endObservation() {
	r := c.result
	c.e.tel.sessionEnded(r.End)
	var failure error
	attrs := []attribute.KeyValue{
		attribute.String("exec.end", endKind(r.End)),
		attribute.String("exec.cause", r.Cause.String()),
		attribute.Int64("exec.duration_ms", r.Duration.Milliseconds()),
		attribute.Int64("exec.bytes_out", int64(r.BytesOut)),         //nolint:gosec // a count of bytes cannot reach the sign bit
		attribute.Int64("exec.bytes_in", int64(r.BytesIn)),           //nolint:gosec // idem
		attribute.Int64("exec.dropped_bytes", int64(r.DroppedBytes)), //nolint:gosec // idem
		attribute.String("exec.output", word(r.Output.String(), "OUTPUT_COMPLETENESS_")),
		attribute.Int("exec.violations", len(r.Violations)),
	}
	switch kind := r.End.GetKind().(type) {
	case *execv2.End_Exited:
		attrs = append(attrs,
			attribute.Int64("exec.exit_code", kind.Exited.GetExitCode()),
			attribute.String("exec.exit_source", word(kind.Exited.GetSource().String(), "EXIT_SOURCE_")))
	case *execv2.End_Signaled:
		attrs = append(attrs, attribute.String("exec.exit_source", word(kind.Signaled.GetSource().String(), "EXIT_SOURCE_")))
	case *execv2.End_Failed:
		failure = errEndFailed
		attrs = append(attrs, attribute.String("exec.error_class", word(kind.Failed.GetClass().String(), "ERROR_CLASS_")))
	case *execv2.End_Lost:
		failure = errEndLost
	}
	c.span.SetAttributes(attrs...)
	obs.End(c.span, failure, endKind(r.End))
	c.log.Debugw(context.Background(), "exec command ended",
		"end", endKind(r.End), "cause", r.Cause.String(), "duration_ms", r.Duration.Milliseconds(),
		"bytes_out", r.BytesOut, "bytes_in", r.BytesIn, "dropped_bytes", r.DroppedBytes, "violations", len(r.Violations))
}

// endKind names the arm of an End.
func endKind(end *execv2.End) string {
	switch end.GetKind().(type) {
	case *execv2.End_Exited:
		return "exited"
	case *execv2.End_Signaled:
		return "signaled"
	case *execv2.End_Closed:
		return "closed"
	case *execv2.End_Failed:
		return "failed"
	case *execv2.End_Lost:
		return "lost"
	}
	return "unknown"
}

// flushTraffic reports what the command moved since the last time to the diagnostics. Only the supervisor calls it.
func (c *command) flushTraffic() {
	stats := c.out.Stats()
	var in uint64
	if c.in != nil {
		in = c.in.Accepted()
	}
	reason := execv2.DropReason_DROP_REASON_REPLAY_FULL
	if c.gate != nil && c.gate.effective().Stall == StallLossy {
		reason = execv2.DropReason_DROP_REASON_EAGER_DRAIN_OVERFLOW
	}
	c.e.tel.traffic(stats.Written-c.flushed.out, in-c.flushed.in, stats.Dropped-c.flushed.dropped, stats.Stalls-c.flushed.stalls, reason)
	c.flushed.out, c.flushed.in, c.flushed.dropped, c.flushed.stalls = stats.Written, in, stats.Dropped, stats.Stalls
}

// ---------------------------------------------------------------------------------------------------------------
// what the consumer of a command calls
// ---------------------------------------------------------------------------------------------------------------

// ID returns the id the command was started with.
func (c *command) ID() string { return c.id }

// Request returns the validated request the function received. The result is the caller's own copy.
func (c *command) Request() Request { return cloneRequest(c.request) }

// Opened returns the capabilities the command was opened with: what a host reads in Opened. The result is the caller's
// own copy.
func (c *command) Opened() Capabilities {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneCapabilities(c.opened)
}

// Effective returns the capabilities of the command now: what it was opened with, narrowed by what the function reported
// in Started.
func (c *command) Effective() Capabilities { return c.gate.effective() }

// Read takes the next output of the command: the ranges its queue lost since the last Read and up to len(buf) bytes of one
// stream. It returns io.EOF once the command has ended and its output was read, and ctx's error when ctx ends first.
func (c *command) Read(ctx context.Context, buf []byte) (outputChunk, error) {
	return c.out.Read(ctx, buf)
}

// WriteInput sends bytes to the function's Stdin, waiting while its queue is full: that wait is the backpressure the host
// sees. It fails with io.ErrClosedPipe once the host closed the input, the command ended, or the command has no input.
func (c *command) WriteInput(ctx context.Context, b []byte) (int, error) {
	if c.in == nil {
		return 0, io.ErrClosedPipe
	}
	return c.in.Write(ctx, b)
}

// CloseInput is the host's stdin EOF: the function's Stdin reports io.EOF once it has read what was sent.
func (c *command) CloseInput() {
	if c.in != nil {
		c.in.CloseWrite()
	}
}

// resizeDisposition is what became of a resize the host sent.
type resizeDisposition int

const (
	// resizeAccepted: the size is the one the function will take next (a newer one replaces it until it does).
	resizeAccepted resizeDisposition = iota
	// resizeIgnored: the command has no terminal, or its target cannot resize one.
	resizeIgnored
	// resizeInvalid: a dimension is zero or above the target's limit.
	resizeInvalid
	// resizeClosed: the command has ended.
	resizeClosed
)

// Resize sends a new terminal size to the function. Latest wins and it never blocks; a command whose target cannot resize
// ignores it, and the host is told so.
func (c *command) Resize(seq uint32, size Size) resizeDisposition {
	limit := c.target.info.Session.MaxDimension
	switch {
	case size.Cols == 0 || size.Rows == 0 || (limit > 0 && (int(size.Cols) > limit || int(size.Rows) > limit)):
		return resizeInvalid
	case !c.request.TTY || !c.gate.effective().Resize:
		return resizeIgnored
	case !c.rz.submit(seq, size):
		return resizeClosed
	}
	return resizeAccepted
}

// Started returns what the host receives in Started: the facts the function reported, or the ones the SDK made when output
// came first. It reports false until the first of those. Started is recorded before the output that follows it can be read.
func (c *command) Started() (SessionFacts, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started == nil {
		return SessionFacts{}, false
	}
	return cloneSessionFacts(*c.started), true
}

// StartedC is closed when Started has something to return.
func (c *command) StartedC() <-chan struct{} { return c.startedC }

// Link returns the last state of the link the function reported, if it reported one.
func (c *command) Link() (LinkState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.link, c.linkSet
}

// LinkChanged receives a value after the link state changed; a change while a value is waiting adds nothing.
func (c *command) LinkChanged() <-chan struct{} { return c.linkC }

// AppliedResize returns the last size the function took that a host sent, with the host's seq: what Resized acknowledges.
func (c *command) AppliedResize() (seq uint32, size Size, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.applied.seq, c.applied.size, c.appliedSet
}

// ResizeApplied receives a value after the function took a size a host sent; a size taken while a value is waiting adds
// nothing.
func (c *command) ResizeApplied() <-chan struct{} { return c.appliedC }

// Close ends the command on behalf of its owner with the given cause, which must be one the SDK ends commands with
// (CauseRequested for a close, CauseTimeout, CauseExpired). Graceful: the function's context is cancelled and it is
// given the grace to return. With kill the end does not wait for the function. The first end of a command stands: a
// later Close changes the cause of nothing, though a kill still stops a wait that is under way.
func (c *command) Close(cause session.Cause, kill bool) {
	if kill {
		c.killOnce.Do(func() { close(c.killC) })
	}
	c.entry.End(cause)
}

// Done is closed when the command has ended and its result is final: the one moment the End is emitted. The output may
// still be waiting to be read.
func (c *command) Done() <-chan struct{} { return c.doneC }

// Result waits for the end of the command and returns it.
func (c *command) Result() commandResult {
	<-c.doneC
	return c.result
}

// Violations returns the ways the function has broken the contract so far.
func (c *command) Violations() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.violations))
	for i, v := range c.violations {
		out[i] = v.String()
	}
	return out
}

// errEndFailed and errEndLost mark a span as failed for an end that is a failure of the command or of the link; the span
// records the kind of the end and never a text.
var (
	errEndFailed = errors.New("exec command failed")
	errEndLost   = errors.New("exec command lost")
)

// boundSessionFacts returns facts that the wire can carry, and says what it had to drop. A fact that breaks its bound is
// dropped, never cut: an argv that is too long or has a NUL, a name or a path that is not plain text, a size with one
// dimension zero or above the target's limit, a TERM over 64 bytes, a transport that is not a closed code. What the
// function reported about the capabilities was held to the opened set by the start gate already.
func boundSessionFacts(f SessionFacts, maxDimension int) (SessionFacts, []string) {
	var dropped []string
	if len(f.Program.Argv) > 0 && !validProgramArgv(f.Program.Argv) {
		dropped = append(dropped, "Started: Program.Argv is over the bounds of an argv or holds a NUL or invalid UTF-8: dropped")
		f.Program.Argv = nil
	}
	if f.Program.Name != "" && boundedFact(f.Program.Name) == "" {
		dropped = append(dropped, "Started: Program.Name is not plain text of at most 256 bytes: dropped")
		f.Program.Name = ""
	}
	if f.Program.Path != "" && (len(f.Program.Path) > maxCwdBytes || !validText(f.Program.Path)) {
		dropped = append(dropped, "Started: Program.Path is not plain text of at most 4096 bytes: dropped")
		f.Program.Path = ""
	}
	if !f.Size.IsZero() && (f.Size.Cols == 0 || f.Size.Rows == 0 || (maxDimension > 0 && (int(f.Size.Cols) > maxDimension || int(f.Size.Rows) > maxDimension))) {
		dropped = append(dropped, "Started: Size has a zero dimension or is above the target's limit: dropped")
		f.Size = Size{}
	}
	if f.Term != "" && (len(f.Term) > maxTermBytes || !validText(f.Term)) {
		dropped = append(dropped, "Started: Term is not plain text of at most 64 bytes: dropped")
		f.Term = ""
	}
	if f.Transport != "" && !closedCode(f.Transport) {
		dropped = append(dropped, "Started: Transport is not a closed code ([a-z0-9_.-], 1 to 40 bytes): dropped")
		f.Transport = ""
	}
	return f, dropped
}

// validProgramArgv reports whether an argv may be reported back to the host: the bounds of a request's argv.
func validProgramArgv(argv []string) bool {
	if len(argv) > maxArgvElements {
		return false
	}
	total := 0
	for _, a := range argv {
		if len(a) > maxArgvElementBytes || strings.ContainsRune(a, 0) || !utf8.ValidString(a) {
			return false
		}
		total += len(a)
	}
	return total <= defaultArgvBytes
}

// configure applies the settings of an in-process engine (engine_hook.go): a test's logger, diagnostics registry and
// timings. It is called once, before the first start.
func (e *engine) configure(cfg exechook.EngineConfig) {
	if cfg.Logger != nil {
		e.setLogger(cfg.Logger)
	}
	if cfg.Diagnostics != nil {
		e.tel = newTelemetry(cfg.Diagnostics)
	}
	if cfg.CloseGrace > 0 {
		e.cfg.closeGrace = cfg.CloseGrace
	}
	if cfg.ResizeUnreadAfter > 0 {
		e.cfg.resizeUnreadAfter = cfg.ResizeUnreadAfter
	}
	if cfg.MetricsEvery > 0 {
		e.cfg.metricsEvery = cfg.MetricsEvery
	}
}

// newRunID returns an id for a run whose request gave none.
func newRunID() string { return uuid.NewString() }
