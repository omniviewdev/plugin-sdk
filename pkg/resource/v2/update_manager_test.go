package resourcev2

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

type controllerUpdateSink struct {
	add    func(WatchAddPayload)
	update func(WatchUpdatePayload)
	remove func(WatchDeletePayload)
	state  func(WatchStateEvent)
}

func (s controllerUpdateSink) OnAdd(p WatchAddPayload) {
	if s.add != nil {
		s.add(p)
	}
}
func (s controllerUpdateSink) OnUpdate(p WatchUpdatePayload) {
	if s.update != nil {
		s.update(p)
	}
}
func (s controllerUpdateSink) OnDelete(p WatchDeletePayload) {
	if s.remove != nil {
		s.remove(p)
	}
}
func (s controllerUpdateSink) OnStateChange(e WatchStateEvent) {
	if s.state != nil {
		s.state(e)
	}
}

func controllerUpdateConfig() Config[string] {
	cfg := controllerConfig()
	cfg.Registrations = []Registration[string]{{Type: "opaque/type", Watch: func(ctx context.Context, _ *string, _ ReadTarget, _ Representation, _ WatchEventSink) error {
		<-ctx.Done()
		return ctx.Err()
	}}}
	return cfg
}

func TestControllerWatchOnlyCatalogAndExplicitTarget(t *testing.T) {
	cfg := controllerConfig()
	started := make(chan ReadTarget, 1)
	cfg.Registrations = []Registration[string]{{Type: "opaque/type", Watch: func(ctx context.Context, _ *string, target ReadTarget, representation Representation, sink WatchEventSink) error {
		if representation.ID != target.Representation {
			return errors.New("representation binding changed")
		}
		started <- target
		sink.OnStateChange(WatchStateEvent{State: WatchStateSynced, Count: Count{Kind: "unknown"}})
		<-ctx.Done()
		return ctx.Err()
	}}}
	c := newTestController(t, cfg)
	catalog, err := c.Catalog(context.Background(), CatalogRequest{Route: controllerTarget().Route})
	if err != nil {
		t.Fatal(err)
	}
	caps := catalog.Types[0].Capabilities
	if len(caps) != 1 || caps[0].Operation != OperationUpdate || caps[0].Guarantee != BestEffortGuarantee {
		t.Fatal("watch-only registration fabricated finite or strong continuity support")
	}
	if err := c.StartConnectionWatch(acceptedControllerContext(t, context.Background(), c), []ReadTarget{controllerTarget()}); err != nil {
		t.Fatal(err)
	}
	target := <-started
	if target.Authority != "authority" || target.Route.Connection != "explicit" {
		t.Fatal("host target identity changed")
	}
	changed := target
	changed.Authority = "different"
	assertControllerCode(t, c.EnsureResourceWatch(acceptedControllerContext(t, context.Background(), c), changed), "RESET_REQUIRED")
	if err := c.StopConnectionWatch(acceptedControllerContext(t, context.Background(), c), target.Route); err != nil {
		t.Fatal(err)
	}
	if c.HasWatch(acceptedControllerContext(t, context.Background(), c), target.Route) {
		t.Fatal("stopped connection watch remains registered")
	}
}

func TestControllerUpdateReadinessReplaysBeforeLiveStates(t *testing.T) {
	c := newTestController(t, controllerUpdateConfig())
	watch := &updateWatch{target: controllerTarget()}
	c.updates.publishState(watch, WatchStateEvent{State: WatchStateSyncing, Count: Count{Kind: "unknown"}})
	states := make(chan WatchStateEvent, 3)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	var readyCalls atomic.Int32
	go func() {
		finished <- c.ListenForEvents(acceptedControllerContext(t, ctx, c), controllerUpdateSink{state: func(e WatchStateEvent) { states <- e }}, func() error {
			readyCalls.Add(1)
			c.updates.publishState(watch, WatchStateEvent{State: WatchStateSynced, Count: Count{Kind: "unknown"}})
			return nil
		})
	}()
	first, second := <-states, <-states
	if first.State != WatchStateSyncing || second.State != WatchStateSynced || readyCalls.Load() != 1 {
		t.Fatal("registration readiness or replay order changed")
	}
	if first.Guarantee != BestEffortGuarantee || first.Route.Connection != "explicit" {
		t.Fatal("state provenance missing")
	}
	cancel()
	assertControllerCode(t, <-finished, "CANCELLED")
	if len(c.updates.snapshot()) != 0 {
		t.Fatal("listener survives cancellation")
	}
}

