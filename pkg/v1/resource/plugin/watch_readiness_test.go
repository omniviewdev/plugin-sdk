package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/resource/resourcetest"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/v1/resource"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Delay the real controller's registration, not the source's first event.
// Acknowledgement must precede StartConnection, whose watch emits ADD once.
type delayedWatchRegistration struct {
	resource.Provider
	entered chan struct{}
	release chan struct{}
	stopped chan struct{}
}

func (p *delayedWatchRegistration) ListenForEvents(ctx context.Context, sink resource.WatchEventSink, onReady func() error) error {
	defer close(p.stopped)
	close(p.entered)
	select {
	case <-p.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return p.Provider.ListenForEvents(ctx, sink, onReady)
}

func TestWatchStreamReadyBeforeInitialNativeEvent(t *testing.T) {
	watchStopped := make(chan struct{})
	provider, err := resource.BuildResourceController(context.Background(), resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.WatchableResourcer[string]{
			WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, sink resource.WatchEventSink) error {
				defer close(watchStopped)
				sink.OnAdd(resource.WatchAddPayload{Key: meta.Key(), ID: "first", Data: json.RawMessage(`{"value":"native-first"}`)})
				<-ctx.Done()
				return nil
			},
		}}},
	})
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	gated := &delayedWatchRegistration{Provider: provider, entered: make(chan struct{}), release: make(chan struct{}), stopped: make(chan struct{})}
	transport := setupBufconn(t, gated).(*client)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream, err := transport.stub.ListenForEvents(outgoingSession(ctx), &resourcepb.ListenRequest{})
	require.NoError(t, err)
	select {
	case <-gated.entered:
	case <-ctx.Done():
		t.Fatal("real event RPC did not reach delayed registration")
	}
	close(gated.release)
	// No source has started. Waiting for a first data message would deadlock:
	// readiness must acknowledge the actual listener registration separately.
	headers, err := stream.Header()
	require.NoError(t, err, "registered event stream must acknowledge readiness before initial events")
	require.Equal(t, []string{"1"}, headers.Get("omniview-watch-ready"))
	_, err = transport.LoadConnections(context.Background())
	require.NoError(t, err)
	_, err = transport.StartConnection(context.Background(), "conn-1")
	require.NoError(t, err)
	for {
		event, recvErr := stream.Recv()
		require.NoError(t, recvErr)
		if added := event.GetAdd(); added != nil {
			require.Equal(t, "first", added.GetId())
			require.JSONEq(t, `{"value":"native-first"}`, string(added.GetData()))
			require.Equal(t, "conn-1", event.GetConnectionId())
			break
		}
	}
	cancel()
	select {
	case <-gated.stopped:
	case <-time.After(time.Second):
		t.Fatal("cancelled RPC did not remove its real controller listener")
	}
	_, err = transport.StopConnection(context.Background(), "conn-1")
	require.NoError(t, err)
	select {
	case <-watchStopped:
	case <-time.After(time.Second):
		t.Fatal("native watch did not stop with its connection")
	}
}

func TestWatchStreamFailureBeforeRegistration(t *testing.T) {
	provider := resourcetest.NewTestProvider(t,
		resourcetest.WithListenForEventsFunc(func(context.Context, resource.WatchEventSink, func() error) error {
			return &resource.ResourceOperationError{Code: "FORBIDDEN", Message: "native event access denied"}
		}),
	)
	transport := setupBufconn(t, provider)
	err := transport.ListenForEvents(context.Background(), resourcetest.NewRecordingSink(), func() error {
		t.Error("failed registration must not acknowledge readiness")
		return nil
	})
	var denied *resource.ResourceOperationError
	require.ErrorAs(t, err, &denied)
	require.Equal(t, "FORBIDDEN", denied.Code)
}

func TestWatchStreamCancelBeforeRegistration(t *testing.T) {
	provider, err := resource.BuildResourceController(context.Background(), resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}},
		},
	})
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	gated := &delayedWatchRegistration{Provider: provider, entered: make(chan struct{}), release: make(chan struct{}), stopped: make(chan struct{})}
	transport := setupBufconn(t, gated)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- transport.ListenForEvents(ctx, resourcetest.NewRecordingSink(), func() error {
			t.Error("cancelled registration must not acknowledge readiness")
			return nil
		})
	}()
	select {
	case <-gated.entered:
	case <-time.After(time.Second):
		t.Fatal("actual RPC did not reach registration gate")
	}
	cancel()
	select {
	case err := <-done:
		require.Equal(t, codes.Canceled, status.Code(err))
	case <-time.After(time.Second):
		t.Fatal("cancelled client listener did not return")
	}
	select {
	case <-gated.stopped:
	case <-time.After(time.Second):
		t.Fatal("cancelled server registration did not return")
	}
}

func TestWatchStreamReadinessCallbackFailureRemovesListener(t *testing.T) {
	provider, err := resource.BuildResourceController(context.Background(), resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{
			{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}},
		},
	})
	require.NoError(t, err)
	t.Cleanup(provider.Close)
	gated := &delayedWatchRegistration{Provider: provider, entered: make(chan struct{}), release: make(chan struct{}), stopped: make(chan struct{})}
	close(gated.release)
	transport := setupBufconn(t, gated)
	rejected := errors.New("native readiness consumer rejected registration")
	err = transport.ListenForEvents(context.Background(), resourcetest.NewRecordingSink(), func() error { return rejected })
	require.ErrorIs(t, err, rejected)
	select {
	case <-gated.stopped:
	case <-time.After(time.Second):
		t.Fatal("rejected registration retained its real controller listener")
	}
}
