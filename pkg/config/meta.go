package config

import (
	"io"
	"os"
	"slices"

	"github.com/hashicorp/go-plugin"
	"gopkg.in/yaml.v3"
)

// PluginMetaFormat selects the serialized format of a plugin manifest.
type PluginMetaFormat int

const (
	// PluginMetaFormatYAML is the YAML format for a plugin config.
	PluginMetaFormatYAML PluginMetaFormat = iota
	// PluginMetaFormatJSON is the JSON format for a plugin config.
	PluginMetaFormatJSON
)

// PluginMeta is the plugin description file located at the root of a plugin.
type PluginMeta struct {
	ID                 string             `json:"id"                   yaml:"id"`
	Version            string             `json:"version"              yaml:"version"`
	Name               string             `json:"name"                 yaml:"name"`
	Icon               string             `json:"icon"                 yaml:"icon"`
	IconURL            string             `json:"icon_url"             yaml:"icon_url,omitempty"`
	Description        string             `json:"description"          yaml:"description"`
	Repository         string             `json:"repository"           yaml:"repository"`
	Website            string             `json:"website"              yaml:"website"`
	Category           string             `json:"category"             yaml:"category,omitempty"`
	License            string             `json:"license"              yaml:"license,omitempty"`
	Markdown           string             `json:"-"                    yaml:"-"`
	Author             *PluginAuthor      `json:"author,omitempty"     yaml:"author,omitempty"`
	Maintainers        []PluginMaintainer `json:"maintainers"          yaml:"maintainers"`
	Tags               []string           `json:"tags"                 yaml:"tags"`
	Dependencies       []string           `json:"dependencies"         yaml:"dependencies"`
	Capabilities       []string           `json:"capabilities"         yaml:"capabilities"`
	Theme              PluginTheme        `json:"theme"                yaml:"theme"`
	Components         PluginComponents   `json:"components"           yaml:"components"`
	SchemaVersion      int                `json:"schema_version"       yaml:"schema_version,omitempty"`
	SDKProtocolVersion int                `json:"sdk_protocol_version" yaml:"sdk_protocol_version,omitempty"`
	// StateIsolation is inspectable before launch; only a trusted builder's
	// attestation may authorize isolated startup, never self-asserted YAML.
	StateIsolation *StateIsolationProvenance `json:"state_isolation,omitempty" yaml:"state_isolation,omitempty"`
}

// HasUICapabilities checks if the plugin has UI capabilities.
func (m *PluginMeta) HasUICapabilities() bool {
	return slices.Contains(m.Capabilities, "ui")
}

// HasBackendCapabilities checks if the plugin has any backend capabilities.
func (m *PluginMeta) HasBackendCapabilities() bool {
	caps := []string{"resource", "exec", "networker", "settings", "log", "metric"}

	for _, capability := range m.Capabilities {
		if slices.Contains(caps, capability) {
			return true
		}
	}
	return false
}

// PluginMaintainer identifies a contact responsible for maintaining the plugin.
type PluginMaintainer struct {
	Name  string `json:"name"  yaml:"name"`
	Email string `json:"email" yaml:"email"`
}

// PluginAuthor identifies the original author and optional contact links.
type PluginAuthor struct {
	Name  string `json:"name"  yaml:"name"`
	Email string `json:"email" yaml:"email,omitempty"`
	URL   string `json:"url"   yaml:"url,omitempty"`
}

// PluginThemeColors defines the plugin's primary, secondary, and tertiary accent colors.
type PluginThemeColors struct {
	Primary   string `json:"primary"   yaml:"primary"`
	Secondary string `json:"secondary" yaml:"secondary"`
	Tertiary  string `json:"tertiary"  yaml:"tertiary"`
}

// PluginTheme supplies presentation defaults for the plugin's frontend.
type PluginTheme struct {
	Colors PluginThemeColors `json:"colors" yaml:"colors"`
}

// PluginComponents declares frontend components associated with resource views.
type PluginComponents struct {
	Resource []PluginResourceComponent `json:"resource" yaml:"resource"`
}

// PluginComponentArea selects the host surface where a resource component appears.
type PluginComponentArea string

const (
	// PluginComponentAreaEditor places a component in the resource editor.
	PluginComponentAreaEditor PluginComponentArea = "EDITOR"
	// PluginComponentAreaSidebar places a component in the resource sidebar.
	PluginComponentAreaSidebar PluginComponentArea = "SIDEBAR"
)

// PluginResourceComponent binds a named frontend extension to resource types and a host surface.
type PluginResourceComponent struct {
	Name           string              `json:"name"      yaml:"name"`
	Plugin         string              `json:"plugin"    yaml:"plugin"`
	Area           PluginComponentArea `json:"area"      yaml:"area"`
	Resources      []string            `json:"resources" yaml:"resources"`
	ExtensionPoint string              `json:"extension" yaml:"extension"`
}

// Load decodes one YAML manifest from reader into m, retaining decoder handling of omitted fields.
func (m *PluginMeta) Load(reader io.Reader) error {
	return yaml.NewDecoder(reader).Decode(m)
}

// LoadMarkdown loads a plugin Markdown file from a given path (if it exists)
// into the plugin config.
func (m *PluginMeta) LoadMarkdown(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	markdown, err := io.ReadAll(file)
	if err != nil {
		return err
	}
	m.Markdown = string(markdown)
	return nil
}

// GenerateHandshakeConfig generates a handshake config for the plugin.
// The magic cookie value uses only the plugin ID (not version) so that
// version bumps don't force binary rebuilds just for the handshake.
// Version negotiation is handled by the PluginLifecycle.GetInfo() RPC.
func (m *PluginMeta) GenerateHandshakeConfig() plugin.HandshakeConfig {
	if m.ID == "" {
		panic("plugin ID must be set")
	}

	return plugin.HandshakeConfig{
		ProtocolVersion:  1,
		MagicCookieKey:   "OMNIVIEW",
		MagicCookieValue: m.ID,
	}
}
