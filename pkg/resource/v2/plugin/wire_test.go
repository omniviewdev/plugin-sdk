package plugin

import (
	"context"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/mem"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type rawCodec struct{ data []byte }

func (r rawCodec) Name() string { return "proto" }
func (r rawCodec) Marshal(any) (mem.BufferSlice, error) {
	return mem.BufferSlice{mem.SliceBuffer(r.data)}, nil
}
func (r rawCodec) Unmarshal(b mem.BufferSlice, v any) error {
	return encoding.GetCodecV2("proto").Unmarshal(b, v)
}

type countProvider struct{ calls atomic.Int32 }

func (p *countProvider) Catalog(context.Context, resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
	p.calls.Add(1)
	return resourcev2.Catalog{Provider: "p", Revision: "r", Types: []resourcev2.ResourceType{}, Coverage: resourcev2.Coverage{Kind: "complete", Paths: []string{}}}, nil
}
func pipe(t *testing.T, s *grpc.Server) *grpc.ClientConn {
	t.Helper()
	l := bufconn.Listen(1 << 20)
	go s.Serve(l)
	t.Cleanup(func() { s.Stop(); l.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := grpc.DialContext(ctx, "passthrough:///resource", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return l.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
func bytesField(n protowire.Number, v []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(nil, n, protowire.BytesType), v)
}
func requestValue(v []byte) []byte {
	action := append(bytesField(4, v), bytesField(3, []byte("a"))...)
	return append([]byte{8, 2}, bytesField(7, action)...)
}
func TestRawRequestAdmissionDefaultProto(t *testing.T) {
	valid := append([]byte{8, 2}, bytesField(2, bytesField(1, []byte("c")))...)
	ts := bytesField(7, []byte{8, 0, 8, 0})
	entry := append(bytesField(1, []byte("k")), bytesField(2, []byte{16, 1})...)
	duplicateMap := bytesField(9, append(bytesField(1, entry), bytesField(1, entry)...))
	deep := []byte{10, 0}
	for range MaxDepth + 1 {
		deep = bytesField(8, bytesField(1, deep))
	}
	fields := make([]byte, 0, 2*(MaxFields+1))
	for range MaxFields + 1 {
		fields = append(fields, 10, 0)
	}
	fieldRequest := append([]byte{8, 2}, bytesField(4, bytesField(1, bytesField(5, bytesField(1, bytesField(3, fields)))))...)
	nodes := make([]byte, 0, 4*(MaxNodes+1))
	for range MaxNodes + 1 {
		nodes = append(nodes, 10, 2, 10, 0)
	}
	cases := []struct {
		name string
		data []byte
		code string
	}{
		{"duplicate scalar", append([]byte{8, 2}, valid...), "INVALID_REQUEST"},
		{"different oneof", requestValue([]byte{16, 1, 26, 0}), "INVALID_REQUEST"},
		{"repeated message oneof", requestValue([]byte{10, 0, 10, 0}), "INVALID_REQUEST"},
		{"timestamp duplicate", requestValue(ts), "INVALID_REQUEST"},
		{"map duplicate", requestValue(duplicateMap), "INVALID_REQUEST"},
		{"unknown outer", append(valid, 120, 1), "UNSUPPORTED"},
		{"unknown nested", requestValue([]byte{120, 1}), "UNSUPPORTED"},
		{"wrong wire", []byte{10, 0}, "INVALID_REQUEST"},
		{"truncated length", []byte{8, 2, 18, 5, 1}, "INVALID_REQUEST"},
		{"truncated varint", []byte{8, 128}, "INVALID_REQUEST"},
		{"group", []byte{11, 12}, "INVALID_REQUEST"},
		{"utf8", append([]byte{8, 2}, bytesField(2, bytesField(1, []byte{255}))...), "INVALID_REQUEST"},
		{"depth", requestValue(deep), "RESOURCE_LIMIT"},
		{"fields", fieldRequest, "RESOURCE_LIMIT"},
		{"nodes", requestValue(bytesField(8, nodes)), "RESOURCE_LIMIT"},
		{"version", append([]byte{8, 3}, valid[2:]...), "UNSUPPORTED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &countProvider{}
			s := grpc.NewServer()
			RegisterServer(s, p)
			c := pipe(t, s)
			var out resourcepb.Response
			err := c.Invoke(context.Background(), resourcepb.ResourceProvider_Call_FullMethodName, &resourcepb.Request{}, &out, grpc.ForceCodecV2(rawCodec{data: tc.data}), grpc.MaxCallSendMsgSize(MaxWireBytes))
			if got := resourcev2.ErrorCode(domainError(err)); got != tc.code {
				t.Fatalf("class %s, want %s: %v", got, tc.code, err)
			}
			if p.calls.Load() != 0 {
				t.Fatal("provider dispatched malformed request")
			}
		})
	}
	t.Run("valid", func(t *testing.T) {
		p := &countProvider{}
		s := grpc.NewServer()
		RegisterServer(s, p)
		c := pipe(t, s)
		var out resourcepb.Response
		if err := c.Invoke(context.Background(), resourcepb.ResourceProvider_Call_FullMethodName, &resourcepb.Request{}, &out, grpc.ForceCodecV2(rawCodec{data: valid})); err != nil {
			t.Fatal(err)
		}
		if p.calls.Load() != 1 {
			t.Fatal("valid request did not dispatch")
		}
	})
}
func TestRawResponseAdmission(t *testing.T) {
	cases := [][]byte{{8, 2, 8, 2}, {8, 2, 120, 1}, append([]byte{8, 2}, bytesField(5, []byte{16, 1, 16, 1})...), {8, 2, 42, 128}}
	entry := append(bytesField(1, []byte("k")), bytesField(2, []byte{10, 0})...)
	for _, value := range [][]byte{{10, 0, 10, 0}, {16, 1, 26, 0}, bytesField(7, []byte{16, 0, 16, 0}), bytesField(9, append(bytesField(1, entry), bytesField(1, entry)...)), {26, 1, 255}, {16, 128}} {
		cases = append(cases, append([]byte{8, 2}, bytesField(5, value)...))
	}
	for _, data := range cases {
		s := grpc.NewServer(grpc.ForceServerCodecV2(rawCodec{data: data}))
		RegisterServer(s, &countProvider{})
		c := NewClient(pipe(t, s))
		_, err := c.Catalog(context.Background(), resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: "c"}})
		want := "INVALID_REQUEST"
		if len(data) == 4 && data[2] == 120 {
			want = "UNSUPPORTED"
		}
		if resourcev2.ErrorCode(err) != want {
			t.Fatalf("response class %s want %s", resourcev2.ErrorCode(err), want)
		}
	}
}
func TestRawValueControls(t *testing.T) {
	values := [][]byte{{10, 0}, {66, 0}, {74, 0}, bytesField(7, nil), bytesField(8, append(bytesField(1, []byte{16, 1}), bytesField(1, []byte{26, 0})...))}
	first := append(bytesField(1, []byte("a")), bytesField(2, []byte{10, 0})...)
	second := append(bytesField(1, []byte("b")), bytesField(2, []byte{26, 0})...)
	values = append(values, bytesField(9, append(bytesField(1, first), bytesField(1, second)...)))
	for _, data := range values {
		if err := ValidateWire(data, (&resourcepb.Value{}).ProtoReflect().Descriptor()); err != nil {
			t.Fatal(err)
		}
		var v resourcepb.Value
		if err := proto.Unmarshal(data, &v); err != nil {
			t.Fatal(err)
		}
		payload := append([]byte{8, 2}, bytesField(5, data)...)
		s := grpc.NewServer(grpc.ForceServerCodecV2(rawCodec{data: payload}))
		RegisterServer(s, &countProvider{})
		c := NewClient(pipe(t, s))
		out, err := c.call(context.Background(), &resourcepb.Request{Operation: &resourcepb.Request_Catalog{Catalog: &resourcepb.AccessRoute{Connection: "c"}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = resourcev2.FromProtoValue(out.GetValue()); err != nil {
			t.Fatal(err)
		}
	}
}
func TestStrictProtoPreservesUnrelatedServices(t *testing.T) {
	s := grpc.NewServer()
	RegisterServer(s, &countProvider{})
	healthpb.RegisterHealthServer(s, health.NewServer())
	out, err := healthpb.NewHealthClient(pipe(t, s)).Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != healthpb.HealthCheckResponse_SERVING {
		t.Fatal("unrelated health RPC changed")
	}
}
