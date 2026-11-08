package metric

import (
	"math"
	"time"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// Finite protocol bounds apply before dispatch and again before publication.
const (
	MaxFiniteTargets = 256
	MaxFiniteMetrics = 32
	MaxFinitePoints  = 10000
	MaxFiniteRange   = 31 * 24 * time.Hour
)

// MetricSourceRef identifies the selected source, not the resource owner.
// Native sources have no datasource kind or UID.
type MetricSourceRef struct {
	PluginID       string `json:"plugin_id"`
	ConnectionID   string `json:"connection_id"`
	DatasourceUID  string `json:"datasource_uid"`
	DatasourceKind string `json:"datasource_kind"`
	Native         bool   `json:"native,omitempty"`
}

// FiniteMetricDescriptor declares physical base units and supported evaluations.
// Units use ValidFiniteUnit; shapes are current or range, never legacy enum numbers.
type FiniteMetricDescriptor struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	ResourceKey string   `json:"resource_key"`
	Unit        string   `json:"unit"`
	Shapes      []string `json:"shapes"`
	Description string   `json:"description"`
	// Source overrides the catalog default for host-composed mixed-source lanes.
	Source *MetricSourceRef `json:"source,omitempty"`
	// RangeReuse is the provider's opt-in to coverage-aware range reuse. It
	// requires the range shape; false or absent never permits reuse. Setting it
	// guarantees that every ok range result for this metric is a pure point
	// series: Points are pointwise evaluations on the grid StartTime+n*Step, and
	// for the same query context and Step each point's value and presence do not
	// depend on StartTime, EndTime or which other points were requested.
	// ObservedAt and Window describe one requested domain, not that series, so a
	// host reuses points across different bounds only from ok results that leave
	// both unset and whose IdentityScope, Source and Dimensions stay compatible;
	// any other result is valid only for its exact domain. Reuse does not imply
	// that a range point equals a current sample or that source history never
	// changes: the host's cache TTL still governs staleness. The descriptor, not
	// an owner's MetricDefinition.RangeReuse declaration, is the opt-in, and a
	// provider may decline that declaration.
	RangeReuse bool `json:"range_reuse,omitempty"`
}

// ResourceMetricTarget binds a caller correlation token to an admitted reference.
// Token does not confer authority. Route must match the selected connection.
type ResourceMetricTarget struct {
	Token     string                       `json:"token"`
	Reference resourcev2.ResourceReference `json:"reference"`
	Route     resourcev2.AccessRoute       `json:"route"`
	// PublicData contains only host-approved scalar fields from owner observations.
	// Providers must never interpret this projection as resource authority.
	PublicData map[string]string `json:"public_data,omitempty"`
}

// ResourceMetricsQuery evaluates every requested target/metric pair once.
// Current forbids range fields. Range requires ordered times and positive Step.
type ResourceMetricsQuery struct {
	Source    MetricSourceRef        `json:"source"`
	Targets   []ResourceMetricTarget `json:"targets"`
	MetricIDs []string               `json:"metric_ids"`
	Shape     string                 `json:"shape"`
	StartTime time.Time              `json:"start_time"`
	EndTime   time.Time              `json:"end_time"`
	Step      time.Duration          `json:"step"`
	Context   MetricQueryContext     `json:"context"`
}

// ResourceMetricPoint carries an evaluation timestamp and a physical base-unit
// value. It does not establish the age of the underlying source sample.
// A nil Value is a gap, never zero. NaN and infinity are invalid.
type ResourceMetricPoint struct {
	Time  time.Time `json:"time"`
	Value *float64  `json:"value"`
}

// ResourceMetricResult reports one target/metric evaluation. ObservedAt and
// Window come from the source; absence must never be replaced by receipt time.
// Non-ok statuses carry no numeric data. Missing is not a zero-valued sample.
type ResourceMetricResult struct {
	ResourceToken string                `json:"resource_token"`
	MetricID      string                `json:"metric_id"`
	Status        string                `json:"status"`
	Value         *float64              `json:"value"`
	Points        []ResourceMetricPoint `json:"points"`
	Dimensions    map[string]string     `json:"dimensions"`
	ObservedAt    *time.Time            `json:"observed_at"`
	Window        time.Duration         `json:"window"`
	// IdentityScope describes source attribution, not host cache fencing.
	// \"incarnation\" means source verified UID; \"name\" means name-only attribution.
	IdentityScope string `json:"identity_scope"`
	Message       string `json:"message"`
	// Source is exact provenance. The host fills external and mixed-source results.
	// A single owner-native lane may omit this redundant field; use its catalog
	// source. Consumers resolve result, descriptor, then catalog source.
	Source *MetricSourceRef `json:"source,omitempty"`
}

// ResourceMetricsResponse separates completeness from individual result status.
// Complete requires exactly one result for every requested target/metric pair.
type ResourceMetricsResponse struct {
	Results  []ResourceMetricResult `json:"results"`
	Complete bool                   `json:"complete"`
	Warnings []string               `json:"warnings"`
}

