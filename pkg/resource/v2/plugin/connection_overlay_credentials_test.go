package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	"google.golang.org/grpc"
)

func TestActualGRPCOverlayCredentialsAuthenticateReplaceClearAndRemainPrivate(t *testing.T) {
	var expected atomic.Value
	expected.Store("native-password")
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		want := expected.Load().(string)
		if !ok || user != "smoke" || password == "" || want != "" && password != want {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer auth.Close()
	native := types.Connection{ID: "registry", UID: "registry-uid", Data: map[string]any{"host": auth.URL}}
	native.SetSensitiveData(map[string]any{"username": "smoke", "token": "native-password"})
	var destroyed atomic.Int32
	controller, err := resourcev2.NewController(resourcev2.Config[int]{
		Catalog: func(context.Context, resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
			return resourcev2.Catalog{Provider: "test", Revision: "1", Types: []resourcev2.ResourceType{}, Coverage: resourcev2.Coverage{Kind: "complete"}}, nil
		},
		Lifecycle: &resourcev2.Lifecycle[int]{
			LoadConnections: func(context.Context) ([]types.Connection, error) { return []types.Connection{native}, nil },
			CreateClient: func(ctx context.Context, connection types.Connection) (*int, error) {
				request, err := http.NewRequestWithContext(ctx, http.MethodGet, auth.URL, nil)
				if err != nil {
					return nil, err
				}
				private := connection.GetSensitiveData()
				username, _ := private["username"].(string)
				token, _ := private["token"].(string)
				request.SetBasicAuth(username, token)
				response, err := http.DefaultClient.Do(request)
				if err != nil {
					return nil, err
				}
				defer response.Body.Close()
				if response.StatusCode != http.StatusNoContent {
					return nil, resourcev2.NewError("UNAUTHORIZED", "Registry authentication failed.")
				}
				client := 1
				return &client, nil
			},
			DestroyClient: func(context.Context, *int) error { destroyed.Add(1); return nil },
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
	ctx := context.Background()
	if _, err := client.LoadConnections(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.StartConnection(ctx, native.ID); err != nil {
		t.Fatal(err)
	}

	override := native
	override.SetSensitiveData(map[string]any{"username": "smoke", "token": "replacement-password"})
	expected.Store("replacement-password")
	before := destroyed.Load()
	if err := client.ApplyConnectionOverlay(ctx, resourcev2.ConnectionOverlay{Upserts: []types.Connection{override}}); err != nil {
		t.Fatal(err)
	}
	if destroyed.Load() != before+1 {
		t.Fatal("credential replacement did not retire the active client")
	}
	if _, err := client.StartConnection(ctx, native.ID); err != nil {
		t.Fatalf("replacement credentials did not authenticate: %v", err)
	}

	public, err := client.GetConnection(ctx, native.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if public.GetSensitiveData() != nil || strings.Contains(string(encoded), "replacement-password") || strings.Contains(string(encoded), "sensitiveData") {
		t.Fatal("a public connection response exposed private credentials")
	}

	override.Name = "Inherit current native credentials"
	override.SetSensitiveData(nil)
	expected.Store("native-password")
	before = destroyed.Load()
	if err := client.ApplyConnectionOverlay(ctx, resourcev2.ConnectionOverlay{Upserts: []types.Connection{override}}); err != nil {
		t.Fatal(err)
	}
	if destroyed.Load() != before+1 {
		t.Fatal("restoring source credentials did not retire the replacement client")
	}
	if _, err := client.StartConnection(ctx, native.ID); err != nil {
		t.Fatalf("omission did not inherit native credentials: %v", err)
	}
	override.Name = "Display-only change"
	before = destroyed.Load()
	if err := client.ApplyConnectionOverlay(ctx, resourcev2.ConnectionOverlay{Upserts: []types.Connection{override}}); err != nil {
		t.Fatal(err)
	}
	if destroyed.Load() != before {
		t.Fatal("display-only change retired unchanged source credentials")
	}

	override.SetSensitiveData(map[string]any{})
	expected.Store("") // Any inherited nonempty password would authenticate, but an explicit clear must not.
	before = destroyed.Load()
	if err := client.ApplyConnectionOverlay(ctx, resourcev2.ConnectionOverlay{Upserts: []types.Connection{override}}); err != nil {
		t.Fatal(err)
	}
	if destroyed.Load() != before+1 {
		t.Fatal("explicit credential clear did not retire the active client")
	}
	if _, err := client.StartConnection(ctx, native.ID); resourcev2.ErrorCode(err) != "UNAUTHORIZED" {
		t.Fatalf("cleared credentials inherited a password: %v", err)
	}

	expected.Store("native-password")
	if err := client.ApplyConnectionOverlay(ctx, resourcev2.ConnectionOverlay{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.StartConnection(ctx, native.ID); err != nil {
		t.Fatalf("reset did not restore native credentials: %v", err)
	}
}

func TestActualGRPCOverlayPrivatePayloadAdmission(t *testing.T) {
	observer := &overlayObserver{}
	server := grpc.NewServer()
	RegisterServer(server, observer)
	client := NewClient(pipe(t, server))
	ctx := context.Background()
	cyclic := map[string]any{}
	cyclic["nested"] = cyclic
	connection := types.Connection{ID: "registry"}
	connection.SetSensitiveData(cyclic)
	if err := client.ApplyConnectionOverlay(ctx, resourcev2.ConnectionOverlay{Upserts: []types.Connection{connection}}); resourcev2.ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("unbounded private payload was accepted: %v", err)
	}
	connection.SetSensitiveData(map[string]any{"token": string([]byte{0xff})})
	if err := client.ApplyConnectionOverlay(ctx, resourcev2.ConnectionOverlay{Upserts: []types.Connection{connection}}); resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("invalid private UTF-8 was accepted: %v", err)
	}
	_, err := client.ancillary(ctx, &resourcepb.AncillaryRequest{Operation: connectionOverlayOperation, ConnectionJson: []byte(`{"upserts":[{"id":"registry","sensitiveData":{"token":"one","token":"two"}}],"disabledIDs":[]}`)})
	if resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("ambiguous private payload was accepted: %v", err)
	}
	if observer.calls.Load() != 0 {
		t.Fatal("invalid private intent reached the provider")
	}
}
