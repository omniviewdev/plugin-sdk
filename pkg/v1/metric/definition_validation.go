package metric

import resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"

// ValidateMetricDefinitionContract validates portable semantics. Providers also
// validate their query language, schema paths, identity rules and execution plan.
func ValidateMetricDefinitionContract(definition MetricDefinition) error {
	if err := resourcev2.ValidateMetadata(definition); err != nil {
		return err
	}
	if definition.ID == "" || definition.Name == "" || definition.Unit < UnitNone || definition.Unit > UnitCores {
		return metricError("INVALID_REQUEST", "Metric definition requires stable identity and canonical unit.")
	}
	for _, binding := range definition.FieldBindings {
		if binding.ResourcePath != "" {
			if binding.Source != "resource" || binding.ResourcePath[0] != '/' {
				return metricError("INVALID_REQUEST", "Resource paths require a resource binding and JSON pointer.")
			}
			for i := 0; i < len(binding.ResourcePath); i++ {
				if binding.ResourcePath[i] == '~' {
					i++
					if i == len(binding.ResourcePath) || (binding.ResourcePath[i] != '0' && binding.ResourcePath[i] != '1') {
						return metricError("INVALID_REQUEST", "Invalid resource JSON pointer escape.")
					}
				}
			}
		}
		switch binding.Transform {
		case "", MetricTransformIdentity, MetricTransformLowercase, MetricTransformUppercase:
		default:
			return metricError("INVALID_REQUEST", "Unsupported metric field transform.")
		}
	}
	for _, shape := range definition.SupportedShapes {
		if shape < ShapeCurrent || shape > ShapeAggregate {
			return metricError("INVALID_REQUEST", "Unsupported metric definition shape.")
		}
	}
	return nil
}
