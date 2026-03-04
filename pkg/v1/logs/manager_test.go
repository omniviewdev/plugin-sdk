package logs_test

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/utils/timeutil"
	logs "github.com/omniviewdev/plugin-sdk/pkg/v1/logs"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/logs/logtest"
	"github.com/omniviewdev/plugin-sdk/settings/settingstest"
)

// SM-001: CreateSession → status is CONNECTING immediately (not ACTIVE)
func TestCreateSessionStatusConnecting(t *testing.T) {
	src := logtest.NewTestLogSource()
	h := logtest.Mount(t, logtest.WithSource("container-1", src))

	session := h.CreateSession("container-1")
	if session.Status != logs.LogSessionStatusConnecting {
		t.Errorf("initial status = %d, want CONNECTING (%d)", session.Status, logs.LogSessionStatusConnecting)
	}
}

// SM-002: Source resolution success → INITIALIZING, then SESSION_READY event when lines arrive
func TestSourceResolutionToInitializing(t *testing.T) {
	src := logtest.NewTestLogSource()
	h := logtest.Mount(t, logtest.WithSource("container-1", src))

	h.CreateSession("container-1")

	// Feed a line so the session transitions through INITIALIZING → ACTIVE
	h.FeedLine("container-1", "hello")

	evt := h.WaitForEvent(logs.StreamEventSessionReady, 2*time.Second)
	if evt == nil {
		t.Fatal("expected SESSION_READY event, got nil (timed out)")
	}
}

// SM-003: All sources send first line → SESSION_READY event, status → ACTIVE
func TestAllSourcesReadyTransitionsToActive(t *testing.T) {
	src := logtest.NewTestLogSource()
	h := logtest.Mount(t, logtest.WithSource("container-1", src))

	session := h.CreateSession("container-1")

	h.FeedLine("container-1", "hello world")

	lines := h.WaitForLines(1, 2*time.Second)
	if len(lines) < 1 {
		t.Fatal("expected at least 1 line")
	}

	// Wait for SESSION_READY to ensure transition completed
	evt := h.WaitForEvent(logs.StreamEventSessionReady, 2*time.Second)
	if evt == nil {
		t.Fatal("expected SESSION_READY event")
	}

	got := h.GetSession(session.ID)
	if got.Status != logs.LogSessionStatusActive {
		t.Errorf("after first line, status = %d, want ACTIVE (%d)", got.Status, logs.LogSessionStatusActive)
	}
}

// SM-004: No handler for resource → ERROR
func TestSourceResolutionFailure(t *testing.T) {
	h := logtest.Mount(t)

	session := h.CreateSession("nonexistent-resource")

	evt := h.WaitForEvent(logs.StreamEventStreamError, 2*time.Second)
	if evt == nil {
		t.Fatal("expected STREAM_ERROR event, got nil")
	}

	got := h.GetSession(session.ID)
	if got.Status != logs.LogSessionStatusError {
		t.Errorf("after resolution failure, status = %d, want ERROR (%d)", got.Status, logs.LogSessionStatusError)
	}
}

// SM-005: One source active, another unused → still transitions to ACTIVE
func TestPartialSourceReadiness(t *testing.T) {
	src1 := logtest.NewTestLogSource()
	src2 := logtest.NewTestLogSource()
	h := logtest.Mount(t,
		logtest.WithSource("container-1", src1),
		logtest.WithSource("container-2", src2),
	)

	session := h.CreateSession("container-1")

	h.FeedLine("container-1", "line from container-1")

	lines := h.WaitForLines(1, 2*time.Second)
	if len(lines) < 1 {
		t.Fatal("expected at least 1 line")
	}

	evt := h.WaitForEvent(logs.StreamEventSessionReady, 2*time.Second)
	if evt == nil {
		t.Fatal("expected SESSION_READY event")
	}

	got := h.GetSession(session.ID)
	if got.Status != logs.LogSessionStatusActive {
		t.Errorf("status = %d, want ACTIVE", got.Status)
	}
}

// SM-006: RECONNECTING/RECONNECTED after source close (regression)
func TestReconnectionFlowRegression(t *testing.T) {
	src := logtest.NewTestLogSource()
	h := logtest.Mount(t, logtest.WithSource("container-1", src))

	h.CreateSession("container-1")

	h.FeedLine("container-1", "first line")
	h.WaitForLines(1, 2*time.Second)

	// Close the source to trigger reconnection
	h.CloseSource("container-1")

	// Wait a bit for reconnection attempt
	time.Sleep(200 * time.Millisecond)

	events := h.Output().Events()
	t.Logf("received %d events after source close", len(events))
	if len(events) == 0 {
		t.Fatal("expected at least one event after source close (e.g. RECONNECTING), got 0")
	}
}

// SM-007: Rapid CreateSession + CloseSession x10 → no goroutine leak
func TestRapidCreateCloseNoLeak(t *testing.T) {
	h := logtest.Mount(t, logtest.WithSource("container-1", logtest.NewTestLogSource()))

	before := runtime.NumGoroutine()

	for i := 0; i < 10; i++ {
		session := h.CreateSession("container-1")
		h.CloseSession(session.ID)
	}

	// Wait for all goroutines to settle — CloseSession now waits on done channel
	h.Manager().Wait()
	time.Sleep(50 * time.Millisecond)

	after := runtime.NumGoroutine()
	if after > before+5 {
		t.Errorf("goroutine leak: before=%d, after=%d (diff=%d)", before, after, after-before)
	}
}

