package exec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/producers"
	logging "github.com/omniviewdev/plugin-sdk/log"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/utils/timeutil"
	"github.com/omniviewdev/plugin-sdk/settings"
)

const (
	// DefaultOutputBufferSize bounds retained output history in bytes per session.
	DefaultOutputBufferSize = 1000000
	// DefaultStreamBufferSize sets terminal read size and output channel capacity.
	DefaultStreamBufferSize = 4096
	// InitialRows sets PTY height before the first resize.
	InitialRows = 27
	// InitialCols sets PTY width before the first resize.
	InitialCols = 72
	// ResizeTimeout bounds delivery to handlers that manage their own resizing.
	ResizeTimeout       = 500 * time.Millisecond
	sessionCloseTimeout = 10 * time.Second
	sessionWorkerCount  = 2

	// terminalDrainTimeout bounds the wait, when the process behind a session is over, for the
	// terminal's output to be read: the kernel keeps what the process wrote last, and the end of
	// the session waits for it to be read, no longer than this.
	terminalDrainTimeout = 500 * time.Millisecond
	// retainedOutputLimit and retainedOutputTTL bound the output kept for the first attach to a
	// session that ended before anyone attached: how many sessions it is kept for, and for how long.
	retainedOutputLimit = 32
	retainedOutputTTL   = time.Minute
)

// ---------------------------------------------------------------------------
// ManagerConfig
// ---------------------------------------------------------------------------

// ManagerConfig configures the Manager.
type ManagerConfig struct {
	Logger          logging.Logger
	Settings        settings.Provider
	Handlers        map[string]Handler
	Sink            OutputSink      // nil → a ChannelSink for each stream, made by Stream()
	TerminalFactory TerminalFactory // nil → NewRealTerminalFactory()
	Clock           timeutil.Clock  // nil → timeutil.RealClock
}

// ---------------------------------------------------------------------------
// Manager
// ---------------------------------------------------------------------------

// Manager manages the lifecycle of terminal sessions within a plugin process.
type Manager struct {
	log                logging.Logger
	settingsProvider   settings.Provider
	handlers           map[string]Handler
	sink               OutputSink
	terminalFactory    TerminalFactory
	clock              timeutil.Clock
	connectionProvider resourcev2.ConnectionContextProvider

	mu       sync.RWMutex
	sessions map[string]*sessionState
	closed   bool // set by Close: no session is admitted from then on
	resize   chan StreamResize

	// stream is the stream the manager delivers to: the one opened last, while it lasts. A stream that
	// has been replaced, or has ended, serves nothing, and its end ends nothing. streamSink is the sink
	// the manager made for it, which is m.sink as long as nobody has put another there.
	stream     *streamState
	streamSink *ChannelSink

	// retained holds the output of sessions that ended while nobody was attached, for the first attach
	// to each (see retainedOutput). retainedSeq numbers them in the order they were kept.
	retained    map[string]*retainedOutput
	retainedSeq uint64

	// admitted counts the sessions that were admitted and have not finished: one producer
	// per session, started by registerSession under mu with the check of closed, and done
	// when the session's goroutines have all returned. Close sets closed and cancels the
	// sessions under mu before it waits, so it cannot miss a session, and none can join once
	// it has begun.
	admitted producers.Group
}

var _ Provider = (*Manager)(nil)

// NewManager creates a new Manager with the given config.
func NewManager(cfg ManagerConfig) *Manager {
	logger := cfg.Logger
	if logger == nil {
		logger = logging.NewNop()
	}

	clk := cfg.Clock
	if clk == nil {
		clk = timeutil.RealClock{}
	}

	tf := cfg.TerminalFactory
	if tf == nil {
		tf = NewRealTerminalFactory()
	}

	// Defensive copy to prevent external mutation of the manager's handler map.
	handlers := maps.Clone(cfg.Handlers)

	return &Manager{
		log:              logger.Named("exec.manager"),
		settingsProvider: cfg.Settings,
		handlers:         handlers,
		sink:             cfg.Sink,
		terminalFactory:  tf,
		clock:            clk,
		sessions:         make(map[string]*sessionState),
		retained:         make(map[string]*retainedOutput),
		resize:           make(chan StreamResize),
	}
}

// BindConnectionContextProvider binds resource utility admission to the active registry.
func (m *Manager) BindConnectionContextProvider(provider resourcev2.ConnectionContextProvider) {
	m.connectionProvider = provider
}

