package plugin

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"google.golang.org/grpc"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
)

type metadataTransportProvider struct {
	qualifiedProvider
	snapshot  resourcev2.ConnectionMetadataSnapshot
	getID     string
	refreshID string
	watchErr  error
	watchWait bool
	joined    chan struct{}
}

func (p *metadataTransportProvider) GetConnectionMetadata(_ context.Context, id string) (resourcev2.ConnectionMetadataSnapshot, error) {
	p.getID = id
	return p.snapshot, nil
}

func (p *metadataTransportProvider) RefreshConnectionMetadata(_ context.Context, id string) (resourcev2.ConnectionMetadataSnapshot, error) {
	p.refreshID = id
	return p.snapshot, nil
}

func (p *metadataTransportProvider) WatchConnectionMetadata(ctx context.Context, sink resourcev2.ConnectionMetadataSink) error {
	if p.joined != nil {
		defer close(p.joined)
	}
	if err := sink(resourcev2.ConnectionMetadataEvent{Snapshot: p.snapshot}); err != nil {
		return err
	}
	if p.watchWait {
		<-ctx.Done()
		return ctx.Err()
	}
	if err := sink(resourcev2.ConnectionMetadataEvent{Snapshot: metadataTransportTombstone(), Removed: true}); err != nil {
		return err
	}
	return p.watchErr
}

func metadataTransportSnapshot() resourcev2.ConnectionMetadataSnapshot {
	now := time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC)
	return resourcev2.ConnectionMetadataSnapshot{
		ConnectionID: "connection", Generation: "generation", Revision: "1", Active: true,
		Entries: map[string]resourcev2.ConnectionMetadataEntry{
			"test.null":  {State: "current", Value: nil, ObservedAt: &now},
			"test.false": {State: "current", Value: false, ObservedAt: &now},
			"test.empty": {State: "current", Value: "", ObservedAt: &now},
		},
	}
}

func metadataTransportTombstone() resourcev2.ConnectionMetadataSnapshot {
	return resourcev2.ConnectionMetadataSnapshot{
		ConnectionID: "connection", Revision: "2",
		Entries: map[string]resourcev2.ConnectionMetadataEntry{},
	}
}

func TestConnectionMetadataActualGRPCPreservesNull(t *testing.T) {
	p := &metadataTransportProvider{snapshot: metadataTransportSnapshot()}
	s := grpc.NewServer()
	RegisterServer(s, p)
	c := NewClient(pipe(t, s))
	ctx := context.Background()
	got, err := c.GetConnectionMetadata(ctx, "connection")
	if err != nil || !reflect.DeepEqual(got, p.snapshot) {
		t.Fatalf("get snapshot: %#v, %v", got, err)
	}
	got, err = c.RefreshConnectionMetadata(ctx, "connection")
	if err != nil || !reflect.DeepEqual(got, p.snapshot) {
		t.Fatalf("refresh snapshot: %#v, %v", got, err)
	}
	if p.getID != "connection" || p.refreshID != "connection" {
		t.Fatal("connection ID did not reach provider")
	}
	var events []resourcev2.ConnectionMetadataEvent
	err = c.WatchConnectionMetadata(ctx, func(event resourcev2.ConnectionMetadataEvent) error {
		events = append(events, event)
		return nil
	})
	want := []resourcev2.ConnectionMetadataEvent{{Snapshot: p.snapshot}, {Snapshot: metadataTransportTombstone(), Removed: true}}
	if err != nil || !reflect.DeepEqual(events, want) {
		t.Fatalf("watch snapshots: %#v, %v", events, err)
	}
}

func TestConnectionMetadataActualGRPCUnsupported(t *testing.T) {
	s := grpc.NewServer()
	RegisterServer(s, &qualifiedProvider{})
	c := NewClient(pipe(t, s))
	_, getErr := c.GetConnectionMetadata(context.Background(), "connection")
	_, refreshErr := c.RefreshConnectionMetadata(context.Background(), "connection")
	watchErr := c.WatchConnectionMetadata(context.Background(), func(resourcev2.ConnectionMetadataEvent) error {
		t.Error("unsupported provider emitted metadata")
		return nil
	})
	for _, err := range []error{getErr, refreshErr, watchErr} {
		if resourcev2.ErrorCode(err) != "UNSUPPORTED" {
			t.Fatalf("expected UNSUPPORTED, got %v", err)
		}
	}
}

func TestConnectionMetadataSinkFailureCancelsProvider(t *testing.T) {
	p := &metadataTransportProvider{snapshot: metadataTransportSnapshot(), watchWait: true, joined: make(chan struct{})}
	s := grpc.NewServer()
	RegisterServer(s, p)
	c := NewClient(pipe(t, s))
	want := errors.New("sink stopped")
	err := c.WatchConnectionMetadata(context.Background(), func(resourcev2.ConnectionMetadataEvent) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("sink error lost: %v", err)
	}
	select {
	case <-p.joined:
	case <-time.After(5 * time.Second):
		t.Fatal("metadata provider did not stop after sink failure")
	}
}

func TestConnectionMetadataProviderFailurePropagates(t *testing.T) {
	p := &metadataTransportProvider{snapshot: metadataTransportSnapshot(), watchErr: resourcev2.NewError("UNAVAILABLE", "metadata unavailable")}
	s := grpc.NewServer()
	RegisterServer(s, p)
	c := NewClient(pipe(t, s))
	err := c.WatchConnectionMetadata(context.Background(), func(resourcev2.ConnectionMetadataEvent) error { return nil })
	if resourcev2.ErrorCode(err) != "UNAVAILABLE" {
		t.Fatalf("provider error lost: %v", err)
	}
}
