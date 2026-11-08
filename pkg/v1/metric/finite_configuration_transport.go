package metric

import metricpb "github.com/omniviewdev/plugin-sdk/proto/v1/metric"

func finiteContextToProto(c MetricQueryContext) *metricpb.MetricQueryContext {
	p := &metricpb.MetricQueryContext{PublicScope: c.PublicScope, MappingRevision: c.MappingRevision, ProjectionRevision: c.ProjectionRevision}
	for _, d := range c.Definitions {
		v := &metricpb.MetricDefinition{Id: d.ID, Name: d.Name, ResourceKeys: d.ResourceKeys, Unit: d.Unit, Semantics: d.Semantics, Dimensions: d.Dimensions, SupportedShapes: d.SupportedShapes, QueryLanguage: d.QueryLanguage, Query: d.Query, IdentityLabels: d.IdentityLabels, Reducer: d.Reducer, BatchStrategy: d.BatchStrategy, Revision: d.Revision, Native: d.Native, RangeReuse: d.RangeReuse}
		for _, b := range d.FieldBindings {
			v.FieldBindings = append(v.FieldBindings, &metricpb.MetricFieldBinding{Field: b.Field, Label: b.Label, Source: b.Source, Required: b.Required, Transform: string(b.Transform), ResourcePath: b.ResourcePath})
		}
		p.Definitions = append(p.Definitions, v)
	}
	return p
}
func finiteContextFromProto(p *metricpb.MetricQueryContext) (MetricQueryContext, error) {
	var c MetricQueryContext
	if p == nil {
		return c, nil
	}
	if len(p.Definitions) > MaxFiniteMetrics {
		return c, metricError("RESOURCE_LIMIT", "Too many finite definitions.")
	}
	c.PublicScope = p.PublicScope
	c.MappingRevision = p.MappingRevision
	c.ProjectionRevision = p.ProjectionRevision
	for _, d := range p.Definitions {
		if d == nil || len(d.FieldBindings) > 32 {
			return c, metricError("INVALID_REQUEST", "Invalid finite mapping.")
		}
		v := MetricDefinition{ID: d.Id, Name: d.Name, ResourceKeys: d.ResourceKeys, Unit: d.Unit, Semantics: d.Semantics, Dimensions: d.Dimensions, SupportedShapes: d.SupportedShapes, QueryLanguage: d.QueryLanguage, Query: d.Query, IdentityLabels: d.IdentityLabels, Reducer: d.Reducer, BatchStrategy: d.BatchStrategy, Revision: d.Revision, Native: d.Native, RangeReuse: d.RangeReuse}
		for _, b := range d.FieldBindings {
			if b == nil {
				return c, metricError("INVALID_REQUEST", "Nil finite field binding.")
			}
			v.FieldBindings = append(v.FieldBindings, MetricFieldBinding{Field: b.Field, Label: b.Label, Source: b.Source, Required: b.Required, Transform: MetricFieldTransform(b.Transform), ResourcePath: b.ResourcePath})
		}
		c.Definitions = append(c.Definitions, v)
	}
	return c, ValidateMetricQueryContext(c)
}
