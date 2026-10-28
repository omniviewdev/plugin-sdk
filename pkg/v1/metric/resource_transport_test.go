package metric

import (
	"context"
	"fmt"
	"github.com/omniviewdev/plugin-sdk/pkg/interceptors"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	metricpb "github.com/omniviewdev/plugin-sdk/proto/v1/metric"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/mem"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"math"
	"net"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func metricPipe(t *testing.T, s *grpc.Server) *grpc.ClientConn {
	t.Helper()
	l := bufconn.Listen(1 << 20)
	go s.Serve(l)
	t.Cleanup(func() { s.Stop(); l.Close() })
	options := append(sdk.GRPCDialOptions(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return l.Dial() }))
	c, e := grpc.NewClient("passthrough:///metric-test", options...)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
func metricFixture(t *testing.T, query QueryFunc) (*PluginClient, *grpc.ClientConn) {
	t.Helper()
	s := grpc.NewServer(append(resourceplugin.ServerOptions(), grpc.ChainUnaryInterceptor(interceptors.UnaryPluginContext()), grpc.ChainStreamInterceptor(interceptors.StreamPluginContext()))...)
	m := NewManager(nil, nil, ProviderInfo{ID: "metric"}, nil, query, nil)
	RegisterMetricPluginServer(s, &PluginServer{Impl: m})
	conn := metricPipe(t, s)
	return &PluginClient{client: metricpb.NewMetricPluginClient(conn)}, conn
}
func exactMetricBody(t *testing.T, size int) *resourcev2.Value {
	t.Helper()
	v, e := resourcev2.ValueFromNative(map[string]any{"min": int64(math.MinInt64), "max": uint64(math.MaxUint64), "time": time.Unix(-1, 123).UTC(), "nested": []any{math.Copysign(0, -1), []any{uint64(math.MaxUint64)}}, "large": strings.Repeat("x", size)})
	if e != nil {
		t.Fatal(e)
	}
	return &v
}
func assertMetricBody(t *testing.T, want, got *resourcev2.Value) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Fatal("canonical body changed")
	}
	if got != nil && !math.Signbit(got.Map["nested"].List[0].Double) {
		t.Fatal("nested negative zero changed")
	}
}
func TestMetricResourceActualGRPCQueryAndPolling(t *testing.T) {
	for _, size := range []int{0, 3 << 20} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			seen := make(chan QueryRequest, 4)
			contexts := make(chan context.Context, 4)
			c, registry := metricRegistry(t, []types.Connection{{ID: "connection", Name: "configured", Data: map[string]any{}}}, func(ctx *QueryContext, r QueryRequest) (*QueryResponse, error) {
				seen <- r
				contexts <- ctx.Context
				return &QueryResponse{Success: true}, nil
			}, nil)
			pin := metricPin(t, registry, "connection")
			body := exactMetricBody(t, size)
			reference := &resourcev2.ResourceReference{Address: resourcev2.ResourceAddress{Provider: "opaque", Authority: "authority", Type: "opaque::type", ID: "id", Scope: []resourcev2.ScopeEntry{}}}
			route := &resourcev2.AccessRoute{Connection: "connection"}
			req := QueryRequest{ResourceKey: "opaque::type", ResourceData: body, Reference: reference, Route: route}
			pinned := resourcev2.WithCatalogRevision(metricConnectionContext("connection"), pin)
			pc := types.PluginContextFromContext(pinned)
			pc.Context = pinned
			if _, e := c.Query(pc, req); e != nil {
				t.Fatal(e)
			}
			r := <-seen
			assertMetricBody(t, body, r.ResourceData)
			if !reflect.DeepEqual(r.Reference, reference) || !reflect.DeepEqual(r.Route, route) {
				t.Fatal("reference or route changed")
			}
			<-contexts
			ctx, cancel := context.WithCancel(metricConnectionContext("connection"))
			in := make(chan StreamInput, 1)
			out, e := c.StreamResourceMetrics(ctx, in)
			if e != nil {
				t.Fatal(e)
			}
			in <- StreamInput{SubscriptionID: "resource", ResourceKey: req.ResourceKey, ResourceData: body, Reference: reference, Route: route, CatalogRevision: pin, Interval: time.Hour}
			select {
			case r = <-seen:
			case <-time.After(5 * time.Second):
				t.Fatal("poll did not reach query callback")
			}
			assertMetricBody(t, body, r.ResourceData)
			if !reflect.DeepEqual(r.Reference, reference) || !reflect.DeepEqual(r.Route, route) {
				t.Fatal("poll lost resource target")
			}
			callbackCtx := <-contexts
			select {
			case output := <-out:
				if output.OperationError != nil {
					t.Fatal(output.OperationError)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("missing poll output")
			}
			cancel()
			select {
			case <-callbackCtx.Done():
			case <-time.After(time.Second):
				t.Fatal("callback context not cancelled")
			}
		})
	}
}
func TestMetricClusterNonresourceQueryAndStream(t *testing.T) {
	seen := make(chan QueryRequest, 4)
	c, _ := metricFixture(t, func(_ *QueryContext, r QueryRequest) (*QueryResponse, error) {
		seen <- r
		return &QueryResponse{Success: true}, nil
	})
	if _, e := c.Query(nil, QueryRequest{ResourceKey: "cluster::metrics"}); e != nil {
		t.Fatal(e)
	}
	if (<-seen).HasResourceIntent() {
		t.Fatal("cluster selector invented resource intent")
	}
	for _, modern := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		in := make(chan StreamInput, 1)
		var out chan StreamOutput
		var e error
		if modern {
			out, e = c.StreamResourceMetrics(ctx, in)
		} else {
			out, e = c.StreamMetrics(ctx, in)
		}
		if e != nil {
			t.Fatal(e)
		}
		in <- StreamInput{SubscriptionID: fmt.Sprint(modern), ResourceKey: "cluster::metrics", Interval: time.Hour}
		select {
		case r := <-seen:
			if r.HasResourceIntent() {
				t.Fatal("cluster stream invented resource intent")
			}
		case <-time.After(time.Second):
			t.Fatal("cluster poll missing")
		}
		select {
		case <-out:
		case <-time.After(time.Second):
			t.Fatal("cluster output missing")
		}
		cancel()
	}
}

