package exec

import (
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
)

// ccPod is the Pod kind of the guide: the most any session of it can do. Exec and attach each narrow it in a field of
// their own.
func ccPod() Capabilities {
	return Capabilities{
		TTY: true, SeparateStderr: true, Resize: true, InitialSize: true, StdinEOF: true,
		Signals:     []Signal{SignalInt, SignalTerm},
		Kill:        KillClosesStreamOnly,
		ExitReport:  ExitReportCode,
		Stall:       StallLossy,
		HostRestart: SurvivalTargetProcess,
		Integration: []IntegrationMode{IntegrationArgvWrapper},
	}
}

// ccNarrowed is a copy of kind that edit narrows: the complete set a choice declares.
func ccNarrowed(kind Capabilities, edit func(*Capabilities)) *Capabilities {
	caps := cloneCapabilities(kind)
	edit(&caps)
	return &caps
}

// ccPodOptions are the options of a Pod: its containers, which do not narrow, and its mode, which does: an exec has an
// exit code and cannot be reached after a host restart, an attach has no status and can.
func ccPodOptions() []Option {
	kind := ccPod()
	return []Option{
		{Name: "container", Required: true, Choices: []Choice{{Value: "app", IsDefault: true}, {Value: "sidecar"}}},
		{Name: "mode", Choices: []Choice{
			{Value: "exec", IsDefault: true, Capabilities: ccNarrowed(kind, func(c *Capabilities) { c.HostRestart = SurvivalNone })},
			{Value: "attach", Capabilities: ccNarrowed(kind, func(c *Capabilities) { c.ExitReport = ExitReportNone })},
		}},
	}
}

// What a session is opened with is the kind's declared set narrowed by the choice that each option takes in the
// selection: the one the selection names, and the default choice of an option it omits. A value that names no choice
// narrows nothing, and does not fall back to the default.
func TestOpenedCapabilitiesAreTheKindNarrowedByTheChoicesTheSelectionNames(t *testing.T) {
	exec := func(c *Capabilities) { c.HostRestart = SurvivalNone }
	attach := func(c *Capabilities) { c.ExitReport = ExitReportNone }
	nothing := func(*Capabilities) {}
	for _, tc := range []struct {
		name      string
		selection map[string]string
		narrow    func(*Capabilities)
	}{
		{"the mode exec", map[string]string{"mode": "exec"}, exec},
		{"the mode attach", map[string]string{"mode": "attach"}, attach},
		{"a mode the selection omits takes its default choice", map[string]string{"container": "sidecar"}, exec},
		{"an empty selection takes every default", map[string]string{}, exec},
		{"no selection takes every default", nil, exec},
		{"a value that names no choice narrows nothing, and is not the default", map[string]string{"mode": "nope"}, nothing},
		{"an option the target does not have is ignored", map[string]string{"nope": "x", "mode": "attach"}, attach},
		{"a container that does not narrow leaves the mode's narrowing", map[string]string{"container": "app", "mode": "attach"}, attach},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := ccPod()
			tc.narrow(&want)
			got := openedCapabilities(ccPod(), ccPodOptions(), tc.selection)
			if diff := capabilitiesDiff(got, want); len(diff) > 0 {
				t.Errorf("opened with %+v, want %+v (differs in %v)", got, want, diff)
			}
		})
	}
}

// An option the selection omits and that has no default choice narrows nothing, and so does a choice with no
// capabilities.
func TestAnOptionWithNoDefaultAndAChoiceWithNoCapabilitiesNarrowNothing(t *testing.T) {
	kind := ccPod()
	options := []Option{{Name: "mode", Choices: []Choice{
		{Value: "exec", Capabilities: ccNarrowed(kind, func(c *Capabilities) { c.HostRestart = SurvivalNone })},
		{Value: "plain"},
	}}}
	for _, selection := range []map[string]string{nil, {"mode": "plain"}} {
		if diff := capabilitiesDiff(openedCapabilities(kind, options, selection), kind); len(diff) > 0 {
			t.Errorf("selection %v narrowed %v, want nothing", selection, diff)
		}
	}
	if got := openedCapabilities(kind, nil, map[string]string{"mode": "exec"}); len(capabilitiesDiff(got, kind)) > 0 {
		t.Errorf("a target with no options narrowed %+v", got)
	}
}

