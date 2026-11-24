package exec

import "context"

// SetPluginBeforeAttach sets the seam that holds an attach on its way to its session, in the engine of a plugin that serves targets:
// hook runs on the goroutine of the handler of an Open that attaches (or of the in-process caller of Attach) once the request was
// admitted, and before the engine looks for the session and registers the stream. ctx is the attach's own: its metadata says which attach
// it is, and it ends when the host cancels the stream. A test holds the call there to make an attach late: the window between the open
// frame having been read and the stream being registered, in which another attach can register and the stream of this one can end. A test
// calls it before the first attach.
func SetPluginBeforeAttach(p *Plugin, hook func(ctx context.Context, id string, from uint64)) {
	p.v2.server.engine.beforeAttach = hook
}
