package exectest

import (
	"context"
	"reflect"
	"slices"
	"time"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// ---------------------------------------------------------------------------------------------------------------
// case 23: link
// ---------------------------------------------------------------------------------------------------------------

// linkState is a state of the link, as the function reports it and as the host reads it.
type linkState struct {
	state exec.LinkState
	wire  execv2.LinkState
}

var (
	linkGood     = linkState{exec.LinkGood, execv2.LinkState_LINK_STATE_GOOD}
	linkDegraded = linkState{exec.LinkDegraded, execv2.LinkState_LINK_STATE_DEGRADED}
	linkStalled  = linkState{exec.LinkStalled, execv2.LinkState_LINK_STATE_STALLED}
)

// linkStep is one report of the function: the state, whether it is a change of the last one reported (the host is told of a change and of
// nothing else), and whether the case reads its frame before the next report. Only the last report of a function that returns at once is
// not read before the end: the frame must still arrive, before Ended.
type linkStep struct {
	to     linkState
	change bool
	await  bool
}

// linkCommand is what the case tells the function: a state to report, or the exit to return.
type linkCommand struct {
	state *linkState
	exit  exec.Exit
}

// linkDriver is a function that reports the states the case gives it one at a time, and says when it has, so that the case controls what
// the SDK sees and when: two reports that follow each other at once could be one change.
type linkDriver struct {
	commands chan linkCommand
	applied  chan struct{}
}

// linkFunction registers the function of a driver under a name.
func (r *wireRig) linkFunction(name string) *linkDriver {
	d := &linkDriver{commands: make(chan linkCommand), applied: make(chan struct{})}
	r.scriptFunc(name, func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		for {
			select {
			case c := <-d.commands:
				if c.state == nil {
					return c.exit, nil
				}
				p.Link(c.state.state)
				select {
				case d.applied <- struct{}{}:
				case <-ctx.Done():
					return exec.Exit{}, ctx.Err()
				}
			case <-ctx.Done():
				return exec.Exit{}, ctx.Err()
			}
		}
	})
	return d
}

// tell gives the function a command, within the bound of a case.
func (d *linkDriver) tell(r *wireRig, c linkCommand) {
	r.t.Helper()
	select {
	case d.commands <- c:
	case <-time.After(caseBound):
		r.t.Fatalf("the function took no command within %v", caseBound)
	}
}

// report has the function report a state, and returns once it has.
func (d *linkDriver) report(r *wireRig, to linkState) {
	r.t.Helper()
	d.tell(r, linkCommand{state: &to})
	select {
	case <-d.applied:
	case <-time.After(caseBound):
		r.t.Fatalf("the function did not report the state %v within %v", to.wire, caseBound)
	}
}

// nextLink reads the stream until the next Link frame.
func (s *wireSession) nextLink(what string) *execv2.Link {
	t := s.r.t
	t.Helper()
	for {
		frame := s.next(what)
		if link := frame.GetLink(); link != nil {
			return link
		}
		if frame.GetEnded() != nil || frame.GetOutput() != nil {
			t.Fatalf("session %q: %s came while the host waited for %s: the session wrote nothing and reported a change that was not told", s.id, frameName(frame), what)
		}
	}
}

