package resourceconformance

import (
	"context"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	resource "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
)

type profileChange struct {
	revision uint64
	change   resource.SourceChange
}
type profileStore struct {
	mu          sync.Mutex
	rev         uint64
	objects     map[string]resource.Record
	log         []profileChange
	subscribers []chan profileChange
}

func (s *profileStore) snapshotAt() ([]resource.Record, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]resource.Record, 0, len(s.objects))
	for _, r := range s.objects {
		out = append(out, r)
	}
	return out, s.rev
}
func (s *profileStore) follow(from uint64) <-chan profileChange {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan profileChange, 32)
	for _, e := range s.log {
		if e.revision > from {
			ch <- e
		}
	}
	s.subscribers = append(s.subscribers, ch)
	return ch
}
func (s *profileStore) write(r resource.Record, remove bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rev++
	rev := strconv.FormatUint(s.rev, 10)
	r.SourceRevision = &rev
	var change resource.SourceChange
	if remove {
		delete(s.objects, r.Reference.Address.ID)
		ref := r.Reference
		change = resource.SourceChange{Kind: resource.SourceChangeRemove, Reference: &ref, SourceRevision: &rev}
	} else {
		s.objects[r.Reference.Address.ID] = r
		obs := resource.SourceObservation{Record: r}
		change = resource.SourceChange{Kind: resource.SourceChangeUpsert, Observation: &obs}
	}
	e := profileChange{revision: s.rev, change: change}
	s.log = append(s.log, e)
	for _, ch := range s.subscribers {
		ch <- e
	}
}

