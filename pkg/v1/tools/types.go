package tools

import (
	"encoding/json"
	"fmt"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	toolspb "github.com/omniviewdev/plugin-sdk/proto/v1/tools"
)

// Scope states what a tool acts on.
type Scope string

const (
	// ScopePlugin tools act on the plugin itself and take no connection.
	ScopePlugin Scope = "plugin"
	// ScopeConnection tools act on one configured connection selected by an
	// explicit CallInput route and admitted by the native connection registry.
	ScopeConnection Scope = "connection"
)

// Annotations are the MCP tool annotation hints.
type Annotations struct {
	ReadOnly    *bool `json:"readOnly,omitempty"`
	Destructive *bool `json:"destructive,omitempty"`
	Idempotent  *bool `json:"idempotent,omitempty"`
	OpenWorld   *bool `json:"openWorld,omitempty"`
}

// EffectiveAnnotations contains MCP defaults, not authorization decisions.
type EffectiveAnnotations struct {
	ReadOnly, Destructive, Idempotent, OpenWorld bool
}

// Resolve preserves explicit false values while applying MCP defaults.
func (a Annotations) Resolve() EffectiveAnnotations {
	value := func(p *bool, fallback bool) bool {
		if p != nil { return *p }
		return fallback
	}
	return EffectiveAnnotations{value(a.ReadOnly, false), value(a.Destructive, true), value(a.Idempotent, false), value(a.OpenWorld, true)}
}

// Descriptor describes one tool to the host.
type Descriptor struct {
	Name         string          `json:"name"`
	Title        string          `json:"title"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	Scope        Scope           `json:"scope"`
	Annotations  Annotations     `json:"annotations"`
}

// Content is one text block of a tool result.
type Content struct {
	Text string `json:"text"`
}

// Result is the outcome of a tool call. IsError results expose only reviewed,
// fixed safe presentation; diagnostic content stays local.
type Result struct {
	Content           []Content       `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError"`
	ErrorCode         string          `json:"errorCode,omitempty"`
}

// CallInput names the tool and carries its JSON object arguments.
type CallInput struct {
	Name      string
	Arguments json.RawMessage
	Route     *resourcev2.AccessRoute
}

// Handler runs one tool call.
type Handler func(ctx *types.PluginContext, arguments json.RawMessage) (*Result, error)

// Tool pairs a descriptor with its handler.
type Tool struct {
	Descriptor

	Handler Handler
}

// TextResult returns a plain text result.
func TextResult(text string) *Result {
	return &Result{Content: []Content{{Text: text}}}
}

// JSONResult returns v as indented JSON text and as structured content.
func JSONResult(v any) (*Result, error) {
	compact, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal tool result: %w", err)
	}
	indented, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal tool result: %w", err)
	}
	return &Result{
		Content:           []Content{{Text: string(indented)}},
		StructuredContent: compact,
	}, nil
}

// ErrorResult creates a local failure. Diagnostic text is removed at dispatch.
func ErrorResult(format string, args ...any) *Result {
	return &Result{
		Content: []Content{{Text: fmt.Sprintf(format, args...)}},
		IsError: true,
	}
}

// ObjectSchema builds a closed JSON object schema.
func ObjectSchema(properties map[string]any, required ...string) json.RawMessage {
	if properties == nil {
		properties = map[string]any{}
	}
	if required == nil {
		required = []string{}
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		panic(fmt.Sprintf("tools: marshal object schema: %v", err))
	}
	return raw
}

func scopeToProto(s Scope) toolspb.ToolScope {
	switch s {
	case ScopePlugin:
		return toolspb.ToolScope_TOOL_SCOPE_PLUGIN
	case ScopeConnection:
		return toolspb.ToolScope_TOOL_SCOPE_CONNECTION
	default:
		return toolspb.ToolScope_TOOL_SCOPE_UNSPECIFIED
	}
}

func scopeFromProto(s toolspb.ToolScope) Scope {
	switch s {
	case toolspb.ToolScope_TOOL_SCOPE_PLUGIN:
		return ScopePlugin
	case toolspb.ToolScope_TOOL_SCOPE_CONNECTION:
		return ScopeConnection
	case toolspb.ToolScope_TOOL_SCOPE_UNSPECIFIED:
		return ""
	default:
		return ""
	}
}

func descriptorToProto(d Descriptor) *toolspb.ToolDescriptor {
	return &toolspb.ToolDescriptor{
		Name:         d.Name,
		Title:        d.Title,
		Description:  d.Description,
		InputSchema:  d.InputSchema,
		OutputSchema: d.OutputSchema,
		Scope:        scopeToProto(d.Scope),
		Annotations: &toolspb.ToolAnnotations{
			ReadOnly:    d.Annotations.ReadOnly,
			Destructive: d.Annotations.Destructive,
			Idempotent:  d.Annotations.Idempotent,
			OpenWorld:   d.Annotations.OpenWorld,
		},
	}
}

func descriptorFromProto(d *toolspb.ToolDescriptor) Descriptor {
	out := Descriptor{
		Name:         d.GetName(),
		Title:        d.GetTitle(),
		Description:  d.GetDescription(),
		InputSchema:  d.GetInputSchema(),
		OutputSchema: d.GetOutputSchema(),
		Scope:        scopeFromProto(d.GetScope()),
	}
	if a := d.GetAnnotations(); a != nil {
		out.Annotations = Annotations{
			ReadOnly:    a.ReadOnly,
			Destructive: a.Destructive,
			Idempotent:  a.Idempotent,
			OpenWorld:   a.OpenWorld,
		}
	}
	return out
}

func resultToProto(r *Result) *toolspb.CallToolResponse {
	if r == nil {
		return &toolspb.CallToolResponse{}
	}
	out := &toolspb.CallToolResponse{
		Content:           make([]*toolspb.ToolContent, 0, len(r.Content)),
		StructuredContent: r.StructuredContent,
		IsError:           r.IsError,
		ErrorCode:         r.ErrorCode,
	}
	for _, c := range r.Content {
		out.Content = append(out.Content, &toolspb.ToolContent{
			Kind: &toolspb.ToolContent_Text{Text: c.Text},
		})
	}
	return out
}

func resultFromProto(r *toolspb.CallToolResponse) *Result {
	out := &Result{
		Content:           make([]Content, 0, len(r.GetContent())),
		StructuredContent: r.GetStructuredContent(),
		IsError:           r.GetIsError(),
		ErrorCode:         r.GetErrorCode(),
	}
	for _, c := range r.GetContent() {
		out.Content = append(out.Content, Content{Text: c.GetText()})
	}
	return out
}
