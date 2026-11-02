package plugin

import (
	"context"
	"testing"
	"time"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"google.golang.org/grpc"
)

func TestControllerSourceStopResetsBindingOverTransport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered := make(chan struct{})
	catalogProvider := &transportSourceProvider{}
	controller, err := resourcev2.NewController(resourcev2.Config[string]{
		Catalog: catalogProvider.Catalog,
		Lifecycle: &resourcev2.Lifecycle[string]{
			LoadConnections: func(context.Context) ([]types.Connection, error) {
				return []types.Connection{{ID: "explicit"}}, nil
			},
			CreateClient: func(context.Context, types.Connection) (*string, error) {
				value := "client"
				return &value, nil
			},
			DestroyClient: func(context.Context, *string) error { return nil },
		},
		Registrations: []resourcev2.Registration[string]{{Type: "opaque/type", Source: func(ctx context.Context, _ *string, _ resourcev2.SourceRequest, emitter resourcev2.SourceEmitter) error {
			if err := emitter.EndSnapshot(ctx, resourcev2.SourceCoverage{Enumeration: resourcev2.EnumerationExhausted, Consistency: resourcev2.ConsistencyBestEffort}, resourcev2.ContinuityBestEffort); err != nil {
				return err
			}
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controller.Close() })
	if _, err = controller.LoadConnections(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = controller.StartConnection(ctx, "explicit"); err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	RegisterServer(server, controller)
	client := NewClient(pipe(t, server))
	request := transportSourceRequest()
	request.Mode = resourcev2.SourceModeSnapshotAndUpdates
	stream, err := client.OpenSourceStream(acceptedCatalogContext(ctx, t, client, request.Target.Route), request)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	for range 2 {
		if _, err = stream.Recv(); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	stopped := make(chan error, 1)
	go func() { _, err := controller.StopConnection(ctx, "explicit"); stopped <- err }()
	frame, err := stream.Recv()
	if err != nil || frame.Reset == nil || frame.Reset.Reason != resourcev2.SourceResetBindingChanged {
		t.Fatalf("reset frame = %+v, err = %v", frame, err)
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
