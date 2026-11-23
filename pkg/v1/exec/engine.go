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
// counted, logged once), ignores its context (abandoned after the grace, after the kill wait for a kill, or after the
// shutdown grace for a plugin that is stopping: counted in the abandoned gauge, logged at error, and its place held until it
// returns), panics (a Failed end of ErrorClassInternal, never the plugin), never reads Proc.Resize (nothing blocks; a warning
// after a wait), returns an exit 0 for a connection it lost (read as the transport's word where the SDK has evidence), or
// reports its start twice or too late (refused). Each is either loud (a log line, a diagnostics count, a violation on the
// result) or contained, and most are both.

const (
	// defaultCloseGrace is how long a graceful end waits for the function to return after its context was cancelled before
	// the engine gives up on it. A kill waits defaultKillWait, and a plugin that is stopping defaultShutdownGrace.
	defaultCloseGrace = 5 * time.Second
	// defaultResizeUnreadAfter is how long a size may wait for a function that declared Resize and does not read it before
	// the engine says so in the log.
	defaultResizeUnreadAfter = 5 * time.Second
	// defaultMetricsEvery is how often a running session reports its traffic to the diagnostics.
	defaultMetricsEvery = 10 * time.Second
	// defaultOptionsDeadline is how long an author's Options callback may take during a start before the engine cancels its
	// context; the callback then has the grace to return before the engine gives up on it. It matches the call timeout the
	// host applies to the Options RPC.
	defaultOptionsDeadline = 30 * time.Second
	// defaultClassifyDeadline is how long an author's Classify function may take before the engine gives up on it: it maps
	// an error to a class and has no context to honor, so it is given no grace.
	defaultClassifyDeadline = 5 * time.Second
	// defaultKillWait is how long a kill waits for the function to return before the engine gives up on it, counted from the
	// kill. A function that honors its cancelled context returns within it: it is then not abandoned and holds no place when
	// the command's end is delivered. One that does not is given up on at the end of it, never at the end of the grace.
	defaultKillWait = 50 * time.Millisecond
	// defaultShutdownGrace is how long each function has to return after its context was cancelled when the engine closes
	// (the plugin is stopping), instead of the close grace: the host stops a plugin two seconds after it asks, so a function
	// that ignores its context must not be waited for longer than the plugin has.
	defaultShutdownGrace = time.Second
	// defaultOptionsInFlight is how many Options callbacks of one target the unary Options RPC may have in flight, the ones the
	// engine gave up on and that have not returned included. A call that would be one more is refused UNAVAILABLE: a unary
	// call has no session to hold a place with, so this is what bounds the callbacks that never return.
	defaultOptionsInFlight = 32
	// defaultQueryDeadline is how long a host's query for a fact may wait for the function's answer before the SDK answers it
	// unknown (QUERY_FAILED): a query is a question a person is waiting on, and a function that never answers must not leave the
	// host waiting for ever.
	defaultQueryDeadline = 5 * time.Second
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
	// optionsDeadline and classifyDeadline bound the author's Options callback and Classify function.
	optionsDeadline  time.Duration
	classifyDeadline time.Duration
	// killWait and shutdownGrace bound the wait for a function that was told to stop by a kill and by the engine's close, and
	// optionsInFlight how many Options callbacks of one target the unary Options RPC may have in flight. Zero is the default.
	killWait        time.Duration
	shutdownGrace   time.Duration
	optionsInFlight int
	// queryDeadline is how long a query for a fact waits for the function's answer; zero is the default.
	queryDeadline time.Duration
	// optionsCacheTTL is how long the admission of a start reuses the result of an Options callback for the same request (the
	// provider and type, the resource with its incarnation, the connection and the route); zero or less turns the cache off,
	// and the server sets the default. optionsCacheMax is the most results it keeps; zero is the default.
	optionsCacheTTL time.Duration
	optionsCacheMax int
	// tombstoneTTL is how long an ended session stays listed and readable and keeps its id, maxTombstones how many ended sessions
	// the engine remembers, and tombstoneBytes how many bytes of their output it keeps in all. Zero is the default. They are
	// fields so that a test can shorten them.
	tombstoneTTL   time.Duration
	maxTombstones  int
	tombstoneBytes int
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
	// by the decision that the function's own return also makes (under one lock: authorCall), whether it gave up on the
	// function.
	afterAbandonDecision func(abandoned bool)

	// afterCallReturned is a test seam, nil in production: it runs on the goroutine of a bounded call of author code (an Options
	// callback, a Classify) right after the call returned and was marked as returned, and before the engine hears of it: the
	// moment at which a wait that runs out is decided against a call that has already returned.
	afterCallReturned func()

	// logs decides when the lines that a host could provoke without end are written (a refusal for abandoned places, an
	// abandoned function, a hung Options callback): the first one, and then at most one a second for each target.
	logs logLimit

	// closingC is closed when the engine begins to close: the functions that are waited for then have the shutdown grace.
	closingC  chan struct{}
	closeOnce sync.Once

	// flightMu guards flights, what each target has of the unary Options RPC in flight (abandon.go).
	flightMu sync.Mutex
	flights  map[*registeredTarget]*optionsFlight

	// optionsCache holds the results of the Options callbacks that the admission of a start may reuse (options_cache.go).
	optionsCache optionsCache

	// now and newTimer are test seams, nil in production. now is the clock of the log limits, and newTimer makes the timers of
	// the waits for author code (the grace, the kill wait, the shutdown grace), so that a test decides when each runs out.
	now      func() time.Time
	newTimer func(d time.Duration) (c <-chan time.Time, stop func())

	// afterEndEmitted is a test seam, nil in production: it runs on the registry's finisher goroutine right after a command's
	// Done is closed and before its id is released, which is the window between Done and Gone that a test holds open.
	afterEndEmitted func()

	// newLeaseTimer is a test seam, nil in production: it makes the timer of the lease of a detached session, so that a test
	// decides when one runs out. The timers of the waits for author code are newTimer's, and a lease is not one of them.
	newLeaseTimer func(d time.Duration) (c <-chan time.Time, stop func())

	// betweenStoreReads is a test seam, nil in production: it runs in a call that reads two of the engine's stores under two locks
	// (the registry's and the tombstones'), after the first was read and before the second is: in listSessions after the live
	// sessions, and in readOutput after the tombstone lookup. It is the window in which a session can end between the two.
	betweenStoreReads func()

	// beforeStreamLeaves is a test seam, nil in production: it runs on the goroutine of a served stream's handler when it has
	// noticed that its stream is over (the host half-closed it, cancelled it or lost its connection), and before it releases the
	// session: the window in which the stream is over and the engine has not yet been told, which a test holds open.
	beforeStreamLeaves func()

	// tombs holds the sessions that ended (tombstone.go).
	tombs *tombstones
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
	if cfg.optionsDeadline <= 0 {
		cfg.optionsDeadline = defaultOptionsDeadline
	}
	if cfg.classifyDeadline <= 0 {
		cfg.classifyDeadline = defaultClassifyDeadline
	}
	if cfg.killWait <= 0 {
		cfg.killWait = defaultKillWait
	}
	if cfg.shutdownGrace <= 0 {
		cfg.shutdownGrace = defaultShutdownGrace
	}
	if cfg.optionsInFlight <= 0 {
		cfg.optionsInFlight = defaultOptionsInFlight
	}
	if cfg.queryDeadline <= 0 {
		cfg.queryDeadline = defaultQueryDeadline
	}
	if cfg.optionsCacheMax <= 0 {
		cfg.optionsCacheMax = defaultOptionsCacheEntries
	}
	if cfg.tombstoneTTL <= 0 {
		cfg.tombstoneTTL = defaultTombstoneTTL
	}
	if cfg.maxTombstones <= 0 {
		cfg.maxTombstones = defaultMaxTombstones
	}
	if cfg.tombstoneBytes <= 0 {
		cfg.tombstoneBytes = defaultTombstoneBytes
	}
	e := &engine{cfg: cfg, log: cfg.log, tel: newTelemetry(cfg.diagnostics), closingC: make(chan struct{})}
	e.reg = session.New(session.Config[*command]{OnEnd: e.onEnd})
	e.tombs = newTombstones(cfg.tombstoneTTL, cfg.maxTombstones, cfg.tombstoneBytes, e.clock, e.realClock)
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

// close refuses every later start, ends every command (graceful: each function is cancelled and given the shutdown grace, not
// the close grace) and waits until each has ended and emitted its end, or until ctx ends. It may be called again and by many
// goroutines. A function that is already being waited for when it is called has at most the shutdown grace left from then.
func (e *engine) close(ctx context.Context) error {
	e.closeOnce.Do(func() { close(e.closingC) })
	err := e.reg.Close(ctx)
	e.tombs.close() // the sessions have ended and what they left stays readable until it expires; nothing is armed for it
	return err
}

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
	// DetachTimeout is the lease of a session whose stream left, when the host did not ask for another; zero is the target's
	// default.
	DetachTimeout time.Duration
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
// started), and an *Error of ErrorClassCancelled or ErrorClassTimeout when ctx ended first. Everything after the hand-over is the
// command's End.
func (e *engine) start(ctx context.Context, p startParams) (*command, error) {
	if p.ID == "" || p.Target == nil {
		return nil, &Error{Class: ErrorClassInternal, Err: errors.New("start without an id or a target")}
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
	if p.Kind == kindSession && !e.closing() && e.tombs.has(p.ID) {
		// The id of a session that ended is held by its tombstone, so that a late reader of that id never reads another session's
		// output as its own. This check is for the order of the refusals (the id before the limit, as for a live id); the
		// registry decides the same again under its own lock, below, and a session that ends between the two leaves its
		// tombstone before it releases its id.
		return nil, admissionRefusal(errIDTombstoned, tooMany)
	}
	entry, err := e.reg.Admit(parent, session.Spec{ID: p.ID, Group: group, Limit: limit}, func(context.Context) (*command, error) {
		if p.Kind == kindSession && e.tombs.has(p.ID) {
			return nil, errIDTombstoned
		}
		c = e.newCommand(p)
		return c, nil
	})
	if err != nil {
		if errors.Is(err, session.ErrHeld) {
			e.refusedForAbandoned(ctx, p, group)
		}
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
		return "run\x00" + key, info.Run.MaxConcurrent, ErrorReasonTooManyRuns
	}
	return "session\x00" + key, info.Session.MaxSessions, ErrorReasonTooManySessions
}

// admissionRefusal types an error of the registry's admission. A target whose places the abandoned calls hold is not full: it has
// fewer sessions than it declares, and the host must not read "too many sessions" into it. It is wedged: functions that were
// given up on and have not returned keep what they hold until they return, which they may never do, so it is UNAVAILABLE with
// the reason TARGET_WEDGED ("restart the plugin"), which a host tells from a shutdown (UNAVAILABLE, no reason) and from a
// full target (LIMIT).
func admissionRefusal(err error, tooMany ErrorReason) error {
	switch {
	case errors.Is(err, session.ErrClosed):
		return &Error{Class: ErrorClassUnavailable, Err: err}
	case errors.Is(err, session.ErrExists), errors.Is(err, errIDTombstoned):
		return NewError(ErrorClassConflict, ErrorReasonSessionExists, err)
	case errors.Is(err, session.ErrLimit):
		return NewError(ErrorClassLimit, tooMany, err)
	case errors.Is(err, session.ErrHeld):
		return NewError(ErrorClassUnavailable, ErrorReasonTargetWedged, err)
	}
	return &Error{Class: ErrorClassInternal, Err: err}
}

// refusedForAbandoned counts a start refused because the places of its target are held by author code that was given up on,
// and says so in the log: the first time, and at most once a second after that for each target, because a host that retries
// is a host that would otherwise write a line each.
func (e *engine) refusedForAbandoned(ctx context.Context, p startParams, group string) {
	e.tel.abandonedRefused()
	if !e.logs.allow("held\x00"+group, e.clock()) {
		return
	}
	e.log.Warnw(ctx, "exec start refused: the target's places are held by functions that were abandoned",
		"provider", p.Target.info.Provider, "type", p.Target.info.Type, "held", e.reg.Held(group), "kind", p.Kind.String())
}

// contextRefusal is the refusal of a start whose context ended: ErrorClassTimeout for a deadline, ErrorClassCancelled otherwise.
func contextRefusal(err error) *Error { return contextError(err) }

// refusalForCause is the refusal of a start that the SDK ended before the command was handed to its function. cause is
// the registry's; err is what the lease or the options said, when they said anything.
func refusalForCause(cause session.Cause, err error) error {
	switch cause {
	case session.CauseShutdown:
		return &Error{Class: ErrorClassUnavailable, Err: err}
	case session.CauseCancelled:
		return &Error{Class: ErrorClassCancelled, Err: err}
	case session.CauseRetired:
		return NewError(ErrorClassConnection, ErrorReasonConnectionRetired, err)
	case session.CauseNone, session.CauseFinished, session.CauseRequested, session.CauseExpired, session.CauseTimeout:
	}
	if err != nil {
		return err
	}
	return &Error{Class: ErrorClassCancelled}
}

// phase is how far a command got in its start.
type phase int

const (
	phaseAdmitting phase = iota // the lease and the options are being taken
	phaseRunning                // handed to the function
	phaseRefused                // never handed over: nothing ran
)

// command is one command: the author's function running with a Proc, its pipes and its end.
type command struct {
	e       *engine
	id      string
	kind    commandKind
	target  *registeredTarget
	route   resourcev2.AccessRoute
	request Request
	group   string // the group the command is counted in among its target's commands (admissionGroup)
	// replayBytes is the replay ring the host asked for; zero is the target's default. detachTimeout is the lease the host asked
	// for a detached session; zero is the target's default.
	replayBytes   int
	detachTimeout time.Duration
	created       time.Time
	log           logging.Logger

	// entry is set by start before any goroutine of the command exists, and read only by them and by what start hands out.
	entry    *session.Entry[*command]
	admitted chan error // the answer to start: nil once handed over, an error for a refusal

	in *inputPipe // nil: the command has no input
	// out is the command's output as the function writes it: the replay ring of a session, the bounded queue of a run. A session
	// has a ring, and creator is the attachment that reads it from the first byte; a run has a pipe.
	out     outputSide
	ring    *ring
	creator *attachment
	pipe    *outputPipe
	rz      *resizer
	ctl     procControl // the signals, the queries and the pushed facts of the function (proc_control.go)

	// opened is what Opened carries: the kind's capabilities narrowed by the mode the selection names. It and gate are set
	// once, at the hand-over, under mu, as are selection (the choice of every option the request resolved to, which the function
	// sees as Request.Selection) and noProgram (the choice runs no program: the facts carry none and the function has no argv).
	opened    Capabilities
	gate      *startGate
	selection map[string]string
	noProgram bool

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
	flushed struct{ out, in, dropped, droppedDetached, stalls uint64 }

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
	// Abandoned is true when the engine did not wait for the function to return: it ignored its cancelled context for the grace
	// or, after a kill, for the kill wait.
	Abandoned bool
	// Violations are the ways the function broke the contract, one for each kind, in the order they happened.
	Violations []string
}

// newCommand builds the command of an admitted start. It runs under the registry's lock: it allocates and nothing else.
// The output queue is made at the hand-over, when the capabilities that decide its policy are known.
func (e *engine) newCommand(p startParams) *command {
	group, _, _ := admissionGroup(p)
	c := &command{
		e: e, id: p.ID, kind: p.Kind, group: group, target: p.Target, route: p.Route, replayBytes: p.ReplayBytes, detachTimeout: p.DetachTimeout,
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
func (c *command) handOver(mode resolvedMode) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.phase != phaseAdmitting || c.entry.Cause() != session.CauseNone {
		return false
	}
	c.phase = phaseRunning
	c.opened, c.selection, c.noProgram = mode.opened, mode.selection, mode.noProgram
	c.gate = newStartGate(c.functionRequestLocked(), mode.opened)
	c.out = c.newOutput(mode.opened)
	return true
}

// runLeased runs inside the connection's lease for as long as the command runs. It returns when the command has ended
// (or was never handed over); the lease is released when it returns.
func (c *command) runLeased(bound *types.PluginContext) error {
	mode, refusal := c.resolveOptions(bound)
	if refusal != nil {
		return refusal
	}
	opened := mode.opened
	if !c.handOver(mode) {
		return nil
	}

	authorCtx, cancelAuthor := context.WithCancel(bound.Context)
	defer cancelAuthor()
	author := *bound
	author.Context = authorCtx
	proc := &Proc{
		Context:      &author,
		Request:      c.functionRequest(),
		Capabilities: cloneCapabilities(opened),
		Stdout:       commandWriter{c: c, stream: c.stdoutStream()},
		Resize:       c.rz.channel(),
		Started:      c.reportStarted,
		Link:         c.reportLink,
	}
	proc.Signals, proc.Queries, proc.Fact = c.ctl.init(c, opened)
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
	call := c.e.newAuthorCall(c.entry)
	returned := make(chan functionOutcome, 1)
	// The function's goroutine is the one goroutine of a command that the registry does not wait for beyond the grace:
	// see abandon.go.
	go c.callFunction(authorCtx, proc, call, returned)
	c.admitted <- nil

	outcome, gaveUp := c.await(bound.Context, returned, call, cancelAuthor)
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

// outputSide is what the engine asks of the output of a command, whichever structure holds it: the replay ring of a session or the
// bounded queue of a run.
type outputSide interface {
	Write(stream execv2.Stream, b []byte) (int, error)
	CloseWrite()
	isClosed() bool
	beginEnding()
	makeLossy()
	Stats() pipeStats
}

// newOutput makes the output of the command for the capabilities it was opened with. A session gets a replay ring of the size the
// host asked for (the target's default for a host that asked for none), with its first reader attached from the first byte; a
// target that stalls lossily gets the policy that never holds the function. A run gets a bounded queue.
func (c *command) newOutput(opened Capabilities) outputSide {
	if c.kind == kindRun {
		c.pipe = newOutputPipe(stdioQueueBytes, opened.Stall == StallLossy)
		return c.pipe
	}
	capacity := c.target.info.Session.ReplayBytes
	if c.replayBytes > 0 {
		capacity = c.replayBytes
	}
	c.ring = newRing(ringConfig{
		capacity: capacity, lossy: opened.Stall == StallLossy, unreadLimit: stdioQueueBytes,
		initialSize: c.initialSize(), now: c.e.clock,
	})
	c.ring.att.host = c
	c.creator = c.ring.newAttachment(0)
	c.ring.att.cur = c.creator
	return c.ring
}

// ending and end are what the ring asks of the session, under its lock: see sessionHost.
func (c *command) ending() bool { return c.entry.Cause() != session.CauseNone }

func (c *command) end(cause session.Cause, kill bool) { c.Close(cause, kill) }

// callFunction runs the author's function and reports how it ended. It decides who ended the session first: if the
// function's context was cancelled before it returned, the SDK ended it (whoever asked), and what the function returned
// is the aftermath; otherwise the function ended it. It closes the stdio before it reports, so that a goroutine the
// function left behind is refused at once.
func (c *command) callFunction(ctx context.Context, proc *Proc, call *authorCall, returned chan<- functionOutcome) {
	var outcome functionOutcome
	defer func() {
		if recovered := recover(); recovered != nil {
			outcome = panicOutcome(recovered)
		}
		c.fnReturned.Store(true)
		c.closeStdio()
		// Whether the engine gave up on this function is decided here, once and before the end is recorded: the engine's
		// grace and kill paths decide it under the same lock (authorCall), so a function that has returned is never taken for
		// an abandoned one, whatever order its return and the end reached the supervisor in.
		gaveUpOn, release := call.markReturned()
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
		// If the engine had given up on this function it has returned now: its place is free and it is no longer counted.
		call.settle(gaveUpOn, release)
	}()
	outcome.exit, outcome.err = c.target.target.Exec(ctx, proc)
}

// await watches the function until it has returned or the engine gave up on it. It reports what the function returned
// and whether the engine gave up.
func (c *command) await(lease context.Context, returned <-chan functionOutcome, call *authorCall, cancelAuthor func()) (outcome functionOutcome, gaveUp bool) {
	flush := time.NewTicker(c.e.cfg.metricsEvery)
	defer flush.Stop()
	leaseDone := lease.Done()
	for {
		select {
		case out := <-returned:
			return out, false
		case <-c.entry.Ended():
			return c.drain(returned, call, cancelAuthor)
		case <-leaseDone:
			c.entry.End(session.CauseRetired)
			leaseDone = nil
		case <-flush.C:
			c.flushTraffic()
		}
	}
}

// drain is the wait for a function whose session the SDK has ended: it cancels the function's context, releases a function
// that a full queue holds in Write, and waits for it to return. How long is decided by the bounds that apply (graceWait). A
// graceful end waits for the grace. A kill waits for the kill wait, counted from the kill, and never for the grace: a function
// that honors its cancelled context returns within it, is not abandoned, and holds no place when the command's end is
// delivered, so that a host that kills a session and starts another at once is not refused for the first. A kill that comes
// while the grace runs leaves the grace running and adds the kill wait, so the wait ends at the first of the two. An engine that
// is closing gives each function the shutdown grace instead of the grace. A function that has not returned when the first bound
// runs out is abandoned: the command ends without it, the engine says so in the log and records the violation, and the place
// of the command stays taken until the function returns.
func (c *command) drain(returned <-chan functionOutcome, call *authorCall, cancelAuthor func()) (functionOutcome, bool) {
	c.out.beginEnding()
	if c.in != nil {
		// The session has ended: the host can send no more, and a goroutine of the function blocked reading Stdin returns
		// with io.EOF, as the contract says. Output stays open for the function to flush until it returns.
		c.in.Close()
	}
	c.ctl.close() // the host can send no more signals or queries: a goroutine of the function that ranges over them returns
	cancelAuthor()
	wait := c.e.newGraceWait(c.e.cfg.closeGrace, c.killed())
	defer wait.stop()
	const graceDetail = "the function did not return within the grace after its context was cancelled: the command ends without it"
	const killDetail = "the function did not return within the kill wait after its context was cancelled: the command ends without it"
	killC, closingC := c.killC, c.e.closingC
	var word, detail string
	for word == "" {
		select {
		case out := <-returned:
			return out, false
		case <-killC:
			killC = nil
			wait.killed()
		case <-closingC:
			closingC = nil
			wait.closing()
		case <-wait.kill:
			word, detail = violationDidNotReturn, killDetail
		case <-wait.grace:
			word, detail = violationIgnoredCancel, graceDetail
		case <-wait.shutdown:
			word, detail = violationIgnoredCancel, graceDetail
		}
	}
	abandon := call.giveUp()
	if hook := c.e.afterAbandonDecision; hook != nil {
		hook(abandon)
	}
	if abandon {
		// The violation is recorded only when the engine gave up: a function that returned as the wait ran out is not blamed for it.
		c.violate(violation{word, detail})
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
// (a goroutine of the function that reads Stdin returns), and Proc.Resize, Proc.Signals and Proc.Queries close. It may be called
// again.
func (c *command) closeStdio() {
	c.out.CloseWrite()
	if c.in != nil {
		c.in.Close()
	}
	c.ctl.close()
	c.rz.stop()
}

// conclude decides the end of the command and publishes it to the entry: the supervisor's last act. The result is read by
// finish, which the registry calls once every goroutine of the command has returned.
func (c *command) conclude(outcome functionOutcome, gaveUp bool) {
	cause := c.entry.Cause()
	if cause == session.CauseRetired {
		// The resource lane has no hook for a retirement: this is where a command sees it. What the connection's requests cached is
		// stale for a connection that is started again under the same id.
		c.e.optionsCache.dropConnection(c.route.Connection)
	}
	stats := c.out.Stats()
	decision := decideEnd(endInputs{
		outcome:  outcome,
		cause:    cause,
		caps:     c.gate.effective(),
		started:  c.startObserved(),
		link:     c.linkState(),
		classify: c.guardedClassify(),
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
// to emit. The id is released when it returns: Done closes before that, and Gone after.
func (e *engine) onEnd(entry *session.Entry[*command]) {
	c := entry.Value()
	if !c.handedOver() {
		close(c.doneC)
		return
	}
	c.endObservation()
	if c.ring != nil {
		// The session's end is decided here: no stream attaches or detaches from now on, and the stream that is attached, if there
		// is one, is the one that is told Ended. The tombstone is made before Done is closed and the id released, so a host that
		// has read Ended finds it, and a start of the id cannot slip in between.
		c.sealSession()
	}
	close(c.doneC)
	if hook := e.afterEndEmitted; hook != nil {
		hook()
	}
}

// ---------------------------------------------------------------------------------------------------------------
// options, the mode of the session
// ---------------------------------------------------------------------------------------------------------------

// resolveOptions asks the target's options for the resource (inside the lease, as the Options RPC does), checks the
// request's selection against them, and returns the mode of the session: the capabilities it is opened with, the choice of every
// option the request resolved to and whether it runs a program (resolveMode). A selection that names something the options do not
// offer, or omits a required option that has no default, is a refusal. Request.Selection holds the choice of every option that the
// request resolved to, the default choice of an option it omitted included.
//
// The answer of the callback is reused for a short time (options_cache.go): a start right after the Options RPC for the same
// request, or after another start of it, does not run the callback again. The choices are advisory state, so a stale answer only
// moves a refusal later: the target refuses a container that stopped, and Classify types the refusal.
func (c *command) resolveOptions(bound *types.PluginContext) (resolvedMode, error) {
	info := &c.target.info
	var options []Option
	if fn := c.target.target.Options; fn != nil {
		key := optionsKeyOf(c.request.Ref, c.route)
		var cached bool
		if options, cached = c.e.cachedOptions(key); !cached {
			var err error
			if options, err = c.askOptions(bound, fn); err != nil {
				return resolvedMode{}, err
			}
			c.e.cacheOptions(key, options)
		}
		var widened []choiceViolation
		options, widened = clampChoiceCapabilities(info.Capabilities, options)
		for _, v := range widened {
			c.violate(violation{violationWidened, fmt.Sprintf("option %q choice %q: %s", v.option, v.choice, v.text)})
		}
	}
	return c.resolveMode(options)
}

// askOptions runs the target's Options callback for a start and returns what it answered, held to the contract of its bounds.
//
// The callback is author code that runs while the start waits, so it is bounded as the callback of the Options RPC is
// (boundedOptions): it has the engine's deadline (and its context says so), and when its context ends for any reason (the
// deadline, a caller that gave up, a shutdown, the connection's retirement) it has the grace to return, the shutdown grace when
// the plugin is stopping. A callback that does not is given up on: the start is refused, the lease is released, and the
// goroutine keeps the start's place until it returns (abandon.go).
func (c *command) askOptions(bound *types.PluginContext, fn OptionsFunc) ([]Option, error) {
	answer, ok := c.e.boundedOptions(c.e.newAuthorCall(c.entry), bound, func(callPC *types.PluginContext) optionsAnswer {
		options, err := callOptions(callPC.Context, c.e.log, fn, callPC, c.request.Ref)
		return optionsAnswer{options: options, err: err}
	})
	if !ok {
		c.violate(violation{violationOptionsHung, "the options callback did not return within its deadline and the grace after its context ended: the start is refused without it"})
		return nil, c.callbackRefusal(bound)
	}
	if err := answer.err; err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			// The callback was stopped by its context: the refusal says what stopped it, not what the callback made of it.
			return nil, c.callbackRefusal(bound)
		}
		failure, classifyPanic := classifyErrorSafely(c.guardedClassify(), err)
		if classifyPanic != nil {
			c.violate(violation{violationClassifyPanic, "the target's Classify panicked on an error of Options: the error is classified as INTERNAL"})
		}
		return nil, failure
	}
	if err := validateOptions(answer.options); err != nil {
		c.violate(violation{violationOptionsContract, "the options callback broke its contract: " + err.Error()})
		return nil, &Error{Class: ErrorClassInternal, Err: err}
	}
	return answer.options, nil
}

// optionsAnswer is what an Options callback returned.
type optionsAnswer struct {
	options []Option
	err     error
}

// boundedOptions runs an Options callback, which run calls with the context that the callback gets, as author code that cannot be
// stopped but by returning (callBounded): its context is bound's with the engine's deadline, and it ends when that does, when
// bound's does, or when the engine closes, and the callback has the grace to return after that. call is the account of the
// callback, which says what the callback holds when the engine gives up on it. It is the one bound that a start and the Options
// RPC share. It reports false when the callback was given up on.
func (e *engine) boundedOptions(call *authorCall, bound *types.PluginContext, run func(*types.PluginContext) optionsAnswer) (optionsAnswer, bool) {
	callCtx, cancel := context.WithTimeout(bound.Context, e.cfg.optionsDeadline)
	defer cancel()
	callPC := *bound
	callPC.Context = callCtx
	return callBounded(call, callCtx, e.cfg.closeGrace, func() optionsAnswer { return run(&callPC) })
}

// callbackRefusal is the refusal of a start whose Options callback was stopped by its context or given up on: when the SDK
// ended the start (a shutdown, a caller that gave up) the refusal says so; when the connection's lease was cancelled under
// the callback the connection was retired, which drops what its requests cached; otherwise it was the engine's own deadline.
func (c *command) callbackRefusal(bound *types.PluginContext) error {
	if cause := c.entry.Cause(); cause != session.CauseNone {
		if cause == session.CauseRetired {
			c.e.optionsCache.dropConnection(c.route.Connection)
		}
		return refusalForCause(cause, nil)
	}
	if bound.Context.Err() != nil {
		c.e.optionsCache.dropConnection(c.route.Connection)
		return NewError(ErrorClassConnection, ErrorReasonConnectionRetired, nil)
	}
	return &Error{Class: ErrorClassTimeout}
}

// guardedClassify is the target's Classify held to the engine's deadline, or nil when the target has none. Classify maps an
// error to a class and has no context to honor, so a call that does not return by the deadline is given up on at once: the
// error is then classified by the SDK's own rules, the author is told in the log and the counters, and the goroutine keeps
// the command's place until it returns. A panic of the author's function is raised again in the caller, which records it
// (classifyErrorSafely), because a goroutine that nobody waits for cannot be recovered from outside.
func (c *command) guardedClassify() ClassifyFunc {
	classify := c.target.target.Classify
	if classify == nil {
		return nil
	}
	return func(err error) (*Error, bool) {
		type classified struct {
			failure  *Error
			ok       bool
			panicked any
		}
		ctx, cancel := context.WithTimeout(context.Background(), c.e.cfg.classifyDeadline)
		defer cancel()
		got, ok := callBounded(c.e.newAuthorCall(c.entry), ctx, 0, func() (answer classified) {
			defer func() {
				if recovered := recover(); recovered != nil {
					answer = classified{panicked: recovered}
				}
			}()
			answer.failure, answer.ok = classify(err)
			return answer
		})
		switch {
		case !ok:
			c.violate(violation{violationClassifyHung, "the target's Classify did not return within its deadline: the error is classified by the SDK's own rules"})
			return nil, false
		case got.panicked != nil:
			panic(got.panicked)
		}
		return got.failure, got.ok
	}
}

// callOptions runs the author's callback; a panic in it is the author's bug and must not take the plugin down.
func callOptions(ctx context.Context, log logging.Logger, fn OptionsFunc, pc *types.PluginContext, ref resourcev2.ResourceReference) (options []Option, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Errorw(ctx, "exec options callback panicked", "panic", fmt.Sprint(recovered))
			options, err = nil, &Error{Class: ErrorClassInternal, Err: fmt.Errorf("options callback panicked: %v", recovered)}
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
			return &Error{Class: ErrorClassInvalidRequest, Facts: ErrorFacts{SelectionName: name}, Err: fmt.Errorf("the resource offers no option %q", name)}
		}
		if !slices.ContainsFunc(options[i].Choices, func(c Choice) bool { return c.Value == value }) {
			return &Error{Class: ErrorClassInvalidRequest, Facts: ErrorFacts{SelectionName: name, SelectionValue: value}, Err: fmt.Errorf("option %q has no such choice", name)}
		}
	}
	for i := range options {
		o := &options[i]
		if _, chosen := selection[o.Name]; chosen || !o.Required {
			continue
		}
		if !slices.ContainsFunc(o.Choices, func(c Choice) bool { return c.IsDefault }) {
			return &Error{Class: ErrorClassInvalidRequest, Facts: ErrorFacts{SelectionName: o.Name}, Err: fmt.Errorf("option %q is required and has no default", o.Name)}
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
	if p := bounded.Program; c.noProgram && (len(p.Argv) > 0 || p.Name != "" || p.Path != "") {
		// A session that joins a process that already runs has no program of its own, and the facts never carry one.
		c.violate(violation{violationStartedFacts, "Started: Program is reported for a choice that runs no program: dropped"})
		bounded.Program = Program{}
	}
	c.mu.Lock()
	c.started = &bounded
	c.mu.Unlock()
	if c.ring != nil {
		// The size the target says it applied is the size of the output from its first byte: Started comes before it.
		c.ring.noteSize(bounded.Size)
	}
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
	if c.ring != nil {
		c.ring.noteSize(size) // what the function writes from here on is laid out for it
	}
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
// what to fix, the count says how many commands broke the rule, and a function in a loop cannot flood either. The two kinds
// that say a function was given up on (ignored_cancel after the grace, did_not_return after a kill) are written at error, at the
// moment it is given up on, and for each target at most once a second: a host that kills and starts again as fast as it can,
// against a function that returns a moment after the engine gave up on it, would otherwise write a line for each command. Every
// one is still counted, and listed in the result of its command.
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
	switch v.kind {
	case violationPanic:
		level = c.log.Errorw
	case violationIgnoredCancel, violationDidNotReturn:
		if !c.e.logs.allow("abandoned\x00"+c.group, c.e.clock()) {
			return
		}
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
	// What left the ring while no stream was attached is counted under its own reason: nobody was there to read it.
	attached, flushedAttached := stats.Dropped-stats.DroppedDetached, c.flushed.dropped-c.flushed.droppedDetached
	c.e.tel.traffic(stats.Written-c.flushed.out, in-c.flushed.in, attached-flushedAttached, stats.DroppedDetached-c.flushed.droppedDetached, stats.Stalls-c.flushed.stalls, reason)
	c.flushed.out, c.flushed.in, c.flushed.dropped, c.flushed.droppedDetached, c.flushed.stalls = stats.Written, in, stats.Dropped, stats.DroppedDetached, stats.Stalls
}

// ---------------------------------------------------------------------------------------------------------------
// what the consumer of a command calls
// ---------------------------------------------------------------------------------------------------------------

// ID returns the id the command was started with.
func (c *command) ID() string { return c.id }

// Request returns the validated request the function received. The result is the caller's own copy.
func (c *command) Request() Request { return c.functionRequest() }

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
// stream. It returns io.EOF once the command has ended and its output was read, and ctx's error when ctx ends first. For a
// session it is the read of the attachment the session was created with, which is how a consumer in this process reads it; a
// stream of the wire reads its own attachment.
func (c *command) Read(ctx context.Context, buf []byte) (outputChunk, error) {
	if c.creator != nil {
		return c.creator.Read(ctx, buf)
	}
	return c.pipe.Read(ctx, buf)
}

// WriteInput sends bytes to the function's Stdin, waiting while its queue is full: that wait is the backpressure the host
// sees. It fails with io.ErrClosedPipe once the host closed the input, the command ended, or the command has no input.
func (c *command) WriteInput(ctx context.Context, b []byte) (int, error) {
	if c.in == nil {
		return 0, io.ErrClosedPipe
	}
	n, err := c.in.Write(ctx, b)
	if n > 0 && c.ring != nil {
		c.ring.lastActivity.Store(c.e.clock().UnixMilli())
	}
	return n, err
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
// ignores it, and the host is told so. owner is the stream of the host that sent the size, which is told when it is accepted and
// when the function takes it; the in-process consumer has none and reads AppliedResize.
func (c *command) Resize(seq uint32, size Size, owner resizeOwner) resizeDisposition {
	limit := c.target.info.Session.MaxDimension
	switch {
	case size.Cols == 0 || size.Rows == 0 || (limit > 0 && (int(size.Cols) > limit || int(size.Rows) > limit)):
		return resizeInvalid
	case !c.request.TTY || !c.gate.effective().Resize:
		return resizeIgnored
	case !c.rz.submit(seq, size, owner):
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
// still be waiting to be read. The command's id is still held at that moment, until its end has been emitted; Gone is
// closed once it is free.
func (c *command) Done() <-chan struct{} { return c.doneC }

// Gone is closed once the command has been removed from the engine: its id and its place among the target's sessions are
// free. It is closed after Done, and never before the end has been emitted, which is why a start of the same id right
// after Done may still be refused as a conflict, and one after Gone never is.
func (c *command) Gone() <-chan struct{} { return c.entry.Removed() }

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
	if cfg.OptionsDeadline > 0 {
		e.cfg.optionsDeadline = cfg.OptionsDeadline
	}
	if cfg.ClassifyDeadline > 0 {
		e.cfg.classifyDeadline = cfg.ClassifyDeadline
	}
	if cfg.KillWait > 0 {
		e.cfg.killWait = cfg.KillWait
	}
	if cfg.ShutdownGrace > 0 {
		e.cfg.shutdownGrace = cfg.ShutdownGrace
	}
	if cfg.OptionsInFlight > 0 {
		e.cfg.optionsInFlight = cfg.OptionsInFlight
	}
	if cfg.OptionsCacheTTL != 0 {
		e.cfg.optionsCacheTTL = cfg.OptionsCacheTTL
	}
	e.tombs.set(cfg.TombstoneTTL, cfg.MaxTombstones, cfg.TombstoneBytes)
}

// newRunID returns an id for a run whose request gave none.
func newRunID() string { return uuid.NewString() }
