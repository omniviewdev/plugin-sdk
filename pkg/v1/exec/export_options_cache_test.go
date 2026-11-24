package exec

// OptionsCacheLen returns how many results the cache of Options callbacks in the engine of a plugin that serves targets holds
// (what exectestkit.Serve builds): the entries that have not been swept or dropped, expired ones that nothing has swept yet
// included.
func OptionsCacheLen(p *Plugin) int { return p.v2.server.engine.optionsCache.len() }
