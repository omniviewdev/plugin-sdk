package resourcev2

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

func TestCatalogRevisionRetiredSlotCannotResurrect(t *testing.T) {
	ctx := context.Background()
	route := AccessRoute{Connection: "explicit"}
	cfg := controllerConfig()
	var calls atomic.Int32
	cfg.Registrations = []Registration[string]{
		{Type: "opaque/type", Get: func(context.Context, *string, GetRequest) (Record, error) {
			calls.Add(1)
			return controllerRecord(), nil
		}},
	}
	cfg.Lifecycle = &Lifecycle[string]{
		LoadConnections: func(context.Context) ([]types.Connection, error) {
			return []types.Connection{{ID: route.Connection, Data: map[string]any{"source": "A"}}}, nil
		},
		CreateClient: func(_ context.Context, connection types.Connection) (*string, error) {
			value := connection.Data["source"].(string)
			return &value, nil
		},
		DestroyClient: func(context.Context, *string) error { return nil },
	}
	c := newTestController(t, cfg)
	if _, err := c.LoadConnections(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.StartConnection(ctx, route.Connection); err != nil {
		t.Fatal(err)
	}
	accepted, err := c.Catalog(ctx, CatalogRequest{Route: route})
	if err != nil {
		t.Fatal(err)
	}
	request := GetRequest{
		Route:          route,
		Reference:      ResourceReference{Address: controllerAddress()},
		Representation: "document",
	}
	for _, source := range []string{"B", "A"} {
		if _, err := c.UpdateConnection(
			ctx,
			types.Connection{ID: route.Connection, Data: map[string]any{"source": source}},
		); err != nil {
			t.Fatal(err)
		}
		if _, err := c.StartConnection(ctx, route.Connection); err != nil {
			t.Fatal(err)
		}
		_, err := c.Get(WithCatalogRevision(ctx, accepted.Revision), request)
		assertControllerCode(t, err, "RESET_REQUIRED")
		if calls.Load() != 0 {
			t.Fatal("stale admission reached native handler")
		}
		current, err := c.Catalog(ctx, CatalogRequest{Route: route})
		if err != nil {
			t.Fatal(err)
		}
		if current.Revision == accepted.Revision {
			t.Fatal("retired slot revision resurrected")
		}
		if _, err := c.Get(WithCatalogRevision(ctx, current.Revision), request); err != nil {
			t.Fatal(err)
		}
		calls.Store(0)
	}
}

func TestCatalogRevisionRejectsMalformedBeforeDispatch(t *testing.T) {
	cfg := controllerConfig()
	var calls atomic.Int32
	cfg.Registrations = []Registration[string]{
		{Type: "opaque/type", Get: func(context.Context, *string, GetRequest) (Record, error) {
			calls.Add(1)
			return controllerRecord(), nil
		}},
	}
	c := newTestController(t, cfg)
	request := GetRequest{
		Route:          AccessRoute{Connection: "explicit"},
		Reference:      ResourceReference{Address: controllerAddress()},
		Representation: "document",
	}
	for _, revision := range []string{"", strings.Repeat("x", MaxCatalogRevisionBytes+1), string([]byte{0xff})} {
		_, err := c.Get(WithCatalogRevision(context.Background(), revision), request)
		assertControllerCode(t, err, "RESET_REQUIRED")
	}
	if calls.Load() != 0 {
		t.Fatal("malformed admission reached native handler")
	}
}

func TestCatalogRevisionFencesProviderIgnoringCancellation(t *testing.T) {
	cfg := controllerConfig()
	nativeCatalog := cfg.Catalog
	var generation atomic.Int32
	cfg.Catalog = func(ctx context.Context, request CatalogRequest) (Catalog, error) {
		catalog, err := nativeCatalog(ctx, request)
		if generation.Load() != 0 {
			catalog.Revision = "catalog-B"
		}
		return catalog, err
	}
	cfg.Registrations = []Registration[string]{
		{Type: "opaque/type", Get: func(context.Context, *string, GetRequest) (Record, error) {
			generation.Store(1)
			return controllerRecord(), nil
		}},
	}
	c := newTestController(t, cfg)
	ctx := context.Background()
	route := AccessRoute{Connection: "explicit"}
	accepted, err := c.Catalog(ctx, CatalogRequest{Route: route})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Get(
		WithCatalogRevision(ctx, accepted.Revision),
		GetRequest{
			Route:          route,
			Reference:      ResourceReference{Address: controllerAddress()},
			Representation: "document",
		},
	)
	assertControllerCode(t, err, "RESET_REQUIRED")
}

func TestDisplayEditPreservesCatalogGenerationAndCurrentSession(t *testing.T) {
	ctx := context.Background()
	route := AccessRoute{Connection: "explicit"}
	refreshed := time.Unix(1234, 0)
	runtimeClient := &struct{}{}
	cfg := controllerConfig()
	var starts atomic.Int32
	cfg.Lifecycle = &Lifecycle[string]{
		LoadConnections: func(context.Context) ([]types.Connection, error) {
			return []types.Connection{
				{
					ID:          route.Connection,
					Name:        "old",
					Data:        map[string]any{"source": "A"},
					LastRefresh: refreshed,
					ExpiryTime:  time.Hour,
					Client:      runtimeClient,
				},
			}, nil
		},
		CreateClient: func(context.Context, types.Connection) (*string, error) {
			starts.Add(1)
			value := "A"
			return &value, nil
		},
		DestroyClient: func(context.Context, *string) error { return nil },
	}
	c := newTestController(t, cfg)
	if _, err := c.LoadConnections(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.StartConnection(ctx, route.Connection); err != nil {
		t.Fatal(err)
	}
	before, err := c.Catalog(ctx, CatalogRequest{Route: route})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateConnection(
		ctx,
		types.Connection{ID: route.Connection, Name: "new", Data: map[string]any{"source": "A"}},
	); err != nil {
		t.Fatal(err)
	}
	after, err := c.Catalog(ctx, CatalogRequest{Route: route})
	if err != nil {
		t.Fatal(err)
	}
	if before.Revision != after.Revision || starts.Load() != 1 {
		t.Fatal("display edit replaced native generation")
	}
	status, err := c.CheckConnection(ctx, route.Connection)
	if err != nil {
		t.Fatal(err)
	}
	if status.Connection.Name != "new" {
		t.Fatal("active session retained stale display metadata")
	}
	if !status.Connection.LastRefresh.Equal(refreshed) || status.Connection.ExpiryTime != time.Hour ||
		status.Connection.Client != runtimeClient {
		t.Fatal("display edit dropped native runtime state")
	}
}

func TestCatalogBindingEpochIsCanonicalAndSlotSpecific(t *testing.T) {
	firstNonce, err := newCatalogNonce()
	if err != nil {
		t.Fatal(err)
	}
	secondNonce, err := newCatalogNonce()
	if err != nil {
		t.Fatal(err)
	}
	first := boundCatalogRevision(firstNonce, "native-A")
	churn := boundCatalogRevision(firstNonce, "native-B")
	restarted := boundCatalogRevision(secondNonce, "native-A")
	firstEpoch, err := CatalogBindingEpoch(first)
	if err != nil {
		t.Fatal(err)
	}
	churnEpoch, err := CatalogBindingEpoch(churn)
	if err != nil {
		t.Fatal(err)
	}
	restartedEpoch, err := CatalogBindingEpoch(restarted)
	if err != nil {
		t.Fatal(err)
	}
	if first == churn || firstEpoch != churnEpoch || firstEpoch == restartedEpoch {
		t.Fatal("catalog churn and native client epochs were not separated")
	}
	for _, malformed := range []string{"", "native-A", first + "=", strings.Replace(first, "ovc1.", "ovc2.", 1), "ovc1.x.x", first + ".extra"} {
		_, err := CatalogBindingEpoch(malformed)
		assertControllerCode(t, err, "RESET_REQUIRED")
	}
}

func TestWatchCatalogFenceDropsChangedGenerationEvent(t *testing.T) {
	cfg := controllerUpdateConfig()
	nativeCatalog := cfg.Catalog
	var changed atomic.Bool
	cfg.Catalog = func(ctx context.Context, request CatalogRequest) (Catalog, error) {
		catalog, err := nativeCatalog(ctx, request)
		if changed.Load() {
			catalog.Revision = "native-B"
		}
		return catalog, err
	}
	c := newTestController(t, cfg)
	target := controllerTarget()
	catalog, err := c.Catalog(context.Background(), CatalogRequest{Route: target.Route})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(WithCatalogRevision(context.Background(), catalog.Revision))
	defer cancel()
	watch := &updateWatch{target: target, ctx: ctx, cancel: cancel}
	var published atomic.Int32
	c.updates.listeners = []*updateListener{
		{ctx: context.Background(), sink: controllerUpdateSink{add: func(WatchAddPayload) { published.Add(1) }}},
	}
	sink := &updateSourceSink[string]{manager: c.updates, watch: watch, descriptor: catalog.Types[0]}
	changed.Store(true)
	sink.OnAdd(WatchAddPayload{Record: controllerRecord()})
	if published.Load() != 0 {
		t.Fatal("changed native generation published under old accepted catalog")
	}
	assertControllerCode(t, watch.validationErr, "RESET_REQUIRED")
	if ctx.Err() == nil {
		t.Fatal("stale watch was not canceled")
	}
}

func TestOverlayPrivateCredentialsStaySourceOwned(t *testing.T) {
	for _, change := range []string{"display", "endpoint", "clear"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			native := types.Connection{ID: "explicit", Name: "old", Data: map[string]any{"endpoint": "A"}}
			native.SetSensitiveData(map[string]any{"token": "source-secret"})
			cfg := controllerConfig()
			var starts atomic.Int32
			cfg.Lifecycle = &Lifecycle[string]{
				LoadConnections: func(context.Context) ([]types.Connection, error) { return []types.Connection{native}, nil },
				CreateClient: func(context.Context, types.Connection) (*string, error) {
					starts.Add(1)
					value := "client"
					return &value, nil
				},
				DestroyClient: func(context.Context, *string) error { return nil },
			}
			c := newTestController(t, cfg)
			if _, err := c.LoadConnections(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := c.StartConnection(ctx, native.ID); err != nil {
				t.Fatal(err)
			}
			before, err := c.Catalog(ctx, CatalogRequest{Route: AccessRoute{Connection: native.ID}})
			if err != nil {
				t.Fatal(err)
			}
			override := types.Connection{ID: native.ID, Name: "renamed", Data: map[string]any{"endpoint": "A"}}
			if change == "endpoint" {
				override.Data["endpoint"] = "B"
			}
			if change == "clear" {
				override.SetSensitiveData(map[string]any{})
			}
			if err := c.ApplyConnectionOverlay(
				ctx,
				ConnectionOverlay{Upserts: []types.Connection{override}},
			); err != nil {
				t.Fatal(err)
			}
			effective, err := c.GetConnection(ctx, native.ID)
			if err != nil {
				t.Fatal(err)
			}
			if change == "display" {
				if effective.GetSensitiveData()["token"] != "source-secret" {
					t.Fatal("display overlay dropped native credentials")
				}
				after, err := c.Catalog(ctx, CatalogRequest{Route: AccessRoute{Connection: native.ID}})
				if err != nil {
					t.Fatal(err)
				}
				if before.Revision != after.Revision || starts.Load() != 1 {
					t.Fatal("display overlay replaced native client")
				}
				desired := c.overrides[native.ID]
				if desired.GetSensitiveData() != nil {
					t.Fatal("private credentials leaked into desired overlay")
				}
			} else {
				if len(effective.GetSensitiveData()) != 0 {
					t.Fatal("endpoint change or explicit clear retained old credentials")
				}
				status, err := c.CheckConnection(ctx, native.ID)
				if err != nil {
					t.Fatal(err)
				}
				if status.Status != types.ConnectionStatusDisconnected {
					t.Fatal("source edit retained old native client")
				}
			}
			if native.GetSensitiveData()["token"] != "source-secret" {
				t.Fatal("overlay mutated source credentials")
			}
		})
	}
}

func TestCustomProviderCatalogBindingIssuer(t *testing.T) {
	epoch, err := NewCatalogBindingEpoch()
	if err != nil {
		t.Fatal(err)
	}
	secondEpoch, err := NewCatalogBindingEpoch()
	if err != nil {
		t.Fatal(err)
	}
	if epoch == secondEpoch {
		t.Fatal("binding issuer reused native generation")
	}
	for _, nativeRevision := range []string{"native-A", "native-B"} {
		revision, err := BindCatalogRevision(epoch, nativeRevision)
		if err != nil {
			t.Fatal(err)
		}
		observed, err := CatalogBindingEpoch(revision)
		if err != nil {
			t.Fatal(err)
		}
		if observed != epoch {
			t.Fatal("custom provider binding epoch changed during catalog churn")
		}
	}
	for _, malformed := range []string{"", "constant", epoch + "=", epoch + ".extra", strings.Replace(epoch, "ovce1.", "ovce2.", 1)} {
		_, err := BindCatalogRevision(malformed, "native-A")
		assertControllerCode(t, err, "RESET_REQUIRED")
	}
}

func TestWithConnectionRejectsSuccessAfterSourceRetiresWithoutCatalogPin(t *testing.T) {
	ctx := context.Background()
	cfg := controllerConfig()
	cfg.Lifecycle = &Lifecycle[string]{
		LoadConnections: func(context.Context) ([]types.Connection, error) { return []types.Connection{{ID: "explicit"}}, nil },
		CreateClient:    func(context.Context, types.Connection) (*string, error) { value := "native"; return &value, nil },
		DestroyClient:   func(context.Context, *string) error { return nil },
	}
	c := newTestController(t, cfg)
	if _, err := c.LoadConnections(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.StartConnection(ctx, "explicit"); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- c.WithConnection(ctx, AccessRoute{Connection: "explicit"}, func(native context.Context, _ types.Connection) error {
			close(entered)
			<-native.Done()
			return nil // A provider that ignores cancellation cannot publish success.
		})
	}()
	<-entered
	if _, err := c.StopConnection(ctx, "explicit"); err != nil {
		t.Fatal(err)
	}
	assertControllerCode(t, <-result, "CANCELLED")
}

func TestWatchReuseRejectsChangedCatalogWithinSameBinding(t *testing.T) {
	cfg := controllerConfig()
	nativeCatalog := cfg.Catalog
	var changed atomic.Bool
	cfg.Catalog = func(ctx context.Context, request CatalogRequest) (Catalog, error) {
		catalog, err := nativeCatalog(ctx, request)
		if changed.Load() {
			catalog.Revision = "native-B"
		}
		return catalog, err
	}
	started := make(chan struct{}, 2)
	cfg.Registrations = []Registration[string]{
		{
			Type: "opaque/type",
			Watch: func(ctx context.Context, _ *string, _ ReadTarget, _ Representation, _ WatchEventSink) error {
				started <- struct{}{}
				<-ctx.Done()
				return ctx.Err()
			},
		},
	}
	c := newTestController(t, cfg)
	target := controllerTarget()
	ctx := context.Background()
	first, err := c.Catalog(ctx, CatalogRequest{Route: target.Route})
	if err != nil {
		t.Fatal(err)
	}
	firstCtx := WithCatalogRevision(ctx, first.Revision)
	if err := c.EnsureResourceWatch(firstCtx, target); err != nil {
		t.Fatal(err)
	}
	<-started
	changed.Store(true)
	second, err := c.Catalog(ctx, CatalogRequest{Route: target.Route})
	if err != nil {
		t.Fatal(err)
	}
	firstEpoch, err := CatalogBindingEpoch(first.Revision)
	if err != nil {
		t.Fatal(err)
	}
	secondEpoch, err := CatalogBindingEpoch(second.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision == second.Revision || firstEpoch != secondEpoch {
		t.Fatal("fixture did not preserve source binding across catalog churn")
	}
	secondCtx := WithCatalogRevision(ctx, second.Revision)
	assertControllerCode(t, c.EnsureResourceWatch(secondCtx, target), "RESET_REQUIRED")
	select {
	case <-started:
		t.Fatal("stale watch reuse unexpectedly started another source")
	default:
	}
	if err := c.StopResourceWatch(secondCtx, target.Route, target.Type); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureResourceWatch(secondCtx, target); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := c.StopResourceWatch(secondCtx, target.Route, target.Type); err != nil {
		t.Fatal(err)
	}
}

func TestControllerSelectsQualifiedPinAndWatchRetainsObservation(t *testing.T) {
	cfg := controllerConfig()
	cfg.Registrations = []Registration[string]{{
		Type: "opaque/type",
		Get:  func(context.Context, *string, GetRequest) (Record, error) { return controllerRecord(), nil },
		Watch: func(ctx context.Context, _ *string, _ ReadTarget, _ Representation, _ WatchEventSink) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}}
	c := newTestController(t, cfg)
	target := controllerTarget()
	catalog, err := c.Catalog(context.Background(), CatalogRequest{Route: target.Route})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithCatalogRevision(context.Background(), "wrong-default")
	ctx = WithCatalogRevisionForRoute(ctx, target.Provider, target.Route, catalog.Revision)
	request := GetRequest{
		Route:          target.Route,
		Reference:      ResourceReference{Address: controllerAddress()},
		Representation: "document",
	}
	if _, err := c.Get(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureResourceWatch(ctx, target); err != nil {
		t.Fatal(err)
	}
	c.updates.mu.Lock()
	retained, _ := CatalogRevisionFromContext(c.updates.watches[target.Route.Connection][target.Type].ctx)
	c.updates.mu.Unlock()
	if retained != catalog.Revision {
		t.Fatal("watch did not retain selected qualified observation")
	}
	if err := c.StopResourceWatch(ctx, target.Route, target.Type); err != nil {
		t.Fatal(err)
	}
	wrong := WithCatalogRevisionForRoute(
		WithCatalogRevision(context.Background(), catalog.Revision),
		target.Provider,
		target.Route,
		"wrong-qualified",
	)
	_, err = c.Get(wrong, request)
	assertControllerCode(t, err, "RESET_REQUIRED")
}
