package resourceconformance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	resource "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
)

type nativeEvent struct {
	state  *resource.WatchStateEvent
	update *resource.WatchUpdatePayload
}
type nativeWatch struct {
	cancel context.CancelFunc
	done   chan struct{}
	target resource.ReadTarget
}

// The fixture implements only the actual advisory contract. File release is a
// test-side barrier, not a new RPC or a snapshot/update continuity mechanism.
type nativeEventProvider struct {
	*nativeEnumerable

	eventMu   sync.Mutex
	listening bool
	events    chan nativeEvent
	watches   map[string]nativeWatch
}

func newNativeEventProvider(p *nativeProvider) *nativeEventProvider {
	return &nativeEventProvider{
		nativeEnumerable: &nativeEnumerable{&nativeReader{p}},
		events:           make(chan nativeEvent, 16),
		watches:          map[string]nativeWatch{},
	}
}

func (p *nativeEventProvider) Catalog(ctx context.Context, in resource.CatalogRequest) (resource.Catalog, error) {
	c, err := p.nativeProvider.Catalog(ctx, in)
	if err != nil {
		return c, err
	}
	s, err := p.scenario(in.Route)
	if err != nil {
		return c, err
	}
	if len(s.Updates) > 0 {
		c.Types[0].Capabilities = append(
			c.Types[0].Capabilities,
			resource.Capability{
				Operation: resource.OperationUpdate,
				Scope:     queryScope(s),
				Guarantee: resource.BestEffortGuarantee,
			},
		)
	}
	return c, nil
}

func (p *nativeEventProvider) ListenForEvents(
	ctx context.Context,
	sink resource.WatchEventSink,
	onReady func() error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.eventMu.Lock()
	if p.listening {
		p.eventMu.Unlock()
		return nativeError("INVALID_REQUEST", "fixture allows one listener")
	}
	p.listening = true
	p.eventMu.Unlock()
	defer func() { p.eventMu.Lock(); p.listening = false; p.eventMu.Unlock() }()
	if onReady != nil {
		if err := onReady(); err != nil {
			return err
		}
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e := <-p.events:
			if err := ctx.Err(); err != nil {
				return err
			}
			if e.state != nil {
				sink.OnStateChange(*e.state)
			}
			if e.update != nil {
				sink.OnUpdate(*e.update)
			}
		}
	}
}

func (p *nativeEventProvider) EnsureResourceWatch(ctx context.Context, target resource.ReadTarget) error {
	if _, err := p.page(ctx, resource.EnumerateRequest{Target: target}); err != nil {
		return err
	}
	s, err := p.scenario(target.Route)
	if err != nil {
		return err
	}
	if len(s.Updates) == 0 {
		return nativeError("UNSUPPORTED", "source has no native updates")
	}
	p.eventMu.Lock()
	defer p.eventMu.Unlock()
	if !p.listening {
		return nativeError("INVALID_REQUEST", "listener must register before source")
	}
	if _, ok := p.watches[s.Name]; ok {
		return nil
	}
	watchCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	p.watches[s.Name] = nativeWatch{cancel: cancel, done: done, target: target}
	go p.runWatch(watchCtx, done, target, s)
	return nil
}

func (p *nativeEventProvider) runWatch(
	ctx context.Context,
	done chan struct{},
	target resource.ReadTarget,
	s Scenario,
) {
	defer close(done)
	state := resource.WatchStateEvent{
		Route:     target.Route,
		Type:      s.Type,
		State:     resource.WatchStateSynced,
		Count:     resource.Count{Kind: "unknown"},
		Guarantee: resource.BestEffortGuarantee,
	}
	select {
	case p.events <- nativeEvent{state: &state}:
	case <-ctx.Done():
		return
	}
	if !p.awaitEventRelease(ctx) {
		return
	}
	for _, change := range s.Updates {
		r := nativeRecord(p.corpus.Record(change.Record), s)
		r.Reference.Address.Authority = target.Authority
		r.Body = nativeValue(change.Body)
		r.SourceRevision = token(change.Revision)
		event := resource.WatchUpdatePayload{
			Route:     target.Route,
			Record:    r,
			Guarantee: resource.BestEffortGuarantee,
		}
		select {
		case p.events <- nativeEvent{update: &event}:
		case <-ctx.Done():
			return
		}
	}
	<-ctx.Done()
}

func (p *nativeEventProvider) awaitEventRelease(ctx context.Context) bool {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			_, err := os.Stat(filepath.Join(p.state, "release-native-events"))
			if os.IsNotExist(err) {
				continue
			}
			return err == nil
		}
	}
}

func (p *nativeEventProvider) StartConnectionWatch(ctx context.Context, targets []resource.ReadTarget) error {
	for _, target := range targets {
		if err := p.EnsureResourceWatch(ctx, target); err != nil {
			return err
		}
	}
	return nil
}

func (p *nativeEventProvider) StopResourceWatch(_ context.Context, route resource.AccessRoute, _ string) error {
	p.eventMu.Lock()
	watch, ok := p.watches[route.Connection]
	delete(p.watches, route.Connection)
	p.eventMu.Unlock()
	if ok {
		watch.cancel()
		<-watch.done
	}
	return nil
}

