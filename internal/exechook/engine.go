package exechook

import (
	"context"
	"errors"
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
	// KillWait is how long a kill waits for the function to return before the engine gives up on it, and ShutdownGrace how
	// long each function has to return when the engine closes (instead of CloseGrace). Zero is the default.
	KillWait      time.Duration
	ShutdownGrace time.Duration
	// OptionsInFlight is how many Options callbacks of one target the unary Options RPC may have in flight, those the engine
	// gave up on included. Zero is the default.
	OptionsInFlight int
	// OptionsCacheTTL is how long the admission of an Open or a Run reuses the result of an Options callback for the same request
	// (the unary Options RPC always runs the callback and refreshes the result). Zero is the default (5 s); a negative value turns
	// the cache off.
	OptionsCacheTTL time.Duration
	// TombstoneTTL is how long an ended session stays listed and readable and keeps its id, MaxTombstones how many ended
	// sessions the engine remembers, and TombstoneBytes how many bytes of their output it keeps in all. Zero is the default.
	TombstoneTTL   time.Duration
	MaxTombstones  int
	TombstoneBytes int
	// Now is the clock of the engine's lifetimes: how long an ended session's tombstone holds its id and its output, when a lease
	// ends, the activity time of a session. Nil is the wall clock. A test that gives one moves it, and the tombstones expire as
	// it passes: the engine's timers (the lease's, the grace's) are not the clock's and stay real.
	Now func() time.Time
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
	// Attach attaches a stream to a session that lives, as a served Open{attach} does: the request is admitted as the served
	// one is (strict, a session id, an attach), the session is found and its older stream, if it has one, is superseded: the
	// engine's attach, not another. The stream it returns reads the replay of the ring from the offset, which begins no earlier
	// than it, and then the live output; a refusal is the *exec.Error the served Open sends as a status (NOT_FOUND /
	// SESSION_UNKNOWN for a session that does not live, INVALID_REQUEST for an offset the session has not reached).
	Attach(ctx context.Context, req *execv2.OpenRequest) (Stream, error)
	// ListSessions is the served ListSessions: the live sessions and the tombstones, by id.
	ListSessions(ctx context.Context) ([]*execv2.SessionInfo, error)
	// ReadOutput is the served ReadOutput: a cursor read of the ring of a session, live or tombstoned.
	ReadOutput(ctx context.Context, req *execv2.ReadOutputRequest) (*execv2.ReadOutputResponse, error)
	// EndSession is the served EndSession: it destroys a session that has no stream and returns once it has ended.
	EndSession(ctx context.Context, req *execv2.EndSessionRequest) error
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
	// Signal sends a signal to the function, as the session's consumer: the session takes it whether or not a stream is attached.
	Signal(sig execv2.Signal) SignalDisposition
	// Stream is the stream that started the session, which Read reads and which carries the frames a host's stream sends
	// (the ones that take effect only while the stream is the session's). It is nil for a run, which has no stream.
	Stream() Stream
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

// SignalDisposition is what became of a signal.
type SignalDisposition int

// The dispositions of a signal.
const (
	// SignalQueued: the signal is in the function's queue.
	SignalQueued SignalDisposition = iota
	// SignalUndeclared: the signal is not among the signals the session declares now. Served, a stream that sends one has broken
	// what it was told, and ends with the session.
	SignalUndeclared
	// SignalClosed: the command has ended and takes no signal.
	SignalClosed
)

// ErrStreamLeft is what a read of a Stream, and a frame sent on it, returns once the stream is no longer the session's: it
// detached, another stream took the session over, or it was released. The stream then has its last frame, Final.
var ErrStreamLeft = errors.New("exechook: the stream left its session")

// Replay is what the Opened of a stream says of the replay that follows: exactly the bytes [Start, End) of the output, and
// then the live stream, which begins at End. Start is no earlier than the offset the stream attached from, and is later when the
// ring no longer held it. A new session's replay is empty, at offset 0.
type Replay struct {
	Start, End, Bytes uint64
}

// Final is the last frame of a stream: Ended, when the session ended while the stream was attached to it, or Detached, when the
// stream left and the session lives on.
type Final struct {
	// Detached is false for Ended.
	Detached bool
	// Reason is why a Detached stream left: the host asked, another stream took the session over, or neither (it was released).
	Reason execv2.DetachReason
	// Lease is when the session ends if nobody attaches, for a stream that left by itself; zero for a superseded one.
	Lease time.Time
}

// Stream is one stream's attachment to a session: what an Open stream is, without the transport. A session has at most one
// stream that is its own at a time. The frames a stream sends take effect only while it is (a stream that another took the
// session from, or that left, sends frames that are ignored, and these return ErrStreamLeft); a frame is applied whole before the
// session changes hands, or not at all.
type Stream interface {
	// Opened is the Opened of the stream: the session's facts as of now and the description of its replay.
	Opened() *execv2.Opened
	// Replay is Opened's replay.
	Replay() Replay
	// Read waits for what the stream is to send: first the replay, then the live output, as the ranges lost since the last Read
	// and up to len(buf) bytes of one stream. It returns io.EOF once the session ended while the stream was attached and the
	// output was read to its end, ErrStreamLeft once the stream is no longer the session's, and ctx's error when ctx ends first.
	Read(ctx context.Context, buf []byte) (Chunk, error)
	// Final is the last frame of the stream, once Read has returned io.EOF or ErrStreamLeft.
	Final() Final
	// Write is an input frame, which waits while the function's input queue is full.
	Write(ctx context.Context, p []byte) (int, error)
	// CloseInput is a close_input frame.
	CloseInput() error
	// Resize is a resize frame.
	Resize(seq uint32, cols, rows uint16) (ResizeDisposition, error)
	// Signal is a signal frame.
	Signal(sig execv2.Signal) (SignalDisposition, error)
	// Detach is a Detach frame: the stream leaves and the session lives for the lease (zero is the session's own, and the
	// target's maximum is the most). It returns when the lease ends as applied, and false when the stream cannot leave: it is no
	// longer the session's, or the session is ending, which the stream is then told in its last frame.
	Detach(lease time.Duration) (expires time.Time, ok bool)
	// Close is a Close frame: it ends the session, if this stream is the session's, gracefully or with a kill.
	Close(kill bool)
	// Release is the host closing its side of the stream, or the stream breaking: an implicit detach, for the session's own lease. The stream
	// may still be read, and is told Detached with no reason; and when the session has ended already it is a no-op, and the stream reads
	// the tail of the output it was given.
	Release()
	// Leave is the end of the stream's handler, after which the stream is no longer read: it releases the stream, as Release does, and drops
	// what the stream still held of the replay it was given. Whoever reads the stream stops first.
	Leave()
}

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
