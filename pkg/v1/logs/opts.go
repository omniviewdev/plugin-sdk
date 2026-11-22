package logs

import (
	"io"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
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
	resourceData *resourcev2.Value,
	opts LogSessionOptions,
) []LogSource

// Handler describes a log handler for a specific resource type.
type Handler struct {
	Plugin        string              `json:"plugin"`
	Resource      string              `json:"resource"`
	TargetBuilder ActionTargetBuilder `json:"target_builder"`
	// Handler opens the stream of a single source. Without Replay it is the whole
	// stream; with Replay it is the live follow that comes after the backlog.
	Handler LogHandlerFunc `json:"-"`
	// Replay, when set, splits every source's stream into a finite backlog and a
	// live follow, so the SDK can tell readers exactly where the backlog ends
	// instead of leaving them to guess from timing (see [ParamReplayMarkers]).
	//
	// Replay opens the backlog: the lines the source already holds, oldest first,
	// bounded by the request's TailLines, SinceSeconds, SinceTime and LimitBytes
	// (or, when Resume is set, only the lines at or after SinceTime). The request always
	// has Follow false and Replay must honour that: return io.EOF right after the
	// last line the source holds and never wait for new ones. The SDK sends the
	// [StreamEventSourceCaughtUp] event when the stream ends, then calls Handler
	// with Follow and Resume true to follow what comes next. A session without
	// Follow runs Replay alone, and so does a handler that sets no Handler: a
	// finite source, whose sessions end with the backlog.
	//
	// Setting Replay is the whole declaration: the SDK numbers and flags the lines
	// and sends the marker itself, so there is nothing to forget. A Replay stream
	// that neither returns a line nor ends for the manager's ReplayIdleTimeout
	// fails its source, because a backlog that never ends would leave every reader
	// waiting for a marker that cannot come. The timeout covers the stream, not the
	// call that opens it: bound that call with the context it is given.
	//
	// Handler, as the follow, continues as [LogStreamRequest.Resume] says: at or
	// after SinceTime, or from the start of the caller's window when nothing was
	// read. It never replays the backlog, and it need not drop what the SDK already
	// read: lines it returns again, such as the boundary second of a source whose
	// time filter is coarser than its timestamps, are dropped by the SDK.
	Replay        LogHandlerFunc    `json:"-"`
	SourceBuilder SourceBuilderFunc `json:"-"`
}

// ToProto encodes handler metadata without its executable callbacks.
func (h Handler) ToProto() *logspb.LogHandler {
	return &logspb.LogHandler{
		Plugin:        h.Plugin,
		Resource:      h.Resource,
		TargetBuilder: h.TargetBuilder.ToProto(),
	}
}

// HandlerFromProto decodes handler metadata; callbacks must be registered locally.
func HandlerFromProto(p *logspb.LogHandler) Handler {
	return Handler{
		Plugin:        p.GetPlugin(),
		Resource:      p.GetResource(),
		TargetBuilder: ActionTargetBuilderFromProto(p.GetTargetBuilder()),
	}
}

// ID returns the plugin-qualified resource handler key.
func (h Handler) ID() string {
	return h.Plugin + "/" + h.Resource
}

// LogHandlerFunc opens a log stream for a single source, returning an [io.ReadCloser].
type LogHandlerFunc func(ctx *types.PluginContext, req LogStreamRequest) (io.ReadCloser, error)

// SourceResolver resolves a "group" resource into individual log sources.
type SourceResolver func(
	ctx *types.PluginContext,
	resourceData *resourcev2.Value,
	opts SourceResolverOptions,
) (*SourceResolverResult, error)

// SourceResolverOptions configures how sources are resolved.
type SourceResolverOptions struct {
	Reference *resourcev2.ResourceReference
	Route     *resourcev2.AccessRoute
	Watch     bool              // if true, also return a channel for source changes
	Target    string            // optional filter (e.g., specific container name)
	Params    map[string]string // plugin-specific resolver params
}

// SourceResolverResult contains the resolved sources and optional event channel.
type SourceResolverResult struct {
	Sources []LogSource        // current sources
	Events  <-chan SourceEvent // source lifecycle stream (nil if Watch=false)
}
