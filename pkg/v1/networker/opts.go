package networker

import (
	"time"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/pkg/utils/timeutil"
	"github.com/omniviewdev/plugin-sdk/settings"
)

// ManagerConfig configures the Manager.
type ManagerConfig struct {
	Logger       logging.Logger
	Settings     settings.Provider
	PortChecker  PortChecker    // nil → RealPortChecker
	Clock        timeutil.Clock // nil → timeutil.RealClock
	CloseTimeout time.Duration  // default 10s
}

// PluginOpts contains the options for the networker plugin, passed to
// RegisterPlugin.
type PluginOpts struct {
	// ResourceForwarders maps resource key → ResourceForwarder.
	ResourceForwarders map[string]ResourceForwarder
	// StaticForwarders maps key → StaticForwarder.
	StaticForwarders map[string]StaticForwarder
}
