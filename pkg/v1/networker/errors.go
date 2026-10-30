// Package networker manages resource and static port-forward sessions.
// Resource forwards preserve caller cancellation through readiness and retain
// their admitted connection-generation lease for the accepted session lifetime.
package networker

import (
	"errors"
	"fmt"
)

// NetworkerErrorCode classifies networker errors for programmatic handling.
type NetworkerErrorCode int

const (
	// ErrCodeSessionNotFound identifies an unknown session ID.
	ErrCodeSessionNotFound NetworkerErrorCode = iota + 1
	// ErrCodeNoHandlerFound identifies a resource without a registered forwarder.
	ErrCodeNoHandlerFound
	// ErrCodePortUnavailable identifies a local port already in use.
	ErrCodePortUnavailable
	// ErrCodeForwarderFailed identifies a native tunnel startup or runtime failure.
	ErrCodeForwarderFailed
	// ErrCodeManagerShuttingDown rejects work after manager shutdown begins.
	ErrCodeManagerShuttingDown
	// ErrCodeInvalidStateTransition identifies an unsupported session state change.
	ErrCodeInvalidStateTransition
	// ErrCodeInvalidConnectionType identifies an unsupported connection kind.
	ErrCodeInvalidConnectionType
)

// String returns the stable uppercase error code, or UNKNOWN for an unrecognized code.
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
	case ErrCodeManagerShuttingDown:
		return "MANAGER_SHUTTING_DOWN"
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

// Error formats the code, optional session ID, and diagnostic message.
func (e *NetworkerError) Error() string {
	if e.SessionID != "" {
		return fmt.Sprintf("[%s] session %s: %s", e.Code, e.SessionID, e.Message)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// Unwrap returns the underlying forwarder error, if present.
func (e *NetworkerError) Unwrap() error { return e.Err }

// Is reports whether target matches this error's code.
func (e *NetworkerError) Is(target error) bool {
	var t *NetworkerError
	if errors.As(target, &t) {
		return e.Code == t.Code
	}
	return false
}

// Sentinel errors support code-based matching with [errors.Is].
var (
	ErrNetSessionNotFound        = &NetworkerError{Code: ErrCodeSessionNotFound}
	ErrNetNoHandlerFound         = &NetworkerError{Code: ErrCodeNoHandlerFound}
	ErrNetPortUnavailable        = &NetworkerError{Code: ErrCodePortUnavailable}
	ErrNetForwarderFailed        = &NetworkerError{Code: ErrCodeForwarderFailed}
	ErrNetManagerShuttingDown    = &NetworkerError{Code: ErrCodeManagerShuttingDown}
	ErrNetInvalidStateTransition = &NetworkerError{Code: ErrCodeInvalidStateTransition}
	ErrNetInvalidConnectionType  = &NetworkerError{Code: ErrCodeInvalidConnectionType}
)

// NewSessionNotFoundError identifies the session that could not be found.
func NewSessionNotFoundError(sessionID string) *NetworkerError {
	return &NetworkerError{
		Code:      ErrCodeSessionNotFound,
		SessionID: sessionID,
		Message:   "session not found",
	}
}

// NewNoHandlerFoundError identifies a resource key without a registered forwarder.
func NewNoHandlerFoundError(resourceKey string) *NetworkerError {
	return &NetworkerError{
		Code:    ErrCodeNoHandlerFound,
		Message: fmt.Sprintf("no handler found for resource %q", resourceKey),
	}
}

// NewPortUnavailableError identifies the local port that cannot be used.
func NewPortUnavailableError(port int32) *NetworkerError {
	return &NetworkerError{
		Code:    ErrCodePortUnavailable,
		Message: fmt.Sprintf("port %d is unavailable", port),
	}
}

// NewForwarderFailedError wraps a native failure and associates it with a session.
func NewForwarderFailedError(sessionID string, err error) *NetworkerError {
	return &NetworkerError{
		Code:      ErrCodeForwarderFailed,
		SessionID: sessionID,
		Message:   fmt.Sprintf("forwarder failed: %v", err),
		Err:       err,
	}
}

// NewManagerShuttingDownError reports that shutdown prevents work on the session.
func NewManagerShuttingDownError(sessionID string) *NetworkerError {
	return &NetworkerError{
		Code:      ErrCodeManagerShuttingDown,
		SessionID: sessionID,
		Message:   "manager is shutting down",
	}
}

// NewInvalidStateTransitionError describes the rejected session state change.
func NewInvalidStateTransitionError(sessionID string, from, to SessionState) *NetworkerError {
	return &NetworkerError{
		Code:      ErrCodeInvalidStateTransition,
		SessionID: sessionID,
		Message:   fmt.Sprintf("cannot transition from %s to %s", from, to),
	}
}

// NewInvalidConnectionTypeError identifies an unsupported connection kind.
func NewInvalidConnectionTypeError(connectionType string) *NetworkerError {
	return &NetworkerError{
		Code:    ErrCodeInvalidConnectionType,
		Message: fmt.Sprintf("unsupported connection type %q", connectionType),
	}
}
