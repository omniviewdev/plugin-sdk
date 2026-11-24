package plugin

import (
	"context"
	"errors"
	"net"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/mem"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
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
	return resourcev2.Catalog{
		Provider: "p",
		Revision: "r",
		Types:    []resourcev2.ResourceType{},
		Coverage: resourcev2.Coverage{Kind: "complete", Paths: []string{}},
	}, nil
}

func pipe(t *testing.T, s *grpc.Server) *grpc.ClientConn {
	t.Helper()
	l := bufconn.Listen(1 << 20)
	go func() {
		if serveErr := s.Serve(l); serveErr != nil && !errors.Is(serveErr, grpc.ErrServerStopped) {
			t.Errorf("serve test transport: %v", serveErr)
		}
	}()
	t.Cleanup(func() {
		s.Stop()
		if closeErr := l.Close(); closeErr != nil {
			t.Errorf("close test listener: %v", closeErr)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := grpc.NewClient(
		"passthrough:///resource",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return l.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	c.Connect()
	waitForPipeReady(ctx, t, c)
	t.Cleanup(func() {
		if closeErr := c.Close(); closeErr != nil {
			t.Errorf("close test connection: %v", closeErr)
		}
	})
	return c
}

// waitForPipeReady keeps fixture setup synchronous without deprecated blocking dial options.
func waitForPipeReady(ctx context.Context, t *testing.T, c *grpc.ClientConn) {
	t.Helper()
	for {
		state := c.GetState()
		if state == connectivity.Ready {
			return
		}
		if !c.WaitForStateChange(ctx, state) {
			t.Fatalf("test transport did not become ready: %v", ctx.Err())
		}
	}
}

func bytesField(n protowire.Number, v []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(nil, n, protowire.BytesType), v)
}

func requestValue(v []byte) []byte {
	action := append(bytesField(4, v), bytesField(3, []byte("a"))...)
	return append([]byte{8, byte(resourcev2.WireVersion)}, bytesField(7, action)...)
}

func TestRawRequestAdmissionDefaultProto(t *testing.T) {
	valid := append([]byte{8, byte(resourcev2.WireVersion)}, bytesField(10, bytesField(1, bytesField(1, []byte("c"))))...)
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
	fieldRequest := append(
		[]byte{8, byte(resourcev2.WireVersion)},
		bytesField(4, bytesField(1, bytesField(5, bytesField(1, bytesField(3, fields)))))...)
	nodes := make([]byte, 0, 4*(MaxNodes+1))
	for range MaxNodes + 1 {
		nodes = append(nodes, 10, 2, 10, 0)
	}
	cases := []struct {
		name string
		data []byte
		code string
	}{
		{"duplicate scalar", append([]byte{8, byte(resourcev2.WireVersion)}, valid...), "INVALID_REQUEST"},
		{"different oneof", requestValue([]byte{16, 1, 26, 0}), "INVALID_REQUEST"},
		{"repeated message oneof", requestValue([]byte{10, 0, 10, 0}), "INVALID_REQUEST"},
		{"timestamp duplicate", requestValue(ts), "INVALID_REQUEST"},
		{"map duplicate", requestValue(duplicateMap), "INVALID_REQUEST"},
		{"unknown outer", append(valid, 120, 1), "UNSUPPORTED"},
		{"unknown nested", requestValue([]byte{120, 1}), "UNSUPPORTED"},
		{"wrong wire", []byte{10, 0}, "INVALID_REQUEST"},
		{"truncated length", []byte{8, byte(resourcev2.WireVersion), 82, 5, 1}, "INVALID_REQUEST"},
		{"truncated varint", []byte{8, 128}, "INVALID_REQUEST"},
		{"group", []byte{11, 12}, "INVALID_REQUEST"},
		{"utf8", append([]byte{8, byte(resourcev2.WireVersion)}, bytesField(10, bytesField(1, bytesField(1, []byte{255})))...), "INVALID_REQUEST"},
		{"depth", requestValue(deep), "RESOURCE_LIMIT"},
		{"fields", fieldRequest, "RESOURCE_LIMIT"},
		{"nodes", requestValue(bytesField(8, nodes)), "RESOURCE_LIMIT"},
		{"version", append([]byte{8, 2}, valid[2:]...), "UNSUPPORTED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &countProvider{}
			s := grpc.NewServer()
			RegisterServer(s, p)
			c := pipe(t, s)
			var out resourcepb.Response
			err := c.Invoke(
				context.Background(),
				resourcepb.ResourceProvider_Call_FullMethodName,
				&resourcepb.Request{},
				&out,
				grpc.ForceCodecV2(rawCodec{data: tc.data}),
				grpc.MaxCallSendMsgSize(MaxWireBytes),
			)
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
		if err := c.Invoke(
			context.Background(),
			resourcepb.ResourceProvider_Call_FullMethodName,
			&resourcepb.Request{},
			&out,
			grpc.ForceCodecV2(rawCodec{data: valid}),
		); err != nil {
			t.Fatal(err)
		}
		if p.calls.Load() != 1 {
			t.Fatal("valid request did not dispatch")
		}
	})
}

func TestRawResponseAdmission(t *testing.T) {
	cases := make([][]byte, 0, 10)
	cases = append(cases,
		[]byte{8, byte(resourcev2.WireVersion), 8, byte(resourcev2.WireVersion)},
		[]byte{8, byte(resourcev2.WireVersion), 120, 1},
		append([]byte{8, byte(resourcev2.WireVersion)}, bytesField(5, []byte{16, 1, 16, 1})...),
		[]byte{8, byte(resourcev2.WireVersion), 42, 128},
	)
	entry := append(bytesField(1, []byte("k")), bytesField(2, []byte{10, 0})...)
	for _, value := range [][]byte{{10, 0, 10, 0}, {16, 1, 26, 0}, bytesField(7, []byte{16, 0, 16, 0}), bytesField(9, append(bytesField(1, entry), bytesField(1, entry)...)), {26, 1, 255}, {16, 128}} {
		cases = append(cases, append([]byte{8, byte(resourcev2.WireVersion)}, bytesField(5, value)...))
	}
	for _, data := range cases {
		s := grpc.NewServer(grpc.ForceServerCodecV2(rawCodec{data: data}))
		RegisterServer(s, &countProvider{})
		c := NewClient(pipe(t, s))
		c.featureMode.Store(1) // This fixture supplies raw responses, not a feature RPC.
		_, err := c.call(context.Background(), &resourcepb.Request{
			Operation: &resourcepb.Request_CatalogRequest{CatalogRequest: &resourcepb.CatalogRequest{Route: &resourcepb.AccessRoute{Connection: "c"}}},
		})
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
	first := append(bytesField(1, []byte("a")), bytesField(2, []byte{10, 0})...)
	second := append(bytesField(1, []byte("b")), bytesField(2, []byte{26, 0})...)
	values := [][]byte{
		{10, 0},
		{66, 0},
		{74, 0},
		bytesField(7, nil),
		bytesField(8, append(bytesField(1, []byte{16, 1}), bytesField(1, []byte{26, 0})...)),
		bytesField(9, append(bytesField(1, first), bytesField(1, second)...)),
	}
	for _, data := range values {
		if err := ValidateWire(data, (&resourcepb.Value{}).ProtoReflect().Descriptor()); err != nil {
			t.Fatal(err)
		}
		var v resourcepb.Value
		if err := proto.Unmarshal(data, &v); err != nil {
			t.Fatal(err)
		}
		payload := append([]byte{8, byte(resourcev2.WireVersion)}, bytesField(5, data)...)
		s := grpc.NewServer(grpc.ForceServerCodecV2(rawCodec{data: payload}))
		RegisterServer(s, &countProvider{})
		c := NewClient(pipe(t, s))
		c.featureMode.Store(1) // This fixture supplies raw responses, not a feature RPC.
		out, err := c.call(
			context.Background(),
			&resourcepb.Request{
				Operation: &resourcepb.Request_CatalogRequest{CatalogRequest: &resourcepb.CatalogRequest{Route: &resourcepb.AccessRoute{Connection: "c"}}},
			},
		)
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

type mixedAuthorityProvider struct{ catalog resourcev2.Catalog }

func (p *mixedAuthorityProvider) Catalog(context.Context, resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
	return p.catalog, nil
}

func TestActualGRPCMixedCatalogAuthorityHintIsolation(t *testing.T) {
	cluster := &resourcev2.NativeAuthorityHint{
		Scheme:     "kubernetes.cluster.v1",
		ID:         "cluster/opaque",
		Continuity: resourcev2.ContinuityStableNative,
	}
	helm := &resourcev2.NativeAuthorityHint{
		Scheme:     "helm.local.v1",
		ID:         "helm/opaque",
		Continuity: resourcev2.ContinuityConnectionLocal,
	}
	descriptor := func(name string, hint *resourcev2.NativeAuthorityHint) resourcev2.ResourceType {
		return resourcev2.ResourceType{
			Type:                name,
			IdentityDimensions:  []resourcev2.Dimension{},
			QueryDimensions:     []resourcev2.Dimension{},
			Representations:     []resourcev2.Representation{testRepresentation()},
			Capabilities:        []resourcev2.Capability{},
			NativeAuthorityHint: hint,
		}
	}
	want := resourcev2.Catalog{
		Provider: "mixed", Revision: "mixed-1", Coverage: resourcev2.Coverage{Kind: "complete", Paths: []string{}},
		Types: []resourcev2.ResourceType{
			descriptor("cluster/pod", cluster),
			descriptor("localHelm/release", helm),
			descriptor("unknown/backing", nil),
			descriptor("cluster/service", cluster),
		},
	}
	s := grpc.NewServer()
	RegisterServer(s, &mixedAuthorityProvider{catalog: want})
	client := NewClient(pipe(t, s))
	got, err := client.Catalog(
		context.Background(),
		resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: "mixed-route"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mixed catalog changed across gRPC:\ngot %#v\nwant %#v", got, want)
	}
	if got.Types[2].NativeAuthorityHint != nil {
		t.Fatal("unhinted descriptor inherited another type's backing")
	}
	got.Types[0].NativeAuthorityHint.ID = "changed"
	if got.Types[1].NativeAuthorityHint.ID != helm.ID || got.Types[3].NativeAuthorityHint.ID != cluster.ID {
		t.Fatal("decoded descriptors share hint storage")
	}
	if cluster.ID != "cluster/opaque" {
		t.Fatal("decoded hint aliases provider storage")
	}
	again, err := client.Catalog(
		context.Background(),
		resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: "mixed-route"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, want) {
		t.Fatal("client mutation leaked into a later catalog response")
	}
}

func TestRawRetiredCatalogAuthorityHintRejected(t *testing.T) {
	// Catalog field 4 belonged to the retired catalog-wide hint. The current
	// strict version table must reject it, including an empty hint payload.
	for _, hint := range [][]byte{nil, append(bytesField(1, []byte("legacy.v1")), bytesField(2, []byte("legacy-id"))...)} {
		catalog := bytesField(4, hint)
		data := append([]byte{8, byte(resourcev2.WireVersion)}, bytesField(2, catalog)...)
		s := grpc.NewServer(grpc.ForceServerCodecV2(rawCodec{data: data}))
		RegisterServer(s, &countProvider{})
		client := NewClient(pipe(t, s))
		_, err := client.Catalog(
			context.Background(),
			resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: "c"}},
		)
		if resourcev2.ErrorCode(err) != "UNSUPPORTED" {
			t.Fatalf("retired catalog field 4 class %s, want UNSUPPORTED: %v", resourcev2.ErrorCode(err), err)
		}
	}
}

func TestActualGRPCCatalogEnvironmentIsPerType(t *testing.T) {
	cluster := []resourcev2.EnvironmentIdentity{
		{Scheme: "kubernetes.namespace-uid.v1", ID: "cluster-uid", Strength: resourcev2.EnvironmentStrong, Exclusive: true},
		{Scheme: "kubernetes.root-ca.v1", ID: "ca-1", Strength: resourcev2.EnvironmentStrong},
		{Scheme: "aws.eks-cluster-arn.v1", ID: "arn:aws:eks:us-east-1:111122223333:cluster/c", Strength: resourcev2.EnvironmentWeak},
	}
	descriptor := func(name string, environment []resourcev2.EnvironmentIdentity) resourcev2.ResourceType {
		return resourcev2.ResourceType{
			Type: name, IdentityDimensions: []resourcev2.Dimension{}, QueryDimensions: []resourcev2.Dimension{},
			Representations: []resourcev2.Representation{testRepresentation()}, Capabilities: []resourcev2.Capability{},
			Environment: environment,
		}
	}
	want := resourcev2.Catalog{
		Provider: "mixed", Revision: "mixed-1", Coverage: resourcev2.Coverage{Kind: "complete", Paths: []string{}},
		Types: []resourcev2.ResourceType{descriptor("cluster/pod", cluster), descriptor("localHelm/chart", nil), descriptor("cluster/service", cluster)},
	}
	s := grpc.NewServer()
	RegisterServer(s, &mixedAuthorityProvider{catalog: want})
	client := NewClient(pipe(t, s))
	got, err := client.Catalog(context.Background(), resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: "route"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("catalog changed across gRPC:\ngot %#v\nwant %#v", got, want)
	}
	if got.Types[1].Environment != nil {
		t.Fatal("a type without a declared environment inherited its neighbours'")
	}
	got.Types[0].Environment[0].ID = "changed"
	if got.Types[2].Environment[0].ID != "cluster-uid" || cluster[0].ID != "cluster-uid" {
		t.Fatal("decoded environments share storage")
	}
}

// rawCatalogWithType wraps one encoded ResourceType in an otherwise valid
// catalog response, so a test controls exactly the bytes of that type.
func rawCatalogWithType(resourceType []byte) []byte {
	coverage := bytesField(1, []byte("complete"))
	catalog := bytesField(1, []byte("p"))
	catalog = append(catalog, bytesField(2, resourceType)...)
	catalog = append(catalog, bytesField(3, []byte("r"))...)
	catalog = append(catalog, bytesField(5, coverage)...)
	return append([]byte{8, byte(resourcev2.WireVersion)}, bytesField(2, catalog)...)
}

// rawEnvironment encodes one EnvironmentIdentity message, so a test controls its exact bytes.
func rawEnvironment(scheme, id, strength string, exclusive bool, extra ...[]byte) []byte {
	entry := append(bytesField(1, []byte(scheme)), bytesField(2, []byte(id))...)
	if strength != "" {
		entry = append(entry, bytesField(3, []byte(strength))...)
	}
	if exclusive {
		entry = protowire.AppendVarint(protowire.AppendTag(entry, 4, protowire.VarintType), 1)
	}
	for _, field := range extra {
		entry = append(entry, field...)
	}
	return entry
}

func TestRawCatalogEnvironmentFieldAdmission(t *testing.T) {
	uid := rawEnvironment("kubernetes.namespace-uid.v1", "cluster-uid", "strong", true)
	ca := rawEnvironment("kubernetes.root-ca.v1", "ca-1", "strong", false)
	base := bytesField(1, []byte("cluster/pod"))
	typeWith := func(entries ...[]byte) []byte {
		out := append([]byte(nil), base...)
		for _, entry := range entries {
			out = append(out, bytesField(13, entry)...)
		}
		return out
	}
	many := make([][]byte, resourcev2.MaxEnvironmentIdentities+1)
	for i := range many {
		many[i] = rawEnvironment("s.v1", string(rune('a'+i)), "strong", false)
	}
	// The strict admission that rejects an unknown field is what makes a provider
	// that declares an environment unreadable to a receiver built before it
	// existed: that receiver sees field 13 exactly as this one sees field 99.
	cases := []struct {
		name         string
		resourceType []byte
		class        string
		count        int
	}{
		{"declared set", typeWith(uid, ca), "", 2},
		{"absent", base, "", 0},
		{"same fingerprint twice", typeWith(ca, ca), "INVALID_REQUEST", 0},
		{"unknown outer field", append(append([]byte(nil), base...), bytesField(99, ca)...), "UNSUPPORTED", 0},
		{"empty entry", typeWith(nil), "INVALID_REQUEST", 0},
		{"missing strength", typeWith(rawEnvironment("s.v1", "x", "", false)), "INVALID_REQUEST", 0},
		{"unknown strength", typeWith(rawEnvironment("s.v1", "x", "medium", false)), "INVALID_REQUEST", 0},
		{"weak and exclusive", typeWith(rawEnvironment("s.v1", "x", "weak", true)), "INVALID_REQUEST", 0},
		{"two values of an exclusive scheme", typeWith(rawEnvironment("s.v1", "a", "strong", true), rawEnvironment("s.v1", "b", "strong", true)), "INVALID_REQUEST", 0},
		{"unknown inner field", typeWith(rawEnvironment("s.v1", "x", "strong", false, bytesField(9, []byte("x")))), "UNSUPPORTED", 0},
		{"repeated singular field", typeWith(rawEnvironment("s.v1", "x", "strong", false, bytesField(2, []byte("y")))), "INVALID_REQUEST", 0},
		{"too many fingerprints", typeWith(many...), "RESOURCE_LIMIT", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := grpc.NewServer(grpc.ForceServerCodecV2(rawCodec{data: rawCatalogWithType(tc.resourceType)}))
			RegisterServer(s, &countProvider{})
			client := NewClient(pipe(t, s))
			client.featureMode.Store(1) // This fixture supplies raw responses, not a feature RPC.
			catalog, err := client.Catalog(context.Background(), resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: "c"}})
			if got := resourcev2.ErrorCode(err); got != tc.class {
				t.Fatalf("class %q, want %q: %v", got, tc.class, err)
			}
			if err == nil && len(catalog.Types[0].Environment) != tc.count {
				t.Fatalf("decoded %d fingerprints, want %d", len(catalog.Types[0].Environment), tc.count)
			}
		})
	}
}
