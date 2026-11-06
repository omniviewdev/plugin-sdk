package metric

import (
	"context"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	metricpb "github.com/omniviewdev/plugin-sdk/proto/v1/metric"
	"google.golang.org/protobuf/types/known/emptypb"
)

// ValidateMetricOwnerCatalog bounds owner metadata independently of source execution.
func ValidateMetricOwnerCatalog(c *MetricOwnerCatalog) error {
	if c == nil || len(c.ScopeFields) > 32 {
		return metricError("INVALID_REQUEST", "Invalid metric owner catalog.")
	}
	if err := ValidateMetricQueryContext(MetricQueryContext{Definitions: c.Definitions}); err != nil {
		return err
	}
	keys := make(map[string]bool, len(c.ScopeFields))
	for _, f := range c.ScopeFields {
		if f.Key == "" || f.Name == "" || keys[f.Key] {
			return metricError("INVALID_REQUEST", "Invalid metric scope field.")
		}
		keys[f.Key] = true
	}
	for _, d := range c.Definitions {
		for _, b := range d.FieldBindings {
			if b.Source == "scope" && !keys[b.Field] {
				return metricError("INVALID_REQUEST", "Metric binding uses undeclared owner scope.")
			}
		}
	}
	return resourcev2.ValidateMetadata(c)
}

func (m *Manager) GetMetricOwnerCatalog(pc *types.PluginContext) (*MetricOwnerCatalog, error) {
	if m.getMetricOwnerCatalogFunc == nil {
		return nil, metricError("UNSUPPORTED", "Metric owner catalog is unsupported.")
	}
	if pc == nil || pc.Context == nil || pc.Connection == nil {
		return nil, metricError("INVALID_REQUEST", "Metric owner catalog requires a selected connection.")
	}
	var out *MetricOwnerCatalog
	err := resourcev2.WithUtilityConnection(pc, m.connectionProvider, nil, nil, func(bound *types.PluginContext) error {
		var err error
		out, err = m.getMetricOwnerCatalogFunc(bound)
		if err != nil {
			return err
		}
		return ValidateMetricOwnerCatalog(out)
	})
	return out, err
}

func (s *PluginServer) GetMetricOwnerCatalog(ctx context.Context, _ *emptypb.Empty) (*metricpb.MetricOwnerCatalog, error) {
	impl, ok := s.Impl.(MetricOwnerCatalogProvider)
	if !ok {
		return nil, resourceplugin.ResourceRPCError(metricError("UNSUPPORTED", "Metric owner catalog is unsupported."))
	}
	ctx, err := resourceplugin.IncomingSessionContext(ctx)
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	c, err := impl.GetMetricOwnerCatalog(metricPluginContext(ctx))
	if err == nil {
		err = ValidateMetricOwnerCatalog(c)
	}
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	p := &metricpb.MetricOwnerCatalog{Definitions: finiteContextToProto(MetricQueryContext{Definitions: c.Definitions}).Definitions}
	for _, f := range c.ScopeFields {
		p.ScopeFields = append(p.ScopeFields, &metricpb.MetricScopeField{Key: f.Key, Name: f.Name, Description: f.Description})
	}
	return p, nil
}

// GetMetricOwnerCapabilities probes owner support without requiring finite execution.
func (c *PluginClient) GetMetricOwnerCapabilities(ctx context.Context) (bool, error) {
	option, admission := resourceplugin.ResponseAdmission()
	p, err := c.client.GetFiniteCapabilities(ctx, &emptypb.Empty{}, option)
	if e := admission(); e != nil {
		return false, e
	}
	if err != nil {
		return false, metricDomainError(err)
	}
	return p.GetOwnerCatalog(), nil
}

func (c *PluginClient) GetMetricOwnerCatalog(pc *types.PluginContext) (*MetricOwnerCatalog, error) {
	ctx, err := resourceplugin.OutgoingSessionContext(pluginCallContext(pc))
	if err != nil {
		return nil, err
	}
	supported, err := c.GetMetricOwnerCapabilities(ctx)
	if err != nil {
		return nil, err
	}
	if !supported {
		return nil, metricError("UNSUPPORTED", "Metric owner catalog is unsupported.")
	}
	option, admission := resourceplugin.ResponseAdmission()
	p, err := c.client.GetMetricOwnerCatalog(ctx, &emptypb.Empty{}, option)
	if e := admission(); e != nil {
		return nil, e
	}
	if err != nil {
		return nil, metricDomainError(err)
	}
	if p == nil || len(p.ScopeFields) > 32 {
		return nil, metricError("INVALID_REQUEST", "Invalid metric owner catalog.")
	}
	mapping, err := finiteContextFromProto(&metricpb.MetricQueryContext{Definitions: p.Definitions})
	if err != nil {
		return nil, err
	}
	out := &MetricOwnerCatalog{Definitions: mapping.Definitions}
	for _, f := range p.ScopeFields {
		if f == nil {
			return nil, metricError("INVALID_REQUEST", "Nil metric scope field.")
		}
		out.ScopeFields = append(out.ScopeFields, MetricScopeField{Key: f.Key, Name: f.Name, Description: f.Description})
	}
	return out, ValidateMetricOwnerCatalog(out)
}
