package metric

import (
	"bytes"
	"encoding/json"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"io"
)

// DecodeMetricContract performs strict shared-domain JSON admission.
func DecodeMetricContract(data []byte, target any) error {
	if err := resourcev2.ValidateJSON(data); err != nil {
		return err
	}
	if len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
		return metricError("INVALID_REQUEST", "Metric contract requires an object.")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return metricError("INVALID_REQUEST", err.Error())
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return metricError("INVALID_REQUEST", "Trailing metric JSON.")
	}
	return resourcev2.ValidateMetadata(target)
}

// ValidateResourceSetQuery validates provider inputs without admitting foreign
// references against the provider catalog. Resource authorization is host-owned.
func ValidateResourceSetQuery(r ResourceSetQueryRequest) error {
	if err := resourcev2.ValidateMetadata(r); err != nil {
		return err
	}
	if r.ResourcePluginID == "" || r.ResourceConnectionID == "" || r.Source.PluginID == "" || r.Source.ConnectionID == "" {
		return metricError("INVALID_REQUEST", "Resource owner and provider connection are required.")
	}
	if len(r.Resources) == 0 || len(r.Resources) > 1024 || len(r.MetricIDs) == 0 || len(r.MetricIDs) > 128 {
		return metricError("RESOURCE_LIMIT", "An explicit bounded resource and metric set is required.")
	}
	if r.Shape < ShapeCurrent || r.Shape > ShapeAggregate || r.Step < 0 || r.EndTime.Before(r.StartTime) {
		return metricError("INVALID_REQUEST", "Invalid metric shape or time range.")
	}
	tokens := make(map[string]bool, len(r.Resources))
	for _, item := range r.Resources {
		if item.Token == "" || item.ResourceID == "" || item.ResourceKey == "" || tokens[item.Token] {
			return metricError("INVALID_REQUEST", "Resource tokens must be unique and resources explicit.")
		}
		tokens[item.Token] = true
		if item.Route != nil && item.Route.Connection != r.ResourceConnectionID {
			return metricError("INVALID_REQUEST", "Resource route conflicts with resource owner connection.")
		}
		if item.Reference != nil {
			if err := resourcev2.ValidateAddress(item.Reference.Address); err != nil {
				return err
			}
			if item.Reference.Address.Type != item.ResourceKey {
				return metricError("INVALID_REQUEST", "Resource type conflicts with canonical reference.")
			}
		}
		if item.Reference != nil && item.Reference.Incarnation != nil && *item.Reference.Incarnation == "" {
			return metricError("INVALID_REQUEST", "Empty resource incarnation.")
		}
		if item.PublicData != nil {
			if err := resourcev2.ValidateValue(*item.PublicData); err != nil {
				return err
			}
		}
	}
	ids := make(map[string]bool, len(r.MetricIDs))
	definitions := make(map[string]bool, len(r.Definitions))
	for _, definition := range r.Definitions {
		if err := ValidateMetricDefinitionContract(definition); err != nil {
			return err
		}
		if definitions[definition.ID] {
			return metricError("INVALID_REQUEST", "Duplicate metric definition identity.")
		}
		definitions[definition.ID] = true
	}
	for _, id := range r.MetricIDs {
		if id == "" || ids[id] {
			return metricError("INVALID_REQUEST", "Metric IDs must be unique and nonempty.")
		}
		ids[id] = true
	}
	return nil
}

// ValidateResourceSetResponse rejects ambiguous identities and absent-as-zero data.
func ValidateResourceSetResponse(req ResourceSetQueryRequest, response *ResourceSetQueryResponse) error {
	if response == nil {
		return metricError("INVALID_REQUEST", "Provider returned no resource-set response.")
	}
	if err := resourcev2.ValidateMetadata(response); err != nil {
		return err
	}
	resources := make(map[string]bool, len(req.Resources))
	metrics := make(map[string]bool, len(req.MetricIDs))
	seen := make(map[[3]string]bool)
	declared := make(map[string]map[string]bool, len(req.Definitions))
	for _, definition := range req.Definitions {
		dimensions := make(map[string]bool, len(definition.Dimensions))
		for _, name := range definition.Dimensions {
			dimensions[name] = true
		}
		declared[definition.ID] = dimensions
	}
	for _, r := range req.Resources {
		resources[r.Token] = true
	}
	for _, id := range req.MetricIDs {
		metrics[id] = true
	}
	for _, result := range response.Results {
		for name := range result.Dimensions {
			if !declared[result.MetricID][name] {
				return metricError("INVALID_REQUEST", "Provider returned an undeclared metric dimension.")
			}
		}
		// encoding/json sorts string map keys, giving order-independent identity.
		var dimensions []byte
		if len(result.Dimensions) != 0 {
			var err error
			dimensions, err = json.Marshal(result.Dimensions)
			if err != nil {
				return metricError("INVALID_REQUEST", "Invalid metric dimensions.")
			}
		}
		key := [3]string{result.ResourceToken, result.MetricID, string(dimensions)}
		if !resources[result.ResourceToken] || !metrics[result.MetricID] || seen[key] {
			return metricError("INVALID_REQUEST", "Provider returned unknown or duplicate metric identity.")
		}
		seen[key] = true
		switch result.Status {
		case MetricStatusOK, MetricStatusPartial, MetricStatusStale:
			if result.Value == nil {
				return metricError("INVALID_REQUEST", "Available metric requires a value.")
			}
		case MetricStatusMissing, MetricStatusDenied, MetricStatusFailed, MetricStatusUnsupported, MetricStatusInvalidContext, MetricStatusAmbiguous:
			if result.Value != nil {
				return metricError("INVALID_REQUEST", "Unavailable metric cannot carry a value.")
			}
		default:
			return metricError("INVALID_REQUEST", "Unknown metric result status.")
		}
		if result.Value != nil {
			n := 0
			if result.Value.CurrentValue != nil {
				n++
			}
			if result.Value.TimeSeries != nil {
				n++
			}
			if result.Value.AggregateValue != nil {
				n++
			}
			if n != 1 {
				return metricError("INVALID_REQUEST", "Metric result requires exactly one value shape.")
			}
		}
	}
	return nil
}
