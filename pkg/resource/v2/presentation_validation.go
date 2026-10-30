package resourcev2

import (
	"math"
	"sort"
)

// ValidateResourceDefinition bounds and admits presentation metadata before
// transport serialization. Extractors never confer identity or access.
func ValidateResourceDefinition(v ResourceDefinition) error {
	if err := admission(v); err != nil {
		return err
	}
	seen := map[string]bool{}
	for i := range v.ColumnDefs {
		column := &v.ColumnDefs[i]
		if err := text(column.ID, true); err != nil {
			return err
		}
		if seen[column.ID] {
			return invalid("duplicate presentation column")
		}
		seen[column.ID] = true
		if column.Width < 0 {
			return invalid("negative presentation column width")
		}
		if column.ResourceLink != nil {
			if err := validateResourceLink(*column.ResourceLink); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateResourceLink(v ResourceLink) error {
	if err := text(v.IDAccessor, true); err != nil {
		return err
	}
	if err := validateResourceLinkType(v); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, extractor := range v.ScopeAccessors {
		if err := validateScopeExtractor(extractor); err != nil {
			return err
		}
		if seen[extractor.Dimension] {
			return invalid("duplicate resource link scope dimension")
		}
		seen[extractor.Dimension] = true
	}
	return nil
}

func validateResourceLinkType(v ResourceLink) error {
	if v.Type != "" {
		if v.TypeAccessor != "" || len(v.TypeMap) != 0 {
			return invalid("resource link has conflicting type selectors")
		}
		return nil
	}
	if v.TypeAccessor == "" || len(v.TypeMap) == 0 {
		return invalid("resource link lacks explicit type mapping")
	}
	for native, canonical := range v.TypeMap {
		if err := text(native, true); err != nil {
			return err
		}
		if err := text(canonical, true); err != nil {
			return err
		}
	}
	return nil
}

// ValidateResourceGroups rejects duplicate groups and repeated resource types within each group.
func ValidateResourceGroups(v []ResourceGroup) error {
	if err := admission(v); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, group := range v {
		if err := text(group.ID, true); err != nil {
			return err
		}
		if seen[group.ID] {
			return invalid("duplicate resource group")
		}
		seen[group.ID] = true
		types := map[string]bool{}
		for _, id := range group.Resources {
			if err := text(id, true); err != nil {
				return err
			}
			if types[id] {
				return invalid("duplicate group resource type")
			}
			types[id] = true
		}
	}
	return nil
}

// ValidateActionDescriptors checks unique action IDs, explicit scopes, and bounded parameter and output schemas.
func ValidateActionDescriptors(v []ActionDescriptor) error {
	if err := admission(v); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, action := range v {
		if err := text(action.ID, true); err != nil {
			return err
		}
		if seen[action.ID] {
			return invalid("duplicate action descriptor")
		}
		seen[action.ID] = true
		switch action.Scope {
		case ActionScopeInstance, ActionScopeType:
		default:
			return invalid("unknown action scope")
		}
		for _, schema := range []*Schema{action.ParamsSchema, action.OutputSchema} {
			if schema != nil {
				if err := validateActionSchema(*schema); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateActionSchema(schema Schema) error {
	return ValidateSchema(schema)
}

func validateActionProperty(p SchemaProperty, depth int) error {
	if depth > MaxValueDepth {
		return limited("action schema depth limit")
	}
	switch p.Type {
	case SchemaString, SchemaInteger, SchemaNumber, SchemaBoolean, SchemaObject, SchemaArray:
	default:
		return invalid("unknown action schema type")
	}
	if err := validateActionEnum(p); err != nil {
		return err
	}
	if (p.Minimum != nil || p.Maximum != nil) && p.Type != SchemaInteger && p.Type != SchemaNumber {
		return invalid("action schema bounds require numeric type")
	}
	if (p.Properties != nil || p.Required != nil) && p.Type != SchemaObject {
		return invalid("action schema properties require object type")
	}
	if p.Minimum != nil && (math.IsNaN(*p.Minimum) || math.IsInf(*p.Minimum, 0)) {
		return invalid("nonfinite action schema minimum")
	}
	if p.Maximum != nil && (math.IsNaN(*p.Maximum) || math.IsInf(*p.Maximum, 0)) {
		return invalid("nonfinite action schema maximum")
	}
	if p.Minimum != nil && p.Maximum != nil && *p.Minimum > *p.Maximum {
		return invalid("action schema minimum exceeds maximum")
	}
	return validateSchemaObject(p.Properties, p.Required, depth+1)
}

func validateActionEnum(p SchemaProperty) error {
	if p.Enum == nil {
		return nil
	}
	if p.Type != SchemaString {
		return invalid("action schema enum requires string type")
	}
	if len(p.Enum) == 0 {
		return invalid("action schema enum must not be empty")
	}
	seen := make(map[string]bool, len(p.Enum))
	for _, item := range p.Enum {
		if seen[item] {
			return invalid("duplicate action schema enum value")
		}
		seen[item] = true
	}
	return nil
}

func validateActionProperties(properties map[string]SchemaProperty, depth int) error {
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := properties[name]
		if err := text(name, true); err != nil {
			return err
		}
		if err := validateActionProperty(p, depth); err != nil {
			return err
		}
	}
	return nil
}

func validateActionRequired(required []string) error {
	// Required names must be unique even when only one name is supplied.
	seen := make(map[string]bool, len(required))
	for _, name := range required {
		if err := text(name, true); err != nil {
			return err
		}
		if seen[name] {
			return invalid("duplicate action schema required name")
		}
		seen[name] = true
	}
	return nil
}