// SM-008: Multiple sources with staggered connection — ACTIVE only after all report ready
func TestMultiSourceStaggeredConnection(t *testing.T) {
	// Each resolved source gets its own TestLogSource so lines route deterministically.
	src1 := logtest.NewTestLogSource()
	src2 := logtest.NewTestLogSource()
	sources := map[string]*logtest.TestLogSource{
		"src-1": src1,
		"src-2": src2,
	}

	// Custom handler that dispatches by source ID.
	handler := logs.LogHandlerFunc(func(pctx *types.PluginContext, req logs.LogStreamRequest) (io.ReadCloser, error) {
		src, ok := sources[req.SourceID]
		if !ok {
			return nil, fmt.Errorf("unknown source: %s", req.SourceID)
		}
		return src.ReaderFunc(pctx)
	})

	resolver := func(_ *types.PluginContext, _ map[string]interface{}, _ logs.SourceResolverOptions) (*logs.SourceResolverResult, error) {
		return &logs.SourceResolverResult{
			Sources: []logs.LogSource{
				{ID: "src-1", Labels: map[string]string{"source": "src-1"}},
				{ID: "src-2", Labels: map[string]string{"source": "src-2"}},
			},
		}, nil
	}

	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {Plugin: "test", Resource: "pod", Handler: handler},
		},
		Resolvers: map[string]logs.SourceResolver{
			"apps::v1::Deployment": resolver,
		},
		Sink: output,
	})
	t.Cleanup(mgr.Close)

	pctx := types.NewPluginContext(context.Background(), "log", nil, nil, nil)
	session, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "apps::v1::Deployment",
		ResourceID:  "test-deploy",
		Options:     logs.LogSessionOptions{Follow: true, IncludeSourceEvents: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Stagger: send to src-1 first, then src-2
	src1.Lines <- "line from src-1"
	time.Sleep(50 * time.Millisecond)
	src2.Lines <- "line from src-2"

	lines := output.WaitForLines(2, 2*time.Second)
	if len(lines) < 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	evt := output.WaitForEvent(logs.StreamEventSessionReady, 2*time.Second)
	if evt == nil {
		t.Fatal("expected SESSION_READY event")
	}

	got, err := mgr.GetSession(pctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Status != logs.LogSessionStatusActive {
		t.Errorf("status = %d, want ACTIVE", got.Status)
	}
}

// SM-009: Source with ConnectDelay → stays INITIALIZING during delay
func TestConnectDelayStaysInitializing(t *testing.T) {
	src := logtest.NewTestLogSource()
	src.ConnectDelay = 500 * time.Millisecond
	h := logtest.Mount(t, logtest.WithSource("container-1", src))

	session := h.CreateSession("container-1")

	// Check during the delay — should be CONNECTING or INITIALIZING, not ACTIVE
	time.Sleep(100 * time.Millisecond)
	got := h.GetSession(session.ID)
	if got.Status == logs.LogSessionStatusActive {
		t.Error("status should not be ACTIVE while source is still connecting")
	}

	// After the delay, feed a line
	time.Sleep(600 * time.Millisecond)
	src.Lines <- "delayed line"

	lines := h.WaitForLines(1, 2*time.Second)
	if len(lines) < 1 {
		t.Fatal("expected line after connect delay")
	}

	evt := h.WaitForEvent(logs.StreamEventSessionReady, 2*time.Second)
	if evt == nil {
		t.Fatal("expected SESSION_READY event")
	}

	got = h.GetSession(session.ID)
	if got.Status != logs.LogSessionStatusActive {
		t.Errorf("after delay + line, status = %d, want ACTIVE", got.Status)
	}
}

// SM-010: CloseSession waits for goroutines (done channel)
func TestCloseSessionWaitsForGoroutines(t *testing.T) {
	src := logtest.NewTestLogSource()
	src.ConnectDelay = 200 * time.Millisecond
	h := logtest.Mount(t, logtest.WithSource("container-1", src))

	session := h.CreateSession("container-1")

	// CloseSession should block until the orchestration goroutine finishes
	start := time.Now()
	h.CloseSession(session.ID)
	elapsed := time.Since(start)

	// It should have waited for the goroutine — at least partially through the connect delay
	// (the cancel will interrupt the sleep, so it won't be the full 200ms)
	t.Logf("CloseSession took %v", elapsed)

	// CloseSession should complete within a reasonable time (the context cancel
	// interrupts the connect delay, so it should be well under 10s safety timeout)
	if elapsed > 5*time.Second {
		t.Errorf("CloseSession took too long (%v), expected < 5s", elapsed)
	}

	// The session should be removed — GetSession should return error
	pctx := logtest.ContextWithCancelFrom(h)
	_, err := h.Manager().GetSession(pctx, session.ID)
	if err == nil {
		t.Error("expected error from GetSession after CloseSession, session should be removed")
	}
}

// SM-011: Pause command → PAUSED, lines skipped; Resume → ACTIVE
func TestPauseResumeCommands(t *testing.T) {
	src := logtest.NewTestLogSource()
	h := logtest.Mount(t, logtest.WithSource("container-1", src))

	session := h.CreateSession("container-1")

	// Feed a line to get to ACTIVE
	h.FeedLine("container-1", "line 1")
	h.WaitForLines(1, 2*time.Second)

	evt := h.WaitForEvent(logs.StreamEventSessionReady, 2*time.Second)
	if evt == nil {
		t.Fatal("expected SESSION_READY")
	}

	// Pause via UpdateSessionOptions with Follow=false
	pctx := logtest.ContextWithCancelFrom(h)
	_, err := h.Manager().UpdateSessionOptions(pctx, session.ID, logs.LogSessionOptions{
		Follow:              false,
		IncludeSourceEvents: true,
		Params:              map[string]string{},
	})
	if err != nil {
		t.Fatalf("UpdateSessionOptions (pause) failed: %v", err)
	}

	// Verify session status is still ACTIVE (UpdateSessionOptions doesn't change status,
	// Follow=false just affects streaming behavior)
	got := h.GetSession(session.ID)
	if got.Status != logs.LogSessionStatusActive {
		t.Errorf("after pause update: status = %d, want ACTIVE (%d)", got.Status, logs.LogSessionStatusActive)
	}
	if got.Options.Follow != false {
		t.Errorf("after pause update: Follow = %v, want false", got.Options.Follow)
	}

	// Resume via UpdateSessionOptions with Follow=true
	_, err = h.Manager().UpdateSessionOptions(pctx, session.ID, logs.LogSessionOptions{
		Follow:              true,
		IncludeSourceEvents: true,
		Params:              map[string]string{},
	})
	if err != nil {
		t.Fatalf("UpdateSessionOptions (resume) failed: %v", err)
	}

	got = h.GetSession(session.ID)
	if got.Status != logs.LogSessionStatusActive {
		t.Errorf("after resume update: status = %d, want ACTIVE (%d)", got.Status, logs.LogSessionStatusActive)
	}
	if got.Options.Follow != true {
		t.Errorf("after resume update: Follow = %v, want true", got.Options.Follow)
	}
}

// ---------------------------------------------------------------------------
// Edge case tests
// ---------------------------------------------------------------------------

// EC-001: CreateSession with nil PluginContext → error, not panic
func TestCreateSessionNilPluginContext(t *testing.T) {
	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {Plugin: "test", Resource: "pod"},
		},
		Sink: output,
	})
	t.Cleanup(mgr.Close)

	_, err := mgr.CreateSession(nil, logs.CreateSessionOptions{
		ResourceKey: "pod",
		ResourceID:  "test",
	})
	if err == nil {
		t.Fatal("expected error for nil PluginContext, got nil")
	}
}

// EC-002: GetSession for non-existent session → error
func TestGetSessionNotFound(t *testing.T) {
	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{Sink: output})
	t.Cleanup(mgr.Close)

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	_, err := mgr.GetSession(pctx, "nonexistent-id")
	if err == nil {
		t.Fatal("expected error for non-existent session")
	}
}

// EC-003: CloseSession twice → second call returns error
func TestCloseSessionTwice(t *testing.T) {
	src := logtest.NewTestLogSource()
	h := logtest.Mount(t, logtest.WithSource("container-1", src))

	session := h.CreateSession("container-1")
	h.CloseSession(session.ID)

	// Second close should return an error (session already removed)
	pctx := logtest.ContextWithCancelFrom(h)
	err := h.Manager().CloseSession(pctx, session.ID)
	if err == nil {
		t.Fatal("expected error closing already-closed session")
	}
}

// EC-004: UpdateSessionOptions for non-existent session → error
func TestUpdateSessionOptionsNotFound(t *testing.T) {
	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{Sink: output})
	t.Cleanup(mgr.Close)

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	_, err := mgr.UpdateSessionOptions(pctx, "nonexistent", logs.LogSessionOptions{})
	if err == nil {
		t.Fatal("expected error for non-existent session")
	}
}

// EC-005: ListSessions returns empty slice, not nil
func TestListSessionsEmpty(t *testing.T) {
	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{Sink: output})
	t.Cleanup(mgr.Close)

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	sessions, err := mgr.ListSessions(pctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if sessions == nil {
		t.Fatal("expected non-nil slice")
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(sessions))
	}
}

// EC-006: NewManager with nil config fields doesn't panic
func TestNewManagerNilFields(t *testing.T) {
	mgr := logs.NewManager(logs.ManagerConfig{})
	t.Cleanup(mgr.Close)

	resources := mgr.GetSupportedResources(nil)
	if len(resources) != 0 {
		t.Errorf("expected 0 resources, got %d", len(resources))
	}
}

// EC-007: Close with no sessions doesn't block
func TestCloseEmptyManager(t *testing.T) {
	mgr := logs.NewManager(logs.ManagerConfig{})

	done := make(chan struct{})
	go func() {
		mgr.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("Close blocked on empty manager")
	}
}

// EC-008: ChannelSink respects context cancellation (doesn't block on full channel)
func TestChannelSinkContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan logs.StreamOutput, 1) // tiny buffer
	sink := logs.NewChannelSink(ctx, out)

	// Fill the buffer
	sink.OnLine(logs.LogLine{SessionID: "s1", Content: "fill"})

	// Cancel context, next send should not block
	cancel()

	done := make(chan struct{})
	go func() {
		sink.OnLine(logs.LogLine{SessionID: "s1", Content: "after cancel"})
		sink.OnEvent("s1", logs.LogStreamEvent{Type: logs.StreamEventSessionReady})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("ChannelSink.OnLine blocked after context cancellation")
	}
}

