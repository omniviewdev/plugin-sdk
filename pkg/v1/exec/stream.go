package exec

import (
	"fmt"
	"math"

	execpb "github.com/omniviewdev/plugin-sdk/proto/v1/exec"
)

// StreamTarget identifies which process output channel carries a chunk.
type StreamTarget int

const (
	// StreamTargetStdOut selects standard output and is the zero value.
	StreamTargetStdOut StreamTarget = iota
	// StreamTargetStdErr selects standard error.
	StreamTargetStdErr
)

// String returns the channel name, or unknown for an invalid target.
func (t StreamTarget) String() string {
	switch t {
	case StreamTargetStdOut:
		return "stdout"
	case StreamTargetStdErr:
		return "stderr"
	}
	return "unknown"
}

// StreamSignal identifies process signals and stream lifecycle notifications.
type StreamSignal int

const (
	// StreamSignalNone carries data without a signal.
	StreamSignalNone StreamSignal = iota
	// StreamSignalClose marks stream closure rather than a process signal.
	StreamSignalClose
	// StreamSignalSigint requests interrupt.
	StreamSignalSigint
	// StreamSignalSigquit requests quit.
	StreamSignalSigquit
	// StreamSignalSigterm requests termination.
	StreamSignalSigterm
	// StreamSignalSigkill requests immediate termination.
	StreamSignalSigkill
	// StreamSignalSighup requests hangup.
	StreamSignalSighup
	// StreamSignalSigusr1 carries the first user-defined signal.
	StreamSignalSigusr1
	// StreamSignalSigusr2 carries the second user-defined signal.
	StreamSignalSigusr2
	// StreamSignalSigwinch reports a terminal size change.
	StreamSignalSigwinch
	// StreamSignalError marks a structured session failure.
	StreamSignalError
)

// String returns the signal name, defaulting to NONE for unknown values.
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

// ToProto converts a StreamSignal value to its proto representation.
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

// NewStreamSignalFromProto decodes a wire signal, treating unknown values as none.
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

// Error returns the wrapped failure's text; Err must be nonnil.
func (e *ExecError) Error() string { return e.Err.Error() }

// Unwrap exposes the underlying failure for error classification.
func (e *ExecError) Unwrap() error { return e.Err }

// StreamOutput carries session data or a lifecycle event and its output sequence.
type StreamOutput struct {
	SessionID string       `json:"session_id"`
	Data      []byte       `json:"data"`
	Target    StreamTarget `json:"target"`
	Signal    StreamSignal `json:"signal"`
	Error     *StreamError `json:"error,omitempty"`
	Seq       uint64       `json:"seq"`
}

// ToProto encodes the error, preserving nil and sharing RetryCommands storage.
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

// NewStreamErrorFromProto decodes an error, preserving nil and sharing retry commands.
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

// ToProto encodes output, sharing Data storage; nil yields an empty message.
func (o *StreamOutput) ToProto() *execpb.StreamOutput {
	if o == nil {
		return &execpb.StreamOutput{}
	}
	var target execpb.StreamOutput_Target
	switch o.Target {
	case StreamTargetStdOut:
		target = execpb.StreamOutput_STDOUT
	case StreamTargetStdErr:
		target = execpb.StreamOutput_STDERR
	default:
		target = execpb.StreamOutput_STDOUT
	}
	return &execpb.StreamOutput{
		Id:     o.SessionID,
		Target: target,
		Data:   o.Data,
		Signal: o.Signal.ToProto(),
		Error:  o.Error.ToProto(),
		Seq:    o.Seq,
	}
}

// NewStreamOutputFromProto decodes output, sharing Data; nil yields the zero value.
func NewStreamOutputFromProto(p *execpb.StreamOutput) StreamOutput {
	if p == nil {
		return StreamOutput{}
	}
	var target StreamTarget
	switch p.GetTarget() {
	case execpb.StreamOutput_STDOUT:
		target = StreamTargetStdOut
	case execpb.StreamOutput_STDERR:
		target = StreamTargetStdErr
	default:
		target = StreamTargetStdOut // default to stdout for forward compatibility
	}
	return StreamOutput{
		SessionID: p.GetId(),
		Target:    target,
		Data:      p.GetData(),
		Signal:    NewStreamSignalFromProto(p.GetSignal()),
		Error:     NewStreamErrorFromProto(p.GetError()),
		Seq:       p.GetSeq(),
	}
}

// StreamInput carries bytes to write to a session's terminal.
type StreamInput struct {
	// SessionID
	SessionID string `json:"session_id"`
	// Data
	Data []byte `json:"data"`
}

// ToProto encodes input, sharing Data storage; nil yields an empty message.
func (i *StreamInput) ToProto() *execpb.StreamInput {
	if i == nil {
		return &execpb.StreamInput{}
	}
	return &execpb.StreamInput{
		Id:   i.SessionID,
		Data: i.Data,
	}
}

// NewStreamInputFromProto decodes input, sharing Data; nil yields the zero value.
func NewStreamInputFromProto(p *execpb.StreamInput) StreamInput {
	if p == nil {
		return StreamInput{}
	}
	return StreamInput{
		SessionID: p.GetId(),
		Data:      p.GetData(),
	}
}

// StreamResize requests nonzero terminal dimensions measured in characters.
type StreamResize struct {
	SessionID string `json:"session_id"`
	Cols      uint16 `json:"cols"`
	Rows      uint16 `json:"rows"`
}

// ToProto encodes dimensions; nil yields an empty request.
func (r *StreamResize) ToProto() *execpb.ResizeSessionRequest {
	if r == nil {
		return &execpb.ResizeSessionRequest{}
	}
	return &execpb.ResizeSessionRequest{
		Id:   r.SessionID,
		Cols: int32(r.Cols),
		Rows: int32(r.Rows),
	}
}

// NewStreamResizeFromProto rejects nil requests and dimensions outside 1..65535.
func NewStreamResizeFromProto(p *execpb.ResizeSessionRequest) (StreamResize, error) {
	if p == nil {
		return StreamResize{}, fmt.Errorf("nil ResizeSessionRequest")
	}
	cols := p.GetCols()
	rows := p.GetRows()
	if cols == 0 || rows == 0 {
		return StreamResize{}, fmt.Errorf("resize dimensions must be non-zero: cols=%d rows=%d", cols, rows)
	}
	if cols < 0 || cols > math.MaxUint16 || rows < 0 || rows > math.MaxUint16 {
		return StreamResize{}, fmt.Errorf("resize dimensions out of range: cols=%d rows=%d", cols, rows)
	}
	return StreamResize{
		SessionID: p.GetId(),
		Cols:      uint16(cols),
		Rows:      uint16(rows),
	}, nil
}
