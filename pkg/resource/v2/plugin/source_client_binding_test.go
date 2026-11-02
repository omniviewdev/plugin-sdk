package plugin

import (
	"context"
	"testing"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

func TestSourceClientRejectsUnboundPeerRecords(t *testing.T) {
	for _, mutation := range []string{"revision", "schema", "identity"} {
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
				case "schema":
					record.Representation.Schema.State = "unavailable"
				case "identity":
					record.Reference.Address.Scope = []resourcev2.ScopeEntry{{Dimension: "undeclared", Value: "value"}}
				}
				if err := resourcev2.ValidateRecord(record); err != nil {
					t.Fatalf("regression requires a structurally valid record: %v", err)
				}
				frames := []resourcev2.SourceFrame{{Begin: &resourcev2.SourceBegin{Mode: request.Mode}}}
				observation := resourcev2.SourceObservation{Record: record}
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
				if _, err = stream.Recv(); resourcev2.ErrorCode(err) != "PROVIDER_ERROR" {
					t.Fatalf("hostile unbound record: %v", err)
				}
			})
		}
	}
}
