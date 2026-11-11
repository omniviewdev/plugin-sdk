package plugin

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

func TestSourceClientRejectsUnboundPeerRecords(t *testing.T) {
	for _, mutation := range []string{"revision", "identity"} {
		for _, phase := range []string{"snapshot", "upsert", "remove"} {
			if phase == "remove" && mutation != "identity" {
				continue
			}
			t.Run(mutation+"/"+phase, func(t *testing.T) {
				request := transportSourceRequest()
				request.Mode = resourcev2.SourceModeSnapshotAndUpdates
				record, err := (&qualifiedProvider{}).Get(context.Background(), testGet())
				if err != nil {
					t.Fatal(err)
				}
				switch mutation {
				case "revision":
					record.Representation.Revision = "unaccepted-revision"
				case "identity":
					record.Reference.Address.Scope = []resourcev2.ScopeEntry{{Dimension: "undeclared", Value: "value"}}
				}
				if err := resourcev2.ValidateRecord(record); err != nil {
					t.Fatalf("regression requires a structurally valid record: %v", err)
				}
				frames := []resourcev2.SourceFrame{{Begin: &resourcev2.SourceBegin{Mode: request.Mode}}}
				observation := resourcev2.SourceObservation{Record: resourcev2.CompactSourceRecord(record)}
				if phase == "snapshot" {
					frames = append(frames, resourcev2.SourceFrame{Snapshot: &resourcev2.SourceSnapshot{Observations: []resourcev2.SourceObservation{observation}}})
				} else {
					frames = append(frames, resourcev2.SourceFrame{SnapshotEnd: &resourcev2.SourceSnapshotEnd{Coverage: resourcev2.SourceCoverage{Enumeration: resourcev2.EnumerationExhausted, Consistency: resourcev2.ConsistencyBestEffort}, Continuity: resourcev2.ContinuityBestEffort}})
					change := resourcev2.SourceChange{Kind: resourcev2.SourceChangeUpsert, Observation: &observation}
					if phase == "remove" {
						change = resourcev2.SourceChange{Kind: resourcev2.SourceChangeRemove, Reference: &record.Reference}
					}
					frames = append(frames, resourcev2.SourceFrame{Changes: &resourcev2.SourceChanges{Changes: []resourcev2.SourceChange{change}}})
				}
				wire := make([]*resourcepb.SourceFrame, len(frames))
				for i := range frames {
					frames[i].SessionID, frames[i].Generation, frames[i].Sequence = request.SessionID, request.Generation, uint64(i+1)
					wire[i], err = resourcev2.ToProtoSourceFrame(frames[i])
					if err != nil {
						t.Fatal(err)
					}
				}
				client := hostileTCP(t, wire, false)
				stream, err := client.OpenSourceStream(acceptedCatalogContext(context.Background(), t, client, request.Target.Route), request)
				if err != nil {
					t.Fatal(err)
				}
				defer stream.Close()
				for range len(frames) - 1 {
					if _, err = stream.Recv(); err != nil {
						t.Fatal(err)
					}
				}
				want := "RESET_REQUIRED"
				if mutation == "identity" {
					want = "PROVIDER_ERROR"
				}
				if _, err = stream.Recv(); resourcev2.ErrorCode(err) != want {
					t.Fatalf("hostile unbound record: %v", err)
				}
			})
		}
	}
}

type unboundSourceProvider struct{ transportSourceProvider }

func (p *unboundSourceProvider) Catalog(ctx context.Context, request resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
	catalog, err := p.transportSourceProvider.Catalog(ctx, request)
	catalog.Revision = "unbound-native-generation"
	return catalog, err
}

func TestSourceRejectsUnboundNativeGenerationBeforeDispatch(t *testing.T) {
	called := make(chan struct{}, 1)
	provider := &unboundSourceProvider{transportSourceProvider: transportSourceProvider{
		callback: func(context.Context, resourcev2.SourceEmitter) error {
			called <- struct{}{}
			return nil
		},
	}}
	server := grpc.NewServer()
	RegisterServer(server, provider)
	client := NewClient(pipe(t, server))
	request := transportSourceRequest()
	ctx := acceptedCatalogContext(context.Background(), t, client, request.Target.Route)
	if _, err := client.OpenSourceStream(ctx, request); resourcev2.ErrorCode(err) != "RESET_REQUIRED" {
		t.Fatalf("unbound native generation admitted: %v", err)
	}
	select {
	case <-called:
		t.Fatal("unbound source reached native dispatch")
	default:
	}
}

type sourceRecheckProvider struct {
	transportSourceProvider
	checking atomic.Bool
	checks   atomic.Int32
	change   string
	stall    bool
	entered  chan context.Context
	exited   chan error
}

