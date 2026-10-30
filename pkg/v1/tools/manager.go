// Package tools defines plugin tools, validates their descriptors, and exposes
// tool discovery and calls over the plugin gRPC transport.
package tools

import (
	"context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"errors"
	"fmt"
	"regexp"

	logging "github.com/omniviewdev/plugin-sdk/log"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

var (
	// ErrToolNotFound is returned when no tool has the requested name.
	ErrToolNotFound = errors.New("tool not found")
	// ErrConnectionRequired is returned when a connection-scoped tool is
	// called without an explicit, nonempty body route.
	ErrConnectionRequired = errors.New("tool requires a connection")
)

var toolNamePattern = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// Manager holds the registered tools and dispatches calls to them.
type Manager struct {
	log                logging.Logger
	tools              map[string]Tool
	order              []string
	connectionProvider resourcev2.ConnectionContextProvider
	inputs map[string]*compiledSchema
	outputs map[string]*compiledSchema
}

var _ Provider = (*Manager)(nil)

// NewManager validates every tool and returns the provider.
func NewManager(log logging.Logger, opts PluginOpts) (*Manager, error) {
	m := &Manager{
		log:   log,
		tools: make(map[string]Tool, len(opts.Tools)),
		order: make([]string, 0, len(opts.Tools)),
		inputs: make(map[string]*compiledSchema),
		outputs: make(map[string]*compiledSchema),
	}
	for i := range opts.Tools {
		tool := &opts.Tools[i]
		if err := validateTool(*tool); err != nil {
			return nil, fmt.Errorf("tool %q: %w", tool.Name, err)
		}
		if _, exists := m.tools[tool.Name]; exists {
			return nil, fmt.Errorf("tool %q: duplicate name", tool.Name)
		}
		input, err := compileSchema(tool.InputSchema, true)
		if err != nil { return nil, fmt.Errorf("tool %q input schema: %w", tool.Name, err) }
		m.inputs[tool.Name] = input
		if len(tool.OutputSchema) != 0 {
			output, err := compileSchema(tool.OutputSchema, true)
			if err != nil { return nil, fmt.Errorf("tool %q output schema: %w", tool.Name, err) }
			m.outputs[tool.Name] = output
		}
		m.tools[tool.Name] = *tool
		m.order = append(m.order, tool.Name)
	}
	return m, nil
}

// BindConnectionContextProvider installs the sole native registry during setup,
// before requests are served, matching the other utility managers.
func (m *Manager) BindConnectionContextProvider(provider resourcev2.ConnectionContextProvider) {
	m.connectionProvider = provider
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
	if err := validateJSON(m.inputs[input.Name], input.Arguments); err != nil {
		return nil, resourcev2.NewError("INVALID_REQUEST", "Tool arguments do not match the declared schema.")
	}
	if tool.Scope == ScopePlugin {
		if input.Route != nil || (ctx != nil && ctx.Connection != nil) {
			return nil, resourcev2.NewError(
				"INVALID_REQUEST",
				"Plugin-scoped tools require no route or selected connection.",
			)
		}
		return m.finish(tool, tool.Handler, ctx, input.Arguments)
	}
	if input.Route == nil || input.Route.Connection == "" {
		return nil, fmt.Errorf("%w: %s", ErrConnectionRequired, input.Name)
	}
	if _, err := resourcev2.ToProtoAccessRoute(*input.Route); err != nil {
		return nil, err
	}
	if ctx != nil && ctx.Connection != nil && ctx.Connection.ID != input.Route.Connection {
		return nil, resourcev2.NewError("INVALID_REQUEST", "Tool route conflicts with selected connection.")
	}
	// Metadata supplies only a selector. The lease replaces all caller profile data.
	selected := types.PluginContext{}
	if ctx != nil {
		selected = *ctx
	}
	selected.Connection = &types.Connection{ID: input.Route.Connection}
	var result *Result
	err := resourcev2.WithUtilityConnection(&selected, m.connectionProvider, nil, input.Route,
		func(bound *types.PluginContext) error {
			var err error
			result, err = m.finish(tool, tool.Handler, bound, input.Arguments)
			return err
		})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (m *Manager) finish(tool Tool, handler Handler, ctx *types.PluginContext, arguments []byte) (*Result, error) {
	result, err := handler(ctx, arguments)
	if errors.Is(err, context.Canceled) || status.Code(err) == codes.Canceled { return nil, context.Canceled }
	if errors.Is(err, context.DeadlineExceeded) || status.Code(err) == codes.DeadlineExceeded { return nil, context.DeadlineExceeded }
	if err != nil {
		code := ErrorExecutionFailed
		switch resourcev2.ErrorCode(err) {
		case ErrorUnavailable: code = ErrorUnavailable
		case ErrorPermissionDenied, "FORBIDDEN": code = ErrorPermissionDenied
		}
		switch status.Code(err) {
		case codes.Unavailable: code = ErrorUnavailable
		case codes.PermissionDenied: code = ErrorPermissionDenied
		}
		return safeFailure(&Result{ErrorCode: code}), nil
	}
	if result == nil || result.IsError {
		return safeFailure(result), nil
	}
	if output := m.outputs[tool.Name]; output != nil {
		if err := validateJSON(output, result.StructuredContent); err != nil {
			return safeFailure(nil), nil
		}
	} else if len(result.StructuredContent) != 0 {
		if _, err := decodeObject(result.StructuredContent); err != nil { return safeFailure(nil), nil }
	}
	return result, nil
}
