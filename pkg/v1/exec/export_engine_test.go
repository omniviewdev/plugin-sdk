package exec

import (
	"context"
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
	// NewLeaseTimer makes the timer of the lease of a detached session, which is not one of the waits for author code: a test that
	// supplies it decides when each lease runs out. Nil is the real timer. The end of a lease is read from Now, and so is the age
	// of a tombstone: a test that gives both drives them with one clock.
	NewLeaseTimer func(d time.Duration) (c <-chan time.Time, stop func())
	// StoreWindow runs in a call that reads two of the engine's stores, after the first and before the second: in a listing of the
	// sessions after the live ones were read and before the tombstones are, and in a read of the output of a session after the
	// lookup of its tombstone and before the lookup of the live session. A session that ends there is the window that the two locks
	// leave open.
	StoreWindow func()
	// StreamLeaving runs on the goroutine of a served stream's handler (its receiver, and the handler itself) when it has noticed that
	// its stream is over (the host half-closed it, cancelled it or lost its connection), and before it releases the session: the
	// window in which the stream is over and the engine has not yet been told, which a test holds open.
	StreamLeaving func()
}

func (s EngineSeams) apply(e *engine) {
	e.afterFunctionEnded, e.afterAbandonDecision, e.afterEndEmitted, e.afterCallReturned = s.FunctionEnded, s.AbandonDecided, s.EndEmitted, s.CallReturned
	e.now, e.newTimer, e.newLeaseTimer, e.betweenStoreReads = s.Now, s.NewTimer, s.NewLeaseTimer, s.StoreWindow
	e.beforeStreamLeaves = s.StreamLeaving
}

// PluginTombstones returns how many tombstones the engine of a plugin that serves targets holds, and how many bytes of output
// they keep.
func PluginTombstones(p *Plugin) (count, bytes int) {
	return p.v2.server.engine.tombs.len(), p.v2.server.engine.tombs.retainedBytes()
}

// PluginTombstoneState returns the ids the store of the tombstones holds, oldest first, and whether its timer is armed, read without
// sweeping what has expired: a use of the store would take it, and the timer is what a test of the timer has to see.
func PluginTombstoneState(p *Plugin) (ids []string, timerArmed bool) {
	s := p.v2.server.engine.tombs
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.order {
		ids = append(ids, t.id)
	}
	return ids, s.timer != nil
}

// PluginTombstoneRetainedCopy returns how many bytes of the replay that a stream was given the tombstone of an ended session still
// refers to, and whether the tombstone holds the session's ring. The output of a tombstone is what its ring holds, counted by the
// store's budget; a copy that a stream took when it attached is not the ring's and is no part of the budget, so it must be gone.
func PluginTombstoneRetainedCopy(p *Plugin, id string) (bytes int, held bool) {
	t, found := p.v2.server.engine.tombs.get(id)
	if !found || t.ring == nil {
		return 0, false
	}
	r := t.ring
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur := r.att.cur; cur != nil && cur.snap != nil {
		for _, c := range cur.snap.chunks[cur.snap.idx:] {
			bytes += c.n
		}
		bytes -= cur.snap.pos
	}
	return bytes, true
}

// PluginLive returns the ids of the commands the engine of a plugin that serves targets holds, sorted.
func PluginLive(p *Plugin) []string { return p.v2.server.engine.live() }

// PluginCurrentSize returns the terminal size that the replay ring of a live session of a plugin that serves targets says is in force
// now (the last size the function took), and false for a session that has none: it has no terminal, or it is not live.
func PluginCurrentSize(p *Plugin, id string) (cols, rows uint16, ok bool) {
	c, found := p.v2.server.engine.session(id)
	if !found {
		return 0, 0, false
	}
	c.ring.mu.Lock()
	defer c.ring.mu.Unlock()
	size, ok := c.ring.currentSize()
	return size.Cols, size.Rows, ok
}

// StatusError decodes the status that a handler of the exec service returned, as the host's client does: an *Error for a refusal of
// the exec lane, and the resource lane's own error for the connection and the catalog pin.
func StatusError(err error) error { return errorFromStatus(context.Background(), err) }

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
