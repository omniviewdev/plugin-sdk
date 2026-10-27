package resourceconformance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
	"github.com/stretchr/testify/require"
)

// Event release is a test-side file barrier outside the plugin API. Receiving
// the buffered Synced state proves the real event listener is registered before
// the parent releases the one finite native change. No startup sleep/replay or
// fixture-specific RPC is used. This does not prove a gap-free source cutover.
type nativeEvents struct{ *nativeSource }

func (*nativeEvents) SyncPolicy() resource.SyncPolicy { return resource.SyncNever }

func (p *nativeEvents) Watch(ctx context.Context, s *Scenario, meta resource.ResourceMeta, sink resource.WatchEventSink) error {
	if err := p.admit(ctx, s, meta); err != nil {
		return err
	}
	if len(s.Updates) == 0 {
		return nativeError("UNSUPPORTED", "selected source has no update feed")
	}
	sink.OnStateChange(resource.WatchStateEvent{ResourceKey: p.typeID, State: resource.WatchStateSynced})
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(p.state, "release-native-events")); os.IsNotExist(err) {
				continue
			} else if err != nil {
				return err
			}
			for _, change := range s.Updates {
				r := p.corpus.Record(change.Record)
				// The engine scope remains in the body. UID and namespace stay
				// empty, honestly exposing this old event envelope's limitation.
				sink.OnUpdate(resource.WatchUpdatePayload{Key: p.typeID, ID: r.ID, Data: change.Body})
			}
			<-ctx.Done()
			return nil
		}
	}
}

type nativeEventSink struct {
	t       *testing.T
	ctx     context.Context
	updates chan resource.WatchUpdatePayload
	states  chan resource.WatchStateEvent
}

func (s *nativeEventSink) OnAdd(resource.WatchAddPayload) { s.t.Error("unexpected native add") }
func (s *nativeEventSink) OnDelete(resource.WatchDeletePayload) {
	s.t.Error("unexpected native delete")
}
func (s *nativeEventSink) OnUpdate(p resource.WatchUpdatePayload) {
	select {
	case s.updates <- p:
	case <-s.ctx.Done():
	}
}
func (s *nativeEventSink) OnStateChange(p resource.WatchStateEvent) {
	select {
	case s.states <- p:
	case <-s.ctx.Done():
	}
}

func probeNativeEvents(t *testing.T, provider resource.Provider, session context.Context, receipts string, s Scenario, c Corpus) {
	t.Helper()
	ctx, cancel := context.WithCancel(session)
	defer cancel()
	sink := &nativeEventSink{t: t, ctx: ctx, updates: make(chan resource.WatchUpdatePayload, 4), states: make(chan resource.WatchStateEvent, 16)}
	done := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		done <- provider.ListenForEvents(ctx, sink, func() error {
			close(ready)
			return nil
		})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("native event listener returned before readiness: %v", err)
	case <-session.Done():
		t.Fatal("native event listener did not register")
	}
	// StartConnection already initialized the real watch manager. The current
	// remote client has no StartConnectionWatch RPC; use its supported per-type control.
	require.NoError(t, provider.EnsureResourceWatch(session, s.Name, s.Type))
	select {
	case state := <-sink.states:
		require.Equal(t, resource.WatchStateSynced, state.State)
		require.Equal(t, s.Type, state.ResourceKey)
	case <-session.Done():
		t.Fatal("native event listener did not register")
	}
	require.NoError(t, os.WriteFile(filepath.Join(receipts, "release-native-events"), []byte("release"), 0600))
	var changes []Change
	for _, want := range s.Updates {
		select {
		case update := <-sink.updates:
			r := c.Record(want.Record)
			require.Equal(t, r.ID, update.ID)
			require.Equal(t, s.Type, update.Key)
			require.Equal(t, s.Name, update.Connection)
			require.Empty(t, update.Namespace)
			require.Empty(t, update.Metadata.UID)
			require.True(t, equalJSON(update.Data, want.Body), "native event body/revision changed")
			var native struct {
				Revision string `json:"revision"`
			}
			require.NoError(t, json.Unmarshal(update.Data, &native))
			changes = append(changes, Change{Record: want.Record, Revision: native.Revision, Body: update.Data})
		case <-session.Done():
			t.Fatal("native update did not traverse plugin stream")
		}
	}
	require.NoError(t, CheckChanges(s, changes))
	t.Log("LIMIT update body retains engine/revision/incarnation; old event identity envelope has no native exact scope or gap-free/freshness guarantee")
	require.NoError(t, provider.StopResourceWatch(session, s.Name, s.Type))
	cancel()
	select {
	case <-done:
	case <-session.Done():
		t.Fatal("event stream cancellation did not finish")
	}
}