// Close refuses every later session start, cancels the active sessions and waits for their
// goroutines to finish. A start that loses the race with Close, and one that comes after
// it, is refused with a CONNECTION_ERROR (see [resourcev2.ErrorCode]): never admitted, and
// never left running. Close may be called more than once, and concurrently; each call
// returns once the sessions are gone. The output kept for sessions that ended is dropped.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	for _, ss := range m.sessions {
		ss.stop()
	}
	clear(m.retained)
	m.mu.Unlock()
	m.admitted.Wait()
}

// managerClosedError is what a start gets from a manager that has been closed. The
// classification crosses the plugin boundary; the sentence does not, as for every other
// error the plugin servers carry (see ResourceRPCError): the host composes its own.
func managerClosedError() error {
	return resourcev2.NewError("CONNECTION_ERROR", "Exec manager is closed.")
}

// GetSupportedResources returns registered terminal handlers.
func (m *Manager) GetSupportedResources(_ *types.PluginContext) []Handler {
	return slices.Collect(maps.Values(m.handlers))
}

// streamState is one call of Stream. A stream is told from another by its pointer, so it has fields: two
// pointers to values of no size may be equal. cancel ends the sink the manager made for the stream, if it
// did: a send that waits for a consumer that does not read, in a stream that has been replaced or has
// ended, must not hold up the session that makes it.
type streamState struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// Stream opens a stream that multiplexes the input and the output of the sessions. The output goes to
// the stream opened last: when the manager makes the sink itself (there is no Sink in its config), every
// stream has a channel and a sink of its own, delivered to from the moment the stream is open, and the
// stream it replaces is retired, with its sink: what that sink holds up is dropped.
//
// The end of a stream ends the sessions it served, once, whichever way it ended: its context is done,
// or its input is closed (the host closed its side of the RPC), or both. Their output can no longer be
// delivered, so they are not left running. The sessions of a stream that has been replaced are the
// newer stream's, and its end ends nothing. The sessions are cancelled, and not waited for: that is Close's.
func (m *Manager) Stream(ctx context.Context, in chan StreamInput) (chan StreamOutput, error) {
	sinkCtx, cancel := context.WithCancel(ctx)
	st := &streamState{ctx: ctx, cancel: cancel}
	var out chan StreamOutput

	m.mu.Lock()
	if m.sink == nil || (m.streamSink != nil && m.sink == OutputSink(m.streamSink)) {
		out = make(chan StreamOutput, DefaultStreamBufferSize)
		m.streamSink = NewChannelSink(sinkCtx, out)
		m.sink = m.streamSink
	}
	retired := m.stream
	m.stream = st
	m.mu.Unlock()
	if retired != nil {
		retired.cancel()
	}

	m.log.Debugw(ctx, "stream opened")
	go m.handleStreamInput(ctx, in, st)

	return out, nil
}

func (m *Manager) handleStreamInput(ctx context.Context, in chan StreamInput, st *streamState) {
	defer m.endStream(st)
	for {
		select {
		case <-ctx.Done():
			return

		case input, ok := <-in:
			if !ok {
				return
			}
			logger := m.log.With(logging.String("session", input.SessionID))
			logger.Debug(ctx, "received stream input")

			m.mu.RLock()
			ss, exists := m.sessions[input.SessionID]
			m.mu.RUnlock()
			if !exists {
				logger.Error(ctx, "session not found")
				continue
			}

			if err := m.writeToSession(ss, input.Data); err != nil {
				logger.Errorw(ctx, "error writing to session", "error", err)
			}

		case resize := <-m.resize:
			m.handleResize(ctx, resize)
		}
	}
}

// endStream is where every end of a stream comes: it is the last thing the stream's input loop does, so it
// runs once for each stream, however many ways the stream ended in at once. If the stream is the one the
// manager delivers to, the sessions it served are ended and the manager has no stream, and no sink of its
// own, until the next Stream: there is nowhere to deliver to. Otherwise a newer stream serves them.
func (m *Manager) endStream(st *streamState) {
	ended := 0
	m.mu.Lock()
	if m.stream == st {
		m.stream = nil
		if m.streamSink != nil && m.sink == OutputSink(m.streamSink) {
			m.sink = nil
		}
		m.streamSink = nil
		for _, ss := range m.sessions {
			ss.stop()
			ended++
		}
	}
	m.mu.Unlock()
	st.cancel()
	m.log.Debugw(st.ctx, "stream ended", "sessions_ended", ended)
}

