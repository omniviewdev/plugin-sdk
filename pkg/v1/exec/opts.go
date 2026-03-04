package exec

import (
	commonpb "github.com/omniviewdev/plugin-sdk/proto/v1/common"
	execpb "github.com/omniviewdev/plugin-sdk/proto/v1/exec"
)

// PluginOpts contains the options for the exec plugin.
type PluginOpts struct {
	Handlers []Handler `json:"handlers"`
}

// ActionTargetBuilder builds a dynamic list of targets for an action.
type ActionTargetBuilder struct {
	Label         string            `json:"label"`
	LabelSelector string            `json:"label_selector"`
	Paths         []string          `json:"paths"`
	Selectors     map[string]string `json:"selectors"`
}

// ToProto converts ActionTargetBuilder to its proto representation.
func (a ActionTargetBuilder) ToProto() *commonpb.ActionTargetBuilder {
	return &commonpb.ActionTargetBuilder{
		Label:         a.Label,
		LabelSelector: a.LabelSelector,
		Paths:         a.Paths,
		Selectors:     a.Selectors,
	}
}

// ActionTargetBuilderFromProto converts a proto ActionTargetBuilder to the domain type.
func ActionTargetBuilderFromProto(p *commonpb.ActionTargetBuilder) ActionTargetBuilder {
	if p == nil {
		return ActionTargetBuilder{}
	}
	return ActionTargetBuilder{
		Label:         p.GetLabel(),
		LabelSelector: p.GetLabelSelector(),
		Paths:         p.GetPaths(),
		Selectors:     p.GetSelectors(),
	}
}

// Handler handles performs running commands and creating sessions for a resource.
type Handler struct {
	Plugin         string              `json:"plugin"`
	Resource       string              `json:"resource"`
	TargetBuilder  ActionTargetBuilder `json:"target_builder"`
	DefaultCommand []string            `json:"default_command"`
	TTYHandler     TTYHandler          `json:"-"`
	CommandHandler CommandHandler      `json:"-"`
	// if the handler supports resizing, it will be sent through the channel instead of the pty file
	HandlesResize bool `json:"-"`
}

func (h Handler) ToProto() *execpb.ExecHandler {
	return &execpb.ExecHandler{
		Plugin:         h.Plugin,
		Resource:       h.Resource,
		TargetBuilder:  h.TargetBuilder.ToProto(),
		DefaultCommand: h.DefaultCommand,
	}
}

func HandlerFromProto(p *execpb.ExecHandler) Handler {
	if p == nil {
		return Handler{}
	}
	return Handler{
		Plugin:         p.GetPlugin(),
		Resource:       p.GetResource(),
		TargetBuilder:  ActionTargetBuilderFromProto(p.GetTargetBuilder()),
		DefaultCommand: p.GetDefaultCommand(),
	}
}

func (h Handler) ID() string {
	return h.Plugin + "/" + h.Resource
}

func (h Handler) String() string {
	return h.ID()
}
