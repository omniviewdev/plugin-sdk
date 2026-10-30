package resourcev2

import "math"

// ValidateResourceDefinition bounds and admits presentation metadata before
// transport serialization. Extractors never confer identity or access.
func ValidateResourceDefinition(v ResourceDefinition) error {
	if err := admission(v); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, column := range v.ColumnDefs {
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
	if v.Type != "" {
		if v.TypeAccessor != "" || len(v.TypeMap) != 0 {
			return invalid("resource link has conflicting type selectors")
		}
	} else {
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
	var property func(SchemaProperty, int) error
	property = func(p SchemaProperty, depth int) error {
		if depth > MaxValueDepth {
			return limited("action schema depth limit")
		}
		switch p.Type {
		case SchemaString, SchemaInteger, SchemaNumber, SchemaBoolean, SchemaObject, SchemaArray:
		default:
			return invalid("unknown action schema type")
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
		for name, child := range p.Properties {
			if err := text(name, true); err != nil {
				return err
			}
			if err := property(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	for name, p := range schema.Properties {
		if err := text(name, true); err != nil {
			return err
		}
		if err := property(p, 0); err != nil {
			return err
		}
	}
	return nil
}
