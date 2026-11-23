package exechook

import (
	"context"
	"time"

	logging "github.com/omniviewdev/plugin-sdk/log"
	sdkdiag "github.com/omniviewdev/plugin-sdk/pkg/diagnostics"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The in-process entry point of the exec engine. Nothing serves the engine over the wire yet (Open and Run are not
// served by this build), so the engine is driven in process: by the tests of pkg/v1/exec, and by pkg/v1/exec/exectest,
// whose conformance cases and stress harness run an author's target, or a scripted one, through the real engine without a
// transport. The types below speak in wire messages and plain values, because exectest cannot name pkg/v1/exec's
// unexported types; pkg/v1/exec registers the factory when it is initialized.

// EngineConfig is what an in-process engine is built with.
type EngineConfig struct {
	// Connections is the registry of connections that the plugin's resource provider owns, as the SDK binds it to the
	// exec capability. Nil binds none: a start that addresses a resource is then refused by the resource layer.
	Connections resourcev2.ConnectionContextProvider
	// Logger receives the engine's logs. Nil discards them.
	Logger logging.Logger
	// Diagnostics is the registry the engine publishes its measurements to. Nil is the process registry.
	Diagnostics *sdkdiag.Registry
	// CloseGrace, ResizeUnreadAfter and MetricsEvery shorten the engine's timings, which a test needs. Zero is the default.
	CloseGrace        time.Duration
	ResizeUnreadAfter time.Duration
	MetricsEvery      time.Duration
	// OptionsDeadline and ClassifyDeadline shorten how long the engine waits for an author's Options callback during a start
	// and for its Classify function at the end of a command before it gives up on them. Zero is the default.
	OptionsDeadline  time.Duration
	ClassifyDeadline time.Duration
}

// Engine is the exec engine of a plugin, driven in process.
type Engine interface {
	// Open starts a session from the first frame of an Open: it is admitted exactly as the served Open will admit it (the
	// strict request, the features this in-process engine serves, the registered target), and then started. A refusal is
	// the *exec.Error or resource-lane error the served Open would send as a status.
	Open(ctx context.Context, req *execv2.OpenRequest) (Command, error)
	// Run starts a bounded run from a RunRequest, admitted the same way. The command it returns is the run's: it has no
	// terminal, an input only when the request carries stdin (already sent, and closed), and it ends with ctx.
	Run(ctx context.Context, req *execv2.RunRequest) (Command, error)
	// Close refuses later starts, ends every command and waits until each has ended, or until ctx ends.
	Close(ctx context.Context) error
	// Live returns the ids of the commands the engine holds, sorted.
	Live() []string
	// Abandoned returns how many functions the engine gave up on that have not returned yet.
	Abandoned() int64
}

// Command is a running command, as its consumer sees it.
type Command interface {
	ID() string
	// Opened returns the capabilities the command was opened with.
	Opened() *execv2.Capabilities
	// Effective returns the capabilities of the command now.
	Effective() *execv2.Capabilities
	// Read waits for output: the ranges lost since the last Read and up to len(buf) bytes of one stream. It returns io.EOF
	// when the command has ended and everything was read, and ctx's error when ctx ends first.
	Read(ctx context.Context, buf []byte) (Chunk, error)
	// Write sends input, waiting while the command's input queue is full.
	Write(ctx context.Context, p []byte) (int, error)
	// CloseInput is stdin EOF.
	CloseInput()
	// Resize sends a terminal size.
	Resize(seq uint32, cols, rows uint16) ResizeDisposition
	// Close ends the command with the cause "requested": graceful, or with kill without waiting for the function.
	Close(kill bool)
	// Done is closed when the command has ended and its Result is final. The command's id is still held at that moment: an
	// Open of the same id may be refused as a conflict until Gone is closed.
	Done() <-chan struct{}
	// Gone is closed once the command has been removed from the engine: its id and its place among the target's sessions are
	// free. It is closed after Done.
	Gone() <-chan struct{}
	// Result waits for the end and returns it.
	Result() Result
	// Started returns what the host would receive in Started, once there is one.
	Started() (Started, bool)
	// StartedC is closed when Started has something to return.
	StartedC() <-chan struct{}
	// Link returns the last link state the function reported, if it reported one.
	Link() (execv2.LinkState, bool)
	// LinkChanged receives a value after the link state changed.
	LinkChanged() <-chan struct{}
	// AppliedResize returns the last size the function took that a host sent, with the host's seq.
	AppliedResize() (seq uint32, cols, rows uint16, ok bool)
	// ResizeApplied receives a value after the function took a size a host sent.
	ResizeApplied() <-chan struct{}
	// Violations returns the ways the function has broken the contract so far.
	Violations() []string
}

// Chunk is what one Read of a command's output returns.
type Chunk struct {
	Stream  execv2.Stream
	Offset  uint64
	N       int
	Dropped []Drop
}

// Drop is a range of output the command lost: [From, From+Bytes) of one stream.
type Drop struct {
	Stream execv2.Stream
	From   uint64
	Bytes  uint64
}

// ResizeDisposition is what became of a resize.
type ResizeDisposition int

// The dispositions of a resize.
const (
	ResizeAccepted ResizeDisposition = iota
	ResizeIgnored
	ResizeInvalid
	ResizeClosed
)

// Started is what the host receives when the command is running.
type Started struct {
	Argv         []string
	Name, Path   string
	PID          uint32
	TTY          *bool
	Cols, Rows   uint16
	Term         string
	Transport    string
	Capabilities *execv2.Capabilities
}

// Result is the end of a command and its accounts.
type Result struct {
	End *execv2.End
	// Cause is the lifecycle's word for why the session ended: "finished", "requested", "shutdown", "retired", "expired",
	// "timeout" or "cancelled".
	Cause         string
	StartObserved bool
	Duration      time.Duration
	BytesOut      uint64
	BytesIn       uint64
	DroppedBytes  uint64
	LateWrites    uint64
	LateBytes     uint64
	Output        execv2.OutputCompleteness
	Abandoned     bool
	Violations    []string
}

// engineFactory holds the registered constructor, a func(targets []T, cfg EngineConfig) (Engine, error).
var engineFactory any

// SetEngineFactory registers the constructor of the in-process engine (pkg/v1/exec's), for the targets type T.
func SetEngineFactory[T any](factory func(targets []T, cfg EngineConfig) (Engine, error)) {
	engineFactory = factory
}

// NewEngine builds an in-process engine for targets. It panics when no constructor is registered for T, which means
// pkg/v1/exec is not linked into the program or registered another type: a bug of this module, which no author can cause.
func NewEngine[T any](targets []T, cfg EngineConfig) (Engine, error) {
	factory, ok := engineFactory.(func([]T, EngineConfig) (Engine, error))
	if !ok {
		panic("exechook: no engine factory is registered for this type: pkg/v1/exec is not linked")
	}
	return factory(targets, cfg)
}
