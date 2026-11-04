package metric

import "github.com/omniviewdev/plugin-sdk/pkg/types"

// A saved metrics connection identity is not a configured resource profile.
// Validate both selectors without manufacturing a profile or foreign lease.
func validateProviderQueryContext(q *QueryContext, source MetricSourceReference) error {
	if q == nil {
		return nil
	}
	if q.ConnectionID != "" && q.ConnectionID != source.ConnectionID {
		return metricError("INVALID_REQUEST", "Provider context conflicts with selected source connection.")
	}
	if q.Connection != nil && q.Connection.ID != source.ConnectionID {
		return metricError("INVALID_REQUEST", "Provider profile conflicts with selected source connection.")
	}
	return nil
}

func (m *Manager) providerQueryContext(bound *types.PluginContext, original *QueryContext) *QueryContext {
	q := m.queryContext(bound.Context, bound)
	if original != nil && q.Connection == nil {
		q.ConnectionID = original.ConnectionID
	}
	return q
}
