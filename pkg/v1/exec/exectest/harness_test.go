package exectest_test

import (
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
)

func TestHarness_MountAndClose(t *testing.T) {
	handler := exec.Handler{
		Plugin:     "test",
		Resource:   "pod",
		TTYHandler: exectest.NoopTTYHandler(),
	}
	h := exectest.Mount(t, exectest.WithHandler(handler))

	sess, err := h.CreateSession(exec.SessionOptions{
		ResourcePlugin: "test",
		ResourceKey:    "pod",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.ID == "" {
		t.Fatal("expected non-empty session ID")
	}
}

func TestHarness_CloseSession(t *testing.T) {
	handler := exec.Handler{
		Plugin:     "test",
		Resource:   "pod",
		TTYHandler: exectest.NoopTTYHandler(),
	}
	h := exectest.Mount(t, exectest.WithHandler(handler))

	sess, err := h.CreateSession(exec.SessionOptions{
		ResourcePlugin: "test",
		ResourceKey:    "pod",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := h.CloseSession(sess.ID); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}

	closeOut := h.WaitForClose(5 * time.Second)
	if closeOut == nil {
		t.Fatal("expected close signal")
	}
}

func TestRecordingOutput_WaitForOutputs(t *testing.T) {
	r := exectest.NewRecordingOutput()

	go func() {
		time.Sleep(50 * time.Millisecond)
		r.OnOutput(exec.StreamOutput{SessionID: "s1", Data: []byte("a")})
		time.Sleep(50 * time.Millisecond)
		r.OnOutput(exec.StreamOutput{SessionID: "s1", Data: []byte("b")})
	}()

	outputs := r.WaitForOutputs(2, 5*time.Second)
	if len(outputs) < 2 {
		t.Fatalf("expected at least 2 outputs, got %d", len(outputs))
	}
}

func TestRecordingOutput_WaitForSignal_Timeout(t *testing.T) {
	r := exectest.NewRecordingOutput()
	out := r.WaitForSignal(exec.StreamSignalClose, 100*time.Millisecond)
	if out != nil {
		t.Fatal("expected nil on timeout")
	}
}
