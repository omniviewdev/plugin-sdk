package exec

import (
	"io"
	"os"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	execpb "github.com/omniviewdev/plugin-sdk/proto/v1/exec"
)

type StreamTarget int

const (
	StreamTargetStdOut StreamTarget = iota
	StreamTargetStdErr
)

func (t StreamTarget) String() string {
	switch t {
	case StreamTargetStdOut:
		return "stdout"
	case StreamTargetStdErr:
		return "stderr"
	}
	return "unknown"
}

type StreamSignal int

const (
	// while close isn't a signal, it's used to close the stream.
	StreamSignalNone StreamSignal = iota
	StreamSignalClose
	StreamSignalSigint
	StreamSignalSigquit
	StreamSignalSigterm
	StreamSignalSigkill
	StreamSignalSighup
	StreamSignalSigusr1
	StreamSignalSigusr2
	StreamSignalSigwinch
	StreamSignalError
)

func (s StreamSignal) String() string {
	switch s {
	case StreamSignalNone:
		return "NONE"
	case StreamSignalClose:
		return "CLOSE"
	case StreamSignalSigint:
		return "SIGINT"
	case StreamSignalSigquit:
		return "SIGQUIT"
	case StreamSignalSigterm:
		return "SIGTERM"
	case StreamSignalSigkill:
		return "SIGKILL"
	case StreamSignalSighup:
		return "SIGHUP"
	case StreamSignalSigusr1:
		return "SIGUSR1"
	case StreamSignalSigusr2:
		return "SIGUSR2"
	case StreamSignalSigwinch:
		return "SIGWINCH"
	case StreamSignalError:
		return "ERROR"
	}
	return "NONE"
}

// pointer receiver to allow nil signal (when none is actually sent).
func (s StreamSignal) ToProto() execpb.StreamSignal {
	switch s {
	case StreamSignalNone:
		return execpb.StreamSignal_STREAM_SIGNAL_NONE
	case StreamSignalClose:
		return execpb.StreamSignal_STREAM_SIGNAL_CLOSE
	case StreamSignalSigint:
		return execpb.StreamSignal_STREAM_SIGNAL_SIGINT
	case StreamSignalSigquit:
		return execpb.StreamSignal_STREAM_SIGNAL_SIGQUIT
	case StreamSignalSigterm:
		return execpb.StreamSignal_STREAM_SIGNAL_SIGTERM
	case StreamSignalSigkill:
		return execpb.StreamSignal_STREAM_SIGNAL_SIGKILL
	case StreamSignalSighup:
		return execpb.StreamSignal_STREAM_SIGNAL_SIGHUP
	case StreamSignalSigusr1:
		return execpb.StreamSignal_STREAM_SIGNAL_SIGUSR1
	case StreamSignalSigusr2:
		return execpb.StreamSignal_STREAM_SIGNAL_SIGUSR2
	case StreamSignalSigwinch:
		return execpb.StreamSignal_STREAM_SIGNAL_SIGWINCH
	case StreamSignalError:
		return execpb.StreamSignal_STREAM_SIGNAL_ERROR
	}
	return execpb.StreamSignal_STREAM_SIGNAL_NONE
}

func NewStreamSignalFromProto(p execpb.StreamSignal) StreamSignal {
	switch p {
	case execpb.StreamSignal_STREAM_SIGNAL_NONE:
		return StreamSignalNone
	case execpb.StreamSignal_STREAM_SIGNAL_CLOSE:
		return StreamSignalClose
	case execpb.StreamSignal_STREAM_SIGNAL_SIGINT:
		return StreamSignalSigint
	case execpb.StreamSignal_STREAM_SIGNAL_SIGQUIT:
		return StreamSignalSigquit
	case execpb.StreamSignal_STREAM_SIGNAL_SIGTERM:
		return StreamSignalSigterm
	case execpb.StreamSignal_STREAM_SIGNAL_SIGKILL:
		return StreamSignalSigkill
	case execpb.StreamSignal_STREAM_SIGNAL_SIGHUP:
		return StreamSignalSighup
	case execpb.StreamSignal_STREAM_SIGNAL_SIGUSR1:
		return StreamSignalSigusr1
	case execpb.StreamSignal_STREAM_SIGNAL_SIGUSR2:
		return StreamSignalSigusr2
	case execpb.StreamSignal_STREAM_SIGNAL_SIGWINCH:
		return StreamSignalSigwinch
	case execpb.StreamSignal_STREAM_SIGNAL_ERROR:
		return StreamSignalError
	}
	return StreamSignalNone
}

// StreamError contains structured error information from a failed exec session.
type StreamError struct {
	Title         string   `json:"title"`
	Message       string   `json:"message"`
	Suggestion    string   `json:"suggestion"`
	Retryable     bool     `json:"retryable"`
	RetryCommands []string `json:"retry_commands,omitempty"`
}

