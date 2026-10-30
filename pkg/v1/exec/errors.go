package exec

import (
	"errors"
	"fmt"
)

// ErrorCode classifies exec errors for programmatic handling.
type ErrorCode int

const (
	// ErrCodeSessionNotFound identifies an unknown session ID.
	ErrCodeSessionNotFound ErrorCode = iota + 1
	// ErrCodeHandlerNotFound identifies an unsupported handler key.
	ErrCodeHandlerNotFound
	// ErrCodeSessionClosed identifies a session that no longer accepts work.
	ErrCodeSessionClosed
	// ErrCodeTerminalError identifies a terminal operation failure.
	ErrCodeTerminalError
	// ErrCodeSessionExists identifies a duplicate session ID.
	ErrCodeSessionExists
)

// String returns the stable error classification, or UNKNOWN for an invalid code.
func (c ErrorCode) String() string {
	switch c {
	case ErrCodeSessionNotFound:
		return "SESSION_NOT_FOUND"
	case ErrCodeHandlerNotFound:
		return "HANDLER_NOT_FOUND"
	case ErrCodeSessionClosed:
		return "SESSION_CLOSED"
	case ErrCodeTerminalError:
		return "TERMINAL_ERROR"
	case ErrCodeSessionExists:
		return "SESSION_EXISTS"
	default:
		return "UNKNOWN"
	}
}

// ExecSessionError is a structured error for exec operations.
type ExecSessionError struct {
	Code      ErrorCode
	SessionID string
	Message   string
	Err       error // optional wrapped error
}

// Error formats the classification and optional session ID; a nil receiver is safe.
func (e *ExecSessionError) Error() string {
	if e == nil {
		return "<nil ExecSessionError>"
	}
	if e.SessionID != "" {
		return fmt.Sprintf("[%s] session %s: %s", e.Code, e.SessionID, e.Message)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// Unwrap returns the underlying failure, or nil for a nil receiver.
func (e *ExecSessionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Is reports whether target matches this error's code.
func (e *ExecSessionError) Is(target error) bool {
	if e == nil {
		return false
	}
	var t *ExecSessionError
	if errors.As(target, &t) {
		return e.Code == t.Code
	}
	return false
}

// Sentinel errors support classification with [errors.Is].
var (
	ErrSessionNotFound = &ExecSessionError{Code: ErrCodeSessionNotFound}
	ErrHandlerNotFound = &ExecSessionError{Code: ErrCodeHandlerNotFound}
	ErrSessionClosed   = &ExecSessionError{Code: ErrCodeSessionClosed}
	ErrTerminalError   = &ExecSessionError{Code: ErrCodeTerminalError}
	ErrSessionExists   = &ExecSessionError{Code: ErrCodeSessionExists}
)

// NewSessionNotFoundError records the session ID that could not be found.
func NewSessionNotFoundError(sessionID string) *ExecSessionError {
	return &ExecSessionError{
		Code:      ErrCodeSessionNotFound,
		SessionID: sessionID,
		Message:   "session not found",
	}
}

// NewHandlerNotFoundError reports the requested key and available handlers.
func NewHandlerNotFoundError(handlerKey string, available []string) *ExecSessionError {
	return &ExecSessionError{
		Code:    ErrCodeHandlerNotFound,
		Message: fmt.Sprintf("no handler for %q (available: %v)", handlerKey, available),
	}
}

// NewSessionClosedError records a session that no longer accepts work.
func NewSessionClosedError(sessionID string) *ExecSessionError {
	return &ExecSessionError{
		Code:      ErrCodeSessionClosed,
		SessionID: sessionID,
		Message:   "session is closed",
	}
}

// NewTerminalError wraps err while retaining the affected session ID.
func NewTerminalError(sessionID string, err error) *ExecSessionError {
	return &ExecSessionError{
		Code:      ErrCodeTerminalError,
		SessionID: sessionID,
		Message:   fmt.Sprintf("terminal error: %v", err),
		Err:       err,
	}
}

// NewSessionExistsError reports an attempt to reuse a live session ID.
func NewSessionExistsError(sessionID string) *ExecSessionError {
	return &ExecSessionError{
		Code:      ErrCodeSessionExists,
		SessionID: sessionID,
		Message:   "session already exists",
	}
}
