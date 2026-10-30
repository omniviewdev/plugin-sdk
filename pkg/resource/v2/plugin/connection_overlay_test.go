package plugin

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

func assertConnectionNames(t *testing.T, c *Client, want map[string]string) {
	t.Helper()
	connections, err := c.LoadConnections(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(connections) != len(want) {
		t.Fatalf("connection count %d want %d: %+v", len(connections), len(want), connections)
	}
	seen := map[string]bool{}
	for _, connection := range connections {
		if seen[connection.ID] || want[connection.ID] != connection.Name {
			t.Fatalf("unexpected effective connection: %+v", connection)
		}
		seen[connection.ID] = true
	}
}

func TestActualGRPCConnectionOverlayResetAndNativeRefresh(t *testing.T) {
	var mu sync.Mutex
	native := []types.Connection{{ID: "native", Name: "native-v1"}}
	var destroyed atomic.Int32
	controller, err := resourcev2.NewController(
		resourcev2.Config[int]{Catalog: func(context.Context, resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
			return resourcev2.Catalog{
				Provider: "test",
				Revision: "catalog-1",
				Types:    []resourcev2.ResourceType{},
				Coverage: resourcev2.Coverage{Kind: "complete", Paths: []string{}},
			}, nil
		}, Lifecycle: &resourcev2.Lifecycle[int]{LoadConnections: func(context.Context) ([]types.Connection, error) {
			mu.Lock()
			defer mu.Unlock()
			return append([]types.Connection(nil), native...), nil
		}, CreateClient: func(context.Context, types.Connection) (*int, error) { value := 1; return &value, nil }, DestroyClient: func(context.Context, *int) error { destroyed.Add(1); return nil }}},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Error(err)
		}
	})
	s := grpc.NewServer()
	RegisterServer(s, controller)
	c := NewClient(pipe(t, s))
	ctx := context.Background()
	features, err := c.Features(ctx)
	if err != nil || !features.Has(resourcev2.FeatureConnectionOverlay) {
		t.Fatalf("real overlay feature unavailable: %+v %v", features, err)
	}
	assertConnectionNames(t, c, map[string]string{"native": "native-v1"})
	if _, err = c.StartConnection(ctx, "native"); err != nil {
		t.Fatal(err)
	}
	overlay := resourcev2.ConnectionOverlay{
		Upserts:     []types.Connection{{ID: "native", Name: "override"}, {ID: "custom", Name: "host-custom"}},
		DisabledIDs: []string{},
	}
	if err = c.ApplyConnectionOverlay(ctx, overlay); err != nil {
		t.Fatal(err)
	}
	if destroyed.Load() != 0 {
		t.Fatal("display-only overlay drained the native client")
	}
	assertConnectionNames(t, c, map[string]string{"native": "override", "custom": "host-custom"})
	overlay.Upserts[0].Data = map[string]any{"endpoint": "https://changed.example"}
	if err = c.ApplyConnectionOverlay(ctx, overlay); err != nil {
		t.Fatal(err)
	}
	if destroyed.Load() != 1 {
		t.Fatal("runtime connection data change did not drain the native client")
	}
	mu.Lock()
	native = []types.Connection{{ID: "native", Name: "native-v2"}, {ID: "fresh", Name: "new-native"}}
	mu.Unlock()
	snapshot, err := c.NativeConnections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != 1 || snapshot[0].Name != "native-v1" {
		t.Fatal("NativeConnections performed an implicit refresh or returned overlay intent")
	}
	assertConnectionNames(t, c, map[string]string{"native": "override", "custom": "host-custom", "fresh": "new-native"})
	overlay.DisabledIDs = []string{"fresh"}
	if err = c.ApplyConnectionOverlay(ctx, overlay); err != nil {
		t.Fatal(err)
	}
	assertConnectionNames(t, c, map[string]string{"native": "override", "custom": "host-custom"})
	snapshot, err = c.NativeConnections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != 2 {
		t.Fatal("native provenance omitted disabled native connection or included manual connection")
	}
	for _, connection := range snapshot {
		if connection.ID == "custom" || connection.ID == "native" && connection.Name != "native-v2" {
			t.Fatal("native provenance contains overlay data")
		}
	}
	if err = c.ApplyConnectionOverlay(
		ctx,
		resourcev2.ConnectionOverlay{Upserts: []types.Connection{}, DisabledIDs: []string{}},
	); err != nil {
		t.Fatal(err)
	}
	assertConnectionNames(t, c, map[string]string{"native": "native-v2", "fresh": "new-native"})
	if _, err = c.GetConnection(ctx, "custom"); resourcev2.ErrorCode(err) != "NOT_FOUND" {
		t.Fatalf("reset retained ephemeral custom connection: %v", err)
	}
	mu.Lock()
	if len(native) != 2 || native[0].Name != "native-v2" {
		t.Fatal("overlay mutated native discovery")
	}
	mu.Unlock()
}

type overlayObserver struct {
	countProvider
	calls atomic.Int32
}

func (p *overlayObserver) ApplyConnectionOverlay(context.Context, resourcev2.ConnectionOverlay) error {
	p.calls.Add(1)
	return nil
}

func TestActualGRPCOverlayRejectsAmbiguousAndConflictingIntent(t *testing.T) {
	p := &overlayObserver{}
	s := grpc.NewServer()
	RegisterServer(s, p)
	c := NewClient(pipe(t, s))
	ctx := context.Background()
	for _, data := range [][]byte{[]byte(`{"upserts":[],"disabledIDs":[],"disabledIDs":[]}`), []byte(`{"upserts":[{"id":"same"}],"disabledIDs":["same"]}`), []byte(`{"upserts":[],"disabledIDs":[],"unknown":true}`)} {
		_, err := c.ancillary(
			ctx,
			&resourcepb.AncillaryRequest{Operation: "applyConnectionOverlay", ConnectionJson: data},
		)
		if resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
			t.Fatalf("invalid overlay accepted: %v", err)
		}
	}
	if p.calls.Load() != 0 {
		t.Fatal("invalid overlay dispatched")
	}
	if err := c.ApplyConnectionOverlay(
		ctx,
		resourcev2.ConnectionOverlay{Upserts: []types.Connection{}, DisabledIDs: []string{}},
	); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 1 {
		t.Fatal("explicit overlay reset did not dispatch")
	}
}

func (p *overlayObserver) NativeConnections(context.Context) ([]types.Connection, error) {
	return []types.Connection{}, nil
}
