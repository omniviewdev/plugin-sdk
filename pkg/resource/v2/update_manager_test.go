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
	cfg.Registrations = []Registration[string]{
		{
			Type: "opaque/type",
			Watch: func(ctx context.Context, _ *string, _ ReadTarget, _ Representation, _ WatchEventSink) error {
				<-ctx.Done()
				return ctx.Err()
			},
		},
	}
	return cfg
}

func TestControllerWatchOnlyCatalogAndExplicitTarget(t *testing.T) {
	cfg := controllerConfig()
	started := make(chan ReadTarget, 1)
	cfg.Registrations = []Registration[string]{
		{
			Type: "opaque/type",
			Watch: func(ctx context.Context, _ *string, target ReadTarget, representation Representation, sink WatchEventSink) error {
				if representation.ID != target.Representation {
					return errors.New("representation binding changed")
				}
				started <- target
				sink.OnStateChange(WatchStateEvent{State: WatchStateSynced, Count: Count{Kind: "unknown"}})
				<-ctx.Done()
				return ctx.Err()
			},
		},
	}
	c := newTestController(t, cfg)
	catalog, err := c.Catalog(context.Background(), CatalogRequest{Route: controllerTarget().Route})
	if err != nil {
		t.Fatal(err)
	}
	caps := catalog.Types[0].Capabilities
	if len(caps) != 1 || caps[0].Operation != OperationUpdate || caps[0].Guarantee != BestEffortGuarantee {
		t.Fatal("watch-only registration fabricated finite or strong continuity support")
	}
	if err := c.StartConnectionWatch(
		acceptedControllerContext(context.Background(), t, c),
		[]ReadTarget{controllerTarget()},
	); err != nil {
		t.Fatal(err)
	}
	target := <-started
	if target.Authority != "authority" || target.Route.Connection != "explicit" {
		t.Fatal("host target identity changed")
	}
	changed := target
	changed.Authority = "different"
	assertControllerCode(
		t,
		c.EnsureResourceWatch(acceptedControllerContext(context.Background(), t, c), changed),
		"RESET_REQUIRED",
	)
	if err := c.StopConnectionWatch(acceptedControllerContext(context.Background(), t, c), target.Route); err != nil {
		t.Fatal(err)
	}
	if c.HasWatch(acceptedControllerContext(context.Background(), t, c), target.Route) {
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
		finished <- c.ListenForEvents(acceptedControllerContext(ctx, t, c), controllerUpdateSink{state: func(e WatchStateEvent) { states <- e }}, func() error {
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
	err := c.ListenForEvents(
		acceptedControllerContext(context.Background(), t, c),
		controllerUpdateSink{},
		func() error { return rejected },
	)
	if !errors.Is(err, rejected) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	delivered := make(chan WatchStateEvent, 1)
	finished := make(chan error, 1)
	go func() {
		finished <- c.ListenForEvents(acceptedControllerContext(ctx, t, c), controllerUpdateSink{state: func(event WatchStateEvent) { delivered <- event }}, func() error { return nil })
	}()
	if (<-delivered).State != WatchStateSynced {
		t.Fatal("rejected registration lost replay history")
	}
	cancel()
	assertControllerCode(t, <-finished, "CANCELLED")
}

func TestControllerUpdateReplayOverflowFailsBeforeReadiness(t *testing.T) {
	c := newTestController(t, controllerUpdateConfig())
	watch := &updateWatch{target: controllerTarget()}
	for range maxPendingUpdateStates + 1 {
		c.updates.publishState(watch, WatchStateEvent{State: WatchStateSyncing, Count: Count{Kind: "unknown"}})
	}
	err := c.ListenForEvents(
		acceptedControllerContext(context.Background(), t, c),
		controllerUpdateSink{},
		func() error { t.Fatal("overflow reached readiness"); return nil },
	)
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
	cfg.Registrations = []Registration[string]{
		{
			Type: "opaque/type",
			Watch: func(ctx context.Context, client *string, _ ReadTarget, _ Representation, _ WatchEventSink) error {
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
			},
		},
	}
	c := newTestController(t, cfg)
	c.updates.baseBackoff = time.Millisecond
	startTestConnection(t, c)
	if err := c.EnsureResourceWatch(
		acceptedControllerContext(context.Background(), t, c),
		controllerTarget(),
	); err != nil {
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
	cfg.Registrations = []Registration[string]{
		{
			Type: "opaque/type",
			Watch: func(ctx context.Context, _ *string, _ ReadTarget, _ Representation, sink WatchEventSink) error {
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
			},
		},
	}
	c := newTestController(t, cfg)
	if err := c.EnsureResourceWatch(
		acceptedControllerContext(context.Background(), t, c),
		controllerTarget(),
	); err != nil {
		t.Fatal(err)
	}
	c.updates.mu.Lock()
	watch := c.updates.watches["explicit"]["opaque/type"]
	c.updates.mu.Unlock()
	close(release)
	<-watch.done
	summary, err := c.GetWatchState(acceptedControllerContext(context.Background(), t, c), controllerTarget().Route)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Resources["opaque/type"] != WatchStateFailed {
		t.Fatal("invalid record did not fail source")
	}
}

func TestControllerUpdateSyncPolicyAndOptionalCallback(t *testing.T) {
	cfg := controllerConfig()
	cfg.Registrations = []Registration[string]{
		{
			Type:       "opaque/type",
			SyncPolicy: SyncOnFirstQuery,
			Get:        func(context.Context, *string, GetRequest) (Record, error) { return controllerRecord(), nil },
			Watch: func(ctx context.Context, _ *string, _ ReadTarget, _ Representation, _ WatchEventSink) error {
				<-ctx.Done()
				return ctx.Err()
			},
		},
	}
	c := newTestController(t, cfg)
	if err := c.StartConnectionWatch(
		acceptedControllerContext(context.Background(), t, c),
		[]ReadTarget{controllerTarget()},
	); err != nil {
		t.Fatal(err)
	}
	if c.HasWatch(acceptedControllerContext(context.Background(), t, c), controllerTarget().Route) {
		t.Fatal("on-first-query source started eagerly")
	}
	if _, err := c.Get(
		acceptedControllerContext(context.Background(), t, c),
		GetRequest{
			Route:          controllerTarget().Route,
			Reference:      ResourceReference{Address: controllerAddress()},
			Representation: "document",
		},
	); err != nil {
		t.Fatal(err)
	}
	if running, runningErr := c.IsResourceWatchRunning(
		acceptedControllerContext(context.Background(), t, c),
		controllerTarget().Route,
		"opaque/type",
	); runningErr != nil ||
		!running {
		t.Fatalf("watch not running: %v", runningErr)
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
	if err := c.ApplyConnections(
		context.Background(),
		[]types.Connection{
			{ID: "explicit", Name: "Explicit"},
			{ID: "other", Name: "Changed", Data: map[string]any{"endpoint": "https://changed.invalid"}},
		},
	); err != nil {
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
	if _, updateErr := c.UpdateConnection(
		context.Background(),
		types.Connection{ID: "other", Name: "Updated"},
	); updateErr != nil {
		t.Fatal(updateErr)
	}
	connection, err := c.GetConnection(context.Background(), "other")
	if err != nil || connection.Name != "Updated" {
		t.Fatal("connection update not applied")
	}
}

func TestControllerMissingWatchNeverAcknowledgesListenerReadiness(t *testing.T) {
	c := newTestController(t, controllerConfig())
	err := c.ListenForEvents(
		acceptedControllerContext(context.Background(), t, c),
		controllerUpdateSink{},
		func() error { t.Fatal("missing watch acknowledged listener readiness"); return nil },
	)
	assertControllerCode(t, err, "UNSUPPORTED")
}

func TestControllerUpdateListenerPanicRetiresRegistration(t *testing.T) {
	c := newTestController(t, controllerUpdateConfig())
	c.updates.publishState(
		&updateWatch{target: controllerTarget()},
		WatchStateEvent{State: WatchStateSynced, Count: Count{Kind: "unknown"}},
	)
	err := c.ListenForEvents(
		acceptedControllerContext(context.Background(), t, c),
		controllerUpdateSink{state: func(WatchStateEvent) { panic("private callback data") }},
		func() error { return nil },
	)
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
		finished <- c.ListenForEvents(acceptedControllerContext(ctx, t, c), controllerUpdateSink{}, func() error { close(ready); return nil })
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
				descriptor.Capabilities = []Capability{
					{
						Operation: OperationUpdate,
						Scope:     QueryScope{{Dimension: "region", Match: ScopeAny}},
						Guarantee: BestEffortGuarantee,
					},
				}
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
			if err := c.EnsureResourceWatch(acceptedControllerContext(context.Background(), t, c), target); err != nil {
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
			if err := c.RestartResourceWatch(
				acceptedControllerContext(context.Background(), t, c),
				target,
			); err != nil {
				t.Fatal(err)
			}
			replacement := <-started
			if replacement.Authority != target.Authority || replacement.Representation != target.Representation ||
				!scopeContains(replacement.Scope, target.Scope) ||
				!scopeContains(target.Scope, replacement.Scope) {
				t.Fatal("restart retained a drained target")
			}
		})
	}
}

func TestUpdateListenerDropsPreReadyAdvisoryData(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "rejected"}[reject], func(t *testing.T) {
			c := newTestController(t, controllerUpdateConfig())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered, release := make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			var data atomic.Int32
			sink := controllerUpdateSink{
				add:    func(WatchAddPayload) { data.Add(1) },
				update: func(WatchUpdatePayload) { data.Add(1) },
				remove: func(WatchDeletePayload) { data.Add(1) },
			}
			go func() {
				done <- c.updates.listen(ctx, sink, func() error {
					close(entered)
					<-release
					if reject {
						return errors.New("readiness rejected")
					}
					return nil
				})
			}()
			<-entered
			listener := c.updates.snapshot()[0]
			deliver := func() {
				listener.data(func() { listener.sink.OnAdd(WatchAddPayload{}) })
				listener.data(func() { listener.sink.OnUpdate(WatchUpdatePayload{}) })
				listener.data(func() { listener.sink.OnDelete(WatchDeletePayload{}) })
			}
			deliver()
			if data.Load() != 0 {
				t.Fatal("data crossed blocked readiness")
			}
			// Replay completion signals data admission without polling or sleeps.
			listener.state(WatchStateEvent{State: WatchStateSynced})
			replayed := make(chan struct{})
			listener.mu.Lock()
			listener.sink = controllerUpdateSink{
				add: sink.add, update: sink.update, remove: sink.remove,
				state: func(WatchStateEvent) { close(replayed) },
			}
			listener.mu.Unlock()
			close(release)
			assertPreReadyDataOutcome(t, reject, done, replayed, cancel, deliver, &data, c.updates)
		})
	}
}

func assertPreReadyDataOutcome(
	t *testing.T,
	reject bool,
	done <-chan error,
	replayed <-chan struct{},
	cancel context.CancelFunc,
	deliver func(),
	data *atomic.Int32,
	manager *updateManager[string],
) {
	t.Helper()
	if reject {
		if <-done == nil {
			t.Fatal("readiness rejection succeeded")
		}
		deliver()
		if data.Load() != 0 || len(manager.snapshot()) != 0 {
			t.Fatal("rejected listener retained data admission")
		}
		return
	}
	<-replayed
	cancel()
	assertControllerCode(t, <-done, "CANCELLED")
	if data.Load() != 0 {
		t.Fatal("pre-ready data replayed")
	}
}

func TestUpdateTargetAndRepresentationCopiesOwnContainers(t *testing.T) {
	values := []string{"east"}
	target := controllerTarget()
	target.Scope = QueryScope{{Dimension: "region", Match: ScopeOneOf, Values: &values}}
	retained := cloneReadTarget(target)
	representation := Representation{ID: "json", Schema: SchemaBinding{
		Document: []byte(`{"type":"object"}`), Unvalidated: []string{"original"},
		Revision: copyToken(&target.Type),
	}}
	registration := &Registration[string]{
		Watch: func(_ context.Context, _ *string, callback ReadTarget, rep Representation, _ WatchEventSink) error {
			(*callback.Scope[0].Values)[0] = "callback"
			callback.Scope[0].Dimension = "changed"
			rep.Schema.Document[0] = '!'
			rep.Schema.Unvalidated[0] = "changed"
			*rep.Schema.Revision = "changed"
			return nil
		},
	}
	values[0] = "west"
	target.Scope[0].Dimension = "caller"
	for range 2 {
		if err := invokeUpdate(
			context.Background(),
			new(string),
			retained,
			registration,
			representation,
			controllerUpdateSink{},
		); err != nil {
			t.Fatal(err)
		}
		if retained.Scope[0].Dimension != "region" || (*retained.Scope[0].Values)[0] != "east" {
			t.Fatal("caller or callback changed retained admission")
		}
	}
	if representation.Schema.Document[0] != '{' || representation.Schema.Unvalidated[0] != "original" ||
		*representation.Schema.Revision != target.Type {
		t.Fatal("callback changed retained representation")
	}
	descriptor := ResourceType{
		IdentityDimensions: []Dimension{{Name: "region"}},
		Representations:    []Representation{representation},
	}
	owned := copyUpdateDescriptor(descriptor)
	descriptor.IdentityDimensions[0].Name = "changed"
	descriptor.Representations[0].Schema.Document[0] = '!'
	if owned.IdentityDimensions[0].Name != "region" || owned.Representations[0].Schema.Document[0] != '{' {
		t.Fatal("descriptor admission shares mutable containers")
	}
}

func TestUpdateRetryFencesRetainedCatalog(t *testing.T) {
	cfg := controllerUpdateConfig()
	nativeCatalog := cfg.Catalog
	var changed atomic.Bool
	cfg.Catalog = func(ctx context.Context, request CatalogRequest) (Catalog, error) {
		catalog, err := nativeCatalog(ctx, request)
		if changed.Load() {
			catalog.Revision = "changed-before-retry"
		}
		return catalog, err
	}
	firstRelease := make(chan struct{})
	var calls atomic.Int32
	cfg.Registrations[0].Watch = func(context.Context, *string, ReadTarget, Representation, WatchEventSink) error {
		calls.Add(1)
		<-firstRelease
		return errors.New("retry")
	}
	c := newTestController(t, cfg)
	c.updates.baseBackoff = 0
	ctx := acceptedControllerContext(context.Background(), t, c)
	// A synchronous state observer changes metadata between attempts, before
	// backoff completes. No timing window or native generation retirement needed.
	c.updates.listeners = []*updateListener{
		{
			ctx:    context.Background(),
			failed: make(chan error, 1),
			sink: controllerUpdateSink{state: func(event WatchStateEvent) {
				if event.State == WatchStateError {
					changed.Store(true)
				}
			}},
		},
	}
	target := controllerTarget()
	if err := c.EnsureResourceWatch(ctx, target); err != nil {
		t.Fatal(err)
	}
	c.updates.mu.Lock()
	watch := c.updates.watches[target.Route.Connection][target.Type]
	c.updates.mu.Unlock()
	close(firstRelease)
	<-watch.done
	if calls.Load() != 1 || watch.state != WatchStateFailed {
		t.Fatalf("calls=%d state=%v", calls.Load(), watch.state)
	}
	// Terminal state must carry an explicit reset, not the retryable source error.
	c.updates.listenersMu.Lock()
	pending := append([]WatchStateEvent(nil), c.updates.pending...)
	c.updates.listenersMu.Unlock()
	if len(pending) == 0 || pending[len(pending)-1].Error == nil {
		t.Fatal("missing terminal reset outcome")
	}
	assertControllerCode(t, pending[len(pending)-1].Error, "RESET_REQUIRED")
}

func TestUpdateWatchRetainsScopeAfterCallerAndProviderMutation(t *testing.T) {
	for _, owner := range []string{"caller", "provider"} {
		t.Run(owner, func(t *testing.T) {
			cfg := controllerUpdateConfig()
			baseCatalog := cfg.Catalog
			cfg.Catalog = func(ctx context.Context, request CatalogRequest) (Catalog, error) {
				catalog, err := baseCatalog(ctx, request)
				catalog.Types[0].IdentityDimensions = []Dimension{{Name: "region"}}
				catalog.Types[0].QueryDimensions = []Dimension{{Name: "region"}}
				for i := range catalog.Types[0].Capabilities {
					catalog.Types[0].Capabilities[i].Scope = QueryScope{{Dimension: "region", Match: ScopeAny}}
				}
				return catalog, err
			}
			callback := make(chan ReadTarget, 1)
			emit := make(chan struct{})
			cfg.Registrations[0].Watch = func(ctx context.Context, _ *string, target ReadTarget, _ Representation, sink WatchEventSink) error {
				callback <- target
				<-emit
				record := controllerRecord()
				record.Reference.Address.Scope = []ScopeEntry{{Dimension: "region", Value: "west"}}
				sink.OnAdd(WatchAddPayload{Record: record})
				<-ctx.Done()
				return ctx.Err()
			}
			c := newTestController(t, cfg)
			ctx := acceptedControllerContext(context.Background(), t, c)
			target := controllerTarget()
			values := []string{"east"}
			target.Scope = QueryScope{{Dimension: "region", Match: ScopeOneOf, Values: &values}}
			if err := c.EnsureResourceWatch(ctx, target); err != nil {
				t.Fatal(err)
			}
			providerTarget := <-callback
			if owner == "caller" {
				values[0] = "west"
			} else {
				(*providerTarget.Scope[0].Values)[0] = "west"
			}
			c.updates.mu.Lock()
			watch := c.updates.watches[target.Route.Connection][target.Type]
			c.updates.mu.Unlock()
			close(emit)
			<-watch.done
			assertControllerCode(t, watch.validationErr, "INTERNAL")
			if (*watch.target.Scope[0].Values)[0] != "east" {
				t.Fatal("retained scope changed")
			}
		})
	}
}