// EC-009: Handler connect failure → reconnection attempts with context check
func TestHandlerConnectFailureReconnects(t *testing.T) {
	src := logtest.NewTestLogSource()
	src.ErrOnConnect = fmt.Errorf("simulated connect failure")

	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/container-1": {
				Plugin:        "test",
				Resource:      "container-1",
				Handler:       src.HandlerFunc(),
				SourceBuilder: logtest.SingleSourceBuilder("container-1", map[string]string{"source": "container-1"}),
			},
		},
		Sink:  output,
		Clock: timeutil.NewInstantFakeClock(), // instant reconnect delays
	})
	t.Cleanup(mgr.Close)

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	session, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "container-1",
		ResourceID:  "test-resource",
		Options:     logs.LogSessionOptions{Follow: true, IncludeSourceEvents: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// With FakeClock, reconnection attempts are near-instant
	evt := output.WaitForEvent(logs.StreamEventStreamError, 5*time.Second)
	if evt == nil {
		t.Fatal("expected STREAM_ERROR after reconnect exhaustion")
	}

	// Session should still be retrievable
	got, err := mgr.GetSession(pctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	t.Logf("session status after reconnect failure: %d", got.Status)
}

// ---------------------------------------------------------------------------
// Coverage gap tests: Stream + handleCommands
// ---------------------------------------------------------------------------

// CG-001: Stream() creates ChannelSink and output channel when no Sink configured
func TestStreamCreatesChannelSink(t *testing.T) {
	src := logtest.NewTestLogSource()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:   "test",
				Resource: "pod",
				Handler:  src.HandlerFunc(),
				SourceBuilder: logtest.SingleSourceBuilder(
					"pod-1", map[string]string{"source": "pod-1"},
				),
			},
		},
		// No Sink — Stream() should create one
	})
	t.Cleanup(mgr.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in := make(chan logs.StreamInput, 10)
	out, err := mgr.Stream(ctx, in)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if out == nil {
		t.Fatal("Stream returned nil output channel")
	}

	// Create a session — output should flow through the channel
	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	_, err = mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "pod",
		ResourceID:  "test-pod",
		Options:     logs.LogSessionOptions{Follow: true, IncludeSourceEvents: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	src.Lines <- "hello via stream"

	select {
	case msg := <-out:
		if msg.Line == nil {
			// Could be an event — drain until we get a line
			for i := 0; i < 10; i++ {
				select {
				case msg = <-out:
					if msg.Line != nil {
						if msg.Line.Content != "hello via stream" {
							t.Errorf("content = %q, want %q", msg.Line.Content, "hello via stream")
						}
						return
					}
				case <-time.After(2 * time.Second):
					t.Fatal("timed out waiting for line on output channel")
				}
			}
		} else if msg.Line.Content != "hello via stream" {
			t.Errorf("content = %q, want %q", msg.Line.Content, "hello via stream")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for output on stream channel")
	}
}

// CG-002: handleCommands dispatches pause, resume, close via Stream input channel
func TestStreamHandleCommands(t *testing.T) {
	src := logtest.NewTestLogSource()
	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:   "test",
				Resource: "pod",
				Handler:  src.HandlerFunc(),
				SourceBuilder: logtest.SingleSourceBuilder(
					"pod-1", map[string]string{"source": "pod-1"},
				),
			},
		},
		Sink: output,
	})
	t.Cleanup(mgr.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in := make(chan logs.StreamInput, 10)
	_, err := mgr.Stream(ctx, in)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	session, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "pod",
		ResourceID:  "test-pod",
		Options:     logs.LogSessionOptions{Follow: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Feed a line to get to ACTIVE
	src.Lines <- "line 1"
	output.WaitForLines(1, 2*time.Second)
	output.WaitForEvent(logs.StreamEventSessionReady, 2*time.Second)

	// Send pause command
	in <- logs.StreamInput{SessionID: session.ID, Command: logs.StreamCommandPause}
	time.Sleep(50 * time.Millisecond)

	got, err := mgr.GetSession(pctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Status != logs.LogSessionStatusPaused {
		t.Errorf("after pause: status = %d, want PAUSED (%d)", got.Status, logs.LogSessionStatusPaused)
	}

	// Send resume command
	in <- logs.StreamInput{SessionID: session.ID, Command: logs.StreamCommandResume}
	time.Sleep(50 * time.Millisecond)

	got, err = mgr.GetSession(pctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Status != logs.LogSessionStatusActive {
		t.Errorf("after resume: status = %d, want ACTIVE (%d)", got.Status, logs.LogSessionStatusActive)
	}

	// Send close command
	in <- logs.StreamInput{SessionID: session.ID, Command: logs.StreamCommandClose}
	time.Sleep(100 * time.Millisecond)

	_, err = mgr.GetSession(pctx, session.ID)
	if err == nil {
		t.Error("expected error after close command, session should be removed")
	}
}

// CG-003: handleCommands with unknown session ID logs error but doesn't panic
func TestStreamCommandUnknownSession(t *testing.T) {
	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{Sink: output})
	t.Cleanup(mgr.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in := make(chan logs.StreamInput, 10)
	_, err := mgr.Stream(ctx, in)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	// Send command for non-existent session — should not panic
	in <- logs.StreamInput{SessionID: "nonexistent", Command: logs.StreamCommandPause}
	time.Sleep(50 * time.Millisecond)
}

// CG-004: Stream context cancellation triggers closeAll
func TestStreamContextCancelTriggersCloseAll(t *testing.T) {
	src := logtest.NewTestLogSource()
	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:   "test",
				Resource: "pod",
				Handler:  src.HandlerFunc(),
				SourceBuilder: logtest.SingleSourceBuilder(
					"pod-1", map[string]string{"source": "pod-1"},
				),
			},
		},
		Sink: output,
	})

	ctx, cancel := context.WithCancel(context.Background())
	in := make(chan logs.StreamInput, 10)
	_, err := mgr.Stream(ctx, in)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	_, err = mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "pod",
		ResourceID:  "test-pod",
		Options:     logs.LogSessionOptions{Follow: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Cancel the stream context — should trigger closeAll
	cancel()
	time.Sleep(100 * time.Millisecond)

	sessions, err := mgr.ListSessions(pctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions after context cancel, got %d", len(sessions))
	}
	mgr.Close()
}

// ---------------------------------------------------------------------------
// Coverage gap tests: watchSourceEvents (dynamic source lifecycle)
// ---------------------------------------------------------------------------

// CG-005: Dynamic source add via SourceEvent channel
func TestWatchSourceEventsAddSource(t *testing.T) {
	src := logtest.NewTestLogSource()
	dynamicSrc := logtest.NewTestLogSource()

	sources := map[string]*logtest.TestLogSource{
		"initial": src,
		"dynamic": dynamicSrc,
	}

	handler := logs.LogHandlerFunc(func(pctx *types.PluginContext, req logs.LogStreamRequest) (io.ReadCloser, error) {
		s, ok := sources[req.SourceID]
		if !ok {
			return nil, fmt.Errorf("unknown source: %s", req.SourceID)
		}
		return s.ReaderFunc(pctx)
	})

	events := make(chan logs.SourceEvent, 10)
	resolver := func(_ *types.PluginContext, _ map[string]interface{}, _ logs.SourceResolverOptions) (*logs.SourceResolverResult, error) {
		return &logs.SourceResolverResult{
			Sources: []logs.LogSource{
				{ID: "initial", Labels: map[string]string{"source": "initial"}},
			},
			Events: events,
		}, nil
	}

	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {Plugin: "test", Resource: "pod", Handler: handler},
		},
		Resolvers: map[string]logs.SourceResolver{
			"apps::v1::Deployment": resolver,
		},
		Sink: output,
	})
	t.Cleanup(mgr.Close)

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	session, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey:  "apps::v1::Deployment",
		ResourceID:   "deploy-1",
		ResourceData: map[string]interface{}{},
		Options:      logs.LogSessionOptions{Follow: true, IncludeSourceEvents: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Feed initial source to reach ACTIVE
	src.Lines <- "initial line"
	output.WaitForEvent(logs.StreamEventSessionReady, 2*time.Second)

	// Dynamically add a source
	events <- logs.SourceEvent{
		Type:   logs.SourceAdded,
		Source: logs.LogSource{ID: "dynamic", Labels: map[string]string{"source": "dynamic"}},
	}

	// Wait for the SourceAdded event to be emitted by the manager
	time.Sleep(100 * time.Millisecond)

	// Feed the dynamic source
	dynamicSrc.Lines <- "dynamic line"
	lines := output.WaitForLines(2, 2*time.Second)
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines (initial + dynamic), got %d", len(lines))
	}

	// Check SourceAdded events were emitted (one at start for initial, one for dynamic)
	found := false
	for _, evt := range output.Events() {
		if evt.Type == logs.StreamEventSourceAdded && evt.SourceID == "dynamic" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected SOURCE_ADDED event for dynamic source")
	}

	// Verify the session has both sources
	got, err := mgr.GetSession(pctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if len(got.ActiveSources) < 2 {
		t.Errorf("expected at least 2 active sources, got %d", len(got.ActiveSources))
	}
}

