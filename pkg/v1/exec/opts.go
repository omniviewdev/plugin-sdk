package exec

import (
	"io"
	"os"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
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

// Handler handles running commands and creating sessions for a resource.
type Handler struct {
	Plugin         string              `json:"plugin"`
	Resource       string              `json:"resource"`
	TargetBuilder  ActionTargetBuilder `json:"target_builder"`
	DefaultCommand []string            `json:"default_command"`
	TTYHandler     TTYHandlerFunc      `json:"-"`
	CommandHandler CommandHandler      `json:"-"`
	// HandlesResize: if true, resize events are sent through the channel
	// instead of via terminal.Resize().
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

// ---------------------------------------------------------------------------
// Handler function types
// ---------------------------------------------------------------------------

// TTYHandlerFunc is the expected signature for a function that creates a new
// session with a TTY. It is passed the TTY file descriptor, a stop channel
// for signalling errors, and a receive-only resize channel.
//
// Renamed from TTYHandler to avoid confusion with the Handler struct.
type TTYHandlerFunc func(
	ctx *types.PluginContext,
	opts SessionOptions,
	tty *os.File,
	stopCh chan error,
	resize <-chan SessionResizeInput,
) error

// CommandHandler is the expected signature for a non-TTY handler. It should
// immediately return its standard output and error readers.
type CommandHandler func(
	ctx *types.PluginContext,
	opts SessionOptions,
) (stdout io.Reader, stderr io.Reader, err error)

// SessionHandler is the expected signature for a function that creates a new
// session, returning the standard input, output, and error streams which will
// be multiplexed to the client.
type SessionHandler func(
	ctx *types.PluginContext,
	opts SessionOptions,
) (stdin io.Writer, stdout io.Reader, stderr io.Reader, err error)
