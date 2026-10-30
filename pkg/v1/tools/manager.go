// Package tools defines plugin tools, validates their descriptors, and exposes
// tool discovery and calls over the plugin gRPC transport.
package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	logging "github.com/omniviewdev/plugin-sdk/log"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

var (
	// ErrToolNotFound is returned when no tool has the requested name.
	ErrToolNotFound = errors.New("tool not found")
	// ErrConnectionRequired is returned when a connection-scoped tool is
	// called without a connection in the PluginContext.
	ErrConnectionRequired = errors.New("tool requires a connection")
)

var toolNamePattern = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// Manager holds the registered tools and dispatches calls to them.
type Manager struct {
	log   logging.Logger
	tools map[string]Tool
	order []string
}

var _ Provider = (*Manager)(nil)

// NewManager validates every tool and returns the provider.
func NewManager(log logging.Logger, opts PluginOpts) (*Manager, error) {
	m := &Manager{
		log:   log,
		tools: make(map[string]Tool, len(opts.Tools)),
		order: make([]string, 0, len(opts.Tools)),
	}
	for i := range opts.Tools {
		tool := &opts.Tools[i]
		if err := validateTool(*tool); err != nil {
			return nil, fmt.Errorf("tool %q: %w", tool.Name, err)
		}
		if _, exists := m.tools[tool.Name]; exists {
			return nil, fmt.Errorf("tool %q: duplicate name", tool.Name)
		}
		m.tools[tool.Name] = *tool
		m.order = append(m.order, tool.Name)
	}
	return m, nil
}

func validateTool(tool Tool) error {
	if !toolNamePattern.MatchString(tool.Name) {
		return errors.New("name must match ^[a-z0-9_]{1,64}$")
	}
	if tool.Handler == nil {
		return errors.New("handler is nil")
	}
	switch tool.Scope {
	case ScopePlugin, ScopeConnection:
	default:
		return fmt.Errorf("scope must be %q or %q", ScopePlugin, ScopeConnection)
	}
	var schema struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		return fmt.Errorf("input schema is not valid JSON: %w", err)
	}
	if schema.Type != "object" {
		return errors.New(`input schema "type" must be "object"`)
	}
	return nil
}

// ListTools returns the descriptors in registration order.
func (m *Manager) ListTools(_ *types.PluginContext) ([]Descriptor, error) {
	out := make([]Descriptor, 0, len(m.order))
	for _, name := range m.order {
		out = append(out, m.tools[name].Descriptor)
	}
	return out, nil
}

// CallTool dispatches the call to the named tool's handler.
func (m *Manager) CallTool(ctx *types.PluginContext, input CallInput) (*Result, error) {
	tool, ok := m.tools[input.Name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrToolNotFound, input.Name)
	}
	if tool.Scope == ScopeConnection && (ctx == nil || ctx.Connection == nil) {
		return nil, fmt.Errorf("%w: %s", ErrConnectionRequired, input.Name)
	}
	return tool.Handler(ctx, input.Arguments)
}