type oldMetricServer struct {
	metricpb.UnimplementedMetricPluginServer
	calls atomic.Int32
}

func (s *oldMetricServer) Query(context.Context, *metricpb.QueryMetricsRequest) (*metricpb.QueryMetricsResponse, error) {
	s.calls.Add(1)
	return &metricpb.QueryMetricsResponse{}, nil
}
func (s *oldMetricServer) StreamMetrics(metricpb.MetricPlugin_StreamMetricsServer) error {
	s.calls.Add(1)
	return nil
}
func TestMetricOldServerNoNativeEffects(t *testing.T) {
	old := new(oldMetricServer)
	s := grpc.NewServer(resourceplugin.ServerOptions()...)
	metricpb.RegisterMetricPluginServer(s, old)
	c := &PluginClient{client: metricpb.NewMetricPluginClient(metricPipe(t, s))}
	_, e := c.Query(nil, QueryRequest{ResourceData: exactMetricBody(t, 0)})
	if resourcev2.ErrorCode(e) != "UNSUPPORTED" {
		t.Fatalf("query: %v", e)
	}
	_, e = c.StreamResourceMetrics(context.Background(), make(chan StreamInput))
	if resourcev2.ErrorCode(e) != "UNSUPPORTED" {
		t.Fatalf("stream: %v", e)
	}
	if old.calls.Load() != 0 {
		t.Fatal("legacy native callback invoked")
	}
}

type metricRawCodec struct{ data []byte }

