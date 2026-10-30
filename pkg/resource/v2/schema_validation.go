package resourcev2

import (
	"math"
	"math/big"
	"sort"
)

// ValidateSchema checks the restricted object-root ancillary schema. Defaults,
// coercion, item schemas, and other JSON Schema keywords are unsupported.
func ValidateSchema(s Schema) error {
	if err := admission(s); err != nil {
		return err
	}
	return validateSchemaObject(s.Properties, s.Required, 0)
}

func validateSchemaObject(properties map[string]SchemaProperty, required []string, depth int) error {
	if err := validateActionRequired(required); err != nil {
		return err
	}
	return validateActionProperties(properties, depth)
}

// ValidateSchemaValue validates without changing values or their numeric kinds.
// Undeclared object properties and array elements remain unconstrained.
func ValidateSchemaValue(s Schema, v Value) error {
	if err := ValidateSchema(s); err != nil {
		return err
	}
	if err := ValidateValue(v); err != nil {
		return err
	}
	return validateSchemaObjectValue(s.Properties, s.Required, v)
}

func validateSchemaObjectValue(properties map[string]SchemaProperty, required []string, v Value) error {
	if v.Kind != "map" {
		return invalid("action schema requires object")
	}
	for _, name := range required {
		if _, ok := v.Map[name]; !ok {
			return invalid("required action property is missing")
		}
	}
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if value, ok := v.Map[name]; ok {
			if err := validateSchemaPropertyValue(properties[name], value); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateSchemaPropertyValue(p SchemaProperty, v Value) error {
	numeric := v.Kind == "int64" || v.Kind == "uint64" || v.Kind == "double"
	switch p.Type {
	case SchemaString:
		if v.Kind != "string" {
			return invalid("action schema requires string")
		}
		if len(p.Enum) > 0 {
			found := false
			for _, item := range p.Enum {
				if item == v.String {
					found = true
					break
				}
			}
			if !found {
				return invalid("action string is outside enum")
			}
		}
	case SchemaBoolean:
		if v.Kind != "bool" {
			return invalid("action schema requires boolean")
		}
	case SchemaObject:
		return validateSchemaObjectValue(p.Properties, p.Required, v)
	case SchemaArray:
		if v.Kind != "list" {
			return invalid("action schema requires array")
		}
	case SchemaInteger, SchemaNumber:
		if !numeric || (p.Type == SchemaInteger && v.Kind == "double" && math.Trunc(v.Double) != v.Double) {
			return invalid("action schema requires numeric type")
		}
		if p.Minimum != nil || p.Maximum != nil {
			n := new(big.Rat)
			switch v.Kind {
			case "int64":
				n.SetString(v.Int)
			case "uint64":
				n.SetString(v.Uint)
			case "double":
				n.SetFloat64(v.Double)
			}
			if p.Minimum != nil && n.Cmp(new(big.Rat).SetFloat64(*p.Minimum)) < 0 {
				return invalid("action number is below minimum")
			}
			if p.Maximum != nil && n.Cmp(new(big.Rat).SetFloat64(*p.Maximum)) > 0 {
				return invalid("action number exceeds maximum")
			}
		}
	}
	return nil
}

// ValidateActionInput checks declared action input without applying defaults.
func ValidateActionInput(d ActionDescriptor, v Value) error {
	if d.ParamsSchema != nil {
		return ValidateSchemaValue(*d.ParamsSchema, v)
	}
	return ValidateValue(v)
}

// ValidateActionOutput checks declared successful action output.
func ValidateActionOutput(d ActionDescriptor, v Value) error {
	if d.OutputSchema != nil {
		return ValidateSchemaValue(*d.OutputSchema, v)
	}
	return ValidateValue(v)
}
