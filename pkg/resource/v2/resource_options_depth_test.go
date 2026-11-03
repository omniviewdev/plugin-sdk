package resourcev2_test

import (
	"encoding/json"
	"strings"
	"testing"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/logs"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/metric"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/networker"
)

func TestResourceOptionJSONValueDepth(t *testing.T) {
	value := resourcev2.Value{Kind: "null"}
	for range resourcev2.MaxValueDepth - 1 {
		value = resourcev2.Value{Kind: "list", List: []resourcev2.Value{value}}
	}
	value = resourcev2.Value{Kind: "map", Map: map[string]resourcev2.Value{"child": value}}
	body, err := value.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	tooDeep := `{"kind":"map","value":{"child":` + strings.Repeat(`{"kind":"list","value":[`, resourcev2.MaxValueDepth) + `{"kind":"null"}` + strings.Repeat(`]}`, resourcev2.MaxValueDepth) + `}}`
	for _, tc := range []struct {
		name, prefix string
		newValue func() any
	}{
		{"exec", `{"resource_plugin":"p","resource_key":"t","resource_data":`, func() any { return &exec.SessionOptions{} }},
		{"logs", `{"resource_data":`, func() any { return &logs.CreateSessionOptions{} }},
		{"metric query", `{"resource_data":`, func() any { return &metric.QueryRequest{} }},
		{"metric stream", `{"resource_data":`, func() any { return &metric.StreamInput{} }},
		{"network", `{"resource_data":`, func() any { return &networker.PortForwardResourceConnection{} }},
		{"network session", `{"connection_type":"RESOURCE","connection":{"resource_data":`, func() any { return &networker.PortForwardSessionOptions{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			suffix := "}"
			if tc.name == "network session" {
				suffix = "}}"
			}
			out := tc.newValue()
			if err := json.Unmarshal([]byte(tc.prefix+string(body)+suffix), out); err != nil {
				t.Fatalf("depth %d decode: %v", resourcev2.MaxValueDepth, err)
			}
			encoded, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, tc.newValue()); err != nil {
				t.Fatalf("round trip decode: %v", err)
			}
			if err := json.Unmarshal([]byte(tc.prefix+tooDeep+suffix), tc.newValue()); err == nil {
				t.Fatal("accepted depth above value limit")
			}
		})
	}
}