func (m *Manager) handleResize(ctx context.Context, resize StreamResize) {
	logger := m.log.With(logging.String("session", resize.SessionID))
	logger.Debug(ctx, "received stream resize")
	m.mu.RLock()
	ss, exists := m.sessions[resize.SessionID]
	m.mu.RUnlock()
	if !exists {
		logger.Error(ctx, "session not found")
		return
	}
	if ss.ttyResize {
		if err := ss.terminal.Resize(resize.Rows, resize.Cols); err != nil {
			logger.Errorw(ctx, "failed to resize terminal", "error", err)
		}
		return
	}
	select {
	case ss.resize <- SessionResizeInput{Cols: int32(resize.Cols), Rows: int32(resize.Rows)}:
	case <-m.clock.After(ResizeTimeout):
		logger.Error(ctx, "timeout resizing session")
	}
}

// GetSession returns a session by ID.
func (m *Manager) GetSession(_ *types.PluginContext, sessionID string) (*Session, error) {
	m.mu.RLock()
	ss, exists := m.sessions[sessionID]
	m.mu.RUnlock()

	if !exists {
		return nil, NewSessionNotFoundError(sessionID)
	}
	snap := ss.snapshot()
	return &snap, nil
}

// ListSessions returns a list of details for all active sessions.
func (m *Manager) ListSessions(_ *types.PluginContext) ([]*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sessions := make([]*Session, 0, len(m.sessions))
	for _, ss := range m.sessions {
		snap := ss.snapshot()
		sessions = append(sessions, &snap)
	}
	return sessions, nil
}

// resourceAdmissionContext retains the caller's deadline until native creation
// accepts the session. Its cancellation channel remains the session lifetime.
type resourceAdmissionContext struct {
	context.Context

	parent   context.Context
	accepted atomic.Bool
}

func (c *resourceAdmissionContext) Deadline() (time.Time, bool) {
	if !c.accepted.Load() {
		return c.parent.Deadline()
	}
	return time.Time{}, false
}

func (c *resourceAdmissionContext) Err() error {
	if !c.accepted.Load() {
		if err := c.parent.Err(); err != nil {
			return err
		}
	}
	return c.Context.Err()
}

// CreateSession creates a new terminal session with a given command. Once Close has begun
// it is refused with a CONNECTION_ERROR (see [resourcev2.ErrorCode]).
func (m *Manager) CreateSession(
	pluginctx *types.PluginContext,
	opts SessionOptions,
) (*Session, error) {
	if opts.HasResourceIntent() {
		return nil, resourcev2.NewError("UNSUPPORTED", "resource session requires CreateResourceSession")
	}
	if err := ValidateSessionOptions(opts); err != nil {
		return nil, err
	}
	return m.createSession(pluginctx, opts, false)
}

// CreateResourceSession admits resource inputs before invoking the shared
// terminal engine. The body is utility input, never an authorization grant. Once Close has
// begun it is refused with a CONNECTION_ERROR (see [resourcev2.ErrorCode]).
func (m *Manager) CreateResourceSession(pluginctx *types.PluginContext, opts SessionOptions) (*Session, error) {
	normalized, err := NormalizeResourceSessionOptions(opts)
	if err != nil {
		return nil, err
	}
	if normalized.Route != nil &&
		(pluginctx == nil || pluginctx.Connection == nil || pluginctx.Connection.ID != normalized.Route.Connection) {
		return nil, resourcev2.NewError(
			"INVALID_INPUT",
			"resource session route must match the explicit caller connection",
		)
	}
	var session *Session
	err = resourcev2.WithUtilityConnection(
		pluginctx,
		m.connectionProvider,
		normalized.Reference,
		normalized.Route,
		func(bound *types.PluginContext) error {
			var createErr error
			session, createErr = m.createSession(bound, normalized, true)
			return createErr
		},
	)
	if err != nil {
		if session != nil {
			if closeErr := m.CloseSession(pluginctx, session.ID); closeErr != nil {
				m.log.Errorw(
					pluginContextOr(context.Background(), pluginctx),
					"failed to close rejected session",
					"error", closeErr,
				)
			}
		}
		return nil, err
	}
	return session, nil
}

