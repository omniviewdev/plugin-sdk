package metric

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/interceptors"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	metricpb "github.com/omniviewdev/plugin-sdk/proto/v1/metric"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// The fixture binds the real started registry only when a test needs references.
func metricRegistry(t *testing.T, profiles []types.Connection, query QueryFunc, stream StreamFunc) (*PluginClient, *resourcev2.Controller[string]) {
	t.Helper()
	controller, err := resourcev2.NewController(resourcev2.Config[string]{
		Catalog: func(context.Context, resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
			return resourcev2.Catalog{Provider: "opaque", Revision: "catalog", Coverage: resourcev2.Coverage{Kind: "complete", Paths: []string{}}, Types: []resourcev2.ResourceType{{Type: "opaque::type", IdentityDimensions: []resourcev2.Dimension{}, QueryDimensions: []resourcev2.Dimension{}, Representations: []resourcev2.Representation{{ID: "document", Revision: "1", Schema: resourcev2.SchemaBinding{State: "absent", Validation: "none", Unvalidated: []string{}}}}, Capabilities: []resourcev2.Capability{}}}}, nil
		},
		Lifecycle: &resourcev2.Lifecycle[string]{
			LoadConnections: func(context.Context) ([]types.Connection, error) { return profiles, nil },
			CreateClient: func(_ context.Context, c types.Connection) (*string, error) {
				endpoint, _ := c.Data["endpoint"].(string)
				return &endpoint, nil
			},
			DestroyClient: func(context.Context, *string) error { return nil },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controller.Close() })
	if _, err = controller.LoadConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, p := range profiles {
		if _, err = controller.StartConnection(context.Background(), p.ID); err != nil {
			t.Fatal(err)
		}
	}
	m := NewManager(nil, nil, ProviderInfo{ID: "metric"}, nil, query, stream)
	m.BindConnectionContextProvider(controller)
	s := grpc.NewServer(append(resourceplugin.ServerOptions(), grpc.ChainUnaryInterceptor(interceptors.UnaryPluginContext()), grpc.ChainStreamInterceptor(interceptors.StreamPluginContext()))...)
	RegisterMetricPluginServer(s, &PluginServer{Impl: m})
	return &PluginClient{client: metricpb.NewMetricPluginClient(metricPipe(t, s))}, controller
}

func metricConnectionContext(id string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "omniview-connection-id", id, "omniview-request-id", "metric-diagnostic")
}
func metricPin(t *testing.T, c *resourcev2.Controller[string], id string) string {
	t.Helper()
	catalog, err := c.Catalog(context.Background(), resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: id}})
	if err != nil {
		t.Fatal(err)
	}
	return catalog.Revision
}
func metricReference() *resourcev2.ResourceReference {
	return &resourcev2.ResourceReference{Address: resourcev2.ResourceAddress{Provider: "opaque", Authority: "authority", Type: "opaque::type", ID: "id", Scope: []resourcev2.ScopeEntry{}}}
}
func metricReceive(t *testing.T, out chan StreamOutput, value float64) {
	t.Helper()
	select {
	case r, ok := <-out:
		if !ok || r.OperationError != nil || len(r.Results) != 1 || r.Results[0].CurrentValue == nil || r.Results[0].CurrentValue.Value != value {
			t.Fatalf("wanted native value %v: %+v", value, r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native sample missing")
	}
}

func TestMetricActualConnectionGenerationIsolation(t *testing.T) {
	blocked := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	var block atomic.Bool
	endpoint := func(value string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if value == "2" && block.Load() {
				select {
				case blocked <- struct{}{}:
				default:
				}
				<-r.Context().Done()
				select {
				case canceled <- struct{}{}:
				default:
				}
				return
			}
			_, _ = io.WriteString(w, value)
		}))
	}
	a, b, c := endpoint("1"), endpoint("2"), endpoint("3")
	defer a.Close()
	defer b.Close()
	defer c.Close()
	profiles := []types.Connection{{ID: "A", Name: "actual-A", Data: map[string]any{"endpoint": a.URL}}, {ID: "B", Name: "actual-B", Data: map[string]any{"endpoint": b.URL}}}
	var calls atomic.Int32
	client, controller := metricRegistry(t, profiles, func(q *QueryContext, r QueryRequest) (*QueryResponse, error) {
		calls.Add(1)
		if q.Connection == nil || q.Connection.ID != q.ConnectionID || q.Connection.Name != "actual-"+q.ConnectionID {
			return nil, resourcev2.NewError("INVALID_REQUEST", "Configured profile missing.")
		}
		if q.ConnectionID == "A" && r.HasResourceIntent() {
			return nil, resourcev2.NewError("INVALID_REQUEST", "Cluster input gained resource intent.")
		}
		pc := types.PluginContextFromContext(q.Context)
		if pc == nil || pc.RequestID != "metric-diagnostic" {
			return nil, resourcev2.NewError("INVALID_REQUEST", "Source context diagnostics lost.")
		}
		request, err := http.NewRequestWithContext(q.Context, http.MethodGet, q.Connection.Data["endpoint"].(string), nil)
		if err != nil {
			return nil, err
		}
		// The callback owns its profile snapshot, not the registry or next poll.
		q.Connection.Name = "callback-edited"
		q.Connection.Data["endpoint"] = "http://callback-edited.invalid"
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			return nil, err
		}
		value, err := strconv.ParseFloat(string(data), 64)
		if err != nil {
			return nil, err
		}
		return &QueryResponse{Success: true, Results: []MetricResult{{CurrentValue: &CurrentValue{MetricID: "native", Value: value}}}}, nil
	}, nil)
	open := func(id string) (chan StreamInput, chan StreamOutput, context.CancelFunc) {
		ctx, cancel := context.WithCancel(metricConnectionContext(id))
		in := make(chan StreamInput, 4)
		out, err := client.StreamResourceMetrics(ctx, in)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		return in, out, cancel
	}
	inA, outA, cancelA := open("A")
	defer cancelA()
	inB, outB, cancelB := open("B")
	defer cancelB()
	pin := metricPin(t, controller, "B")
	subscribeB := StreamInput{SubscriptionID: "same-opaque-id", Reference: metricReference(), Route: &resourcev2.AccessRoute{Connection: "B"}, ResourceKey: "opaque::type", CatalogRevision: pin, Interval: 20 * time.Millisecond}
	inA <- StreamInput{SubscriptionID: "same-opaque-id", ResourceKey: "cluster::metrics", Interval: time.Hour}
	inB <- subscribeB
	metricReceive(t, outA, 1)
	metricReceive(t, outB, 2)
	inA <- StreamInput{SubscriptionID: "same-opaque-id", Command: StreamCommandUnsubscribe}
	metricReceive(t, outB, 2)
	block.Store(true)
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("B native HTTP poll did not block")
	}
	changed := profiles[1]
	changed.Data = map[string]any{"endpoint": c.URL}
	if _, err := controller.UpdateConnection(context.Background(), changed); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("native HTTP request survived generation replacement")
	}
	// Drain samples produced before replacement, but reject publication from C on the old lane.
	deadline := time.After(5 * time.Second)
