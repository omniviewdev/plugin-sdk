package plugin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/mem"
	"google.golang.org/protobuf/proto"
)

type nativeSchemaReasonProvider struct {
	qualifiedProvider
	endpoint string
}

func (p *nativeSchemaReasonProvider) GetEditorSchemas(ctx context.Context, _ resourcev2.AccessRoute) ([]resourcev2.EditorSchema, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint+"/native", nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	document, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		return nil, err
	}
	revision := "native-schema-v1"
	native := resourcev2.EditorSchema{Type: "native:pod", FileMatch: "*.yaml", URI: "schema://native/pod", Language: "yaml", Binding: resourcev2.SchemaBinding{State: "available", Revision: &revision, Dialect: "https://json-schema.org/draft/2020-12/schema", Document: document, Validation: "none", Unvalidated: []string{}}}
	request, err = http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint+"/helm", nil)
	if err != nil {
		return nil, err
	}
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		return nil, resourcev2.NewError("PROVIDER_ERROR", "Unexpected native schema status.")
	}
	reason := "FORBIDDEN"
	helm := resourcev2.EditorSchema{Type: "helm:values", FileMatch: "values.yaml", URI: "schema://helm/values", Language: "yaml", Binding: resourcev2.SchemaBinding{State: "unavailable", Validation: "none", Unvalidated: []string{}, UnavailableReason: &reason}}
	return []resourcev2.EditorSchema{native, helm}, nil
}

func TestActualRPCSchemaUnavailableReasonRetainsNativeSchemas(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/helm" {
			http.Error(w, "sensitive native diagnostic must not cross wire", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"type":"object","properties":{"metadata":{"type":"object"}}}`)
	}))
	defer endpoint.Close()
	provider := &nativeSchemaReasonProvider{endpoint: endpoint.URL}
	server := grpc.NewServer()
	RegisterServer(server, provider)
	client := NewClient(pipe(t, server))
	route := resourcev2.AccessRoute{Connection: "explicit"}
	ctx := acceptedCatalogContext(t, context.Background(), client, route)
	schemas, err := client.GetEditorSchemas(ctx, route)
	if err != nil || len(schemas) != 2 {
		t.Fatalf("native schemas lost after partial denial: %+v %v", schemas, err)
	}
	if schemas[0].Binding.State != "available" || !strings.Contains(string(schemas[0].Binding.Document), "metadata") {
		t.Fatal("actual native document was not retained")
	}
	binding := schemas[1].Binding
	if binding.State != "unavailable" || binding.UnavailableReason == nil || *binding.UnavailableReason != "FORBIDDEN" || binding.Validation != "none" || len(binding.Document) != 0 || len(binding.Unvalidated) != 0 {
		t.Fatalf("unavailable role misrepresented: %+v", binding)
	}
	output, err := json.Marshal(schemas)
	if err != nil || strings.Contains(string(output), "sensitive") || !strings.Contains(string(output), `"unavailableReason":"FORBIDDEN"`) {
		t.Fatalf("unsafe/unobservable availability reason: %s %v", output, err)
	}
}

func TestSchemaUnavailableReasonStrictAdmissionBeforeDecode(t *testing.T) {
	cases := []struct{ state, reason string }{
		{"available", "FORBIDDEN"}, {"absent", "FORBIDDEN"}, {"unavailable", ""}, {"unavailable", "forbidden"}, {"unavailable", "FORBIDDEN secret"}, {"unavailable", "https://credential.invalid"}, {"unavailable", strings.Repeat("A", 65)}, {"unavailable", "UNREVIEWED_CODE"}, {"unavailable", "FORBIDDEN_é"},
	}
	for _, tc := range cases {
		t.Run(tc.state+"/"+tc.reason, func(t *testing.T) {
			reason := tc.reason
			wire := &resourcepb.SchemaBinding{State: tc.state, Validation: "none", UnavailableReason: &reason}
			data, err := proto.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			decoded := new(resourcepb.SchemaBinding)
			envelope := NewTransportEnvelope(decoded, false)
			if err := encoding.GetCodecV2("proto").Unmarshal(mem.BufferSlice{mem.SliceBuffer(data)}, envelope); err != nil {
				t.Fatal(err)
			}
			if resourcev2.ErrorCode(envelope.AdmissionError()) != "INVALID_REQUEST" || decoded.State != "" {
				t.Fatalf("invalid schema materialized before admission: %+v %v", decoded, envelope.AdmissionError())
			}
			value := resourcev2.SchemaBinding{State: tc.state, Validation: "none", Unvalidated: []string{}, UnavailableReason: &reason}
			if _, err := resourcev2.ToProtoSchemaBinding(value); resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
				t.Fatalf("native invalid reason admitted: %v", err)
			}
			input, _ := json.Marshal(map[string]any{"state": tc.state, "validation": "none", "unvalidated": []string{}, "unavailableReason": tc.reason})
			var binding resourcev2.SchemaBinding
			if err := json.Unmarshal(input, &binding); resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
				t.Fatalf("invalid JSON schema reason admitted: %s %v", input, err)
			}
		})
	}
	var absent resourcev2.SchemaBinding
	if err := json.Unmarshal([]byte(`{"state":"unavailable","validation":"none","unvalidated":[]}`), &absent); err != nil || absent.UnavailableReason != nil {
		t.Fatalf("absent reason synthesized: %+v %v", absent, err)
	}
	if err := json.Unmarshal([]byte(`{"state":"unavailable","validation":"none","unvalidated":[],"unavailableReason":null}`), &absent); resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("explicit null reason admitted: %v", err)
	}
}
