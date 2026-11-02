package resourceconformance

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	resource "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

type sessionController struct {
	*resource.Controller[struct{}]
	base *nativeProvider
}

func (p *sessionController) LoadConnections(ctx context.Context) ([]types.Connection, error) {
	return p.base.LoadConnections(ctx)
}
func (p *sessionController) GetConnection(ctx context.Context, id string) (types.Connection, error) {
	return p.base.GetConnection(ctx, id)
}
func (p *sessionController) StartConnection(ctx context.Context, id string) (types.ConnectionStatus, error) {
	return p.base.StartConnection(ctx, id)
}
func (p *sessionController) StopConnection(ctx context.Context, id string) (types.Connection, error) {
	return p.base.StopConnection(ctx, id)
}
func (p *sessionController) CheckConnection(ctx context.Context, id string) (types.ConnectionStatus, error) {
	return p.base.CheckConnection(ctx, id)
}
func (p *sessionController) Close() error { _ = p.base.Close(); return p.Controller.Close() }

func newSessionController(p *nativeProvider) *sessionController {
	cfg := resource.Config[struct{}]{DynamicSources: true}
	cfg.Catalog = func(ctx context.Context, in resource.CatalogRequest) (resource.Catalog, error) {
		c, err := p.Catalog(ctx, in)
		if err != nil {
			return c, err
		}
		s, err := p.scenario(in.Route)
		if err != nil {
			return c, err
		}
		if len(s.Updates) > 0 {
			c.Types[0].Capabilities = append(c.Types[0].Capabilities, resource.Capability{Operation: resource.OperationSourceUpdates, Scope: queryScope(s), Guarantee: resource.ContinuityBestEffort})
		}
		return c, nil
	}
	cfg.ResolveRegistration = func(_ context.Context, _ resource.AccessRoute, typ string) (resource.Registration[struct{}], error) {
		return resource.Registration[struct{}]{Type: typ, Enumerate: func(ctx context.Context, _ *struct{}, in resource.EnumerateRequest) (resource.SourcePage, error) {
			return p.page(ctx, in)
		}, Source: func(ctx context.Context, _ *struct{}, in resource.SourceRequest, out resource.SourceEmitter) error {
			s, err := p.scenario(in.Target.Route)
			if err != nil {
				return err
			}
			var observations []resource.SourceObservation
			for _, page := range s.Pages {
				for _, name := range page.Records {
					r := nativeRecord(p.corpus.Record(name), s)
					observations = append(observations, resource.SourceObservation{Record: r, Metadata: &resource.ResourceMetadata{Labels: map[string]string{"fixture": "container"}}})
				}
			}
			if err = out.Snapshot(ctx, observations); err != nil {
				return err
			}
			if err = out.EndSnapshot(ctx, resource.SourceCoverage{Enumeration: resource.EnumerationExhausted, Consistency: resource.ConsistencyBestEffort, Limitations: []string{"native event stream has no snapshot handoff"}}, resource.ContinuityBestEffort); err != nil {
				return err
			}
			for _, update := range s.Updates {
				r := p.corpus.Record(update.Record)
				r.Body = update.Body
				r.Revision = update.Revision
				obs := resource.SourceObservation{Record: nativeRecord(r, s), Metadata: &resource.ResourceMetadata{Labels: map[string]string{"fixture": "container"}}}
				if err = out.Changes(ctx, []resource.SourceChange{{Kind: resource.SourceChangeUpsert, Observation: &obs}}, nil); err != nil {
					return err
				}
			}
			<-ctx.Done()
			return ctx.Err()
		}}, nil
	}
	c, err := resource.NewController(cfg)
	if err != nil {
		panic(err)
	}
	return &sessionController{Controller: c, base: p}
}

func sourceRequest(s Scenario, c Corpus, mode string) resource.SourceOpenRequest {
	return resource.SourceOpenRequest{SessionID: "session-1", Generation: "generation-1", Target: target(s, c), Mode: mode, Acquisition: resource.SourceAcquisition{Kind: resource.SourceAcquisitionEnumerate}, Limits: resource.SourceLimits{MaxFrameBytes: resource.MaxSourceFrameBytes, MaxBatchItems: resource.MaxSourceBatchItems}}
}