drain:
	for {
		select {
		case r, ok := <-outB:
			if !ok {
				break drain
			}
			for _, v := range r.Results {
				if v.CurrentValue != nil && v.CurrentValue.Value == 3 {
					t.Fatal("old lane published replacement sample")
				}
			}
		case <-deadline:
			t.Fatal("retired lane remained open")
		}
	}
	before := calls.Load()
	stoppedCtx, cancelStopped := context.WithCancel(metricConnectionContext("B"))
	defer cancelStopped()
	_, stoppedErr := client.StreamResourceMetrics(stoppedCtx, make(chan StreamInput))
	if resourcev2.ErrorCode(stoppedErr) != "CONNECTION_ERROR" {
		t.Fatalf("stopped generation: %v", stoppedErr)
	}
	if calls.Load() != before {
		t.Fatal("stopped lane invoked native callback")
	}
	if _, err := controller.StartConnection(context.Background(), "B"); err != nil {
		t.Fatal(err)
	}
	inNew, outNew, cancelNew := open("B")
	defer cancelNew()
	subscribeB.CatalogRevision = metricPin(t, controller, "B")
	inNew <- subscribeB
	metricReceive(t, outNew, 3)
}

func TestMetricReferenceBeforeOpenAndABARejectStaleCatalog(t *testing.T) {
	var calls atomic.Int32
	original := types.Connection{ID: "connection", Data: map[string]any{"endpoint": "A"}}
	client, controller := metricRegistry(t, []types.Connection{original}, func(*QueryContext, QueryRequest) (*QueryResponse, error) {
		calls.Add(1)
		return &QueryResponse{Success: true}, nil
	}, nil)
	pin := metricPin(t, controller, "connection")
	assertStale := func() {
		t.Helper()
		before := calls.Load()
		pc := &types.PluginContext{Context: resourcev2.WithCatalogRevision(metricConnectionContext("connection"), pin)}
		_, err := client.Query(pc, QueryRequest{Reference: metricReference(), Route: &resourcev2.AccessRoute{Connection: "connection"}})
		if resourcev2.ErrorCode(err) != "RESET_REQUIRED" {
			t.Fatalf("stale reference admitted: %v", err)
		}
		in := make(chan StreamInput, 1)
		ctx, cancel := context.WithCancel(metricConnectionContext("connection"))
		defer cancel()
		out, err := client.StreamResourceMetrics(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		in <- StreamInput{SubscriptionID: "stale", Reference: metricReference(), Route: &resourcev2.AccessRoute{Connection: "connection"}, CatalogRevision: pin}
		select {
		case r := <-out:
			if r.OperationError == nil || r.OperationError.Code != "RESET_REQUIRED" {
				t.Fatalf("stale stream: %+v", r)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("stale stream failure missing")
		}
		if calls.Load() != before {
			t.Fatal("stale reference reached native callback")
		}
	}
	changed := original
	changed.Data = map[string]any{"endpoint": "B"}
	if _, err := controller.UpdateConnection(context.Background(), changed); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.StartConnection(context.Background(), "connection"); err != nil {
		t.Fatal(err)
	}
	assertStale()
	if _, err := controller.UpdateConnection(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.StartConnection(context.Background(), "connection"); err != nil {
		t.Fatal(err)
	}
	assertStale()
}

func TestMetricCustomNativeStreamUsesOwnedConfiguredProfile(t *testing.T) {
	profiles := []types.Connection{{ID: "A", Name: "configured-A", Data: map[string]any{"nested": map[string]any{"value": "original"}}}, {ID: "B", Name: "configured-B", Data: map[string]any{"nested": map[string]any{"value": "original"}}}}
	seen := make(chan *StreamContext, 2)
	client, controller := metricRegistry(t, profiles, nil, func(ctx *StreamContext, in chan StreamInput) (chan StreamOutput, error) {
		seen <- ctx
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
					select {
					case out <- StreamOutput{SubscriptionID: r.SubscriptionID, Results: []MetricResult{{CurrentValue: &CurrentValue{Value: float64(len(ctx.ConnectionID))}}}}:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
		return out, nil
	})
	for _, id := range []string{"A", "B"} {
		ctx, cancel := context.WithCancel(metricConnectionContext(id))
		in := make(chan StreamInput, 1)
		out, err := client.StreamResourceMetrics(ctx, in)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		in <- StreamInput{SubscriptionID: "same", ResourceKey: "cluster::metrics"}
		metricReceive(t, out, 1)
		native := <-seen
		if native.ConnectionID != id || native.Connection == nil || native.Connection.Name != "configured-"+id {
			t.Fatalf("native context lost configured profile: %+v", native)
		}
		native.Connection.Data["nested"].(map[string]any)["value"] = "mutated"
		stored, err := controller.GetConnection(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Data["nested"].(map[string]any)["value"] != "original" {
			t.Fatal("native stream profile aliases registry")
		}
		cancel()
		select {
		case <-native.Done():
		case <-time.After(time.Second):
			t.Fatal("native context cancellation missing")
		}
	}
}

func metricTestOutgoingContext(ctx context.Context) context.Context {
	pc := types.PluginContextFromContext(ctx)
	if pc == nil {
		return ctx
	}
	if pc.RequestID != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "omniview-request-id", pc.RequestID)
	}
	if pc.Connection != nil {
		ctx = metadata.AppendToOutgoingContext(ctx, "omniview-connection-id", pc.Connection.ID)
	}
	return ctx
}
func metricTestUnaryContext(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	return invoke(metricTestOutgoingContext(ctx), method, req, reply, cc, opts...)
}
func metricTestStreamContext(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return streamer(metricTestOutgoingContext(ctx), desc, cc, method, opts...)
}

func TestMetricRetiredReferenceCannotBeReusedOrPublishQueuedNativeSample(t *testing.T) {
	subscribed := make(chan struct{}, 1)
	retired := make(chan struct{}, 1)
	release := make(chan struct{})
	native := func(ctx *StreamContext, in chan StreamInput) (chan StreamOutput, error) {
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
					if r.Command == StreamCommandUnsubscribe {
						retired <- struct{}{}
						select {
						case <-release:
						case <-ctx.Done():
							return
						}
						select {
						case out <- StreamOutput{SubscriptionID: r.SubscriptionID, Results: []MetricResult{{CurrentValue: &CurrentValue{Value: 99}}}}:
						case <-ctx.Done():
							return
						}
					} else if r.SubscriptionID == "retired" {
						subscribed <- struct{}{}
					} else {
						select {
						case out <- StreamOutput{SubscriptionID: r.SubscriptionID, Results: []MetricResult{{CurrentValue: &CurrentValue{Value: 7}}}}:
						case <-ctx.Done():
							return
						}
					}
				}
			}
		}()
		return out, nil
	}
	client, controller := metricRegistry(t, []types.Connection{{ID: "connection", Data: map[string]any{}}}, nil, native)
	ctx, cancel := context.WithCancel(metricConnectionContext("connection"))
	defer cancel()
	in := make(chan StreamInput, 4)
	out, err := client.StreamResourceMetrics(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	in <- StreamInput{SubscriptionID: "retired", Reference: metricReference(), Route: &resourcev2.AccessRoute{Connection: "connection"}, CatalogRevision: metricPin(t, controller, "connection")}
	select {
	case <-subscribed:
	case <-time.After(5 * time.Second):
		t.Fatal("reference subscribe missed native")
	}
	in <- StreamInput{SubscriptionID: "retired", Command: StreamCommandUnsubscribe}
	select {
	case <-retired:
	case <-time.After(5 * time.Second):
		t.Fatal("unsubscribe missed native")
	}
	in <- StreamInput{SubscriptionID: "retired", ResourceKey: "cluster::metrics"}
	select {
	case r := <-out:
		if r.OperationError == nil || r.OperationError.Code != "CONFLICT" || r.SubscriptionID != "retired" {
			t.Fatalf("same-lane ID reuse admitted: %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ID reuse failure missing")
	}
	close(release)
	in <- StreamInput{SubscriptionID: "fresh", ResourceKey: "cluster::metrics"}
	metricReceive(t, out, 7)
}

func TestMetricStreamBoundsAllUsedSubscriptionIDsBeforeNative(t *testing.T) {
	received := make(chan string, 1)
	client, _ := metricRegistry(t, []types.Connection{{ID: "connection", Data: map[string]any{}}}, nil, func(ctx *StreamContext, in chan StreamInput) (chan StreamOutput, error) {
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
					if r.Command == StreamCommandSubscribe {
						select {
						case received <- r.SubscriptionID:
						case <-ctx.Done():
							return
						}
					}
				}
			}
		}()
		return out, nil
	})
	ctx, cancel := context.WithCancel(metricConnectionContext("connection"))
	defer cancel()
	in := make(chan StreamInput, 1)
	out, err := client.StreamResourceMetrics(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 4096 {
		id := strconv.Itoa(i)
		in <- StreamInput{SubscriptionID: id, ResourceKey: "cluster::metrics"}
		select {
		case got := <-received:
			if got != id {
				t.Fatalf("native subscribe changed: %q", got)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("native subscribe %d missing", i)
		}
		in <- StreamInput{SubscriptionID: id, Command: StreamCommandUnsubscribe}
	}
	in <- StreamInput{SubscriptionID: "overflow", ResourceKey: "cluster::metrics"}
	select {
	case r := <-out:
		if r.OperationError == nil || r.OperationError.Code != "RESOURCE_LIMIT" {
			t.Fatalf("used-ID cap missing: %+v", r)
		}
	case got := <-received:
		t.Fatalf("overflow reached native: %q", got)
	case <-time.After(5 * time.Second):
		t.Fatal("used-ID cap failure missing")
	}
}

func TestMetricStreamBoundsRetainedMetadataBeforeNative(t *testing.T) {
	received := make(chan string, 1)
	client, _ := metricRegistry(t, []types.Connection{{ID: "connection", Data: map[string]any{}}}, nil, func(ctx *StreamContext, in chan StreamInput) (chan StreamOutput, error) {
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
					if r.Command == StreamCommandSubscribe {
						select {
						case received <- r.SubscriptionID:
						case <-ctx.Done():
							return
						}
					}
				}
			}
		}()
		return out, nil
	})
	ctx, cancel := context.WithCancel(metricConnectionContext("connection"))
	defer cancel()
	in := make(chan StreamInput, 1)
	out, err := client.StreamResourceMetrics(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	// IDs are retained even after unsubscribe. No resource body is involved.
	prefix := strings.Repeat("m", 1<<20)
	for i := range 33 {
		id := prefix + strconv.Itoa(i)
		in <- StreamInput{SubscriptionID: id, ResourceKey: "cluster::metrics"}
		select {
		case got := <-received:
			if got != id {
				t.Fatal("native subscription changed")
			}
			if i == 32 {
				t.Fatal("retained metadata exceeded 32 MiB")
			}
			in <- StreamInput{SubscriptionID: id, Command: StreamCommandUnsubscribe}
		case r := <-out:
			if i == 0 || r.OperationError == nil || r.OperationError.Code != "RESOURCE_LIMIT" {
				t.Fatalf("metadata cap classification: %+v", r.OperationError)
			}
			select {
			case <-received:
				t.Fatal("rejected metadata reached native")
			default:
			}
			return
		case <-time.After(5 * time.Second):
			t.Fatal("metadata admission stalled")
		}
	}
	t.Fatal("metadata cap was not enforced")
}