// Several options may narrow, each a field of its own or the same one. The result is the narrowest of them and does not
// depend on the order of the options, nor on the order in which a choice lists its signals: a set is written in the
// order the kind declared it.
func TestOpenedCapabilitiesDoNotDependOnTheOrderOfTheOptions(t *testing.T) {
	kind := Capabilities{
		TTY: true, Signals: []Signal{SignalInt, SignalQuit, SignalTerm, SignalKill},
		Kill: KillTerminates, ExitReport: ExitReportCodeAndSignal, Stall: StallPropagates,
		HostRestart: SurvivalTargetSession, Integration: []IntegrationMode{IntegrationArgvWrapper},
	}
	options := []Option{
		{Name: "a", Choices: []Choice{{Value: "x", Capabilities: ccNarrowed(kind, func(c *Capabilities) {
			c.Signals = []Signal{SignalKill, SignalInt, SignalTerm}
			c.ExitReport = ExitReportCode
			c.HostRestart = SurvivalTargetProcess
		})}}},
		{Name: "b", Choices: []Choice{{Value: "y", Capabilities: ccNarrowed(kind, func(c *Capabilities) {
			c.Signals = []Signal{SignalTerm, SignalInt}
			c.ExitReport = ExitReportNone
			c.Integration = nil
		})}}},
		{Name: "c", Choices: []Choice{{Value: "z", IsDefault: true, Capabilities: ccNarrowed(kind, func(c *Capabilities) {
			c.TTY = false
			c.HostRestart = SurvivalNone
		})}}},
	}
	selection := map[string]string{"a": "x", "b": "y"}
	want := Capabilities{
		TTY: false, Signals: []Signal{SignalInt, SignalTerm},
		Kill: KillTerminates, ExitReport: ExitReportNone, Stall: StallPropagates,
		HostRestart: SurvivalNone,
	}
	for _, order := range [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		shuffled := make([]Option, len(order))
		for i, j := range order {
			shuffled[i] = options[j]
		}
		got := openedCapabilities(kind, shuffled, selection)
		if len(capabilitiesDiff(got, want)) > 0 {
			t.Errorf("options in order %v: opened with %+v, want %+v", order, got, want)
		}
		if len(got.Signals) != 2 || got.Signals[0] != SignalInt || got.Signals[1] != SignalTerm {
			t.Errorf("options in order %v: signals %v, want INT then TERM, the kind's order", order, got.Signals)
		}
	}
}

// A choice cannot widen what the kind declared: what it claims above the kind is the kind's. The author is told when the
// SDK serves the options (see clampChoiceCapabilities); here it is clamped.
func TestAChoiceCannotOpenASessionWiderThanItsKind(t *testing.T) {
	kind := ccPod()
	wide := widestCapabilities()
	options := []Option{{Name: "mode", Choices: []Choice{{Value: "wide", Capabilities: &wide}}}}
	got := openedCapabilities(kind, options, map[string]string{"mode": "wide"})
	if diff := capabilitiesDiff(got, kind); len(diff) > 0 {
		t.Errorf("a choice that claims the widest set opened a session that differs from its kind in %v: %+v", diff, got)
	}
}

// The call changes neither the kind nor the options, and the result shares no storage with them.
func TestOpenedCapabilitiesShareNothingWithTheirArguments(t *testing.T) {
	kind, options := ccPod(), ccPodOptions()
	kindBefore, optionsBefore := ccPod(), ccPodOptions()
	got := openedCapabilities(kind, options, map[string]string{"mode": "attach"})
	got.Signals[0], got.Integration[0] = SignalHup, IntegrationOff
	if !reflect.DeepEqual(kind, kindBefore) {
		t.Errorf("the call or a write to its result changed the kind: %+v", kind)
	}
	if !reflect.DeepEqual(options, optionsBefore) {
		t.Errorf("the call or a write to its result changed the options: %+v", options)
	}
}