func (p *sourceRecheckProvider) Catalog(ctx context.Context, request resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
	catalog, err := p.transportSourceProvider.Catalog(ctx, request)
	if err != nil || !p.checking.Load() {
		return catalog, err
	}
	p.checks.Add(1)
	if p.entered != nil {
		p.entered <- ctx
	}
	if p.stall {
		<-ctx.Done()
		p.exited <- ctx.Err()
		return resourcev2.Catalog{}, ctx.Err()
	}
	switch p.change {
	case "revision":
		epoch, err := resourcev2.CatalogBindingEpoch(catalog.Revision)
		if err != nil {
			return resourcev2.Catalog{}, err
		}
		catalog.Revision, err = resourcev2.BindCatalogRevision(epoch, "cat-2")
		return catalog, err
	case "generation":
		epoch, err := resourcev2.NewCatalogBindingEpoch()
		if err != nil {
			return resourcev2.Catalog{}, err
		}
		catalog.Revision, err = resourcev2.BindCatalogRevision(epoch, "cat-1")
		return catalog, err
	case "representation":
		catalog.Types[0].Representations[0].Revision = "rep-2"
	}
	return catalog, nil
}

func TestSourceClientCatalogRecheck(t *testing.T) {
	for _, pin := range []string{"default", "qualified"} {
		for _, phase := range []string{"snapshot", "snapshot_end", "changes"} {
			for _, change := range []string{"unchanged", "revision", "generation", "representation"} {
				if change != "unchanged" && (pin != "default" || phase != "snapshot_end") {
					continue
				}
				t.Run(pin+"/"+phase+"/"+change, func(t *testing.T) {
					request := transportSourceRequest()
					request.Mode = resourcev2.SourceModeSnapshotAndUpdates
					record, err := (&qualifiedProvider{}).Get(context.Background(), testGet())
					if err != nil {
						t.Fatal(err)
					}
					observation := resourcev2.SourceObservation{Record: resourcev2.CompactSourceRecord(record)}
					release := make(chan struct{})
					providerDone := make(chan struct{})
					provider := &sourceRecheckProvider{change: change}
					provider.callback = func(ctx context.Context, emitter resourcev2.SourceEmitter) error {
						defer close(providerDone)
						// Exercise the client fence independently of the server's periodic fence.
						emitter.(*sourceEmitter).nextCatalogCheck = time.Now().Add(time.Hour)
						if err := emitter.Snapshot(ctx, nil); err != nil {
							return err
						}
						coverage := resourcev2.SourceCoverage{Enumeration: resourcev2.EnumerationExhausted, Consistency: resourcev2.ConsistencyBestEffort}
						if phase == "changes" {
							if err := emitter.EndSnapshot(ctx, coverage, resourcev2.ContinuityBestEffort); err != nil {
								return err
							}
						}
						select {
						case <-release:
						case <-ctx.Done():
							return ctx.Err()
						}
						var err error
						switch phase {
						case "snapshot":
							err = emitter.Snapshot(ctx, []resourcev2.SourceObservation{observation})
						case "snapshot_end":
							err = emitter.EndSnapshot(ctx, coverage, resourcev2.ContinuityBestEffort)
						case "changes":
							err = emitter.Changes(ctx, []resourcev2.SourceChange{{Kind: resourcev2.SourceChangeUpsert, Observation: &observation}}, nil)
						}
						if err != nil {
							return err
						}
						<-ctx.Done()
						return ctx.Err()
					}
					server := grpc.NewServer()
					RegisterServer(server, provider)
					client := NewClient(pipe(t, server))
					base, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					var ctx context.Context
					if pin == "qualified" {
						ctx = acceptedQualifiedCatalogContext(base, t, client, request.Target.Provider, request.Target.Route)
					} else {
						ctx = acceptedCatalogContext(base, t, client, request.Target.Route)
					}
					stream, err := client.OpenSourceStream(ctx, request)
					if err != nil {
						t.Fatal(err)
					}
					defer stream.Close()
					if frame, err := stream.Recv(); err != nil || frame.Begin == nil {
						t.Fatalf("begin: %#v %v", frame, err)
					}
					if phase == "changes" {
						if frame, err := stream.Recv(); err != nil || frame.SnapshotEnd == nil {
							t.Fatalf("snapshot boundary: %#v %v", frame, err)
						}
					}
					stream.(*sourceClientStream).nextCatalogCheck = time.Time{}
					provider.checking.Store(true)
					close(release)
					frame, err := stream.Recv()
					if change == "generation" || change == "representation" {
						if resourcev2.ErrorCode(err) != "RESET_REQUIRED" {
							t.Fatalf("changed binding admitted: %#v %v", frame, err)
						}
					} else {
						if err != nil {
							t.Fatalf("compatible catalog rejected: %v", err)
						}
						switch phase {
						case "snapshot":
							if frame.Snapshot == nil || len(frame.Snapshot.Observations) != 1 {
								t.Fatalf("snapshot not delivered: %#v", frame)
							}
						case "snapshot_end":
							if frame.SnapshotEnd == nil {
								t.Fatalf("snapshot boundary not delivered: %#v", frame)
							}
						case "changes":
							if frame.Changes == nil || len(frame.Changes.Changes) != 1 {
								t.Fatalf("change not delivered: %#v", frame)
							}
						}
					}
					if checks := provider.checks.Load(); checks != 1 {
						t.Fatalf("catalog recheck count: %d", checks)
					}
					stream.Close()
					select {
					case <-providerDone:
					case <-time.After(time.Second):
						t.Fatal("provider did not cancel after recheck")
					}
				})
			}
		}
	}
}