func TestCurrentSDKSourceSessions(t *testing.T) {
	c := NativeCorpus()
	for _, name := range []string{"empty", "cloud-bundled-pages", "cloud-partial-eventual", "hypervisor-bundled-vms", "container-bundled-events"} {
		t.Run(name, func(t *testing.T) {
			var s Scenario
			lookup := name
			if name == "empty" {
				lookup = "cloud-absent-schema"
			}
			for _, x := range c.Scenarios {
				if x.Name == lookup {
					s = x
					break
				}
			}
			profile := "source-sessions"
			if name == "empty" {
				profile = "source-sessions-empty"
			}
			p, _, stop := startNativeProfile(t, profile)
			defer stop()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err := p.(resource.ConnectionLifecycle).StartConnection(ctx, s.Name)
			require.NoError(t, err)
			catalog, err := p.Catalog(ctx, resource.CatalogRequest{Route: resource.AccessRoute{Connection: s.Name}})
			require.NoError(t, err)
			ctx = resource.WithCatalogRevision(ctx, catalog.Revision)
			mode := resource.SourceModeSnapshot
			if len(s.Updates) > 0 {
				mode = resource.SourceModeSnapshotAndUpdates
			}
			opener := p.(resource.SourceStreamOpener)
			stream, err := opener.OpenSourceStream(ctx, sourceRequest(s, c, mode))
			require.NoError(t, err)
			defer stream.Close()
			count := 0
			seq := uint64(0)
			ended := false
			snapshotFrames := 0
			sawInitial := false
			for {
				f, e := stream.Recv()
				if e == io.EOF {
					break
				}
				require.NoError(t, e)
				seq++
				require.Equal(t, seq, f.Sequence)
				require.Nil(t, f.Failed)
				if f.Snapshot != nil {
					snapshotFrames++
					for _, o := range f.Snapshot.Observations {
						count++
						if mode == resource.SourceModeSnapshot {
							require.Nil(t, o.Metadata)
						} else {
							require.Equal(t, "container", o.Metadata.Labels["fixture"])
							if o.Record.SourceRevision != nil && *o.Record.SourceRevision == "event:z9" {
								require.Equal(t, "running", o.Record.Body.Map["state"].String)
								sawInitial = true
							}
						}
					}
				}
				if f.SnapshotEnd != nil {
					ended = true
					if mode == resource.SourceModeSnapshot {
						require.Equal(t, resource.ContinuityNotApplicable, f.SnapshotEnd.Continuity)
						require.Equal(t, s.Coverage.Enumeration, f.SnapshotEnd.Coverage.Enumeration)
						if name == "cloud-partial-eventual" {
							require.Equal(t, []string{"selected listing truncated; eventual visibility"}, f.SnapshotEnd.Coverage.Limitations)
						}
					} else {
						require.Equal(t, resource.ContinuityBestEffort, f.SnapshotEnd.Continuity)
						require.Equal(t, []string{"native event stream has no snapshot handoff"}, f.SnapshotEnd.Coverage.Limitations)
					}
				}
				if f.Changes != nil {
					require.True(t, ended)
					require.True(t, sawInitial)
					got := f.Changes.Changes[0].Observation
					require.NoError(t, CheckChanges(s, []Change{{Record: recordContainerA, Revision: *got.Record.SourceRevision, Body: nativeBodyJSON(got.Record.Body)}}))
					require.Equal(t, "event:a1", *f.Changes.Changes[0].Observation.Record.SourceRevision)
					require.Equal(t, "paused", f.Changes.Changes[0].Observation.Record.Body.Map["state"].String)
					break
				}
			}
			require.True(t, ended)
			n := 0
			if name != "empty" {
				for _, page := range s.Pages {
					n += len(page.Records)
				}
			}
			if name == "cloud-bundled-pages" {
				n++
			}
			require.Equal(t, n, count)
			if name == "cloud-bundled-pages" {
				require.GreaterOrEqual(t, snapshotFrames, 3)
			}
			if s.Profile == providerHypervisor {
				stream2, e := opener.OpenSourceStream(ctx, sourceRequest(s, c, resource.SourceModeSnapshotAndUpdates))
				require.NoError(t, e)
				defer stream2.Close()
				f, e := stream2.Recv()
				require.NoError(t, e)
				require.NotNil(t, f.Failed)
				require.Equal(t, "UNSUPPORTED", f.Failed.Code)
			}
		})
	}
}
