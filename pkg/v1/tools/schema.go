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
	depth, nodes := 0, 0
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func() (any, error)
	value = func() (any, error) {
		depth++
		nodes++
		defer func() { depth-- }()
		if depth > 128 || nodes > 10000 {
			return nil, fmt.Errorf("JSON exceeds structural budget")
		}
		token, err := d.Token()
		if err != nil {
			return nil, err
		}
		if number, ok := token.(json.Number); ok {
			if err := admitNumber(number); err != nil {
				return nil, err
			}
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return token, nil
		}
		switch delimiter {
		case '{':
			object := map[string]any{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, err
				}
				name := key.(string)
				if _, exists := object[name]; exists {
					return nil, fmt.Errorf("duplicate JSON member")
				}
				item, err := value()
				if err != nil {
					return nil, err
				}
				object[name] = item
			}
			_, err := d.Token()
			return object, err
		case '[':
			array := []any{}
			for d.More() {
				item, err := value()
				if err != nil {
					return nil, err
				}
				array = append(array, item)
			}
			_, err := d.Token()
			return array, err
		default:
			return nil, fmt.Errorf("unexpected JSON delimiter")
		}
	}
	result, err := value()
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON data")
	}
	return result, nil
}

type localSchemaLoader struct{}

func (localSchemaLoader) Load(string) (any, error) {
	return nil, fmt.Errorf("external schema references are forbidden")
}

type compiledSchema struct {
	schema *jsonschema.Schema
	work   uint64
}

func compileSchema(raw []byte, object bool) (*compiledSchema, error) {
	doc, err := decodeJSON(raw)
	if err != nil {
		return nil, err
	}
	if object {
		root, ok := doc.(map[string]any)
		if !ok || root["type"] != "object" {
			return nil, fmt.Errorf("input schema type must be object")
		}
	}
	work, err := schemaWork(doc)
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(localSchemaLoader{})
	const location = "urn:omniview:tools:schema"
	if err := compiler.AddResource(location, doc); err != nil {
		return nil, err
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
