package exec_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/log/logtest"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
)

// recordDefaultLog makes the plugin's logger, which a plugin built by NewTargetPlugin takes from logging.Default at
// that moment, a recording one for the test. Call it before the fixture is built.
func recordDefaultLog(t *testing.T) *logtest.RecordingSink {
	t.Helper()
	sink := &logtest.RecordingSink{}
	previous := logging.Default()
	logging.SetDefault(logging.New(logging.Config{
		Backend: logging.NewSinkBackend(sink),
		Level:   logging.NewLevelController(logging.LevelDebug),
	}))
	t.Cleanup(func() { logging.SetDefault(previous) })
	return sink
}

// narrowedFrom is a copy of the capabilities a kind declared, that edit changes: the complete set a choice declares.
func narrowedFrom(kind exec.Capabilities, edit func(*exec.Capabilities)) *exec.Capabilities {
	caps := kind
	caps.Signals = slices.Clone(kind.Signals)
	caps.Integration = slices.Clone(kind.Integration)
	edit(&caps)
	return &caps
}

// The SDK serves the capabilities of a choice as the author declared them when they are within the kind's, and clamps a
// field that is above the kind's to the kind's value: the host never reads a mode wider than its kind. The author is told
// in the plugin's log, one record per field, that names the option, the choice and the field. What the author returned
// is not changed, so a target that returns the same options on every call is served the same way every time.
func TestAChoiceWiderThanItsKindIsServedClampedAndTheAuthorIsToldInTheLog(t *testing.T) {
	sink := recordDefaultLog(t)
	kind := exectest.NewFakeTarget().Info.Capabilities
	options := []exec.Option{{Name: "mode", Choices: []exec.Choice{
		{Value: "narrow", IsDefault: true, Capabilities: narrowedFrom(kind, func(c *exec.Capabilities) { c.ExitReport = exec.ExitReportNone })},
		{Value: "wide", Capabilities: narrowedFrom(kind, func(c *exec.Capabilities) {
			c.LinkHealth = true                                         // the kind has none
			c.HostRestart = exec.SurvivalTargetSession                  // above the kind's none
			c.Signals = append(slices.Clone(c.Signals), exec.SignalHup) // not the kind's
			c.ExitReport = exec.ExitReportCode                          // below the kind's: fine
		})},
		{Value: "plain"},
	}}}
	before := []exec.Option{{Name: "mode", Choices: []exec.Choice{
		{Value: "narrow", IsDefault: true, Capabilities: narrowedFrom(kind, func(c *exec.Capabilities) { c.ExitReport = exec.ExitReportNone })},
		{Value: "wide", Capabilities: narrowedFrom(kind, func(c *exec.Capabilities) {
			c.LinkHealth, c.HostRestart = true, exec.SurvivalTargetSession
			c.Signals = append(slices.Clone(c.Signals), exec.SignalHup)
			c.ExitReport = exec.ExitReportCode
		})},
		{Value: "plain"},
	}}}
	f := newFixture(t, exectest.FakeStaticOptions(options...))

	for call := 1; call <= 2; call++ {
		result, err := f.client.Options(f.ctx(t), f.ref("pod-1"))
		if err != nil {
			t.Fatalf("call %d: %v", call, err)
		}
		choices := result.Options[0].Choices
		if got := choices[0].Capabilities; got == nil || !reflect.DeepEqual(*got, *narrowedFrom(kind, func(c *exec.Capabilities) { c.ExitReport = exec.ExitReportNone })) {
			t.Errorf("call %d: a choice within its kind was served as %+v", call, got)
		}
		wantWide := narrowedFrom(kind, func(c *exec.Capabilities) { c.ExitReport = exec.ExitReportCode })
		if got := choices[1].Capabilities; got == nil || !reflect.DeepEqual(*got, *wantWide) {
			t.Errorf("call %d: a choice above its kind was served as %+v, want the kind's value for each field it widened: %+v", call, got, *wantWide)
		}
		if choices[2].Capabilities != nil {
			t.Errorf("call %d: a choice with no capabilities was served with %+v", call, choices[2].Capabilities)
		}
	}
	if !reflect.DeepEqual(options, before) {
		t.Errorf("the SDK changed the options the author returned:\n%+v\nwas\n%+v", options, before)
	}

	// Each call tells the author what it did wrong, once for each field, and says nothing of the choices that are fine.
	var told []string
	for _, record := range sink.Records() {
		fields := record.ToMap()
		if fields["option"] != "mode" {
			continue
		}
		if fields["choice"] != "wide" {
			t.Errorf("the log blames choice %v: %v", fields["choice"], record.Message)
		}
		if record.Level != logging.LevelWarn {
			t.Errorf("the author's bug is logged at %v, want a warning", record.Level)
		}
		told = append(told, fields["violation"].(string))
	}
	want := []string{"Signals: reported HUP", "HostRestart: reported TARGET_SESSION above the declared NONE", "LinkHealth: reported true above the declared false"}
	if len(told) != 2*len(want) {
		t.Fatalf("the log has %d records about the options, want %d (two calls, each field once): %q", len(told), 2*len(want), told)
	}
	for i, phrase := range want {
		for _, at := range []int{i, len(want) + i} {
			if !strings.Contains(told[at], phrase) {
				t.Errorf("record %d says %q, want it to say %q", at, told[at], phrase)
			}
		}
	}
}

// The wire carries the mode of a choice as a complete Capabilities message, and no message for a choice that does not
// narrow.
func TestTheWireCarriesTheCapabilitiesOfAChoice(t *testing.T) {
	kind := exectest.NewFakeTarget().Info.Capabilities
	f := newFixture(t, exectest.FakeStaticOptions(exec.Option{Name: "mode", Choices: []exec.Choice{
		{Value: "attach", Capabilities: narrowedFrom(kind, func(c *exec.Capabilities) { c.ExitReport = exec.ExitReportNone })},
		{Value: "plain"},
	}}))
	response, err := f.server.Raw().Options(f.rawCtx(t), mustOptionsRequest(t, f, "pod-1"))
	if err != nil {
		t.Fatal(err)
	}
	choices := response.GetOptions()[0].GetChoices()
	caps := choices[0].GetCapabilities()
	if caps == nil || caps.Tty == nil || caps.Resize == nil || caps.LinkHealth == nil || caps.Signals == nil || caps.Integration == nil {
		t.Fatalf("the choice with capabilities is served as %v, want every field present", caps)
	}
	if caps.GetExitReport().String() != "EXIT_REPORT_NONE" || caps.GetKill().String() != "KILL_BEHAVIOR_TERMINATES" || len(caps.GetSignals().GetSignals()) != 3 {
		t.Errorf("the choice is served as %v", caps)
	}
	if choices[1].GetCapabilities() != nil {
		t.Errorf("a choice that does not narrow is served with %v", choices[1].GetCapabilities())
	}
}
