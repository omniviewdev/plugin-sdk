package tools

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"strconv"
	"strings"
)

const (
	maxSchemaExpansion uint64 = 10000
	maxValidationWork  uint64 = 1000000
	maxExactNumberLength = 4096
	maxExactExponent = 4096
)

// admitNumber bounds exact arithmetic before the validator sees a number. The
// validator assumes [big.Rat.SetString] succeeds, including for exponent notation.
// Restrict both numeric length and exponent; never round through float64.
func admitNumber(number json.Number) error {
	text := string(number)
	if len(text) > maxExactNumberLength {
		return fmt.Errorf("JSON number exceeds exact arithmetic budget")
	}
	if index := strings.IndexAny(text, "eE"); index >= 0 {
		exponent, err := strconv.ParseInt(text[index+1:], 10, 32)
		if err != nil || exponent < -maxExactExponent || exponent > maxExactExponent {
			return fmt.Errorf("JSON exponent exceeds exact arithmetic budget")
		}
	}
	if _, ok := new(big.Rat).SetString(text); !ok {
		return fmt.Errorf("JSON number is unsupported by exact arithmetic")
	}
	return nil
}

// Count assertions are compiled by the dependency into platform int fields.
// Reject out-of-range declarations before its unchecked Int64/int conversions.
func admitCount(value any) error {
	number, ok := value.(json.Number)
	if !ok {
		return fmt.Errorf("schema count must be a nonnegative platform integer")
	}
	rational, ok := new(big.Rat).SetString(string(number))
	if !ok || !rational.IsInt() || rational.Sign() < 0 {
		return fmt.Errorf("schema count must be a nonnegative platform integer")
	}
	maximum := new(big.Int).SetUint64(uint64(^uint(0) >> 1))
	if rational.Num().Cmp(maximum) > 0 {
		return fmt.Errorf("schema count exceeds platform integer range")
	}
	return nil
}

func jsonNodes(value any) uint64 {
	count := uint64(1)
	switch value := value.(type) {
	case map[string]any:
		for _, child := range value {
			count += jsonNodes(child)
		}
	case []any:
		for _, child := range value {
			count += jsonNodes(child)
		}
	}
	return count
}

type schemaNode struct {
	children []string
	ref      string
}

// schemaWork conservatively expands every static schema edge, including unused
// definitions. Memoized costs retain repeated-edge multiplicity.
func schemaWork(doc any) (uint64, error) {
	g := schemaGraph{nodes: map[string]*schemaNode{}, anchors: map[string]string{}}
	if err := g.collect(doc, ""); err != nil {
		return 0, err
	}
	if err := g.resolveReferences(); err != nil {
		return 0, err
	}
	expansion, err := g.expansion()
	if err != nil {
		return 0, err
	}
	work := expansion + jsonNodes(doc)
	if work > maxSchemaExpansion {
		return 0, fmt.Errorf("schema declaration exceeds work budget")
	}
	return work, nil
}

type schemaGraph struct {
	nodes map[string]*schemaNode
	anchors map[string]string
}

func (g *schemaGraph) collect(value any, path string) error {
		current := &schemaNode{}
		g.nodes[path] = current
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		for key, value := range object {
			if err := g.declaration(current, path, key, value); err != nil {
				return err
			}
			if err := g.children(current, path, key, value); err != nil {
				return err
			}
		}
		return nil
}

// declaration enforces the bounded profile at schema nodes, not annotation data.
func (g *schemaGraph) declaration(current *schemaNode, path, key string, value any) error {
			switch key {
			case "$dynamicRef", "$dynamicAnchor", "$recursiveRef", "$recursiveAnchor":
				return fmt.Errorf("dynamic or recursive schema references are outside the bounded profile")
			case "dependencies", "definitions", "additionalItems":
				return fmt.Errorf("legacy schema keyword is outside the 2020-12 profile: %s", key)
			case "$schema":
				if value != "https://json-schema.org/draft/2020-12/schema" {
					return fmt.Errorf("unsupported schema dialect")
				}
			case "format":
				return fmt.Errorf("format assertions are unsupported; use explicit constraints")
			case "$id":
				return fmt.Errorf("schema resource identifiers are unsupported")
			case "minLength",
				"maxLength",
				"minItems",
				"maxItems",
				"minProperties",
				"maxProperties",
				"minContains",
				"maxContains":
				if err := admitCount(value); err != nil {
					return err
				}
			case "$anchor":
				if anchor, ok := value.(string); ok {
					if _, exists := g.anchors[anchor]; exists {
						return fmt.Errorf("duplicate schema anchor")
					}
					g.anchors[anchor] = path
				}
			case "$ref":
				ref, ok := value.(string)
				if !ok || !strings.HasPrefix(ref, "#") {
					return fmt.Errorf("only local schema references are supported")
				}
				current.ref = ref
			case "items":
				if _, legacy := value.([]any); legacy {
					return fmt.Errorf("legacy tuple items are outside the 2020-12 profile; use prefixItems")
				}
			}
			return nil
}

// children is the shared walker for all admitted schema-valued keywords.
func (g *schemaGraph) children(current *schemaNode, path, key string, value any) error {
		escape := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1") }
		add := func(child any, childPath string) error {
			current.children = append(current.children, childPath)
			return g.collect(child, childPath)
		}
		switch key {
			case "properties", "patternProperties", "$defs", "dependentSchemas":
				if children, ok := value.(map[string]any); ok {
					for name, child := range children {
						if err := add(child, path+"/"+escape(key)+"/"+escape(name)); err != nil {
							return err
						}
					}
				}
			case "allOf", "anyOf", "oneOf", "prefixItems":
				if children, ok := value.([]any); ok {
					for index, child := range children {
						if err := add(child, path+"/"+key+"/"+strconv.Itoa(index)); err != nil {
							return err
						}
					}
				}
			case "items", "contains",
				"additionalProperties",
				"propertyNames",
				"unevaluatedProperties",
				"unevaluatedItems",
				"not",
				"if",
				"then",
				"else",
				"contentSchema":
				return add(value, path+"/"+key)
			}
		return nil
}

func (g *schemaGraph) resolveReferences() error {
	for _, node := range g.nodes {
		if node.ref == "" {
			continue
		}
		target, err := url.PathUnescape(node.ref[1:])
		if err != nil {
			return fmt.Errorf("invalid local schema reference")
		}
		if target != "" && !strings.HasPrefix(target, "/") {
			var ok bool
			target, ok = g.anchors[target]
			if !ok {
				return fmt.Errorf("unknown schema anchor")
			}
		}
		if _, ok := g.nodes[target]; !ok {
			return fmt.Errorf("reference does not target an admitted schema node")
		}
		node.children = append(node.children, target)
	}
	return nil
}

// expansion preserves repeated-edge multiplicity and rejects cyclic references.
func (g *schemaGraph) expansion() (uint64, error) {
	active := map[string]bool{}
	costs := map[string]uint64{}
	var cost func(string) (uint64, error)
	cost = func(path string) (uint64, error) {
		if active[path] {
			return 0, fmt.Errorf("cyclic schema references are outside the bounded profile")
		}
		if known, ok := costs[path]; ok {
			return known, nil
		}
		active[path] = true
		total := uint64(1)
		for _, child := range g.nodes[path].children {
			count, err := cost(child)
			if err != nil {
				return 0, err
			}
			if count > maxSchemaExpansion-total {
				return 0, fmt.Errorf("schema reference expansion exceeds work budget")
			}
			total += count
		}
		active[path] = false
		costs[path] = total
		return total, nil
	}
	return cost("")
}
