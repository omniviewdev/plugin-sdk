package activity

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	pb "github.com/omniviewdev/plugin-sdk/proto/v1/activity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type recordingReporter struct {
	Reporter
	received Spec
}

func (r *recordingReporter) BeginTask(_ context.Context, v Spec) (Record, error) {
	r.received = v
	return Record{
		ID:           "host-id",
		Source:       Source{PluginID: "authenticated-plugin"},
		Presentation: v.Presentation,
	}, nil
}

func TestReporterRoundTripAndRetirement(t *testing.T) {
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	reporter := &recordingReporter{}
	RegisterServer(server, reporter)
	go server.Serve(listener)
	defer server.Stop()
	conn, err := grpc.NewClient(
		"passthrough:///activity",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	binding := &Binding{}
	if err := binding.Bind(conn); err != nil {
		t.Fatal(err)
	}
	spec := Spec{Presentation: Presentation{
		Title: "Copy",
		Progress: &resourcev2.ActivityProgress{
			Kind:    "units",
			Current: "9007199254740993",
			Total:   "18446744073709551615",
			Unit:    "bytes",
		},
	}}
	record, err := binding.BeginTask(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if record.ID != "host-id" || record.Source.PluginID != "authenticated-plugin" || reporter.received.Presentation.Progress.Current != "9007199254740993" {
		t.Fatalf("lost host identity or precision: %#v", record)
	}
	if err := binding.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := binding.BeginTask(context.Background(), spec); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("closed binding admitted report: %v", err)
	}
	if err := binding.Bind(conn); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("retired binding accepted replacement: %v", err)
	}
}

func TestMalformedReportsNeverReachProducer(t *testing.T) {
	reporter := &recordingReporter{}
	server := &reporterServer{reporter: reporter}
	for _, request := range []*pb.ReportRequest{
		{Version: Version + 1, Operation: "begin-task", Payload: []byte(`{"presentation":{"title":"x"}}`)},
		{Version: Version, Operation: "begin-task", Payload: []byte(`{"presentation":{"title":"x"},"callback":"execute"}`)},
		{Version: Version, Operation: "begin-task", Payload: []byte(`{"presentation":{"title":"x","progress":{"kind":"steps","current":1,"total":2,"label":"copy"}}}`)},
		{Version: Version, Operation: "begin-task", Payload: []byte(`{"presentation":{"title":"x"}} {}`)},
	} {
		if _, err := server.Report(context.Background(), request); err == nil {
			t.Fatalf("admitted malformed report: %#v", request)
		}
	}
	if reporter.received.Presentation.Title != "" {
		t.Fatal("invalid report reached producer")
	}
}

type budgetReporter struct {
	Reporter
	record Record
}

func (r *budgetReporter) BeginTask(context.Context, Spec) (Record, error)      { return r.record, nil }
func (r *budgetReporter) PublishMessage(context.Context, Spec) (Record, error) { return r.record, nil }
func (r *budgetReporter) ReportTask(context.Context, ReportTaskRequest) (Record, error) {
	return r.record, nil
}
func (r *budgetReporter) FinishTask(context.Context, FinishTaskRequest) (Record, error) {
	return r.record, nil
}
func (r *budgetReporter) UpsertAlert(context.Context, AlertRequest) (Record, error) {
	return r.record, nil
}
func (r *budgetReporter) ResolveAlert(context.Context, ResolveAlertRequest) (Record, error) {
	return r.record, nil
}

func TestAllReporterResponsesReservePersistedMetadata(t *testing.T) {
	reporter := &budgetReporter{record: Record{
		ID:           "persisted-task",
		Source:       Source{PluginID: "authenticated-plugin", ConnectionID: strings.Repeat("s", maxRequestBytes-1024)},
		Presentation: Presentation{Title: "Copy", Details: strings.Repeat("d", 65536)},
		Task:         &Task{State: "running"},
	}}
	server := &reporterServer{reporter: reporter}
	for operation, request := range map[string]any{
		"begin-task":      Spec{Presentation: Presentation{Title: "Copy"}},
		"publish-message": Spec{Presentation: Presentation{Title: "Copy"}},
		"report-task":     ReportTaskRequest{ID: "persisted-task", Presentation: Presentation{Title: "Copy"}},
		"finish-task":     FinishTaskRequest{ID: "persisted-task", State: "succeeded"},
		"upsert-alert":    AlertRequest{Spec: Spec{Presentation: Presentation{Title: "Copy"}}, Key: "key", ObservedAt: "2026-11-03T00:00:00Z"},
		"resolve-alert":   ResolveAlertRequest{Key: "key", ObservedAt: "2026-11-03T00:00:00Z"},
	} {
		t.Run(operation, func(t *testing.T) {
			payload, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			response, err := server.Report(context.Background(), &pb.ReportRequest{Version: Version, Operation: operation, Payload: payload})
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Record) <= maxRequestBytes {
				t.Fatal("fixture did not exceed request budget")
			}
			var received Record
			if err := decodeLimit(response.Record, &received, maxResponseBytes); err != nil {
				t.Fatal(err)
			}
			if received.ID != reporter.record.ID || received.Source.ConnectionID != reporter.record.Source.ConnectionID {
				t.Fatal("lost persisted identity or source")
			}
		})
	}
}

func TestActivityFrameBudgets(t *testing.T) {
	for _, limit := range []int{maxRequestBytes, maxResponseBytes} {
		payload := append([]byte(`{}`), []byte(strings.Repeat(" ", limit-2))...)
		var decoded map[string]any
		if err := decodeLimit(payload, &decoded, limit); err != nil {
			t.Fatal(err)
		}
		if err := decodeLimit(append(payload, ' '), &decoded, limit); err == nil {
			t.Fatal("accepted oversized activity frame")
		}
	}
}
