package activity

import (
	"context"
	"errors"
	"net"
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