type metricObservedConnectionProvider struct {
	*resourcev2.Controller[string]
	released chan error
}

func (p metricObservedConnectionProvider) WithConnection(ctx context.Context, route resourcev2.AccessRoute, fn func(context.Context, types.Connection) error) error {
	err := p.Controller.WithConnection(ctx, route, fn)
	p.released <- err
	return err
}

func TestMetricDirectManagerNativeOutputCloseReleasesConnectionLease(t *testing.T) {
	_, controller := metricRegistry(t, []types.Connection{{ID: "connection", Name: "actual", Data: map[string]any{}}}, nil, nil)
	released := make(chan error, 1)
	nativeContext := make(chan context.Context, 1)
	manager := NewManager(nil, nil, ProviderInfo{}, nil, nil, func(ctx *StreamContext, _ chan StreamInput) (chan StreamOutput, error) {
		nativeContext <- ctx.Context
		out := make(chan StreamOutput)
		close(out)
		return out, nil
	})
	manager.BindConnectionContextProvider(metricObservedConnectionProvider{Controller: controller, released: released})
	// No RPC or caller cancellation may release the lease on this path.
	ctx := types.WithPluginContext(context.Background(), &types.PluginContext{Connection: &types.Connection{ID: "connection"}})
	out, err := manager.StreamResourceMetrics(ctx, make(chan StreamInput))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-out:
		if ok {
			t.Fatal("closed native output invented a sample")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closed native output did not close manager output")
	}
	select {
	case err := <-released:
		if err != nil {
			t.Fatalf("completed native stream failed its connection callback: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closed native output retained the WithConnection lease")
	}
	select {
	case <-((<-nativeContext).Done()):
	case <-time.After(time.Second):
		t.Fatal("completed native stream context remained live")
	}
}
