package plugin

import (
	"context"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

func TestActualGRPCManagedConnectionUIWithoutNativeWatch(t *testing.T) {
	var created atomic.Int32
	controller, err := resourcev2.NewController(resourcev2.Config[int]{
		Catalog: func(context.Context, resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
			return resourcev2.Catalog{
				Provider: "managed",
				Revision: "native",
				Types:    []resourcev2.ResourceType{},
				Coverage: resourcev2.Coverage{Kind: "complete", Paths: []string{}},
			}, nil
		},
		Lifecycle: &resourcev2.Lifecycle[int]{LoadConnections: func(context.Context) ([]types.Connection, error) {
			return []types.Connection{{ID: "native", Name: "discovered"}}, nil
		}, CreateClient: func(context.Context, types.Connection) (*int, error) { created.Add(1); value := 1; return &value, nil }, DestroyClient: func(context.Context, *int) error { return nil }},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, fake := any(controller).(resourcev2.ConnectionBrowser); fake {
		t.Fatal("managed fixture unexpectedly implements native watching")
	}
	server := grpc.NewServer()
	RegisterServer(server, controller)
	client := NewClient(pipe(t, server))
	ctx := context.Background()
	features, err := client.Features(ctx)
	if err != nil || !features.Has(resourcev2.FeatureConnections) || features.Has(resourcev2.FeatureConnectionBrowser) {
		t.Fatalf("dishonest managed/native support: %+v %v", features, err)
	}
	if _, err := client.LoadConnections(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.ApplyConnectionOverlay(
		ctx,
		resourcev2.ConnectionOverlay{
			Upserts: []types.Connection{
				{ID: "manual", Name: "configured", Data: map[string]any{"endpoint": "native-config"}},
			},
			DisabledIDs: []string{},
		},
	); err != nil {
		t.Fatal(err)
	}
	connections, err := client.ListConnections(ctx)
	if err != nil || len(connections) != 2 {
		t.Fatalf("managed list requires unrelated watch: %+v %v", connections, err)
	}
	var manual types.Connection
	for _, connection := range connections {
		if connection.ID == "manual" {
			manual = connection
		}
	}
	manual.Name = "renamed"
	if _, err := client.UpdateConnection(ctx, manual); err != nil {
		t.Fatalf("managed update requires unrelated watch: %v", err)
	}
	connections, err = client.ListConnections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, connection := range connections {
		if connection.ID == "manual" && connection.Name == "renamed" {
			found = true
		}
	}
	if !found {
		t.Fatal("managed update not reflected in real registry")
	}
	if err := client.DeleteConnection(ctx, "manual"); err != nil {
		t.Fatalf("managed delete requires unrelated watch: %v", err)
	}
	connections, err = client.ListConnections(ctx)
	if err != nil || len(connections) != 1 || connections[0].ID != "native" {
		t.Fatalf("managed delete not applied: %+v %v", connections, err)
	}
	if created.Load() != 0 {
		t.Fatal("unstarted connection UI implicitly created a native client")
	}
}
