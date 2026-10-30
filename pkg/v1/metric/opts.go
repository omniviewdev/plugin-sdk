package metric

import (
	"context"
	"time"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	metricpb "github.com/omniviewdev/plugin-sdk/proto/v1/metric"
)

// PluginOpts contains the options for the metric plugin.
type PluginOpts struct {
	// ProviderInfo describes this metric provider.
	ProviderInfo ProviderInfo `json:"provider_info"`

	// Handlers maps resource keys to their metric handlers.
	// For example: "core::v1::Pod" -> Handler
	Handlers map[string]Handler `json:"handlers"`

	// QueryFunc is the function that queries metrics from the provider.
	QueryFunc QueryFunc `json:"-"`

	// StreamFunc is the optional function that streams metrics. If nil,
	// the SDK will poll QueryFunc at the requested interval.
	StreamFunc StreamFunc `json:"-"`
}

// QueryFunc queries metrics for a resource.
type QueryFunc func(ctx *QueryContext, req QueryRequest) (*QueryResponse, error)

// StreamFunc streams metrics for subscriptions. If nil, the SDK polls QueryFunc.
type StreamFunc func(ctx *StreamContext, in chan StreamInput) (chan StreamOutput, error)

// ===================== Provider Types ===================== //

// ProviderInfo describes a metric provider.
type ProviderInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Icon        string `json:"icon"`
	Description string `json:"description"`
}

// ToProto encodes provider metadata into a new wire message.
func (p *ProviderInfo) ToProto() *metricpb.MetricProviderInfo {
	return &metricpb.MetricProviderInfo{
		Id:          p.ID,
		Name:        p.Name,
		Icon:        p.Icon,
		Description: p.Description,
	}
}

// ProviderInfoFromProto returns zero metadata for nil input.
func ProviderInfoFromProto(p *metricpb.MetricProviderInfo) ProviderInfo {
	if p == nil {
		return ProviderInfo{}
	}
	return ProviderInfo{
		ID:          p.GetId(),
		Name:        p.GetName(),
		Icon:        p.GetIcon(),
		Description: p.GetDescription(),
	}
}

// ===================== Enums ===================== //

// MetricUnit represents the unit of a metric value.
type MetricUnit int

const (
	// UnitNone denotes a dimensionless value.
	UnitNone        MetricUnit = 0
	// UnitBytes measures bytes.
	UnitBytes       MetricUnit = 1
	// UnitKB measures kilobytes.
	UnitKB          MetricUnit = 2
	// UnitMB measures megabytes.
	UnitMB          MetricUnit = 3
	// UnitGB measures gigabytes.
	UnitGB          MetricUnit = 4
	// UnitPercentage measures percentages.
	UnitPercentage  MetricUnit = 5
	// UnitMillis measures milliseconds.
	UnitMillis      MetricUnit = 6
	// UnitSeconds measures seconds.
	UnitSeconds     MetricUnit = 7
	// UnitCount measures discrete occurrences.
	UnitCount       MetricUnit = 8
	// UnitOpsPerSec measures operations per second.
	UnitOpsPerSec   MetricUnit = 9
	// UnitBytesPerSec measures byte throughput.
	UnitBytesPerSec MetricUnit = 10
	// UnitMillicores measures thousandths of a CPU core.
	UnitMillicores  MetricUnit = 11
	// UnitCores measures CPU cores.
	UnitCores       MetricUnit = 12
)

// ToProto converts the unit to the int32 wire enum. Out-of-range ints truncate.
func (u MetricUnit) ToProto() metricpb.MetricUnit {
	return metricpb.MetricUnit(u)
}

// MetricUnitFromProto preserves every int32 wire unit, including unknown values.
func MetricUnitFromProto(p metricpb.MetricUnit) MetricUnit {
	return MetricUnit(p)
}

// MetricShape indicates the type of metric data.
type MetricShape int