// ccRandom builds a random complete set: any value of every field, the three enums that can be left unsaid included.
func ccRandom(r *rand.Rand) Capabilities {
	pick := func(n int) int { return r.IntN(n) }
	flag := func() bool { return pick(2) == 1 }
	signals := []Signal{SignalInt, SignalQuit, SignalTerm, SignalKill, SignalHup, SignalUsr1, SignalUsr2}
	r.Shuffle(len(signals), func(i, j int) { signals[i], signals[j] = signals[j], signals[i] })
	var integration []IntegrationMode
	if flag() {
		integration = []IntegrationMode{IntegrationArgvWrapper}
	}
	return Capabilities{
		TTY: flag(), SeparateStderr: flag(), Resize: flag(), InitialSize: flag(), StdinEOF: flag(), Env: flag(), Cwd: flag(), LinkHealth: flag(),
		Signals:        slices.Clone(signals[:pick(len(signals)+1)]),
		Kill:           []KillBehavior{KillUnspecified, KillClosesStreamOnly, KillBestEffort, KillTerminates}[pick(4)],
		CwdFact:        factModeOrder[pick(3)],
		ForegroundFact: factModeOrder[pick(3)],
		HostRestart:    survivalOrder[pick(3)],
		ExitReport:     []ExitReport{ExitReportUnspecified, ExitReportNone, ExitReportCode, ExitReportCodeAndSignal}[pick(4)],
		Stall:          []StallBehavior{StallUnspecified, StallLossy, StallPropagates}[pick(3)],
		Integration:    integration,
	}
}

// ccLowest is the narrowest of a declared enum value and the values that choices state. A value outside the order is the
// enum left unsaid, which states nothing; a declared value outside the order is below every value.
func ccLowest[T ~int](order []T, declared T, stated []T) T {
	lowest := declared
	for _, v := range stated {
		if rank := slices.Index(order, v); rank >= 0 && rank < slices.Index(order, lowest) {
			lowest = v
		}
	}
	return lowest
}

// ccShared is the members of declared, in declared order and once each, that every one of sets holds.
func ccShared[T comparable](declared []T, sets [][]T) []T {
	var shared []T
	for _, v := range declared {
		if slices.Contains(shared, v) {
			continue
		}
		if !slices.ContainsFunc(sets, func(s []T) bool { return !slices.Contains(s, v) }) {
			shared = append(shared, v)
		}
	}
	return shared
}

// ccExpected is the narrowest set that is at most the kind's and at most every set a chosen choice declares, written
// field by field and without the rule under test: a flag is the conjunction, an enum the lowest of its order, a set the
// members they all hold in the kind's order.
func ccExpected(kind Capabilities, chosen []Capabilities) Capabilities {
	all := append([]Capabilities{kind}, chosen...)
	flag := func(get func(Capabilities) bool) bool {
		return !slices.ContainsFunc(all, func(c Capabilities) bool { return !get(c) })
	}
	out := Capabilities{
		TTY:            flag(func(c Capabilities) bool { return c.TTY }),
		SeparateStderr: flag(func(c Capabilities) bool { return c.SeparateStderr }),
		Resize:         flag(func(c Capabilities) bool { return c.Resize }),
		InitialSize:    flag(func(c Capabilities) bool { return c.InitialSize }),
		StdinEOF:       flag(func(c Capabilities) bool { return c.StdinEOF }),
		Env:            flag(func(c Capabilities) bool { return c.Env }),
		Cwd:            flag(func(c Capabilities) bool { return c.Cwd }),
		LinkHealth:     flag(func(c Capabilities) bool { return c.LinkHealth }),
	}
	signals := make([][]Signal, len(chosen))
	integration := make([][]IntegrationMode, len(chosen))
	var (
		kills        []KillBehavior
		facts, focus []FactMode
		survivals    []Survival
		reports      []ExitReport
		stalls       []StallBehavior
	)
	for i, c := range chosen {
		signals[i], integration[i] = c.Signals, c.Integration
		kills, facts, focus = append(kills, c.Kill), append(facts, c.CwdFact), append(focus, c.ForegroundFact)
		survivals, reports, stalls = append(survivals, c.HostRestart), append(reports, c.ExitReport), append(stalls, c.Stall)
	}
	out.Signals = ccShared(kind.Signals, signals)
	out.Integration = ccShared(kind.Integration, integration)
	out.Kill = ccLowest(killOrder, kind.Kill, kills)
	out.CwdFact = ccLowest(factModeOrder, kind.CwdFact, facts)
	out.ForegroundFact = ccLowest(factModeOrder, kind.ForegroundFact, focus)
	out.HostRestart = ccLowest(survivalOrder, kind.HostRestart, survivals)
	out.ExitReport = ccLowest(exitReportOrder, kind.ExitReport, reports)
	out.Stall = ccLowest(stallOrder, kind.Stall, stalls)
	return out
}

