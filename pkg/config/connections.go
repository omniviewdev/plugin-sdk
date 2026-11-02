package config

import (
	"fmt"
	"strings"
)

// ExportsSpec declares services this plugin offers to installed consumers.
type ExportsSpec struct {
	Connections *ConnectionExport `json:"connections,omitempty" yaml:"connections,omitempty"`
}

// RequiresSpec declares services this plugin needs from installed owners.
type RequiresSpec struct {
	Connections []ConnectionRequirement `json:"connections" yaml:"connections"`
}

// ConnectionExport limits exported operations to explicit resource patterns.
type ConnectionExport struct {
	Operations    []string `json:"operations" yaml:"operations"`
	ResourceTypes []string `json:"resource_types" yaml:"resource_types"`
}

// ConnectionRequirement names one owner and the complete access a consumer needs.
type ConnectionRequirement struct {
	Plugin        string   `json:"plugin" yaml:"plugin"`
	Operations    []string `json:"operations" yaml:"operations"`
	ResourceTypes []string `json:"resource_types" yaml:"resource_types"`
}

// validPattern admits opaque literals or one terminal /* wildcard.
func validPattern(p string) bool {
	if p == "" {
		return false
	}
	if !strings.Contains(p, "*") {
		return true
	}
	return strings.HasSuffix(p, "/*") && len(p) > 2 && strings.Count(p, "*") == 1
}

// validateConnectionLists rejects empty, duplicate, or unsupported declarations.
func validateConnectionLists(operations, patterns []string) error {
	if len(operations) == 0 || len(patterns) == 0 {
		return fmt.Errorf("connection operations and resource types must not be empty")
	}
	seen := map[string]bool{}
	for _, op := range operations {
		if op != "catalog" && op != "get" && op != "enumerate" && op != "update" {
			return fmt.Errorf("unknown connection operation %q", op)
		}
		if seen[op] {
			return fmt.Errorf("duplicate connection operation %q", op)
		}
		seen[op] = true
	}
	seen = map[string]bool{}
	for _, p := range patterns {
		if !validPattern(p) {
			return fmt.Errorf("invalid resource pattern %q", p)
		}
		if seen[p] {
			return fmt.Errorf("duplicate resource pattern %q", p)
		}
		seen[p] = true
	}
	return nil
}

// Validate checks connection declarations only; absent declarations remain valid.
func (m PluginMeta) Validate() error {
	if m.Exports != nil && m.Exports.Connections != nil {
		if err := validateConnectionLists(m.Exports.Connections.Operations, m.Exports.Connections.ResourceTypes); err != nil {
			return err
		}
	}
	if m.Requires != nil {
		if m.Requires.Connections != nil && len(m.Requires.Connections) == 0 {
			return fmt.Errorf("connection requirements must not be empty")
		}
		seen := map[string]bool{}
		for _, r := range m.Requires.Connections {
			if strings.TrimSpace(r.Plugin) == "" || r.Plugin == m.ID {
				return fmt.Errorf("invalid connection owner %q", r.Plugin)
			}
			if seen[r.Plugin] {
				return fmt.Errorf("duplicate connection owner %q", r.Plugin)
			}
			seen[r.Plugin] = true
			if err := validateConnectionLists(r.Operations, r.ResourceTypes); err != nil {
				return err
			}
		}
	}
	return nil
}

// patternCovers checks complete containment without normalizing either pattern.
func patternCovers(export, required string) bool {
	if export == required {
		return true
	}
	if !strings.HasSuffix(export, "/*") {
		return false
	}
	prefix := export[:len(export)-1]
	if strings.HasSuffix(required, "/*") {
		return strings.HasPrefix(required[:len(required)-1], prefix)
	}
	return strings.HasPrefix(required, prefix) && len(required) > len(prefix)
}

// ConnectionGrant is an immutable snapshot of an admitted consumer requirement.
type ConnectionGrant struct {
	operations []string
	patterns   []string
}

// GrantConnections requires full owner coverage and never returns a partial grant.
func GrantConnections(consumer, owner PluginMeta) (ConnectionGrant, error) {
	if err := consumer.Validate(); err != nil {
		return ConnectionGrant{}, err
	}
	if err := owner.Validate(); err != nil {
		return ConnectionGrant{}, err
	}
	if consumer.Requires == nil || owner.Exports == nil || owner.Exports.Connections == nil {
		return ConnectionGrant{}, fmt.Errorf("connection grant missing")
	}
	var requirement *ConnectionRequirement
	for i := range consumer.Requires.Connections {
		if consumer.Requires.Connections[i].Plugin == owner.ID {
			requirement = &consumer.Requires.Connections[i]
			break
		}
	}
	if requirement == nil {
		return ConnectionGrant{}, fmt.Errorf("connection requirement missing")
	}
	export := owner.Exports.Connections
	for _, op := range requirement.Operations {
		found := false
		for _, x := range export.Operations {
			if x == op {
				found = true
				break
			}
		}
		if !found {
			return ConnectionGrant{}, fmt.Errorf("connection operation not exported")
		}
	}
	for _, p := range requirement.ResourceTypes {
		found := false
		for _, x := range export.ResourceTypes {
			if patternCovers(x, p) {
				found = true
				break
			}
		}
		if !found {
			return ConnectionGrant{}, fmt.Errorf("resource pattern not exported")
		}
	}
	return ConnectionGrant{operations: append([]string(nil), requirement.Operations...), patterns: append([]string(nil), requirement.ResourceTypes...)}, nil
}

// Allows checks an operation and literal resource type against the admitted requirement.
func (g ConnectionGrant) Allows(operation, resourceType string) bool {
	found := false
	for _, op := range g.operations {
		if op == operation {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	for _, p := range g.patterns {
		if validPattern(resourceType) && !strings.Contains(resourceType, "*") && patternCovers(p, resourceType) {
			return true
		}
	}
	return false
}
