package metric

import (
	"context"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	metricpb "github.com/omniviewdev/plugin-sdk/proto/v1/metric"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"time"
)

const finiteWireVersion = 2

func sourceToProto(s MetricSourceRef) *metricpb.MetricSourceRef {
	return &metricpb.MetricSourceRef{PluginId: s.PluginID, ConnectionId: s.ConnectionID, DatasourceUid: s.DatasourceUID, DatasourceKind: s.DatasourceKind, Native: s.Native}
}
func sourceFromProto(s *metricpb.MetricSourceRef) MetricSourceRef {
	return MetricSourceRef{PluginID: s.GetPluginId(), ConnectionID: s.GetConnectionId(), DatasourceUID: s.GetDatasourceUid(), DatasourceKind: s.GetDatasourceKind(), Native: s.GetNative()}
}
func finiteTime(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}
func decodeFiniteTime(t *timestamppb.Timestamp) (time.Time, error) {
	if t == nil {
		return time.Time{}, nil
	}
	if err := t.CheckValid(); err != nil {
		return time.Time{}, metricError("INVALID_REQUEST", "Invalid finite timestamp.")
	}
	return t.AsTime(), nil
}
func decodeFiniteDuration(d *durationpb.Duration) (time.Duration, error) {
	if d == nil {
		return 0, nil
	}
	if err := d.CheckValid(); err != nil {
		return 0, metricError("INVALID_REQUEST", "Invalid finite duration.")
	}
	v := d.AsDuration()
	if durationpb.New(v).Seconds != d.Seconds || durationpb.New(v).Nanos != d.Nanos {
		return 0, metricError("INVALID_REQUEST", "Finite duration overflow.")
	}
	return v, nil
}
func catalogToProto(c *ResourceMetricsCatalog) *metricpb.ResourceMetricsCatalog {
	p := &metricpb.ResourceMetricsCatalog{Source: sourceToProto(c.Source), MaxTargets: int32(c.MaxTargets), MaxSampleAge: durationpb.New(c.MaxSampleAge)}
	for _, d := range c.Metrics {
		v := &metricpb.FiniteMetricDescriptor{Id: d.ID, Name: d.Name, ResourceKey: d.ResourceKey, Unit: d.Unit, Shapes: d.Shapes, Description: d.Description, RangeReuse: d.RangeReuse}
		if d.Source != nil {
			v.Source = sourceToProto(*d.Source)
		}
		p.Metrics = append(p.Metrics, v)
	}
	return p
}
func catalogFromProto(p *metricpb.ResourceMetricsCatalog) (*ResourceMetricsCatalog, error) {
	if p == nil {
		return nil, metricError("INVALID_REQUEST", "Missing finite catalog wire message.")
	}
	maxAge, err := decodeFiniteDuration(p.MaxSampleAge)
	if err != nil {
		return nil, err
	}
	c := &ResourceMetricsCatalog{Source: sourceFromProto(p.Source), MaxTargets: int(p.MaxTargets), MaxSampleAge: maxAge}
	for _, d := range p.Metrics {
		if d == nil {
			return nil, metricError("INVALID_REQUEST", "Nil finite descriptor.")
		}
		v := FiniteMetricDescriptor{ID: d.Id, Name: d.Name, ResourceKey: d.ResourceKey, Unit: d.Unit, Shapes: d.Shapes, Description: d.Description, RangeReuse: d.RangeReuse}
		if d.Source != nil {
			source := sourceFromProto(d.Source)
			v.Source = &source
		}
		c.Metrics = append(c.Metrics, v)
	}
	return c, ValidateResourceMetricsCatalog(c)
}
func finiteQueryToProto(q ResourceMetricsQuery) (*metricpb.ResourceMetricsQuery, error) {
	if err := ValidateResourceMetricsQuery(q); err != nil {
		return nil, err
	}
	p := &metricpb.ResourceMetricsQuery{Source: sourceToProto(q.Source), MetricIds: q.MetricIDs, Shape: q.Shape, StartTime: finiteTime(q.StartTime), EndTime: finiteTime(q.EndTime), Context: finiteContextToProto(q.Context)}
	if q.Step != 0 {
		p.Step = durationpb.New(q.Step)
	}
	for _, t := range q.Targets {
		ref, err := resourcev2.ToProtoResourceReference(t.Reference)
		if err != nil {
			return nil, err
		}
		route, err := resourcev2.ToProtoAccessRoute(t.Route)
		if err != nil {
			return nil, err
		}
		p.Targets = append(p.Targets, &metricpb.ResourceMetricTarget{Token: t.Token, Reference: ref, Route: route, PublicData: t.PublicData})
	}
	return p, nil
}
func finiteQueryFromProto(p *metricpb.ResourceMetricsQuery) (ResourceMetricsQuery, error) {
	var q ResourceMetricsQuery
	if p == nil {
		return q, metricError("INVALID_REQUEST", "Missing finite query.")
	}
	q.Source = sourceFromProto(p.Source)
	q.MetricIDs = p.MetricIds
	q.Shape = p.Shape
	var err error
	q.Context, err = finiteContextFromProto(p.Context)
	if err != nil {
		return q, err
	}
	q.StartTime, err = decodeFiniteTime(p.StartTime)
	if err != nil {
		return q, err
	}
	q.EndTime, err = decodeFiniteTime(p.EndTime)
	if err != nil {
		return q, err
	}
	q.Step, err = decodeFiniteDuration(p.Step)
	if err != nil {
		return q, err
	}
	if len(p.Targets) > MaxFiniteTargets {
		return q, metricError("RESOURCE_LIMIT", "Too many finite targets.")
	}
	for _, t := range p.Targets {
		if t == nil || t.Reference == nil || t.Route == nil {
			return q, metricError("INVALID_REQUEST", "Finite target requires reference and route.")
		}
		ref, err := resourcev2.FromProtoResourceReference(t.Reference)
		if err != nil {
			return q, err
		}
		route, err := resourcev2.FromProtoAccessRoute(t.Route)
		if err != nil {
			return q, err
		}
		q.Targets = append(q.Targets, ResourceMetricTarget{Token: t.Token, Reference: ref, Route: route, PublicData: t.PublicData})
	}
	return q, ValidateResourceMetricsQuery(q)
}
func finiteResponseToProto(r *ResourceMetricsResponse) *metricpb.ResourceMetricsResponse {
	p := &metricpb.ResourceMetricsResponse{Complete: r.Complete, Warnings: r.Warnings}
	for _, v := range r.Results {
		out := &metricpb.ResourceMetricResult{ResourceToken: v.ResourceToken, MetricId: v.MetricID, Status: v.Status, Value: v.Value, Dimensions: v.Dimensions, Window: durationpb.New(v.Window), Message: v.Message, IdentityScope: v.IdentityScope}
		if v.Source != nil {
			out.Source = sourceToProto(*v.Source)
		}
		if v.ObservedAt != nil {
			out.ObservedAt = finiteTime(*v.ObservedAt)
		}
		for _, point := range v.Points {
			out.Points = append(out.Points, &metricpb.ResourceMetricPoint{Time: finiteTime(point.Time), Value: point.Value})
		}
		p.Results = append(p.Results, out)
	}
	return p
}
func finiteResponseFromProto(q ResourceMetricsQuery, p *metricpb.ResourceMetricsResponse) (*ResourceMetricsResponse, error) {
	if p == nil || len(p.Results) > len(q.Targets)*len(q.MetricIDs) {
		return nil, metricError("RESOURCE_LIMIT", "Invalid finite result count.")
	}
	r := &ResourceMetricsResponse{Complete: p.Complete, Warnings: p.Warnings}
	points := 0
	for _, v := range p.Results {
		if v == nil {
			return nil, metricError("INVALID_REQUEST", "Nil finite result.")
		}
		out := ResourceMetricResult{ResourceToken: v.ResourceToken, MetricID: v.MetricId, Status: v.Status, Value: v.Value, Dimensions: v.Dimensions, Message: v.Message, IdentityScope: v.IdentityScope}
		if v.Source != nil {
			source := sourceFromProto(v.Source)
			out.Source = &source
		}
		var err error
		out.Window, err = decodeFiniteDuration(v.Window)
		if err != nil {
			return nil, err
		}
		if v.ObservedAt != nil {
			t, err := decodeFiniteTime(v.ObservedAt)
			if err != nil {
				return nil, err
			}
			out.ObservedAt = &t
		}
		points += len(v.Points)
		if points > MaxFinitePoints {
			return nil, metricError("RESOURCE_LIMIT", "Too many finite points.")
		}
		for _, point := range v.Points {
			if point == nil {
				return nil, metricError("INVALID_REQUEST", "Nil finite point.")
			}
			t, err := decodeFiniteTime(point.Time)
			if err != nil {
				return nil, err
			}
			out.Points = append(out.Points, ResourceMetricPoint{Time: t, Value: point.Value})
		}
		r.Results = append(r.Results, out)
	}
	return r, ValidateResourceMetricsResponse(q, r)
}

