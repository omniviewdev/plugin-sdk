package metric

import (
	"context"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// QueryResources leases only the selected provider connection. Source references
// remain owner-admitted inputs and never enter the provider's resource catalog.
func (m *Manager) QueryResources(q *QueryContext, req ResourceSetQueryRequest) (*ResourceSetQueryResponse, error) {
	if err := ValidateResourceSetQuery(req); err != nil {
		return nil, err
	}
	if m.resourceSetProvider == nil {
		return nil, metricError("UNSUPPORTED", "Resource-set metrics are unavailable.")
	}
	ctx := context.Background()
	if q != nil && q.Context != nil {
		ctx = q.Context
	}
	pc := &types.PluginContext{Context: ctx}
	if q != nil && q.Connection != nil {
		pc.Connection = q.Connection
	}
	if err := validateProviderQueryContext(q, req.Source); err != nil {
		return nil, err
	}
	var out *ResourceSetQueryResponse
	err := resourcev2.WithUtilityConnection(pc, m.connectionProvider, nil, nil, func(bound *types.PluginContext) error {
		var err error
		out, err = m.resourceSetProvider.QueryResources(m.providerQueryContext(bound, q), req)
		if err == nil {
			err = ValidateResourceSetResponse(req, out)
		}
		return err
	})
	return out, err
}
func (m *Manager) GetMetricDefinitions(q *QueryContext) ([]MetricDefinition, error) {
	if m.catalog == nil {
		return nil, nil
	}
	return m.catalog.GetMetricDefinitions(q)
}
func (m *Manager) GetProviderConnections(q *QueryContext) ([]ProviderConnection, error) {
	if m.configuration == nil {
		return nil, metricError("UNSUPPORTED", "Provider configuration is unavailable.")
	}
	return m.configuration.GetProviderConnections(q)
}
func (m *Manager) SaveProviderConnection(q *QueryContext, input ProviderConnectionInput) (*ProviderConnection, error) {
	if m.configuration == nil {
		return nil, metricError("UNSUPPORTED", "Provider configuration is unavailable.")
	}
	return m.configuration.SaveProviderConnection(q, input)
}
func (m *Manager) DeleteProviderConnection(q *QueryContext, id string) error {
	if m.configuration == nil {
		return metricError("UNSUPPORTED", "Provider configuration is unavailable.")
	}
	return m.configuration.DeleteProviderConnection(q, id)
}
func (m *Manager) GetDataSources(q *QueryContext, source MetricSourceReference) ([]MetricDataSource, error) {
	if m.configuration == nil {
		return nil, metricError("UNSUPPORTED", "Provider configuration is unavailable.")
	}
	return m.configuration.GetDataSources(q, source)
}
func (m *Manager) TestProviderConnection(q *QueryContext, id string) (*MetricValidationResult, error) {
	if m.configuration == nil {
		return nil, metricError("UNSUPPORTED", "Provider configuration is unavailable.")
	}
	return m.configuration.TestProviderConnection(q, id)
}
func (m *Manager) ValidateMetricDefinition(q *QueryContext, source MetricSourceReference, definition MetricDefinition) (*MetricValidationResult, error) {
	if m.configuration == nil {
		return nil, metricError("UNSUPPORTED", "Provider configuration is unavailable.")
	}
	return m.configuration.ValidateMetricDefinition(q, source, definition)
}