const (
	// ShapeCurrent requests the latest value.
	ShapeCurrent    MetricShape = 0
	// ShapeTimeseries requests timestamped samples.
	ShapeTimeseries MetricShape = 1
	// ShapeAggregate requests a statistical window.
	ShapeAggregate  MetricShape = 2
)

// ToProto converts the shape to the int32 wire enum. Out-of-range ints truncate.
func (s MetricShape) ToProto() metricpb.MetricShape {
	return metricpb.MetricShape(s)
}

// MetricShapeFromProto preserves every int32 wire shape, including unknown values.
func MetricShapeFromProto(p metricpb.MetricShape) MetricShape {
	return MetricShape(p)
}

// MetricStreamCommand represents a command for the metric stream.
type MetricStreamCommand int

const (
	// StreamCommandSubscribe starts a subscription within its owning lane.
	StreamCommandSubscribe   MetricStreamCommand = 0
	// StreamCommandUnsubscribe retires a subscription within its owning lane.
	StreamCommandUnsubscribe MetricStreamCommand = 1
	// StreamCommandClose retires the addressed subscription, not other lanes.
	StreamCommandClose       MetricStreamCommand = 2
)

// ToProto converts the command to the int32 wire enum. Out-of-range ints truncate.
func (c MetricStreamCommand) ToProto() metricpb.MetricStreamCommand {
	return metricpb.MetricStreamCommand(c)
}

// MetricStreamCommandFromProto preserves unknown wire commands for validation.
func MetricStreamCommandFromProto(p metricpb.MetricStreamCommand) MetricStreamCommand {
	return MetricStreamCommand(p)
}

// ===================== Descriptor Types ===================== //

// ColorRange defines a color to use for a range of metric values.
type ColorRange struct {
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
	Color string  `json:"color"`
}

// ToProto copies the range into a new wire message.
func (c *ColorRange) ToProto() *metricpb.MetricColorRange {
	return &metricpb.MetricColorRange{
		Min:   c.Min,
		Max:   c.Max,
		Color: c.Color,
	}
}

// ColorRangeFromProto returns a zero range for nil input.
func ColorRangeFromProto(p *metricpb.MetricColorRange) ColorRange {
	return ColorRange{
		Min:   p.GetMin(),
		Max:   p.GetMax(),
		Color: p.GetColor(),
	}
}

// MetricDescriptor describes a single metric.
type MetricDescriptor struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	Unit            MetricUnit    `json:"unit"`
	Icon            string        `json:"icon"`
	ColorRanges     []ColorRange  `json:"color_ranges"`
	FormatString    string        `json:"format_string"`
	SupportedShapes []MetricShape `json:"supported_shapes"`
	ChartGroup      string        `json:"chart_group"`
}

// ToProto copies descriptor slices into a new wire message.
func (d *MetricDescriptor) ToProto() *metricpb.MetricDescriptor {
	colorRanges := make([]*metricpb.MetricColorRange, 0, len(d.ColorRanges))
	for i := range d.ColorRanges {
		colorRanges = append(colorRanges, d.ColorRanges[i].ToProto())
	}
	shapes := make([]metricpb.MetricShape, 0, len(d.SupportedShapes))
	for _, s := range d.SupportedShapes {
		shapes = append(shapes, s.ToProto())
	}
	return &metricpb.MetricDescriptor{
		Id:              d.ID,
		Name:            d.Name,
		Unit:            d.Unit.ToProto(),
		Icon:            d.Icon,
		ColorRanges:     colorRanges,
		FormatString:    d.FormatString,
		SupportedShapes: shapes,
		ChartGroup:      d.ChartGroup,
	}
}

// MetricDescriptorFromProto decodes descriptor slices; nil input yields zero metadata.
func MetricDescriptorFromProto(p *metricpb.MetricDescriptor) MetricDescriptor {
	colorRanges := make([]ColorRange, 0, len(p.GetColorRanges()))
	for _, cr := range p.GetColorRanges() {
		colorRanges = append(colorRanges, ColorRangeFromProto(cr))
	}
	shapes := make([]MetricShape, 0, len(p.GetSupportedShapes()))
	for _, s := range p.GetSupportedShapes() {
		shapes = append(shapes, MetricShapeFromProto(s))
	}
	return MetricDescriptor{
		ID:              p.GetId(),
		Name:            p.GetName(),
		Unit:            MetricUnitFromProto(p.GetUnit()),
		Icon:            p.GetIcon(),
		ColorRanges:     colorRanges,
		FormatString:    p.GetFormatString(),
		SupportedShapes: shapes,
		ChartGroup:      p.GetChartGroup(),
	}
}

