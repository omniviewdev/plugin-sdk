package exec

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// roundTrip sends options over the wire and back: encoded, serialized, parsed and decoded, as a host reads them.
func roundTrip(t *testing.T, options []Option) []Option {
	t.Helper()
	raw, err := proto.Marshal(&execv2.OptionsResponse{Options: optionsToProto(options)})
	if err != nil {
		t.Fatal(err)
	}
	decoded := &execv2.OptionsResponse{}
	if err := proto.Unmarshal(raw, decoded); err != nil {
		t.Fatal(err)
	}
	got, err := optionsFromProto(decoded.GetOptions())
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// A choice carries its mode over the wire in both directions: a choice with capabilities writes a complete message, one
// without writes none (absent: it does not narrow) and reads back without, and what is read is what was written.
func TestAChoiceCarriesItsCapabilitiesOverTheWireAndNilStaysAbsent(t *testing.T) {
	kind := ccPod()
	options := ccPodOptions()
	wire := optionsToProto(options)

	for _, choice := range wire[0].GetChoices() {
		if choice.GetCapabilities() != nil {
			t.Errorf("container %q is written with capabilities %v: a choice that does not narrow writes none", choice.GetValue(), choice.GetCapabilities())
		}
	}
	for _, choice := range wire[1].GetChoices() {
		caps := choice.GetCapabilities()
		if caps == nil {
			t.Fatalf("mode %q is written without capabilities", choice.GetValue())
		}
		// Every field is written: a consumer reads an absent field as unknown, never as false.
		if absent := absentCapabilityFields(caps); len(absent) > 0 {
			t.Errorf("mode %q is written without %v", choice.GetValue(), absent)
		}
	}

	got := roundTrip(t, options)
	for i := range options {
		for j := range options[i].Choices {
			want, have := options[i].Choices[j].Capabilities, got[i].Choices[j].Capabilities
			switch {
			case want == nil && have != nil:
				t.Errorf("option %q choice %q read back with capabilities %+v, written with none", options[i].Name, options[i].Choices[j].Value, have)
			case want != nil && have == nil:
				t.Errorf("option %q choice %q read back with none, written with %+v", options[i].Name, options[i].Choices[j].Value, want)
			case want != nil:
				if diff := capabilitiesDiff(*have, *want); len(diff) > 0 {
					t.Errorf("option %q choice %q differs in %v after the round trip", options[i].Name, options[i].Choices[j].Value, diff)
				}
			}
		}
	}
	if attach := got[1].Choices[1].Capabilities; attach == nil || attach.ExitReport != ExitReportNone || attach.HostRestart != kind.HostRestart {
		t.Errorf("attach reads back as %+v, want ExitReport none and the kind's HostRestart", attach)
	}
}

// A set the author built from nothing (every flag false, no signals, every enum at its zero) is a statement too: it is
// written whole and read back as it was, and so is a set that leaves Kill, ExitReport and Stall unsaid.
func TestAChoiceThatCanDoNothingAndAChoiceThatLeavesEnumsUnsaidRoundTrip(t *testing.T) {
	for name, caps := range map[string]Capabilities{
		"nothing":      {},
		"unsaid":       {TTY: true, Signals: []Signal{SignalTerm, SignalInt}, HostRestart: SurvivalTargetSession, CwdFact: FactModePush},
		"narrowest":    narrowestCapabilities(),
		"the widest":   widestCapabilities(),
		"all enum low": {Kill: KillClosesStreamOnly, ExitReport: ExitReportNone, Stall: StallLossy},
	} {
		t.Run(name, func(t *testing.T) {
			got := roundTrip(t, []Option{{Name: "mode", Choices: []Choice{{Value: "m", Capabilities: &caps}}}})
			have := got[0].Choices[0].Capabilities
			if have == nil {
				t.Fatalf("a set that was written came back absent")
			}
			if diff := capabilitiesDiff(*have, caps); len(diff) > 0 {
				t.Errorf("differs in %v: wrote %+v, read %+v", diff, caps, *have)
			}
			if !slices.Equal(have.Signals, caps.Signals) {
				t.Errorf("signals %v, wrote %v: the order of a set is kept", have.Signals, caps.Signals)
			}
		})
	}
}

// absentCapabilityFields lists the fields of a wire set that a consumer would read as unknown.
func absentCapabilityFields(c *execv2.Capabilities) []string {
	var absent []string
	for name, present := range map[string]bool{
		"tty": c.Tty != nil, "separate_stderr": c.SeparateStderr != nil, "resize": c.Resize != nil,
		"initial_size": c.InitialSize != nil, "stdin_eof": c.StdinEof != nil, "env": c.Env != nil, "cwd": c.Cwd != nil,
		"link_health": c.LinkHealth != nil, "signals": c.Signals != nil, "integration": c.Integration != nil,
	} {
		if !present {
			absent = append(absent, name)
		}
	}
	slices.Sort(absent)
	return absent
}

// The SDK always writes a complete set, so a set that leaves a flag or a list out came from another producer. A Go
// Capabilities cannot say "not known" for a flag, and reading the absent one as false would say "cannot": an answer that
// is not complete is refused, and the field is named.
func TestAnIncompleteCapabilitiesOfAChoiceIsRefusedAndNamesTheField(t *testing.T) {
	complete := func() *execv2.Capabilities { return capabilitiesToProto(ccPod()) }
	for field, strip := range map[string]func(*execv2.Capabilities){
		"tty":             func(c *execv2.Capabilities) { c.Tty = nil },
		"separate_stderr": func(c *execv2.Capabilities) { c.SeparateStderr = nil },
		"resize":          func(c *execv2.Capabilities) { c.Resize = nil },
		"initial_size":    func(c *execv2.Capabilities) { c.InitialSize = nil },
		"stdin_eof":       func(c *execv2.Capabilities) { c.StdinEof = nil },
		"env":             func(c *execv2.Capabilities) { c.Env = nil },
		"cwd":             func(c *execv2.Capabilities) { c.Cwd = nil },
		"link_health":     func(c *execv2.Capabilities) { c.LinkHealth = nil },
		"signals":         func(c *execv2.Capabilities) { c.Signals = nil },
		"integration":     func(c *execv2.Capabilities) { c.Integration = nil },
	} {
		t.Run(field, func(t *testing.T) {
			caps := complete()
			strip(caps)
			wire := []*execv2.Option{{Name: "mode", Choices: []*execv2.Choice{{Value: "m", Capabilities: caps}}}}
			_, err := optionsFromProto(wire)
			if err == nil || !strings.Contains(err.Error(), field) || !strings.Contains(err.Error(), `"mode"`) || !strings.Contains(err.Error(), `"m"`) {
				t.Errorf("a set without %s was read as %v, want a refusal that names the option, the choice and the field", field, err)
			}
		})
	}
}

// A value this build does not know (a newer peer) is not an error. A narrowing set reads it as the narrowest: an enum
// that has an unsaid value reads as unsaid, one that has none reads as none, and a signal or an integration mode this
// build cannot name is left out, so that nothing is ever offered that this build cannot name.
func TestAChoiceWithValuesThisBuildDoesNotKnowIsReadAsNarrowAsItCan(t *testing.T) {
	caps := capabilitiesToProto(ccPod())
	caps.Kill = execv2.KillBehavior(99)
	caps.ExitReport = execv2.ExitReport(99)
	caps.Stall = execv2.StallBehavior(99)
	caps.HostRestart = execv2.Survival(99)
	caps.CwdFact = execv2.FactMode(99)
	caps.Signals = &execv2.SignalSet{Signals: []execv2.Signal{execv2.Signal_SIGNAL_TERM, execv2.Signal(99), execv2.Signal_SIGNAL_TERM, execv2.Signal_SIGNAL_INT}}
	caps.Integration = &execv2.IntegrationModeSet{Modes: []execv2.IntegrationMode{execv2.IntegrationMode(99), execv2.IntegrationMode_INTEGRATION_MODE_NONE}}
	got, err := optionsFromProto([]*execv2.Option{{Name: "mode", Choices: []*execv2.Choice{{Value: "m", Capabilities: caps}}}})
	if err != nil {
		t.Fatalf("a value this build does not know was an error: %v", err)
	}
	want := ccPod()
	want.Kill, want.ExitReport, want.Stall = KillUnspecified, ExitReportUnspecified, StallUnspecified
	want.HostRestart, want.CwdFact = SurvivalNone, FactModeNone
	want.Signals, want.Integration = []Signal{SignalTerm, SignalInt}, nil
	have := got[0].Choices[0].Capabilities
	if have == nil || len(capabilitiesDiff(*have, want)) > 0 || !reflect.DeepEqual(have.Signals, want.Signals) {
		t.Errorf("read %+v, want %+v", have, want)
	}
}
