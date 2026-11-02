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

	"google.golang.org/grpc"

	"github.com/omniviewdev/plugin-sdk/pkg/interceptors"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	metricpb "github.com/omniviewdev/plugin-sdk/proto/v1/metric"
)

// The fixture binds the real started registry only when a test needs references.
func metricRegistry(
	t *testing.T,
	profiles []types.Connection,
	query QueryFunc,
	stream StreamFunc,
) (*PluginClient, *resourcev2.Controller[string]) {
	t.Helper()
	controller, err := resourcev2.NewController(resourcev2.Config[string]{
		Catalog: func(context.Context, resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
			return resourcev2.Catalog{
				Provider: "opaque",
				Revision: "catalog",
				Coverage: resourcev2.Coverage{Kind: "complete", Paths: []string{}},
				Types: []resourcev2.ResourceType{
					{
						Type:               "opaque::type",
						IdentityDimensions: []resourcev2.Dimension{},
						QueryDimensions:    []resourcev2.Dimension{},
						Representations: []resourcev2.Representation{
							{
								ID:       "document",
								Revision: "1",
								Schema: resourcev2.SchemaBinding{
									State: "absent",
								},
							},
						},
						Capabilities: []resourcev2.Capability{},
					},
				},
			}, nil
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
	for i := range profiles {
		if _, err = controller.StartConnection(context.Background(), profiles[i].ID); err != nil {
			t.Fatal(err)
		}
	}
	m := NewManager(nil, nil, ProviderInfo{ID: "metric"}, nil, query, stream)
	m.BindConnectionContextProvider(controller)
	s := grpc.NewServer(
		append(
			resourceplugin.ServerOptions(),
			grpc.ChainUnaryInterceptor(interceptors.UnaryPluginContext()),
			grpc.ChainStreamInterceptor(interceptors.StreamPluginContext()),
		)...)
	RegisterMetricPluginServer(s, &PluginServer{Impl: m})
	return &PluginClient{client: metricpb.NewMetricPluginClient(metricPipe(t, s))}, controller
}

func metricConnectionContext(id string) context.Context {
	return types.WithPluginContext(
		context.Background(),
		&types.PluginContext{RequestID: "metric-diagnostic", Connection: &types.Connection{ID: id}},
	)
}

func metricPin(t *testing.T, c *resourcev2.Controller[string], id string) string {
	t.Helper()
	catalog, err := c.Catalog(
		context.Background(),
		resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: id}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return catalog.Revision
}

func metricReference() *resourcev2.ResourceReference {
	return &resourcev2.ResourceReference{
		Address: resourcev2.ResourceAddress{
			Provider:  "opaque",
			Authority: "authority",
			Type:      "opaque::type",
			ID:        "id",
			Scope:     []resourcev2.ScopeEntry{},
		},
	}
}

func metricReceive(t *testing.T, out chan StreamOutput, value float64) {
	t.Helper()
	select {
	case r, ok := <-out:
		if !ok || r.OperationError != nil || len(r.Results) != 1 || r.Results[0].CurrentValue == nil ||
			r.Results[0].CurrentValue.Value != value {
			t.Fatalf("wanted native value %v: %+v", value, r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native sample missing")
	}
}

func TestMetricRouteOnlyQueryAdmission(t *testing.T) {
	var calls atomic.Int32
	query := func(ctx *QueryContext, _ QueryRequest) (*QueryResponse, error) {
		calls.Add(1)
		if ctx.Connection == nil || ctx.ConnectionID != "selected" || ctx.Connection.Data["endpoint"] != "configured" {
			t.Errorf("native query did not receive configured connection: %+v", ctx)
		}
		return &QueryResponse{Success: true}, nil
	}
	client, controller := metricRegistry(
		t,
		[]types.Connection{{ID: "selected", Data: map[string]any{"endpoint": "configured"}}},
		query,
		nil,
	)
	manager := NewManager(nil, nil, ProviderInfo{ID: "metric"}, nil, query, nil)
	manager.BindConnectionContextProvider(controller)
	request := QueryRequest{Route: &resourcev2.AccessRoute{Connection: "selected"}}
	wire, err := queryToProto(request, true)
	if err != nil {
		t.Fatalf("context-free encoding rejected route: %v", err)
	}
	for _, test := range []struct {
		name string
		pc   *types.PluginContext
		want string
	}{
		{name: "missing", pc: &types.PluginContext{}, want: "INVALID_REQUEST"},
		{name: "empty", pc: &types.PluginContext{Connection: &types.Connection{}}, want: "INVALID_REQUEST"},
		{name: "mismatch", pc: &types.PluginContext{Connection: &types.Connection{ID: "other"}}, want: "INVALID_REQUEST"},
		{name: "matching", pc: &types.PluginContext{Connection: &types.Connection{ID: "selected"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			// The transport interceptor extracts connection metadata only for identified requests.
			test.pc.RequestID = "metric-route-admission"
			before := calls.Load()
			_, err := manager.QueryResource(test.pc, request)
			if resourcev2.ErrorCode(err) != test.want {
				t.Fatalf("manager admission: %v", err)
			}
			// Use the raw RPC client so local client validation cannot hide server admission bugs.
			_, err = client.client.QueryResource(types.WithPluginContext(context.Background(), test.pc), wire)
			if resourcev2.ErrorCode(metricDomainError(err)) != test.want {
				t.Fatalf("server admission: %v", err)
			}
			wantCalls := int32(0)
			if test.want == "" {
				wantCalls = 2
			}
			if got := calls.Load() - before; got != wantCalls {
				t.Fatalf("native calls = %d, want %d", got, wantCalls)
			}
		})
	}
	before := calls.Load()
	if _, err := manager.QueryResource(nil, request); resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("nil manager context admitted: %v", err)
	}
	if calls.Load() != before {
		t.Fatal("nil context reached native query")
	}
}

func TestUtilityRouteRequiresSelectedConnection(t *testing.T) {
	for _, pc := range []*types.PluginContext{nil, {}} {
		called := false
		callback := func(*types.PluginContext) error {
			called = true
			return nil
		}
		err := resourcev2.WithUtilityConnection(pc, nil, nil, &resourcev2.AccessRoute{Connection: "selected"}, callback)
		if resourcev2.ErrorCode(err) != "INVALID_REQUEST" || called {
			t.Fatalf("route-only utility reached native callback: called=%v err=%v", called, err)
		}
		if err := resourcev2.WithUtilityConnection(pc, nil, nil, nil, callback); err != nil || !called {
			t.Fatalf("ordinary nonreference utility rejected: called=%v err=%v", called, err)
		}
	}
}

func TestMetricActualConnectionGenerationIsolation(t *testing.T) {
	blocked := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	var block atomic.Bool
	endpoint := metricBlockingEndpoint(blocked, canceled, &block)
	a, b, c := endpoint("1"), endpoint("2"), endpoint("3")
	defer a.Close()
	defer b.Close()
	defer c.Close()
	profiles := []types.Connection{
		{ID: "A", Name: "actual-A", Data: map[string]any{"endpoint": a.URL}},
		{ID: "B", Name: "actual-B", Data: map[string]any{"endpoint": b.URL}},
	}
	var calls atomic.Int32
	client, controller := metricRegistry(t, profiles, metricGenerationQuery(&calls), nil)
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
	subscribeB := StreamInput{
		SubscriptionID:  "same-opaque-id",
		Reference:       metricReference(),
		Route:           &resourcev2.AccessRoute{Connection: "B"},
		ResourceKey:     "opaque::type",
		CatalogRevision: pin,
		Interval:        20 * time.Millisecond,
	}
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
	metricAssertRetiredLane(t, outB, 3)
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

// metricAssertRetiredLane drains already-produced samples until retirement closes
// the lane, rejecting values that could only come from the replacement generation.
func metricAssertRetiredLane(t *testing.T, out <-chan StreamOutput, replacement float64) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case r, ok := <-out:
			if !ok {
				return
			}
			for _, v := range r.Results {
				if v.CurrentValue != nil && v.CurrentValue.Value == replacement {
					t.Fatal("old lane published replacement sample")
				}
			}
		case <-deadline:
			t.Fatal("retired lane remained open")
		}
	}
}

func TestMetricReferenceBeforeOpenAndABARejectStaleCatalog(t *testing.T) {
	var calls atomic.Int32
	original := types.Connection{ID: "connection", Data: map[string]any{"endpoint": "A"}}
	client, controller := metricRegistry(
		t,
		[]types.Connection{original},
		func(*QueryContext, QueryRequest) (*QueryResponse, error) {
			calls.Add(1)
			return &QueryResponse{Success: true}, nil
		},
		nil,
	)
	pin := metricPin(t, controller, "connection")
	assertStale := func() {
		t.Helper()
		before := calls.Load()
		pinned := resourcev2.WithCatalogRevision(metricConnectionContext("connection"), pin)
		pc := types.PluginContextFromContext(pinned)
		pc.Context = pinned
		_, err := client.Query(
			pc,
			QueryRequest{Reference: metricReference(), Route: &resourcev2.AccessRoute{Connection: "connection"}},
		)
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
		in <- StreamInput{
			SubscriptionID: "stale", Reference: metricReference(),
			Route: &resourcev2.AccessRoute{Connection: "connection"}, CatalogRevision: pin,
		}
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
	profiles := []types.Connection{
		{ID: "A", Name: "configured-A", Data: map[string]any{"nested": map[string]any{"value": "original"}}},
		{ID: "B", Name: "configured-B", Data: map[string]any{"nested": map[string]any{"value": "original"}}},
	}
	seen := make(chan *StreamContext, 2)
	client, controller := metricRegistry(
		t,
		profiles,
		nil,
		metricOwnedProfileStream(seen),
	)
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
		nested, ok := native.Connection.Data["nested"].(map[string]any)
		if !ok {
			t.Fatal("native profile lost nested data")
		}
		nested["value"] = "mutated"
		stored, err := controller.GetConnection(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		storedNested, ok := stored.Data["nested"].(map[string]any)
		if !ok || storedNested["value"] != "original" {
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

func TestMetricRetiredReferenceCannotBeReusedOrPublishQueuedNativeSample(t *testing.T) {
	subscribed := make(chan struct{}, 1)
	retired := make(chan struct{}, 1)
	release := make(chan struct{})
	native := metricRetiredSampleStream(subscribed, retired, release)
	client, controller := metricRegistry(
		t, []types.Connection{{ID: "connection", Data: map[string]any{}}}, nil, native,
	)
	ctx, cancel := context.WithCancel(metricConnectionContext("connection"))
	defer cancel()
	in := make(chan StreamInput, 4)
	out, err := client.StreamResourceMetrics(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	in <- StreamInput{
		SubscriptionID:  "retired",
		Reference:       metricReference(),
		Route:           &resourcev2.AccessRoute{Connection: "connection"},
		CatalogRevision: metricPin(t, controller, "connection"),
	}
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
	client, _ := metricRegistry(
		t,
		[]types.Connection{{ID: "connection", Data: map[string]any{}}},
		nil,
		metricSubscriptionObserver(received),
	)
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
	client, _ := metricRegistry(
		t,
		[]types.Connection{{ID: "connection", Data: map[string]any{}}},
		nil,
		metricSubscriptionObserver(received),
	)
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

func (p metricObservedConnectionProvider) WithConnection(
	ctx context.Context,
	route resourcev2.AccessRoute,
	fn func(context.Context, types.Connection) error,
) error {
	err := p.Controller.WithConnection(ctx, route, fn)
	p.released <- err
	return err
}

func TestMetricDirectManagerNativeOutputCloseReleasesConnectionLease(t *testing.T) {
	_, controller := metricRegistry(
		t,
		[]types.Connection{{ID: "connection", Name: "actual", Data: map[string]any{}}},
		nil,
		nil,
	)
	released := make(chan error, 1)
	nativeContext := make(chan context.Context, 1)
	manager := NewManager(
		nil,
		nil,
		ProviderInfo{},
		nil,
		nil,
		func(ctx *StreamContext, _ chan StreamInput) (chan StreamOutput, error) {
			nativeContext <- ctx.Context
			out := make(chan StreamOutput)
			close(out)
			return out, nil
		},
	)
	manager.BindConnectionContextProvider(metricObservedConnectionProvider{Controller: controller, released: released})
	// No RPC or caller cancellation may release the lease on this path.
	ctx := types.WithPluginContext(
		context.Background(),
		&types.PluginContext{Connection: &types.Connection{ID: "connection"}},
	)
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
	case <-(<-nativeContext).Done():
	case <-time.After(time.Second):
		t.Fatal("completed native stream context remained live")
	}
}

// metricBlockingEndpoint models native polling that must stop with its request.
func metricBlockingEndpoint(blocked, canceled chan struct{}, block *atomic.Bool) func(string) *httptest.Server {
	return func(value string) *httptest.Server {
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
}

func metricGenerationQuery(calls *atomic.Int32) QueryFunc {
	return func(q *QueryContext, r QueryRequest) (*QueryResponse, error) {
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
		endpoint, ok := q.Connection.Data["endpoint"].(string)
		if !ok {
			return nil, resourcev2.NewError("INVALID_REQUEST", "Configured endpoint missing.")
		}
		request, err := http.NewRequestWithContext(q.Context, http.MethodGet, endpoint, nil)
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
		return &QueryResponse{
			Success: true,
			Results: []MetricResult{{CurrentValue: &CurrentValue{MetricID: "native", Value: value}}},
		}, nil
	}
}

func metricOwnedProfileStream(seen chan *StreamContext) StreamFunc {
	return func(ctx *StreamContext, in chan StreamInput) (chan StreamOutput, error) {
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
					sample := StreamOutput{
						SubscriptionID: r.SubscriptionID,
						Results: []MetricResult{
							{CurrentValue: &CurrentValue{Value: float64(len(ctx.ConnectionID))}},
						},
					}
					select {
					case out <- sample:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
		return out, nil
	}
}

func metricSubscriptionObserver(received chan string) StreamFunc {
	return func(ctx *StreamContext, in chan StreamInput) (chan StreamOutput, error) {
		out := make(chan StreamOutput)
		go observeMetricSubscriptions(ctx, in, received, out)
		return out, nil
	}
}

func observeMetricSubscriptions(
	ctx *StreamContext,
	in <-chan StreamInput,
	received chan<- string,
	out chan<- StreamOutput,
) {
	defer close(out)
	for {
		select {
		case <-ctx.Done():
			return
		case r, ok := <-in:
			if !ok {
				return
			}
			if r.Command != StreamCommandSubscribe {
				continue
			}
			select {
			case received <- r.SubscriptionID:
			case <-ctx.Done():
				return
			}
		}
	}
}

// metricRetiredSampleStream queues stale output until the test releases retirement.
func metricRetiredSampleStream(subscribed, retired, release chan struct{}) StreamFunc {
	return func(ctx *StreamContext, in chan StreamInput) (chan StreamOutput, error) {
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
					if !metricHandleRetiredSample(ctx, r, out, subscribed, retired, release) {
						return
					}
				}
			}
		}()
		return out, nil
	}
}

func metricHandleRetiredSample(
	ctx *StreamContext,
	r StreamInput,
	out chan StreamOutput,
	subscribed, retired, release chan struct{},
) bool {
	value := float64(7)
	switch {
	case r.Command == StreamCommandUnsubscribe:
		retired <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return false
		}
		value = 99
	case r.SubscriptionID == "retired":
		subscribed <- struct{}{}
		return true
	}
	sample := StreamOutput{
		SubscriptionID: r.SubscriptionID,
		Results:        []MetricResult{{CurrentValue: &CurrentValue{Value: value}}},
	}
	select {
	case out <- sample:
		return true
	case <-ctx.Done():
		return false
	}
}
