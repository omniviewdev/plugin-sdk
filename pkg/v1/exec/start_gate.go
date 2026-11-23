package exec

import (
	"slices"
	"sync"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
)

// exectest.ProcRig holds what a command reports with Proc.Started to the same gate as the SDK. The gate is not exported,
// so it is handed over through internal/exechook, which is the only place that knows it.
func init() {
	exechook.SetStartGate(func(request Request, opened Capabilities) exechook.StartGate[SessionFacts, Capabilities] {
		gate := newStartGate(request, opened)
		return exechook.StartGate[SessionFacts, Capabilities]{Report: gate.report, Output: gate.output, Effective: gate.effective}
	})
}

// What a refused report says, for the caller to tell the author: the SDK logs it, ProcRig fails the author's test.
const (
	startedTwice       = "Started: reported twice; the first report stands and this one is ignored"
	startedAfterOutput = "Started: reported after the first output; the SDK sent the Started it made from the facts of Opened before that output, and this report is ignored"
)

// startGate holds the rule for the Started of one session (proto/exec/v2/exec.proto, message Started): the host receives
// at most one, and always before the first Output. The author's function reports with Proc.Started, usually before it
// writes, but it may call it from another goroutine than the one that writes, and a library that copies the command's
// output (client-go's executor) writes from its own: the order of the two is not the author's to guarantee, and no
// output may run under facts the host has not been told.
//
// So the gate is what the engine passes both through. A report that comes before the first output is taken, once. The
// first output, when no report was taken, makes a Started from the facts of Opened (the requested program and the
// capabilities the session was opened with, and nothing the author did not say), which must be sent before that output.
// A report that comes after the first output, or a second one, is refused with a violation and changes nothing.
//
// What the host receives is handed to the send function that the caller passes to report and to output, which the gate
// calls with its lock held: the Started is sent before report or output returns, so that an output frame sent after
// output returned is always behind it. send must not call the gate.
//
// The gate holds the capabilities of the session too: what Opened carried, narrowed by the report taken, which may only
// narrow what the session was opened with.
type startGate struct {
	mu      sync.Mutex
	argv    []string     // the requested program
	current Capabilities // what Opened carried, narrowed by the report that was taken

	reported   bool // the author's report was taken
	outputSeen bool // the first output was seen
}

// newStartGate returns the gate of a session that was opened for request with the capabilities opened. It keeps its own
// copy of both.
func newStartGate(request Request, opened Capabilities) *startGate {
	return &startGate{argv: slices.Clone(request.Argv), current: cloneCapabilities(opened)}
}

// report takes the author's report, which is what Proc.Started does. A report before the first output, and the first
// one, is taken: it is held to the capabilities the session was opened with (a field above them is replaced by them and
// named in the violations returned, as narrowCapabilities names it), and what the host receives, the report with the
// session's capabilities in it, is handed to send. A report that is late or a second is refused: it returns a violation
// that says which, sends nothing and changes nothing.
func (g *startGate) report(facts SessionFacts, send func(SessionFacts)) (violations []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case g.reported:
		return []string{startedTwice}
	case g.outputSeen:
		return []string{startedAfterOutput}
	}
	g.reported = true
	taken := cloneSessionFacts(facts)
	if facts.Capabilities != nil {
		g.current, violations = narrowCapabilities(g.current, *facts.Capabilities)
	}
	taken.Capabilities = new(cloneCapabilities(g.current))
	send(taken)
	return violations
}

// output is called before the output of the session is delivered, the first byte of either stream: only that call does
// anything. When the author has not reported, it hands the Started that the SDK makes from the facts of Opened to send,
// which the caller sends before it delivers the output; after that no report is taken.
func (g *startGate) output(send func(SessionFacts)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.outputSeen {
		return
	}
	g.outputSeen = true
	if g.reported {
		return
	}
	send(SessionFacts{
		Program:      Program{Argv: slices.Clone(g.argv)},
		Capabilities: new(cloneCapabilities(g.current)),
	})
}

// effective returns the capabilities of the session now: what Opened carried, narrowed by the report that was taken.
func (g *startGate) effective() Capabilities {
	g.mu.Lock()
	defer g.mu.Unlock()
	return cloneCapabilities(g.current)
}

// cloneSessionFacts returns a deep copy: the terminal flag, the capabilities with their lists and the program's argv are
// not shared with the original.
func cloneSessionFacts(f SessionFacts) SessionFacts {
	if f.TTY != nil {
		f.TTY = new(*f.TTY)
	}
	if f.Capabilities != nil {
		f.Capabilities = new(cloneCapabilities(*f.Capabilities))
	}
	f.Program.Argv = slices.Clone(f.Program.Argv)
	return f
}
