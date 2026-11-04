package metric

import (
	"context"
	"testing"
)

type discoveryRecorder struct {
	calls    int
	overflow bool
}

func (r *discoveryRecorder) DiscoverMetricLabels(_ *QueryContext, req MetricDiscoveryRequest) (*MetricDiscoveryResult, error) {
	r.calls++
	out := &MetricDiscoveryResult{Values: []string{"cpu_usage"}}
	if r.overflow {
		out.Values = append(out.Values, "memory_usage")
	}
	return out, nil
}
func TestDiscoveryEnforcesRequestAndResponseLimits(t *testing.T) {
	recorder := &discoveryRecorder{}
	manager := NewManager(nil, nil, ProviderInfo{}, nil, nil, nil)
	manager.discovery = recorder
	req := MetricDiscoveryRequest{Source: MetricSourceReference{PluginID: "grafana", ConnectionID: "provider"}, Limit: 1, LabelName: "__name__"}
	if _, err := manager.DiscoverMetricLabels(&QueryContext{Context: context.Background()}, req); err != nil {
		t.Fatal(err)
	}
	req.Limit = 101
	if _, err := manager.DiscoverMetricLabels(nil, req); err == nil {
		t.Fatal("unbounded discovery reached provider")
	}
	if recorder.calls != 1 {
		t.Fatal("invalid discovery dispatched")
	}
	req.Limit = 1
	recorder.overflow = true
	if _, err := manager.DiscoverMetricLabels(nil, req); err == nil {
		t.Fatal("provider discovery overflow accepted")
	}
}
func TestDefinitionRejectsUnboundedTransform(t *testing.T) {
	definition := MetricDefinition{ID: "cpu", Name: "CPU", Unit: UnitCores, FieldBindings: []MetricFieldBinding{{Transform: "exec"}}}
	if ValidateMetricDefinitionContract(definition) == nil {
		t.Fatal("arbitrary transform accepted")
	}
	for _, transform := range []MetricFieldTransform{"", MetricTransformIdentity, MetricTransformLowercase, MetricTransformUppercase} {
		definition.FieldBindings[0].Transform = transform
		if err := ValidateMetricDefinitionContract(definition); err != nil {
			t.Fatal(err)
		}
	}
}