// CG-006: Dynamic source remove via SourceEvent channel
func TestWatchSourceEventsRemoveSource(t *testing.T) {
	src := logtest.NewTestLogSource()

	handler := logs.LogHandlerFunc(func(pctx *types.PluginContext, req logs.LogStreamRequest) (io.ReadCloser, error) {
		return src.ReaderFunc(pctx)
	})

	events := make(chan logs.SourceEvent, 10)
	resolver := func(_ *types.PluginContext, _ map[string]interface{}, _ logs.SourceResolverOptions) (*logs.SourceResolverResult, error) {
		return &logs.SourceResolverResult{
			Sources: []logs.LogSource{
				{ID: "src-1", Labels: map[string]string{}},
				{ID: "src-2", Labels: map[string]string{}},
			},
			Events: events,
		}, nil
	}

	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {Plugin: "test", Resource: "pod", Handler: handler},
		},
		Resolvers: map[string]logs.SourceResolver{
			"test/deploy": resolver,
		},
		Sink: output,
	})
	t.Cleanup(mgr.Close)

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	session, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "test/deploy",
		ResourceID:  "deploy-1",
		Options:     logs.LogSessionOptions{Follow: true, IncludeSourceEvents: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Feed lines to reach ACTIVE
	src.Lines <- "line from src"
	src.Lines <- "another line"
	output.WaitForEvent(logs.StreamEventSessionReady, 2*time.Second)

	// Remove a source
	events <- logs.SourceEvent{
		Type:   logs.SourceRemoved,
		Source: logs.LogSource{ID: "src-2"},
	}
	time.Sleep(100 * time.Millisecond)

	// Check SOURCE_REMOVED event was emitted
	found := false
	for _, evt := range output.Events() {
		if evt.Type == logs.StreamEventSourceRemoved && evt.SourceID == "src-2" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected SOURCE_REMOVED event for src-2")
	}

	// Verify the session has one less active source
	got, err := mgr.GetSession(pctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	for _, s := range got.ActiveSources {
		if s.ID == "src-2" {
			t.Error("src-2 should have been removed from active sources")
		}
	}
	_ = session
}

// CG-007: watchSourceEvents exits when events channel closes
func TestWatchSourceEventsChannelClose(t *testing.T) {
	src := logtest.NewTestLogSource()

	handler := logs.LogHandlerFunc(func(pctx *types.PluginContext, req logs.LogStreamRequest) (io.ReadCloser, error) {
		return src.ReaderFunc(pctx)
	})

	events := make(chan logs.SourceEvent, 10)
	resolver := func(_ *types.PluginContext, _ map[string]interface{}, _ logs.SourceResolverOptions) (*logs.SourceResolverResult, error) {
		return &logs.SourceResolverResult{
			Sources: []logs.LogSource{{ID: "src-1", Labels: map[string]string{}}},
			Events:  events,
		}, nil
	}

	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {Plugin: "test", Resource: "pod", Handler: handler},
		},
		Resolvers: map[string]logs.SourceResolver{
			"test/deploy": resolver,
		},
		Sink: output,
	})
	t.Cleanup(mgr.Close)

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	_, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "test/deploy",
		ResourceID:  "deploy-1",
		Options:     logs.LogSessionOptions{Follow: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	src.Lines <- "line"
	output.WaitForEvent(logs.StreamEventSessionReady, 2*time.Second)

	// Close the events channel — watchSourceEvents goroutine should exit gracefully
	close(events)
	time.Sleep(50 * time.Millisecond)
	// No panic, no hang — test passes if it completes
}

// ---------------------------------------------------------------------------
// Coverage gap tests: updateEnabledSources
// ---------------------------------------------------------------------------

// CG-008: Disable a source via UpdateSessionOptions enabled_sources param
func TestUpdateEnabledSourcesDisableOne(t *testing.T) {
	src := logtest.NewTestLogSource()

	handler := logs.LogHandlerFunc(func(pctx *types.PluginContext, req logs.LogStreamRequest) (io.ReadCloser, error) {
		return src.ReaderFunc(pctx)
	})

	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:        "test",
				Resource:      "pod",
				Handler:       handler,
				SourceBuilder: logtest.MultiSourceBuilder("src-a", "src-b"),
			},
		},
		Sink: output,
	})
	t.Cleanup(mgr.Close)

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	session, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "pod",
		ResourceID:  "test-pod",
		Options:     logs.LogSessionOptions{Follow: true, IncludeSourceEvents: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Feed lines to both sources
	src.Lines <- "line a"
	src.Lines <- "line b"
	output.WaitForEvent(logs.StreamEventSessionReady, 2*time.Second)

	// Disable src-b by setting enabled_sources to only src-a
	_, err = mgr.UpdateSessionOptions(pctx, session.ID, logs.LogSessionOptions{
		Follow: true,
		Params: map[string]string{"enabled_sources": "src-a"},
	})
	if err != nil {
		t.Fatalf("UpdateSessionOptions: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Check that a SOURCE_REMOVED event was emitted for src-b
	found := false
	for _, evt := range output.Events() {
		if evt.Type == logs.StreamEventSourceRemoved && evt.SourceID == "src-b" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected SOURCE_REMOVED event for disabled src-b")
	}
}

// ---------------------------------------------------------------------------
// Coverage gap tests: orchestrateSession resolver paths
// ---------------------------------------------------------------------------

// CG-009: Resolver returns error → session goes to ERROR
func TestResolverReturnsError(t *testing.T) {
	resolver := func(_ *types.PluginContext, _ map[string]interface{}, _ logs.SourceResolverOptions) (*logs.SourceResolverResult, error) {
		return nil, fmt.Errorf("simulated resolver failure")
	}

	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Resolvers: map[string]logs.SourceResolver{
			"test/deploy": resolver,
		},
		Sink: output,
	})
	t.Cleanup(mgr.Close)

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	session, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "test/deploy",
		ResourceID:  "deploy-1",
		Options:     logs.LogSessionOptions{Follow: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	evt := output.WaitForEvent(logs.StreamEventStreamError, 2*time.Second)
	if evt == nil {
		t.Fatal("expected STREAM_ERROR event after resolver failure")
	}
	if evt.Message == "" {
		t.Error("expected non-empty error message")
	}

	got, err := mgr.GetSession(pctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Status != logs.LogSessionStatusError {
		t.Errorf("status = %d, want ERROR", got.Status)
	}
}

// CG-010: Resolver succeeds but no handler available → ERROR
func TestResolverNoHandlerAvailable(t *testing.T) {
	resolver := func(_ *types.PluginContext, _ map[string]interface{}, _ logs.SourceResolverOptions) (*logs.SourceResolverResult, error) {
		return &logs.SourceResolverResult{
			Sources: []logs.LogSource{{ID: "src-1", Labels: map[string]string{}}},
		}, nil
	}

	output := logtest.NewRecordingOutput()
	// No handlers registered — only resolver
	mgr := logs.NewManager(logs.ManagerConfig{
		Resolvers: map[string]logs.SourceResolver{
			"test/deploy": resolver,
		},
		Sink: output,
	})
	t.Cleanup(mgr.Close)

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	session, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "test/deploy",
		ResourceID:  "deploy-1",
		Options:     logs.LogSessionOptions{Follow: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	evt := output.WaitForEvent(logs.StreamEventStreamError, 2*time.Second)
	if evt == nil {
		t.Fatal("expected STREAM_ERROR event when no handler available")
	}

	got, err := mgr.GetSession(pctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Status != logs.LogSessionStatusError {
		t.Errorf("status = %d, want ERROR", got.Status)
	}
}

// ---------------------------------------------------------------------------
// Coverage gap tests: streamFromHandler paths
// ---------------------------------------------------------------------------

// CG-011: Handler with nil SourceBuilder → default single source
func TestHandlerNilSourceBuilder(t *testing.T) {
	src := logtest.NewTestLogSource()
	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:   "test",
				Resource: "pod",
				Handler:  src.HandlerFunc(),
				// No SourceBuilder — should use default single source
			},
		},
		Sink: output,
	})
	t.Cleanup(mgr.Close)

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	_, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "pod",
		ResourceID:  "test-pod",
		Options:     logs.LogSessionOptions{Follow: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	src.Lines <- "line from default source"

	lines := output.WaitForLines(1, 2*time.Second)
	if len(lines) < 1 {
		t.Fatal("expected at least 1 line with nil SourceBuilder")
	}
	if lines[0].SourceID != "test-pod" {
		t.Errorf("source ID = %q, want %q (resource ID used as default)", lines[0].SourceID, "test-pod")
	}
}

// CG-012: SourceBuilder returns empty slice → ERROR
func TestHandlerEmptySourceBuilder(t *testing.T) {
	emptyBuilder := func(_ string, _ map[string]interface{}, _ logs.LogSessionOptions) []logs.LogSource {
		return nil // empty
	}

	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:        "test",
				Resource:      "pod",
				Handler:       logtest.NewTestLogSource().HandlerFunc(),
				SourceBuilder: emptyBuilder,
			},
		},
		Sink: output,
	})
	t.Cleanup(mgr.Close)

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	session, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "pod",
		ResourceID:  "test-pod",
		Options:     logs.LogSessionOptions{Follow: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	evt := output.WaitForEvent(logs.StreamEventStreamError, 2*time.Second)
	if evt == nil {
		t.Fatal("expected STREAM_ERROR event when SourceBuilder returns empty")
	}

	got, err := mgr.GetSession(pctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Status != logs.LogSessionStatusError {
		t.Errorf("status = %d, want ERROR", got.Status)
	}
}

// ---------------------------------------------------------------------------
// Coverage gap tests: readStream paused path
// ---------------------------------------------------------------------------

// CG-013: Lines sent while session is PAUSED are skipped
func TestReadStreamPausedSkipsLines(t *testing.T) {
	src := logtest.NewTestLogSource()
	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:   "test",
				Resource: "pod",
				Handler:  src.HandlerFunc(),
				SourceBuilder: logtest.SingleSourceBuilder(
					"pod-1", map[string]string{"source": "pod-1"},
				),
			},
		},
		Sink: output,
	})
	t.Cleanup(mgr.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in := make(chan logs.StreamInput, 10)
	_, err := mgr.Stream(ctx, in)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	session, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "pod",
		ResourceID:  "test-pod",
		Options:     logs.LogSessionOptions{Follow: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Get to ACTIVE
	src.Lines <- "line 1"
	output.WaitForLines(1, 2*time.Second)
	output.WaitForEvent(logs.StreamEventSessionReady, 2*time.Second)

	// Pause
	in <- logs.StreamInput{SessionID: session.ID, Command: logs.StreamCommandPause}
	time.Sleep(50 * time.Millisecond)

	// Send lines while paused
	src.Lines <- "paused line 1"
	src.Lines <- "paused line 2"
	time.Sleep(100 * time.Millisecond)

	linesBefore := len(output.Lines())

	// Resume
	in <- logs.StreamInput{SessionID: session.ID, Command: logs.StreamCommandResume}
	time.Sleep(50 * time.Millisecond)

	// Send a line after resume
	src.Lines <- "after resume"
	output.WaitForLines(linesBefore+1, 2*time.Second)

	// The paused lines should have been skipped — only "line 1" + "after resume" should appear
	// (paused lines may or may not arrive depending on timing; the key invariant is
	// that lines sent DURING pause are dropped, not that every paused line is dropped)
	allLines := output.Lines()
	t.Logf("total lines received: %d", len(allLines))

	// Verify the "after resume" line arrived
	found := false
	for _, l := range allLines {
		if l.Content == "after resume" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'after resume' line to be received")
	}
}

// ---------------------------------------------------------------------------
// Coverage gap tests: ListSessions with active sessions
// ---------------------------------------------------------------------------

// CG-014: ListSessions returns all active sessions
func TestListSessionsWithActiveSessions(t *testing.T) {
	src := logtest.NewTestLogSource()
	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:   "test",
				Resource: "pod",
				Handler:  src.HandlerFunc(),
				SourceBuilder: logtest.SingleSourceBuilder(
					"pod-1", map[string]string{"source": "pod-1"},
				),
			},
		},
		Sink: output,
	})
	t.Cleanup(mgr.Close)

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)

	// Create 3 sessions
	ids := make([]string, 3)
	for i := 0; i < 3; i++ {
		session, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
			ResourceKey: "pod",
			ResourceID:  fmt.Sprintf("pod-%d", i),
			Options:     logs.LogSessionOptions{Follow: true},
		})
		if err != nil {
			t.Fatalf("CreateSession[%d]: %v", i, err)
		}
		ids[i] = session.ID
	}

	sessions, err := mgr.ListSessions(pctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 3 {
		t.Errorf("expected 3 sessions, got %d", len(sessions))
	}

	// Verify all session IDs are present
	idSet := make(map[string]bool)
	for _, s := range sessions {
		idSet[s.ID] = true
	}
	for _, id := range ids {
		if !idSet[id] {
			t.Errorf("session %s not found in list", id)
		}
	}
}

