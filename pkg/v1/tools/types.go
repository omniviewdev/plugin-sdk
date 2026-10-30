package tools

import (
	"encoding/json"
	"fmt"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	toolspb "github.com/omniviewdev/plugin-sdk/proto/v1/tools"
)

// Scope states what a tool acts on.
type Scope string

const (
	// ScopePlugin tools act on the plugin itself and take no connection.
	ScopePlugin Scope = "plugin"
	// ScopeConnection tools act on one plugin connection. The host resolves
	// the connection and passes it in the PluginContext.
	ScopeConnection Scope = "connection"
)

// Annotations are the MCP tool annotation hints.
type Annotations struct {
	ReadOnly    bool `json:"readOnly"`
	Destructive bool `json:"destructive"`
	Idempotent  bool `json:"idempotent"`
	OpenWorld   bool `json:"openWorld"`
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

// Result is the outcome of a tool call. IsError marks a tool-level failure
// that the caller (an AI model) should read, as opposed to a transport error.
type Result struct {
	Content           []Content       `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError"`
}

// CallInput names the tool and carries its JSON object arguments.
type CallInput struct {
	Name      string
	Arguments json.RawMessage
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

// ErrorResult returns a tool-level error result.
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
			ReadOnly:    a.GetReadOnly(),
			Destructive: a.GetDestructive(),
			Idempotent:  a.GetIdempotent(),
			OpenWorld:   a.GetOpenWorld(),
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
	}
	for _, c := range r.GetContent() {
		out.Content = append(out.Content, Content{Text: c.GetText()})
	}
	return out
}
