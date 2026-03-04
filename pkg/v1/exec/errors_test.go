package exec_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
)

func TestExecSessionError_Is(t *testing.T) {
	err := exec.NewSessionNotFoundError("abc")

	if !errors.Is(err, exec.ErrSessionNotFound) {
		t.Fatal("expected errors.Is to match ErrSessionNotFound")
	}
	if errors.Is(err, exec.ErrHandlerNotFound) {
		t.Fatal("expected errors.Is NOT to match ErrHandlerNotFound")
	}
}

func TestExecSessionError_Unwrap(t *testing.T) {
	inner := fmt.Errorf("broken pipe")
	err := exec.NewTerminalError("sess-1", inner)

	if !errors.Is(err, inner) {
		t.Fatal("expected Unwrap to expose the inner error")
	}
}

func TestExecSessionError_Error_WithSessionID(t *testing.T) {
	err := exec.NewSessionNotFoundError("sess-42")
	got := err.Error()
	if got != "[SESSION_NOT_FOUND] session sess-42: session not found" {
		t.Fatalf("unexpected error string: %s", got)
	}
}

func TestExecSessionError_Error_WithoutSessionID(t *testing.T) {
	err := exec.NewHandlerNotFoundError("plugin/resource", []string{"a/b", "c/d"})
	got := err.Error()
	expected := `[HANDLER_NOT_FOUND] no handler for "plugin/resource" (available: [a/b c/d])`
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestErrorCode_String(t *testing.T) {
	tests := []struct {
		code exec.ErrorCode
		want string
	}{
		{exec.ErrCodeSessionNotFound, "SESSION_NOT_FOUND"},
		{exec.ErrCodeHandlerNotFound, "HANDLER_NOT_FOUND"},
		{exec.ErrCodeSessionClosed, "SESSION_CLOSED"},
		{exec.ErrCodeTerminalError, "TERMINAL_ERROR"},
		{exec.ErrCodeSessionExists, "SESSION_EXISTS"},
		{exec.ErrorCode(999), "UNKNOWN"},
	}
	for _, tt := range tests {
		if got := tt.code.String(); got != tt.want {
			t.Errorf("ErrorCode(%d).String() = %q, want %q", tt.code, got, tt.want)
		}
	}
}
