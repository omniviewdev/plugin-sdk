package logs

import (
	"testing"

	logspb "github.com/omniviewdev/plugin-sdk/proto/v1/logs"
)

// SS-001: LogSessionStatus.ToProto() round-trips CONNECTING, INITIALIZING
func TestLogSessionStatusNewValues(t *testing.T) {
	tests := []struct {
		name  string
		go_   LogSessionStatus
		proto logspb.LogSessionStatus
	}{
		{"CONNECTING", LogSessionStatusConnecting, logspb.LogSessionStatus_LOG_SESSION_STATUS_CONNECTING},
		{"INITIALIZING", LogSessionStatusInitializing, logspb.LogSessionStatus_LOG_SESSION_STATUS_INITIALIZING},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.go_.ToProto()
			if got != tc.proto {
				t.Errorf("ToProto() = %v, want %v", got, tc.proto)
			}
			back := LogSessionStatusFromProto(got)
			if back != tc.go_ {
				t.Errorf("FromProto(%v) = %d, want %d", got, back, tc.go_)
			}
		})
	}
}

// SS-002: LogSessionStatus.FromProto() handles unknown values gracefully
func TestLogSessionStatusFromProtoUnknown(t *testing.T) {
	got := LogSessionStatusFromProto(logspb.LogSessionStatus(99))
	if got != LogSessionStatusActive {
		t.Errorf("FromProto(99) = %d, want LogSessionStatusActive", got)
	}
}

// SS-003: All existing status values still convert correctly (regression)
func TestLogSessionStatusExistingValuesRegression(t *testing.T) {
	tests := []struct {
		name  string
		go_   LogSessionStatus
		proto logspb.LogSessionStatus
	}{
		{"ACTIVE", LogSessionStatusActive, logspb.LogSessionStatus_LOG_SESSION_STATUS_ACTIVE},
		{"PAUSED", LogSessionStatusPaused, logspb.LogSessionStatus_LOG_SESSION_STATUS_PAUSED},
		{"CLOSED", LogSessionStatusClosed, logspb.LogSessionStatus_LOG_SESSION_STATUS_CLOSED},
		{"ERROR", LogSessionStatusError, logspb.LogSessionStatus_LOG_SESSION_STATUS_ERROR},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.go_.ToProto()
			if got != tc.proto {
				t.Errorf("ToProto() = %v, want %v", got, tc.proto)
			}
			back := LogSessionStatusFromProto(got)
			if back != tc.go_ {
				t.Errorf("FromProto(%v) = %d, want %d", got, back, tc.go_)
			}
		})
	}
}
