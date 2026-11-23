package exec

import execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"

// OpenServer returns the exec v2 service of a plugin that serves targets, for a test that drives a stream of it in memory, with
// no transport: the handler of Open is called with a stream the test holds.
func OpenServer(p *Plugin) execv2.ExecServer { return p.v2.server }