// playLink runs a function that reports the steps and then returns exit, and holds what the host reads to what the declared kind promises: a
// Link frame for each change of the state, in order, with silent_ms 0, and none for a report that changes nothing; none at all for a kind
// that declared no LinkHealth, whose reports are ignored; and the end the exit becomes, which follows the last state the function reported
// (a status of the process that came while the link was STALLED is the transport's word).
func (r *wireRig) playLink(name string, steps []linkStep, exit exec.Exit, end *execv2.End, started bool) {
	r.t.Helper()
	declared := r.info.Capabilities.LinkHealth
	script := r.newID("link")
	d := r.linkFunction(script)
	s := r.mustOpen(r.newID("link"), script)
	var want []execv2.LinkState
	var got []*execv2.Link
	for _, step := range steps {
		d.report(r, step.to)
		if !declared || !step.change {
			continue
		}
		want = append(want, step.to.wire)
		if step.await {
			got = append(got, s.nextLink("the Link frame for "+step.to.wire.String()))
		}
	}
	d.tell(r, linkCommand{exit: exit})
	frames, ended := s.readToEnd()
	for _, frame := range frames {
		if link := frame.GetLink(); link != nil {
			got = append(got, link)
		}
	}
	var states []execv2.LinkState
	for _, link := range got {
		states = append(states, link.GetState())
		if link.GetSilentMs() != 0 {
			r.t.Errorf("%s: a Link frame says silent_ms %d: the function reports a state and not how long it has lasted", name, link.GetSilentMs())
		}
	}
	if !slices.Equal(states, want) {
		r.t.Errorf("%s: the host was told the link states %v, want %v: a Link frame for each change, in order, and none for a report that changes nothing (a kind that declares LinkHealth: %v)",
			name, states, want, declared)
	}
	s.checkEnded(name, ended, wantEnded{end: end, started: started})
}

// caseWireLink: the function reports the state of its link and the host is told of each change, and of nothing else: a report of the state
// the link already has sends no frame, and a state that does not change back and forth is not repeated; the frames come in the order of the
// changes, and the last one comes before the Ended of a function that returns at once. A kind that declared no LinkHealth reports nothing:
// its Link calls are ignored and no Link frame is ever sent. A stalled link that ends the session ends it Lost, for LINK_DEAD, with the process
// that may still run; and a status of the process that the function returns while the link is STALLED is no longer the process's: it is
// the transport's word (TRANSPORT, the survival not known, the completeness UNSPECIFIED), until the link has recovered.
func caseWireLink(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	info := target.Info
	if info.Session.IsZero() {
		t.Skipf(noSessions)
	}
	caps := info.Capabilities
	rig := newWireRig(t, env, target)
	exit0, end0 := processExit(caps, 0)
	stalledStatus := end0
	if caps.LinkHealth {
		stalledStatus = endExited(0, execv2.ExitSource_EXIT_SOURCE_TRANSPORT, nil)
	}
	lostLink := endLost(execv2.LostCause_LOST_CAUSE_LINK_DEAD)
	rig.playLink("states that change and states that do not", []linkStep{
		{linkGood, true, true}, {linkGood, false, false}, {linkDegraded, true, true}, {linkDegraded, false, false},
		{linkStalled, true, true}, {linkGood, true, true}, {linkGood, false, false},
	}, exit0, end0, true)
	rig.playLink("a stalled link that gives up", []linkStep{{linkGood, true, true}, {linkStalled, true, true}}, exec.ExitLost(exec.LostLinkDead), lostLink, false)
	rig.playLink("a status of the process while the link is stalled", []linkStep{{linkStalled, true, true}}, exit0, stalledStatus, true)
	rig.playLink("a status of the process once the link has recovered", []linkStep{{linkStalled, true, true}, {linkGood, true, true}}, exit0, end0, true)
	rig.playLink("a state reported just before the function returns", []linkStep{{linkGood, true, true}, {linkStalled, true, false}}, exec.ExitLost(exec.LostLinkDead), lostLink, false)
	rig.playLink("a link that is never reported", nil, exit0, end0, true)
}

// ---------------------------------------------------------------------------------------------------------------
// case 26: capabilities narrow per session
// ---------------------------------------------------------------------------------------------------------------

