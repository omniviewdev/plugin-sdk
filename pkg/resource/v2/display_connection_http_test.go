package resourcev2

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

func TestDisplayRenamePreservesActiveNativeHTTPRequestAndPrivateCredentials(t *testing.T) {
	entered := make(chan struct{})
	canceled := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer source-secret" {
			http.Error(w, "wrong native credentials", http.StatusUnauthorized)
			return
		}
		close(entered)
		select {
		case <-request.Context().Done():
			canceled <- struct{}{}
		case <-release:
			_, _ = io.WriteString(w, "native-A-response")
		}
	}))
	t.Cleanup(endpoint.Close)
	connection := ownershipConnection(endpoint.URL)
	connection.Name = "old name"
	connection.SetSensitiveData(map[string]any{"token": "source-secret"})
	cfg := Config[ownershipEndpointClient]{Catalog: controllerConfig().Catalog, Lifecycle: &Lifecycle[ownershipEndpointClient]{
		LoadConnections: func(context.Context) ([]types.Connection, error) { return []types.Connection{connection}, nil },
		CreateClient: func(_ context.Context, configured types.Connection) (*ownershipEndpointClient, error) {
			return &ownershipEndpointClient{endpoint: ownershipEndpoint(configured), http: endpoint.Client()}, nil
		},
		DestroyClient: func(context.Context, *ownershipEndpointClient) error { return nil },
	}}
	controller, err := NewController(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		unblock()
		if err := controller.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx := context.Background()
	route := AccessRoute{Connection: connection.ID}
	if _, err := controller.LoadConnections(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.StartConnection(ctx, connection.ID); err != nil {
		t.Fatal(err)
	}
	accepted, err := controller.Catalog(ctx, CatalogRequest{Route: route})
	if err != nil {
		t.Fatal(err)
	}
	acceptedCtx := WithCatalogRevision(ctx, accepted.Revision)
	type leaseObservation struct {
		ctx    context.Context
		client *ownershipEndpointClient
	}
	observed := make(chan leaseObservation, 1)
	result := make(chan error, 1)
	body := make(chan string, 1)
	go func() {
		result <- controller.WithClient(acceptedCtx, route, func(native context.Context, client *ownershipEndpointClient) error {
			observed <- leaseObservation{ctx: native, client: client}
			request, err := http.NewRequestWithContext(native, http.MethodGet, client.endpoint, nil)
			if err != nil {
				return err
			}
			session, _ := SessionFromContext(native)
			request.Header.Set("Authorization", "Bearer "+session.Connection.GetSensitiveData()["token"].(string))
			response, err := client.http.Do(request)
			if err != nil {
				return err
			}
			defer response.Body.Close()
			data, err := io.ReadAll(response.Body)
			if err == nil {
				body <- string(data)
			}
			return err
		})
	}()
	active := <-observed
	<-entered // The real native HTTP endpoint now holds an in-flight request.
	override := ownershipConnection(endpoint.URL)
	override.Name = "renamed"
	if err := controller.ApplyConnectionOverlay(ctx, ConnectionOverlay{Upserts: []types.Connection{override}}); err != nil {
		t.Fatal(err)
	}
	current, err := controller.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Name != "renamed" || ownershipEndpoint(current) != endpoint.URL || current.GetSensitiveData()["token"] != "source-secret" {
		t.Fatal("display overlay changed native endpoint/credentials or retained old name")
	}
	after, err := controller.Catalog(ctx, CatalogRequest{Route: route})
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != accepted.Revision {
		t.Fatal("display rename changed accepted catalog observation")
	}
	beforeEpoch, err := CatalogBindingEpoch(accepted.Revision)
	if err != nil {
		t.Fatal(err)
	}
	afterEpoch, err := CatalogBindingEpoch(after.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if beforeEpoch != afterEpoch {
		t.Fatal("display rename changed native binding epoch")
	}
	if err := controller.WithClient(acceptedCtx, route, func(native context.Context, client *ownershipEndpointClient) error {
		if client != active.client {
			t.Error("display rename replaced active native client")
		}
		session, _ := SessionFromContext(native)
		if session.Connection.Name != "renamed" || ownershipEndpoint(*session.Connection) != endpoint.URL {
			t.Error("new native lease retained stale display metadata")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := controller.WithConnection(ctx, route, func(_ context.Context, configured types.Connection) error {
		if configured.Name != "renamed" || configured.GetSensitiveData()["token"] != "source-secret" {
			t.Error("utility lease lost current display/native credentials")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if active.ctx.Err() != nil {
		t.Fatal("display rename canceled active native request context")
	}
	select {
	case <-canceled:
		t.Fatal("native HTTP request canceled during display rename")
	default:
	}
	unblock()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if got := <-body; got != "native-A-response" {
		t.Fatalf("native HTTP response = %q", got)
	}
}