// ResourceMetricsCatalog is authoritative for source units, shapes and batch size.
type ResourceMetricsCatalog struct {
	Source     MetricSourceRef          `json:"source"`
	Metrics    []FiniteMetricDescriptor `json:"metrics"`
	MaxTargets int                      `json:"max_targets"`
	// MaxSampleAge is the source's live-data age policy, not cache TTL.
	// Zero leaves freshness unknown. Range completeness is independent of live age.
	MaxSampleAge time.Duration `json:"max_sample_age"`
}

// FiniteQueryProvider is optional and independent of legacy Query/Stream.
// Implementations must honor cancellation and must not fabricate missing samples.
type FiniteQueryProvider interface {
	GetResourceMetricsCatalog(*types.PluginContext, ResourceMetricsCatalogRequest) (*ResourceMetricsCatalog, error)
	QueryResources(*types.PluginContext, ResourceMetricsQuery) (*ResourceMetricsResponse, error)
}

// ResourceMetricsCatalogFunc returns descriptors for one canonical resource key.
type ResourceMetricsCatalogFunc func(*types.PluginContext, ResourceMetricsCatalogRequest) (*ResourceMetricsCatalog, error)

// QueryResourcesFunc receives the source-owned leased connection context.
// All target admissions remain leased until the callback returns.
type QueryResourcesFunc func(*types.PluginContext, ResourceMetricsQuery) (*ResourceMetricsResponse, error)

func validateFiniteSource(s MetricSourceRef) error {
	if s.PluginID == "" || s.ConnectionID == "" {
		return metricError("INVALID_REQUEST", "Finite source identity is required.")
	}
	if s.Native {
		if s.DatasourceKind != "" || s.DatasourceUID != "" {
			return metricError("INVALID_REQUEST", "Native source cannot have a datasource kind or UID.")
		}
	} else if s.DatasourceKind == "" || s.DatasourceUID == "" {
		return metricError("INVALID_REQUEST", "External source requires a datasource kind and UID.")
	}
	return resourcev2.ValidateMetadata(s)
}

// ValidateResourceMetricsQuery checks finite domains, duplicate tokens and bounds.
// Dispatch additionally checks authority against the configured connection registry.
func ValidateResourceMetricsQuery(q ResourceMetricsQuery) error {
	if err := validateFiniteSource(q.Source); err != nil {
		return err
	}
	if err := ValidateMetricQueryContext(q.Context); err != nil {
		return err
	}
	if len(q.Targets) == 0 || len(q.Targets) > MaxFiniteTargets || len(q.MetricIDs) == 0 || len(q.MetricIDs) > MaxFiniteMetrics {
		return metricError("RESOURCE_LIMIT", "Finite target or metric count is outside bounds.")
	}
	tokens := make(map[string]bool, len(q.Targets))
	for _, t := range q.Targets {
		if t.Token == "" || tokens[t.Token] {
			return metricError("INVALID_REQUEST", "Finite target tokens must be nonempty and unique.")
		}
		tokens[t.Token] = true
		if err := validateQuery(QueryRequest{Reference: &t.Reference, Route: &t.Route}, nil); err != nil {
			return err
		}
		if t.Route.Connection == "" {
			return metricError("INVALID_REQUEST", "Finite targets require routes.")
		}
	}
	ids := make(map[string]bool, len(q.MetricIDs))
	for _, id := range q.MetricIDs {
		if id == "" || ids[id] {
			return metricError("INVALID_REQUEST", "Finite metric IDs must be nonempty and unique.")
		}
		ids[id] = true
	}
	switch q.Shape {
	case "current":
		if !q.StartTime.IsZero() || !q.EndTime.IsZero() || q.Step != 0 {
			return metricError("INVALID_REQUEST", "Current query cannot carry a range.")
		}
	case "range":
		if q.StartTime.IsZero() || q.EndTime.IsZero() || !q.EndTime.After(q.StartTime) || q.EndTime.Sub(q.StartTime) > MaxFiniteRange || q.Step <= 0 || q.Step > MaxFiniteRange || q.EndTime.Sub(q.StartTime)/q.Step >= MaxFinitePoints {
			return metricError("INVALID_REQUEST", "Invalid finite range or sample bound.")
		}
	default:
		return metricError("INVALID_REQUEST", "Unknown finite shape.")
	}
	if !q.StartTime.IsZero() && finiteTime(q.StartTime).CheckValid() != nil || !q.EndTime.IsZero() && finiteTime(q.EndTime).CheckValid() != nil {
		return metricError("INVALID_REQUEST", "Finite range time is outside the wire domain.")
	}
	return resourcev2.ValidateMetadata(q)
}

