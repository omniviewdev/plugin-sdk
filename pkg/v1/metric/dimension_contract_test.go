package metric

import "testing"

func TestResourceSetPreservesDeclaredDimensions(t *testing.T) {
	request := resourceSetFixture()
	request.Definitions = []MetricDefinition{{ID: "cpu", Name: "CPU", Dimensions: []string{"container", "node"}}}
	result := func(container string) ResourceMetricResult {
		return ResourceMetricResult{ResourceToken: "pod-a", MetricID: "cpu", Status: MetricStatusOK, Dimensions: map[string]string{"container": container, "node": "worker"}, Value: &MetricResult{CurrentValue: &CurrentValue{Value: 1}}}
	}
	response := &ResourceSetQueryResponse{Results: []ResourceMetricResult{result("app"), result("sidecar")}}
	if err := ValidateResourceSetResponse(request, response); err != nil {
		t.Fatal(err)
	}
	duplicate := result("app")
	duplicate.Dimensions = map[string]string{}
	duplicate.Dimensions["node"] = "worker"
	duplicate.Dimensions["container"] = "app"
	response.Results = append(response.Results, duplicate)
	if ValidateResourceSetResponse(request, response) == nil {
		t.Fatal("map iteration order hid duplicate dimensional identity")
	}
	response.Results = []ResourceMetricResult{result("app")}
	response.Results[0].Dimensions["undeclared"] = "value"
	if ValidateResourceSetResponse(request, response) == nil {
		t.Fatal("undeclared metric dimension accepted")
	}
}

func TestResourceFieldPathsArePointersNotExpressions(t *testing.T) {
	definition := MetricDefinition{ID: "cpu", Name: "CPU", FieldBindings: []MetricFieldBinding{{Field: "workload", Source: "resource"}}}
	for _, path := range []string{"/metadata/labels/app~1name", "/metadata/name", "/metadata/labels/tilde~0key"} {
		definition.FieldBindings[0].ResourcePath = path
		if err := ValidateMetricDefinitionContract(definition); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"metadata.name", "$.metadata.name", "/metadata/~2", "/metadata/~"} {
		definition.FieldBindings[0].ResourcePath = path
		if ValidateMetricDefinitionContract(definition) == nil {
			t.Fatalf("invalid pointer accepted: %q", path)
		}
	}
	definition.FieldBindings[0].ResourcePath = "/metadata/name"
	definition.FieldBindings[0].Source = "scope"
	if ValidateMetricDefinitionContract(definition) == nil {
		t.Fatal("resource pointer accepted for unrelated scope input")
	}
}
