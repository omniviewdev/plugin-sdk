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

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"google.golang.org/grpc"
)

func TestActualGRPCFailedStartReadinessSurvivesNativeCleanup(t *testing.T) {
	for _, failure := range []string{"create", "on-start"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			var failing atomic.Bool
			failing.Store(true)
			var created, probes, catalogs, httpCalls, nextID atomic.Int32
			var mu sync.Mutex
			allocated := map[string]bool{}
			native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				httpCalls.Add(1)
				mu.Lock()
				defer mu.Unlock()
				if r.Method == http.MethodPost {
					if r.URL.Path == "/create" && failing.Load() {
						http.Error(w, "native creation denied", http.StatusServiceUnavailable)
						return
					}
					id := strconv.Itoa(int(nextID.Add(1)))
					allocated[id] = true
					w.Header().Set("X-Native-ID", id)
					w.WriteHeader(http.StatusCreated)
					return
				}
				if r.Method == http.MethodDelete && allocated[strings.TrimPrefix(r.URL.Path, "/")] {
					delete(allocated, strings.TrimPrefix(r.URL.Path, "/"))
					w.WriteHeader(http.StatusNoContent)
					return
				}
				http.NotFound(w, r)
			}))
			t.Cleanup(native.Close)
			type nativeClient struct{ id, mode string }
			cleanupEntered, releaseCleanup := make(chan struct{}), make(chan struct{})
			var cleanupOnce, releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(releaseCleanup) })
			controller, err := resourcev2.NewController(resourcev2.Config[nativeClient]{
				Catalog: func(context.Context, resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
					catalogs.Add(1)
					return resourcev2.Catalog{}, nil
				},
				Lifecycle: &resourcev2.Lifecycle[nativeClient]{
					LoadConnections: func(context.Context) ([]types.Connection, error) {
						return []types.Connection{{ID: "native", Name: "original", Data: map[string]any{"mode": failure}}}, nil
					},
					CreateClient: func(ctx context.Context, connection types.Connection) (*nativeClient, error) {
						created.Add(1)
						mode := connection.Data["mode"].(string)
						request, err := http.NewRequestWithContext(ctx, http.MethodPost, native.URL+"/"+mode, nil)
						if err != nil {
							return nil, err
						}
						response, err := http.DefaultClient.Do(request)
						if err != nil {
							return nil, err
						}
						defer response.Body.Close()
						if response.StatusCode != http.StatusCreated {
							return nil, fmt.Errorf("native creation failed")
						}
						return &nativeClient{id: response.Header.Get("X-Native-ID"), mode: mode}, nil
					},
					OnStart: func(_ context.Context, _ types.Connection, client *nativeClient) error {
						if client.mode == "on-start" && failing.Load() {
							return fmt.Errorf("native startup failed")
						}
						return nil
					},
					DestroyClient: func(ctx context.Context, client *nativeClient) error {
						if client.mode == "on-start" && failing.Load() {
							cleanupOnce.Do(func() {
								close(cleanupEntered)
								select {
								case <-releaseCleanup:
								case <-ctx.Done():
								}
							})
						}
						request, err := http.NewRequestWithContext(ctx, http.MethodDelete, native.URL+"/"+client.id, nil)
						if err != nil {
							return err
						}
						response, err := http.DefaultClient.Do(request)
						if err != nil {
							return err
						}
						response.Body.Close()
						if response.StatusCode != http.StatusNoContent {
							return fmt.Errorf("native cleanup failed")
						}
						return nil
					},
					CheckConnection: func(context.Context, types.Connection, *nativeClient) (types.ConnectionStatus, error) {
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
			if _, err := client.LoadConnections(ctx); err != nil {
				t.Fatal(err)
			}
			state := func(want types.ConnectionStatusCode) {
				t.Helper()
				beforeHTTP, beforeCreate := httpCalls.Load(), created.Load()
				status, err := client.GetConnectionStatus(ctx, "native")
				if err != nil || status.Status != want || status.Connection == nil || status.Error != "" || status.Details != "" {
					t.Fatalf("want %s: %+v %v", want, status, err)
				}
				if httpCalls.Load() != beforeHTTP || created.Load() != beforeCreate || probes.Load() != 0 || catalogs.Load() != 0 {
					t.Fatal("readiness performed native work")
				}
			}
			failedStart := func() {
				t.Helper()
				if _, err := client.StartConnection(ctx, "native"); err == nil {
					t.Fatal("native failure returned success")
				}
				state(types.ConnectionStatusFailed)
				mu.Lock()
				remaining := len(allocated)
				mu.Unlock()
				if remaining != 0 {
					t.Fatalf("failed startup leaked %d actual native allocations", remaining)
				}
			}
			if failure == "on-start" {
				started := make(chan error, 1)
				go func() { _, err := client.StartConnection(ctx, "native"); started <- err }()
				select {
				case <-cleanupEntered:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				state(types.ConnectionStatusFailed)
				mu.Lock()
				remaining := len(allocated)
				mu.Unlock()
				if remaining != 1 {
					t.Fatal("OnStart failure did not retain actual native resource during blocked cleanup")
				}
				releaseOnce.Do(func() { close(releaseCleanup) })
				if err := <-started; err == nil {
					t.Fatal("OnStart failure returned success")
				}
				state(types.ConnectionStatusFailed)
				mu.Lock()
				remaining = len(allocated)
				mu.Unlock()
				if remaining != 0 {
					t.Fatal("failed OnStart did not clean actual native resource")
				}
			} else {
				failedStart()
			}
			connection, err := client.GetConnection(ctx, "native")
			if err != nil {
				t.Fatal(err)
			}
			connection.Name = "renamed"
			if _, err := client.UpdateConnection(ctx, connection); err != nil {
				t.Fatal(err)
			}
			state(types.ConnectionStatusFailed)
			failing.Store(false)
			if _, err := client.StartConnection(ctx, "native"); err != nil {
				t.Fatal(err)
			}
			state(types.ConnectionStatusConnected)
			if _, err := client.StopConnection(ctx, "native"); err != nil {
				t.Fatal(err)
			}
			state(types.ConnectionStatusDisconnected)
			failing.Store(true)
			failedStart()
			if _, err := client.StopConnection(ctx, "native"); err != nil {
				t.Fatal(err)
			}
			state(types.ConnectionStatusDisconnected)
			failedStart()
			connection.Data = map[string]any{"mode": "healthy"}
			if err := client.ApplyConnectionOverlay(ctx, resourcev2.ConnectionOverlay{Upserts: []types.Connection{connection}, DisabledIDs: []string{}}); err != nil {
				t.Fatal(err)
			}
			state(types.ConnectionStatusDisconnected)
			if _, err := client.StartConnection(ctx, "native"); err != nil {
				t.Fatal(err)
			}
			state(types.ConnectionStatusConnected)
			if _, err := client.StopConnection(ctx, "native"); err != nil {
				t.Fatal(err)
			}
			state(types.ConnectionStatusDisconnected)
			mu.Lock()
			remaining := len(allocated)
			mu.Unlock()
			if remaining != 0 {
				t.Fatal("recovery leaked actual native resources")
			}
		})
	}
}