// ValidateResourceMetricsCatalog rejects unknown units/shapes, duplicate IDs and
// range-reuse opt-in without the range shape.
func ValidateResourceMetricsCatalog(c *ResourceMetricsCatalog) error {
	if c == nil {
		return metricError("INVALID_REQUEST", "Missing finite catalog.")
	}
	if err := validateFiniteSource(c.Source); err != nil {
		return err
	}
	if c.MaxTargets < 1 || c.MaxTargets > MaxFiniteTargets || len(c.Metrics) > MaxFiniteMetrics {
		return metricError("RESOURCE_LIMIT", "Invalid finite catalog bounds.")
	}
	if c.MaxSampleAge < 0 || c.MaxSampleAge > MaxFiniteRange {
		return metricError("INVALID_REQUEST", "Invalid source sample-age policy.")
	}
	ids := map[string]bool{}
	for _, d := range c.Metrics {
		if d.Source != nil {
			if err := validateFiniteSource(*d.Source); err != nil {
				return err
			}
		}
		if d.ID == "" || ids[d.ID] || d.ResourceKey == "" || !ValidFiniteUnit(d.Unit) || len(d.Shapes) == 0 || len(d.Shapes) > 2 {
			return metricError("INVALID_REQUEST", "Invalid finite descriptor.")
		}
		ids[d.ID] = true
		shapes := map[string]bool{}
		for _, s := range d.Shapes {
			if (s != "current" && s != "range") || shapes[s] {
				return metricError("INVALID_REQUEST", "Invalid finite descriptor shapes.")
			}
			shapes[s] = true
		}
		if d.RangeReuse && !shapes["range"] {
			return metricError("INVALID_REQUEST", "Range reuse requires the range shape.")
		}
	}
	return resourcev2.ValidateMetadata(c)
}

func finiteNumber(v *float64) bool { return v == nil || (!math.IsNaN(*v) && !math.IsInf(*v, 0)) }

// ValidateResourceMetricsResponse rejects unknown/duplicate pairs and invalid data.
// Incomplete responses may omit pairs; they cannot claim Complete when pairs lack.
func ValidateResourceMetricsResponse(q ResourceMetricsQuery, r *ResourceMetricsResponse) error {
	if r == nil {
		return metricError("INVALID_REQUEST", "Missing finite response.")
	}
	if len(r.Results) > len(q.Targets)*len(q.MetricIDs) {
		return metricError("RESOURCE_LIMIT", "Too many finite results.")
	}
	tokens := map[string]bool{}
	ids := map[string]bool{}
	for _, t := range q.Targets {
		tokens[t.Token] = true
	}
	for _, id := range q.MetricIDs {
		ids[id] = true
	}
	seen := map[[2]string]bool{}
	points := 0
	for _, v := range r.Results {
		if v.Source != nil && *v.Source != q.Source {
			return metricError("INVALID_REQUEST", "Result source conflicts with selected query source.")
		}
		key := [2]string{v.ResourceToken, v.MetricID}
		if !tokens[v.ResourceToken] || !ids[v.MetricID] || seen[key] {
			return metricError("INVALID_REQUEST", "Unknown or duplicate finite result.")
		}
		seen[key] = true
		switch v.Status {
		case "ok", "missing", "unsupported", "denied", "invalid_context", "invalid_mapping", "ambiguous", "failed":
		default:
			return metricError("INVALID_REQUEST", "Unknown finite result status.")
		}
		if !finiteNumber(v.Value) || v.Window < 0 || (v.ObservedAt != nil && v.ObservedAt.IsZero()) {
			return metricError("INVALID_REQUEST", "Invalid finite value or observation metadata.")
		}
		if v.ObservedAt != nil && !v.ObservedAt.IsZero() && finiteTime(*v.ObservedAt).CheckValid() != nil {
			return metricError("INVALID_REQUEST", "Finite observation time is outside the wire domain.")
		}
		if v.Status != "ok" && (v.Value != nil || len(v.Points) > 0) {
			return metricError("INVALID_REQUEST", "Non-ok result carries numeric data.")
		}
		if q.Shape == "current" && len(v.Points) > 0 || q.Shape == "range" && v.Value != nil {
			return metricError("INVALID_REQUEST", "Finite result shape mismatch.")
		}
		if v.Status == "ok" && q.Shape == "current" && v.Value == nil {
			return metricError("INVALID_REQUEST", "Current sample requires a value.")
		}
		if v.IdentityScope != "" && v.IdentityScope != "name" && v.IdentityScope != "incarnation" {
			return metricError("INVALID_REQUEST", "Invalid finite identity scope.")
		}
		points += len(v.Points)
		if points > MaxFinitePoints {
			return metricError("RESOURCE_LIMIT", "Finite response sample limit exceeded.")
		}
		var previous time.Time
		for _, p := range v.Points {
			if p.Time.IsZero() || !finiteNumber(p.Value) || p.Time.Before(q.StartTime) || p.Time.After(q.EndTime) || (!previous.IsZero() && !p.Time.After(previous)) {
				return metricError("INVALID_REQUEST", "Invalid finite point domain.")
			}
			previous = p.Time
		}
	}
	if r.Complete && len(seen) != len(q.Targets)*len(q.MetricIDs) {
		return metricError("INVALID_REQUEST", "Complete finite response omits requested pairs.")
	}
	return resourcev2.ValidateMetadata(r)
}
