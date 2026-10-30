// Package types defines shared plugin metadata, connection state, and request context values.
package types

// Capability represents a plugin capability as a string type.
// Using strings allows direct matching with YAML/JSON config values
// and makes adding new capabilities non-breaking.
type Capability string

const (
	// CapabilityResource enables resource discovery and operations.
	CapabilityResource Capability = "resource"
	// CapabilityExec enables remote command execution.
	CapabilityExec Capability = "exec"
	// CapabilityNetworker enables network forwarding.
	CapabilityNetworker Capability = "networker"
	// CapabilityLog enables log streaming.
	CapabilityLog Capability = "log"
	// CapabilityMetric enables metric queries.
	CapabilityMetric Capability = "metric"
	// CapabilitySettings enables plugin settings.
	CapabilitySettings Capability = "settings"
	// CapabilityTools enables tool execution.
	CapabilityTools Capability = "tools"
	// CapabilityUI enables frontend components without requiring a backend process.
	CapabilityUI Capability = "ui"
)

// IsBackend returns true if this capability requires a backend plugin process.
func (c Capability) IsBackend() bool {
	switch c {
	case CapabilityResource, CapabilityExec, CapabilityNetworker,
		CapabilityLog, CapabilityMetric, CapabilitySettings, CapabilityTools:
		return true
	case CapabilityUI:
		return false
	default:
		return false
	}
}

// String implements the Stringer interface.
func (c Capability) String() string {
	return string(c)
}

// AllCapabilities returns all known capabilities.
func AllCapabilities() []Capability {
	return []Capability{
		CapabilityResource,
		CapabilityExec,
		CapabilityNetworker,
		CapabilityLog,
		CapabilityMetric,
		CapabilitySettings,
		CapabilityTools,
		CapabilityUI,
	}
}

// ParseCapability converts a string to a Capability.
// Returns the Capability and true if valid, or empty and false if unknown.
func ParseCapability(s string) (Capability, bool) {
	c := Capability(s)
	switch c {
	case CapabilityResource, CapabilityExec, CapabilityNetworker,
		CapabilityLog, CapabilityMetric, CapabilitySettings, CapabilityTools, CapabilityUI:
		return c, true
	default:
		return "", false
	}
}