// Handler maps a resource key to its available metrics.
type Handler struct {
	Resource string             `json:"resource"`
	Metrics  []MetricDescriptor `json:"metrics"`
}

// ToProto copies the handler and its metric descriptors into wire messages.
func (h *Handler) ToProto() *metricpb.MetricHandler {
	metrics := make([]*metricpb.MetricDescriptor, 0, len(h.Metrics))
	for i := range h.Metrics {
		metrics = append(metrics, h.Metrics[i].ToProto())
	}
	return &metricpb.MetricHandler{
		Resource: h.Resource,
		Metrics:  metrics,
	}
}

// HandlerFromProto decodes the available metrics; nil input yields an empty handler.
func HandlerFromProto(p *metricpb.MetricHandler) Handler {
	metrics := make([]MetricDescriptor, 0, len(p.GetMetrics()))
	for _, m := range p.GetMetrics() {
		metrics = append(metrics, MetricDescriptorFromProto(m))
	}
	return Handler{
		Resource: p.GetResource(),
		Metrics:  metrics,
	}
}

// ===================== Data Types ===================== //

// DataPoint represents a single data point in a time series.
type DataPoint struct {
	Timestamp time.Time         `json:"timestamp"`
	Value     float64           `json:"value"`
	Labels    map[string]string `json:"labels"`
}

// ToProto encodes the timestamp and value; the labels map remains shared.
func (d *DataPoint) ToProto() *metricpb.DataPoint {
	return &metricpb.DataPoint{
		Timestamp: timestamppb.New(d.Timestamp),
		Value:     d.Value,
		Labels:    d.Labels,
	}
}

// DataPointFromProto decodes a sample and shares labels with the input.
// A missing timestamp yields the zero time.
func DataPointFromProto(p *metricpb.DataPoint) DataPoint {
	var ts time.Time
	if p.GetTimestamp() != nil {
		ts = p.GetTimestamp().AsTime()
	}
	return DataPoint{
		Timestamp: ts,
		Value:     p.GetValue(),
		Labels:    p.GetLabels(),
	}
}

// TimeSeries represents a series of data points for a metric.
type TimeSeries struct {
	MetricID   string            `json:"metric_id"`
	DataPoints []DataPoint       `json:"data_points"`
	Labels     map[string]string `json:"labels"`
}

// ToProto allocates sample messages and shares their label maps with the result.
func (t *TimeSeries) ToProto() *metricpb.TimeSeries {
	points := make([]*metricpb.DataPoint, 0, len(t.DataPoints))
	for i := range t.DataPoints {
		points = append(points, t.DataPoints[i].ToProto())
	}
	return &metricpb.TimeSeries{
		MetricId:   t.MetricID,
		DataPoints: points,
		Labels:     t.Labels,
	}
}

// TimeSeriesFromProto decodes samples and shares label maps with the wire input.
func TimeSeriesFromProto(p *metricpb.TimeSeries) TimeSeries {
	points := make([]DataPoint, 0, len(p.GetDataPoints()))
	for _, dp := range p.GetDataPoints() {
		points = append(points, DataPointFromProto(dp))
	}
	return TimeSeries{
		MetricID:   p.GetMetricId(),
		DataPoints: points,
		Labels:     p.GetLabels(),
	}
}

// CurrentValue represents the current value of a metric.
type CurrentValue struct {
	MetricID  string            `json:"metric_id"`
	Value     float64           `json:"value"`
	Timestamp time.Time         `json:"timestamp"`
	Labels    map[string]string `json:"labels"`
}

