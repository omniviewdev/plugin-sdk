package resourcev2

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// farDeadline is far beyond testWait: a start that returns inside testWait was
// released by something other than its acquisition deadline.
const farDeadline = time.Hour

// releaseOnCleanup unblocks the first CreateClient when the test ends, so a test
// that fails while the call is still blocked does not leave Close waiting for it.
// Register it after the controller: cleanups run last in, first out.
func (f *lateClientFixture) releaseOnCleanup(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		select {
		case <-f.release:
		default:
			close(f.release)
		}
	})
}

// recordStarts makes OnStart note which clients it was called for.
func (f *lateClientFixture) recordStarts() *sync.Map {
	var started sync.Map
	f.cfg.Lifecycle.OnStart = func(_ context.Context, _ types.Connection, client *string) error {
		started.Store(*client, struct{}{})
		return nil
	}
	return &started
}

// assertLateClientNeverStarted fails when OnStart ran for the client the first,
// context-ignoring CreateClient of the fixture returns after its start was abandoned.
func assertLateClientNeverStarted(t *testing.T, started *sync.Map) {
	t.Helper()
	if _, ok := started.Load("late"); ok {
		t.Fatal("OnStart ran for \"late\", a client whose start had already been abandoned")
	}
}

// A CreateClient that returns after its start was abandoned produces a client
// nothing can use: the SDK destroys it and must not call OnStart for it, with an
// expired context, while the next generation of the connection may be live.
func TestStartConnectionTimeoutNeverStartsAbandonedClient(t *testing.T) {
	f := newLateClientFixture()
	started := f.recordStarts()
	c := shortAcquisitionController(t, f.cfg)
	f.releaseOnCleanup(t)
	creator := startAsync(c)
	<-f.entered
	assertControllerCode(t, waitForError(t, "creator", creator), "TIMEOUT")
	close(f.release)
	f.expectDestroyed(t, "late")
	assertLateClientNeverStarted(t, started)
}

// Cancelling the creator ends a start whose CreateClient ignores the
// cancellation, without waiting for the acquisition deadline. The client it
// returns later is destroyed once and never started.
func TestStartConnectionCancelReleasesCreatorForContextIgnoringCreateClient(t *testing.T) {
	f := newLateClientFixture()
	started := f.recordStarts()
	c := acquisitionController(t, f.cfg, farDeadline)
	f.releaseOnCleanup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	creator := make(chan error, 1)
	go func() { _, err := c.StartConnection(ctx, "explicit"); creator <- err }()
	<-f.entered
	cancel()
	assertControllerCode(t, waitForError(t, "creator", creator), "CANCELLED")
	status, err := c.GetConnectionStatus(context.Background(), "explicit")
	if err != nil || status.Status != types.ConnectionStatusDisconnected {
		t.Fatalf("status after a cancelled start = %v, %v; cancellation is not a failure", status.Status, err)
	}
	// The slot is free: a later start does not wait for the abandoned call.
	if _, startErr := c.StartConnection(context.Background(), "explicit"); startErr != nil {
		t.Fatalf("start after the cancelled one: %v", startErr)
	}
	close(f.release)
	f.expectDestroyed(t, "late")
	assertLateClientNeverStarted(t, started)
}

// StopConnection cancels the generation, which releases a start whose
// CreateClient ignores the cancellation. It does not wait for the deadline.
func TestStopConnectionReleasesContextIgnoringCreateClientWithoutWaitingForTheDeadline(t *testing.T) {
	f := newLateClientFixture()
	started := f.recordStarts()
	c := acquisitionController(t, f.cfg, farDeadline)
	f.releaseOnCleanup(t)
	creator := startAsync(c)
	<-f.entered
	stopped := make(chan error, 1)
	go func() { _, err := c.StopConnection(context.Background(), "explicit"); stopped <- err }()
	if err := waitForError(t, "StopConnection", stopped); err != nil {
		t.Fatalf("StopConnection: %v", err)
	}
	assertControllerCode(t, waitForError(t, "creator", creator), "CANCELLED")
	close(f.release)
	f.expectDestroyed(t, "late")
	assertLateClientNeverStarted(t, started)
}

