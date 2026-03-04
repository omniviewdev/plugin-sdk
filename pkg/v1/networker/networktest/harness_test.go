package networktest_test

import (
	"errors"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/networker"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/networker/networktest"
)

func TestHarness_MountAndStartSession(t *testing.T) {
	forwarder := &networktest.StubResourceForwarder{}
	h := networktest.Mount(t,
		networktest.WithResourceForwarder("core::v1::Pod", forwarder),
	)

	sess, err := h.StartSession(networker.PortForwardSessionOptions{
		ConnectionType: networker.PortForwardConnectionTypeResource,
		Connection: networker.PortForwardResourceConnection{
			ResourceKey: "core::v1::Pod",
		},
		RemotePort: 8080,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if sess.ID == "" {
		t.Fatal("expected non-empty session ID")
	}
}

func TestHarness_CloseSession(t *testing.T) {
	forwarder := &networktest.StubResourceForwarder{}
	h := networktest.Mount(t,
		networktest.WithResourceForwarder("core::v1::Pod", forwarder),
	)

	sess, err := h.StartSession(networker.PortForwardSessionOptions{
		ConnectionType: networker.PortForwardConnectionTypeResource,
		Connection: networker.PortForwardResourceConnection{
			ResourceKey: "core::v1::Pod",
		},
		RemotePort: 8080,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	closed, err := h.CloseSession(sess.ID)
	if err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	if closed.State != networker.SessionStateStopped {
		t.Fatalf("expected STOPPED, got %s", closed.State)
	}
}

func TestFakePortChecker_BlockPort(t *testing.T) {
	pc := networktest.NewFakePortChecker(10000)
	pc.BlockPort(8080)

	if !pc.IsPortUnavailable(8080) {
		t.Fatal("expected port 8080 to be unavailable")
	}
	if pc.IsPortUnavailable(9090) {
		t.Fatal("expected port 9090 to be available")
	}

	port, err := pc.FindFreePort()
	if err != nil {
		t.Fatalf("FindFreePort: %v", err)
	}
	if port != 10000 {
		t.Fatalf("expected port 10000, got %d", port)
	}
}

func TestStubForwarder_FailWith(t *testing.T) {
	rootCause := errors.New("test error")
	forwarder := &networktest.StubResourceForwarder{
		FailWith: rootCause,
	}
	h := networktest.Mount(t,
		networktest.WithResourceForwarder("core::v1::Pod", forwarder),
	)

	_, err := h.StartSession(networker.PortForwardSessionOptions{
		ConnectionType: networker.PortForwardConnectionTypeResource,
		Connection: networker.PortForwardResourceConnection{
			ResourceKey: "core::v1::Pod",
		},
		RemotePort: 8080,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, networker.ErrNetForwarderFailed) {
		t.Fatalf("expected ForwarderFailed, got: %v", err)
	}
	if !errors.Is(err, rootCause) {
		t.Fatalf("expected root cause to be preserved, got: %v", err)
	}
}
