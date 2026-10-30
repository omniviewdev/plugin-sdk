package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// decodeJSON rejects duplicate object members at every depth and preserves numbers.
func decodeJSON(raw []byte) (any, error) {
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("JSON exceeds byte budget")
	}
	d := &boundedJSONDecoder{decoder: json.NewDecoder(bytes.NewReader(raw))}
	d.decoder.UseNumber()
	result, err := d.value()
	if err != nil {
		return nil, err
	}
	if _, trailingErr := d.decoder.Token(); trailingErr != io.EOF {
		return nil, fmt.Errorf("trailing JSON data")
	}
	return result, nil
}

type boundedJSONDecoder struct {
	decoder *json.Decoder
	depth int
	nodes int
}

func (d *boundedJSONDecoder) value() (any, error) {
	d.depth++
	d.nodes++
	defer func() { d.depth-- }()
	if d.depth > 128 || d.nodes > 10000 {
		return nil, fmt.Errorf("JSON exceeds structural budget")
	}
	token, err := d.decoder.Token()
	if err != nil {
		return nil, err
	}
	if number, ok := token.(json.Number); ok {
		if numberErr := admitNumber(number); numberErr != nil {
			return nil, numberErr
		}
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delimiter {
	case '{':
		return d.object()
	case '[':
		return d.array()
	default:
		return nil, fmt.Errorf("unexpected JSON delimiter")
	}
}

func (d *boundedJSONDecoder) object() (any, error) {
	object := map[string]any{}
	for d.decoder.More() {
		key, err := d.decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := key.(string)
		if !ok {
			return nil, fmt.Errorf("JSON member name must be a string")
		}
		if _, exists := object[name]; exists {
			return nil, fmt.Errorf("duplicate JSON member")
		}
		item, err := d.value()
		if err != nil {
			return nil, err
		}
		object[name] = item
	}
	_, err := d.decoder.Token()
	return object, err
}

func (d *boundedJSONDecoder) array() (any, error) {
	array := []any{}
	for d.decoder.More() {
		item, err := d.value()
		if err != nil {
			return nil, err
		}
		array = append(array, item)
	}
	_, err := d.decoder.Token()
	return array, err
}

type localSchemaLoader struct{}

func (localSchemaLoader) Load(string) (any, error) {
	return nil, fmt.Errorf("external schema references are forbidden")
}

type compiledSchema struct {
	schema *jsonschema.Schema
	work   uint64
}

func compileSchema(raw []byte) (*compiledSchema, error) {
	doc, err := decodeJSON(raw)
	if err != nil {
		return nil, err
	}
	root, ok := doc.(map[string]any)
	if !ok || root["type"] != "object" {
		return nil, fmt.Errorf("input schema type must be object")
	}
	work, err := schemaWork(doc)
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(localSchemaLoader{})
	const location = "urn:omniview:tools:schema"
	if addErr := compiler.AddResource(location, doc); addErr != nil {
		return nil, addErr
	}
	schema, err := compiler.Compile(location)
	if err != nil {
		return nil, err
	}
	return &compiledSchema{schema: schema, work: work}, nil
}

func validateJSON(schema *compiledSchema, raw []byte) error {
	value, err := decodeObject(raw)
	if err != nil {
		return err
	}
	nodes := jsonNodes(value)
	// Squaring instance size conservatively covers pairwise assertions such as uniqueItems.
	if schema.work > maxValidationWork/nodes/nodes {
		return fmt.Errorf("schema validation exceeds work budget")
	}
	return schema.schema.Validate(value)
}

func decodeObject(raw []byte) (map[string]any, error) {
	value, err := decodeJSON(raw)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("structured JSON must be an object")
	}
	return object, nil
}
