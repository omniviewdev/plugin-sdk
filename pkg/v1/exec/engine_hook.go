package exec

import (
	"context"
	"errors"
	"slices"

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
			return nil, &Error{Class: ClassUnsupported, Err: errNoTargets}
		}
		plugin.BindConnectionContextProvider(cfg.Connections)
		server := plugin.v2.server
		server.engine.configure(cfg)
		return &hookEngine{server: server}, nil
	})
}

// errNoTargets is why an in-process engine cannot be built for no targets.
var errNoTargets = errors.New("an exec engine needs at least one target")

// inProcessFeatures are the features the in-process engine admits requests for: a session, a run and what they honor.
// What Describe lists is another matter (servedFeatures lists nothing until a step serves the RPC).
func inProcessFeatures() featureSet {
	return newFeatureSet([]execv2.Feature{
		execv2.Feature_FEATURE_SESSION, execv2.Feature_FEATURE_RUN,
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
		return nil, unsupportedRequest("an attach is not served by this build")
	}
	start := open.Start
	cmd, err := h.server.engine.start(ctx, startParams{
		ID:          open.SessionID,
		Kind:        kindSession,
		Target:      target,
		Route:       start.Target.Route,
		Request:     start.request(target),
		Stdin:       true,
		ReplayBytes: start.ReplayBytes,
	})
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
	id := run.RequestID
	if id == "" {
		id = "run-" + newRunID()
	}
	cmd, err := h.server.engine.start(ctx, startParams{
		ID:      id,
		Kind:    kindRun,
		Target:  target,
		Route:   run.Target.Route,
		Request: run.request(target),
		Stdin:   len(run.Stdin) > 0,
		Owner:   ctx,
	})
	if err != nil {
		return nil, err
	}
	if len(run.Stdin) > 0 {
		// A run's stdin is given with the request: it is sent and closed, so that the command reads it and then EOF.
		go func() {
			_, _ = cmd.WriteInput(context.Background(), run.Stdin)
			cmd.CloseInput()
		}()
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
	chunk, err := h.c.Read(ctx, buf)
	out := exechook.Chunk{Stream: chunk.Stream, Offset: chunk.Offset, N: chunk.N}
	for _, d := range chunk.Dropped {
		out.Dropped = append(out.Dropped, exechook.Drop{Stream: d.Stream, From: d.From, Bytes: d.Bytes})
	}
	return out, err
}

func (h hookCommand) Write(ctx context.Context, p []byte) (int, error) { return h.c.WriteInput(ctx, p) }

func (h hookCommand) CloseInput() { h.c.CloseInput() }

func (h hookCommand) Resize(seq uint32, cols, rows uint16) exechook.ResizeDisposition {
	switch h.c.Resize(seq, Size{Cols: cols, Rows: rows}) {
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
