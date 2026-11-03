package plugin

import (
	"context"
	"testing"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/mem"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestLegacySchemaClaimsAdmission(t *testing.T) {
	field := func(n protowire.Number, value string) []byte {
		return protowire.AppendString(protowire.AppendTag(nil, n, protowire.BytesType), value)
	}
	base := field(1, "absent")
	claims := append(field(5, "validated"), field(6, "/private")...)
	valid := append(append([]byte{}, base...), claims...)
	decode := func(data []byte, legacy bool) (*resourcepb.SchemaBinding, error) {
		message := &resourcepb.SchemaBinding{}
		env := &envelope{message: message, legacySchemaClaims: legacy}
		if err := encoding.GetCodecV2("proto").Unmarshal(mem.BufferSlice{mem.SliceBuffer(data)}, env); err != nil {
			t.Fatal(err)
		}
		return message, env.err
	}
	message, err := decode(valid, true)
	if err != nil || message.State != "absent" || len(message.ProtoReflect().GetUnknown()) != 0 {
		t.Fatalf("legacy claims: %v, %v", message, err)
	}
	if _, err = decode(valid, false); resourcev2.ErrorCode(err) != "UNSUPPORTED" {
		t.Fatalf("strict decoder admitted claims: %v", err)
	}
	unknown := append(append([]byte{}, valid...), field(10, "unknown")...)
	if _, err = decode(unknown, true); resourcev2.ErrorCode(err) != "UNSUPPORTED" {
		t.Fatalf("unknown field accepted: %v", err)
	}
	for _, data := range [][]byte{
		append(append([]byte{}, valid...), field(5, "duplicate")...),
		append(append([]byte{}, base...), protowire.AppendVarint(protowire.AppendTag(nil, 5, protowire.VarintType), 1)...),
		append(append([]byte{}, base...), field(6, "\xff")...),
	} {
		if _, err = decode(data, true); resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
			t.Fatalf("malformed legacy claims accepted: %v", err)
		}
	}
}

type legacyClaimsServer struct {
	resourcepb.UnimplementedResourceProviderServer
	sourceSessions bool
	unknown bool
}

func (s *legacyClaimsServer) Call(ctx context.Context, request *resourcepb.Request) (*resourcepb.Response, error) {
	if request.GetFeatures() != nil {
		var traits []string
		if s.sourceSessions {
			traits = []string{"sourceSessions"}
		}
		return &resourcepb.Response{Version: 2, Result: &resourcepb.Response_Features{Features: &resourcepb.FeatureSet{Traits: traits}}}, nil
	}
	catalog, err := (&qualifiedProvider{}).Catalog(ctx, resourcev2.CatalogRequest{})
	if err != nil {
		return nil, err
	}
	wire, err := resourcev2.ToProtoCatalog(catalog)
	if err != nil {
		return nil, err
	}
	number := protowire.Number(5)
	if s.unknown {
		number = 10
	}
	claim := protowire.AppendString(protowire.AppendTag(nil, number, protowire.BytesType), "claim")
	wire.Types[0].Representations[0].Schema.ProtoReflect().SetUnknown(claim)
	return &resourcepb.Response{Version: 2, Result: &resourcepb.Response_Catalog{Catalog: wire}}, nil
}

func TestNegotiatedLegacyCatalogClaims(t *testing.T) {
	for _, tc := range []struct {
		name string
		sourceSessions, unknown bool
		code string
	}{
		{name: "legacy"},
		{name: "current", sourceSessions: true, code: "UNSUPPORTED"},
		{name: "unknown", unknown: true, code: "UNSUPPORTED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := grpc.NewServer()
			resourcepb.RegisterResourceProviderServer(server, &legacyClaimsServer{sourceSessions: tc.sourceSessions, unknown: tc.unknown})
			client := NewClient(pipe(t, server))
			catalog, err := client.Catalog(context.Background(), resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: "explicit"}})
			if tc.code != "" {
				if resourcev2.ErrorCode(err) != tc.code {
					t.Fatalf("catalog error = %v, want %s", err, tc.code)
				}
				return
			}
			if err != nil || catalog.Types[0].Representations[0].Schema.State != "absent" {
				t.Fatalf("legacy catalog = %+v, %v", catalog, err)
			}
		})
	}
}
