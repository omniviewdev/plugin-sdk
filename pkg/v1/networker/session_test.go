package networker_test

import (
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/networker"
)

func TestCanTransition_ValidPaths(t *testing.T) {
	valid := []struct {
		from networker.SessionState
		to   networker.SessionState
	}{
		{networker.SessionStateActive, networker.SessionStatePaused},
		{networker.SessionStateActive, networker.SessionStateStopped},
		{networker.SessionStateActive, networker.SessionStateFailed},
		{networker.SessionStatePaused, networker.SessionStateActive},
		{networker.SessionStatePaused, networker.SessionStateStopped},
		{networker.SessionStatePaused, networker.SessionStateFailed},
	}
	for _, tt := range valid {
		if !networker.CanTransition(tt.from, tt.to) {
			t.Errorf("expected valid transition %s → %s", tt.from, tt.to)
		}
	}
}

func TestCanTransition_InvalidPaths(t *testing.T) {
	invalid := []struct {
		from networker.SessionState
		to   networker.SessionState
	}{
		{networker.SessionStateStopped, networker.SessionStateActive},
		{networker.SessionStateFailed, networker.SessionStateActive},
		{networker.SessionStateStopped, networker.SessionStatePaused},
		{networker.SessionStateFailed, networker.SessionStatePaused},
	}
	for _, tt := range invalid {
		if networker.CanTransition(tt.from, tt.to) {
			t.Errorf("expected invalid transition %s → %s", tt.from, tt.to)
		}
	}
}

func TestConnectionInterface_ResourceConnection(t *testing.T) {
	var c networker.Connection = networker.PortForwardResourceConnection{
		ResourceKey: "core::v1::Pod",
		ResourceID:  "my-pod",
	}
	_ = c // just verifying the interface is satisfied
}

func TestConnectionInterface_StaticConnection(t *testing.T) {
	var c networker.Connection = networker.PortForwardStaticConnection{
		Address: "localhost:8080",
	}
	_ = c
}