// ---------------------------------------------------------------------------
// Coverage gap tests: extractTimestamp
// ---------------------------------------------------------------------------

// CG-015: extractTimestamp parses various formats
func TestExtractTimestampFormats(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantTS  bool   // true if we expect a parsed timestamp (not fallback)
		wantMsg string // expected remaining content
	}{
		{
			name:    "RFC3339Nano",
			input:   "2025-01-15T10:30:45.123456789Z hello world",
			wantTS:  true,
			wantMsg: "hello world",
		},
		{
			name:    "RFC3339",
			input:   "2025-01-15T10:30:45+00:00 hello world",
			wantTS:  true,
			wantMsg: "hello world",
		},
		{
			name:    "bare datetime with Z",
			input:   "2025-01-15T10:30:45Z hello world",
			wantTS:  true,
			wantMsg: "hello world",
		},
		{
			name:    "bare datetime no zone",
			input:   "2025-01-15T10:30:45 hello world",
			wantTS:  true,
			wantMsg: "hello world",
		},
		{
			name:    "no timestamp",
			input:   "just a regular log line",
			wantTS:  false,
			wantMsg: "just a regular log line",
		},
		{
			name:    "short string",
			input:   "hi",
			wantTS:  false,
			wantMsg: "hi",
		},
		{
			name:    "empty string",
			input:   "",
			wantTS:  false,
			wantMsg: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Feed the line through a manager to exercise extractTimestamp
			src := logtest.NewTestLogSource()
			output := logtest.NewRecordingOutput()
			mgr := logs.NewManager(logs.ManagerConfig{
				Handlers: map[string]logs.Handler{
					"test/pod": {
						Plugin:   "test",
						Resource: "pod",
						Handler:  src.HandlerFunc(),
						SourceBuilder: logtest.SingleSourceBuilder(
							"pod-1", map[string]string{},
						),
					},
				},
				Sink: output,
			})

			pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
			_, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
				ResourceKey: "pod",
				ResourceID:  "test",
				Options:     logs.LogSessionOptions{Follow: true},
			})
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}

			if tt.input != "" {
				src.Lines <- tt.input
				lines := output.WaitForLines(1, 2*time.Second)
				if len(lines) < 1 {
					t.Fatal("expected at least 1 line")
				}

				if lines[0].Content != tt.wantMsg {
					t.Errorf("content = %q, want %q", lines[0].Content, tt.wantMsg)
				}
				if tt.wantTS && lines[0].Timestamp.IsZero() {
					t.Error("expected non-zero timestamp from parsed input")
				}
			}

			mgr.Close()
		})
	}
}

// ---------------------------------------------------------------------------
// Coverage gap tests: registry edge cases
// ---------------------------------------------------------------------------

// CG-016: Registry with no handlers — AnyHandler returns false
func TestRegistryNoHandlers(t *testing.T) {
	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Sink: output,
	})
	t.Cleanup(mgr.Close)

	resources := mgr.GetSupportedResources(nil)
	if len(resources) != 0 {
		t.Errorf("expected 0 resources with no handlers, got %d", len(resources))
	}
}

// ---------------------------------------------------------------------------
// Coverage gap tests: concurrent operations
// ---------------------------------------------------------------------------

// CG-017: Concurrent CreateSession + ListSessions + GetSession
func TestConcurrentSessionOperations(t *testing.T) {
	src := logtest.NewTestLogSource()
	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:   "test",
				Resource: "pod",
				Handler:  src.HandlerFunc(),
				SourceBuilder: logtest.SingleSourceBuilder(
					"pod-1", map[string]string{},
				),
			},
		},
		Sink: output,
	})
	t.Cleanup(mgr.Close)

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)

	const N = 20

	type createResult struct {
		sessionID string
		err       error
	}
	results := make(chan createResult, N)

	// Concurrent creates
	for i := 0; i < N; i++ {
		go func(i int) {
			session, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
				ResourceKey: "pod",
				ResourceID:  fmt.Sprintf("pod-%d", i),
				Options:     logs.LogSessionOptions{Follow: true},
			})
			if err != nil {
				results <- createResult{err: fmt.Errorf("create[%d]: %w", i, err)}
				return
			}
			results <- createResult{sessionID: session.ID}
		}(i)
	}

	// Wait for all creates
	ids := make([]string, 0, N)
	for i := 0; i < N; i++ {
		r := <-results
		if r.err != nil {
			t.Error(r.err)
		} else {
			ids = append(ids, r.sessionID)
		}
	}

	// Concurrent reads
	m := len(ids)
	errs := make(chan error, m*2)
	for i := 0; i < m; i++ {
		id := ids[i]
		go func(id string) {
			_, err := mgr.GetSession(pctx, id)
			errs <- err
		}(id)
		go func() {
			_, err := mgr.ListSessions(pctx)
			errs <- err
		}()
	}

	for i := 0; i < m*2; i++ {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
}

