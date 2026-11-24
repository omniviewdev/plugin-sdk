package exec

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/internal/session"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// pkg/v1/exec/exectest and the tests of this package drive the engine in process through internal/exechook, which is the
// only place that names the engine to them: the engine and its command are unexported, and what they need is a handle that
// speaks in wire messages and plain values. The adapter below is that handle. It builds the plugin's targets the way a plugin
// does (NewTargetPlugin, so the engine is the one the plugin's server holds), admits an Open or a Run the way the served RPC
// will (the strict request, the registered target), and starts the command.
func init() {
	exechook.SetEngineFactory(func(targets []Target, cfg exechook.EngineConfig) (exechook.Engine, error) {
		plugin, err := NewTargetPlugin(targets)
		if err != nil {
			return nil, err
		}
		if plugin.v2 == nil {
			return nil, &Error{Class: ErrorClassUnsupported, Err: errNoTargets}
		}
		plugin.BindConnectionContextProvider(cfg.Connections)
		server := plugin.v2.server
		server.engine.configure(cfg)
		if cfg.Now != nil {
			// The clock the tombstones, the leases' ends and the activity times read; the timers stay real. Set before the first start, as
			// EngineSeams.Now is.
			server.engine.now = cfg.Now
		}
		return &hookEngine{server: server}, nil
	})
}

// errNoTargets is why an in-process engine cannot be built for no targets.
var errNoTargets = errors.New("an exec engine needs at least one target")

// inProcessFeatures are the features the in-process engine admits requests for: a session, a run and what they honor (the lease and
// the replay ring of a session included, which a start asks for). What Describe lists is servedFeatures, which is the wire's.
func inProcessFeatures() featureSet {
	return newFeatureSet([]execv2.Feature{
		execv2.Feature_FEATURE_SESSION, execv2.Feature_FEATURE_SESSION_MANAGEMENT, execv2.Feature_FEATURE_RUN,
		execv2.Feature_FEATURE_ENV, execv2.Feature_FEATURE_CWD, execv2.Feature_FEATURE_STDIN_EOF,
	})
}

// hookEngine is the engine of a plugin's server, as exechook.Engine.
type hookEngine struct{ server *serverV2 }

func (h *hookEngine) Open(ctx context.Context, wire *execv2.OpenRequest) (exechook.Command, error) {
	open, target, refusal := admitOpenRequest(wire, h.server.reg, inProcessFeatures())
	if refusal != nil {
		return nil, refusal
	}
	if open.Start == nil {
		return nil, unsupportedRequest("an attach has a stream and not a command: Attach takes it")
	}
	cmd, err := h.server.startSession(ctx, open, target)
	if err != nil {
		return nil, err
	}
	return hookCommand{cmd}, nil
}

func (h *hookEngine) Run(ctx context.Context, wire *execv2.RunRequest) (exechook.Command, error) {
	run, target, refusal := admitRunRequest(wire, h.server.reg, inProcessFeatures())
	if refusal != nil {
		return nil, refusal
	}
	// The run is started as the served RPC starts it (startRun): the same id rule, the same owner and the same stdin feed. The
	// served RPC adds the timeout and the capture of the output; a test of the engine takes those on itself.
	cmd, err := h.server.startRun(ctx, run, target)
	if err != nil {
		return nil, err
	}
	return hookCommand{cmd}, nil
}

func (h *hookEngine) Close(ctx context.Context) error { return h.server.engine.close(ctx) }

func (h *hookEngine) Live() []string { return h.server.engine.live() }

func (h *hookEngine) Abandoned() int64 { return h.server.engine.tel.abandonedCount() }

// hookCommand is a command, as exechook.Command.
type hookCommand struct{ c *command }

func (h hookCommand) ID() string { return h.c.ID() }

func (h hookCommand) Opened() *execv2.Capabilities { return capabilitiesToProto(h.c.Opened()) }

func (h hookCommand) Effective() *execv2.Capabilities { return capabilitiesToProto(h.c.Effective()) }

func (h hookCommand) Read(ctx context.Context, buf []byte) (exechook.Chunk, error) {
	return hookRead(h.c.Read(ctx, buf))
}

