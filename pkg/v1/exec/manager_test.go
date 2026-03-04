package exec_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
)

func defaultHandler() exec.Handler {
	return exec.Handler{
		Plugin:   "test",
		Resource: "pod",
		TTYHandler: exectest.NoopTTYHandler(),
	}
}

func TestManager_SessionLifecycle(t *testing.T) {
	h := exectest.Mount(t, exectest.WithHandler(defaultHandler()))

	sess, err := h.CreateSession(exec.SessionOptions{
		ResourcePlugin: "test",
		ResourceKey:    "pod",
		Labels:         map[string]string{"env": "test"},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.ID == "" {
		t.Fatal("session ID should not be empty")
	}

	// GetSession
	got, err := h.GetSession(sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.ID != sess.ID {
		t.Fatalf("expected ID %s, got %s", sess.ID, got.ID)
	}

	// CloseSession
	if err := h.CloseSession(sess.ID); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}

	// Wait for close signal
	closeOut := h.Output.WaitForSignal(exec.StreamSignalClose, 5*time.Second)
	if closeOut == nil {
		t.Fatal("expected close signal")
	}

	// Session should be gone
	_, err = h.GetSession(sess.ID)
	if !errors.Is(err, exec.ErrSessionNotFound) {
		t.Fatalf("expected SessionNotFound, got: %v", err)
	}
}

func TestManager_AttachDetach(t *testing.T) {
	h := exectest.Mount(t, exectest.WithHandler(defaultHandler()))

	sess, err := h.CreateSession(exec.SessionOptions{
		ResourcePlugin: "test",
		ResourceKey:    "pod",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Attach
	attached, buf, err := h.AttachSession(sess.ID)
	if err != nil {
		t.Fatalf("AttachSession: %v", err)
	}
	if !attached.Attached {
		t.Fatal("expected session to be attached")
	}
	_ = buf // buffer may be empty, that's fine

	// Detach
	detached, err := h.DetachSession(sess.ID)
	if err != nil {
		t.Fatalf("DetachSession: %v", err)
	}
	if detached.Attached {
		t.Fatal("expected session to be detached")
	}
}

func TestManager_HandlerNotFound(t *testing.T) {
	h := exectest.Mount(t) // no handlers registered

	_, err := h.CreateSession(exec.SessionOptions{
		ResourcePlugin: "test",
		ResourceKey:    "pod",
	})
	if !errors.Is(err, exec.ErrHandlerNotFound) {
		t.Fatalf("expected HandlerNotFound, got: %v", err)
	}
}

func TestManager_HandlerError(t *testing.T) {
	handlerErr := errors.New("connection refused")
	handler := exec.Handler{
		Plugin:     "test",
		Resource:   "pod",
		TTYHandler: exectest.FailingTTYHandler(handlerErr),
	}
	h := exectest.Mount(t, exectest.WithHandler(handler))

	_, err := h.CreateSession(exec.SessionOptions{
		ResourcePlugin: "test",
		ResourceKey:    "pod",
	})
	if err == nil {
		t.Fatal("expected error from failing handler")
	}
	if !errors.Is(err, exec.ErrTerminalError) {
		t.Fatalf("expected TerminalError, got: %v", err)
	}
}

func TestManager_StopChErrorPropagation(t *testing.T) {
	handlerErr := &exec.ExecError{
		Err:        errors.New("pod crashed"),
		Title:      "Pod Crashed",
		Message:    "The container exited unexpectedly.",
		Suggestion: "Check pod logs.",
		Retryable:  true,
	}
	handler := exec.Handler{
		Plugin:     "test",
		Resource:   "pod",
		TTYHandler: exectest.StopChTTYHandler(handlerErr, 50*time.Millisecond),
	}
	h := exectest.Mount(t, exectest.WithHandler(handler))

	_, err := h.CreateSession(exec.SessionOptions{
		ResourcePlugin: "test",
		ResourceKey:    "pod",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Wait for error signal
	errOut := h.Output.WaitForSignal(exec.StreamSignalError, 5*time.Second)
	if errOut == nil {
		t.Fatal("expected error signal")
	}
	if errOut.Error == nil {
		t.Fatal("expected StreamError to be populated")
	}
	if errOut.Error.Title != "Pod Crashed" {
		t.Fatalf("expected title 'Pod Crashed', got %q", errOut.Error.Title)
	}

	// Close signal should follow
	closeOut := h.Output.WaitForSignal(exec.StreamSignalClose, 5*time.Second)
	if closeOut == nil {
		t.Fatal("expected close signal after error")
	}
}

func TestManager_ContextCancellation(t *testing.T) {
	h := exectest.Mount(t, exectest.WithHandler(defaultHandler()))

	sess, err := h.CreateSession(exec.SessionOptions{
		ResourcePlugin: "test",
		ResourceKey:    "pod",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Close triggers context cancellation
	if err := h.CloseSession(sess.ID); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}

	closeOut := h.Output.WaitForSignal(exec.StreamSignalClose, 5*time.Second)
	if closeOut == nil {
		t.Fatal("expected close signal on context cancellation")
	}
}

func TestManager_DoubleClose(t *testing.T) {
	h := exectest.Mount(t, exectest.WithHandler(defaultHandler()))

	sess, err := h.CreateSession(exec.SessionOptions{
		ResourcePlugin: "test",
		ResourceKey:    "pod",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := h.CloseSession(sess.ID); err != nil {
		t.Fatalf("first CloseSession: %v", err)
	}

	// Wait for cleanup
	h.Output.WaitForSignal(exec.StreamSignalClose, 5*time.Second)

	// Second close should return not found
	err = h.CloseSession(sess.ID)
	if !errors.Is(err, exec.ErrSessionNotFound) {
		t.Fatalf("expected SessionNotFound on double close, got: %v", err)
	}
}

func TestManager_SessionExists(t *testing.T) {
	h := exectest.Mount(t, exectest.WithHandler(defaultHandler()))

	opts := exec.SessionOptions{
		ID:             "fixed-id",
		ResourcePlugin: "test",
		ResourceKey:    "pod",
	}

	_, err := h.CreateSession(opts)
	if err != nil {
		t.Fatalf("first CreateSession: %v", err)
	}

	// Second create with same ID should fail
	_, err = h.CreateSession(opts)
	if !errors.Is(err, exec.ErrSessionExists) {
		t.Fatalf("expected SessionExists, got: %v", err)
	}
}

func TestManager_ConcurrentSessions(t *testing.T) {
	h := exectest.Mount(t, exectest.WithHandler(defaultHandler()))

	const count = 10
	var wg sync.WaitGroup
	errs := make(chan error, count)

	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.CreateSession(exec.SessionOptions{
				ResourcePlugin: "test",
				ResourceKey:    "pod",
			})
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent CreateSession failed: %v", err)
	}

	// List should show all sessions
	pctx := &exec.Session{} // just need the count
	_ = pctx
	sessions, err := h.Manager.ListSessions(nil)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != count {
		t.Fatalf("expected %d sessions, got %d", count, len(sessions))
	}
}

func TestManager_ListSessions_Empty(t *testing.T) {
	h := exectest.Mount(t, exectest.WithHandler(defaultHandler()))

	sessions, err := h.Manager.ListSessions(nil)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected 0 sessions, got %d", len(sessions))
	}
}