// The orders of the enum fields of Capabilities, each from the lowest value to the highest, as the proto writes them at the field: a session
// may only narrow its kind, and what it reports above the kind's value is replaced by it. They are what the oracle of the case is made of,
// and are not read from the SDK.
var (
	movesKill       = []exec.KillBehavior{exec.KillClosesStreamOnly, exec.KillBestEffort, exec.KillTerminates}
	movesFactMode   = []exec.FactMode{exec.FactModeNone, exec.FactModePull, exec.FactModePush}
	movesSurvival   = []exec.Survival{exec.SurvivalNone, exec.SurvivalTargetProcess, exec.SurvivalTargetSession}
	movesExitReport = []exec.ExitReport{exec.ExitReportNone, exec.ExitReportCode, exec.ExitReportCodeAndSignal}
	movesSignals    = []exec.Signal{exec.SignalInt, exec.SignalQuit, exec.SignalTerm, exec.SignalKill, exec.SignalHup, exec.SignalUsr1, exec.SignalUsr2}
)

// stepDown is the value one below v in the order, and whether there is one. A value that is not in the order (unspecified) has none below it.
func stepDown[T comparable](order []T, v T) (T, bool) {
	if i := slices.Index(order, v); i > 0 {
		return order[i-1], true
	}
	return v, false
}

// stepUp is the value one above v in the order, and whether there is one. A value that is not in the order (unspecified) is below every value.
func stepUp[T comparable](order []T, v T) (T, bool) {
	switch i := slices.Index(order, v); {
	case i < 0:
		return order[0], true
	case i < len(order)-1:
		return order[i+1], true
	}
	return v, false
}

// capabilityMoves returns the capabilities of the kind with every field that has room moved one step down (a narrower set, which a choice
// or a Started may declare) and one step up (a wider one, which the SDK must refuse to serve), and whether any field moved. The fields that shape a
// request (the terminal, the separate standard error, the initial size) and the ones that change what a session does with its output (the stall
// and the integration) are left as they are, so that the sessions of the case behave alike.
func capabilityMoves(c exec.Capabilities) (lower, raise exec.Capabilities, lowered, raised bool) {
	lower, raise = cloneCapabilities(c), cloneCapabilities(c)
	flag := func(declared bool, lowerTo, raiseTo *bool) {
		if declared {
			*lowerTo, lowered = false, true
		} else {
			*raiseTo, raised = true, true
		}
	}
	flag(c.Resize, &lower.Resize, &raise.Resize)
	flag(c.StdinEOF, &lower.StdinEOF, &raise.StdinEOF)
	flag(c.Env, &lower.Env, &raise.Env)
	flag(c.Cwd, &lower.Cwd, &raise.Cwd)
	flag(c.LinkHealth, &lower.LinkHealth, &raise.LinkHealth)
	if v, ok := stepDown(movesKill, c.Kill); ok {
		lower.Kill, lowered = v, true
	}
	if v, ok := stepUp(movesKill, c.Kill); ok {
		raise.Kill, raised = v, true
	}
	if v, ok := stepDown(movesFactMode, c.CwdFact); ok {
		lower.CwdFact, lowered = v, true
	}
	if v, ok := stepUp(movesFactMode, c.CwdFact); ok {
		raise.CwdFact, raised = v, true
	}
	if v, ok := stepDown(movesFactMode, c.ForegroundFact); ok {
		lower.ForegroundFact, lowered = v, true
	}
	if v, ok := stepUp(movesFactMode, c.ForegroundFact); ok {
		raise.ForegroundFact, raised = v, true
	}
	if v, ok := stepDown(movesSurvival, c.HostRestart); ok {
		lower.HostRestart, lowered = v, true
	}
	if v, ok := stepUp(movesSurvival, c.HostRestart); ok {
		raise.HostRestart, raised = v, true
	}
	if v, ok := stepDown(movesExitReport, c.ExitReport); ok {
		lower.ExitReport, lowered = v, true
	}
	if v, ok := stepUp(movesExitReport, c.ExitReport); ok {
		raise.ExitReport, raised = v, true
	}
	if n := len(c.Signals); n > 0 {
		lower.Signals, lowered = slices.Clone(c.Signals[:n-1]), true
	}
	if i := slices.IndexFunc(movesSignals, func(s exec.Signal) bool { return !slices.Contains(c.Signals, s) }); i >= 0 {
		raise.Signals, raised = append(slices.Clone(c.Signals), movesSignals[i]), true
	}
	return lower, raise, lowered, raised
}