// hookRead is a read of an attachment or of a run's pipe as exechook says it: the range lost and the bytes, and the one error of the
// engine's that the hook names, the stream that left.
func hookRead(chunk outputChunk, err error) (exechook.Chunk, error) {
	out := exechook.Chunk{Stream: chunk.Stream, Offset: chunk.Offset, N: chunk.N}
	for _, d := range chunk.Dropped {
		out.Dropped = append(out.Dropped, exechook.Drop{Stream: d.Stream, From: d.From, Bytes: d.Bytes})
	}
	if errors.Is(err, errStreamLeft) {
		err = exechook.ErrStreamLeft
	}
	return out, err
}

func (h hookCommand) Write(ctx context.Context, p []byte) (int, error) { return h.c.WriteInput(ctx, p) }

func (h hookCommand) CloseInput() { h.c.CloseInput() }

func (h hookCommand) Resize(seq uint32, cols, rows uint16) exechook.ResizeDisposition {
	return hookResize(h.c.Resize(seq, Size{Cols: cols, Rows: rows}, nil))
}

func hookResize(d resizeDisposition) exechook.ResizeDisposition {
	switch d {
	case resizeAccepted:
		return exechook.ResizeAccepted
	case resizeIgnored:
		return exechook.ResizeIgnored
	case resizeInvalid:
		return exechook.ResizeInvalid
	case resizeClosed:
	}
	return exechook.ResizeClosed
}

func (h hookCommand) Close(kill bool) { h.c.Close(session.CauseRequested, kill) }

func (h hookCommand) Done() <-chan struct{} { return h.c.Done() }

func (h hookCommand) Gone() <-chan struct{} { return h.c.Gone() }

func (h hookCommand) Result() exechook.Result {
	r := h.c.Result()
	return exechook.Result{
		End: r.End, Cause: r.Cause.String(), StartObserved: r.StartObserved, Duration: r.Duration,
		BytesOut: r.BytesOut, BytesIn: r.BytesIn, DroppedBytes: r.DroppedBytes, LateWrites: r.LateWrites, LateBytes: r.LateBytes,
		Output: r.Output, Abandoned: r.Abandoned, Violations: slices.Clone(r.Violations),
	}
}

func (h hookCommand) Started() (exechook.Started, bool) {
	facts, ok := h.c.Started()
	if !ok {
		return exechook.Started{}, false
	}
	out := exechook.Started{
		Argv: slices.Clone(facts.Program.Argv), Name: facts.Program.Name, Path: facts.Program.Path, PID: facts.PID,
		Cols: facts.Size.Cols, Rows: facts.Size.Rows, Term: facts.Term, Transport: facts.Transport,
	}
	if facts.TTY != nil {
		out.TTY = new(*facts.TTY)
	}
	if facts.Capabilities != nil {
		out.Capabilities = capabilitiesToProto(*facts.Capabilities)
	}
	return out, true
}

func (h hookCommand) StartedC() <-chan struct{} { return h.c.StartedC() }

func (h hookCommand) Link() (execv2.LinkState, bool) {
	state, ok := h.c.Link()
	return state.toProto(), ok
}

func (h hookCommand) Violations() []string { return h.c.Violations() }

func (h hookCommand) LinkChanged() <-chan struct{} { return h.c.LinkChanged() }

func (h hookCommand) AppliedResize() (seq uint32, cols, rows uint16, ok bool) {
	seq, size, ok := h.c.AppliedResize()
	return seq, size.Cols, size.Rows, ok
}

func (h hookCommand) ResizeApplied() <-chan struct{} { return h.c.ResizeApplied() }

func (h hookCommand) Signal(sig execv2.Signal) exechook.SignalDisposition {
	return hookSignal(h.c.Signal(enumFromProto(signalPairs, sig)))
}

func hookSignal(d signalDisposition) exechook.SignalDisposition {
	switch d {
	case signalQueued:
		return exechook.SignalQueued
	case signalUndeclared:
		return exechook.SignalUndeclared
	case signalClosed:
	}
	return exechook.SignalClosed
}

// Stream is the stream that started the session: the creator's attachment, which is what Read reads, with the Opened of a new session.
func (h hookCommand) Stream() exechook.Stream {
	if h.c.creator == nil {
		return nil // a run has a pipe and no stream
	}
	return &hookStream{c: h.c, att: h.c.creator, opened: &execv2.Opened{Facts: h.c.openedFacts(), Replay: &execv2.Replay{}}}
}