func TestControllerUpdateRejectedReadinessRetainsReplay(t *testing.T) {
	c := newTestController(t, controllerUpdateConfig())
	watch := &updateWatch{target: controllerTarget()}
	c.updates.publishState(watch, WatchStateEvent{State: WatchStateSynced, Count: Count{Kind: "unknown"}})
	rejected := errors.New("listener not ready")
	err := c.ListenForEvents(acceptedControllerContext(t, context.Background(), c), controllerUpdateSink{}, func() error { return rejected })
	if !errors.Is(err, rejected) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	delivered := make(chan WatchStateEvent, 1)
	finished := make(chan error, 1)
	go func() {
		finished <- c.ListenForEvents(acceptedControllerContext(t, ctx, c), controllerUpdateSink{state: func(event WatchStateEvent) { delivered <- event }}, func() error { return nil })
	}()
	if (<-delivered).State != WatchStateSynced {
		t.Fatal("rejected registration lost replay history")
	}
	cancel()
	<-finished
}

func TestControllerUpdateReplayOverflowFailsBeforeReadiness(t *testing.T) {
	c := newTestController(t, controllerUpdateConfig())
	watch := &updateWatch{target: controllerTarget()}
	for range maxPendingUpdateStates + 1 {
		c.updates.publishState(watch, WatchStateEvent{State: WatchStateSyncing, Count: Count{Kind: "unknown"}})
	}
	err := c.ListenForEvents(acceptedControllerContext(t, context.Background(), c), controllerUpdateSink{}, func() error { t.Fatal("overflow reached readiness"); return nil })
	assertControllerCode(t, err, "RESOURCE_LIMIT")
}

func TestControllerUpdateRetriesAndStopDrainsBeforeDestroy(t *testing.T) {
	cfg := controllerConfig()
	var destroyed, calls atomic.Int32
	var watchFinished atomic.Bool
	cfg.Lifecycle = controllerLifecycle(&destroyed)
	baseDestroy := cfg.Lifecycle.DestroyClient
	cfg.Lifecycle.DestroyClient = func(ctx context.Context, client *string) error {
		if !watchFinished.Load() {
			return errors.New("client destroyed before watch teardown")
		}
		return baseDestroy(ctx, client)
	}
	started := make(chan struct{})
	cfg.Registrations = []Registration[string]{{Type: "opaque/type", Watch: func(ctx context.Context, client *string, _ ReadTarget, _ Representation, _ WatchEventSink) error {
		if client == nil || *client != "explicit" {
			return errors.New("wrong client")
		}
		if calls.Add(1) == 1 {
			return errors.New("retryable source failure")
		}
		close(started)
		<-ctx.Done()
		watchFinished.Store(true)
		return ctx.Err()
	}}}
	c := newTestController(t, cfg)
	c.updates.baseBackoff = time.Millisecond
	startTestConnection(t, c)
	if err := c.EnsureResourceWatch(acceptedControllerContext(t, context.Background(), c), controllerTarget()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("watch did not retry")
	}
	if _, err := c.StopConnection(context.Background(), "explicit"); err != nil {
		t.Fatal(err)
	}
	if destroyed.Load() != 1 || calls.Load() != 2 {
		t.Fatalf("destroy=%d calls=%d", destroyed.Load(), calls.Load())
	}
}

