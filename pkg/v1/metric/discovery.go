package metric

import (
	"context"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	metricpb "github.com/omniviewdev/plugin-sdk/proto/v1/metric"
	"time"
)

type MetricFieldTransform string

const (
	MetricTransformIdentity  MetricFieldTransform = "identity"
	MetricTransformLowercase MetricFieldTransform = "lowercase"
	MetricTransformUppercase MetricFieldTransform = "uppercase"
)

// MetricDiscoveryRequest permits exact matchers only, never arbitrary selectors.
type MetricDiscoveryRequest struct {
	Source     MetricSourceReference `json:"source"`
	MetricName string                `json:"metric_name"`
	LabelName  string                `json:"label_name"`
	Matchers   map[string]string     `json:"matchers"`
	StartTime  time.Time             `json:"start_time"`
	EndTime    time.Time             `json:"end_time"`
	Limit      int                   `json:"limit"`
}
type MetricDiscoveryResult struct {
	Values    []string `json:"values"`
	Truncated bool     `json:"truncated"`
	Warnings  []string `json:"warnings"`
}
type MetricDiscoveryProvider interface {
	DiscoverMetricLabels(*QueryContext, MetricDiscoveryRequest) (*MetricDiscoveryResult, error)
}

func ValidateMetricDiscovery(req MetricDiscoveryRequest) error {
	if err := resourcev2.ValidateMetadata(req); err != nil {
		return err
	}
	if req.Source.PluginID == "" || req.Source.ConnectionID == "" || req.Limit < 1 || req.Limit > 100 || len(req.Matchers) > 32 || req.EndTime.Before(req.StartTime) {
		return metricError("INVALID_REQUEST", "Invalid bounded metric discovery request.")
	}
	return nil
}
func validateMetricDiscoveryResult(req MetricDiscoveryRequest, out *MetricDiscoveryResult) error {
	if out == nil || len(out.Values) > req.Limit {
		return metricError("INVALID_REQUEST", "Provider exceeded discovery limit.")
	}
	return resourcev2.ValidateMetadata(out)
}
func (m *Manager) DiscoverMetricLabels(q *QueryContext, req MetricDiscoveryRequest) (*MetricDiscoveryResult, error) {
	if err := ValidateMetricDiscovery(req); err != nil {
		return nil, err
	}
	if m.discovery == nil {
		return nil, metricError("UNSUPPORTED", "Metric discovery is unavailable.")
	}
	ctx := context.Background()
	if q != nil && q.Context != nil {
		ctx = q.Context
	}
	pc := &types.PluginContext{Context: ctx}
	if q != nil {
		pc.Connection = q.Connection
	}
	if err := validateProviderQueryContext(q, req.Source); err != nil {
		return nil, err
	}
	var out *MetricDiscoveryResult
	err := resourcev2.WithUtilityConnection(pc, m.connectionProvider, nil, nil, func(bound *types.PluginContext) error {
		var err error
		out, err = m.discovery.DiscoverMetricLabels(m.providerQueryContext(bound, q), req)
		if err == nil {
			err = validateMetricDiscoveryResult(req, out)
		}
		return err
	})
	return out, err
}
func (s *resourceSetServer) DiscoverMetricLabels(ctx context.Context, p *metricpb.MetricContractRequest) (*metricpb.MetricContractResponse, error) {
	var input MetricDiscoveryRequest
	if err := contractInput(p, &input); err != nil {
		return contractOutput(nil, err)
	}
	if err := ValidateMetricDiscovery(input); err != nil {
		return contractOutput(nil, err)
	}
	provider, ok := s.impl.(MetricDiscoveryProvider)
	if !ok {
		return contractOutput(nil, metricError("UNSUPPORTED", "Metric discovery is unavailable."))
	}
	q := wireQueryContext(ctx, p)
	if err := validateProviderQueryContext(q, input.Source); err != nil {
		return contractOutput(nil, err)
	}
	out, err := provider.DiscoverMetricLabels(q, input)
	if err == nil {
		err = validateMetricDiscoveryResult(input, out)
	}
	return contractOutput(out, err)
}
func (c *PluginClient) DiscoverMetricLabels(q *QueryContext, input MetricDiscoveryRequest) (*MetricDiscoveryResult, error) {
	if err := ValidateMetricDiscovery(input); err != nil {
		return nil, err
	}
	var out MetricDiscoveryResult
	err := c.contractCall(q, "DiscoverMetricLabels", input, &out)
	if err == nil {
		err = validateMetricDiscoveryResult(input, &out)
	}
	return &out, err
}
