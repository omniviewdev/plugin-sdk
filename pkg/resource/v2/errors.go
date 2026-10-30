package resourcev2

import (
	"context"
	"encoding/json"
	"errors"
)

const (
	errorCodeInternal    = "INTERNAL"
	errorCodeUnsupported = "UNSUPPORTED"
)

// ResourceOperationError preserves provider classification across the resource
// transport. Title, Message, and Suggestions must contain public-safe text:
// never include credentials, private configuration, or raw native diagnostics.
// Put native diagnostics in Err, which is excluded from JSON and exposed only
// through Unwrap. Error serializes classification and public text, not Err.
// The host still owns redaction, canonical AppError provenance, and recovery
// policy; provider text and classification never grant permission.
type ResourceOperationError struct {
	Err         error    `json:"-"`
	Code        string   `json:"code"`
	Title       string   `json:"title"`
	Message     string   `json:"message"`
	Suggestions []string `json:"suggestions,omitempty"`
}

// Error returns JSON containing public classification and presentation only.
// A nil receiver returns an INTERNAL classification without native diagnostics.
func (e *ResourceOperationError) Error() string {
	if e == nil {
		return `{"code":"INTERNAL","title":"","message":""}`
	}
	b, _ := json.Marshal(struct {
		Code        string   `json:"code"`
		Title       string   `json:"title"`
		Message     string   `json:"message"`
		Suggestions []string `json:"suggestions,omitempty"`
	}{e.Code, e.Title, e.Message, e.Suggestions})
	return string(b)
}

// Unwrap exposes the native cause for local inspection; a nil receiver has no cause.
func (e *ResourceOperationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// NewError constructs a classified error whose Message is detail. Both code and
// detail are provider assertions, not host authority. Use a reviewed IsErrorCode
// code and public-safe detail; attach raw native diagnostics through
// ResourceOperationError.Err instead. This function does not validate or redact
// detail, and host redaction remains authoritative.
func NewError(code, detail string) error { return &ResourceOperationError{Code: code, Message: detail} }

// ErrorCode finds a reviewed classification through wrapping.
// It maps cancellation and deadlines explicitly, nil to empty, and unknown errors to INTERNAL.
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "CANCELLED"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "TIMEOUT"
	}
	var classified *ResourceOperationError
	if errors.As(err, &classified) && classified != nil && IsErrorCode(classified.Code) {
		return classified.Code
	}
	return errorCodeInternal
}

// ErrorClassifier can decline an error by returning nil.
type ErrorClassifier interface{ ClassifyError(error) error }

// IsErrorCode accepts the existing C02 provider-classification vocabulary.
// Unknown peer/provider codes never widen the host's canonical error contract.
func IsErrorCode(code string) bool {
	switch code {
	case "NOT_FOUND", "ALREADY_EXISTS", "PERMISSION_DENIED", "INVALID_INPUT",
		"CONFLICT", errorCodeInternal, "UNAVAILABLE", "TIMEOUT", "FILTER_UNKNOWN_FIELD",
		"FILTER_INVALID_OPERATOR", "FORBIDDEN", "UNAUTHORIZED", "CONNECTION_ERROR",
		"CERTIFICATE_ERROR", "INVALID_REQUEST", errorCodeUnsupported, "CANCELLED",
		"RESOURCE_LIMIT", "CONTINUATION_EXPIRED", "RESET_REQUIRED", "PARTIAL_RESULT",
		"PROVIDER_ERROR":
		return true
	default:
		return false
	}
}