func (m *Manager) createSession(
	pluginctx *types.PluginContext,
	opts SessionOptions,
	resource bool,
) (*Session, error) {
	logger := m.log.With(
		logging.String("resource", opts.ResourceKey),
		logging.Any("param_keys", sanitizeMapKeys(opts.Params)),
		logging.Any("label_keys", sanitizeMapKeys(opts.Labels)),
	)

	opts.ID = ensureSessionID(opts.ID)
	handler, err := m.sessionHandler(opts)
	if err != nil {
		return nil, err
	}

	pctxCopy, cancel, admission, stopAdmission, err := m.sessionContext(pluginctx, opts, resource)
	if err != nil {
		return nil, err
	}
	defer stopAdmission()
	ctx := pctxCopy.Context
	if admission != nil {
		ctx = admission.Context
	}
	baseCtx := pluginContextOr(context.Background(), pluginctx)

	// Create terminal via factory
	terminal, err := m.terminalFactory()
	if err != nil {
		cancel()
		return nil, NewTerminalError(opts.ID, err)
	}
	if terminal == nil {
		cancel()
		return nil, NewTerminalError(opts.ID, fmt.Errorf("terminal factory returned nil"))
	}

	if resource && baseCtx.Err() != nil {
		cancel()
		m.closeTerminal(ctx, terminal)
		return nil, baseCtx.Err()
	}
	resizeChan := make(chan SessionResizeInput)
	stopChan := make(chan error, 1)

	// Call the handler with the TTY side
	if err = handler.TTYHandler(&pctxCopy, opts, terminal.SlaveFd(), stopChan, resizeChan); err != nil {
		cancel()
		m.closeTerminal(ctx, terminal)
		if resource && baseCtx.Err() != nil {
			return nil, baseCtx.Err()
		}
		return nil, NewTerminalError(opts.ID, err)
	}

	ss := &sessionState{
		session: Session{
			ID:        opts.ID,
			Labels:    maps.Clone(opts.Labels),
			Params:    maps.Clone(opts.Params),
			Command:   slices.Clone(opts.Command),
			Attached:  false,
			CreatedAt: m.clock.Now(),
		},
		ctx:        ctx,
		cancel:     cancel,
		terminal:   terminal,
		buffer:     NewOutputBuffer(DefaultOutputBufferSize),
		ttyResize:  !handler.HandlesResize,
		resize:     resizeChan,
		stopChan:   stopChan,
		done:       make(chan struct{}),
		readerDone: make(chan struct{}),
	}

	if err = m.registerSession(baseCtx, ss, resource, admission, stopAdmission); err != nil {
		cancel()
		m.closeTerminal(ctx, terminal)
		return nil, err
	}
	logger.Debugw(ctx, "session created", "session", opts.ID)
	ss.wg.Add(sessionWorkerCount)
	go m.handleStream(ss)
	go m.handleSignals(ss)
	go func() {
		defer m.admitted.Done()
		ss.wg.Wait()
		close(ss.done)
	}()

	snap := ss.snapshot()
	return &snap, nil
}

func (m *Manager) sessionHandler(opts SessionOptions) (Handler, error) {
	// Refused here, before any terminal or handler work, once the manager is closed;
	// registerSession decides for good.
	m.mu.RLock()
	closed := m.closed
	_, exists := m.sessions[opts.ID]
	m.mu.RUnlock()
	if closed {
		return Handler{}, managerClosedError()
	}
	handlerKey := opts.ResourcePlugin + "/" + opts.ResourceKey
	handler, ok := m.handlers[handlerKey]
	if !ok || handler.TTYHandler == nil {
		return Handler{}, NewHandlerNotFoundError(handlerKey, slices.Collect(maps.Keys(m.handlers)))
	}
	if exists {
		return Handler{}, NewSessionExistsError(opts.ID)
	}
	return handler, nil
}

func (m *Manager) sessionContext(
	pluginctx *types.PluginContext,
	opts SessionOptions,
	resource bool,
) (types.PluginContext, context.CancelFunc, *resourceAdmissionContext, func() bool, error) {
	var pctx types.PluginContext
	if pluginctx != nil {
		pctx = *pluginctx
	}
	baseCtx := pluginContextOr(context.Background(), pluginctx)
	// Keep values, but let accepted sessions outlive the creating RPC.
	ctx, cancel := context.WithCancel(context.WithoutCancel(baseCtx))
	var admission *resourceAdmissionContext
	stopAdmission := func() bool { return true }
	pctx.Context = ctx
	if resource {
		if err := baseCtx.Err(); err != nil {
			cancel()
			return pctx, nil, nil, nil, err
		}
		admission = &resourceAdmissionContext{Context: ctx, parent: baseCtx}
		stopAdmission = context.AfterFunc(baseCtx, cancel)
		pctx.Context = admission
	}
	if m.settingsProvider != nil {
		pctx.SetSettingsProvider(m.settingsProvider)
	}
	pctx.Logger = m.log.With(
		logging.String("session", opts.ID),
		logging.String("resource", opts.ResourceKey),
	)
	return pctx, cancel, admission, stopAdmission, nil
}

