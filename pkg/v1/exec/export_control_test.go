package exec

import "time"

// SetQueryDeadline sets how long the engine of a plugin that serves targets waits for a function's answer to a host's query for a
// fact before it answers unknown itself. A test calls it before the first call.
func SetQueryDeadline(p *Plugin, d time.Duration) { p.v2.server.engine.cfg.queryDeadline = d }