// ccRandomOptions builds options of random choices (a default in some, capabilities in most) and a selection that, for
// each option, omits it, names one of its choices or names a value it does not have. It returns the capabilities of the
// choices the selection takes, found by the rule written out here: the named choice, else the default of an omitted option.
func ccRandomOptions(r *rand.Rand) (options []Option, selection map[string]string, taken []Capabilities) {
	selection = map[string]string{}
	for o := range 1 + r.IntN(4) {
		option := Option{Name: string(rune('a' + o))}
		defaultAt := -1
		if r.IntN(2) == 0 {
			defaultAt = r.IntN(4)
		}
		for c := range 1 + r.IntN(4) {
			choice := Choice{Value: string(rune('p' + c)), IsDefault: c == defaultAt}
			if r.IntN(3) > 0 {
				choice.Capabilities = new(ccRandom(r))
			}
			option.Choices = append(option.Choices, choice)
		}
		options = append(options, option)

		var chosen *Choice
		switch r.IntN(3) {
		case 0: // omitted: the default, if the option has one
			for i := range option.Choices {
				if option.Choices[i].IsDefault {
					chosen = &option.Choices[i]
				}
			}
		case 1: // names one of its choices
			chosen = &option.Choices[r.IntN(len(option.Choices))]
			selection[option.Name] = chosen.Value
		default: // names a value it does not have
			selection[option.Name] = "unknown"
		}
		if chosen != nil && chosen.Capabilities != nil {
			taken = append(taken, *chosen.Capabilities)
		}
	}
	return options, selection, taken
}

// The rule, checked against what it says on random kinds, options and selections: the opened set is the narrowest set that
// no chosen choice and not the kind exceeds, and the order of the options does not change it.
func TestOpenedCapabilitiesAreTheNarrowestSetNoChosenChoiceExceeds(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11))
	for i := range 4000 {
		kind := ccRandom(r)
		options, selection, taken := ccRandomOptions(r)
		want := ccExpected(kind, taken)

		got := openedCapabilities(kind, options, selection)
		if diff := capabilitiesDiff(got, want); len(diff) > 0 {
			t.Fatalf("case %d: opened with %+v, want %+v (differs in %v)\nkind %+v\noptions %+v\nselection %v", i, got, want, diff, kind, options, selection)
		}
		if _, violations := narrowCapabilities(kind, got); len(violations) > 0 {
			t.Fatalf("case %d: the opened set is wider than its kind: %q", i, violations)
		}

		shuffled := slices.Clone(options)
		r.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		if again := openedCapabilities(kind, shuffled, selection); !reflect.DeepEqual(got, again) {
			t.Fatalf("case %d: the order of the options changed the result:\n%+v\n%+v", i, got, again)
		}
	}
}
