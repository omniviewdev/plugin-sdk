package plugin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

type failedStartNativeClient struct{ id, mode string }

type failedStartScenario struct {
	ctx                                          context.Context
	cancel                                       context.CancelFunc
	client                                       *Client
	failing                                      atomic.Bool
	created, probes, catalogs, httpCalls, nextID atomic.Int32
	mu                                           sync.Mutex
	allocated                                    map[string]bool
	native                                       *httptest.Server
	cleanupEntered, releaseCleanup               chan struct{}
	cleanupOnce, releaseOnce                     sync.Once
}

func TestActualGRPCFailedStartReadinessSurvivesNativeCleanup(t *testing.T) {
	for _, failure := range []string{"create", "on-start"} {
		t.Run(failure, func(t *testing.T) {
			scenario := newFailedStartScenario(t, failure)
			defer scenario.cancel()
			defer scenario.releaseOnce.Do(func() { close(scenario.releaseCleanup) })
			scenario.checkInitialFailure(t, failure)
			scenario.checkRecovery(t)
		})
	}
}

func newFailedStartScenario(t *testing.T, failure string) *failedStartScenario {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	s := &failedStartScenario{
		ctx:            ctx,
		cancel:         cancel,
		allocated:      map[string]bool{},
		cleanupEntered: make(chan struct{}),
		releaseCleanup: make(chan struct{}),
	}
	s.failing.Store(true)
	s.native = httptest.NewServer(http.HandlerFunc(s.serveNative))
	t.Cleanup(s.native.Close)
	controller, err := resourcev2.NewController(resourcev2.Config[failedStartNativeClient]{
		Catalog: func(context.Context, resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
			s.catalogs.Add(1)
			return resourcev2.Catalog{}, nil
		},
		Lifecycle: s.lifecycle(failure),
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
	if _, err = s.client.LoadConnections(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *failedStartScenario) serveNative(w http.ResponseWriter, r *http.Request) {
	s.httpCalls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Method == http.MethodPost {
		if r.URL.Path == "/create" && s.failing.Load() {
			http.Error(w, "native creation denied", http.StatusServiceUnavailable)
			return
		}
		id := strconv.Itoa(int(s.nextID.Add(1)))
		s.allocated[id] = true
		w.Header().Set("X-Native-Id", id)
		w.WriteHeader(http.StatusCreated)
		return
	}
	if r.Method == http.MethodDelete && s.allocated[strings.TrimPrefix(r.URL.Path, "/")] {
		delete(s.allocated, strings.TrimPrefix(r.URL.Path, "/"))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.NotFound(w, r)
}

func (s *failedStartScenario) lifecycle(failure string) *resourcev2.Lifecycle[failedStartNativeClient] {
	return &resourcev2.Lifecycle[failedStartNativeClient]{
		LoadConnections: func(context.Context) ([]types.Connection, error) {
			return []types.Connection{
				{ID: "native", Name: "original", Data: map[string]any{"mode": failure}},
			}, nil
		},
		CreateClient: s.createClient,
		OnStart: func(_ context.Context, _ types.Connection, client *failedStartNativeClient) error {
			if client.mode == "on-start" && s.failing.Load() {
				return fmt.Errorf("native startup failed")
			}
			return nil
		},
		DestroyClient: s.destroyClient,
		CheckConnection: func(
			context.Context, types.Connection, *failedStartNativeClient,
		) (types.ConnectionStatus, error) {
			s.probes.Add(1)
			return types.ConnectionStatus{}, fmt.Errorf("health probe must not run")
		},
	}
}

func (s *failedStartScenario) createClient(
	ctx context.Context, connection types.Connection,
) (*failedStartNativeClient, error) {
	s.created.Add(1)
	mode, ok := connection.Data["mode"].(string)
	if !ok {
		panic("connection fixture mode must be a string")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.native.URL+"/"+mode, nil)
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
	if response.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("native creation failed")
	}
	return &failedStartNativeClient{id: response.Header.Get("X-Native-Id"), mode: mode}, nil
}

func (s *failedStartScenario) destroyClient(ctx context.Context, client *failedStartNativeClient) error {
	if client.mode == "on-start" && s.failing.Load() {
		s.cleanupOnce.Do(func() {
			close(s.cleanupEntered)
			select {
			case <-s.releaseCleanup:
			case <-ctx.Done():
			}
		})
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.native.URL+"/"+client.id, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	if err = response.Body.Close(); err != nil {
		return err
	}
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("native cleanup failed")
	}
	return nil
}

func (s *failedStartScenario) state(t *testing.T, want types.ConnectionStatusCode) {
	t.Helper()
	beforeHTTP, beforeCreate := s.httpCalls.Load(), s.created.Load()
	status, err := s.client.GetConnectionStatus(s.ctx, "native")
	if err != nil || status.Status != want || status.Connection == nil || status.Error != "" ||
		status.Details != "" {
		t.Fatalf("want %s: %+v %v", want, status, err)
	}
	if s.httpCalls.Load() != beforeHTTP || s.created.Load() != beforeCreate || s.probes.Load() != 0 ||
		s.catalogs.Load() != 0 {
		t.Fatal("readiness performed native work")
	}
}

func (s *failedStartScenario) allocationCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.allocated)
}

func (s *failedStartScenario) failedStart(t *testing.T) {
	t.Helper()
	if _, err := s.client.StartConnection(s.ctx, "native"); err == nil {
		t.Fatal("native failure returned success")
	}
	s.state(t, types.ConnectionStatusFailed)
	if remaining := s.allocationCount(); remaining != 0 {
		t.Fatalf("failed startup leaked %d actual native allocations", remaining)
	}
}

func (s *failedStartScenario) checkInitialFailure(t *testing.T, failure string) {
	t.Helper()
	// Keep this defer in the running scenario, ahead of all registered cleanup.
	defer s.releaseOnce.Do(func() { close(s.releaseCleanup) })
	if failure != "on-start" {
		s.failedStart(t)
		return
	}
	started := make(chan error, 1)
	go func() { _, err := s.client.StartConnection(s.ctx, "native"); started <- err }()
	select {
	case <-s.cleanupEntered:
	case <-s.ctx.Done():
		t.Fatal(s.ctx.Err())
	}
	s.state(t, types.ConnectionStatusFailed)
	if s.allocationCount() != 1 {
		t.Fatal("OnStart failure did not retain actual native resource during blocked cleanup")
	}
	s.releaseOnce.Do(func() { close(s.releaseCleanup) })
	if err := <-started; err == nil {
		t.Fatal("OnStart failure returned success")
	}
	s.state(t, types.ConnectionStatusFailed)
	if s.allocationCount() != 0 {
		t.Fatal("failed OnStart did not clean actual native resource")
	}
}

func (s *failedStartScenario) checkRecovery(t *testing.T) {
	t.Helper()
	connection, err := s.client.GetConnection(s.ctx, "native")
	if err != nil {
		t.Fatal(err)
	}
	connection.Name = "renamed"
	if _, err = s.client.UpdateConnection(s.ctx, connection); err != nil {
		t.Fatal(err)
	}
	s.state(t, types.ConnectionStatusFailed)
	s.failing.Store(false)
	if _, err = s.client.StartConnection(s.ctx, "native"); err != nil {
		t.Fatal(err)
	}
	s.state(t, types.ConnectionStatusConnected)
	if _, err = s.client.StopConnection(s.ctx, "native"); err != nil {
		t.Fatal(err)
	}
	s.state(t, types.ConnectionStatusDisconnected)
	s.failing.Store(true)
	s.failedStart(t)
	if _, err = s.client.StopConnection(s.ctx, "native"); err != nil {
		t.Fatal(err)
	}
	s.state(t, types.ConnectionStatusDisconnected)
	s.failedStart(t)
	connection.Data = map[string]any{"mode": "healthy"}
	if err = s.client.ApplyConnectionOverlay(
		s.ctx,
		resourcev2.ConnectionOverlay{Upserts: []types.Connection{connection}, DisabledIDs: []string{}},
	); err != nil {
		t.Fatal(err)
	}
	s.state(t, types.ConnectionStatusDisconnected)
	if _, err = s.client.StartConnection(s.ctx, "native"); err != nil {
		t.Fatal(err)
	}
	s.state(t, types.ConnectionStatusConnected)
	if _, err = s.client.StopConnection(s.ctx, "native"); err != nil {
		t.Fatal(err)
	}
	s.state(t, types.ConnectionStatusDisconnected)
	if s.allocationCount() != 0 {
		t.Fatal("recovery leaked actual native resources")
	}
}
