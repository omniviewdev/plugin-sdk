package networker_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/networker"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/networker/networktest"
)

func TestManager_StartSession_Success(t *testing.T) {
	forwarder := &networktest.StubResourceForwarder{}
	h := networktest.Mount(t,
		networktest.WithResourceForwarder("core::v1::Pod", forwarder),
	)

	sess, err := h.StartSession(networker.PortForwardSessionOptions{
		ConnectionType: networker.PortForwardConnectionTypeResource,
		Connection: networker.PortForwardResourceConnection{
			ResourceKey: "core::v1::Pod",
			ResourceID:  "my-pod",
		},
		RemotePort: 8080,
		Protocol:   networker.PortForwardProtocolTCP,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if sess.ID == "" {
		t.Fatal("expected non-empty session ID")
	}
	if sess.State != networker.SessionStateActive {
		t.Fatalf("expected ACTIVE, got %s", sess.State)
	}
	if forwarder.Calls() != 1 {
		t.Fatalf("expected 1 call, got %d", forwarder.Calls())
	}
}

func TestManager_StartSession_PortUnavailable(t *testing.T) {
	pc := networktest.NewFakePortChecker(10000)
	pc.BlockPort(8080)

	forwarder := &networktest.StubResourceForwarder{}
	h := networktest.Mount(t,
		networktest.WithResourceForwarder("core::v1::Pod", forwarder),
		networktest.WithPortChecker(pc),
	)

	_, err := h.StartSession(networker.PortForwardSessionOptions{
		ConnectionType: networker.PortForwardConnectionTypeResource,
		Protocol:       networker.PortForwardProtocolTCP,
		Connection: networker.PortForwardResourceConnection{
			ResourceKey: "core::v1::Pod",
		},
		LocalPort:  8080,
		RemotePort: 8080,
	})
	if !errors.Is(err, networker.ErrNetPortUnavailable) {
		t.Fatalf("expected PortUnavailable, got: %v", err)
	}
}

func TestManager_StartSession_NoHandler(t *testing.T) {
	h := networktest.Mount(t) // no forwarders registered

	_, err := h.StartSession(networker.PortForwardSessionOptions{
		ConnectionType: networker.PortForwardConnectionTypeResource,
		Protocol:       networker.PortForwardProtocolTCP,
		Connection: networker.PortForwardResourceConnection{
			ResourceKey: "core::v1::Pod",
		},
		RemotePort: 8080,
	})
	if !errors.Is(err, networker.ErrNetNoHandlerFound) {
		t.Fatalf("expected NoHandlerFound, got: %v", err)
	}
}

func TestManager_StartSession_ForwarderFails(t *testing.T) {
	forwarder := &networktest.StubResourceForwarder{
		FailWith: errors.New("connection refused"),
	}
	h := networktest.Mount(t,
		networktest.WithResourceForwarder("core::v1::Pod", forwarder),
	)

	_, err := h.StartSession(networker.PortForwardSessionOptions{
		ConnectionType: networker.PortForwardConnectionTypeResource,
		Protocol:       networker.PortForwardProtocolTCP,
		Connection: networker.PortForwardResourceConnection{
			ResourceKey: "core::v1::Pod",
		},
		RemotePort: 8080,
	})
	if err == nil {
		t.Fatal("expected error from failing forwarder")
	}
	if !errors.Is(err, networker.ErrNetForwarderFailed) {
		t.Fatalf("expected ForwarderFailed, got: %v", err)
	}
}

func TestManager_CloseSession_Success(t *testing.T) {
	forwarder := &networktest.StubResourceForwarder{}
	h := networktest.Mount(t,
		networktest.WithResourceForwarder("core::v1::Pod", forwarder),
	)

	sess, err := h.StartSession(networker.PortForwardSessionOptions{
		ConnectionType: networker.PortForwardConnectionTypeResource,
		Protocol:       networker.PortForwardProtocolTCP,
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

func TestManager_CloseSession_NotFound(t *testing.T) {
	h := networktest.Mount(t)
	_, err := h.CloseSession("nonexistent")
	if !errors.Is(err, networker.ErrNetSessionNotFound) {
		t.Fatalf("expected SessionNotFound, got: %v", err)
	}
}

func TestManager_StopAll_CleansUp(t *testing.T) {
	forwarder := &networktest.StubResourceForwarder{}
	h := networktest.Mount(t,
		networktest.WithResourceForwarder("core::v1::Pod", forwarder),
	)

	for i := 0; i < 3; i++ {
		_, err := h.StartSession(networker.PortForwardSessionOptions{
			ConnectionType: networker.PortForwardConnectionTypeResource,
			Protocol:       networker.PortForwardProtocolTCP,
			Connection: networker.PortForwardResourceConnection{
				ResourceKey: "core::v1::Pod",
			},
			RemotePort: 8080,
		})
		if err != nil {
			t.Fatalf("StartSession %d: %v", i, err)
		}
	}

	sessions, _ := h.ListSessions()
	if len(sessions) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(sessions))
	}

	h.Manager.StopAll()

	sessions, _ = h.ListSessions()
	if len(sessions) != 0 {
		t.Fatalf("expected 0 sessions after StopAll, got %d", len(sessions))
	}
}

func TestManager_GetSession_NotFound(t *testing.T) {
	h := networktest.Mount(t)
	_, err := h.GetSession("nonexistent")
	if !errors.Is(err, networker.ErrNetSessionNotFound) {
		t.Fatalf("expected SessionNotFound, got: %v", err)
	}
}

func TestManager_ListSessions_Empty(t *testing.T) {
	h := networktest.Mount(t)
	sessions, err := h.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected 0, got %d", len(sessions))
	}
}

func TestManager_FindSessions_ByResourceID(t *testing.T) {
	forwarder := &networktest.StubResourceForwarder{}
	h := networktest.Mount(t,
		networktest.WithResourceForwarder("core::v1::Pod", forwarder),
	)

	_, err := h.StartSession(networker.PortForwardSessionOptions{
		ConnectionType: networker.PortForwardConnectionTypeResource,
		Protocol:       networker.PortForwardProtocolTCP,
		Connection: networker.PortForwardResourceConnection{
			ResourceKey: "core::v1::Pod",
			ResourceID:  "pod-1",
		},
		RemotePort: 8080,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	_, err = h.StartSession(networker.PortForwardSessionOptions{
		ConnectionType: networker.PortForwardConnectionTypeResource,
		Protocol:       networker.PortForwardProtocolTCP,
		Connection: networker.PortForwardResourceConnection{
			ResourceKey: "core::v1::Pod",
			ResourceID:  "pod-2",
		},
		RemotePort: 9090,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	pctx := networktest.TestPluginCtx()
	found, err := h.Manager.FindPortForwardSessions(pctx, networker.FindPortForwardSessionRequest{
		ResourceID: "pod-1",
	})
	if err != nil {
		t.Fatalf("FindSessions: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("expected 1 session for pod-1, got %d", len(found))
	}
}

func TestManager_ConcurrentStartClose(t *testing.T) {
	forwarder := &networktest.StubResourceForwarder{}
	h := networktest.Mount(t,
		networktest.WithResourceForwarder("core::v1::Pod", forwarder),
	)

	const count = 10
	var wg sync.WaitGroup
	errs := make(chan error, count*2)

	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sess, err := h.StartSession(networker.PortForwardSessionOptions{
				ConnectionType: networker.PortForwardConnectionTypeResource,
				Protocol:       networker.PortForwardProtocolTCP,
				Connection: networker.PortForwardResourceConnection{
					ResourceKey: "core::v1::Pod",
				},
				RemotePort: 8080,
			})
			if err != nil {
				errs <- err
				return
			}
			// small delay to let monitor start
			time.Sleep(10 * time.Millisecond)
			if _, err := h.CloseSession(sess.ID); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent operation failed: %v", err)
	}
}

func TestManager_InvalidConnectionType(t *testing.T) {
	h := networktest.Mount(t)
	_, err := h.StartSession(networker.PortForwardSessionOptions{
		ConnectionType: "INVALID",
		Protocol:       networker.PortForwardProtocolTCP,
		Connection: networker.PortForwardResourceConnection{
			ResourceKey: "core::v1::Pod",
		},
	})
	if !errors.Is(err, networker.ErrNetInvalidConnectionType) {
		t.Fatalf("expected InvalidConnectionType, got: %v", err)
	}
}