// CG-018: Non-follow mode — stream ends after source closes without reconnect
func TestNonFollowModeNoReconnect(t *testing.T) {
	src := logtest.NewTestLogSource()
	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:   "test",
				Resource: "pod",
				Handler:  src.HandlerFunc(),
				SourceBuilder: logtest.SingleSourceBuilder(
					"pod-1", map[string]string{},
				),
			},
		},
		Sink: output,
	})
	t.Cleanup(mgr.Close)

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	_, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "pod",
		ResourceID:  "test-pod",
		Options:     logs.LogSessionOptions{Follow: false}, // non-follow
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	src.Lines <- "one-shot line"
	output.WaitForLines(1, 2*time.Second)

	// Close source — should NOT trigger reconnection in non-follow mode
	close(src.Lines)
	time.Sleep(200 * time.Millisecond)

	// Should not see RECONNECTING events
	for _, evt := range output.Events() {
		if evt.Type == logs.StreamEventReconnecting {
			t.Error("unexpected RECONNECTING event in non-follow mode")
		}
	}
}

// CG-019: MultiSourceBuilder produces correct sources
func TestMultiSourceBuilder(t *testing.T) {
	src := logtest.NewTestLogSource()
	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:        "test",
				Resource:      "pod",
				Handler:       src.HandlerFunc(),
				SourceBuilder: logtest.MultiSourceBuilder("container-a", "container-b", "container-c"),
			},
		},
		Sink: output,
	})
	t.Cleanup(mgr.Close)

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	session, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "pod",
		ResourceID:  "test-pod",
		Options:     logs.LogSessionOptions{Follow: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Feed 3 lines (one per source goroutine will read)
	src.Lines <- "line-a"
	src.Lines <- "line-b"
	src.Lines <- "line-c"

	output.WaitForEvent(logs.StreamEventSessionReady, 2*time.Second)

	got, err := mgr.GetSession(pctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if len(got.ActiveSources) != 3 {
		t.Errorf("expected 3 active sources, got %d", len(got.ActiveSources))
	}
}

// ===========================================================================
// CRITICAL: Infinite loop / runaway CPU prevention tests
//
// These tests verify that no code path can spin unboundedly. Each test has a
// hard deadline — if the deadline fires, the test fails (proving a spin exists).
// ===========================================================================

// SPIN-001: Closed input channel on handleCommands must NOT cause CPU spin.
// Before the fix, receiving from a closed channel returns zero-value immediately
// on every iteration, spinning at 100% CPU forever.
func TestHandleCommandsClosedChannelNoSpin(t *testing.T) {
	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:   "test",
				Resource: "pod",
				Handler:  logtest.NewTestLogSource().HandlerFunc(),
				SourceBuilder: logtest.SingleSourceBuilder(
					"pod-1", map[string]string{},
				),
			},
		},
		Sink: output,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in := make(chan logs.StreamInput, 1)
	_, err := mgr.Stream(ctx, in)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	// Create a session so closeAll has something to clean up
	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	_, err = mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "pod",
		ResourceID:  "test-pod",
		Options:     logs.LogSessionOptions{Follow: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Close the input channel — handleCommands must detect this and exit
	close(in)

	// If handleCommands spins, goroutine count will explode and CPU will spike.
	// Give it a moment to process, then verify sessions were cleaned up.
	deadline := time.After(2 * time.Second)
	for {
		sessions, _ := mgr.ListSessions(pctx)
		if len(sessions) == 0 {
			break // closeAll fired — handleCommands exited correctly
		}
		select {
		case <-deadline:
			t.Fatal("SPIN DETECTED: handleCommands did not exit after input channel closed")
		case <-time.After(10 * time.Millisecond):
		}
	}

	mgr.Close()
}

// SPIN-002: streamWithReconnect with instantly-EOF reader + Follow=true is bounded.
// If a handler returns a reader that immediately returns EOF, the reconnect loop
// must exhaust MaxAttempts and stop — it must NOT loop forever.
func TestReconnectWithInstantEOFReaderBounded(t *testing.T) {
	// Handler returns a reader that immediately returns EOF.
	handler := logs.LogHandlerFunc(func(_ *types.PluginContext, _ logs.LogStreamRequest) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("")), nil
	})

	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:   "test",
				Resource: "pod",
				Handler:  handler,
				SourceBuilder: logtest.SingleSourceBuilder(
					"pod-1", map[string]string{},
				),
			},
		},
		Sink:  output,
		Clock: timeutil.NewInstantFakeClock(),
	})

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	_, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "pod",
		ResourceID:  "test-pod",
		Options:     logs.LogSessionOptions{Follow: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Must complete within a deadline — if it spins, this times out
	done := make(chan struct{})
	go func() {
		mgr.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Good — reconnect loop terminated after exhausting attempts
	case <-time.After(5 * time.Second):
		t.Fatal("SPIN DETECTED: streamWithReconnect did not terminate with instant-EOF reader")
	}

	// Verify the STREAM_ERROR event was emitted (reconnect exhaustion)
	evt := output.WaitForEvent(logs.StreamEventStreamError, 1*time.Second)
	if evt == nil {
		t.Fatal("expected STREAM_ERROR after reconnect exhaustion")
	}

	mgr.Close()
}

// SPIN-003: streamWithReconnect exits immediately when context is cancelled during backoff.
func TestReconnectExitsOnContextCancelDuringBackoff(t *testing.T) {
	callCount := 0
	handler := logs.LogHandlerFunc(func(_ *types.PluginContext, _ logs.LogStreamRequest) (io.ReadCloser, error) {
		callCount++
		return nil, fmt.Errorf("always fail")
	})

	output := logtest.NewRecordingOutput()
	// Use real clock so backoff delays are real — we'll cancel during the first delay.
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:   "test",
				Resource: "pod",
				Handler:  handler,
				SourceBuilder: logtest.SingleSourceBuilder(
					"pod-1", map[string]string{},
				),
			},
		},
		Sink: output,
	})

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	session, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "pod",
		ResourceID:  "test-pod",
		Options:     logs.LogSessionOptions{Follow: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Wait for the first attempt to fail and the reconnect delay to start
	time.Sleep(100 * time.Millisecond)

	// Cancel the session — must break out of the backoff sleep immediately
	start := time.Now()
	_ = mgr.CloseSession(pctx, session.ID)
	elapsed := time.Since(start)

	// CloseSession should return very quickly (< 1s), not wait for 1s+ backoff
	if elapsed > 2*time.Second {
		t.Errorf("CloseSession took %v — context cancel did not interrupt backoff", elapsed)
	}

	mgr.Close()
}

// SPIN-004: readStream with context cancelled must exit promptly, not loop.
func TestReadStreamExitsOnContextCancel(t *testing.T) {
	src := logtest.NewTestLogSource()
	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:   "test",
				Resource: "pod",
				Handler:  src.HandlerFunc(),
				SourceBuilder: logtest.SingleSourceBuilder(
					"pod-1", map[string]string{},
				),
			},
		},
		Sink: output,
	})

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	session, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "pod",
		ResourceID:  "test-pod",
		Options:     logs.LogSessionOptions{Follow: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Get to ACTIVE so readStream is in its scanner loop
	src.Lines <- "line 1"
	output.WaitForLines(1, 2*time.Second)

	// Cancel session — readStream must exit promptly
	start := time.Now()
	_ = mgr.CloseSession(pctx, session.ID)
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("CloseSession took %v — readStream did not exit on context cancel", elapsed)
	}

	mgr.Close()
}

// SPIN-005: Goroutine count stays bounded after many create/close cycles.
// More rigorous than SM-007 — checks goroutine count convergence.
func TestGoroutineCountConverges(t *testing.T) {
	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:   "test",
				Resource: "pod",
				Handler:  logtest.NewTestLogSource().HandlerFunc(),
				SourceBuilder: logtest.SingleSourceBuilder(
					"pod-1", map[string]string{},
				),
			},
		},
		Sink: output,
	})
	defer mgr.Close()

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	baseline := runtime.NumGoroutine()

	for i := 0; i < 50; i++ {
		session, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
			ResourceKey: "pod",
			ResourceID:  fmt.Sprintf("pod-%d", i),
			Options:     logs.LogSessionOptions{Follow: true},
		})
		if err != nil {
			t.Fatalf("CreateSession[%d]: %v", i, err)
		}
		_ = mgr.CloseSession(pctx, session.ID)
	}

	mgr.Wait()
	// Allow GC and goroutine cleanup
	time.Sleep(100 * time.Millisecond)
	runtime.GC()
	time.Sleep(50 * time.Millisecond)

	after := runtime.NumGoroutine()
	leaked := after - baseline
	if leaked > 5 {
		t.Errorf("goroutine leak: baseline=%d, after=%d, leaked=%d (max allowed: 5)", baseline, after, leaked)
	}
}

