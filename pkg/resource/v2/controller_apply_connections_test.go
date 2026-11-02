package resourcev2

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

func TestControllerApplyConnectionsRetiresChangedAndRemovedClients(t *testing.T) {
	cfg := controllerConfig()
	var destroyed atomic.Int32
	cfg.Lifecycle = controllerLifecycle(&destroyed)
	c := newTestController(t, cfg)
	startTestConnection(t, c)
	if _, err := c.StartConnection(context.Background(), "other"); err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyConnections(context.Background(), []types.Connection{
		{ID: "explicit", Name: "Explicit"},
		{ID: "other", Name: "Changed", Data: map[string]any{"endpoint": "https://changed.invalid"}},
	}); err != nil {
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
	if _, err := c.UpdateConnection(context.Background(), types.Connection{ID: "other", Name: "Updated"}); err != nil {
		t.Fatal(err)
	}
	connection, err := c.GetConnection(context.Background(), "other")
	if err != nil || connection.Name != "Updated" {
		t.Fatal("connection update not applied")
	}
}
