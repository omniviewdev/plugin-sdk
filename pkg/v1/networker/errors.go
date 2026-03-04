package networker

import (
	"errors"
	"fmt"
)

// NetworkerErrorCode classifies networker errors for programmatic handling.
type NetworkerErrorCode int

const (
	ErrCodeSessionNotFound         NetworkerErrorCode = iota + 1
	ErrCodeNoHandlerFound
	ErrCodePortUnavailable
	ErrCodeForwarderFailed
	ErrCodeInvalidStateTransition
	ErrCodeInvalidConnectionType
)

func (c NetworkerErrorCode) String() string {
	switch c {
	case ErrCodeSessionNotFound:
		return "SESSION_NOT_FOUND"
	case ErrCodeNoHandlerFound:
		return "NO_HANDLER_FOUND"
	case ErrCodePortUnavailable:
		return "PORT_UNAVAILABLE"
	case ErrCodeForwarderFailed:
		return "FORWARDER_FAILED"
	case ErrCodeInvalidStateTransition:
		return "INVALID_STATE_TRANSITION"
	case ErrCodeInvalidConnectionType:
		return "INVALID_CONNECTION_TYPE"
	default:
		return "UNKNOWN"
	}
}

// NetworkerError is a structured error for networker operations.
type NetworkerError struct {
	Code      NetworkerErrorCode
	SessionID string
	Message   string
	Err       error // optional wrapped error
}

func (e *NetworkerError) Error() string {
	if e.SessionID != "" {
		return fmt.Sprintf("[%s] session %s: %s", e.Code, e.SessionID, e.Message)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

func (e *NetworkerError) Unwrap() error { return e.Err }

// Is reports whether target matches this error's code.
func (e *NetworkerError) Is(target error) bool {
	var t *NetworkerError
	if errors.As(target, &t) {
		return e.Code == t.Code
	}
	return false
}

// Sentinel errors for errors.Is matching.
var (
	ErrNetSessionNotFound         = &NetworkerError{Code: ErrCodeSessionNotFound}
	ErrNetNoHandlerFound          = &NetworkerError{Code: ErrCodeNoHandlerFound}
	ErrNetPortUnavailable         = &NetworkerError{Code: ErrCodePortUnavailable}
	ErrNetForwarderFailed         = &NetworkerError{Code: ErrCodeForwarderFailed}
	ErrNetInvalidStateTransition  = &NetworkerError{Code: ErrCodeInvalidStateTransition}
	ErrNetInvalidConnectionType   = &NetworkerError{Code: ErrCodeInvalidConnectionType}
)

// Constructor helpers.

func NewSessionNotFoundError(sessionID string) *NetworkerError {
	return &NetworkerError{
		Code:      ErrCodeSessionNotFound,
		SessionID: sessionID,
		Message:   "session not found",
	}
}

func NewNoHandlerFoundError(resourceKey string) *NetworkerError {
	return &NetworkerError{
		Code:    ErrCodeNoHandlerFound,
		Message: fmt.Sprintf("no handler found for resource %q", resourceKey),
	}
}

func NewPortUnavailableError(port int32) *NetworkerError {
	return &NetworkerError{
		Code:    ErrCodePortUnavailable,
		Message: fmt.Sprintf("port %d is unavailable", port),
	}
}

func NewForwarderFailedError(sessionID string, err error) *NetworkerError {
	return &NetworkerError{
		Code:      ErrCodeForwarderFailed,
		SessionID: sessionID,
		Message:   fmt.Sprintf("forwarder failed: %v", err),
		Err:       err,
	}
}

func NewInvalidStateTransitionError(sessionID string, from, to SessionState) *NetworkerError {
	return &NetworkerError{
		Code:      ErrCodeInvalidStateTransition,
		SessionID: sessionID,
		Message:   fmt.Sprintf("cannot transition from %s to %s", from, to),
	}
}

func NewInvalidConnectionTypeError(connectionType string) *NetworkerError {
	return &NetworkerError{
		Code:    ErrCodeInvalidConnectionType,
		Message: fmt.Sprintf("unsupported connection type %q", connectionType),
	}
}