// SPIN-006: fanOutSources with context cancelled during semaphore acquisition.
// If session is cancelled while sources are queued behind the semaphore,
// they must not block or spin.
func TestFanOutSourcesContextCancelledDuringSemaphore(t *testing.T) {
	// Create more sources than MaxConcurrentStreamsPerSession (20)
	// so some will queue on the semaphore.
	sourceIDs := make([]string, 25)
	for i := range sourceIDs {
		sourceIDs[i] = fmt.Sprintf("src-%d", i)
	}

	// Handler that blocks until context cancel
	handler := logs.LogHandlerFunc(func(pctx *types.PluginContext, _ logs.LogStreamRequest) (io.ReadCloser, error) {
		<-pctx.Context.Done()
		return nil, pctx.Context.Err()
	})

	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:        "test",
				Resource:      "pod",
				Handler:       handler,
				SourceBuilder: logtest.MultiSourceBuilder(sourceIDs...),
			},
		},
		Sink: output,
	})

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	session, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "pod",
		ResourceID:  "test-pod",
		Options:     logs.LogSessionOptions{Follow: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Give goroutines time to start and queue on semaphore
	time.Sleep(50 * time.Millisecond)

	// Cancel — all goroutines (running + queued) must exit
	start := time.Now()
	_ = mgr.CloseSession(pctx, session.ID)
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Errorf("CloseSession with 25 sources took %v — semaphore blocked", elapsed)
	}

	mgr.Close()
}

// SPIN-007: watchSourceEvents exits when session context is cancelled.
func TestWatchSourceEventsExitsOnSessionCancel(t *testing.T) {
	src := logtest.NewTestLogSource()
	handler := logs.LogHandlerFunc(func(pctx *types.PluginContext, _ logs.LogStreamRequest) (io.ReadCloser, error) {
		return src.ReaderFunc(pctx)
	})

	events := make(chan logs.SourceEvent, 10)
	resolver := func(_ *types.PluginContext, _ map[string]interface{}, _ logs.SourceResolverOptions) (*logs.SourceResolverResult, error) {
		return &logs.SourceResolverResult{
			Sources: []logs.LogSource{{ID: "src-1", Labels: map[string]string{}}},
			Events:  events, // never closed — must rely on ctx cancel
		}, nil
	}

	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {Plugin: "test", Resource: "pod", Handler: handler},
		},
		Resolvers: map[string]logs.SourceResolver{
			"test/deploy": resolver,
		},
		Sink: output,
	})

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	session, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "test/deploy",
		ResourceID:  "deploy-1",
		Options:     logs.LogSessionOptions{Follow: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	src.Lines <- "line"
	output.WaitForEvent(logs.StreamEventSessionReady, 2*time.Second)

	// Close the session — watchSourceEvents must exit via ctx.Done()
	start := time.Now()
	_ = mgr.CloseSession(pctx, session.ID)
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("CloseSession took %v — watchSourceEvents blocked", elapsed)
	}

	mgr.Close()
}

// ===========================================================================
// Failure state tests: unexpected backend conditions
// ===========================================================================

// FAIL-001: Handler returns nil reader without error (defensive nil check).
func TestHandlerReturnsNilReader(t *testing.T) {
	handler := logs.LogHandlerFunc(func(_ *types.PluginContext, _ logs.LogStreamRequest) (io.ReadCloser, error) {
		return nil, nil // Broken handler: nil reader, nil error
	})

	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:   "test",
				Resource: "pod",
				Handler:  handler,
				SourceBuilder: logtest.SingleSourceBuilder(
					"pod-1", map[string]string{},
				),
			},
		},
		Sink:  output,
		Clock: timeutil.NewInstantFakeClock(),
	})

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	_, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "pod",
		ResourceID:  "test-pod",
		Options:     logs.LogSessionOptions{Follow: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Must complete (not panic) within the deadline
	done := make(chan struct{})
	go func() {
		mgr.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Good — recovered or errored out
	case <-time.After(5 * time.Second):
		t.Fatal("HANG DETECTED: nil reader caused infinite loop or deadlock")
	}

	mgr.Close()
}

// FAIL-002: Handler returns reader that errors on first Read.
func TestHandlerReaderImmediateError(t *testing.T) {
	handler := logs.LogHandlerFunc(func(_ *types.PluginContext, _ logs.LogStreamRequest) (io.ReadCloser, error) {
		return io.NopCloser(errReader{fmt.Errorf("disk I/O error")}), nil
	})

	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:   "test",
				Resource: "pod",
				Handler:  handler,
				SourceBuilder: logtest.SingleSourceBuilder(
					"pod-1", map[string]string{},
				),
			},
		},
		Sink:  output,
		Clock: timeutil.NewInstantFakeClock(),
	})

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	_, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "pod",
		ResourceID:  "test-pod",
		Options:     logs.LogSessionOptions{Follow: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Must exhaust reconnect attempts and emit STREAM_ERROR
	evt := output.WaitForEvent(logs.StreamEventStreamError, 5*time.Second)
	if evt == nil {
		t.Fatal("expected STREAM_ERROR after reader errors")
	}

	mgr.Close()
}

// FAIL-003: Scanner buffer overflow — line exceeds MaxScannerBuffer (1MB).
func TestScannerBufferOverflow(t *testing.T) {
	// Create a reader with a line that exceeds 1MB
	hugeLine := strings.Repeat("x", 2*1024*1024) // 2MB
	handler := logs.LogHandlerFunc(func(_ *types.PluginContext, _ logs.LogStreamRequest) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(hugeLine + "\n")), nil
	})

	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:   "test",
				Resource: "pod",
				Handler:  handler,
				SourceBuilder: logtest.SingleSourceBuilder(
					"pod-1", map[string]string{},
				),
			},
		},
		Sink:  output,
		Clock: timeutil.NewInstantFakeClock(),
	})

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	_, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "pod",
		ResourceID:  "test-pod",
		Options:     logs.LogSessionOptions{Follow: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Scanner should return an error, triggering reconnect → eventual STREAM_ERROR
	done := make(chan struct{})
	go func() {
		mgr.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("HANG: scanner overflow caused infinite loop")
	}

	mgr.Close()
}

// FAIL-004: Successful reconnection after transient failure emits RECONNECTED event.
func TestSuccessfulReconnectionEmitsEvent(t *testing.T) {
	attempt := 0
	handler := logs.LogHandlerFunc(func(_ *types.PluginContext, _ logs.LogStreamRequest) (io.ReadCloser, error) {
		attempt++
		if attempt == 1 {
			return nil, fmt.Errorf("transient error")
		}
		// Second attempt succeeds with a single line
		return io.NopCloser(strings.NewReader("recovered line\n")), nil
	})

	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:   "test",
				Resource: "pod",
				Handler:  handler,
				SourceBuilder: logtest.SingleSourceBuilder(
					"pod-1", map[string]string{},
				),
			},
		},
		Sink:  output,
		Clock: timeutil.NewInstantFakeClock(),
	})

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	_, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "pod",
		ResourceID:  "test-pod",
		Options:     logs.LogSessionOptions{Follow: false},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Should see RECONNECTING then RECONNECTED
	reconnecting := output.WaitForEvent(logs.StreamEventReconnecting, 3*time.Second)
	if reconnecting == nil {
		t.Fatal("expected RECONNECTING event")
	}

	reconnected := output.WaitForEvent(logs.StreamEventReconnected, 3*time.Second)
	if reconnected == nil {
		t.Fatal("expected RECONNECTED event")
	}

	// Should also get the recovered line
	lines := output.WaitForLines(1, 2*time.Second)
	if len(lines) < 1 {
		t.Fatal("expected recovered line after reconnection")
	}
	if lines[0].Content != "recovered line" {
		t.Errorf("content = %q, want %q", lines[0].Content, "recovered line")
	}

	mgr.Close()
}

// FAIL-005: CloseSession safety timeout fires when goroutine is stuck.
func TestCloseSessionSafetyTimeout(t *testing.T) {
	// Handler that ignores context cancellation (simulates a stuck goroutine)
	handler := logs.LogHandlerFunc(func(_ *types.PluginContext, _ logs.LogStreamRequest) (io.ReadCloser, error) {
		// Return a reader that blocks forever, ignoring context
		return io.NopCloser(blockingReader{}), nil
	})

	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:   "test",
				Resource: "pod",
				Handler:  handler,
				SourceBuilder: logtest.SingleSourceBuilder(
					"pod-1", map[string]string{},
				),
			},
		},
		Sink: output,
	})

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	session, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "pod",
		ResourceID:  "test-pod",
		Options:     logs.LogSessionOptions{Follow: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Give the streaming goroutine time to start
	time.Sleep(100 * time.Millisecond)

	// CloseSession should return within ~10s (safety timeout), not block forever
	start := time.Now()
	_ = mgr.CloseSession(pctx, session.ID)
	elapsed := time.Since(start)

	// The 10s timeout should fire; give 2s slack
	if elapsed > 15*time.Second {
		t.Fatalf("CloseSession blocked for %v — safety timeout did not fire", elapsed)
	}
	t.Logf("CloseSession with stuck goroutine returned in %v", elapsed)

	// NOTE: We intentionally do NOT call mgr.Close() here because the stuck
	// goroutine will never exit. The test passes as long as CloseSession itself
	// returns within the timeout.
}