func (p *nativeEventProvider) StopConnectionWatch(ctx context.Context, route resource.AccessRoute) error {
	return p.StopResourceWatch(ctx, route, "")
}

func (p *nativeEventProvider) HasWatch(_ context.Context, route resource.AccessRoute) bool {
	p.eventMu.Lock()
	defer p.eventMu.Unlock()
	_, ok := p.watches[route.Connection]
	return ok
}

func (p *nativeEventProvider) IsResourceWatchRunning(
	ctx context.Context,
	route resource.AccessRoute,
	_ string,
) (bool, error) {
	return p.HasWatch(ctx, route), nil
}

func (p *nativeEventProvider) RestartResourceWatch(ctx context.Context, target resource.ReadTarget) error {
	if err := p.StopResourceWatch(ctx, target.Route, target.Type); err != nil {
		return err
	}
	return p.EnsureResourceWatch(ctx, target)
}

func (p *nativeEventProvider) GetWatchState(
	_ context.Context,
	route resource.AccessRoute,
) (*resource.WatchConnectionSummary, error) {
	p.eventMu.Lock()
	watch, ok := p.watches[route.Connection]
	p.eventMu.Unlock()
	if !ok {
		return nil, nativeError("NOT_FOUND", "watch is not running")
	}
	return &resource.WatchConnectionSummary{
		Route:          route,
		Resources:      map[string]resource.WatchState{watch.target.Type: resource.WatchStateSynced},
		ResourceCounts: map[string]resource.Count{watch.target.Type: {Kind: "unknown"}},
		Scope:          watch.target.Scope,
		Guarantee:      resource.BestEffortGuarantee,
	}, nil
}

func (p *nativeEventProvider) Close() error {
	p.eventMu.Lock()
	watches := p.watches
	p.watches = map[string]nativeWatch{}
	p.eventMu.Unlock()
	for _, watch := range watches {
		watch.cancel()
		<-watch.done
	}
	return p.nativeProvider.Close()
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

func probeNativeEvents(
	session context.Context,
	t *testing.T,
	provider resource.Provider,
	receipts string,
	s Scenario,
	c Corpus,
) {
	t.Helper()
	updates, ok := provider.(resource.BestEffortUpdateProvider)
	require.True(t, ok)
	ctx, cancel := context.WithCancel(session)
	defer cancel()
	sink := &nativeEventSink{
		t:       t,
		ctx:     ctx,
		updates: make(chan resource.WatchUpdatePayload, 4),
		states:  make(chan resource.WatchStateEvent, 4),
	}
	done := make(chan error, 1)
	ready := make(chan struct{})
	go func() { done <- updates.ListenForEvents(ctx, sink, func() error { close(ready); return nil }) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("listener failed before readiness: %v", err)
	case <-session.Done():
		t.Fatal("listener did not register")
	}
	require.NoError(t, updates.EnsureResourceWatch(session, target(s, c)))
	select {
	case state := <-sink.states:
		require.Equal(t, resource.WatchStateSynced, state.State)
		require.Equal(t, resource.BestEffortGuarantee, state.Guarantee)
		require.Equal(t, resource.AccessRoute{Connection: s.Name}, state.Route)
		require.Equal(t, s.Type, state.Type)
		require.Equal(t, resource.Count{Kind: "unknown"}, state.Count)
	case <-session.Done():
		t.Fatal("native source did not become ready")
	}
	require.NoError(t, os.WriteFile(filepath.Join(receipts, "release-native-events"), []byte("release"), 0o600))
	changes := []Change{}
	for _, want := range s.Updates {
		select {
		case got := <-sink.updates:
			expected := nativeRecord(c.Record(want.Record), s)
			expected.Body = nativeValue(want.Body)
			expected.SourceRevision = token(want.Revision)
			require.Equal(t, resource.AccessRoute{Connection: s.Name}, got.Route)
			require.Equal(t, resource.BestEffortGuarantee, got.Guarantee)
			require.Equal(t, expected, got.Record)
			var body struct {
				Revision string `json:"revision"`
			}
			require.NoError(t, json.Unmarshal(nativeBodyJSON(got.Record.Body), &body))
			changes = append(
				changes,
				Change{Record: want.Record, Revision: body.Revision, Body: nativeBodyJSON(got.Record.Body)},
			)
		case <-session.Done():
			t.Fatal("native update did not traverse new-major gRPC")
		}
	}
	require.NoError(t, CheckChanges(s, changes))
	require.NoError(t, updates.StopResourceWatch(session, resource.AccessRoute{Connection: s.Name}, s.Type))
	running, err := updates.IsResourceWatchRunning(session, resource.AccessRoute{Connection: s.Name}, s.Type)
	require.NoError(t, err)
	require.False(t, running)
	cancel()
	select {
	case listenerErr := <-done:
		if listenerErr != nil && !errors.Is(listenerErr, context.Canceled) &&
			status.Code(listenerErr) != codes.Canceled {
			require.NoError(t, listenerErr)
		}
	case <-session.Done():
		t.Fatal("listener cancellation did not finish")
	}
}