// GetFiniteCapabilities negotiates the typed finite protocol without legacy calls.
func (s *PluginServer) GetFiniteCapabilities(_ context.Context, _ *emptypb.Empty) (*metricpb.FiniteMetricCapabilities, error) {
	_, finite := s.Impl.(FiniteQueryProvider)
	_, owner := s.Impl.(MetricOwnerCatalogProvider)
	if m, ok := s.Impl.(*Manager); ok {
		finite = m.queryResourcesFunc != nil && m.resourceMetricsCatalogFunc != nil
		owner = m.getMetricOwnerCatalogFunc != nil
	}
	var version uint32
	if finite {
		version = finiteWireVersion
	}
	return &metricpb.FiniteMetricCapabilities{Version: version, OwnerCatalog: owner}, nil
}
func (s *PluginServer) GetResourceMetricsCatalog(ctx context.Context, p *metricpb.ResourceMetricsCatalogRequest) (*metricpb.ResourceMetricsCatalog, error) {
	impl, ok := s.Impl.(FiniteQueryProvider)
	if !ok {
		return nil, resourceplugin.ResourceRPCError(metricError("UNSUPPORTED", "Finite metrics are unsupported."))
	}
	if p == nil || p.ResourceKey == "" {
		return nil, resourceplugin.ResourceRPCError(metricError("INVALID_REQUEST", "Missing resource key."))
	}
	ctx, err := resourceplugin.IncomingSessionContext(ctx)
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	mapping, err := finiteContextFromProto(p.Context)
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	request := ResourceMetricsCatalogRequest{Source: sourceFromProto(p.Source), ResourceKey: p.ResourceKey, Context: mapping}
	if err := ValidateResourceMetricsCatalogRequest(request); err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	c, err := impl.GetResourceMetricsCatalog(metricPluginContext(ctx), request)
	if err == nil {
		err = ValidateResourceMetricsCatalog(c)
	}
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	return catalogToProto(c), nil
}
func (s *PluginServer) QueryResources(ctx context.Context, p *metricpb.ResourceMetricsQuery) (*metricpb.ResourceMetricsResponse, error) {
	impl, ok := s.Impl.(FiniteQueryProvider)
	if !ok {
		return nil, resourceplugin.ResourceRPCError(metricError("UNSUPPORTED", "Finite metrics are unsupported."))
	}
	ctx, err := resourceplugin.IncomingSessionContext(ctx)
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	q, err := finiteQueryFromProto(p)
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	r, err := impl.QueryResources(metricPluginContext(ctx), q)
	if err == nil {
		err = ValidateResourceMetricsResponse(q, r)
	}
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	return finiteResponseToProto(r), nil
}
func (c *PluginClient) negotiateFinite(ctx context.Context) error {
	option, admission := resourceplugin.ResponseAdmission()
	p, err := c.client.GetFiniteCapabilities(ctx, &emptypb.Empty{}, option)
	if e := admission(); e != nil {
		return e
	}
	if err != nil {
		return metricDomainError(err)
	}
	if p.GetVersion() != finiteWireVersion {
		return metricError("UNSUPPORTED", "Finite metric protocol version is unsupported.")
	}
	return nil
}