// ToProto encodes the value and timestamp; the labels map remains shared.
func (c *CurrentValue) ToProto() *metricpb.CurrentValue {
	return &metricpb.CurrentValue{
		MetricId:  c.MetricID,
		Value:     c.Value,
		Timestamp: timestamppb.New(c.Timestamp),
		Labels:    c.Labels,
	}
}

// CurrentValueFromProto shares labels and uses zero time for a missing timestamp.
func CurrentValueFromProto(p *metricpb.CurrentValue) CurrentValue {
	var ts time.Time
	if p.GetTimestamp() != nil {
		ts = p.GetTimestamp().AsTime()
	}
	return CurrentValue{
		MetricID:  p.GetMetricId(),
		Value:     p.GetValue(),
		Timestamp: ts,
		Labels:    p.GetLabels(),
	}
}

// AggregateValue represents aggregated metric data over a window.
type AggregateValue struct {
	MetricID string            `json:"metric_id"`
	Min      float64           `json:"min"`
	Max      float64           `json:"max"`
	Avg      float64           `json:"avg"`
	Sum      float64           `json:"sum"`
	P50      float64           `json:"p50"`
	P90      float64           `json:"p90"`
	P99      float64           `json:"p99"`
	Count    int64             `json:"count"`
	Window   time.Duration     `json:"window"`
	Labels   map[string]string `json:"labels"`
}

// ToProto encodes the statistical window and shares the labels map.
func (a *AggregateValue) ToProto() *metricpb.AggregateValue {
	return &metricpb.AggregateValue{
		MetricId: a.MetricID,
		Min:      a.Min,
		Max:      a.Max,
		Avg:      a.Avg,
		Sum:      a.Sum,
		P50:      a.P50,
		P90:      a.P90,
		P99:      a.P99,
		Count:    a.Count,
		Window:   durationpb.New(a.Window),
		Labels:   a.Labels,
	}
}

// AggregateValueFromProto shares labels and uses zero duration for a missing window.
func AggregateValueFromProto(p *metricpb.AggregateValue) AggregateValue {
	var window time.Duration
	if p.GetWindow() != nil {
		window = p.GetWindow().AsDuration()
	}
	return AggregateValue{
		MetricID: p.GetMetricId(),
		Min:      p.GetMin(),
		Max:      p.GetMax(),
		Avg:      p.GetAvg(),
		Sum:      p.GetSum(),
		P50:      p.GetP50(),
		P90:      p.GetP90(),
		P99:      p.GetP99(),
		Count:    p.GetCount(),
		Window:   window,
		Labels:   p.GetLabels(),
	}
}

// MetricResult wraps one of the possible metric result types.
type MetricResult struct {
	TimeSeries     *TimeSeries     `json:"time_series,omitempty"`
	CurrentValue   *CurrentValue   `json:"current_value,omitempty"`
	AggregateValue *AggregateValue `json:"aggregate_value,omitempty"`
}

// ToProto encodes the first present result in series, current, aggregate order.
func (r *MetricResult) ToProto() *metricpb.MetricResult {
	result := &metricpb.MetricResult{}
	if r.TimeSeries != nil {
		result.Result = &metricpb.MetricResult_TimeSeries{TimeSeries: r.TimeSeries.ToProto()}
	} else if r.CurrentValue != nil {
		result.Result = &metricpb.MetricResult_CurrentValue{CurrentValue: r.CurrentValue.ToProto()}
	} else if r.AggregateValue != nil {
		result.Result = &metricpb.MetricResult_AggregateValue{AggregateValue: r.AggregateValue.ToProto()}
	}
	return result
}

// MetricResultFromProto decodes the selected wire variant; absent variants yield zero.
func MetricResultFromProto(p *metricpb.MetricResult) MetricResult {
	r := MetricResult{}
	switch v := p.GetResult().(type) {
	case *metricpb.MetricResult_TimeSeries:
		ts := TimeSeriesFromProto(v.TimeSeries)
		r.TimeSeries = &ts
	case *metricpb.MetricResult_CurrentValue:
		cv := CurrentValueFromProto(v.CurrentValue)
		r.CurrentValue = &cv
	case *metricpb.MetricResult_AggregateValue:
		av := AggregateValueFromProto(v.AggregateValue)
		r.AggregateValue = &av
	}
	return r
}

