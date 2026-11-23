package sdk

import (
	"github.com/hashicorp/go-plugin"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/pkg/config"
)

// NewPluginForTest returns a plugin with the given id and log and no capability, without the manifest that NewPlugin reads.
func NewPluginForTest(id string, log logging.Logger) *Plugin {
	return &Plugin{Log: log, meta: config.PluginMeta{ID: id}}
}

// ServeWith serves the plugin through serve, which is go-plugin's entry point or a stand-in for it (go-plugin's test mode):
// everything else Serve does runs as it does in production, including the server factory it hands go-plugin and the shutdown
// of the capabilities once go-plugin returns. It is for the tests of this package that serve a plugin in process.
func (p *Plugin) ServeWith(serve func(*plugin.ServeConfig)) { p.serveWith(serve) }

// The bounds the plugin gives its capabilities when it stops, for the tests that pin what a capability is given.
const (
	CapabilityPreStopBound  = capabilityPreStopBound
	CapabilityShutdownBound = capabilityShutdownBound
)