func (metricRawCodec) Name() string { return "proto" }
func (c metricRawCodec) Marshal(any) (mem.BufferSlice, error) {
	return mem.BufferSlice{mem.SliceBuffer(c.data)}, nil
}
func (metricRawCodec) Unmarshal(b mem.BufferSlice, v any) error {
	return encoding.GetCodecV2("proto").Unmarshal(b, v)
}
func metricVarint(n protowire.Number, v uint64) []byte {
	return protowire.AppendVarint(protowire.AppendTag(nil, n, protowire.VarintType), v)
}
func metricBytes(n protowire.Number, v []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(nil, n, protowire.BytesType), v)
}
func TestMetricMalformedRequestsBeforeNativeDispatch(t *testing.T) {
	var calls atomic.Int32
	_, conn := metricFixture(t, func(*QueryContext, QueryRequest) (*QueryResponse, error) { calls.Add(1); return &QueryResponse{}, nil })
	for _, data := range [][]byte{metricBytes(4, nil), append(metricVarint(12, 2), metricVarint(12, 2)...), append(metricVarint(12, 2), metricBytes(11, append(metricVarint(2, 1), metricVarint(2, 0)...))...)} {
		var response metricpb.ResourceQueryMetricsResponse
		e := conn.Invoke(context.Background(), "/"+metricpb.MetricPlugin_ServiceDesc.ServiceName+"/QueryResource", &metricpb.QueryMetricsRequest{}, &response, grpc.ForceCodecV2(metricRawCodec{data: data}))
		if e == nil {
			t.Fatal("malformed query admitted")
		}
	}
	for _, modern := range []bool{false, true} {
		name := "StreamMetrics"
		if modern {
			name = "StreamResourceMetrics"
		}
		stream, e := conn.NewStream(context.Background(), &grpc.StreamDesc{ClientStreams: true, ServerStreams: true}, "/"+metricpb.MetricPlugin_ServiceDesc.ServiceName+"/"+name, grpc.ForceCodecV2(metricRawCodec{data: append(metricVarint(10, 2), metricVarint(10, 2)...)}))
		if e != nil {
			t.Fatal(e)
		}
		_ = stream.SendMsg(&metricpb.MetricStreamInput{})
		var response metricpb.ResourceMetricStreamOutput
		if e = stream.RecvMsg(&response); e == nil {
			t.Fatal("duplicate stream version admitted")
		}
		stream.CloseSend()
	}
	if calls.Load() != 0 {
		t.Fatal("malformed resource reached native query")
	}
}

type malformedMetricServer struct {
	metricpb.UnimplementedMetricPluginServer
}

func (malformedMetricServer) QueryResource(context.Context, *metricpb.QueryMetricsRequest) (*metricpb.ResourceQueryMetricsResponse, error) {
	return &metricpb.ResourceQueryMetricsResponse{ResourceWireVersion: 2, Response: &metricpb.QueryMetricsResponse{}}, nil
}
func (malformedMetricServer) StreamResourceMetrics(stream metricpb.MetricPlugin_StreamResourceMetricsServer) error {
	if _, e := stream.Recv(); e != nil {
		return e
	}
	return stream.Send(&metricpb.ResourceMetricStreamOutput{ResourceWireVersion: 2, Payload: &metricpb.ResourceMetricStreamOutput_Ready{Ready: true}})
}
func TestMetricMalformedResponsesBeforeDecode(t *testing.T) {
	for _, data := range [][]byte{append(metricVarint(1, 2), metricVarint(1, 2)...), append(metricVarint(1, 2), append(metricVarint(3, 1), metricBytes(2, nil)...)...)} {
		s := grpc.NewServer(append(resourceplugin.ServerOptions(), grpc.ForceServerCodecV2(metricRawCodec{data: data}))...)
		metricpb.RegisterMetricPluginServer(s, malformedMetricServer{})
		c := &PluginClient{client: metricpb.NewMetricPluginClient(metricPipe(t, s))}
		if _, e := c.Query(nil, QueryRequest{ResourceData: exactMetricBody(t, 0)}); e == nil {
			t.Fatal("malformed query response admitted")
		}
		if _, e := c.StreamResourceMetrics(context.Background(), make(chan StreamInput)); e == nil {
			t.Fatal("malformed stream response admitted")
		}
	}
}
func TestMetricLegacyResourceFrameRejectsBeforeNative(t *testing.T) {
	var calls atomic.Int32
	c, conn := metricFixture(t, func(*QueryContext, QueryRequest) (*QueryResponse, error) { calls.Add(1); return &QueryResponse{}, nil })
	_ = c
	raw, _ := proto.Marshal(&metricpb.MetricStreamInput{ResourceWireVersion: 2, ResourceData: nil})
	stream, e := conn.NewStream(context.Background(), &grpc.StreamDesc{ClientStreams: true, ServerStreams: true}, "/"+metricpb.MetricPlugin_ServiceDesc.ServiceName+"/StreamMetrics", grpc.ForceCodecV2(metricRawCodec{data: raw}))
	if e != nil {
		t.Fatal(e)
	}
	stream.SendMsg(&metricpb.MetricStreamInput{})
	var response metricpb.MetricStreamOutput
	if e = stream.RecvMsg(&response); e == nil {
		t.Fatal("legacy admitted canonical fields")
	}
	if calls.Load() != 0 {
		t.Fatal("legacy frame reached native callback")
	}
}
