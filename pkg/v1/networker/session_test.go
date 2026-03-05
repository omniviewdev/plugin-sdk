package networker_test

import (
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/networker"
	networkerpb "github.com/omniviewdev/plugin-sdk/proto/v1/networker"
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

func TestPortForwardProtocolToProtoUnknown(t *testing.T) {
	got := networker.PortForwardProtocol("SCTP").ToProto()
	if got == networkerpb.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP || got == networkerpb.PortForwardProtocol_PORT_FORWARD_PROTOCOL_UDP {
		t.Fatalf("expected unknown proto enum for unknown protocol, got %v", got)
	}
	if int32(got) != -1 {
		t.Fatalf("expected unknown enum value -1, got %d", got)
	}
}

func TestPortForwardProtocolFromProtoUnknown(t *testing.T) {
	got := networker.PortForwardProtocolFromProto(networkerpb.PortForwardProtocol(99))
	if got.Valid() {
		t.Fatalf("expected invalid protocol for unknown enum, got %q", got)
	}
	if got != "" {
		t.Fatalf("expected empty protocol for unknown enum, got %q", got)
	}
}

func TestPortForwardSessionToProtoUnknownProtocol(t *testing.T) {
	session := &networker.PortForwardSession{
		Protocol: networker.PortForwardProtocol("SCTP"),
	}
	got := session.ToProto().GetProtocol()
	if int32(got) != -1 {
		t.Fatalf("expected unknown enum value -1, got %d", got)
	}
}
