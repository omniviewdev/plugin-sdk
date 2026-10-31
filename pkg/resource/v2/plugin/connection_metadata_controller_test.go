package plugin

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

func TestConnectionMetadataControllerActualGRPCLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var observations atomic.Int32
	controller, err := resourcev2.NewController(resourcev2.Config[string]{
		Catalog: func(context.Context, resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
			return resourcev2.Catalog{Provider: "metadata", Revision: "native", Types: []resourcev2.ResourceType{}, Coverage: resourcev2.Coverage{Kind: "complete", Paths: []string{}}}, nil
		},
		Lifecycle: &resourcev2.Lifecycle[string]{
			LoadConnections: func(context.Context) ([]types.Connection, error) {
				return []types.Connection{{ID: "connection"}}, nil
			},
			CreateClient: func(context.Context, types.Connection) (*string, error) {
				client := "native"
				return &client, nil
			},
			DestroyClient: func(context.Context, *string) error { return nil },
		},
		ConnectionMetadata: func(_ context.Context, connection types.Connection, client *string) (resourcev2.ConnectionMetadataUpdate, error) {
			if connection.ID != "connection" || client == nil || *client != "native" {
				return resourcev2.ConnectionMetadataUpdate{}, resourcev2.NewError("PROVIDER_ERROR", "wrong native client")
			}
			value := strconv.Itoa(int(observations.Add(1)))
			return resourcev2.ConnectionMetadataUpdate{Set: map[string]resourcev2.ConnectionMetadataObservation{
				"test.version": {State: "success", Value: value, ObservedAt: time.Now().UTC()},
				"test.null":    {State: "success", Value: nil, ObservedAt: time.Now().UTC()},
			}}, nil
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
	s := grpc.NewServer()
	RegisterServer(s, controller)
	c := NewClient(pipe(t, s))
	features, err := c.Features(ctx)
	if err != nil {
		t.Fatal(err)
	}
	supported := false
	for _, feature := range features.Traits {
		if feature == resourcev2.FeatureConnectionMetadata {
			supported = true
		}
	}
	if !supported {
		t.Fatal("controller metadata feature absent")
	}
	if _, err := c.LoadConnections(ctx); err != nil {
		t.Fatal(err)
	}
	initial, err := c.GetConnectionMetadata(ctx, "connection")
	if err != nil || initial.Active || len(initial.Entries) != 0 || observations.Load() != 0 {
		t.Fatalf("read acquired metadata or lost initial inventory: %#v, %v", initial, err)
	}
	events := make(chan resourcev2.ConnectionMetadataEvent, 64)
	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	watchDone := make(chan error, 1)
	go func() {
		watchDone <- c.WatchConnectionMetadata(watchCtx, func(event resourcev2.ConnectionMetadataEvent) error {
			select {
			case events <- event:
				return nil
			case <-watchCtx.Done():
				return watchCtx.Err()
			}
		})
	}()
	wait := func(match func(resourcev2.ConnectionMetadataEvent) bool) resourcev2.ConnectionMetadataEvent {
		t.Helper()
		for {
			select {
			case event := <-events:
				if match(event) {
					return event
				}
			case err := <-watchDone:
				t.Fatalf("watch ended early: %v", err)
			case <-ctx.Done():
				t.Fatal("metadata lifecycle event timeout")
			}
		}
	}
	wait(func(event resourcev2.ConnectionMetadataEvent) bool { return !event.Snapshot.Active && !event.Removed })
	if _, err := c.StartConnection(ctx, "connection"); err != nil {
		t.Fatal(err)
	}
	current := wait(func(event resourcev2.ConnectionMetadataEvent) bool {
		return event.Snapshot.Active && !event.Snapshot.Refreshing && event.Snapshot.Entries["test.version"].State == "current"
	})
	if current.Snapshot.Generation == "" || current.Snapshot.Entries["test.null"].Value != nil {
		t.Fatalf("native generation or null lost: %#v", current)
	}
	refreshed, err := c.RefreshConnectionMetadata(ctx, "connection")
	if err != nil || refreshed.Entries["test.version"].Value != "2" || observations.Load() != 2 {
		t.Fatalf("refresh did not acquire through native callback: %#v, %v", refreshed, err)
	}
	wait(func(event resourcev2.ConnectionMetadataEvent) bool {
		return event.Snapshot.Revision == refreshed.Revision
	})
	if _, err := c.StopConnection(ctx, "connection"); err != nil {
		t.Fatal(err)
	}
	stale := wait(func(event resourcev2.ConnectionMetadataEvent) bool {
		return !event.Snapshot.Active && event.Snapshot.Entries["test.version"].State == "stale"
	})
	if stale.Snapshot.Entries["test.version"].Value != "2" {
		t.Fatal("stop discarded observed value")
	}
	if err := controller.ApplyConnections(ctx, nil); err != nil {
		t.Fatal(err)
	}
	removed := wait(func(event resourcev2.ConnectionMetadataEvent) bool { return event.Removed })
	if removed.Snapshot.ConnectionID != "connection" || removed.Snapshot.Active || len(removed.Snapshot.Entries) != 0 {
		t.Fatalf("invalid removal tombstone: %#v", removed)
	}
	if _, err := c.GetConnectionMetadata(ctx, "connection"); resourcev2.ErrorCode(err) != "NOT_FOUND" {
		t.Fatalf("removed connection remained readable: %v", err)
	}
	stopWatch()
	select {
	case <-watchDone:
	case <-ctx.Done():
		t.Fatal("watch did not stop")
	}
}
