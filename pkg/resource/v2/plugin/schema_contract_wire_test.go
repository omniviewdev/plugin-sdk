package plugin

import (
	"context"
	"testing"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func TestSchemaContractRejectsRetiredWireFields(t *testing.T) {
	format := append(bytesField(1, []byte("document")), bytesField(2, []byte("format-1"))...)
	for _, retired := range [][]byte{
		bytesField(3, bytesField(1, []byte("absent"))),
		bytesField(3, []byte("old-schema-revision")),
	} {
		data := append(append([]byte(nil), format...), retired...)
		if err := ValidateWire(data, (&resourcepb.Representation{}).ProtoReflect().Descriptor()); resourcev2.ErrorCode(err) != "UNSUPPORTED" {
			t.Fatalf("retired representation field admitted: %v", err)
		}
		if err := ValidateWire(bytesField(3, data), (&resourcepb.SourceRecord{}).ProtoReflect().Descriptor()); resourcev2.ErrorCode(err) != "UNSUPPORTED" {
			t.Fatalf("retired compact format field admitted: %v", err)
		}
	}
	for _, tag := range []protowire.Number{5, 6} {
		data := append(bytesField(1, []byte("absent")), bytesField(tag, []byte("retired-claim"))...)
		if err := ValidateWire(data, (&resourcepb.SchemaBinding{}).ProtoReflect().Descriptor()); resourcev2.ErrorCode(err) != "UNSUPPORTED" {
			t.Fatalf("retired schema claim %d admitted: %v", tag, err)
		}
	}
	oldList := append([]byte{8, byte(resourcev2.WireVersion)}, bytesField(8, nil)...)
	if err := ValidateWire(oldList, (&resourcepb.AncillaryResponse{}).ProtoReflect().Descriptor()); resourcev2.ErrorCode(err) != "UNSUPPORTED" {
		t.Fatalf("retired editor list admitted: %v", err)
	}
}

func TestSchemaContractRequiresOneExactWireTarget(t *testing.T) {
	request := resourcev2.SchemaRequest{
		Route:  resourcev2.AccessRoute{Connection: "explicit"},
		Type:   "thing",
		Target: resourcev2.SchemaTarget{Representation: &resourcev2.Representation{ID: "document", Revision: "format-1"}},
	}
	wire, err := resourcev2.ToProtoSchemaRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	data, err := proto.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateWire(data, wire.ProtoReflect().Descriptor()); err != nil {
		t.Fatalf("exact target rejected: %v", err)
	}
	format, err := proto.Marshal(wire.Target.GetRepresentation())
	if err != nil {
		t.Fatal(err)
	}
	selected := bytesField(1, format)
	for _, target := range [][]byte{
		nil,
		append(append([]byte(nil), selected...), selected...),
		append(append([]byte(nil), selected...), bytesField(2, nil)...),
		bytesField(1, bytesField(1, []byte("document"))),
	} {
		if err := ValidateWire(target, (&resourcepb.SchemaTarget{}).ProtoReflect().Descriptor()); resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
			t.Fatalf("invalid target admitted: %x %v", target, err)
		}
	}
	for _, malformed := range [][]byte{
		bytesField(1, bytesField(1, []byte("explicit"))),
		bytesField(2, nil),
	} {
		if err := ValidateWire(malformed, (&resourcepb.SchemaRequest{}).ProtoReflect().Descriptor()); resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
			t.Fatalf("incomplete target request admitted: %v", err)
		}
	}
}

type refreshCaptureProvider struct {
	countProvider
	requests chan resourcev2.CatalogRequest
}

func (p *refreshCaptureProvider) Catalog(ctx context.Context, request resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
	select {
	case p.requests <- request:
	case <-ctx.Done():
		return resourcev2.Catalog{}, ctx.Err()
	}
	return p.countProvider.Catalog(ctx, request)
}

func TestCatalogRefreshIntentSurvivesActualTransport(t *testing.T) {
	provider := &refreshCaptureProvider{requests: make(chan resourcev2.CatalogRequest, 2)}
	server := grpc.NewServer()
	RegisterServer(server, provider)
	client := NewClient(pipe(t, server))
	for _, refresh := range []bool{false, true} {
		request := resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: "explicit"}, Refresh: refresh}
		if _, err := client.Catalog(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		if got := <-provider.requests; got != request {
			t.Fatalf("catalog request changed: got %+v, want %+v", got, request)
		}
	}
}

func TestIncompatibleResourceCohortRejectsBeforeCatalogDispatch(t *testing.T) {
	oldCatalog := bytesField(2, bytesField(1, []byte("explicit")))
	for _, version := range []byte{2, byte(resourcev2.WireVersion)} {
		provider := &countProvider{}
		server := grpc.NewServer()
		RegisterServer(server, provider)
		connection := pipe(t, server)
		data := append([]byte{8, version}, oldCatalog...)
		var response resourcepb.Response
		err := connection.Invoke(context.Background(), resourcepb.ResourceProvider_Call_FullMethodName,
			&resourcepb.Request{}, &response, grpc.ForceCodecV2(rawCodec{data: data}))
		if resourcev2.ErrorCode(domainError(err)) != "UNSUPPORTED" || provider.calls.Load() != 0 {
			t.Fatalf("incompatible catalog reached provider: version=%d calls=%d error=%v", version, provider.calls.Load(), err)
		}
	}
}

func TestSchemaContractRejectsUnqualifiedRawDemand(t *testing.T) {
	data := append([]byte{8, byte(resourcev2.WireVersion)}, bytesField(2, []byte("resolveSchema"))...)
	if err := ValidateWire(data, (&resourcepb.AncillaryRequest{}).ProtoReflect().Descriptor()); resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("unqualified schema demand admitted: %v", err)
	}
	reference, err := resourcev2.ToProtoResourceReference(testGet().Reference)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := proto.Marshal(reference)
	if err != nil {
		t.Fatal(err)
	}
	auxiliary := append(bytesField(1, []byte("values")), bytesField(2, encoded)...)
	auxiliary = append(auxiliary, bytesField(3, nil)...)
	if err := ValidateWire(auxiliary, (&resourcepb.AuxiliarySchemaTarget{}).ProtoReflect().Descriptor()); resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("present-empty source revision admitted: %v", err)
	}
}
