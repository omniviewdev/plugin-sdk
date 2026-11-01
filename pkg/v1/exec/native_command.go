package exec

import (
	"strings"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// NativeCommand describes a fresh local process, not a running PTY or session.
// Executable is a program name or path. Args are literal arguments, never shell
// source. Env adds environment entries to inherited local credentials; Cwd is
// optional. Plans must not contain secrets, tokens, passwords, or embedded
// credentials, including in arguments and environment values. Only reusable local
// credential references or inherited authentication are supported.
type NativeCommand struct {
	Executable string            `json:"executable"`
	Args       []string          `json:"args"`
	Env        map[string]string `json:"env"`
	Cwd        string            `json:"cwd"`
}

// NativeCommandProvider is an optional planning capability. Existing Provider
// implementations need not implement it. Planning never launches a process.
type NativeCommandProvider interface {
	PlanNativeCommand(*types.PluginContext, SessionOptions) (*NativeCommand, error)
}

// NativeCommandHandler plans a fresh process for an admitted resource. It must
// verify the authoritative resource identity/incarnation and reject unsupported
// authentication. ResourceData is input, not authorization. No existing terminal
// state, remote working directory, shell history, or session is transferred.
type NativeCommandHandler func(*types.PluginContext, SessionOptions) (*NativeCommand, error)

// ValidateNativeCommand checks structured process fields using resource metadata
// bounds. It does not resolve executables, inspect local files, detect secrets,
// authorize a launch, or interpret shell syntax. Shell metacharacters stay literal.
func ValidateNativeCommand(command *NativeCommand) error {
	if command == nil {
		return optionError("INVALID_REQUEST", "Native command must not be nil.")
	}
	if err := resourcev2.ValidateMetadata(command); err != nil {
		return err
	}
	if command.Executable == "" || strings.ContainsRune(command.Executable, 0) {
		return optionError("INVALID_REQUEST", "Native executable must be nonempty and contain no NUL.")
	}
	for _, arg := range command.Args {
		if strings.ContainsRune(arg, 0) {
			return optionError("INVALID_REQUEST", "Native arguments must contain no NUL.")
		}
	}
	for key, value := range command.Env {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return optionError("INVALID_REQUEST", "Native environment requires nonempty names without equals or NUL and values without NUL.")
		}
	}
	if strings.ContainsRune(command.Cwd, 0) {
		return optionError("INVALID_REQUEST", "Native working directory must contain no NUL.")
	}
	return nil
}

var _ NativeCommandProvider = (*Manager)(nil)

// ValidateNativeCommandOptions requires an explicit canonical target and route,
// and applies the same metadata/body bounds as resource terminal requests.
func ValidateNativeCommandOptions(opts SessionOptions) error {
	if err := ValidateResourceSessionOptions(opts); err != nil {
		return err
	}
	if opts.Reference == nil || opts.Route == nil {
		return optionError("INVALID_REQUEST", "Native command requires an explicit resource reference and route.")
	}
	return nil
}

// PlanNativeCommand revalidates explicit resource admission and plans a fresh
// shell/process without allocating a PTY or changing sessions. The connection
// lease ends on return. A plan is not a reusable authorization grant: callers must
// request it immediately before launch, and resource state can still change
// between planning and launch. The handler must verify authoritative identity.
func (m *Manager) PlanNativeCommand(pluginctx *types.PluginContext, opts SessionOptions) (*NativeCommand, error) {
	if err := ValidateNativeCommandOptions(opts); err != nil {
		return nil, err
	}
	normalized, err := NormalizeResourceSessionOptions(opts)
	if err != nil {
		return nil, err
	}
	if pluginctx == nil || pluginctx.Connection == nil || pluginctx.Connection.ID != normalized.Route.Connection {
		return nil, optionError("INVALID_REQUEST", "Native command route must match the explicit caller connection.")
	}
	handler, ok := m.handlers[normalized.ResourcePlugin+"/"+normalized.ResourceKey]
	if !ok || handler.NativeCommandHandler == nil {
		return nil, optionError("UNSUPPORTED", "Native command planning is unavailable for this resource.")
	}
	var command *NativeCommand
	err = resourcev2.WithUtilityConnection(pluginctx, m.connectionProvider, normalized.Reference, normalized.Route, func(bound *types.PluginContext) error {
		var planErr error
		command, planErr = handler.NativeCommandHandler(bound, normalized)
		if planErr != nil {
			return planErr
		}
		return ValidateNativeCommand(command)
	})
	if err != nil {
		return nil, err
	}
	return command, nil
}
