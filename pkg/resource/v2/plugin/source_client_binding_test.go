package plugin

import (
	"context"
	"google.golang.org/grpc"
	"testing"

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
