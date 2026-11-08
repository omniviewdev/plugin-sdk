package metric

import resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"

// ValidFiniteUnit accepts physical base units without inferring conversions.
func ValidFiniteUnit(unit string) bool {
	switch unit {
	case "cores", "bytes", "seconds", "count", "ratio", "percent", "bytes_per_second", "count_per_second":
		return true
	default:
		return false
	}
}

// ValidateMetricDefinitionContract checks portable finite semantics, not a query
// language. A range-reuse declaration must name the range shape.
// Source providers additionally validate compilation and exact identity attribution.
func ValidateMetricDefinitionContract(d MetricDefinition) error {
	if err := resourcev2.ValidateMetadata(d); err != nil {
		return err
	}
	if d.ID == "" || d.Name == "" || !ValidFiniteUnit(d.Unit) || len(d.ResourceKeys) == 0 || len(d.ResourceKeys) > 32 || len(d.FieldBindings) > 32 || len(d.IdentityLabels) > 32 || len(d.Dimensions) > 32 || len(d.SupportedShapes) == 0 || len(d.SupportedShapes) > 2 {
		return metricError("INVALID_REQUEST", "Invalid finite metric definition.")
	}
	for _, b := range d.FieldBindings {
		if b.Field == "" || b.Label == "" {
			return metricError("INVALID_REQUEST", "Finite field bindings require named fields and labels.")
		}
		if b.Source != "resource" && b.Source != "scope" {
			return metricError("INVALID_REQUEST", "Unsupported finite field source.")
		}
		if b.Source == "resource" && b.ResourcePath == "" {
			return metricError("INVALID_REQUEST", "Resource bindings require an explicit resource JSON pointer.")
		}
		if b.ResourcePath != "" {
			if b.Source != "resource" || b.ResourcePath[0] != '/' {
				return metricError("INVALID_REQUEST", "Resource paths require an owner resource JSON pointer.")
			}
			for i := 0; i < len(b.ResourcePath); i++ {
				if b.ResourcePath[i] == '~' {
					i++
					if i == len(b.ResourcePath) || (b.ResourcePath[i] != '0' && b.ResourcePath[i] != '1') {
						return metricError("INVALID_REQUEST", "Invalid resource JSON pointer escape.")
					}
				}
			}
		}
		switch b.Transform {
		case "", MetricTransformIdentity, MetricTransformLowercase, MetricTransformUppercase:
		default:
			return metricError("INVALID_REQUEST", "Unsupported finite field transform.")
		}
	}
	shapes := map[string]bool{}
	for _, s := range d.SupportedShapes {
		if (s != "current" && s != "range") || shapes[s] {
			return metricError("INVALID_REQUEST", "Unsupported finite definition shape.")
		}
		shapes[s] = true
	}
	if d.RangeReuse && !shapes["range"] {
		return metricError("INVALID_REQUEST", "Range reuse requires the range shape.")
	}
	return nil
}

// ValidateMetricQueryContext bounds host-provided mapping material before execution.
func ValidateMetricQueryContext(c MetricQueryContext) error {
	if len(c.Definitions) > MaxFiniteMetrics || len(c.PublicScope) > 32 {
		return metricError("RESOURCE_LIMIT", "Finite mapping context exceeds limits.")
	}
	for k, v := range c.PublicScope {
		if k == "" || len(v) > 1024 {
			return metricError("INVALID_REQUEST", "Invalid public metric scope entry.")
		}
	}
	ids := map[string]bool{}
	for _, d := range c.Definitions {
		if ids[d.ID] {
			return metricError("INVALID_REQUEST", "Duplicate finite mapping.")
		}
		ids[d.ID] = true
		if err := ValidateMetricDefinitionContract(d); err != nil {
			return err
		}
	}
	return resourcev2.ValidateMetadata(c)
}

// ValidateResourceMetricsCatalogRequest validates exact source selection before I/O.
func ValidateResourceMetricsCatalogRequest(r ResourceMetricsCatalogRequest) error {
	if r.ResourceKey == "" {
		return metricError("INVALID_REQUEST", "Finite catalog resource key is required.")
	}
	if err := validateFiniteSource(r.Source); err != nil {
		return err
	}
	if err := ValidateMetricQueryContext(r.Context); err != nil {
		return err
	}
	return resourcev2.ValidateMetadata(r)
}
