package resourcev2

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/settings"
)

// shortAcquisition is the acquisition deadline used by tests that wait for it to
// pass. The setting's own floor is one second, so these tests replace the
// resolver on the controller instead of waiting real seconds. joinWindow is the
// deadline for the test that needs other callers to join a pending start before
// it passes; it only needs to dwarf goroutine scheduling latency.
const (
	shortAcquisition = 50 * time.Millisecond
	joinWindow       = 250 * time.Millisecond
)

// testWait bounds every wait for an outcome that must happen; it is never the
// duration of a successful run.
const testWait = 10 * time.Second

func shortAcquisitionController(t *testing.T, cfg Config[string]) *Controller[string] {
	t.Helper()
	return acquisitionController(t, cfg, shortAcquisition)
}

func acquisitionController(t *testing.T, cfg Config[string], timeout time.Duration) *Controller[string] {
	t.Helper()
	c := newTestController(t, cfg)
	c.acquisitionTimeout = func(context.Context) (time.Duration, error) { return timeout, nil }
	if _, err := c.LoadConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c
}

func waitForError(t *testing.T, name string, results <-chan error) error {
	t.Helper()
	select {
	case err := <-results:
		return err
	case <-time.After(testWait):
		t.Fatalf("%s did not return within %s", name, testWait)
		return nil
	}
}

// startAsync starts the "explicit" connection on its own goroutine.
func startAsync(c *Controller[string]) <-chan error {
	results := make(chan error, 1)
	go func() {
		_, err := c.StartConnection(context.Background(), "explicit")
		results <- err
	}()
	return results
}

// With a caller context that has no deadline and no plugin configuration, the
// context handed to CreateClient carries a finite deadline (the documented
// acquisition default, at most the 300 s maximum), so a hung CreateClient that
// honors its context ends the start instead of holding the pending slot until the
// creator cancels.
func TestStartConnectionAppliesDefaultAcquisitionDeadlineToCreateClient(t *testing.T) {
	cfg := controllerConfig()
	lifecycle := controllerLifecycle(&atomic.Int32{})
	var (
		sawDeadline bool
		deadline    time.Time
	)
	lifecycle.CreateClient = func(ctx context.Context, connection types.Connection) (*string, error) {
		deadline, sawDeadline = ctx.Deadline()
		client := connection.ID
		return &client, nil
	}
	cfg.Lifecycle = lifecycle
	c := newTestController(t, cfg)
	if _, err := c.LoadConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	if _, err := c.StartConnection(context.Background(), "explicit"); err != nil {
		t.Fatal(err)
	}
	if !sawDeadline {
		t.Fatal("CreateClient context has no deadline; a hung CreateClient holds the pending slot until the creator cancels")
	}
	remaining := deadline.Sub(began)
	want := time.Duration(defaultConnectionAcquisitionTimeoutSeconds) * time.Second
	if remaining <= 0 || remaining > want+time.Second {
		t.Fatalf("CreateClient deadline %v from start, want the %v default", remaining, want)
	}
}

// pluginSettings returns a plugin settings provider that declares the acquisition
// setting at the given value, or declares nothing.
func pluginSettings(t *testing.T, declared bool, seconds int) settings.Provider {
	t.Helper()
	setting := ConnectionAcquisitionTimeoutSetting()
	category := settings.Category{ID: "plugin", Settings: map[string]settings.Setting{}}
	if declared {
		category.Settings[setting.ID] = setting
	}
	provider := settings.NewProvider(settings.ProviderOpts{
		Logger: zap.NewNop().Sugar(), PluginID: "example", PluginSettings: []settings.Category{category},
	})
	if declared {
		if err := provider.SetSetting(setting.ID, seconds); err != nil {
			t.Fatal(err)
		}
	}
	return provider
}

// A provider that registers the setting chooses the bound; a provider whose
// settings do not declare it gets the default rather than a failed start.
func TestStartConnectionReadsAcquisitionDeadlineFromPluginSettings(t *testing.T) {
	for _, tc := range []struct {
		name     string
		declared bool
		seconds  int
		want     time.Duration
	}{
		{name: "declared", declared: true, seconds: 7, want: 7 * time.Second},
		{name: "undeclared", want: time.Duration(defaultConnectionAcquisitionTimeoutSeconds) * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := controllerConfig()
			lifecycle := controllerLifecycle(&atomic.Int32{})
			var deadline time.Time
			lifecycle.CreateClient = func(ctx context.Context, connection types.Connection) (*string, error) {
				deadline, _ = ctx.Deadline()
				client := connection.ID
				return &client, nil
			}
			cfg.Lifecycle = lifecycle
			c := newTestController(t, cfg)
			if _, err := c.LoadConnections(context.Background()); err != nil {
				t.Fatal(err)
			}
			ctx := WithSession(context.Background(), Session{PluginConfig: pluginSettings(t, tc.declared, tc.seconds)})
			began := time.Now()
			if _, err := c.StartConnection(ctx, "explicit"); err != nil {
				t.Fatal(err)
			}
			if got := deadline.Sub(began); got <= 0 || got > tc.want+time.Second || got < tc.want-time.Second {
				t.Fatalf("deadline %v from start, want about %v", got, tc.want)
			}
		})
	}
}