// registerSession admits the session: under mu it checks that the manager is open and the ID
// free, and counts the session, so that Close, which closes the manager and cancels the
// sessions under mu, waits for this one or refuses it, and never misses it.
func (m *Manager) registerSession(
	baseCtx context.Context,
	ss *sessionState,
	resource bool,
	admission *resourceAdmissionContext,
	stopAdmission func() bool,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return managerClosedError()
	}
	if _, exists := m.sessions[ss.session.ID]; exists {
		return NewSessionExistsError(ss.session.ID)
	}
	if resource {
		if !stopAdmission() || baseCtx.Err() != nil {
			if err := baseCtx.Err(); err != nil {
				return err
			}
			return context.Canceled
		}
		admission.accepted.Store(true)
	}
	if !m.admitted.Start(1) {
		// Not reachable: the group ends only after Close has set closed, which is checked
		// above, under the lock that admits.
		return managerClosedError()
	}
	delete(m.retained, ss.session.ID) // a session of this ID is live: what an earlier one left is not its output
	m.sessions[ss.session.ID] = ss
	return nil
}

func (m *Manager) closeTerminal(ctx context.Context, terminal Terminal) {
	if err := terminal.Close(); err != nil {
		m.log.Errorw(ctx, "failed to close terminal", "error", err)
	}
}

func (m *Manager) handleSignals(ss *sessionState) {
	defer ss.wg.Done()

	logger := m.log.With(logging.String("session", ss.session.ID))

	select {
	case err := <-ss.stopChan:
		logger.Debug(ss.ctx, "stop channel received, stopping read stream handling")
		m.cleanupSession(ss, err, true)

	case <-ss.ctx.Done():
		logger.Debug(ss.ctx, "context cancelled, stopping read stream handling")
		m.cleanupSession(ss, nil, false)
	}
}

// handleStream reads the terminal's output until the terminal has nothing more to say: it keeps the output in the
// session's buffer, and delivers it while someone is attached. It returns when the terminal's output ends
// (EOF, or EIO as Linux says it when the other side is gone), when the terminal is closed, or when it has read
// the end mark of the session (see drainTerminal). It ends the session, if nothing else did, when the output ends.
func (m *Manager) handleStream(ss *sessionState) {
	defer ss.wg.Done()
	defer close(ss.readerDone) // runs before the wg is done: whoever waits for the session finds the reader done

	logger := m.log.With(logging.String("session", ss.session.ID))
	masterFd := ss.terminal.MasterFd()
	readBuf := make([]byte, DefaultStreamBufferSize)
	var marks markScanner

	for {
		read, err := masterFd.Read(readBuf)
		if read > 0 {
			data, reachedMark := marks.feed(ss.endMark(), readBuf[:read])
			m.deliver(ss, data)
			if reachedMark {
				logger.Debug(ss.ctx, "end mark reached on terminal")
				return
			}
		}
		if err != nil {
			// What the scanner holds back is dropped (see markScanner): the terminal was closed in the middle of the mark.
			if isEndOfOutput(err) {
				logger.Debug(ss.ctx, "end of terminal output")
			} else {
				logger.Errorw(ss.ctx, "error reading from terminal", "error", err)
			}
			// On read error or EOF, cancel so handleSignals fires cleanup.
			ss.cancel()
			return
		}
	}
}

// deliver keeps output in the session's buffer, and hands it to the sink while someone is attached.
func (m *Manager) deliver(ss *sessionState, data []byte) {
	if len(data) == 0 {
		return
	}
	// Buffer first, then check attached — under the same lock that
	// AttachSession uses to set attached + read the buffer. This
	// eliminates the race where data is appended after the attach
	// reads the buffer but before handleStream emits it.
	ss.mu.Lock()
	ss.buffer.Append(data)
	attached := ss.session.Attached
	seq := ss.nextSeq
	ss.nextSeq++
	ss.mu.Unlock()
	if attached {
		// A frame outlives the read that made it, and the next read overwrites the buffer it was read into: the
		// frame gets a copy of exactly the bytes it carries, and not a buffer of four kilobytes.
		m.emitData(ss, StreamOutput{
			SessionID: ss.session.ID,
			Target:    StreamTargetStdOut,
			Data:      slices.Clone(data),
			Seq:       seq,
		})
	}
}

