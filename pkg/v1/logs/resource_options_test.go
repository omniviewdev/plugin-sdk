package logs

import (
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	logspb "github.com/omniviewdev/plugin-sdk/proto/v1/logs"
)

func TestLogsStrictResourceJSON(t *testing.T) {
	for _, input := range []string{
		`null`, `{"resource_key":"pod","resource_key":"other"}`, `{"ResourceKey":"pod"}`,
		`{"resource_data":{"kind":"int64","int":"01"}}`,
		`{"resource_data":{"kind":"uint64","uint":"18446744073709551616"}}`,
		`{"resource_data":{"kind":"map","map":{},"unknown":true}}`,
		`{"unknown":true}`, `{"resource_key":"pod"} {}`,
		`{"session_id":"invalid"}`, `{"session_id":null}`,
		`{"session_id":"00000000-0000-4000-8000-000000000000","session_id":"00000000-0000-4000-8000-000000000001"}`,
	} {
		var options CreateSessionOptions
		if err := json.Unmarshal([]byte(input), &options); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}

func TestLogsOptionWireNoStructFallback(t *testing.T) {
	retired := &logspb.CreateLogSessionRequest{}
	retired.ProtoReflect().SetUnknown(protowire.AppendBytes(protowire.AppendTag(nil, 3, protowire.BytesType), nil))
	for _, request := range []*logspb.CreateLogSessionRequest{retired, {ResourceKey: "pod"}, {ResourceWireVersion: 1}} {
		if _, err := CreateSessionOptionsFromProto(request); resourcev2.ErrorCode(err) != "UNSUPPORTED" {
			t.Fatalf("old wire admitted: %v", err)
		}
	}
}

func TestLogsLegacyManagerRejectsResourceIntent(t *testing.T) {
	manager := NewManager(ManagerConfig{Sink: nativeLogSink{}})
	defer manager.Close()
	for _, options := range []CreateSessionOptions{{SessionID: "00000000-0000-4000-8000-000000000000"}, {ResourceKey: "pod"}, {ResourceID: "opaque/id"}, {ResourceData: exactLogBody()}, {Reference: &resourcev2.ResourceReference{}}, {Route: &resourcev2.AccessRoute{}}} {
		if _, err := manager.CreateSession(nil, options); resourcev2.ErrorCode(err) != "UNSUPPORTED" {
			t.Fatalf("legacy accepted resource intent: %v", err)
		}
	}
}

func TestLogsResourceBodyLimitBeforeProtoAllocation(t *testing.T) {
	body := resourcev2.Value{Kind: "string", String: strings.Repeat("x", resourcev2.MaxMessageBytes)}
	if _, err := (CreateSessionOptions{ResourceData: &body}).ToProto(); resourcev2.ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("oversize body admitted: %v", err)
	}
}

func TestResourceSessionCorrelationRoundTrip(t *testing.T) {
	options := CreateSessionOptions{SessionID: "00000000-0000-4000-8000-000000000000"}
	request, err := options.ToProto()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := CreateSessionOptionsFromProto(request)
	if err != nil || decoded.SessionID != options.SessionID {
		t.Fatalf("session correlation lost: %v %v", decoded, err)
	}
	if _, err := (CreateSessionOptions{SessionID: "not-a-uuid"}).ToProto(); resourcev2.ErrorCode(
		err,
	) != "INVALID_REQUEST" {
		t.Fatalf("invalid correlation admitted: %v", err)
	}
}