func TestControllerUpdateRejectsEscapedNativeRecord(t *testing.T) {
	cfg := controllerConfig()
	release := make(chan struct{})
	cfg.Registrations = []Registration[string]{{Type: "opaque/type", Watch: func(ctx context.Context, _ *string, _ ReadTarget, _ Representation, sink WatchEventSink) error {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		record := controllerRecord()
		record.Reference.Address.Authority = "escaped"
		sink.OnAdd(WatchAddPayload{Record: record})
		<-ctx.Done()
		return ctx.Err()
	}}}
	c := newTestController(t, cfg)
	if err := c.EnsureResourceWatch(acceptedControllerContext(t, context.Background(), c), controllerTarget()); err != nil {
		t.Fatal(err)
	}
	c.updates.mu.Lock()
	watch := c.updates.watches["explicit"]["opaque/type"]
	c.updates.mu.Unlock()
	close(release)
	<-watch.done
	summary, err := c.GetWatchState(acceptedControllerContext(t, context.Background(), c), controllerTarget().Route)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Resources["opaque/type"] != WatchStateFailed {
		t.Fatal("invalid record did not fail source")
	}
}

func TestControllerUpdateSyncPolicyAndOptionalCallback(t *testing.T) {
	cfg := controllerConfig()
	cfg.Registrations = []Registration[string]{{Type: "opaque/type", SyncPolicy: SyncOnFirstQuery, Get: func(context.Context, *string, GetRequest) (Record, error) { return controllerRecord(), nil }, Watch: func(ctx context.Context, _ *string, _ ReadTarget, _ Representation, _ WatchEventSink) error {
		<-ctx.Done()
		return ctx.Err()
	}}}
	c := newTestController(t, cfg)
	if err := c.StartConnectionWatch(acceptedControllerContext(t, context.Background(), c), []ReadTarget{controllerTarget()}); err != nil {
		t.Fatal(err)
	}
	if c.HasWatch(acceptedControllerContext(t, context.Background(), c), controllerTarget().Route) {
		t.Fatal("on-first-query source started eagerly")
	}
	if _, err := c.Get(acceptedControllerContext(t, context.Background(), c), GetRequest{Route: controllerTarget().Route, Reference: ResourceReference{Address: controllerAddress()}, Representation: "document"}); err != nil {
		t.Fatal(err)
	}
	if running, err := c.IsResourceWatchRunning(acceptedControllerContext(t, context.Background(), c), controllerTarget().Route, "opaque/type"); err != nil || !running {
		t.Fatalf("watch not running: %v", err)
	}
}

func TestControllerApplyConnectionsRetiresChangedAndRemovedClients(t *testing.T) {
	cfg := controllerConfig()
	var destroyed atomic.Int32
	cfg.Lifecycle = controllerLifecycle(&destroyed)
	c := newTestController(t, cfg)
	startTestConnection(t, c)
	if _, err := c.StartConnection(context.Background(), "other"); err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyConnections(context.Background(), []types.Connection{{ID: "explicit", Name: "Explicit"}, {ID: "other", Name: "Changed", Data: map[string]any{"endpoint": "https://changed.invalid"}}}); err != nil {
		t.Fatal(err)
	}
	if destroyed.Load() != 1 {
		t.Fatal("unchanged client retired or changed client survived")
	}
	if err := c.DeleteConnection(context.Background(), "explicit"); err != nil {
		t.Fatal(err)
	}
	if destroyed.Load() != 2 {
		t.Fatal("deleted connection client survived")
	}
	connections, err := c.ListConnections(context.Background())
	if err != nil || len(connections) != 1 || connections[0].ID != "other" {
		t.Fatalf("bad inventory: %v %v", connections, err)
	}
	if _, err := c.UpdateConnection(context.Background(), types.Connection{ID: "other", Name: "Updated"}); err != nil {
		t.Fatal(err)
	}
	connection, err := c.GetConnection(context.Background(), "other")
	if err != nil || connection.Name != "Updated" {
		t.Fatal("connection update not applied")
	}
}

func TestControllerMissingWatchNeverAcknowledgesListenerReadiness(t *testing.T) {
	c := newTestController(t, controllerConfig())
	err := c.ListenForEvents(acceptedControllerContext(t, context.Background(), c), controllerUpdateSink{}, func() error { t.Fatal("missing watch acknowledged listener readiness"); return nil })
	assertControllerCode(t, err, "UNSUPPORTED")
}

