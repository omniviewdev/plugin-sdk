package exec

import "github.com/omniviewdev/plugin-sdk/internal/exechook"

// EngineSeams are the two moments of a command's end at which a test can hold the engine, to force an order that a real
// run reaches only by chance.
type EngineSeams struct {
	// FunctionEnded runs on the function's goroutine right after it ended its session, before it reports what it returned.
	FunctionEnded func()
	// AbandonDecided runs on the supervisor's goroutine right after it decided whether to give up on the function.
	AbandonDecided func(abandoned bool)
}

// SetEngineSeams sets the seams of an engine made by NewEngine. A test calls it before it starts a command.
func SetEngineSeams(eng exechook.Engine, s EngineSeams) {
	e := eng.(*hookEngine).server.engine
	e.afterFunctionEnded, e.afterAbandonDecision = s.FunctionEnded, s.AbandonDecided
}
