package exec

import (
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

// sgOpened is what a Pod exec session is opened with: the kind's set narrowed by the mode.
func sgOpened() Capabilities {
	opened := ccPod()
	opened.HostRestart = SurvivalNone
	return opened
}

// sgSent collects what the gate hands to send, which is what the host receives, in order.
type sgSent struct{ facts []SessionFacts }

func (s *sgSent) send(f SessionFacts) { s.facts = append(s.facts, f) }

// A report before the first output is taken, once. What the host receives is the report, with the capabilities of the
// session in it: the ones it was opened with, narrowed by what the report says.
func TestAReportBeforeTheFirstOutputIsTakenOnce(t *testing.T) {
	gate := newStartGate(Request{Argv: []string{"/bin/sh"}}, sgOpened())
	var sent sgSent
	violations := gate.report(SessionFacts{PID: 7, Transport: "k8s-ws-v5", Program: Program{Argv: []string{"/bin/bash"}, Name: "bash"}}, sent.send)
	if len(violations) != 0 {
		t.Fatalf("a report before any output was refused: %q", violations)
	}
	if len(sent.facts) != 1 || sent.facts[0].PID != 7 || sent.facts[0].Transport != "k8s-ws-v5" || sent.facts[0].Program.Name != "bash" {
		t.Fatalf("the host received %+v, want the report", sent.facts)
	}
	if got := sent.facts[0].Capabilities; got == nil || len(capabilitiesDiff(*got, sgOpened())) > 0 {
		t.Errorf("the report carried no capabilities, so the session keeps the ones it was opened with, and the host receives them: got %+v", got)
	}

	// The first output, and every later one, sends nothing more: the session has had its Started.
	gate.output(sent.send)
	gate.output(sent.send)
	if len(sent.facts) != 1 {
		t.Errorf("the host received %d Started frames, want one", len(sent.facts))
	}
	if diff := capabilitiesDiff(gate.effective(), sgOpened()); len(diff) > 0 {
		t.Errorf("a report that narrows nothing changed the effective capabilities in %v", diff)
	}
}

// The first output of a session whose author has not reported yet sends a Started that the SDK makes from the facts of
// Opened: the requested program and the opened capabilities, and nothing the author did not say. It is handed to send
// before the output is, and only once.
func TestTheFirstOutputWithNoReportSendsAStartedMadeFromOpened(t *testing.T) {
	gate := newStartGate(Request{Argv: []string{"/bin/sh", "-l"}, TTY: true, Size: Size{Cols: 80, Rows: 24}}, sgOpened())
	var sent sgSent
	gate.output(sent.send)
	if len(sent.facts) != 1 {
		t.Fatalf("the first output sent %d Started frames, want one", len(sent.facts))
	}
	got := sent.facts[0]
	if !slices.Equal(got.Program.Argv, []string{"/bin/sh", "-l"}) || got.Program.Name != "" || got.Program.Path != "" {
		t.Errorf("the program is %+v, want the requested argv and nothing else", got.Program)
	}
	if got.Capabilities == nil || len(capabilitiesDiff(*got.Capabilities, sgOpened())) > 0 {
		t.Errorf("the capabilities are %+v, want the opened set", got.Capabilities)
	}
	if got.PID != 0 || got.TTY != nil || !got.Size.IsZero() || got.Term != "" || got.Transport != "" {
		t.Errorf("the SDK claims what the author did not say: %+v", got)
	}

	gate.output(sent.send)
	if len(sent.facts) != 1 {
		t.Errorf("the second output sent a Started: %d in all", len(sent.facts))
	}
	if diff := capabilitiesDiff(gate.effective(), sgOpened()); len(diff) > 0 {
		t.Errorf("the Started the SDK made changed the effective capabilities in %v", diff)
	}
}

// A report after the first output is refused with a violation that says so, and changes nothing: the host received the
// Started the SDK made, and the capabilities of the session are the ones it was opened with, whatever the report says.
func TestAReportAfterTheFirstOutputIsRefusedAndChangesNothing(t *testing.T) {
	gate := newStartGate(Request{Argv: []string{"/bin/sh"}}, sgOpened())
	var sent sgSent
	gate.output(sent.send)

	narrower := sgOpened()
	narrower.ExitReport = ExitReportNone
	violations := gate.report(SessionFacts{PID: 9, Capabilities: &narrower}, sent.send)
	if len(violations) != 1 || !strings.Contains(violations[0], "after the first output") {
		t.Fatalf("violations = %q, want one that says the report came after the first output", violations)
	}
	if len(sent.facts) != 1 || sent.facts[0].PID != 0 {
		t.Errorf("a late report reached the host: %+v", sent.facts)
	}
	if diff := capabilitiesDiff(gate.effective(), sgOpened()); len(diff) > 0 {
		t.Errorf("a late report changed the effective capabilities in %v", diff)
	}
}

// A second report is refused with a violation that says so, and changes nothing: the first stands, before and after the
// first output alike.
func TestASecondReportIsRefusedAndChangesNothing(t *testing.T) {
	for _, outputBetween := range []bool{false, true} {
		gate := newStartGate(Request{Argv: []string{"/bin/sh"}}, sgOpened())
		var sent sgSent
		first := sgOpened()
		first.Signals = []Signal{SignalInt}
		if violations := gate.report(SessionFacts{PID: 1, Capabilities: &first}, sent.send); len(violations) != 0 {
			t.Fatalf("the first report was refused: %q", violations)
		}
		if outputBetween {
			gate.output(sent.send)
		}
		second := sgOpened()
		second.ExitReport = ExitReportNone
		violations := gate.report(SessionFacts{PID: 2, Capabilities: &second}, sent.send)
		if len(violations) != 1 || !strings.Contains(violations[0], "twice") {
			t.Fatalf("output between the reports %v: violations = %q, want one that says the report was made twice", outputBetween, violations)
		}
		if len(sent.facts) != 1 || sent.facts[0].PID != 1 {
			t.Errorf("output between the reports %v: the host received %+v, want only the first", outputBetween, sent.facts)
		}
		want := sgOpened()
		want.Signals = []Signal{SignalInt}
		if diff := capabilitiesDiff(gate.effective(), want); len(diff) > 0 {
			t.Errorf("output between the reports %v: the effective capabilities differ from the first report's in %v", outputBetween, diff)
		}
	}
}

// What the author reports is held to what Opened carried, which is already the mode: a report that narrows is taken, and
// a field above the opened value is replaced by it and named. The host receives the clamped set.
func TestAReportIsHeldToWhatOpenedCarried(t *testing.T) {
	gate := newStartGate(Request{}, sgOpened())
	var sent sgSent
	report := ccPod() // the kind's set: HostRestart TARGET_PROCESS is above what an exec session was opened with
	report.ExitReport = ExitReportNone
	violations := gate.report(SessionFacts{Capabilities: &report}, sent.send)
	if !slices.Equal(violations, []string{"HostRestart: reported TARGET_PROCESS above the declared NONE"}) {
		t.Errorf("violations = %q, want the one field that is above the opened set", violations)
	}
	want := sgOpened()
	want.ExitReport = ExitReportNone
	if len(sent.facts) != 1 || sent.facts[0].Capabilities == nil || len(capabilitiesDiff(*sent.facts[0].Capabilities, want)) > 0 {
		t.Errorf("the host received %+v, want the narrowed ExitReport and the opened HostRestart", sent.facts)
	}
	if diff := capabilitiesDiff(gate.effective(), want); len(diff) > 0 {
		t.Errorf("the effective capabilities differ from the clamped report in %v", diff)
	}
}

// Nothing is shared: what the host receives, what the gate returns and what the author handed in or opened with are four
// separate values, so a write to one is no write to another.
func TestTheGateSharesNothingWithItsCallers(t *testing.T) {
	opened := sgOpened()
	gate := newStartGate(Request{Argv: []string{"/bin/sh"}}, opened)
	opened.Signals[0] = SignalHup // the opened set the caller kept

	granted := true
	report := sgOpened()
	facts := SessionFacts{TTY: &granted, Capabilities: &report, Program: Program{Argv: []string{"/bin/bash"}}}
	var sent sgSent
	gate.report(facts, sent.send)
	granted, report.Signals[0], facts.Program.Argv[0] = false, SignalHup, "changed" // the author goes on

	got := sent.facts[0]
	if got.TTY == nil || !*got.TTY || got.Capabilities.Signals[0] != SignalInt || got.Program.Argv[0] != "/bin/bash" {
		t.Errorf("a write by the author reached what the host received: %+v", got)
	}
	got.Capabilities.Signals[0] = SignalHup // the receiver writes
	effective := gate.effective()
	if effective.Signals[0] != SignalInt {
		t.Errorf("a write to what the host received, or to the opened set the caller kept, reached the gate: %+v", effective)
	}
	effective.Signals[0] = SignalHup
	if again := gate.effective(); again.Signals[0] != SignalInt {
		t.Errorf("a write to what effective returned reached the gate: %+v", again)
	}

	other := newStartGate(Request{Argv: []string{"/bin/sh"}}, sgOpened())
	var madeUp sgSent
	other.output(madeUp.send)
	madeUp.facts[0].Capabilities.Signals[0] = SignalHup
	madeUp.facts[0].Program.Argv[0] = "changed"
	if again := other.effective(); again.Signals[0] != SignalInt {
		t.Errorf("a write to the Started the SDK made reached the gate: %+v", again)
	}
}

// However the author's report and the first output race (the author reports from its goroutine while the target's
// library writes from its own), the host never receives an output with no Started before it, and never two: the
// first output waits for a report that is being taken, and a report that lost the race is refused.
func TestStartedIsSentBeforeTheFirstOutputWhoeverWinsTheRace(t *testing.T) {
	const rounds = 300
	for round := range rounds {
		gate := newStartGate(Request{Argv: []string{"/bin/sh"}}, sgOpened())
		var (
			mu     sync.Mutex
			events []string
		)
		note := func(event string) { mu.Lock(); events = append(events, event); mu.Unlock() }
		start := make(chan struct{})
		var violations []string
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { // the author, reporting
			defer wg.Done()
			<-start
			violations = gate.report(SessionFacts{PID: 5}, func(SessionFacts) { note("started") })
		}()
		go func() { // the target's library, writing
			defer wg.Done()
			<-start
			gate.output(func(SessionFacts) { note("started") })
			note("output")
		}()
		close(start)
		wg.Wait()

		if started := slices.Index(events, "started"); started < 0 || slices.Index(events[started+1:], "started") >= 0 {
			t.Fatalf("round %d: events %q, want exactly one started", round, events)
		}
		if slices.Index(events, "started") > slices.Index(events, "output") {
			t.Fatalf("round %d: events %q: the output was delivered before its Started", round, events)
		}
		// The author's report is either the one that was sent or a refused one that says it came late.
		switch {
		case len(violations) == 0:
		case len(violations) == 1 && strings.Contains(violations[0], "after the first output"):
		default:
			t.Fatalf("round %d: violations = %q", round, violations)
		}
	}
}

// A zero Request and a zero opened set are a gate too: nothing in it needs the caller to have filled anything in.
func TestAGateOfNothingSendsAnEmptyStarted(t *testing.T) {
	gate := newStartGate(Request{}, Capabilities{})
	var sent sgSent
	gate.output(sent.send)
	if len(sent.facts) != 1 || sent.facts[0].Capabilities == nil || !reflect.DeepEqual(sent.facts[0].Program, Program{}) {
		t.Errorf("the host received %+v", sent.facts)
	}
}