// isEndOfOutput reports whether err is how a read of a terminal says that there is nothing more to read: EOF, EIO
// (Linux, when every holder of the other side has closed it), or a terminal that was closed.
func isEndOfOutput(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, syscall.EIO) || errors.Is(err, os.ErrClosed)
}

// drainTerminal gives the reader of a session whose process is over the time to take what the process wrote
// last. Output the reader has not taken stays in the kernel's queue, and closing the terminal throws it away; the
// kernel is no help in telling when the reader has everything (macOS and the BSDs drop the queue when the slave
// is closed, and Linux can drop part of it when the slave is closed under load). So the end of the session writes a
// mark to the terminal after that output, which the reader meets when it has read all that came before it (see
// markScanner), and waits for the reader to be done.
//
// The wait is bounded, by terminalDrainTimeout on the manager's clock, so that a consumer that does not take
// output cannot keep a session from ending, and it gives way to the session being cancelled: the host that closes
// a session, or a manager that closes, is not waiting for its output. What the reader has not taken by then is
// lost, and the Close signal is still the last thing the session says. A terminal that cannot take a mark is not waited for.
func (m *Manager) drainTerminal(ss *sessionState) {
	terminal, ok := ss.terminal.(EndMarker)
	if !ok || ss.ctx.Err() != nil {
		return
	}
	select {
	case <-ss.readerDone:
		return
	default:
	}

	mark := newEndMark()
	ss.mark.Store(&mark) // before the write: from now on the reader looks for the mark
	timeout := m.clock.After(terminalDrainTimeout)
	// The kernel queues little for a master that is not read, so the write can wait for the reader: it is done
	// where the wait can be given up, and the close of the terminal sets it free. If it fails the output ends
	// by itself (the slave is closed), and the reader says so.
	go func() { _ = terminal.MarkEnd(mark) }()
	select {
	case <-ss.readerDone:
	case <-ss.ctx.Done():
	case <-timeout:
	}
}

// cleanupSession ends a session: it closes the terminal, takes the session out of the manager,
// and says goodbye through the sink. byProcess is true when the end is the handler's own, the process behind
// the session being over: its output is complete only once the reader has taken it (see drainTerminal), and a
// session that the host or the manager ended has no output to wait for.
func (m *Manager) cleanupSession(ss *sessionState, handlerErr error, byProcess bool) {
	sessionID := ss.session.ID

	if byProcess {
		m.drainTerminal(ss)
	}
	m.closeTerminal(ss.ctx, ss.terminal)

	m.leave(ss)

	// Cancel the context to unblock handleStream if it hasn't returned yet.
	ss.cancel()

	// Say goodbye under the lock that orders the session's output: data that is still being
	// delivered finishes first, data that comes after is dropped. The structured error, if the
	// handler sent one, comes before the Close signal, which is the last thing the session sends.
	ss.emitMu.Lock()
	defer ss.emitMu.Unlock()
	if handlerErr != nil {
		var streamErr *StreamError
		var execErr *ExecError
		if errors.As(handlerErr, &execErr) {
			streamErr = &StreamError{
				Title:         execErr.Title,
				Message:       execErr.Message,
				Suggestion:    execErr.Suggestion,
				Retryable:     execErr.Retryable,
				RetryCommands: execErr.RetryCommands,
			}
		} else {
			streamErr = &StreamError{
				Title:      "Session failed",
				Message:    handlerErr.Error(),
				Suggestion: "The exec session terminated unexpectedly.",
				Retryable:  true,
			}
		}
		m.emitOutput(StreamOutput{
			SessionID: sessionID,
			Target:    StreamTargetStdErr,
			Data:      []byte(handlerErr.Error()),
			Signal:    StreamSignalError,
			Error:     streamErr,
		})
	}

	// Signal close
	m.emitOutput(StreamOutput{
		SessionID: sessionID,
		Target:    StreamTargetStdOut,
		Data:      []byte("\nSession closed\n"),
		Signal:    StreamSignalClose,
	})
	ss.ended = true
}