func TestSourceClientCatalogRecheckBound(t *testing.T) {
	for _, stop := range []string{"timeout", "context", "close"} {
		t.Run(stop, func(t *testing.T) {
			provider := &sourceRecheckProvider{stall: true, entered: make(chan context.Context, 1), exited: make(chan error, 1)}
			server := grpc.NewServer()
			RegisterServer(server, provider)
			client := NewClient(pipe(t, server))
			request := transportSourceRequest()
			request.Mode = resourcev2.SourceModeSnapshotAndUpdates
			catalog, err := client.Catalog(context.Background(), resourcev2.CatalogRequest{Route: request.Target.Route})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(resourcev2.WithCatalogRevision(context.Background(), catalog.Revision))
			defer cancel()
			tracker := resourcev2.NewCatalogBoundSourcePhaseTracker(request, catalog.Types[0])
			for _, frame := range []resourcev2.SourceFrame{
				{SessionID: request.SessionID, Generation: request.Generation, Sequence: 1, Begin: &resourcev2.SourceBegin{Mode: request.Mode}},
				{SessionID: request.SessionID, Generation: request.Generation, Sequence: 2, SnapshotEnd: &resourcev2.SourceSnapshotEnd{Coverage: resourcev2.SourceCoverage{Enumeration: resourcev2.EnumerationExhausted, Consistency: resourcev2.ConsistencyBestEffort}, Continuity: resourcev2.ContinuityBestEffort}},
			} {
				if err := tracker.Admit(frame); err != nil {
					t.Fatal(err)
				}
			}
			record, err := (&qualifiedProvider{}).Get(context.Background(), testGet())
			if err != nil {
				t.Fatal(err)
			}
			observation := resourcev2.SourceObservation{Record: resourcev2.CompactSourceRecord(record)}
			frame, err := resourcev2.ToProtoSourceFrame(resourcev2.SourceFrame{
				SessionID: request.SessionID, Generation: request.Generation, Sequence: 3,
				Changes: &resourcev2.SourceChanges{Changes: []resourcev2.SourceChange{{Kind: resourcev2.SourceChangeUpsert, Observation: &observation}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			// Start in following mode, with no snapshot-progress or parent deadline.
			stream := &sourceClientStream{
				stream: &hostileSourceStream{frames: []*resourcepb.SourceFrame{frame}},
				cancel: cancel, ctx: ctx, maxBatchItems: request.Limits.MaxBatchItems,
				tracker: tracker, client: client, request: request, descriptor: catalog.Types[0],
			}
			defer stream.Close()
			provider.checking.Store(true)
			received := make(chan error, 1)
			started := time.Now()
			go func() { _, err := stream.Recv(); received <- err }()
			var checkCtx context.Context
			select {
			case checkCtx = <-provider.entered:
			case <-time.After(time.Second):
				t.Fatal("client catalog recheck did not start")
			}
			deadline, bounded := checkCtx.Deadline()
			if !bounded || time.Until(deadline) > 5*time.Second+100*time.Millisecond {
				t.Error("client catalog recheck lacks a five-second bound")
				cancel()
			}
			want := "CANCELLED"
			switch stop {
			case "timeout":
				want = "TIMEOUT"
			case "context":
				cancel()
			case "close":
				stream.Close()
			}
			select {
			case err := <-received:
				if resourcev2.ErrorCode(err) != want {
					t.Errorf("recheck %s: %v", stop, err)
				}
				t.Logf("client recheck %s returned after %s", stop, time.Since(started))
			case <-time.After(6 * time.Second):
				cancel()
				t.Fatal("client catalog recheck did not unblock")
			}
			select {
			case err := <-provider.exited:
				if err == nil {
					t.Fatal("stalled client catalog did not observe cancellation")
				}
			case <-time.After(time.Second):
				t.Fatal("stalled client catalog callback did not exit")
			}
			if stream.ctx.Err() == nil {
				t.Fatal("failed client recheck left source stream open")
			}
		})
	}
}
