package networker_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/networker"
)

func TestNetworkerError_Is(t *testing.T) {
	err := networker.NewSessionNotFoundError("abc")

	if !errors.Is(err, networker.ErrNetSessionNotFound) {
		t.Fatal("expected errors.Is to match ErrNetSessionNotFound")
	}
	if errors.Is(err, networker.ErrNetNoHandlerFound) {
		t.Fatal("expected errors.Is NOT to match ErrNetNoHandlerFound")
	}
}

func TestNetworkerError_Unwrap(t *testing.T) {
	inner := fmt.Errorf("connection reset")
	err := networker.NewForwarderFailedError("sess-1", inner)

	if !errors.Is(err, inner) {
		t.Fatal("expected Unwrap to expose the inner error")
	}
}

func TestNetworkerError_Error_WithSessionID(t *testing.T) {
	err := networker.NewSessionNotFoundError("sess-42")
	got := err.Error()
	expected := "[SESSION_NOT_FOUND] session sess-42: session not found"
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestNetworkerError_Error_WithoutSessionID(t *testing.T) {
	err := networker.NewNoHandlerFoundError("core::v1::Pod")
	got := err.Error()
	expected := `[NO_HANDLER_FOUND] no handler found for resource "core::v1::Pod"`
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestNetworkerErrorCode_String(t *testing.T) {
	tests := []struct {
		code networker.NetworkerErrorCode
		want string
	}{
		{networker.ErrCodeSessionNotFound, "SESSION_NOT_FOUND"},
		{networker.ErrCodeNoHandlerFound, "NO_HANDLER_FOUND"},
		{networker.ErrCodePortUnavailable, "PORT_UNAVAILABLE"},
		{networker.ErrCodeForwarderFailed, "FORWARDER_FAILED"},
		{networker.ErrCodeManagerShuttingDown, "MANAGER_SHUTTING_DOWN"},
		{networker.ErrCodeInvalidStateTransition, "INVALID_STATE_TRANSITION"},
		{networker.ErrCodeInvalidConnectionType, "INVALID_CONNECTION_TYPE"},
		{networker.NetworkerErrorCode(999), "UNKNOWN"},
	}
	for _, tt := range tests {
		if got := tt.code.String(); got != tt.want {
			t.Errorf("NetworkerErrorCode(%d).String() = %q, want %q", tt.code, got, tt.want)
		}
	}
}
