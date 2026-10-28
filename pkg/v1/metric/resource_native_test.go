package metric

import (
	"context"
	"encoding/json"
	"errors"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	metricpb "github.com/omniviewdev/plugin-sdk/proto/v1/metric"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/mem"
	"google.golang.org/grpc/status"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMetricNativeMixedStreamAndControl(t *testing.T) {
	received := make(chan StreamInput, 4)
	contexts := make(chan context.Context, 1)
	m := NewManager(nil, nil, ProviderInfo{}, nil, nil, func(ctx *StreamContext, in chan StreamInput) (chan StreamOutput, error) {
		contexts <- ctx.Context
		out := make(chan StreamOutput)
		go func() {
			defer close(out)
			for {
				select {
				case <-ctx.Done():
					return
				case r, ok := <-in:
					if !ok {
						return
					}
					received <- r
					select {
					case out <- StreamOutput{SubscriptionID: r.SubscriptionID}:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
		return out, nil
	})
	s := grpc.NewServer(resourceplugin.ServerOptions()...)
	RegisterMetricPluginServer(s, &PluginServer{Impl: m})
	c := &PluginClient{client: metricpb.NewMetricPluginClient(metricPipe(t, s))}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan StreamInput, 4)
	out, e := c.StreamResourceMetrics(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	body := exactMetricBody(t, 3<<20)
	inputs := []StreamInput{{SubscriptionID: "r", ResourceData: body}, {SubscriptionID: "cluster", ResourceKey: "cluster::metrics"}, {SubscriptionID: "r", Command: StreamCommandUnsubscribe}, {Command: StreamCommandClose}}
	for _, r := range inputs {
		in <- r
		select {
		case got := <-received:
			if got.SubscriptionID != r.SubscriptionID || got.Command != r.Command {
				t.Fatal("stream control changed")
			}
			if r.HasResourceIntent() {
				assertMetricBody(t, body, got.ResourceData)
			} else if got.HasResourceIntent() {
				t.Fatal("nonresource frame invented target")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("native stream missed frame")
		}
		if r.Command != StreamCommandSubscribe {
			continue // Control frames retire bindings; native control outputs are not samples.
		}
		select {
		case <-out:
		case <-time.After(time.Second):
			t.Fatal("native output missing")
		}
	}
	callbackCtx := <-contexts
	cancel()
	select {
	case <-callbackCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("native stream did not receive cancellation")
	}
}
func TestMetricTypedQueryAndPollingFailure(t *testing.T) {
	c, _ := metricFixture(t, func(*QueryContext, QueryRequest) (*QueryResponse, error) {
		return nil, errors.Join(errors.New("provider"), resourcev2.NewError("FORBIDDEN", "Safe detail."))
	})
	body := exactMetricBody(t, 0)
	_, e := c.Query(&types.PluginContext{}, QueryRequest{ResourceData: body})
	if resourcev2.ErrorCode(e) != "FORBIDDEN" {
		t.Fatalf("query error lost type: %v", e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan StreamInput, 1)
	out, e := c.StreamResourceMetrics(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	in <- StreamInput{ResourceData: body, Interval: time.Hour}
	select {
	case r := <-out:
		if r.OperationError == nil || r.OperationError.Code != "FORBIDDEN" || r.OperationError.Message == "" {
			t.Fatalf("poll error lost type: %+v", r)
		}
	case <-time.After(time.Second):
		t.Fatal("poll error missing")
	}
}
func TestMetricStrictJSONAndTrustedRoute(t *testing.T) {
	for _, data := range []string{`null`, `{"resource_data":{"kind":"int64","value":9007199254740993}}`, `{"resource_key":"a","resource_key":"b"}`, `{"unknown":1}`, `{"resource_data":{"kind":"map","value":{},"value":{}}}`} {
		var r QueryRequest
		if json.Unmarshal([]byte(data), &r) == nil {
			t.Fatalf("invalid JSON accepted: %s", data)
		}
	}
	r := QueryRequest{Route: &resourcev2.AccessRoute{Connection: "explicit"}}
	if e := validateQuery(r, &types.PluginContext{Connection: &types.Connection{ID: "trusted"}}); resourcev2.ErrorCode(e) != "INVALID_REQUEST" {
		t.Fatalf("route conflict admitted: %v", e)
	}
}

type malformedMetricOutputCodec struct{}

func (malformedMetricOutputCodec) Name() string { return "proto" }
func (malformedMetricOutputCodec) Marshal(v any) (mem.BufferSlice, error) {
	if p, ok := v.(*metricpb.ResourceMetricStreamOutput); ok && !p.GetReady() {
		data := append(metricVarint(1, 2), append(metricBytes(2, nil), metricBytes(2, nil)...)...)
		return mem.BufferSlice{mem.SliceBuffer(data)}, nil
	}
	return encoding.GetCodecV2("proto").Marshal(v)
}
func (malformedMetricOutputCodec) Unmarshal(b mem.BufferSlice, v any) error {
	return encoding.GetCodecV2("proto").Unmarshal(b, v)
}

type malformedMetricAfterReady struct {
	metricpb.UnimplementedMetricPluginServer
}

func (malformedMetricAfterReady) StreamResourceMetrics(stream metricpb.MetricPlugin_StreamResourceMetricsServer) error {
	if _, e := stream.Recv(); e != nil {
		return e
	}
	if e := stream.Send(&metricpb.ResourceMetricStreamOutput{ResourceWireVersion: 2, Payload: &metricpb.ResourceMetricStreamOutput_Ready{Ready: true}}); e != nil {
		return e
	}
	return stream.Send(&metricpb.ResourceMetricStreamOutput{ResourceWireVersion: 2, Payload: &metricpb.ResourceMetricStreamOutput_Output{Output: &metricpb.MetricStreamOutput{}}})
}
func TestMetricMalformedStreamOutputAfterReady(t *testing.T) {
	s := grpc.NewServer(append(resourceplugin.ServerOptions(), grpc.ForceServerCodecV2(malformedMetricOutputCodec{}))...)
	metricpb.RegisterMetricPluginServer(s, malformedMetricAfterReady{})
	c := &PluginClient{client: metricpb.NewMetricPluginClient(metricPipe(t, s))}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, e := c.StreamResourceMetrics(ctx, make(chan StreamInput))
	if e != nil {
		t.Fatal(e)
	}
	select {
	case r := <-out:
		if r.OperationError == nil || r.OperationError.Code != "INVALID_REQUEST" || r.OperationError.Message == "" {
			t.Fatalf("malformed response did not publish typed failure: %+v", r)
		}
	case <-time.After(time.Second):
		t.Fatal("malformed response failure missing")
	}
}
func TestMetricMalformedStreamInputAfterReady(t *testing.T) {
	called := make(chan QueryRequest, 1)
	_, conn := metricFixture(t, func(_ *QueryContext, r QueryRequest) (*QueryResponse, error) {
		called <- r
		return &QueryResponse{}, nil
	})
	stream, e := metricpb.NewMetricPluginClient(conn).StreamResourceMetrics(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if e = stream.Send(&metricpb.MetricStreamInput{ResourceWireVersion: 2, ResourceOpen: true}); e != nil {
		t.Fatal(e)
	}
	if ack, e := stream.Recv(); e != nil || !ack.GetReady() {
		t.Fatalf("handshake: %v %v", ack, e)
	}
	p := &metricpb.MetricStreamInput{ResourceWireVersion: 2}
	p.ProtoReflect().SetUnknown(metricBytes(6, nil))
	if e = stream.Send(p); e != nil {
		t.Fatal(e)
	}
	if _, e = stream.Recv(); e == nil {
		t.Fatal("retired Struct frame admitted after handshake")
	}
	select {
	case <-called:
		t.Fatal("malformed frame reached callback")
	default:
	}
}

func TestMetricQueryCallbackCancellation(t *testing.T) {
	started := make(chan context.Context, 1)
	c, _ := metricFixture(t, func(ctx *QueryContext, _ QueryRequest) (*QueryResponse, error) {
		started <- ctx.Context
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.Query(&types.PluginContext{Context: ctx}, QueryRequest{ResourceData: exactMetricBody(t, 0)})
		done <- err
	}()
	var native context.Context
	select {
	case native = <-started:
	case <-time.After(time.Second):
		t.Fatal("query callback missing")
	}
	cancel()
	select {
	case <-native.Done():
	case <-time.After(time.Second):
		t.Fatal("query callback did not receive cancellation")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("query client did not return")
	}
}

func TestMetricStreamParamsRemainNonresource(t *testing.T) {
	seen := make(chan QueryRequest, 1)
	c, _ := metricFixture(t, func(_ *QueryContext, r QueryRequest) (*QueryResponse, error) {
		seen <- r
		return &QueryResponse{Success: true}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan StreamInput, 1)
	out, err := c.StreamMetrics(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	in <- StreamInput{ResourceKey: "cluster::metrics", Params: map[string]string{"prometheus_service_name": "prometheus", "prometheus_service_namespace": "monitoring", "prometheus_service_port": "9090"}, Interval: time.Hour}
	select {
	case r := <-seen:
		if r.HasResourceIntent() || r.Params["prometheus_service_name"] != "prometheus" || r.Params["prometheus_service_namespace"] != "monitoring" || r.Params["prometheus_service_port"] != "9090" {
			t.Fatal("polling lost ordinary stream Params")
		}
	case <-time.After(time.Second):
		t.Fatal("nonresource poll missing")
	}
	select {
	case <-out:
	case <-time.After(time.Second):
		t.Fatal("nonresource output missing")
	}
}

func TestMetricStrictJSONRejectsSurrogateAndNodeLimits(t *testing.T) {
	for _, data := range []string{
		`{"params":{"service":"\ud800"}}`,
		`{"resource_key":"one","resource_key":"two"}`,
		`{"Resource_Key":"cluster::metrics"}`,
	} {
		var input StreamInput
		if err := json.Unmarshal([]byte(data), &input); resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
			t.Fatalf("invalid outer JSON admitted: %q %v", data, err)
		}
	}
}

func TestMetricClientLocalFailureKeepsClassAndSubscription(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan StreamInput, 1)
	out := clientStream(ctx, cancel, in, true,
		func(*metricpb.MetricStreamInput) error { t.Fatal("invalid frame reached send"); return nil },
		func() (StreamOutput, error) { <-ctx.Done(); return StreamOutput{}, ctx.Err() },
		func() error { return nil })
	in <- StreamInput{SubscriptionID: "safe-subscription", Route: &resourcev2.AccessRoute{}}
	select {
	case output := <-out:
		if output.SubscriptionID != "safe-subscription" || output.OperationError == nil || output.OperationError.Code != "INVALID_REQUEST" || output.OperationError.Message == "" {
			t.Fatalf("local failure lost classification: %+v", output)
		}
	case <-time.After(time.Second):
		t.Fatal("local failure missing")
	}
}

func TestMetricJSONSharedNodeLimit(t *testing.T) {
	data := `{"metric_ids":[` + strings.Repeat(`"x",`, resourcev2.MaxValueNodes) + `"x"]}`
	var input StreamInput
	if err := json.Unmarshal([]byte(data), &input); resourcev2.ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("shared JSON node budget not enforced: %v", err)
	}
}

func TestMetricDomainErrorKeepsLocalRemoteAndTransportClasses(t *testing.T) {
	local := resourcev2.NewError("RESOURCE_LIMIT", "Local limit.")
	if got := metricDomainError(local); got != local {
		t.Fatalf("local error replaced: %v", got)
	}
	if got := metricDomainError(status.Error(codes.Unimplemented, "unknown method")); resourcev2.ErrorCode(got) != "UNSUPPORTED" {
		t.Fatalf("missing capability not typed: %v", got)
	}
	for _, code := range []string{"FORBIDDEN", "UNSUPPORTED", "RESOURCE_LIMIT", "RESET_REQUIRED"} {
		remote := resourceplugin.ResourceRPCError(resourcev2.NewError(code, "Remote classification."))
		if got := metricDomainError(remote); resourcev2.ErrorCode(got) != code {
			t.Fatalf("remote %s changed: %v", code, got)
		}
	}
	for _, code := range []codes.Code{codes.Unavailable, codes.Canceled, codes.DeadlineExceeded, codes.Internal} {
		if got := metricDomainError(status.Error(code, "transport")); status.Code(got) != code {
			t.Fatalf("transport %s changed: %v", code, got)
		}
	}
}

func TestMetricSubscribeCatalogRevisionRoundTrip(t *testing.T) {
	input := StreamInput{SubscriptionID: "opaque", ResourceKey: "opaque::type", Reference: metricReference(), CatalogRevision: "accepted-generation"}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"catalog_revision":"accepted-generation"`) {
		t.Fatalf("catalog revision missing from JSON: %s", data)
	}
	var decoded StreamInput
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	wire, err := streamToProto(decoded, true)
	if err != nil {
		t.Fatal(err)
	}
	field := wire.ProtoReflect().Descriptor().Fields().ByName("catalog_revision")
	if field == nil || field.Number() != 15 {
		t.Fatal("catalog revision must use protobuf field 15")
	}
	got, err := streamFromProto(wire, true)
	if err != nil || got.CatalogRevision != input.CatalogRevision {
		t.Fatalf("catalog revision changed: %+v %v", got, err)
	}
	if !got.HasResourceIntent() || !reflect.DeepEqual(got.Reference, input.Reference) {
		t.Fatal("canonical reference or empty scope changed")
	}
}
