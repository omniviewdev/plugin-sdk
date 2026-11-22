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
	// Handler opens the one stream of a source: its replay and its live lines together.
	Handler LogHandlerFunc `json:"-"`
	// ReplayCount, when set, lets a reader that asks for replay markers (see
	// [ParamReplayMarkers]) know exactly where the source's replay ends instead of
	// guessing from timing. The replay is the lines the source already held when its
	// stream opened; they come first. ReplayCount is optional: a handler without it
	// works as it always did, and its sessions never get replay markers.
	//
	// Handler is unchanged. It opens the same single stream, with the same request,
	// whether or not ReplayCount is set and whether or not the reader asked: the SDK
	// never splits it or filters it. (For a session that asked it opens the stream
	// after ReplayCount returns, so the open is later by that call.) ReplayCount only
	// says how many of the first lines of that stream are the replay. The SDK flags
	// those lines ([LogLine.Replay]) and sends [StreamEventSourceCaughtUp] after the
	// last one. It changes what the SDK says about the lines, never which lines it
	// delivers.
	//
	// The SDK calls ReplayCount only for a session whose reader asked for replay
	// markers, before each attempt to open a source and with the request Handler is
	// about to get. A reopened source is an attempt too: its tail is delivered again
	// and counted again. A request with Follow false is not asked: that stream ends,
	// and its end is the boundary. Return how many lines, each ended by a newline, at
	// the head of the stream Handler will open for that request were already in the
	// source; zero for none. A source that can measure with a bounded read does so
	// (Kubernetes: a follow=false read with the same tail, since and limit bounds,
	// counting its complete lines, and nothing else: it needs none of the lines); a
	// source that knows its boundary natively returns it.
	//
	// A count says how many lines come first, and the SDK flags that many, whichever
	// lines they turn out to be. If the source changed between the count and the open,
	// they can differ from the lines counted (the window of a tail-limited stream moves
	// forward with the newest lines), but their number is the number counted. A stream
	// that opens with more lines than counted (lines arrived in between and nothing
	// limits the tail) has the extra ones live. One that opens with fewer (the source
	// lost some in between, as a log rotation does) and then waits has its next live
	// lines flagged as replay until the count is reached, and the caught-up event
	// follows the counted line: a source that stays quiet holds it back, and until it
	// comes the reader cannot tell the source's lines are live. None of this changes
	// which lines are delivered. A count that cannot be given is an error: the attempt
	// fails as a failed open does (retried, then reported) and no caught-up event is
	// sent for it. Like Handler, ReplayCount is not bounded by the SDK: honour the
	// context it is given.
	//
	// A session for a group resource (a key with a [SourceResolver]) streams its
	// resolved sources through the registered handler with the smallest key, so that
	// handler's ReplayCount decides whether those sessions get replay markers.
	ReplayCount   ReplayCountFunc   `json:"-"`
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

// ReplayCountFunc reports how many of the first lines of the stream a handler opens for
// req are replay, lines the source already held; see [Handler.ReplayCount].
type ReplayCountFunc func(ctx *types.PluginContext, req LogStreamRequest) (int64, error)

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