// The deadline bounds acquisition only. OnStart shares it; after a successful
// start nothing the client or its generation sees carries a deadline, a context
// retained from CreateClient stays live past the deadline, and it ends with the
// generation.
func TestAcquisitionDeadlineDoesNotOutliveAcquisition(t *testing.T) {
	cfg := controllerConfig()
	lifecycle := controllerLifecycle(&atomic.Int32{})
	var created, started, stopped context.Context
	var onStartDeadline bool
	lifecycle.CreateClient = func(ctx context.Context, connection types.Connection) (*string, error) {
		created = ctx
		client := connection.ID
		return &client, nil
	}
	lifecycle.OnStart = func(ctx context.Context, _ types.Connection, _ *string) error {
		started = ctx
		_, onStartDeadline = ctx.Deadline()
		return nil
	}
	lifecycle.OnStop = func(ctx context.Context, _ types.Connection, _ *string) error {
		stopped = ctx
		return nil
	}
	cfg.Lifecycle = lifecycle
	// The start must succeed well inside the deadline, which must then pass.
	const deadline = 200 * time.Millisecond
	c := acquisitionController(t, cfg, deadline)
	if _, err := c.StartConnection(context.Background(), "explicit"); err != nil {
		t.Fatal(err)
	}
	if !onStartDeadline {
		t.Fatal("OnStart context has no acquisition deadline")
	}
	var leased context.Context
	route := AccessRoute{Connection: "explicit"}
	if err := c.WithClient(context.Background(), route, func(ctx context.Context, _ *string) error {
		leased = ctx
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Let the acquisition deadline pass; a context bound to it would end here.
	select {
	case <-created.Done():
		t.Fatalf("context retained from CreateClient ended after a successful start: %v", created.Err())
	case <-started.Done():
		t.Fatalf("context retained from OnStart ended after a successful start: %v", started.Err())
	case <-time.After(2 * deadline):
	}
	for name, ctx := range map[string]context.Context{"CreateClient": created, "OnStart": started, "lease": leased} {
		if reported, ok := ctx.Deadline(); ok {
			t.Fatalf("%s context still reports deadline %v after a successful start", name, reported)
		}
	}
	if _, err := c.StopConnection(context.Background(), "explicit"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-created.Done():
	case <-time.After(testWait):
		t.Fatal("context retained from CreateClient did not end with the generation")
	}
	if !errors.Is(created.Err(), context.Canceled) {
		t.Fatalf("retained context ended with %v, want cancellation", created.Err())
	}
	if _, ok := stopped.Deadline(); ok {
		t.Fatal("OnStop context carries a deadline")
	}
}

// A hook that honors its context returns within the deadline: the start fails
// with TIMEOUT, status is FAILED, the slot is free and a later start works.
func TestStartConnectionTimeoutFreesSlotForContextHonoringCreateClient(t *testing.T) {
	cfg := controllerConfig()
	var destroyed atomic.Int32
	lifecycle := controllerLifecycle(&destroyed)
	var hang atomic.Bool
	hang.Store(true)
	lifecycle.CreateClient = func(ctx context.Context, connection types.Connection) (*string, error) {
		if hang.Load() {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		client := connection.ID
		return &client, nil
	}
	cfg.Lifecycle = lifecycle
	c := shortAcquisitionController(t, cfg)
	began := time.Now()
	err := waitForError(t, "StartConnection", startAsync(c))
	assertControllerCode(t, err, "TIMEOUT")
	if elapsed := time.Since(began); elapsed > testWait {
		t.Fatalf("start took %v", elapsed)
	}
	status, statusErr := c.GetConnectionStatus(context.Background(), "explicit")
	if statusErr != nil || status.Status != types.ConnectionStatusFailed {
		t.Fatalf("status after timeout = %v, %v", status.Status, statusErr)
	}
	hang.Store(false)
	if _, retryErr := c.StartConnection(context.Background(), "explicit"); retryErr != nil {
		t.Fatalf("start after timeout: %v", retryErr)
	}
	status, _ = c.GetConnectionStatus(context.Background(), "explicit")
	if status.Status != types.ConnectionStatusConnected {
		t.Fatalf("status after retry = %v", status.Status)
	}
}

// OnStart is part of acquisition: a hung OnStart times out the same way, and the
// client CreateClient already returned is destroyed exactly once.
func TestStartConnectionTimeoutCoversOnStart(t *testing.T) {
	cfg := controllerConfig()
	var destroyed atomic.Int32
	lifecycle := controllerLifecycle(&destroyed)
	lifecycle.OnStart = func(ctx context.Context, _ types.Connection, _ *string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	cfg.Lifecycle = lifecycle
	c := shortAcquisitionController(t, cfg)
	assertControllerCode(t, waitForError(t, "StartConnection", startAsync(c)), "TIMEOUT")
	deadline := time.Now().Add(testWait)
	for destroyed.Load() != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("destroy calls = %d, want 1", destroyed.Load())
		}
		time.Sleep(time.Millisecond)
	}
}

type lateClientFixture struct {
	cfg       Config[string]
	entered   chan struct{}
	release   chan struct{}
	destroyed chan string
	calls     atomic.Int32
}

// newLateClientFixture builds a lifecycle whose first CreateClient ignores its
// context until release closes, then returns "late". Later calls return "fresh".
func newLateClientFixture() *lateClientFixture {
	f := &lateClientFixture{
		cfg:       controllerConfig(),
		entered:   make(chan struct{}),
		release:   make(chan struct{}),
		destroyed: make(chan string, 4),
	}
	lifecycle := controllerLifecycle(&atomic.Int32{})
	lifecycle.CreateClient = func(_ context.Context, _ types.Connection) (*string, error) {
		if f.calls.Add(1) == 1 {
			close(f.entered)
			<-f.release
			client := "late"
			return &client, nil
		}
		client := "fresh"
		return &client, nil
	}
	lifecycle.DestroyClient = func(_ context.Context, client *string) error {
		f.destroyed <- *client
		return nil
	}
	f.cfg.Lifecycle = lifecycle
	return f
}

func (f *lateClientFixture) expectDestroyed(t *testing.T, want string) {
	t.Helper()
	select {
	case got := <-f.destroyed:
		if got != want {
			t.Fatalf("destroyed %q, want %q", got, want)
		}
	case <-time.After(testWait):
		t.Fatalf("client %q was not destroyed", want)
	}
}

// A CreateClient that ignores its context cannot be stopped, but it cannot hold
// the slot either: the creator and every waiter are released at the deadline
// with TIMEOUT, a later start succeeds while the old call is still running, and
// the client the old call eventually returns is destroyed once and never used.
func TestStartConnectionTimeoutReleasesWaitersForContextIgnoringCreateClient(t *testing.T) {
	f := newLateClientFixture()
	c := acquisitionController(t, f.cfg, joinWindow)
	creator := startAsync(c)
	<-f.entered
	joiner := startAsync(c)
	checker := make(chan error, 1)
	go func() { _, err := c.CheckConnection(context.Background(), "explicit"); checker <- err }()

	assertControllerCode(t, waitForError(t, "creator", creator), "TIMEOUT")
	assertControllerCode(t, waitForError(t, "joiner", joiner), "TIMEOUT")
	if err := waitForError(t, "CheckConnection", checker); err == nil {
		t.Fatal("CheckConnection succeeded against a failed start")
	}
	if got := f.calls.Load(); got != 1 {
		t.Fatalf("CreateClient calls = %d, the joiner must not start its own", got)
	}

	// The first call is still blocked; a fresh start must not wait for it.
	if _, err := c.StartConnection(context.Background(), "explicit"); err != nil {
		t.Fatalf("start while the abandoned CreateClient still runs: %v", err)
	}
	close(f.release)
	f.expectDestroyed(t, "late")
	var used string
	route := AccessRoute{Connection: "explicit"}
	if err := c.WithClient(context.Background(), route, func(_ context.Context, client *string) error {
		used = *client
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if used != "fresh" {
		t.Fatalf("active client = %q, want the one from the later start", used)
	}
	if _, err := c.StopConnection(context.Background(), "explicit"); err != nil {
		t.Fatal(err)
	}
	f.expectDestroyed(t, "fresh")
	select {
	case extra := <-f.destroyed:
		t.Fatalf("client %q destroyed twice", extra)
	case <-time.After(4 * shortAcquisition):
	}
}

// StopConnection during a hung, context-ignoring start returns once the deadline
// frees the slot instead of waiting for CreateClient.
func TestStopConnectionReturnsAfterAcquisitionDeadlineForContextIgnoringCreateClient(t *testing.T) {
	f := newLateClientFixture()
	c := shortAcquisitionController(t, f.cfg)
	creator := startAsync(c)
	<-f.entered
	stopped := make(chan error, 1)
	go func() { _, err := c.StopConnection(context.Background(), "explicit"); stopped <- err }()
	if err := waitForError(t, "StopConnection", stopped); err != nil {
		t.Fatalf("StopConnection: %v", err)
	}
	if err := waitForError(t, "creator", creator); err == nil {
		t.Fatal("start succeeded after its connection was stopped")
	}
	close(f.release)
	f.expectDestroyed(t, "late")
}

// A start that is not abandoned is unaffected by the late-client path: hooks
// that return in time deliver their client exactly once to every joiner.
func TestStartConnectionWithinDeadlineDeliversClientOnce(t *testing.T) {
	cfg := controllerConfig()
	var destroyed atomic.Int32
	cfg.Lifecycle = controllerLifecycle(&destroyed)
	c := newTestController(t, cfg)
	if _, err := c.LoadConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.StartConnection(context.Background(), "explicit"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if _, err := c.StopConnection(context.Background(), "explicit"); err != nil {
		t.Fatal(err)
	}
	if got := destroyed.Load(); got != 1 {
		t.Fatalf("destroy calls = %d, want 1", got)
	}
}