func TestSourceRaceProfiles(t *testing.T) {
	for _, profile := range []string{"cursor", "weak"} {
		t.Run(profile, func(t *testing.T) {
			corpus := NativeCorpus()
			s := corpus.Scenarios[0]
			initial := nativeRecord(corpus.Record(s.Pages[0].Records[0]), s)
			initial.SourceRevision = token("1")
			initial.Reference.Incarnation = token("uid-1")
			stable := initial
			stable.Reference.Address.ID = "fixture-stable"
			stable.Reference.Incarnation = token("uid-stable")
			store := &profileStore{rev: 1, objects: map[string]resource.Record{initial.Reference.Address.ID: initial, stable.Reference.Address.ID: stable}}
			// Fixture scheduling barriers, not a product ACK.
			snapshotStarted := make(chan struct{})
			writerDone := make(chan struct{})
			writerExit := make(chan struct{})
			other := initial
			other.Reference.Address.ID = "fixture-other"
			other.Reference.Incarnation = token("uid-other")
			go func() {
				defer close(writerExit)
				<-snapshotStarted
				store.write(other, false)
				older := initial
				older.Body = resource.Value{Kind: "map", Map: map[string]resource.Value{"state": {Kind: "string", String: "older"}}}
				store.write(older, false)
				store.write(older, true)
				newer := initial
				newer.Reference.Incarnation = token("uid-2")
				newer.Body = resource.Value{Kind: "map", Map: map[string]resource.Value{"state": {Kind: "string", String: "newer"}}}
				store.write(newer, false)
				close(writerDone)
			}()
			continuity := resource.ContinuityGapFree
			if profile == "weak" {
				continuity = resource.ContinuityBestEffort
			}
			cfg := resource.Config[struct{}]{Catalog: func(context.Context, resource.CatalogRequest) (resource.Catalog, error) {
				return resource.Catalog{Provider: initial.Reference.Address.Provider, Revision: "race-catalog", Coverage: resource.Coverage{Kind: "complete", Paths: []string{}}, Types: []resource.ResourceType{{Type: s.Type, IdentityDimensions: []resource.Dimension{{Name: scopeAccount}, {Name: scopeRegion}}, QueryDimensions: []resource.Dimension{{Name: scopeAccount}, {Name: scopeRegion}}, Representations: []resource.Representation{representation(s)}, Capabilities: []resource.Capability{{Operation: resource.OperationSourceUpdates, Scope: queryScope(s), Guarantee: continuity}}}}}, nil
			}}
			cfg.Registrations = []resource.Registration[struct{}]{{Type: s.Type, Source: func(ctx context.Context, _ *struct{}, _ resource.SourceRequest, out resource.SourceEmitter) error {
				var records []resource.Record
				var events <-chan profileChange
				if profile == "cursor" {
					var rev uint64
					records, rev = store.snapshotAt()
					events = store.follow(rev)
				} else {
					events = store.follow(0)
				}
				if err := out.Snapshot(ctx, []resource.SourceObservation{{Record: stable}}); err != nil {
					return err
				}
				close(snapshotStarted)
				select {
				case <-writerDone:
				case <-ctx.Done():
					return ctx.Err()
				}
				if profile == "weak" {
					records, _ = store.snapshotAt()
				}
				observations := make([]resource.SourceObservation, 0, len(records))
				for _, r := range records {
					if r.Reference.Address.ID != stable.Reference.Address.ID {
						observations = append(observations, resource.SourceObservation{Record: r})
					}
				}
				if err := out.Snapshot(ctx, observations); err != nil {
					return err
				}
				if err := out.EndSnapshot(ctx, resource.SourceCoverage{Enumeration: resource.EnumerationExhausted, Consistency: resource.ConsistencyBestEffort, Limitations: []string{}}, continuity); err != nil {
					return err
				}
				for range 4 {
					select {
					case e := <-events:
						if err := out.Changes(ctx, []resource.SourceChange{e.change}, nil); err != nil {
							return err
						}
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				<-ctx.Done()
				return ctx.Err()
			}}}
			controller, err := resource.NewController(cfg)
			require.NoError(t, err)
			defer controller.Close()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			server := grpc.NewServer()
			require.NoError(t, (&resourceplugin.GRPCPlugin{Impl: controller}).GRPCServer(nil, server))
			go func() { _ = server.Serve(listener) }()
			defer server.Stop()
			conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
			require.NoError(t, err)
			defer conn.Close()
			client := resourceplugin.NewClient(conn)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			catalog, err := client.Catalog(ctx, resource.CatalogRequest{Route: resource.AccessRoute{Connection: s.Name}})
			require.NoError(t, err)
			ctx = resource.WithCatalogRevision(ctx, catalog.Revision)
			stream, err := client.OpenSourceStream(ctx, sourceRequest(s, corpus, resource.SourceModeSnapshotAndUpdates))
			require.NoError(t, err)
			defer stream.Close()
			state := map[string]resource.Record{}
			last := map[string]uint64{}
			var seq uint64
			changes := 0
			stale := false
			apply := func(r resource.Record) {
				id := r.Reference.Address.ID
				revision, e := strconv.ParseUint(*r.SourceRevision, 10, 64)
				require.NoError(t, e)
				if previous := last[id]; previous > revision {
					stale = true
					require.Equal(t, "older", r.Body.Map["state"].String)
				}
				last[id] = revision
				state[id] = r
			}
			for changes < 4 {
				f, e := stream.Recv()
				require.NoError(t, e)
				seq++
				require.Equal(t, seq, f.Sequence)
				require.Nil(t, f.Failed)
				if f.Snapshot != nil {
					for _, o := range f.Snapshot.Observations {
						apply(o.Record)
					}
				}
				if f.SnapshotEnd != nil {
					require.Equal(t, continuity, f.SnapshotEnd.Continuity)
				}
				if f.Changes != nil {
					for _, ch := range f.Changes.Changes {
						changes++
						if ch.Kind == resource.SourceChangeUpsert {
							apply(ch.Observation.Record)
						} else if old, ok := state[ch.Reference.Address.ID]; ok && old.Reference.Incarnation != nil && ch.Reference.Incarnation != nil && *old.Reference.Incarnation == *ch.Reference.Incarnation {
							delete(state, ch.Reference.Address.ID)
						}
					}
				}
			}
			<-writerExit
			if profile == "cursor" {
				final, _ := store.snapshotAt()
				require.Len(t, final, len(state))
				for _, r := range final {
					require.Equal(t, r, state[r.Reference.Address.ID])
				}
				require.False(t, stale)
				t.Logf("cursor continuity=%s per-object revisions monotonic; final state matches native", continuity)
			} else {
				require.True(t, stale)
				t.Logf("weak continuity=%s stale overwrite observed with contiguous sequence=%d", continuity, seq)
			}
		})
	}
}