// ===================== Request/Response Types ===================== //

// QueryRequest is the Go-side representation of a metric query.
type QueryRequest struct {
	ResourceKey       string                        `json:"resource_key"`
	ResourceID        string                        `json:"resource_id"`
	ResourceNamespace string                        `json:"resource_namespace"`
	ResourceData      *resourcev2.Value             `json:"resource_data,omitempty"`
	Reference         *resourcev2.ResourceReference `json:"reference,omitempty"`
	Route             *resourcev2.AccessRoute       `json:"route,omitempty"`
	MetricIDs         []string                      `json:"metric_ids"`
	Shape             MetricShape                   `json:"shape"`
	StartTime         time.Time                     `json:"start_time"`
	EndTime           time.Time                     `json:"end_time"`
	Step              time.Duration                 `json:"step"`
	Params            map[string]string             `json:"params"`
}

// QueryResponse is the Go-side representation of a metric query response.
type QueryResponse struct {
	Success bool           `json:"success"`
	Results []MetricResult `json:"results"`
	Error   string         `json:"error"`
}

// QueryResponseFromProto decodes results; nil input produces an unsuccessful empty response.
func QueryResponseFromProto(p *metricpb.QueryMetricsResponse) *QueryResponse {
	results := make([]MetricResult, 0, len(p.GetResults()))
	for _, r := range p.GetResults() {
		results = append(results, MetricResultFromProto(r))
	}
	return &QueryResponse{
		Success: p.GetSuccess(),
		Results: results,
		Error:   p.GetError(),
	}
}

// ToProto copies result containers; nested label maps remain shared.
func (r *QueryResponse) ToProto() *metricpb.QueryMetricsResponse {
	results := make([]*metricpb.MetricResult, 0, len(r.Results))
	for i := range r.Results {
		results = append(results, r.Results[i].ToProto())
	}
	return &metricpb.QueryMetricsResponse{
		Success: r.Success,
		Results: results,
		Error:   r.Error,
	}
}

// ===================== Stream Types ===================== //

// StreamInput is a control message for the metric stream.
type StreamInput struct {
	SubscriptionID    string                        `json:"subscription_id"`
	Command           MetricStreamCommand           `json:"command"`
	ResourceKey       string                        `json:"resource_key"`
	ResourceID        string                        `json:"resource_id"`
	ResourceNamespace string                        `json:"resource_namespace"`
	ResourceData      *resourcev2.Value             `json:"resource_data,omitempty"`
	Reference         *resourcev2.ResourceReference `json:"reference,omitempty"`
	Route             *resourcev2.AccessRoute       `json:"route,omitempty"`
	MetricIDs         []string                      `json:"metric_ids"`
	Interval          time.Duration                 `json:"interval"`
	Params            map[string]string             `json:"params"`
	CatalogRevision   string                        `json:"catalog_revision,omitempty"`
}

// StreamOutput is a metric data push from the stream.
type StreamOutput struct {
	SubscriptionID string                             `json:"subscription_id"`
	Results        []MetricResult                     `json:"results"`
	Timestamp      time.Time                          `json:"timestamp"`
	Error          string                             `json:"error"`
	OperationError *resourcev2.ResourceOperationError `json:"operation_error,omitempty"`
}

// ===================== Context Types ===================== //

// QueryContext provides context for metric queries. Wraps the plugin context
// plus connection information passed via gRPC metadata.
type QueryContext struct {
	context.Context `json:"-"`
	PluginID        string
	ConnectionID    string
	Connection      *types.Connection
}

// StreamContext provides context for metric streams.
type StreamContext struct {
	PluginID        string
	context.Context `json:"-"`
	ConnectionID    string
	Connection      *types.Connection
}
