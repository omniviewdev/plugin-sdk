package plugin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

func TestActualGRPCRegisteredReadinessDoesNotProbeAndFencesRetarget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var requestsA, requestsB, created, destroyed, probes, catalogs atomic.Int32
	native := func(counter *atomic.Int32, name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			counter.Add(1)
			fmt.Fprintf(w, `{"native":%q}`, name)
		}))
	}
	a, b := native(&requestsA, "A"), native(&requestsB, "B")
	defer a.Close()
	defer b.Close()
	creating, retiring := make(chan struct{}), make(chan struct{})
	releaseCreate, releaseRetire := make(chan struct{}), make(chan struct{})
	var createOnce, retireOnce sync.Once
	defer createOnce.Do(func() { close(releaseCreate) })
	defer retireOnce.Do(func() { close(releaseRetire) })
	controller, err := resourcev2.NewController(resourcev2.Config[string]{
		Catalog: func(context.Context, resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
			catalogs.Add(1)
			return resourcev2.Catalog{
				Provider: "readiness",
				Revision: "native",
				Types:    []resourcev2.ResourceType{},
				Coverage: resourcev2.Coverage{Kind: "complete", Paths: []string{}},
			}, nil
		},
		Lifecycle: &resourcev2.Lifecycle[string]{
			LoadConnections: func(context.Context) ([]types.Connection, error) {
				return []types.Connection{{ID: "native", Data: map[string]any{"endpoint": a.URL}}}, nil
			},
			CreateClient: func(ctx context.Context, connection types.Connection) (*string, error) {
				created.Add(1)
				endpoint := connection.Data["endpoint"].(string)
				if endpoint == a.URL {
					close(creating)
					select {
					case <-releaseCreate:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
				if err != nil {
					return nil, err
				}
				response, err := http.DefaultClient.Do(request)
				if err != nil {
					return nil, err
				}
				response.Body.Close()
				return &endpoint, nil
			},
			DestroyClient: func(context.Context, *string) error {
				if destroyed.Add(1) == 1 {
					close(retiring)
					select {
					case <-releaseRetire:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				return nil
			},
			CheckConnection: func(context.Context, types.Connection, *string) (types.ConnectionStatus, error) {
				probes.Add(1)
				return types.ConnectionStatus{}, fmt.Errorf("health probe must not run")
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Error(err)
		}
	})
	server := grpc.NewServer()
	RegisterServer(server, controller)
	client := NewClient(pipe(t, server))
	features, err := client.Features(ctx)
	if err != nil || !features.Has(resourcev2.FeatureConnectionReadiness) {
		t.Fatalf("readiness negotiation: %+v %v", features, err)
	}
	if _, err := client.LoadConnections(ctx); err != nil {
		t.Fatal(err)
	}
	state := func(want types.ConnectionStatusCode, endpoint string) {
		t.Helper()
		bounded, stop := context.WithTimeout(ctx, time.Second)
		defer stop()
		status, err := client.GetConnectionStatus(bounded, "native")
		if err != nil || status.Status != want || status.Connection == nil ||
			status.Connection.Data["endpoint"] != endpoint ||
			status.Error != "" ||
			status.Details != "" {
			t.Fatalf("registered state want %s/%s: %+v %v", want, endpoint, status, err)
		}
	}
	state(types.ConnectionStatusDisconnected, a.URL)
	if _, err := client.GetConnectionStatus(ctx, "missing"); resourcev2.ErrorCode(err) != "NOT_FOUND" {
		t.Fatalf("unknown registry route: %v", err)
	}
	route := resourcev2.AccessRoute{Connection: "native"}
	if _, err := client.Catalog(ctx, resourcev2.CatalogRequest{Route: route}); err == nil {
		t.Fatal("stopped source minted catalog binding")
	}
	if created.Load() != 0 || catalogs.Load() != 0 || probes.Load() != 0 || requestsA.Load() != 0 ||
		requestsB.Load() != 0 {
		t.Fatal("unstarted readiness performed native work")
	}
	started := make(chan error, 1)
	go func() { _, err := client.StartConnection(ctx, "native"); started <- err }()
	select {
	case <-creating:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	state(types.ConnectionStatusPending, a.URL)
	if created.Load() != 1 || catalogs.Load() != 0 || probes.Load() != 0 || requestsA.Load() != 0 {
		t.Fatal("pending state waited, probed or redispatched startup")
	}
	createOnce.Do(func() { close(releaseCreate) })
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	state(types.ConnectionStatusConnected, a.URL)
	if created.Load() != 1 || catalogs.Load() != 0 || probes.Load() != 0 || requestsA.Load() != 1 {
		t.Fatal("active state was not a pure registry snapshot")
	}
	catalogA, err := client.Catalog(ctx, resourcev2.CatalogRequest{Route: route})
	if err != nil {
		t.Fatal(err)
	}
	changed := make(chan error, 1)
	go func() {
		changed <- client.ApplyConnectionOverlay(ctx, resourcev2.ConnectionOverlay{Upserts: []types.Connection{{ID: "native", Data: map[string]any{"endpoint": b.URL}}}, DisabledIDs: []string{}})
	}()
	select {
	case <-retiring:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	state(types.ConnectionStatusDisconnected, a.URL)
	if created.Load() != 1 || catalogs.Load() != 1 || probes.Load() != 0 || requestsB.Load() != 0 {
		t.Fatal("retiring snapshot started or admitted replacement")
	}
	retireOnce.Do(func() { close(releaseRetire) })
	if err := <-changed; err != nil {
		t.Fatal(err)
	}
	state(types.ConnectionStatusDisconnected, b.URL)
	if _, err := client.StartConnection(ctx, "native"); err != nil {
		t.Fatal(err)
	}
	state(types.ConnectionStatusConnected, b.URL)
	catalogB, err := client.Catalog(ctx, resourcev2.CatalogRequest{Route: route})
	if err != nil {
		t.Fatal(err)
	}
	if catalogA.Revision == catalogB.Revision {
		t.Fatal("replacement reused retired catalog binding")
	}
	if _, err := client.GetWatchState(
		resourcev2.WithCatalogRevision(ctx, catalogA.Revision),
		route,
	); resourcev2.ErrorCode(
		err,
	) != "RESET_REQUIRED" {
		t.Fatalf("retired observation admitted watch diagnostics: %v", err)
	}
	if created.Load() != 2 || destroyed.Load() != 1 || probes.Load() != 0 || requestsA.Load() != 1 ||
		requestsB.Load() != 1 {
		t.Fatal("readiness caused native lifecycle or health side effects")
	}
}

func TestActualGRPCReadinessUnsupportedIsNotInferredFromClient(t *testing.T) {
	var catalogs atomic.Int32
	controller, err := resourcev2.NewController(
		resourcev2.Config[int]{Catalog: func(context.Context, resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
			catalogs.Add(1)
			return resourcev2.Catalog{}, nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Error(err)
		}
	})
	server := grpc.NewServer()
	RegisterServer(server, controller)
	client := NewClient(pipe(t, server))
	features, err := client.Features(context.Background())
	if err != nil || features.Has(resourcev2.FeatureConnectionReadiness) {
		t.Fatalf("invented registry readiness: %+v %v", features, err)
	}
	if _, err := client.GetConnectionStatus(
		context.Background(),
		"explicit",
	); resourcev2.ErrorCode(
		err,
	) != "UNSUPPORTED" {
		t.Fatalf("unsupported readiness: %v", err)
	}
	if catalogs.Load() != 0 {
		t.Fatal("unsupported readiness queried resource catalog")
	}
}