// leave takes an ended session out of the manager. A session that ended by itself while nobody was attached
// leaves its output for the next attach, which comes too late to find the session: the short command
// that is over before the host has attached. The session leaves the map and its output is kept in
// one step, under the lock an attach takes to look for the session, so that an attach finds the session
// or its output and never neither. What the host closed, or the manager did, or a stream's end did, is
// not kept: nobody is waiting for it.
func (m *Manager) leave(ss *sessionState) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ss.mu.Lock()
	ss.gone = true
	var kept *retainedOutput
	if !ss.session.Attached && !ss.requested.Load() {
		kept = &retainedOutput{owner: ss.waitDone(), session: ss.snapshotLocked(), output: ss.getBufferData()}
	}
	ss.mu.Unlock()

	delete(m.sessions, ss.session.ID)
	if kept != nil {
		m.retain(ss.session.ID, kept)
	}
}

// retain keeps what a session left, to the bounds of retainedOutputLimit and retainedOutputTTL: what is
// past its time is dropped first, and then the oldest, to make room. The caller holds m.mu.
func (m *Manager) retain(id string, kept *retainedOutput) {
	now := m.clock.Now()
	kept.at, kept.seq = now, m.retainedSeq
	m.retainedSeq++

	delete(m.retained, id)
	for key, other := range m.retained {
		if now.Sub(other.at) > retainedOutputTTL {
			delete(m.retained, key)
		}
	}
	for len(m.retained) >= retainedOutputLimit {
		var oldest string
		var oldestSeq uint64
		for key, other := range m.retained {
			if oldest == "" || other.seq < oldestSeq {
				oldest, oldestSeq = key, other.seq
			}
		}
		delete(m.retained, oldest)
	}
	m.retained[id] = kept
}

// takeRetained gives the first attach to a session that ended the output it left, if it is still kept. An
// attach that found a session of its own (owner is the session's done channel) takes only what that session left:
// what a session that took its ID in the meantime left is not its output, and stays for the attach that asks for
// that one. An attach that found no session (owner nil) takes what the latest session with the ID left.
func (m *Manager) takeRetained(id string, owner <-chan struct{}) (*retainedOutput, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept, ok := m.retained[id]
	if !ok || (owner != nil && kept.owner != owner) {
		return nil, false
	}
	delete(m.retained, id)
	if m.clock.Now().Sub(kept.at) > retainedOutputTTL {
		return nil, false
	}
	return kept, true
}

// emitData delivers a chunk of the session's output unless the session has said goodbye: the
// Close signal is the last thing a session sends, so a chunk still on its way when the session
// ends is delivered before it, and one that comes after it is dropped.
func (m *Manager) emitData(ss *sessionState, output StreamOutput) {
	ss.emitMu.Lock()
	defer ss.emitMu.Unlock()
	if ss.ended {
		return
	}
	m.emitOutput(output)
}

func (m *Manager) writeToSession(ss *sessionState, data []byte) error {
	if ss == nil {
		return errors.New("session is nil")
	}
	if ss.terminal == nil {
		return NewSessionClosedError(ss.session.ID)
	}
	if _, err := ss.terminal.MasterFd().Write(data); err != nil {
		return NewTerminalError(ss.session.ID, err)
	}
	return nil
}

// AttachSession marks a session as attached and returns its current output buffer.
//
// A session that ended while nobody was attached (the short command that is over before the host comes
// to attach) has left its output for the first attach: that call returns what the session wrote and the
// session as it was, though no other call finds it any more, and then the output is gone. What is kept
// is bounded, in the number of sessions and in time, and a session that the host or the manager ended
// leaves nothing.
func (m *Manager) AttachSession(
	pluginctx *types.PluginContext,
	sessionID string,
) (*Session, []byte, error) {
	m.mu.RLock()
	ss, exists := m.sessions[sessionID]
	m.mu.RUnlock()
	var lost <-chan struct{} // the done channel of the session this call found and lost to its end

	if exists {
		// The session is found, and not yet attached to: it may end before this call has it. A test stops here,
		// by the logger, to have the session end between the two steps.
		m.log.Debugw(pluginContextOr(ss.ctx, pluginctx), "attach found the session", "session", sessionID)

		// Set attached and snapshot the buffer under the same lock that
		// handleStream uses for append+attached-check, so no data is orphaned.
		ss.mu.Lock()
		if !ss.gone {
			ss.session.Attached = true
			bufData := ss.getBufferData()
			ss.mu.Unlock()

			m.log.Debugw(pluginContextOr(ss.ctx, pluginctx), "session attached", "session", sessionID)

			snap := ss.snapshot()
			return &snap, bufData, nil
		}
		ss.mu.Unlock()
		lost = ss.waitDone()
		// The session ended while this call was on its way to it. It left its output in the step in
		// which it left the manager (see leave), so it is found where the ended sessions are.
	}

	if kept, ok := m.takeRetained(sessionID, lost); ok {
		m.log.Debugw(pluginContextOr(context.Background(), pluginctx),
			"output of an ended session attached", "session", sessionID)
		session := kept.session
		session.Attached = true
		return &session, kept.output, nil
	}
	return nil, nil, NewSessionNotFoundError(sessionID)
}