// ExecError is a structured error type that plugins use to provide
// classified exec errors with user-facing information.
type ExecError struct {
	Err           error
	Title         string
	Message       string
	Suggestion    string
	Retryable     bool
	RetryCommands []string
}

func (e *ExecError) Error() string { return e.Err.Error() }
func (e *ExecError) Unwrap() error { return e.Err }

type StreamOutput struct {
	SessionID string       `json:"session_id"`
	Data      []byte       `json:"data"`
	Target    StreamTarget `json:"target"`
	Signal    StreamSignal `json:"signal"`
	Error     *StreamError `json:"error,omitempty"`
}

func (e *StreamError) ToProto() *execpb.StreamError {
	if e == nil {
		return nil
	}
	return &execpb.StreamError{
		Title:         e.Title,
		Message:       e.Message,
		Suggestion:    e.Suggestion,
		Retryable:     e.Retryable,
		RetryCommands: e.RetryCommands,
	}
}

func NewStreamErrorFromProto(p *execpb.StreamError) *StreamError {
	if p == nil {
		return nil
	}
	return &StreamError{
		Title:         p.GetTitle(),
		Message:       p.GetMessage(),
		Suggestion:    p.GetSuggestion(),
		Retryable:     p.GetRetryable(),
		RetryCommands: p.GetRetryCommands(),
	}
}

func (o *StreamOutput) ToProto() *execpb.StreamOutput {
	var target execpb.StreamOutput_Target
	switch o.Target {
	case StreamTargetStdOut:
		target = execpb.StreamOutput_STDOUT
	case StreamTargetStdErr:
		target = execpb.StreamOutput_STDERR
	default:
		return nil
	}
	return &execpb.StreamOutput{
		Id:     o.SessionID,
		Target: target,
		Data:   o.Data,
		Signal: o.Signal.ToProto(),
		Error:  o.Error.ToProto(),
	}
}

func NewStreamOutputFromProto(p *execpb.StreamOutput) StreamOutput {
	var target StreamTarget
	switch p.GetTarget() {
	case execpb.StreamOutput_STDOUT:
		target = StreamTargetStdOut
	case execpb.StreamOutput_STDERR:
		target = StreamTargetStdErr
	}
	return StreamOutput{
		SessionID: p.GetId(),
		Target:    target,
		Data:      p.GetData(),
		Signal:    NewStreamSignalFromProto(p.GetSignal()),
		Error:     NewStreamErrorFromProto(p.GetError()),
	}
}

type StreamInput struct {
	// SessionID
	SessionID string `json:"session_id"`
	// Data
	Data []byte `json:"data"`
}

func (i *StreamInput) ToProto() *execpb.StreamInput {
	return &execpb.StreamInput{
		Id:   i.SessionID,
		Data: i.Data,
	}
}

func NewStreamInputFromProto(p *execpb.StreamInput) StreamInput {
	return StreamInput{
		SessionID: p.GetId(),
		Data:      p.GetData(),
	}
}

type StreamResize struct {
	SessionID string `json:"session_id"`
	Cols      uint16 `json:"cols"`
	Rows      uint16 `json:"rows"`
}

func (r *StreamResize) ToProto() *execpb.ResizeSessionRequest {
	return &execpb.ResizeSessionRequest{
		Id:   r.SessionID,
		Cols: int32(r.Cols),
		Rows: int32(r.Rows),
	}
}

func NewStreamResizeFromProto(p *execpb.ResizeSessionRequest) StreamResize {
	return StreamResize{
		SessionID: p.GetId(),
		Cols:      uint16(p.GetCols()),
		Rows:      uint16(p.GetRows()),
	}
}

// SessionHandler is the expected signature for a function that creates a new session.
//
// The session handler should start a new session against the given resource and return
// the standard input, output, and error streams which will be multiplexed to the client.
type SessionHandler func(ctx *types.PluginContext, opts SessionOptions) (
	stdin io.Writer,
	stdout io.Reader,
	stderr io.Reader,
	err error,
)

// TTYHandler is the expected signature for a function that creates a new session with a TTY.
// It is passed the TTY file descriptor for the session, and a resize channel that will receive
// resize events for the TTY, of which.
type TTYHandler func(ctx *types.PluginContext, opts SessionOptions, tty *os.File, stopCh chan error, resize <-chan SessionResizeInput) error

// CommandHandler is the expected signature for the non-tty handler. It should immediately return it's
// standard output and error.
type CommandHandler func(ctx *types.PluginContext, opts SessionOptions) (
	stdout io.Reader,
	stderr io.Reader,
	err error,
)
