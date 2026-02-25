package types

// Capability represents a plugin capability as a string type.
// Using strings allows direct matching with YAML/JSON config values
// and makes adding new capabilities non-breaking.
type Capability string

const (
	CapabilityResource  Capability = "resource"
	CapabilityExec      Capability = "exec"
	CapabilityNetworker Capability = "networker"
	CapabilityLog       Capability = "log"
	CapabilityMetric    Capability = "metric"
	CapabilitySettings  Capability = "settings"
	CapabilityUI        Capability = "ui"
)

// IsBackend returns true if this capability requires a backend plugin process.
func (c Capability) IsBackend() bool {
	switch c {
	case CapabilityResource, CapabilityExec, CapabilityNetworker,
		CapabilityLog, CapabilityMetric, CapabilitySettings:
		return true
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
		CapabilityUI,
	}
}

// ParseCapability converts a string to a Capability.
// Returns the Capability and true if valid, or empty and false if unknown.
func ParseCapability(s string) (Capability, bool) {
	c := Capability(s)
	switch c {
	case CapabilityResource, CapabilityExec, CapabilityNetworker,
		CapabilityLog, CapabilityMetric, CapabilitySettings, CapabilityUI:
		return c, true
	default:
		return "", false
	}
}