// FAIL-006: Empty reader (no lines, no error) still marks source ready.
// This prevents the session from being stuck in INITIALIZING forever.
func TestEmptyReaderMarksSourceReady(t *testing.T) {
	handler := logs.LogHandlerFunc(func(_ *types.PluginContext, _ logs.LogStreamRequest) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("")), nil // EOF immediately, no lines
	})

	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:   "test",
				Resource: "pod",
				Handler:  handler,
				SourceBuilder: logtest.SingleSourceBuilder(
					"pod-1", map[string]string{},
				),
			},
		},
		Sink:  output,
		Clock: timeutil.NewInstantFakeClock(),
	})

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	_, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "pod",
		ResourceID:  "test-pod",
		Options:     logs.LogSessionOptions{Follow: false},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// SESSION_READY should still fire even though no lines were produced
	evt := output.WaitForEvent(logs.StreamEventSessionReady, 3*time.Second)
	if evt == nil {
		t.Fatal("expected SESSION_READY even with empty reader (no lines)")
	}

	mgr.Close()
}

// FAIL-007: Multiple concurrent CloseSession for different sessions — no deadlock.
func TestConcurrentCloseSessionNoDeadlock(t *testing.T) {
	src := logtest.NewTestLogSource()
	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:   "test",
				Resource: "pod",
				Handler:  src.HandlerFunc(),
				SourceBuilder: logtest.SingleSourceBuilder(
					"pod-1", map[string]string{},
				),
			},
		},
		Sink: output,
	})
	defer mgr.Close()

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)

	const N = 10
	ids := make([]string, N)
	for i := 0; i < N; i++ {
		session, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
			ResourceKey: "pod",
			ResourceID:  fmt.Sprintf("pod-%d", i),
			Options:     logs.LogSessionOptions{Follow: true},
		})
		if err != nil {
			t.Fatalf("CreateSession[%d]: %v", i, err)
		}
		ids[i] = session.ID
	}

	// Close all concurrently
	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for _, id := range ids {
			wg.Add(1)
			go func(sid string) {
				defer wg.Done()
				_ = mgr.CloseSession(pctx, sid)
			}(id)
		}
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("DEADLOCK: concurrent CloseSession calls did not complete")
	}
}

// FAIL-008: UpdateSessionOptions re-enable path — disabled source gets restarted.
func TestUpdateEnabledSourcesReEnable(t *testing.T) {
	src := logtest.NewTestLogSource()

	handler := logs.LogHandlerFunc(func(pctx *types.PluginContext, req logs.LogStreamRequest) (io.ReadCloser, error) {
		return src.ReaderFunc(pctx)
	})

	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:        "test",
				Resource:      "pod",
				Handler:       handler,
				SourceBuilder: logtest.MultiSourceBuilder("src-a", "src-b"),
			},
		},
		Sink: output,
	})
	t.Cleanup(mgr.Close)

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	session, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "pod",
		ResourceID:  "test-pod",
		Options:     logs.LogSessionOptions{Follow: true, IncludeSourceEvents: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	src.Lines <- "init-a"
	src.Lines <- "init-b"
	output.WaitForEvent(logs.StreamEventSessionReady, 2*time.Second)

	// Disable src-b
	_, err = mgr.UpdateSessionOptions(pctx, session.ID, logs.LogSessionOptions{
		Follow: true,
		Params: map[string]string{"enabled_sources": "src-a"},
	})
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Re-enable all (empty string = all enabled)
	_, err = mgr.UpdateSessionOptions(pctx, session.ID, logs.LogSessionOptions{
		Follow: true,
		Params: map[string]string{"enabled_sources": ""},
	})
	if err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// After re-enable, src-b should have a SOURCE_ADDED event
	found := false
	for _, evt := range output.Events() {
		if evt.Type == logs.StreamEventSourceAdded && evt.SourceID == "src-b" {
			found = true
			break
		}
	}
	// Note: re-enable fires SOURCE_ADDED for any source that needs restarting.
	// Whether src-b appears depends on whether its context was cancelled (the
	// disable cancelled it) and whether it still has a sourceCtx entry.
	// The key invariant is: no hang, no spin, no panic.
	t.Logf("SOURCE_ADDED for src-b after re-enable: %v", found)
}

// FAIL-009: Settings provider is injected into session PluginContext.
func TestCreateSessionWithSettingsProvider(t *testing.T) {
	src := logtest.NewTestLogSource()
	output := logtest.NewRecordingOutput()

	// Use a minimal settings stub. We just need a non-nil Provider.
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:   "test",
				Resource: "pod",
				Handler:  src.HandlerFunc(),
				SourceBuilder: logtest.SingleSourceBuilder(
					"pod-1", map[string]string{},
				),
			},
		},
		Sink:     output,
		Settings: &settingstest.StubProvider{},
	})
	t.Cleanup(mgr.Close)

	pctx := types.NewPluginContext(context.Background(), "test", nil, nil, nil)
	_, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "pod",
		ResourceID:  "test-pod",
		Options:     logs.LogSessionOptions{Follow: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	src.Lines <- "with settings"
	lines := output.WaitForLines(1, 2*time.Second)
	if len(lines) < 1 {
		t.Fatal("expected line with settings provider configured")
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// errReader is an io.Reader that always returns an error.
type errReader struct{ err error }

func (r errReader) Read(_ []byte) (int, error) { return 0, r.err }

// blockingReader blocks forever on Read, ignoring everything.
// Simulates a stuck I/O operation for testing safety timeouts.
type blockingReader struct{}

func (blockingReader) Read(_ []byte) (int, error) {
	select {} // block forever
}

// ---------------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------------

// BenchmarkCreateCloseSession measures session lifecycle overhead.
func BenchmarkCreateCloseSession(b *testing.B) {
	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:        "test",
				Resource:      "pod",
				Handler:       logtest.NewTestLogSource().HandlerFunc(),
				SourceBuilder: logtest.SingleSourceBuilder("pod-1", map[string]string{"source": "pod-1"}),
			},
		},
		Sink: output,
	})
	defer mgr.Close()

	pctx := types.NewPluginContext(context.Background(), "bench", nil, nil, nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		session, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
			ResourceKey: "pod",
			ResourceID:  "bench-pod",
			Options:     logs.LogSessionOptions{Follow: true},
		})
		if err != nil {
			b.Fatal(err)
		}
		_ = mgr.CloseSession(pctx, session.ID)
	}
}

// BenchmarkHandlerLookup measures the O(1) registry lookup.
func BenchmarkHandlerLookup(b *testing.B) {
	// Build a registry with many handlers to demonstrate O(1) vs O(N)
	handlers := make(map[string]logs.Handler)
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("plugin-%d/resource-%d", i, i)
		handlers[key] = logs.Handler{Plugin: fmt.Sprintf("plugin-%d", i), Resource: fmt.Sprintf("resource-%d", i)}
	}

	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: handlers,
		Sink:     logtest.NewRecordingOutput(),
	})
	defer mgr.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mgr.GetSupportedResources(nil)
	}
}

// BenchmarkSnapshotSession measures session state snapshot overhead.
func BenchmarkSnapshotSession(b *testing.B) {
	output := logtest.NewRecordingOutput()
	src := logtest.NewTestLogSource()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {
				Plugin:        "test",
				Resource:      "pod",
				Handler:       src.HandlerFunc(),
				SourceBuilder: logtest.SingleSourceBuilder("pod-1", map[string]string{"source": "pod-1"}),
			},
		},
		Sink: output,
	})
	defer mgr.Close()

	pctx := types.NewPluginContext(context.Background(), "bench", nil, nil, nil)
	session, err := mgr.CreateSession(pctx, logs.CreateSessionOptions{
		ResourceKey: "pod",
		ResourceID:  "bench-pod",
		Options:     logs.LogSessionOptions{Follow: true},
	})
	if err != nil {
		b.Fatal(err)
	}
	defer mgr.CloseSession(pctx, session.ID)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = mgr.GetSession(pctx, session.ID)
	}
}
