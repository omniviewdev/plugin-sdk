package exec

import (
	"slices"
	"sync"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The control channels of a Proc: the signals the host asks for (Proc.Signals), the queries it asks (Proc.Queries) and the facts the
// function pushes (Proc.Fact). A command owns them, and not the stream that serves it: the host's frames reach the function through
// the command's methods (Signal, AskFact), and what the function says goes to the stream that is attached to the command (pushSink).
// A session that outlives its stream keeps its function's channels as they are.

const (
	// signalQueue is how many signals wait for a function that does not read Proc.Signals: a function that reads at all sees the
	// newest of them, and the SDK never waits for it.
	signalQueue = 8
	// maxOutstandingQueries is the most queries of one session that wait for its function at once.
	maxOutstandingQueries = 8
)

// pushSink takes the facts a function pushes. It must not block, and must not call back into the control that calls it.
type pushSink interface {
	pushFact(kind FactKind, fact *execv2.Fact)
}

// procControl is what a command keeps for the control channels of its Proc. The zero value is the control of a command that has not
// been handed to its function yet: init makes it ready, and close ends it with the command.
type procControl struct {
	// signals and queries are set once, by init, before any goroutine of the command can see the control. queries is nil unless the
	// capabilities the command was opened with declare a fact that is provided.
	signals chan Signal
	queries chan Query

	mu     sync.Mutex
	closed bool
	// waiting is how many queries were handed to the function and are neither answered nor expired.
	waiting int
	// sink is where the facts the function pushes go: the stream attached to the command, once there is one. latest is the newest
	// fact pushed for each kind, which a stream that attaches after it was pushed is given.
	sink   pushSink
	latest [len(factKinds)]*execv2.Fact
}

// init makes the channels of a command opened with the capabilities opened and returns what Proc carries.
func (p *procControl) init(c *command, opened Capabilities) (signals <-chan Signal, queries <-chan Query, fact func(Fact)) {
	p.signals = make(chan Signal, signalQueue)
	if opened.CwdFact != FactModeNone || opened.ForegroundFact != FactModeNone {
		p.queries = make(chan Query, maxOutstandingQueries)
	}
	return p.signals, p.queries, c.pushFact
}

// close ends the control with the command: Proc.Signals and Proc.Queries close, so that a goroutine of the function that ranges over
// them returns with the rest, and nothing is handed to the function after it. It may be called again.
func (p *procControl) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	if p.signals != nil {
		close(p.signals)
	}
	if p.queries != nil {
		close(p.queries)
	}
}

// queueSignal puts sig in the function's queue, dropping the oldest signal when the queue is full: the newest is what the host
// meant last, and a "Stop" that follows an "Interrupt" must not be the one that is lost. It reports whether a signal was dropped, and
// false for open when the command has ended.
func (p *procControl) queueSignal(sig Signal) (dropped, open bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false, false
	}
	for {
		select {
		case p.signals <- sig:
			return dropped, true
		default:
		}
		// Full. Only this method sends, under the lock, so a place is free once one signal is taken out.
		select {
		case <-p.signals:
			dropped = true
		default:
		}
	}
}

// deliverQuery hands the query to the function, and reports whether it did: not when the command has ended, when the function was not
// given Proc.Queries, when it already holds as many queries as it may, and when its queue is full of queries that nobody read.
func (p *procControl) deliverQuery(q *pendingQuery) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.queries == nil || p.waiting >= maxOutstandingQueries {
		return false
	}
	select {
	case p.queries <- Query{Kind: q.kind, Answer: q.answer}:
		p.waiting++
		return true
	default:
		return false
	}
}

// released frees the place of a query that was answered or expired.
func (p *procControl) released() {
	p.mu.Lock()
	p.waiting--
	p.mu.Unlock()
}

// push hands a fact the function pushed to the attached stream, and keeps it for one that attaches later.
func (p *procControl) push(kind FactKind, fact *execv2.Fact) {
	slot := factSlot(kind)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.latest[slot] = fact
	if p.sink != nil {
		p.sink.pushFact(kind, fact)
	}
}

// attach makes s the stream that takes the facts the function pushes, and gives it the newest of each kind pushed so far, so that a
// function that pushes in its first moments, before the stream began to receive, is not lost. The returned release ends the
// attachment, unless another stream has taken it over.
func (p *procControl) attach(s pushSink) (release func()) {
	p.mu.Lock()
	p.sink = s
	for slot, fact := range p.latest {
		if fact != nil {
			s.pushFact(factKinds[slot], fact)
		}
	}
	p.mu.Unlock()
	return func() {
		p.mu.Lock()
		if p.sink == s {
			p.sink = nil
		}
		p.mu.Unlock()
	}
}

// peek returns the capabilities of the session now, for a read: its lists are shared with the gate and must not be changed. The gate
// replaces a list when it narrows and never changes one in place, so a copy that is read while the gate moves on is a consistent one.
func (g *startGate) peek() Capabilities {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.current
}

// declaresSignal reports whether sig is among the signals the session's capabilities declare now.
func (g *startGate) declaresSignal(sig Signal) bool {
	return sig != SignalUnspecified && slices.Contains(g.peek().Signals, sig)
}