// DetachSession marks a session as not attached, stopping output broadcast.
func (m *Manager) DetachSession(pluginctx *types.PluginContext, sessionID string) (*Session, error) {
	m.mu.RLock()
	ss, exists := m.sessions[sessionID]
	m.mu.RUnlock()

	if !exists {
		return nil, NewSessionNotFoundError(sessionID)
	}

	ss.setAttached(false)
	m.log.Debugw(pluginContextOr(ss.ctx, pluginctx), "session detached", "session", sessionID)

	snap := ss.snapshot()
	return &snap, nil
}

// CloseSession ends the session at the host's request: it cancels the session's context, triggering
// cleanup, and waits for the session's goroutines. A session ended this way keeps no output for an attach.
func (m *Manager) CloseSession(pluginctx *types.PluginContext, sessionID string) error {
	m.mu.RLock()
	ss, exists := m.sessions[sessionID]
	m.mu.RUnlock()

	if !exists {
		return NewSessionNotFoundError(sessionID)
	}

	ctx := pluginContextOr(ss.ctx, pluginctx)
	// The session is found, and not yet stopped: it may end before this call stops it. A test stops here, by the
	// logger, to have the session end between the two steps.
	m.log.Debugw(ctx, "close found the session", "session", sessionID)
	if !ss.stopUnlessGone() {
		// It ended by itself in between, and left its output as such: there is no session to close.
		return NewSessionNotFoundError(sessionID)
	}
	m.log.Debugw(ctx, "session terminated", "session", sessionID)

	// Wait for session goroutines to complete with a safety timeout.
	select {
	case <-ss.waitDone():
		return nil
	case <-m.clock.After(sessionCloseTimeout):
		m.log.Warnw(ctx, "CloseSession timed out waiting for goroutines", "session_id", sessionID)
		return fmt.Errorf("CloseSession timed out waiting for session %s goroutines", sessionID)
	}
}

func pluginContextOr(fallback context.Context, pluginctx *types.PluginContext) context.Context {
	if pluginctx != nil && pluginctx.Context != nil {
		return pluginctx.Context
	}
	if fallback != nil {
		return fallback
	}
	return context.Background()
}

// ResizeSession resizes a session.
func (m *Manager) ResizeSession(
	_ *types.PluginContext,
	sessionID string,
	rows, cols int32,
) error {
	if rows < 1 || rows > 65535 || cols < 1 || cols > 65535 {
		return fmt.Errorf("invalid resize dimensions: rows=%d cols=%d (must be 1..65535)", rows, cols)
	}

	m.mu.RLock()
	_, exists := m.sessions[sessionID]
	m.mu.RUnlock()
	if !exists {
		return NewSessionNotFoundError(sessionID)
	}

	select {
	case m.resize <- StreamResize{
		SessionID: sessionID,
		Rows:      uint16(rows),
		Cols:      uint16(cols),
	}:
		return nil
	case <-m.clock.After(ResizeTimeout):
		return fmt.Errorf("timeout sending resize for session %s", sessionID)
	}
}

// sanitizeMapKeys returns only the keys of a map, stripping values
// that may contain sensitive data (credentials, tokens, PII).
func sanitizeMapKeys(m map[string]string) []string {
	if m == nil {
		return nil
	}
	return slices.Collect(maps.Keys(m))
}

// emitOutput sends output through the sink if available.
// It acquires the read lock to safely access the sink field.
func (m *Manager) emitOutput(output StreamOutput) {
	m.mu.RLock()
	sink := m.sink
	m.mu.RUnlock()
	if sink == nil {
		return
	}
	sink.OnOutput(output)
}
