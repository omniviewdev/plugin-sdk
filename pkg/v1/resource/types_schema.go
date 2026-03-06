package resource

import "encoding/json"

// SchemaType represents a JSON Schema type.
type SchemaType string

const (
	SchemaString  SchemaType = "string"
	SchemaInteger SchemaType = "integer"
	SchemaNumber  SchemaType = "number"
	SchemaBoolean SchemaType = "boolean"
	SchemaObject  SchemaType = "object"
	SchemaArray   SchemaType = "array"
)

// SchemaProperty describes a single property within a Schema.
type SchemaProperty struct {
	Type        SchemaType                `json:"type"`
	Description string                    `json:"description,omitempty"`
	Enum        []string                  `json:"enum,omitempty"`
	Default     any                       `json:"default,omitempty"`
	Minimum     *float64                  `json:"minimum,omitempty"`
	Maximum     *float64                  `json:"maximum,omitempty"`
	Properties  map[string]SchemaProperty `json:"properties,omitempty"`
	Required    []string                  `json:"required,omitempty"`
}

// Schema describes the shape of an action's parameters or output.
// Always serializes as a JSON Schema object with "type":"object".
type Schema struct {
	Properties map[string]SchemaProperty `json:"properties,omitempty"`
	Required   []string                  `json:"required,omitempty"`
}

// MarshalJSON produces a JSON Schema with "type":"object" added automatically.
func (s Schema) MarshalJSON() ([]byte, error) {
	type schemaJSON struct {
		Type       string                    `json:"type"`
		Properties map[string]SchemaProperty `json:"properties,omitempty"`
		Required   []string                  `json:"required,omitempty"`
	}
	return json.Marshal(schemaJSON{
		Type:       "object",
		Properties: s.Properties,
		Required:   s.Required,
	})
}

// UnmarshalJSON reads a JSON Schema object, discarding the "type" field.
func (s *Schema) UnmarshalJSON(data []byte) error {
	type schemaJSON struct {
		Properties map[string]SchemaProperty `json:"properties,omitempty"`
		Required   []string                  `json:"required,omitempty"`
	}
	var raw schemaJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	s.Properties = raw.Properties
	s.Required = raw.Required
	return nil
}

// Ptr returns a pointer to the Schema, for use in struct literals.
func (s Schema) Ptr() *Schema {
	return &s
}

// PtrFloat64 is a convenience helper for setting Minimum/Maximum in schema literals.
func PtrFloat64(v float64) *float64 {
	return &v
}
