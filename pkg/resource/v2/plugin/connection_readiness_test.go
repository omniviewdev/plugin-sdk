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

type registeredReadinessScenario struct {
	ctx                                                        context.Context
	cancel                                                     context.CancelFunc
	client                                                     *Client
	a, b                                                       *httptest.Server
	requestsA, requestsB, created, destroyed, probes, catalogs atomic.Int32
	creating, retiring, releaseCreate, releaseRetire           chan struct{}
	createOnce, retireOnce                                     sync.Once
}

func TestActualGRPCRegisteredReadinessDoesNotProbeAndFencesRetarget(t *testing.T) {
	s := newRegisteredReadinessScenario(t)
	defer s.cancel()
	defer s.createOnce.Do(func() { close(s.releaseCreate) })
	defer s.retireOnce.Do(func() { close(s.releaseRetire) })
	s.checkStartup(t)
	s.checkRetarget(t)
}

func newRegisteredReadinessScenario(t *testing.T) *registeredReadinessScenario {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	s := &registeredReadinessScenario{
		ctx:           ctx,
		cancel:        cancel,
		creating:      make(chan struct{}),
		retiring:      make(chan struct{}),
		releaseCreate: make(chan struct{}),
		releaseRetire: make(chan struct{}),
	}
	s.a = readinessNativeServer(t, &s.requestsA, "A")
	s.b = readinessNativeServer(t, &s.requestsB, "B")
	controller, err := resourcev2.NewController(resourcev2.Config[string]{
		Catalog: func(context.Context, resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
			s.catalogs.Add(1)
			return resourcev2.Catalog{
				Provider: "readiness",
				Revision: "native",
				Types:    []resourcev2.ResourceType{},
				Coverage: resourcev2.Coverage{Kind: "complete", Paths: []string{}},
			}, nil
		},
		Lifecycle: s.lifecycle(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := controller.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	server := grpc.NewServer()
	RegisterServer(server, controller)
	s.client = NewClient(pipe(t, server))
	return s
}

func readinessNativeServer(t *testing.T, counter *atomic.Int32, name string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		counter.Add(1)
		if _, err := fmt.Fprintf(w, `{"native":%q}`, name); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func (s *registeredReadinessScenario) lifecycle() *resourcev2.Lifecycle[string] {
	return &resourcev2.Lifecycle[string]{
		LoadConnections: func(context.Context) ([]types.Connection, error) {
			return []types.Connection{{ID: "native", Data: map[string]any{"endpoint": s.a.URL}}}, nil
		},
		CreateClient: s.createClient,
		DestroyClient: func(context.Context, *string) error {
			if s.destroyed.Add(1) == 1 {
				close(s.retiring)
				select {
				case <-s.releaseRetire:
				case <-s.ctx.Done():
					return s.ctx.Err()
				}
			}
			return nil
		},
		CheckConnection: func(context.Context, types.Connection, *string) (types.ConnectionStatus, error) {
			s.probes.Add(1)
			return types.ConnectionStatus{}, fmt.Errorf("health probe must not run")
		},
	}
}

func (s *registeredReadinessScenario) createClient(
	ctx context.Context, connection types.Connection,
) (*string, error) {
	s.created.Add(1)
	endpoint, ok := connection.Data["endpoint"].(string)
	if !ok {
		panic("connection fixture endpoint must be a string")
	}
	if endpoint == s.a.URL {
		close(s.creating)
		select {
		case <-s.releaseCreate:
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
	if err = response.Body.Close(); err != nil {
		return nil, err
	}
	return &endpoint, nil
}

func (s *registeredReadinessScenario) state(
	t *testing.T, want types.ConnectionStatusCode, endpoint string,
) {
	t.Helper()
	bounded, stop := context.WithTimeout(s.ctx, time.Second)
	defer stop()
	status, err := s.client.GetConnectionStatus(bounded, "native")
	if err != nil || status.Status != want || status.Connection == nil ||
		status.Connection.Data["endpoint"] != endpoint || status.Error != "" || status.Details != "" {
		t.Fatalf("registered state want %s/%s: %+v %v", want, endpoint, status, err)
	}
}

func (s *registeredReadinessScenario) checkUnstarted(t *testing.T) {
	t.Helper()
	ctx, client, a := s.ctx, s.client, s.a
	features, err := client.Features(ctx)
	if err != nil || !features.Has(resourcev2.FeatureConnectionReadiness) {
		t.Fatalf("readiness negotiation: %+v %v", features, err)
	}
	if _, err = client.LoadConnections(ctx); err != nil {
		t.Fatal(err)
	}
	s.state(t, types.ConnectionStatusDisconnected, a.URL)
	if _, err = client.GetConnectionStatus(ctx, "missing"); resourcev2.ErrorCode(err) != "NOT_FOUND" {
		t.Fatalf("unknown registry route: %v", err)
	}
	route := resourcev2.AccessRoute{Connection: "native"}
	if _, err = client.Catalog(ctx, resourcev2.CatalogRequest{Route: route}); err == nil {
		t.Fatal("stopped source minted catalog binding")
	}
	if s.created.Load() != 0 || s.catalogs.Load() != 0 || s.probes.Load() != 0 || s.requestsA.Load() != 0 ||
		s.requestsB.Load() != 0 {
		t.Fatal("unstarted readiness performed native work")
	}
}

func (s *registeredReadinessScenario) checkStartup(t *testing.T) {
	t.Helper()
	s.checkUnstarted(t)
	ctx, client, a := s.ctx, s.client, s.a
	started := make(chan error, 1)
	go func() { _, err := client.StartConnection(ctx, "native"); started <- err }()
	select {
	case <-s.creating:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	s.state(t, types.ConnectionStatusPending, a.URL)
	if s.created.Load() != 1 || s.catalogs.Load() != 0 || s.probes.Load() != 0 || s.requestsA.Load() != 0 {
		t.Fatal("pending state waited, probed or redispatched startup")
	}
	s.createOnce.Do(func() { close(s.releaseCreate) })
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	s.state(t, types.ConnectionStatusConnected, a.URL)
	if s.created.Load() != 1 || s.catalogs.Load() != 0 || s.probes.Load() != 0 || s.requestsA.Load() != 1 {
		t.Fatal("active state was not a pure registry snapshot")
	}
}

func (s *registeredReadinessScenario) checkRetarget(t *testing.T) {
	t.Helper()
	ctx, client, a, b := s.ctx, s.client, s.a, s.b
	route := resourcev2.AccessRoute{Connection: "native"}
	state := func(want types.ConnectionStatusCode, endpoint string) {
		t.Helper()
		s.state(t, want, endpoint)
	}
	catalogA, err := client.Catalog(ctx, resourcev2.CatalogRequest{Route: route})
	if err != nil {
		t.Fatal(err)
	}
	changed := make(chan error, 1)
	go func() {
		changed <- client.ApplyConnectionOverlay(ctx, resourcev2.ConnectionOverlay{
			Upserts:     []types.Connection{{ID: "native", Data: map[string]any{"endpoint": b.URL}}},
			DisabledIDs: []string{},
		})
	}()
	select {
	case <-s.retiring:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	state(types.ConnectionStatusDisconnected, a.URL)
	if s.created.Load() != 1 || s.catalogs.Load() != 1 || s.probes.Load() != 0 || s.requestsB.Load() != 0 {
		t.Fatal("retiring snapshot started or admitted replacement")
	}
	s.retireOnce.Do(func() { close(s.releaseRetire) })
	if err = <-changed; err != nil {
		t.Fatal(err)
	}
	state(types.ConnectionStatusDisconnected, b.URL)
	if _, err = client.StartConnection(ctx, "native"); err != nil {
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
	if _, err = client.GetWatchState(
		resourcev2.WithCatalogRevision(ctx, catalogA.Revision),
		route,
	); resourcev2.ErrorCode(
		err,
	) != "RESET_REQUIRED" {
		t.Fatalf("retired observation admitted watch diagnostics: %v", err)
	}
	if s.created.Load() != 2 || s.destroyed.Load() != 1 || s.probes.Load() != 0 || s.requestsA.Load() != 1 ||
		s.requestsB.Load() != 1 {
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
		if closeErr := controller.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	server := grpc.NewServer()
	RegisterServer(server, controller)
	client := NewClient(pipe(t, server))
	features, err := client.Features(context.Background())
	if err != nil || features.Has(resourcev2.FeatureConnectionReadiness) {
		t.Fatalf("invented registry readiness: %+v %v", features, err)
	}
	if _, err = client.GetConnectionStatus(
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
