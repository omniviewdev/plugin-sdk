package resourcev2

import (
	"encoding/json"
	"math/big"
)

// MaxActivityPresentationBytes bounds the JSON representation on every activity transport.
const MaxActivityPresentationBytes = 131072

// ValidateActivityPresentation checks presentation without interpreting business output.
func ValidateActivityPresentation(p ActivityPresentation) error {
	if err := ValidateMetadata(p); err != nil {
		return err
	}
	if p.Title == "" || len(p.Title) > 512 || len(p.Message) > 4096 || len(p.Details) > 65536 || len(p.Metrics) > 16 || len(p.Actions) > 8 {
		return NewError("INVALID_REQUEST", "Invalid activity presentation.")
	}
	if p.Progress != nil {
		v := p.Progress
		switch v.Kind {
		case "indeterminate":
			if v.Current != "" || v.Total != "" || v.Unit != "" || v.Label != "" {
				return NewError("INVALID_REQUEST", "Invalid indeterminate progress.")
			}
		case "units", "steps":
			current, currentOK := new(big.Int).SetString(v.Current, 10)
			total, totalOK := new(big.Int).SetString(v.Total, 10)
			if !currentOK || !totalOK || current.Sign() < 0 || total.Sign() <= 0 || current.Cmp(total) > 0 || current.String() != v.Current || total.String() != v.Total || len(v.Current) > 128 || len(v.Total) > 128 || v.Message != "" {
				return NewError("INVALID_REQUEST", "Invalid activity progress.")
			}
			if v.Kind == "units" && (v.Unit == "" || v.Label != "") {
				return NewError("INVALID_REQUEST", "Invalid unit progress.")
			}
			if v.Kind == "steps" && (v.Label == "" || v.Unit != "" || !total.IsInt64() || total.Int64() > 1000000) {
				return NewError("INVALID_REQUEST", "Invalid step progress.")
			}
		default:
			return NewError("INVALID_REQUEST", "Unknown activity progress kind.")
		}
	}
	ids := map[string]bool{}
	for _, metric := range p.Metrics {
		if metric.ID == "" || ids[metric.ID] || metric.Label == "" || metric.Value == "" || !activityTone(metric.Tone) {
			return NewError("INVALID_REQUEST", "Invalid activity metric.")
		}
		ids[metric.ID] = true
	}
	ids = map[string]bool{}
	for _, action := range p.Actions {
		if action.ID == "" || ids[action.ID] || action.Label == "" {
			return NewError("INVALID_REQUEST", "Invalid activity action.")
		}
		ids[action.ID] = true
		switch action.Kind {
		case "navigate-resource":
			if action.Target == nil || action.Intent != nil || action.TaskID != "" || action.Target.PluginID == "" || action.Target.TargetID == "" || action.Target.Representation == "" {
				return NewError("INVALID_REQUEST", "Invalid navigation intent.")
			}
			if _, err := ToProtoAccessRoute(action.Target.Route); err != nil {
				return err
			}
			if err := ValidateAddress(action.Target.Reference.Address); err != nil {
				return err
			}
		case "resource-action":
			if action.Intent == nil || action.Target != nil || action.TaskID != "" || action.Intent.PluginID == "" {
				return NewError("INVALID_REQUEST", "Invalid resource action intent.")
			}
			if err := ValidateActionRequest(action.Intent.Request); err != nil {
				return err
			}
		case "cancel-task":
			if action.TaskID == "" || action.Target != nil || action.Intent != nil {
				return NewError("INVALID_REQUEST", "Invalid cancellation intent.")
			}
		default:
			return NewError("INVALID_REQUEST", "Unknown activity action kind.")
		}
	}
	encoded, err := json.Marshal(p)
	if err != nil || len(encoded) > MaxActivityPresentationBytes {
		return NewError("INVALID_REQUEST", "Activity presentation exceeds limit.")
	}
	return nil
}

func activityTone(v string) bool {
	return v == "" || v == "info" || v == "success" || v == "warning" || v == "error"
}