// Attach attaches a stream to a session that lives: the request is admitted as the served Open admits it, and the attach is the engine's
// (engine.attach), the one the served Open makes.
func (h *hookEngine) Attach(ctx context.Context, wire *execv2.OpenRequest) (exechook.Stream, error) {
	open, _, refusal := admitOpenRequest(wire, h.server.reg, inProcessFeatures())
	if refusal != nil {
		return nil, refusal
	}
	if open.Attach == nil {
		return nil, invalidRequest("an attach request attaches: this one starts a session")
	}
	cmd, att, opened, refusal := h.server.engine.attach(ctx, open.SessionID, open.Attach.FromOffset)
	if refusal != nil {
		return nil, refusal
	}
	return &hookStream{c: cmd, att: att, opened: opened}, nil
}

// The calls that manage sessions are the served handlers, called without a transport, and what they refuse is decoded from the status as
// the host's client decodes it.

func (h *hookEngine) ListSessions(ctx context.Context) ([]*execv2.SessionInfo, error) {
	resp, err := h.server.ListSessions(ctx, &execv2.ListSessionsRequest{})
	if err != nil {
		return nil, errorFromStatus(ctx, err)
	}
	return resp.GetSessions(), nil
}

func (h *hookEngine) ReadOutput(ctx context.Context, req *execv2.ReadOutputRequest) (*execv2.ReadOutputResponse, error) {
	resp, err := h.server.ReadOutput(ctx, req)
	if err != nil {
		return nil, errorFromStatus(ctx, err)
	}
	return resp, nil
}

func (h *hookEngine) EndSession(ctx context.Context, req *execv2.EndSessionRequest) error {
	if _, err := h.server.EndSession(ctx, req); err != nil {
		return errorFromStatus(ctx, err)
	}
	return nil
}

// hookStream is a stream of a session, as exechook.Stream: an attachment of the session, and the frames of a host's stream, which take
// effect only while the attachment is the session's (attachment.whileCurrent), as the served stream applies them.
type hookStream struct {
	c      *command
	att    *attachment
	opened *execv2.Opened
}

func (s *hookStream) Opened() *execv2.Opened { return s.opened }

func (s *hookStream) Replay() exechook.Replay {
	r := s.opened.GetReplay()
	return exechook.Replay{Start: r.GetStart(), End: r.GetEnd(), Bytes: r.GetBytes()}
}

func (s *hookStream) Read(ctx context.Context, buf []byte) (exechook.Chunk, error) {
	return hookRead(s.att.Read(ctx, buf))
}

// Final is what the served stream is sent last: lastFrameOf decides it from what became of the attachment.
func (s *hookStream) Final() exechook.Final {
	fin := lastFrameOf(s.att.outcome())
	return exechook.Final{Detached: fin.detached, Reason: fin.reason, Lease: fin.lease}
}

// Write is an input frame. It is not applied inside whileCurrent, because it waits for room in the function's queue and the session
// must be able to change hands meanwhile (see whileCurrent): a stream that has left is refused before it begins.
func (s *hookStream) Write(ctx context.Context, p []byte) (int, error) {
	if s.att.over.Load() {
		return 0, exechook.ErrStreamLeft
	}
	return s.c.WriteInput(ctx, p)
}

func (s *hookStream) CloseInput() error {
	if !s.att.whileCurrent(s.c.CloseInput) {
		return exechook.ErrStreamLeft
	}
	return nil
}

func (s *hookStream) Resize(seq uint32, cols, rows uint16) (exechook.ResizeDisposition, error) {
	var d resizeDisposition
	if !s.att.whileCurrent(func() { d = s.c.Resize(seq, Size{Cols: cols, Rows: rows}, nil) }) {
		return exechook.ResizeClosed, exechook.ErrStreamLeft
	}
	return hookResize(d), nil
}

func (s *hookStream) Signal(sig execv2.Signal) (exechook.SignalDisposition, error) {
	var d signalDisposition
	if !s.att.whileCurrent(func() { d = s.c.Signal(enumFromProto(signalPairs, sig)) }) {
		return exechook.SignalClosed, exechook.ErrStreamLeft
	}
	return hookSignal(d), nil
}

func (s *hookStream) Detach(lease time.Duration) (time.Time, bool) { return s.c.detach(s.att, lease) }

func (s *hookStream) Close(kill bool) { s.c.ring.closeFrom(s.att, kill) }

func (s *hookStream) Release() { s.c.release(s.att) }

func (s *hookStream) Leave() { s.c.leave(s.att) }
