package resourcev2

import "slices"

func cloneAncillarySchema(schema *Schema) *Schema {
	if schema == nil {
		return nil
	}
	return &Schema{Properties: cloneSchemaProperties(schema.Properties), Required: slices.Clone(schema.Required)}
}

func cloneSchemaProperties(properties map[string]SchemaProperty) map[string]SchemaProperty {
	if properties == nil {
		return nil
	}
	copied := make(map[string]SchemaProperty, len(properties))
	for name, property := range properties {
		property.Enum = slices.Clone(property.Enum)
		property.Required = slices.Clone(property.Required)
		property.Properties = cloneSchemaProperties(property.Properties)
		if property.Minimum != nil {
			value := *property.Minimum
			property.Minimum = &value
		}
		if property.Maximum != nil {
			value := *property.Maximum
			property.Maximum = &value
		}
		copied[name] = property
	}
	return copied
}

func cloneActionDescriptor(descriptor ActionDescriptor) ActionDescriptor {
	descriptor.ParamsSchema = cloneAncillarySchema(descriptor.ParamsSchema)
	descriptor.OutputSchema = cloneAncillarySchema(descriptor.OutputSchema)
	return descriptor
}

func cloneActionDescriptors(descriptors []ActionDescriptor) []ActionDescriptor {
	if descriptors == nil {
		return nil
	}
	copied := make([]ActionDescriptor, len(descriptors))
	for i, descriptor := range descriptors {
		copied[i] = cloneActionDescriptor(descriptor)
	}
	return copied
}
