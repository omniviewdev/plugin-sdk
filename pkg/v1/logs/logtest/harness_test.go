package logtest_test

import (
	"testing"
	"time"

	logs "github.com/omniviewdev/plugin-sdk/pkg/v1/logs"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/logs/logtest"
)

// TH-001: Mount + Close lifecycle with no sessions
func TestHarnessMountClose(t *testing.T) {
	h := logtest.Mount(t)
	// Close is automatic via t.Cleanup, but we can also call it explicitly
	h.Close()
}

// TH-002: FeedLine delivers to RecordingOutput
func TestHarnessFeedLine(t *testing.T) {
	src := logtest.NewTestLogSource()
	h := logtest.Mount(t, logtest.WithSource("container-1", src))

	h.CreateSession("container-1")
	h.FeedLine("container-1", "hello world")

	lines := h.WaitForLines(1, 2*time.Second)
	if len(lines) == 0 {
		t.Fatal("expected at least 1 line, got 0")
	}
	if lines[0].Content != "hello world" {
		t.Errorf("line content = %q, want %q", lines[0].Content, "hello world")
	}
}

// TH-003: WaitForStatus times out correctly
func TestHarnessWaitForStatusTimeout(t *testing.T) {
	src := logtest.NewTestLogSource()
	h := logtest.Mount(t, logtest.WithSource("container-1", src))

	output := h.Output()
	// Don't create any session — ACTIVE should never be recorded
	found := output.WaitForStatus(logs.LogSessionStatusActive, 100*time.Millisecond)
	if found {
		t.Error("expected WaitForStatus to timeout, but it found ACTIVE")
	}
}

// TH-004: CloseSource triggers stream end
func TestHarnessCloseSource(t *testing.T) {
	src := logtest.NewTestLogSource()
	h := logtest.Mount(t, logtest.WithSource("container-1", src))

	h.CreateSession("container-1")
	h.FeedLine("container-1", "line before close")
	h.WaitForLines(1, 2*time.Second)

	h.CloseSource("container-1")
	// Wait a bit for the stream to process the close
	time.Sleep(200 * time.Millisecond)

	// The source should have triggered readiness (stream ended = mark ready)
	events := h.Output().Events()
	t.Logf("received %d events after close", len(events))
}

// TH-005: RecordingOutput WaitForEvent works
func TestRecordingOutputWaitForEvent(t *testing.T) {
	output := logtest.NewRecordingOutput()

	go func() {
		time.Sleep(50 * time.Millisecond)
		output.RecordEvent(logs.LogStreamEvent{
			Type:    logs.StreamEventSessionReady,
			Message: "test",
		})
	}()

	evt := output.WaitForEvent(logs.StreamEventSessionReady, 2*time.Second)
	if evt == nil {
		t.Fatal("expected event, got nil")
	}
	if evt.Type != logs.StreamEventSessionReady {
		t.Errorf("event type = %d, want SESSION_READY", evt.Type)
	}
}

// TH-006: RecordingOutput WaitForLines waits correctly
func TestRecordingOutputWaitForLines(t *testing.T) {
	output := logtest.NewRecordingOutput()

	go func() {
		for i := 0; i < 5; i++ {
			time.Sleep(20 * time.Millisecond)
			output.RecordLine(logs.LogLine{Content: "line"})
		}
	}()

	lines := output.WaitForLines(5, 2*time.Second)
	if len(lines) < 5 {
		t.Errorf("expected 5 lines, got %d", len(lines))
	}
}
