package plugin

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

// S0 (SDK0-S0-dual-read.md): one reader, two resource wires. Wire 4 adds a REQUIRED
// member (ResourceType.environment, field 13) to the strict lane. A host that reads
// both wires must (a) accept a wire-3 catalog that has none, (b) refuse a wire-4
// catalog that has none, (c) still reject field 13 on a wire-3 message exactly as a
// wire-3 reader would (unknown field), and (d) a plugin must keep refusing wire 3.

func catalogResponse(wire uint32, environment string) *resourcepb.Response {
	return &resourcepb.Response{Version: wire, Result: &resourcepb.Response_Catalog{Catalog: &resourcepb.Catalog{
		Provider: "p", Revision: "r",
		Coverage: &resourcepb.Coverage{Kind: "complete"},
		Types: []*resourcepb.ResourceType{{
			Type: "t", DisplayName: "T", Environment: environment,
			Representations: []*resourcepb.Representation{{Id: "native", Revision: "1"}},
		}},
	}}}
}

func withFloor(t *testing.T, floor uint32) {
	t.Helper()
	if err := SetReadWireFloor(floor); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetReadWireFloor(resourcev2.WireVersion) })
}

func admit(t *testing.T, m proto.Message) error {
	t.Helper()
	data, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return ValidateWire(data, m.ProtoReflect().Descriptor())
}

func TestDualReadAdmitsEachWireAsItsOwnDescriptorTree(t *testing.T) {
	withFloor(t, resourcev2.FloorWireVersion)

	// A wire-3 catalog as the v0.20.0 SDK writes it: no field 13.
	if err := admit(t, catalogResponse(3, "")); err != nil {
		t.Fatalf("wire 3 without the new member must be admitted: %v", err)
	}
	// Field 13 on a wire-3 message is unknown to a wire-3 reader and stays unknown here.
	err := admit(t, catalogResponse(3, "embedded"))
	if resourcev2.ErrorCode(err) != "UNSUPPORTED" {
		t.Fatalf("field 13 on wire 3 must be UNSUPPORTED (unknown field), got %v", err)
	}
	// Wire 4 may carry it.
	if err := admit(t, catalogResponse(4, "embedded")); err != nil {
		t.Fatalf("wire 4 with the member: %v", err)
	}
	// Outside the floor and ceiling: refused as a coordinated-cohort problem, as today.
	for _, wire := range []uint32{0, 2, 5} {
		if err := admit(t, catalogResponse(wire, "")); resourcev2.ErrorCode(err) != "UNSUPPORTED" {
			t.Fatalf("wire %d must be UNSUPPORTED, got %v", wire, err)
		}
	}
}

func TestDualReadRequiredMemberIsRequiredOnlyFromWire4(t *testing.T) {
	// Decoding a wire-3 peer's catalog: absent is fine, and the domain value is empty.
	cat3, err := resourcev2.FromProtoCatalogWire(catalogResponse(3, "").GetCatalog(), 3)
	if err != nil || cat3.Types[0].Environment != "" {
		t.Fatalf("wire 3 decode: %+v %v", cat3, err)
	}
	// A wire-4 peer that omits it is refused by the decoder.
	if _, err = resourcev2.FromProtoCatalogWire(catalogResponse(4, "").GetCatalog(), 4); err == nil {
		t.Fatal("a wire-4 catalog without the required member must be refused")
	}
	// And the producer (always wire 4) refuses to write a type without it.
	if _, err = resourcev2.ToProtoCatalog(resourcev2.Catalog{
		Provider: "p", Revision: "r", Coverage: resourcev2.Coverage{Kind: "complete"},
		Types: []resourcev2.ResourceType{{Type: "t", DisplayName: "T", Representations: []resourcev2.Representation{{ID: "native", Revision: "1"}}}},
	}); err == nil {
		t.Fatal("the producer must not write a wire-4 type without the required member")
	}
}

func TestPluginKeepsRefusingWire3ByDefault(t *testing.T) {
	// No SetReadWireFloor: this is every plugin built on the SDK. It reads exactly its own wire.
	err := admit(t, catalogResponse(3, ""))
	if resourcev2.ErrorCode(err) != "UNSUPPORTED" {
		t.Fatalf("a plugin must refuse wire 3 unless a host lowered the floor: %v", err)
	}
	if err = SetReadWireFloor(2); resourcev2.ErrorCode(err) != "UNSUPPORTED" {
		t.Fatalf("the floor may not go below what the SDK can read: %v", err)
	}
	if err = SetReadWireFloor(resourcev2.WireVersion + 1); resourcev2.ErrorCode(err) != "UNSUPPORTED" {
		t.Fatalf("the floor may not go above the current wire: %v", err)
	}
}

// legacyCatalogPlugin answers like a v0.20.0/v0.21.0 plugin: it only knows wire 3.
type legacyCatalogPlugin struct {
	resourcepb.UnimplementedResourceProviderServer
}

func (legacyCatalogPlugin) Call(_ context.Context, r *resourcepb.Request) (*resourcepb.Response, error) {
	if r.GetVersion() != 3 {
		return nil, rpcError(unsupported("unsupported resource wire version"))
	}
	if r.GetFeatures() != nil {
		return &resourcepb.Response{Version: 3, Result: &resourcepb.Response_Features{Features: &resourcepb.FeatureSet{}}}, nil
	}
	return catalogResponse(3, ""), nil
}

func TestHostClientOnWire3ReadsAWire3PluginAndWire4ClientIsRefusedByIt(t *testing.T) {
	withFloor(t, resourcev2.FloorWireVersion)
	s := grpc.NewServer()
	resourcepb.RegisterResourceProviderServer(s, legacyCatalogPlugin{})
	conn := pipe(t, s)
	request := resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: "c"}}

	// The host picked the peer's wire from the go-plugin protocol it negotiated: 1 means wire 3.
	catalog, err := NewClientWire(conn, 3).Catalog(context.Background(), request)
	if err != nil {
		t.Fatalf("wire-3 client against a wire-3 plugin: %v", err)
	}
	if len(catalog.Types) != 1 || catalog.Types[0].Environment != "" {
		t.Fatalf("decoded: %+v", catalog)
	}
	// The same plugin and a client that picked the wrong wire: the plugin's own strict reader says no.
	_, err = NewClientWire(conn, 4).Catalog(context.Background(), request)
	if resourcev2.ErrorCode(err) != "UNSUPPORTED" {
		t.Fatalf("wrong wire must come back as a typed UNSUPPORTED, got %v", err)
	}
}