// narrowingOption is the option whose choices are the modes of the case.
const narrowingOption = "mode"

// narrowingOptions is the Options callback of the fake of case 26: a mode that names no capabilities, a mode that narrows the kind (what an
// attach does to an exec) and a mode that claims more than the kind has.
func narrowingOptions(lower, raise exec.Capabilities) exec.OptionsFunc {
	return func(*types.PluginContext, resourcev2.ResourceReference) ([]exec.Option, error) {
		attach, wide := cloneCapabilities(lower), cloneCapabilities(raise)
		return []exec.Option{{Name: narrowingOption, Choices: []exec.Choice{
			{Value: "exec", State: exec.ChoiceStateReady, IsDefault: true},
			{Value: "attach", State: exec.ChoiceStateReady, Capabilities: &attach},
			{Value: "wide", State: exec.ChoiceStateReady, Capabilities: &wide},
		}}}, nil
	}
}

// selecting is the change of a start that selects a mode.
func (r *wireRig) selecting(mode string) func(*execv2.Start) {
	return func(st *execv2.Start) {
		st.Target = r.conns.WireTarget(r.t, r.info.Type, r.resource, &execv2.NameValue{Name: narrowingOption, Value: mode})
	}
}

// narrowCase is a session of case 26: the mode it selects, what its function reports with Proc.Started, and what Opened and the session must
// say.
type narrowCase struct {
	name string
	// mode is the choice the selection names; empty omits the option, which takes its default choice.
	mode string
	// report is what the function reports with Proc.Started; nil reports nothing, and the SDK makes the Started when the function writes.
	report *exec.Capabilities
	// opened is what Opened must carry, and effective what the session is for the rest of its life, which Started carries.
	opened, effective exec.Capabilities
}

