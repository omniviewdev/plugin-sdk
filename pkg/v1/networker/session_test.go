package networker_test

import (
	"encoding/json"
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

func TestNewPortForwardSessionFromProtoNil(t *testing.T) {
	if got := networker.NewPortForwardSessionFromProto(nil); got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}

func TestNewPortForwardSessionOptionsFromProtoNil(t *testing.T) {
	if got := networker.NewPortForwardSessionOptionsFromProto(nil); got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}

func TestPortForwardSessionOptions_UnmarshalJSON_Resource(t *testing.T) {
	data := []byte(`{
		"connection": {
			"resource_data": {"apiVersion": "v1", "kind": "Service"},
			"connection_id": "kind-omniview-test",
			"plugin_id": "kubernetes",
			"resource_id": "eb043938-c5fd-426e-9012-f67d78e16fd1",
			"resource_key": "core::v1::Service"
		},
		"labels": {},
		"params": {},
		"protocol": "TCP",
		"connection_type": "RESOURCE",
		"local_port": 0,
		"remote_port": 80
	}`)

	var opts networker.PortForwardSessionOptions
	if err := json.Unmarshal(data, &opts); err != nil {
		t.Fatalf("UnmarshalJSON failed: %v", err)
	}

	if opts.ConnectionType != networker.PortForwardConnectionTypeResource {
		t.Fatalf("connection_type = %q, want RESOURCE", opts.ConnectionType)
	}
	if opts.RemotePort != 80 {
		t.Fatalf("remote_port = %d, want 80", opts.RemotePort)
	}
	if opts.Protocol != networker.PortForwardProtocolTCP {
		t.Fatalf("protocol = %q, want TCP", opts.Protocol)
	}
	if opts.Connection == nil {
		t.Fatal("Connection is nil, expected PortForwardResourceConnection")
	}
	rc, ok := opts.Connection.(*networker.PortForwardResourceConnection)
	if !ok {
		t.Fatalf("Connection type = %T, want *PortForwardResourceConnection", opts.Connection)
	}
	if rc.ConnectionID != "kind-omniview-test" {
		t.Fatalf("ConnectionID = %q, want kind-omniview-test", rc.ConnectionID)
	}
	if rc.PluginID != "kubernetes" {
		t.Fatalf("PluginID = %q, want kubernetes", rc.PluginID)
	}
	if rc.ResourceKey != "core::v1::Service" {
		t.Fatalf("ResourceKey = %q, want core::v1::Service", rc.ResourceKey)
	}
}

func TestPortForwardSessionOptions_UnmarshalJSON_Static(t *testing.T) {
	data := []byte(`{
		"connection": {"address": "10.0.0.1:3306"},
		"protocol": "TCP",
		"connection_type": "STATIC",
		"local_port": 3306,
		"remote_port": 3306
	}`)

	var opts networker.PortForwardSessionOptions
	if err := json.Unmarshal(data, &opts); err != nil {
		t.Fatalf("UnmarshalJSON failed: %v", err)
	}

	if opts.Connection == nil {
		t.Fatal("Connection is nil, expected PortForwardStaticConnection")
	}
	sc, ok := opts.Connection.(*networker.PortForwardStaticConnection)
	if !ok {
		t.Fatalf("Connection type = %T, want *PortForwardStaticConnection", opts.Connection)
	}
	if sc.Address != "10.0.0.1:3306" {
		t.Fatalf("Address = %q, want 10.0.0.1:3306", sc.Address)
	}
}

func TestPortForwardSessionOptions_UnmarshalJSON_NoConnection(t *testing.T) {
	data := []byte(`{"protocol": "TCP", "connection_type": "RESOURCE", "remote_port": 80}`)

	var opts networker.PortForwardSessionOptions
	if err := json.Unmarshal(data, &opts); err != nil {
		t.Fatalf("UnmarshalJSON failed: %v", err)
	}
	if opts.Connection != nil {
		t.Fatalf("expected nil Connection, got %T", opts.Connection)
	}
}