// Close cancels every generation, which releases a start whose CreateClient
// ignores the cancellation. Close does not wait for the deadline or for the
// abandoned call; the client that call returns later is still destroyed.
func TestCloseReleasesContextIgnoringCreateClientWithoutWaitingForTheDeadline(t *testing.T) {
	f := newLateClientFixture()
	started := f.recordStarts()
	c := acquisitionController(t, f.cfg, farDeadline)
	f.releaseOnCleanup(t)
	creator := startAsync(c)
	<-f.entered
	closed := make(chan error, 1)
	go func() { closed <- c.Close() }()
	if err := waitForError(t, "Close", closed); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertControllerCode(t, waitForError(t, "creator", creator), "CANCELLED")
	close(f.release)
	f.expectDestroyed(t, "late")
	assertLateClientNeverStarted(t, started)
}

// A CreateClient that honors the cancellation but needs a moment to unwind is
// not abandoned. Its client is torn down before StartConnection returns, as for
// any failed start, so a restart cannot overlap the teardown.
func TestStartConnectionCancelAwaitsContextHonoringCreateClientThatUnwindsSlowly(t *testing.T) {
	cfg := controllerConfig()
	var destroyed atomic.Int32
	lifecycle := controllerLifecycle(&destroyed)
	entered := make(chan struct{})
	lifecycle.CreateClient = func(ctx context.Context, _ types.Connection) (*string, error) {
		close(entered)
		<-ctx.Done()
		time.Sleep(100 * time.Millisecond)
		client := "partial"
		return &client, ctx.Err()
	}
	cfg.Lifecycle = lifecycle
	c := acquisitionController(t, cfg, farDeadline)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	creator := make(chan error, 1)
	go func() { _, err := c.StartConnection(ctx, "explicit"); creator <- err }()
	<-entered
	cancel()
	assertControllerCode(t, waitForError(t, "creator", creator), "CANCELLED")
	if got := destroyed.Load(); got != 1 {
		t.Fatalf("destroy calls = %d when StartConnection returned, want 1", got)
	}
}

// The TIMEOUT a user reads names the limit and says the plugin's connection
// timeout setting can raise it. It is a classified error that does not wrap
// context.DeadlineExceeded: Controller.classify drops the sentence of one that does.
func TestStartConnectionTimeoutErrorNamesTheLimitAndTheSetting(t *testing.T) {
	cfg := controllerConfig()
	lifecycle := controllerLifecycle(&atomic.Int32{})
	lifecycle.CreateClient = func(ctx context.Context, _ types.Connection) (*string, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	cfg.Lifecycle = lifecycle
	c := shortAcquisitionController(t, cfg)
	_, err := c.StartConnection(context.Background(), "explicit")
	assertControllerCode(t, err, "TIMEOUT")
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("the timeout wraps context.DeadlineExceeded, which makes classify drop its sentence")
	}
	var typed *ResourceOperationError
	if !errors.As(err, &typed) {
		t.Fatalf("timeout is not a classified error: %v", err)
	}
	const want = "Connecting did not finish within 50ms; the plugin's connection timeout setting can raise this limit."
	if typed.Message != want {
		t.Fatalf("timeout sentence = %q, want %q", typed.Message, want)
	}
}

// The context CreateClient receives is canceled with DeadlineExceeded when the
// deadline passes, not with Canceled at some later point.
func TestStartConnectionTimeoutCancelsHookContextWithDeadlineExceeded(t *testing.T) {
	cfg := controllerConfig()
	lifecycle := controllerLifecycle(&atomic.Int32{})
	seen := make(chan error, 1)
	lifecycle.CreateClient = func(ctx context.Context, _ types.Connection) (*string, error) {
		<-ctx.Done()
		seen <- ctx.Err()
		return nil, ctx.Err()
	}
	cfg.Lifecycle = lifecycle
	c := shortAcquisitionController(t, cfg)
	began := time.Now()
	assertControllerCode(t, waitForError(t, "StartConnection", startAsync(c)), "TIMEOUT")
	if got := waitForError(t, "CreateClient", seen); !errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("hook context ended with %v, want DeadlineExceeded", got)
	}
	if elapsed := time.Since(began); elapsed < shortAcquisition {
		t.Fatalf("start failed after %v, before its %v deadline", elapsed, shortAcquisition)
	}
}
