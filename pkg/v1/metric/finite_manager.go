package metric

import (
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// GetResourceMetricsCatalog resolves metadata under the selected source lease.
// Unconfigured finite callbacks return UNSUPPORTED, never a legacy fallback.
func (m *Manager) GetResourceMetricsCatalog(pc *types.PluginContext, key string) (*ResourceMetricsCatalog, error) {
	if m.resourceMetricsCatalogFunc == nil || m.queryResourcesFunc == nil {
		return nil, metricError("UNSUPPORTED", "Finite metrics are unsupported.")
	}
	if key == "" || pc == nil || pc.Connection == nil || pc.Context == nil {
		return nil, metricError("INVALID_REQUEST", "Finite catalog requires a resource key and caller source context.")
	}
	var out *ResourceMetricsCatalog
	err := resourcev2.WithUtilityConnection(pc, m.connectionProvider, nil, nil, func(bound *types.PluginContext) error {
		var err error
		out, err = m.resourceMetricsCatalogFunc(bound, key)
		if err != nil {
			return err
		}
		if err = ValidateResourceMetricsCatalog(out); err != nil {
			return err
		}
		if m.sourcePluginID == "" || out.Source.PluginID != m.sourcePluginID || out.Source.ConnectionID != bound.Connection.ID {
			return metricError("INVALID_REQUEST", "Catalog source conflicts with configured plugin.")
		}
		for _, d := range out.Metrics {
			if d.ResourceKey != key {
				return metricError("INVALID_REQUEST", "Catalog resource key mismatch.")
			}
		}
		return nil
	})
	return out, err
}

// QueryResources holds every target's catalog/connection lease through execution.
// Source authority comes from configured provider and the selected context, not input.
func (m *Manager) QueryResources(pc *types.PluginContext, q ResourceMetricsQuery) (*ResourceMetricsResponse, error) {
	if m.queryResourcesFunc == nil || m.resourceMetricsCatalogFunc == nil {
		return nil, metricError("UNSUPPORTED", "Finite metrics are unsupported.")
	}
	if err := ValidateResourceMetricsQuery(q); err != nil {
		return nil, err
	}
	var response *ResourceMetricsResponse
	if pc == nil || pc.Context == nil || pc.Connection == nil || pc.Connection.ID != q.Source.ConnectionID || m.sourcePluginID == "" || q.Source.PluginID != m.sourcePluginID {
		return nil, metricError("INVALID_REQUEST", "Finite source conflicts with selected plugin context.")
	}
	var admit func(int, *types.PluginContext) error
	admit = func(i int, bound *types.PluginContext) error {
		if i < len(q.Targets) {
			t := &q.Targets[i]
			return resourcev2.WithUtilityConnection(bound, m.connectionProvider, &t.Reference, &t.Route, func(next *types.PluginContext) error { return admit(i+1, next) })
		}
		catalogs := map[string]*ResourceMetricsCatalog{}
		for _, t := range q.Targets {
			key := t.Reference.Address.Type
			c := catalogs[key]
			if c == nil {
				var err error
				c, err = m.GetResourceMetricsCatalog(bound, key)
				if err != nil {
					return err
				}
				catalogs[key] = c
			}
			if c.Source != q.Source {
				return metricError("INVALID_REQUEST", "Query source does not match authoritative catalog.")
			}
			if len(q.Targets) > c.MaxTargets {
				return metricError("RESOURCE_LIMIT", "Query exceeds catalog target bound.")
			}
			for _, id := range q.MetricIDs {
				found := false
				for _, d := range c.Metrics {
					if d.ID == id {
						for _, shape := range d.Shapes {
							if shape == q.Shape {
								found = true
							}
						}
					}
				}
				if !found {
					return metricError("UNSUPPORTED", "Metric or shape is not in the authoritative catalog.")
				}
			}
		}
		var err error
		response, err = m.queryResourcesFunc(bound, q)
		if err != nil {
			return err
		}
		return ValidateResourceMetricsResponse(q, response)
	}
	if err := admit(0, pc); err != nil {
		return nil, err
	}
	return response, nil
}
