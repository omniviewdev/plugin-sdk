package exec

import (
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
)

// EngineSeams are the moments of a command's end at which a test can hold the engine, to force an order that a real run
// reaches only by chance.
type EngineSeams struct {
	// FunctionEnded runs on the function's goroutine right after it ended its session, before it reports what it returned.
	FunctionEnded func()
	// AbandonDecided runs on the supervisor's goroutine right after it decided whether to give up on the function, and on the
	// goroutine that waits for an Options callback or a Classify right after it decided whether to give up on it.
	AbandonDecided func(abandoned bool)
	// EndEmitted runs on the registry's finisher goroutine right after a command's Done is closed, before its id is released.
	EndEmitted func()
	// CallReturned runs on the goroutine of an Options callback or a Classify right after it returned, before the engine hears of
	// it.
	CallReturned func()
	// Now is the clock of the log limits (a line a host could provoke without end is written at most once a second for each
	// target). Nil is the wall clock.
	Now func() time.Time
	// NewTimer makes the timers of the waits for author code that was told to stop: the grace, the kill wait and the
	// shutdown grace. A test that supplies it decides when each runs out. Nil is the real timer.
	NewTimer func(d time.Duration) (c <-chan time.Time, stop func())
}

func (s EngineSeams) apply(e *engine) {
	e.afterFunctionEnded, e.afterAbandonDecision, e.afterEndEmitted, e.afterCallReturned = s.FunctionEnded, s.AbandonDecided, s.EndEmitted, s.CallReturned
	e.now, e.newTimer = s.Now, s.NewTimer
}

// SetEngineSeams sets the seams of an engine made by NewEngine. A test calls it before it starts a command.
func SetEngineSeams(eng exechook.Engine, s EngineSeams) {
	s.apply(eng.(*hookEngine).server.engine)
}

// SetPluginEngineSeams sets the seams of the engine of a plugin that serves targets (what exectestkit.Serve builds).
func SetPluginEngineSeams(p *Plugin, s EngineSeams) {
	s.apply(p.v2.server.engine)
}

// ConfigurePluginEngine applies the timings and bounds of cfg to the engine of a plugin that serves targets (what
// exectestkit.Serve builds), as NewEngine does for an in-process engine: a test shortens the grace and the Options deadline
// of the engine behind the served Options RPC. A test calls it before the first call.
func ConfigurePluginEngine(p *Plugin, cfg exechook.EngineConfig) {
	p.v2.server.engine.configure(cfg)
}
