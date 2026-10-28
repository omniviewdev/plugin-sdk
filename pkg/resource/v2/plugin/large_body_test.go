package plugin

import (
	"context"
	"fmt"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"strings"
	"testing"
	"time"
)

type largeProvider struct {
	qualifiedProvider
	payload string
}

func (p *largeProvider) Get(context.Context, resourcev2.GetRequest) (resourcev2.Record, error) {
	p.calls.Add(1)
	return resourcev2.Record{Reference: testGet().Reference, Representation: testRepresentation(), Coverage: resourcev2.Coverage{Kind: "complete", Paths: []string{}}, Body: resourcev2.Value{Kind: "map", Map: map[string]resourcev2.Value{
		"apiVersion": {Kind: "string", String: "example.omniview.dev/v1"}, "kind": {Kind: "string", String: "WorkloadConfiguration"},
		"metadata": {Kind: "map", Map: map[string]resourcev2.Value{"name": {Kind: "string", String: "large-native-resource"}, "namespace": {Kind: "string", String: "team-a"}, "labels": {Kind: "map", Map: map[string]resourcev2.Value{"app": {Kind: "string", String: "configuration"}}}}},
		"spec":     {Kind: "map", Map: map[string]resourcev2.Value{"replicas": {Kind: "int64", Int: "3"}, "config": {Kind: "map", Map: map[string]resourcev2.Value{"document": {Kind: "string", String: p.payload}}}}},
	}}}, nil
}
func TestActualGRPCMultiMiBResourceBodies(t *testing.T) {
	for _, size := range []int{2 << 20, 8 << 20} {
		t.Run(fmt.Sprintf("%dMiB", size>>20), func(t *testing.T) {
			p := &largeProvider{qualifiedProvider: qualifiedProvider{allowed: true}, payload: strings.Repeat("k", size)}
			s := grpc.NewServer(ServerOptions()...)
			RegisterServer(s, p)
			c := NewClient(pipe(t, s))
			started := time.Now()
			record, err := c.Get(acceptedCatalogContext(t, context.Background(), c, testGet().Route), testGet())
			elapsed := time.Since(started)
			if err != nil {
				t.Fatal(err)
			}
			got := record.Body.Map["spec"].Map["config"].Map["document"]
			if got.Kind != "string" || got.String != p.payload {
				t.Fatal("large native resource document changed")
			}
			wire, err := resourcev2.ToProtoRecord(record)
			if err != nil {
				t.Fatal(err)
			}
			size := proto.Size(&resourcepb.Response{Version: 2, Result: &resourcepb.Response_Record{Record: wire}})
			t.Logf("payload_bytes=%d encoded_response_bytes=%d elapsed=%s", len(p.payload), size, elapsed)
			if p.calls.Load() != 1 {
				t.Fatal("Get dispatch count mismatch")
			}
		})
	}
}
func TestActualGRPCOversizeBodyRejectedBeforeDispatch(t *testing.T) {
	stringValue := bytesField(3, []byte(strings.Repeat("x", MaxBodyBytes+1)))
	data := requestValue(stringValue)
	p := &countProvider{}
	s := grpc.NewServer(ServerOptions()...)
	RegisterServer(s, p)
	c := pipe(t, s)
	var out resourcepb.Response
	err := c.Invoke(context.Background(), resourcepb.ResourceProvider_Call_FullMethodName, &resourcepb.Request{}, &out, grpc.ForceCodecV2(rawCodec{data: data}), grpc.MaxCallSendMsgSize(MaxWireBytes))
	if resourcev2.ErrorCode(domainError(err)) != "RESOURCE_LIMIT" {
		t.Fatalf("oversize body class: %v", err)
	}
	if p.calls.Load() != 0 {
		t.Fatal("oversize body dispatched")
	}
}
func TestActualGRPCAggregatePageFrameLimit(t *testing.T) {
	// Each body stays below 16 MiB; three records exceed the aggregate 32 MiB frame.
	entry := append(bytesField(1, []byte("data")), bytesField(2, bytesField(3, []byte(strings.Repeat("p", 12<<20))))...)
	body := bytesField(9, bytesField(1, entry))
	if len(body) >= MaxBodyBytes {
		t.Fatal("fixture body exceeds per-document bound")
	}
	record := bytesField(2, body)
	page := []byte{}
	for range 3 {
		page = append(page, bytesField(1, record)...)
	}
	data := append([]byte{8, 2}, bytesField(4, page)...)
	if err := ValidateWire(data, (&resourcepb.Response{}).ProtoReflect().Descriptor()); resourcev2.ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("aggregate raw class: %v", err)
	}
	s := grpc.NewServer(grpc.ForceServerCodecV2(rawCodec{data: data}))
	RegisterServer(s, &countProvider{})
	c := NewClient(pipe(t, s))
	_, err := c.call(context.Background(), &resourcepb.Request{Operation: &resourcepb.Request_Catalog{Catalog: &resourcepb.AccessRoute{Connection: "c"}}})
	if testErrorClass(err) != "RESOURCE_LIMIT" {
		t.Fatalf("aggregate response class: %v", err)
	}
	t.Logf("body_bytes=%d aggregate_response_bytes=%d limit_bytes=%d", len(body), len(data), MaxWireBytes)
}
