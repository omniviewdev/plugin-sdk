package resourcev2

import (
	"context"
	"encoding/json"
	"errors"
)

// ResourceOperationError preserves provider classification across the resource
// transport. The host converts it into its canonical AppError with host-owned
// provenance; provider titles, messages and suggestions are never trusted grants.
type ResourceOperationError struct {
	Err         error    `json:"-"`
	Code        string   `json:"code"`
	Title       string   `json:"title"`
	Message     string   `json:"message"`
	Suggestions []string `json:"suggestions,omitempty"`
}

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
func (e *ResourceOperationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
func NewError(code, detail string) error { return &ResourceOperationError{Code: code, Message: detail} }
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
	return "INTERNAL"
}

// ErrorClassifier can decline an error by returning nil.
type ErrorClassifier interface{ ClassifyError(error) error }

// IsErrorCode accepts the existing C02 provider-classification vocabulary.
// Unknown peer/provider codes never widen the host's canonical error contract.
func IsErrorCode(code string) bool {
	switch code {
	case "NOT_FOUND", "ALREADY_EXISTS", "PERMISSION_DENIED", "INVALID_INPUT",
		"CONFLICT", "INTERNAL", "UNAVAILABLE", "TIMEOUT", "FILTER_UNKNOWN_FIELD",
		"FILTER_INVALID_OPERATOR", "FORBIDDEN", "UNAUTHORIZED", "CONNECTION_ERROR",
		"CERTIFICATE_ERROR", "INVALID_REQUEST", "UNSUPPORTED", "CANCELLED",
		"RESOURCE_LIMIT", "CONTINUATION_EXPIRED", "RESET_REQUIRED", "PARTIAL_RESULT",
		"PROVIDER_ERROR":
		return true
	default:
		return false
	}
}