func TestControllerUpdateListenerPanicRetiresRegistration(t *testing.T) {
	c := newTestController(t, controllerUpdateConfig())
	c.updates.publishState(&updateWatch{target: controllerTarget()}, WatchStateEvent{State: WatchStateSynced, Count: Count{Kind: "unknown"}})
	err := c.ListenForEvents(acceptedControllerContext(t, context.Background(), c), controllerUpdateSink{state: func(WatchStateEvent) { panic("private callback data") }}, func() error { return nil })
	assertControllerCode(t, err, "INTERNAL")
	if len(c.updates.snapshot()) != 0 {
		t.Fatal("panicked listener survives return")
	}
}

func TestControllerListenerCancellationDrainsPausedCallback(t *testing.T) {
	c := newTestController(t, controllerUpdateConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- c.ListenForEvents(acceptedControllerContext(t, ctx, c), controllerUpdateSink{}, func() error { close(ready); return nil })
	}()
	<-ready
	listeners := c.updates.snapshot()
	if len(listeners) != 1 {
		t.Fatal("listener not installed before readiness")
	}
	listener := listeners[0]
	entered, release, callbackDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(callbackDone)
		listener.call(func() { close(entered); <-release })
	}()
	<-entered
	cancel()
	deadline := time.After(3 * time.Second)
	for !listener.retired.Load() {
		select {
		case <-deadline:
			close(release)
			t.Fatal("listener did not retire")
		default:
			runtime.Gosched()
		}
	}
	select {
	case err := <-finished:
		close(release)
		<-callbackDone
		t.Fatalf("listener returned before admitted callback drained: %v", err)
	default:
	}
	listener.call(func() { t.Error("retired listener admitted a late callback") })
	close(release)
	<-callbackDone
	assertControllerCode(t, <-finished, "CANCELLED")
}

func TestControllerRestartReplacesDrainedUpdateTarget(t *testing.T) {
	for _, change := range []string{"authority", "representation", "scope"} {
		t.Run(change, func(t *testing.T) {
			cfg := controllerUpdateConfig()
			base := cfg.Catalog
			cfg.Catalog = func(ctx context.Context, request CatalogRequest) (Catalog, error) {
				catalog, err := base(ctx, request)
				descriptor := &catalog.Types[0]
				descriptor.IdentityDimensions = []Dimension{{Name: "region"}}
				descriptor.QueryDimensions = []Dimension{{Name: "region"}}
				alternate := controllerRepresentation()
				alternate.ID = "alternate"
				descriptor.Representations = append(descriptor.Representations, alternate)
				descriptor.Capabilities = []Capability{{Operation: OperationUpdate, Scope: QueryScope{{Dimension: "region", Match: ScopeAny}}, Guarantee: BestEffortGuarantee}}
				return catalog, err
			}
			started := make(chan ReadTarget, 1)
			cfg.Registrations[0].Watch = func(ctx context.Context, _ *string, target ReadTarget, _ Representation, _ WatchEventSink) error {
				started <- target
				<-ctx.Done()
				return ctx.Err()
			}
			c := newTestController(t, cfg)
			target := controllerTarget()
			east := []string{"east"}
			target.Scope = QueryScope{{Dimension: "region", Match: ScopeOneOf, Values: &east}}
			if err := c.EnsureResourceWatch(acceptedControllerContext(t, context.Background(), c), target); err != nil {
				t.Fatal(err)
			}
			<-started
			switch change {
			case "authority":
				target.Authority = "replacement"
			case "representation":
				target.Representation = "alternate"
			case "scope":
				west := []string{"west"}
				target.Scope = QueryScope{{Dimension: "region", Match: ScopeOneOf, Values: &west}}
			}
			if err := c.RestartResourceWatch(acceptedControllerContext(t, context.Background(), c), target); err != nil {
				t.Fatal(err)
			}
			replacement := <-started
			if replacement.Authority != target.Authority || replacement.Representation != target.Representation || !scopeContains(replacement.Scope, target.Scope) || !scopeContains(target.Scope, replacement.Scope) {
				t.Fatal("restart retained a drained target")
			}
		})
	}
}
