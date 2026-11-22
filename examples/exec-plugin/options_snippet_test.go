package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/omniviewdev/plugin-sdk/internal/exectestkit"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
)

// This file is the Options snippet of docs/exec.md, kept compiling and tested. A host reaches an Options callback only
// through a resource provider registered in the same plugin that owns connection lifecycle (resourcev2.Lifecycle,
// which makes the provider a resourcev2.ConnectionContextProvider); the example's main.go has none, so it carries no
// callback. The test below serves this one behind a fake connection registry, as exectest.Conformance does.

// pod and container stand in for what your client returns, and getPod for the call that fetches it with the client
// of the connection ctx carries.
type pod struct {
	defaultContainer string
	containers       []container
}

type container struct{ name, kind, phase string }

func getPod(_ *types.PluginContext, id string) (pod, error) {
	if id == "" {
		return pod{}, errors.New("no pod")
	}
	return pod{defaultContainer: "app", containers: []container{
		{"app", "container", "running"},
		{"sidecar", "container", "starting"},
		{"init-db", "init", "terminated"},
		{"crashy", "container", "crash-loop"},
	}}, nil
}

// guide:begin podOptions
// podOptions is the target's Options callback: the choices a picker shows for one Pod. ctx carries the connection the
// host named; use it to get your client.
func podOptions(ctx *types.PluginContext, ref resourcev2.ResourceReference) ([]exec.Option, error) {
	info, err := getPod(ctx, ref.Address.ID)
	if err != nil {
		return nil, err
	}
	containers := exec.Option{Name: "container", Required: true}
	for _, c := range info.containers {
		choice := exec.Choice{Value: c.name, Kind: c.kind, IsDefault: c.name == info.defaultContainer}
		switch c.phase {
		case "running":
			choice.State = exec.ChoiceStateReady
		case "starting":
			choice.State = exec.ChoiceStateStarting // will probably be ready soon, and is not now
		default: // "terminated", "crash-loop": cannot run a command now; Detail is a closed code, never prose
			choice.State, choice.Detail = exec.ChoiceStateStopped, c.phase
		}
		containers.Choices = append(containers.Choices, choice)
	}
	// The mode of a session is declared on its choice, before anything opens: the complete set a session that selects it
	// can do, which is the kind's upper bound (podCapabilities) narrowed. The SDK opens the session with it, so the
	// host reads it in Opened, and podExec starts from it and reports nothing.
	execMode := podCapabilities()
	execMode.HostRestart = exec.SurvivalNone // an exec'd process cannot be reached again after a host restart
	attachMode := podCapabilities()
	attachMode.ExitReport = exec.ExitReportNone // attach relays no status at all

	mode := exec.Option{Name: "mode", Choices: []exec.Choice{ // the "mode" podExec reads from the selection
		{Value: "exec", State: exec.ChoiceStateReady, IsDefault: true, Capabilities: &execMode},
		{Value: "attach", State: exec.ChoiceStateReady, Capabilities: &attachMode},
	}}
	return []exec.Option{containers, mode}, nil
}

// guide:end podOptions

// The callback answers the picker as the guide says, and the SDK serves it behind its admission checks.
func TestPodOptionsConform(t *testing.T) {
	options, err := podOptions(nil, resourcev2.ResourceReference{Address: resourcev2.ResourceAddress{ID: "web-0"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 2 || options[0].Name != "container" || options[1].Name != "mode" {
		t.Fatalf("options = %+v, want container and mode", options)
	}
	states := map[string]exec.ChoiceState{}
	for _, choice := range options[0].Choices {
		states[choice.Value] = choice.State
	}
	want := map[string]exec.ChoiceState{
		"app": exec.ChoiceStateReady, "sidecar": exec.ChoiceStateStarting,
		"init-db": exec.ChoiceStateStopped, "crashy": exec.ChoiceStateStopped,
	}
	for name, state := range want {
		if states[name] != state {
			t.Errorf("container %s is %v, want %v", name, states[name], state)
		}
	}

	target := podTarget()
	target.Options = podOptions
	exectest.Conformance(t, target, exectest.ConformanceResource("web-0"))
}

// The SDK serves the mode of each choice as the guide's snippet declares it: a host that asks Options reads, before it
// opens anything, that an attach has no exit status and survives a host restart and that an exec has a status and does not.
// What it reads is what the callback declared: the declarations are within the kind's, so the SDK clamps nothing.
func TestTheSDKServesTheModesOfThePodWithTheirChoices(t *testing.T) {
	target := podTarget()
	target.Options = podOptions
	connections := exectestkit.NewConnectionsT(t, target.Info.Provider)
	server := exectestkit.Serve(t, exectestkit.ServeConfig{Targets: []exec.Target{target}, Connections: connections})
	result, err := server.V2(t).Options(connections.Context(context.Background()), connections.TargetRef(target.Info.Type, "web-0"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Options) != 2 || result.Options[0].Name != "container" || result.Options[1].Name != "mode" {
		t.Fatalf("options = %+v, want container and mode", result.Options)
	}
	for _, choice := range result.Options[0].Choices {
		if choice.Capabilities != nil {
			t.Errorf("container %q is served with capabilities %+v: a container does not narrow", choice.Value, choice.Capabilities)
		}
	}
	wantExec, wantAttach := podCapabilities(), podCapabilities()
	wantExec.HostRestart = exec.SurvivalNone
	wantAttach.ExitReport = exec.ExitReportNone
	modes := result.Options[1].Choices
	if len(modes) != 2 || modes[0].Value != "exec" || !modes[0].IsDefault || modes[1].Value != "attach" {
		t.Fatalf("modes = %+v, want exec (the default) and attach", modes)
	}
	if got := modes[0].Capabilities; got == nil || !reflect.DeepEqual(*got, wantExec) {
		t.Errorf("exec is served with %+v, want the kind's with HostRestart none: %+v", got, wantExec)
	}
	if got := modes[1].Capabilities; got == nil || !reflect.DeepEqual(*got, wantAttach) {
		t.Errorf("attach is served with %+v, want the kind's with ExitReport none: %+v", got, wantAttach)
	}
}

// The guide prints the snippet as it is in this file.
func TestTheGuideShowsTheOptionsSnippet(t *testing.T) {
	assertGuideShows(t, "options_snippet_test.go", "podOptions")
}
