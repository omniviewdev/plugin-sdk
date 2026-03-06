package logs

import (
	"io"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	commonpb "github.com/omniviewdev/plugin-sdk/proto/v1/common"
	logspb "github.com/omniviewdev/plugin-sdk/proto/v1/logs"
)

// PluginOpts contains the options for the log plugin.
type PluginOpts struct {
	// Handlers maps resource keys to their log handlers.
	// For example: "core::v1::Pod" -> Handler
	Handlers map[string]Handler `json:"handlers"`

	// SourceResolvers maps resource keys to source resolvers that resolve
	// group resources into individual log sources.
	// For example: "apps::v1::Deployment" -> SourceResolver
	SourceResolvers map[string]SourceResolver `json:"-"`
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

// SourceBuilderFunc builds LogSources from resource data for direct handler streaming.
// Plugins implement this to translate their resource data into properly-labeled sources
// that the LogHandlerFunc can consume. For example, a K8s plugin extracts pod name,
// namespace, and container names from the pod spec.
type SourceBuilderFunc func(
	resourceID string,
	resourceData map[string]interface{},
	opts LogSessionOptions,
) []LogSource

// Handler describes a log handler for a specific resource type.
type Handler struct {
	Plugin        string              `json:"plugin"`
	Resource      string              `json:"resource"`
	TargetBuilder ActionTargetBuilder `json:"target_builder"`
	Handler       LogHandlerFunc      `json:"-"`
	SourceBuilder SourceBuilderFunc   `json:"-"`
}

func (h Handler) ToProto() *logspb.LogHandler {
	return &logspb.LogHandler{
		Plugin:        h.Plugin,
		Resource:      h.Resource,
		TargetBuilder: h.TargetBuilder.ToProto(),
	}
}

func HandlerFromProto(p *logspb.LogHandler) Handler {
	return Handler{
		Plugin:        p.GetPlugin(),
		Resource:      p.GetResource(),
		TargetBuilder: ActionTargetBuilderFromProto(p.GetTargetBuilder()),
	}
}

func (h Handler) ID() string {
	return h.Plugin + "/" + h.Resource
}

// LogHandlerFunc opens a log stream for a single source, returning an io.ReadCloser.
type LogHandlerFunc func(ctx *types.PluginContext, req LogStreamRequest) (io.ReadCloser, error)

// SourceResolver resolves a "group" resource into individual log sources.
type SourceResolver func(
	ctx *types.PluginContext,
	resourceData map[string]interface{},
	opts SourceResolverOptions,
) (*SourceResolverResult, error)

// SourceResolverOptions configures how sources are resolved.
type SourceResolverOptions struct {
	Watch  bool              // if true, also return a channel for source changes
	Target string            // optional filter (e.g., specific container name)
	Params map[string]string // plugin-specific resolver params
}

// SourceResolverResult contains the resolved sources and optional event channel.
type SourceResolverResult struct {
	Sources []LogSource         // current sources
	Events  <-chan SourceEvent  // source lifecycle stream (nil if Watch=false)
}