// GetResourceMetricsCatalog negotiates finite support; old processes return UNSUPPORTED.
func (c *PluginClient) GetResourceMetricsCatalog(pc *types.PluginContext, request ResourceMetricsCatalogRequest) (*ResourceMetricsCatalog, error) {
	if err := ValidateResourceMetricsCatalogRequest(request); err != nil {
		return nil, err
	}
	ctx, err := resourceplugin.OutgoingSessionContext(pluginCallContext(pc))
	if err != nil {
		return nil, err
	}
	if err = c.negotiateFinite(ctx); err != nil {
		return nil, err
	}
	option, admission := resourceplugin.ResponseAdmission()
	p, err := c.client.GetResourceMetricsCatalog(ctx, &metricpb.ResourceMetricsCatalogRequest{ResourceKey: request.ResourceKey, Source: sourceToProto(request.Source), Context: finiteContextToProto(request.Context)}, option)
	if e := admission(); e != nil {
		return nil, e
	}
	if err != nil {
		return nil, metricDomainError(err)
	}
	catalog, err := catalogFromProto(p)
	if err != nil {
		return nil, err
	}
	if catalog.Source != request.Source {
		return nil, metricError("INVALID_REQUEST", "Catalog response conflicts with selected source.")
	}
	for _, descriptor := range catalog.Metrics {
		if descriptor.ResourceKey != request.ResourceKey || descriptor.Source != nil && *descriptor.Source != request.Source {
			return nil, metricError("INVALID_REQUEST", "Catalog descriptor conflicts with selected source or resource type.")
		}
	}
	return catalog, nil
}

// QueryResources makes one typed batch RPC, never a per-resource fallback loop.
func (c *PluginClient) QueryResources(pc *types.PluginContext, q ResourceMetricsQuery) (*ResourceMetricsResponse, error) {
	p, err := finiteQueryToProto(q)
	if err != nil {
		return nil, err
	}
	ctx, err := resourceplugin.OutgoingSessionContext(pluginCallContext(pc))
	if err != nil {
		return nil, err
	}
	if err = c.negotiateFinite(ctx); err != nil {
		return nil, err
	}
	option, admission := resourceplugin.ResponseAdmission()
	out, err := c.client.QueryResources(ctx, p, option)
	if e := admission(); e != nil {
		return nil, e
	}
	if err != nil {
		return nil, metricDomainError(err)
	}
	return finiteResponseFromProto(q, out)
}
