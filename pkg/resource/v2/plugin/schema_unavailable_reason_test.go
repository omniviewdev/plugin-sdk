package plugin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/mem"
	"google.golang.org/protobuf/proto"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

type nativeSchemaReasonProvider struct {
	qualifiedProvider

	endpoint string
}

func (p *nativeSchemaReasonProvider) ResolveSchema(
	ctx context.Context,
	in resourcev2.SchemaRequest,
) (resourcev2.SchemaBinding, error) {
	path := "/native"
	if in.Target.Auxiliary != nil {
		path = "/helm"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint+path, nil)
	if err != nil {
		return resourcev2.SchemaBinding{}, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return resourcev2.SchemaBinding{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusForbidden {
		reason := "FORBIDDEN"
		return resourcev2.SchemaBinding{State: "unavailable", UnavailableReason: &reason}, nil
	}
	if response.StatusCode != http.StatusOK {
		return resourcev2.SchemaBinding{}, resourcev2.NewError("PROVIDER_ERROR", "Unexpected native schema status.")
	}
	document, err := io.ReadAll(response.Body)
	if err != nil {
		return resourcev2.SchemaBinding{}, err
	}
	revision := "native-schema-v1"
	return resourcev2.SchemaBinding{
		State:    "available",
		Revision: &revision,
		Dialect:  "https://json-schema.org/draft/2020-12/schema",
		Document: document,
	}, nil
}

func TestActualRPCSchemaUnavailableReasonIsTargeted(t *testing.T) {
	requests := make(chan string, 3)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Path
		if r.URL.Path == "/helm" {
			http.Error(w, "sensitive native diagnostic must not cross wire", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{"type":"object","properties":{"metadata":{"type":"object"}}}`); err != nil {
			t.Errorf("write schema response: %v", err)
		}
	}))
	defer endpoint.Close()
	provider := &nativeSchemaReasonProvider{endpoint: endpoint.URL}
	server := grpc.NewServer()
	RegisterServer(server, provider)
	client := NewClient(pipe(t, server))
	route := resourcev2.AccessRoute{Connection: "explicit"}
	ctx := acceptedCatalogContext(context.Background(), t, client, route)
	native, err := client.ResolveSchema(ctx, schemaFixtureRequest())
	if err != nil || native.State != "available" || !strings.Contains(string(native.Document), "metadata") {
		t.Fatalf("actual native document was not retained: %+v %v", native, err)
	}
	if path := <-requests; path != "/native" || len(requests) != 0 {
		t.Fatalf("native demand fetched unrelated schemas: %q, remaining %d", path, len(requests))
	}
	auxiliary := schemaFixtureRequest()
	auxiliary.Target = resourcev2.SchemaTarget{Auxiliary: &resourcev2.AuxiliarySchemaTarget{
		Key: "helm-values", Reference: testGet().Reference,
	}}
	binding, err := client.ResolveSchema(ctx, auxiliary)
	if err != nil {
		t.Fatal(err)
	}
	if binding.State != "unavailable" || binding.UnavailableReason == nil ||
		*binding.UnavailableReason != "FORBIDDEN" ||
		len(binding.Document) != 0 {
		t.Fatalf("unavailable role misrepresented: %+v", binding)
	}
	if path := <-requests; path != "/helm" || len(requests) != 0 {
		t.Fatalf("auxiliary demand fetched unrelated schemas: %q, remaining %d", path, len(requests))
	}
	output, err := json.Marshal(binding)
	if err != nil || strings.Contains(string(output), "sensitive") ||
		!strings.Contains(string(output), `"unavailableReason":"FORBIDDEN"`) {
		t.Fatalf("unsafe/unobservable availability reason: %s %v", output, err)
	}
}

func TestSchemaUnavailableReasonStrictAdmissionBeforeDecode(t *testing.T) {
	cases := []struct{ state, reason string }{
		{
			"available",
			"FORBIDDEN",
		},
		{"absent", "FORBIDDEN"},
		{"unavailable", ""},
		{"unavailable", "forbidden"},
		{"unavailable", "FORBIDDEN secret"},
		{"unavailable", "https://credential.invalid"},
		{"unavailable", strings.Repeat("A", 65)},
		{"unavailable", "UNREVIEWED_CODE"},
		{"unavailable", "FORBIDDEN_é"},
	}
	for _, tc := range cases {
		t.Run(tc.state+"/"+tc.reason, func(t *testing.T) {
			reason := tc.reason
			wire := &resourcepb.SchemaBinding{State: tc.state, UnavailableReason: &reason}
			data, err := proto.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			decoded := new(resourcepb.SchemaBinding)
			envelope := NewTransportEnvelope(decoded, false)
			if err := encoding.GetCodecV2("proto").
				Unmarshal(mem.BufferSlice{mem.SliceBuffer(data)}, envelope); err != nil {
				t.Fatal(err)
			}
			if resourcev2.ErrorCode(envelope.AdmissionError()) != "INVALID_REQUEST" || decoded.State != "" {
				t.Fatalf("invalid schema materialized before admission: %+v %v", decoded, envelope.AdmissionError())
			}
			value := resourcev2.SchemaBinding{
				State:             tc.state,
				UnavailableReason: &reason,
			}
			if _, err := resourcev2.ToProtoSchemaBinding(value); resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
				t.Fatalf("native invalid reason admitted: %v", err)
			}
			input, _ := json.Marshal(
				map[string]any{
					"state":             tc.state,
					"unavailableReason": tc.reason,
				},
			)
			var binding resourcev2.SchemaBinding
			if err := json.Unmarshal(input, &binding); resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
				t.Fatalf("invalid JSON schema reason admitted: %s %v", input, err)
			}
		})
	}
	var absent resourcev2.SchemaBinding
	if err := json.Unmarshal(
		[]byte(`{"state":"unavailable"}`),
		&absent,
	); err != nil ||
		absent.UnavailableReason != nil {
		t.Fatalf("absent reason synthesized: %+v %v", absent, err)
	}
	if err := json.Unmarshal(
		[]byte(`{"state":"unavailable","unavailableReason":null}`),
		&absent,
	); resourcev2.ErrorCode(
		err,
	) != "INVALID_REQUEST" {
		t.Fatalf("explicit null reason admitted: %v", err)
	}
}