// caseWireNarrowing: the capabilities of a session are the kind's, narrowed by the mode that its selection names (Choice.Capabilities: an exec
// and an attach on one Pod kind get different ExitReport and HostRestart in Opened), and by what the function learns at run time and reports in
// Started, which narrows only what the run showed. Both only narrow: a choice that claims more than the kind has, and a Started that claims
// more than the session was opened with, are served the value they were bounded by. Opened carries the session's set before the function runs;
// Started carries the effective one; Describe keeps the kind's, whatever the sessions do; the Options a host reads show each choice as the
// session will be served, a widening clamped; and what the session ends with follows its own set: a Closed end says the process is gone only when
// the session's Kill is TERMINATES, and the exit of a session whose ExitReport was narrowed is the source its own set allows.
func caseWireNarrowing(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	info := target.Info
	if info.Session.IsZero() {
		t.Skipf(noSessions)
	}
	caps := info.Capabilities
	lower, raise, lowered, raised := capabilityMoves(caps)
	if !lowered {
		t.Logf("the target declares the least in every field that a mode may narrow: there is nothing to narrow, and the narrowing is not checked")
	}
	if !raised {
		t.Logf("the target declares the most in every field that a mode may claim: there is nothing to widen, and the clamping is not checked")
	}
	rig := newOptionsRig(t, env, target, narrowingOptions(lower, raise))

	cases := []narrowCase{
		{name: "no selection: the option's default choice, which narrows nothing", opened: caps, effective: caps},
		{name: "a choice that names no capabilities", mode: "exec", opened: caps, effective: caps},
	}
	if lowered {
		cases = append(cases,
			narrowCase{name: "the mode that narrows the kind", mode: "attach", opened: lower, effective: lower},
			narrowCase{name: "a Started that narrows the kind further", mode: "exec", report: &lower, opened: caps, effective: lower})
	}
	if raised {
		cases = append(cases,
			narrowCase{name: "a mode that claims more than the kind: it is served the kind's", mode: "wide", opened: caps, effective: caps},
			narrowCase{name: "a Started that claims more than the kind: it is served the kind's", mode: "exec", report: &raise, opened: caps, effective: caps})
	}
	if lowered && raised {
		cases = append(cases, narrowCase{name: "a Started that claims more than its mode: it is served the mode's", mode: "attach", report: &raise, opened: lower, effective: lower})
	}
	for _, nc := range cases {
		var mods []func(*execv2.Start)
		if nc.mode != "" {
			mods = append(mods, rig.selecting(nc.mode))
		}
		var steps []Step
		if nc.report != nil {
			reported := cloneCapabilities(*nc.report)
			steps = append(steps, ReportStarted(exec.SessionFacts{Capabilities: &reported}))
		}
		exit0, endExit := processExit(nc.effective, 0)
		for _, ending := range []struct {
			name  string
			steps []Step
			drive func(*wireRig, *wireSession)
			end   *execv2.End
		}{
			{"closed by the host", []Step{WriteOut("up"), Block()}, afterUp(closeWith(execv2.CloseMode_CLOSE_MODE_GRACEFUL)), endClosed(execv2.ClosedBy_CLOSED_BY_REQUEST, nc.effective.Kill)},
			{"ending with an exit", []Step{WriteOut("up"), EndWith(exit0)}, nil, endExit},
		} {
			name := nc.name + ", " + ending.name
			s, _ := rig.playSession(endScenario{
				name: name, steps: append(slices.Clone(steps), ending.steps...), drive: ending.drive, end: ending.end, started: true, output: "up", reports: nc.report != nil, mods: mods,
			})
			for _, diff := range capabilityDiffs(s.conn.Opened().GetFacts().GetCapabilities(), nc.opened) {
				t.Errorf("%s: Opened: %s", name, diff)
			}
			if s.started == nil {
				continue
			}
			for _, diff := range capabilityDiffs(s.started.GetFacts().GetCapabilities(), nc.effective) {
				t.Errorf("%s: Started: %s", name, diff)
			}
		}
	}

	// Describe says what the kind is, whatever its sessions are.
	for _, diff := range capabilityDiffs(rig.described().GetCapabilities(), caps) {
		t.Errorf("after sessions of every mode, Describe: %s", diff)
	}
	// The Options a host reads show each mode as a session of it is served.
	result, err := rig.client.Options(rig.request(), rig.conns.TargetRef(rig.info.Type, rig.resource))
	if err != nil {
		t.Fatalf("Options: %v", err)
	}
	if len(result.Options) != 1 || result.Options[0].Name != narrowingOption || len(result.Options[0].Choices) != 3 {
		t.Fatalf("Options lists %+v, want the one option %q with three choices", result.Options, narrowingOption)
	}
	choices := result.Options[0].Choices
	if choices[0].Capabilities != nil {
		t.Errorf("the choice that names no capabilities is served with %+v: it narrows nothing", *choices[0].Capabilities)
	}
	for i, want := range map[int]exec.Capabilities{1: lower, 2: caps} {
		got := choices[i].Capabilities
		if got == nil {
			t.Errorf("the choice %q is served with no capabilities", choices[i].Value)
		} else if !sameCapabilities(*got, want) {
			t.Errorf("the choice %q is served with %+v, want %+v: a choice is served as the session of it is: narrowed, and never wider than the kind", choices[i].Value, *got, want)
		}
	}
}

// sameCapabilities says that two capabilities are the same set, a nil list being an empty one.
func sameCapabilities(a, b exec.Capabilities) bool {
	normalize := func(c exec.Capabilities) exec.Capabilities {
		if len(c.Signals) == 0 {
			c.Signals = nil
		}
		if len(c.Integration) == 0 {
			c.Integration = nil
		}
		return c
	}
	return reflect.DeepEqual(normalize(a), normalize(b))
}
